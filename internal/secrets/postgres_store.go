// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresEnvelopeStore is the durable encrypted store for service variables.
// Only envelope ciphertext and redaction-safe metadata are written to
// service_variables.
type PostgresEnvelopeStore struct {
	Pool *pg.Pool
}

// NewPostgresEnvelopeStore constructs a durable variable store.
func NewPostgresEnvelopeStore(pool *pg.Pool) *PostgresEnvelopeStore {
	return &PostgresEnvelopeStore{Pool: pool}
}

func (s *PostgresEnvelopeStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres variable store: pool is required")
	}
	return nil
}

// ListMetadataInTx returns active redaction-safe metadata in one exact
// ownership scope through a caller-owned transaction. It never selects
// ciphertext or wrapped key material and never starts or completes a
// transaction.
func (s *PostgresEnvelopeStore) ListMetadataInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
) ([]VariableMeta, error) {
	if err := s.requireCallerTx(tx, "ListMetadataInTx"); err != nil {
		return nil, err
	}
	if err := validateVariableOwnershipScope(scope); err != nil {
		return nil, err
	}
	if err := validateVariableOwnershipScopeInTx(ctx, tx, scope, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, variableMetadataSelect+`
		WHERE workspace_id = $1
		  AND project_id = $2
		  AND environment_id = $3
		  AND service_id = $4
		  AND deleted_at IS NULL
		ORDER BY name ASC, id ASC`,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres variable store: list exact metadata: %w", err)
	}
	defer rows.Close()
	out := make([]VariableMeta, 0)
	for rows.Next() {
		meta, scanErr := scanVariableMeta(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, meta)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres variable store: list exact metadata rows: %w", err)
	}
	return out, nil
}

// GetActiveInTx returns one active encrypted row only when both its opaque ID
// and complete canonical ownership scope match. The caller owns transaction
// isolation and commit/rollback.
func (s *PostgresEnvelopeStore) GetActiveInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	id VariableID,
) (PersistedVariable, error) {
	if err := s.requireCallerTx(tx, "GetActiveInTx"); err != nil {
		return PersistedVariable{}, err
	}
	if err := validateVariableOwnershipScope(scope); err != nil {
		return PersistedVariable{}, err
	}
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(id)) != string(id) {
		return PersistedVariable{}, ErrVariableScopeIntegrity
	}
	if err := validateVariableOwnershipScopeInTx(ctx, tx, scope, false); err != nil {
		return PersistedVariable{}, err
	}
	return scanPersistedVariable(tx.QueryRow(ctx, variableSelect+`
		WHERE id = $1
		  AND workspace_id = $2
		  AND project_id = $3
		  AND environment_id = $4
		  AND service_id = $5
		  AND deleted_at IS NULL`,
		id,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
	))
}

// InsertPreparedInTx atomically inserts one already-encrypted active envelope
// and its immutable version-one row through a caller-owned transaction. The
// explicit scope must be server-derived and must exactly match the prepared
// metadata. A duplicate ID or active ownership/name returns a metadata-only
// version conflict without aborting the transaction.
func (s *PostgresEnvelopeStore) InsertPreparedInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	variable PersistedVariable,
) error {
	if err := s.requireCallerTx(tx, "InsertPreparedInTx"); err != nil {
		return err
	}
	if err := validatePreparedVariable(scope, variable, 1); err != nil {
		return err
	}
	if err := validateVariableOwnershipScopeInTx(ctx, tx, scope, true); err != nil {
		return err
	}
	references, err := encodeVariableReferences(variable.Meta.References)
	if err != nil {
		return err
	}
	meta := variable.Meta
	tag, err := tx.Exec(ctx, `
		INSERT INTO service_variables (
			id, workspace_id, project_id, environment_id, service_id, name, scope,
			value_kind, value_ciphertext, kek_id, wrapped_dek,
			canonical_form, "references", length_bucket, kek_fingerprint, version,
			last_rotated_at, created_at, updated_at, deleted_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,1,$16,
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,NULL
		)
		ON CONFLICT DO NOTHING`,
		meta.ID,
		meta.Workspace,
		meta.Project,
		meta.Environment,
		meta.Service,
		meta.Name,
		meta.Scope,
		meta.ValueKind,
		variable.Envelope.Ciphertext,
		variable.Envelope.KEKID,
		variable.Envelope.WrappedDEK,
		variable.Envelope.CanonicalForm,
		references,
		meta.LengthBucket,
		meta.KEKFingerprint,
		meta.LastRotatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: insert prepared envelope: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return &VariableVersionConflict{
			Expected: 0,
			Actual: activeVersionForNameInTx(
				ctx,
				tx,
				scope,
				meta.Name,
			),
		}
	}
	if err := insertPreparedVariableVersionInTx(ctx, tx, variable, references); err != nil {
		return err
	}
	return nil
}

// UpdatePreparedCASInTx replaces the encrypted value and mutable privacy
// metadata of one exact active row, then inserts the new immutable encrypted
// version in the same caller-owned transaction. Identity, ownership, and name
// are immutable.
func (s *PostgresEnvelopeStore) UpdatePreparedCASInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	variable PersistedVariable,
	expectedVersion int,
) error {
	if err := s.requireCallerTx(tx, "UpdatePreparedCASInTx"); err != nil {
		return err
	}
	if expectedVersion < 1 {
		return &VariableVersionConflict{Expected: expectedVersion}
	}
	if err := validatePreparedVariable(scope, variable, expectedVersion+1); err != nil {
		return err
	}
	if err := validateVariableOwnershipScopeInTx(ctx, tx, scope, true); err != nil {
		return err
	}
	references, err := encodeVariableReferences(variable.Meta.References)
	if err != nil {
		return err
	}
	meta := variable.Meta
	tag, err := tx.Exec(ctx, `
		UPDATE service_variables
		   SET scope = $7,
		       value_kind = $8,
		       value_ciphertext = $9,
		       kek_id = $10,
		       wrapped_dek = $11,
		       canonical_form = $12,
		       "references" = $13,
		       length_bucket = $14,
		       kek_fingerprint = $15,
		       version = $16,
		       last_rotated_at = $17,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		   AND workspace_id = $2
		   AND project_id = $3
		   AND environment_id = $4
		   AND service_id = $5
		   AND name = $6
		   AND version = $18
		   AND deleted_at IS NULL`,
		meta.ID,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
		meta.Name,
		meta.Scope,
		meta.ValueKind,
		variable.Envelope.Ciphertext,
		variable.Envelope.KEKID,
		variable.Envelope.WrappedDEK,
		variable.Envelope.CanonicalForm,
		references,
		meta.LengthBucket,
		meta.KEKFingerprint,
		meta.Version,
		meta.LastRotatedAt,
		expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: update prepared envelope: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return &VariableVersionConflict{
			Expected: expectedVersion,
			Actual:   activeVersionForIDInTx(ctx, tx, scope, meta.ID),
		}
	}
	if err := insertPreparedVariableVersionInTx(ctx, tx, variable, references); err != nil {
		return err
	}
	return nil
}

// TombstoneCASInTx erases active envelope material for one exact ownership
// scope and version while retaining immutable historical encrypted versions.
// It never starts or completes the caller-owned transaction.
func (s *PostgresEnvelopeStore) TombstoneCASInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	id VariableID,
	expectedVersion int,
) error {
	if err := s.requireCallerTx(tx, "TombstoneCASInTx"); err != nil {
		return err
	}
	if err := validateVariableOwnershipScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" || strings.TrimSpace(string(id)) != string(id) {
		return ErrVariableScopeIntegrity
	}
	if expectedVersion < 1 {
		return &VariableVersionConflict{Expected: expectedVersion}
	}
	if err := validateVariableOwnershipScopeInTx(ctx, tx, scope, true); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE service_variables
		   SET value_ciphertext = ''::bytea,
		       kek_id = '',
		       wrapped_dek = ''::bytea,
		       "references" = '[]'::jsonb,
		       length_bucket = 'deleted',
		       kek_fingerprint = '',
		       version = version + 1,
		       deleted_at = CURRENT_TIMESTAMP,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1
		   AND workspace_id = $2
		   AND project_id = $3
		   AND environment_id = $4
		   AND service_id = $5
		   AND version = $6
		   AND deleted_at IS NULL`,
		id,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
		expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: tombstone exact envelope: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return &VariableVersionConflict{
			Expected: expectedVersion,
			Actual:   activeVersionForIDInTx(ctx, tx, scope, id),
		}
	}
	return nil
}

func (s *PostgresEnvelopeStore) requireCallerTx(tx *pg.Tx, method string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("postgres variable store %s: transaction is required", method)
	}
	return nil
}

// Put inserts or rotates an encrypted variable. Scope checks are explicit in
// addition to the database foreign keys so callers receive a stable typed
// error without leaking query arguments.
func (s *PostgresEnvelopeStore) Put(ctx context.Context, variable PersistedVariable) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres variable store: pool is required")
	}
	meta := variable.Meta
	if err := s.validateScope(ctx, meta); err != nil {
		return err
	}
	references, err := json.Marshal(meta.References)
	if err != nil {
		return fmt.Errorf("postgres variable store: encode reference metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx,
		`INSERT INTO service_variables (
			id, workspace_id, project_id, environment_id, service_id, name, scope,
			value_kind, value_ciphertext, kek_id, wrapped_dek,
			canonical_form, "references", length_bucket, kek_fingerprint, version,
			last_rotated_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)
		ON CONFLICT (project_id, environment_id, service_id, name)
		WHERE deleted_at IS NULL
		DO UPDATE SET
			id = EXCLUDED.id,
			workspace_id = EXCLUDED.workspace_id,
			scope = EXCLUDED.scope,
			value_kind = EXCLUDED.value_kind,
			value_ciphertext = EXCLUDED.value_ciphertext,
			kek_id = EXCLUDED.kek_id,
			wrapped_dek = EXCLUDED.wrapped_dek,
			canonical_form = EXCLUDED.canonical_form,
			"references" = EXCLUDED."references",
			length_bucket = EXCLUDED.length_bucket,
			kek_fingerprint = EXCLUDED.kek_fingerprint,
			version = EXCLUDED.version,
			last_rotated_at = EXCLUDED.last_rotated_at,
			updated_at = CURRENT_TIMESTAMP`,
		meta.ID, meta.Workspace, meta.Project, meta.Environment, meta.Service, meta.Name, meta.Scope,
		normalizeValueKind(meta.ValueKind), variable.Envelope.Ciphertext, variable.Envelope.KEKID,
		variable.Envelope.WrappedDEK, variable.Envelope.CanonicalForm, references,
		meta.LengthBucket, meta.KEKFingerprint, meta.Version, meta.LastRotatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: persist envelope: %w", err)
	}
	return nil
}

// PutCAS creates when expectedVersion is zero and updates only an active row
// at exactly expectedVersion otherwise. The compare and ciphertext write are
// atomic, preventing read-then-write lost updates.
func (s *PostgresEnvelopeStore) PutCAS(ctx context.Context, variable PersistedVariable, expectedVersion int) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres variable store: pool is required")
	}
	if err := s.validateScope(ctx, variable.Meta); err != nil {
		return err
	}
	references, err := json.Marshal(variable.Meta.References)
	if err != nil {
		return fmt.Errorf("postgres variable store: encode reference metadata: %w", err)
	}
	meta := variable.Meta
	if expectedVersion == 0 {
		err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
			if _, err := tx.Exec(ctx,
				`INSERT INTO service_variables (
					id, workspace_id, project_id, environment_id, service_id, name, scope,
					value_kind, value_ciphertext, kek_id, wrapped_dek,
					canonical_form, "references", length_bucket, kek_fingerprint, version,
					last_rotated_at, created_at, updated_at, deleted_at
				) VALUES (
					$1, $2, $3, $4, $5, $6, $7,
					$8, $9, $10, $11,
					$12, $13, $14, $15, 1,
					$16, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, NULL
				)`,
				meta.ID, meta.Workspace, meta.Project, meta.Environment, meta.Service, meta.Name, meta.Scope,
				normalizeValueKind(meta.ValueKind), variable.Envelope.Ciphertext, variable.Envelope.KEKID,
				variable.Envelope.WrappedDEK, variable.Envelope.CanonicalForm, references,
				meta.LengthBucket, meta.KEKFingerprint, meta.LastRotatedAt,
			); err != nil {
				return err
			}
			return persistVariableVersion(ctx, tx, variable, references)
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			actual := s.activeVersion(ctx, "", meta.Project, meta.Environment, meta.Service, meta.Name)
			return &VariableVersionConflict{Expected: 0, Actual: actual}
		}
		if err != nil {
			return fmt.Errorf("postgres variable store: create envelope: %w", err)
		}
		return nil
	}
	if expectedVersion < 1 || (meta.Version != expectedVersion && meta.Version != expectedVersion+1) {
		return &VariableVersionConflict{Expected: expectedVersion}
	}
	var rowsAffected int64
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE service_variables
			 SET workspace_id = $2,
			     scope = $3,
			     value_kind = $4,
			     value_ciphertext = $5,
			     kek_id = $6,
			     wrapped_dek = $7,
			     canonical_form = $8,
			     "references" = $9,
			     length_bucket = $10,
			     kek_fingerprint = $11,
			     version = $12,
			     last_rotated_at = $13,
			     updated_at = CURRENT_TIMESTAMP
			 WHERE id = $1
			   AND project_id = $14
			   AND environment_id = $15
			   AND service_id = $16
			   AND name = $17
			   AND version = $18
			   AND deleted_at IS NULL`,
			meta.ID, meta.Workspace, meta.Scope, normalizeValueKind(meta.ValueKind),
			variable.Envelope.Ciphertext, variable.Envelope.KEKID, variable.Envelope.WrappedDEK,
			variable.Envelope.CanonicalForm, references, meta.LengthBucket, meta.KEKFingerprint,
			meta.Version, meta.LastRotatedAt, meta.Project, meta.Environment, meta.Service, meta.Name,
			expectedVersion,
		)
		if err != nil {
			return err
		}
		rowsAffected = tag.RowsAffected()
		if rowsAffected != 1 {
			return nil
		}
		return persistVariableVersion(ctx, tx, variable, references)
	})
	if err != nil {
		return fmt.Errorf("postgres variable store: update envelope: %w", err)
	}
	if rowsAffected != 1 {
		return &VariableVersionConflict{
			Expected: expectedVersion,
			Actual:   s.activeVersion(ctx, meta.ID, meta.Project, meta.Environment, meta.Service, meta.Name),
		}
	}
	return nil
}

// DeleteCAS erases encrypted payload material from the active row and leaves a
// metadata-only tombstone. Immutable historical envelopes remain available to
// exact-version deployment retries; active queries exclude them immediately.
func (s *PostgresEnvelopeStore) DeleteCAS(ctx context.Context, id VariableID, expectedVersion int) error {
	if s == nil || s.Pool == nil {
		return errors.New("postgres variable store: pool is required")
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE service_variables
		 SET value_ciphertext = ''::bytea,
		     kek_id = '',
		     wrapped_dek = ''::bytea,
		     "references" = '[]'::jsonb,
		     length_bucket = 'deleted',
		     kek_fingerprint = '',
		     version = version + 1,
		     deleted_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND version = $2 AND deleted_at IS NULL`,
		id, expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: tombstone envelope: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return &VariableVersionConflict{
			Expected: expectedVersion,
			Actual:   s.activeVersion(ctx, id, "", "", "", ""),
		}
	}
	return nil
}

// Get returns one encrypted record by id.
func (s *PostgresEnvelopeStore) Get(ctx context.Context, id VariableID) (PersistedVariable, error) {
	if s == nil || s.Pool == nil {
		return PersistedVariable{}, errors.New("postgres variable store: pool is required")
	}
	row := s.Pool.QueryRow(ctx, variableSelect+` WHERE id = $1 AND deleted_at IS NULL`, id)
	return scanPersistedVariable(row)
}

// GetVersion returns an immutable historical encrypted value, including after
// the variable has been rotated or tombstoned.
func (s *PostgresEnvelopeStore) GetVersion(ctx context.Context, id VariableID, version int) (PersistedVariable, error) {
	if s == nil || s.Pool == nil {
		return PersistedVariable{}, errors.New("postgres variable store: pool is required")
	}
	row := s.Pool.QueryRow(ctx, variableVersionSelect+`
		WHERE variable_id = $1 AND version = $2`,
		id, version,
	)
	return scanPersistedVariable(row)
}

// Find returns one encrypted record by its canonical runtime scope.
func (s *PostgresEnvelopeStore) Find(ctx context.Context, projectID, environmentID, serviceID, name string) (PersistedVariable, error) {
	if s == nil || s.Pool == nil {
		return PersistedVariable{}, errors.New("postgres variable store: pool is required")
	}
	row := s.Pool.QueryRow(ctx, variableSelect+`
		WHERE project_id = $1 AND environment_id = $2
		  AND service_id = $3 AND name = $4 AND deleted_at IS NULL`,
		projectID, environmentID, serviceID, name,
	)
	return scanPersistedVariable(row)
}

// List returns all encrypted records. Callers surface only VariableMeta.
func (s *PostgresEnvelopeStore) List(ctx context.Context) ([]PersistedVariable, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("postgres variable store: pool is required")
	}
	rows, err := s.Pool.Query(ctx, variableSelect+` WHERE deleted_at IS NULL ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("postgres variable store: list envelopes: %w", err)
	}
	defer rows.Close()
	out := make([]PersistedVariable, 0)
	for rows.Next() {
		variable, err := scanPersistedVariable(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, variable)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres variable store: list envelopes: %w", err)
	}
	return out, nil
}

const variableSelect = `SELECT
	id, workspace_id, project_id, environment_id, service_id, name, scope,
	value_kind, value_ciphertext, kek_id, wrapped_dek, canonical_form,
	"references", length_bucket, kek_fingerprint, version, last_rotated_at
FROM service_variables`

const variableVersionSelect = `SELECT
	variable_id, workspace_id, project_id, environment_id, service_id, name, scope,
	value_kind, value_ciphertext, kek_id, wrapped_dek, canonical_form,
	"references", length_bucket, kek_fingerprint, version, last_rotated_at
FROM service_variable_versions`

const variableMetadataSelect = `SELECT
	id, workspace_id, project_id, environment_id, service_id, name, scope,
	value_kind, "references", length_bucket, kek_fingerprint, version,
	last_rotated_at
FROM service_variables`

func persistVariableVersion(ctx context.Context, conn pg.Conn, variable PersistedVariable, references []byte) error {
	meta := variable.Meta
	_, err := conn.Exec(ctx,
		`INSERT INTO service_variable_versions (
			variable_id, version, workspace_id, project_id, environment_id,
			service_id, name, scope, value_kind, value_ciphertext, kek_id,
			wrapped_dek, canonical_form, "references", length_bucket,
			kek_fingerprint, last_rotated_at, created_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			CURRENT_TIMESTAMP
		)
		ON CONFLICT (variable_id, version) DO UPDATE SET
			wrapped_dek = EXCLUDED.wrapped_dek,
			kek_id = EXCLUDED.kek_id,
			kek_fingerprint = EXCLUDED.kek_fingerprint`,
		meta.ID, meta.Version, meta.Workspace, meta.Project, meta.Environment,
		meta.Service, meta.Name, meta.Scope, normalizeValueKind(meta.ValueKind),
		variable.Envelope.Ciphertext, variable.Envelope.KEKID,
		variable.Envelope.WrappedDEK, variable.Envelope.CanonicalForm, references,
		meta.LengthBucket, meta.KEKFingerprint, meta.LastRotatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: persist versioned envelope: %w", err)
	}
	return nil
}

func insertPreparedVariableVersionInTx(
	ctx context.Context,
	tx *pg.Tx,
	variable PersistedVariable,
	references []byte,
) error {
	meta := variable.Meta
	_, err := tx.Exec(ctx, `
		INSERT INTO service_variable_versions (
			variable_id, version, workspace_id, project_id, environment_id,
			service_id, name, scope, value_kind, value_ciphertext, kek_id,
			wrapped_dek, canonical_form, "references", length_bucket,
			kek_fingerprint, last_rotated_at, created_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			CURRENT_TIMESTAMP
		)`,
		meta.ID,
		meta.Version,
		meta.Workspace,
		meta.Project,
		meta.Environment,
		meta.Service,
		meta.Name,
		meta.Scope,
		meta.ValueKind,
		variable.Envelope.Ciphertext,
		variable.Envelope.KEKID,
		variable.Envelope.WrappedDEK,
		variable.Envelope.CanonicalForm,
		references,
		meta.LengthBucket,
		meta.KEKFingerprint,
		meta.LastRotatedAt,
	)
	if err != nil {
		return fmt.Errorf("postgres variable store: insert prepared versioned envelope: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPersistedVariable(row rowScanner) (PersistedVariable, error) {
	var out PersistedVariable
	var references []byte
	if err := row.Scan(
		&out.Meta.ID,
		&out.Meta.Workspace,
		&out.Meta.Project,
		&out.Meta.Environment,
		&out.Meta.Service,
		&out.Meta.Name,
		&out.Meta.Scope,
		&out.Meta.ValueKind,
		&out.Envelope.Ciphertext,
		&out.Envelope.KEKID,
		&out.Envelope.WrappedDEK,
		&out.Envelope.CanonicalForm,
		&references,
		&out.Meta.LengthBucket,
		&out.Meta.KEKFingerprint,
		&out.Meta.Version,
		&out.Meta.LastRotatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PersistedVariable{}, ErrVariableNotFound
		}
		return PersistedVariable{}, fmt.Errorf("postgres variable store: scan envelope: %w", err)
	}
	if len(references) > 0 {
		if err := json.Unmarshal(references, &out.Meta.References); err != nil {
			return PersistedVariable{}, fmt.Errorf("postgres variable store: decode reference metadata: %w", err)
		}
	}
	out.Meta.LastRotatedAt = out.Meta.LastRotatedAt.UTC()
	return out, nil
}

func scanVariableMeta(row rowScanner) (VariableMeta, error) {
	var out VariableMeta
	var references []byte
	if err := row.Scan(
		&out.ID,
		&out.Workspace,
		&out.Project,
		&out.Environment,
		&out.Service,
		&out.Name,
		&out.Scope,
		&out.ValueKind,
		&references,
		&out.LengthBucket,
		&out.KEKFingerprint,
		&out.Version,
		&out.LastRotatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VariableMeta{}, ErrVariableNotFound
		}
		return VariableMeta{}, fmt.Errorf("postgres variable store: scan variable metadata: %w", err)
	}
	if len(references) > 0 {
		if err := json.Unmarshal(references, &out.References); err != nil {
			return VariableMeta{}, fmt.Errorf("postgres variable store: decode variable reference metadata: %w", err)
		}
	}
	out.LastRotatedAt = out.LastRotatedAt.UTC()
	return out, nil
}

func normalizeValueKind(kind string) string {
	if kind == "reference" {
		return kind
	}
	return "literal"
}

func validateVariableOwnershipScope(scope VariableOwnershipScope) error {
	if strings.TrimSpace(scope.Workspace) == "" ||
		strings.TrimSpace(scope.Project) == "" ||
		strings.TrimSpace(scope.Environment) == "" ||
		strings.TrimSpace(scope.Workspace) != scope.Workspace ||
		strings.TrimSpace(scope.Project) != scope.Project ||
		strings.TrimSpace(scope.Environment) != scope.Environment ||
		strings.TrimSpace(scope.Service) != scope.Service {
		return ErrVariableScopeIntegrity
	}
	return nil
}

func validatePreparedVariable(
	scope VariableOwnershipScope,
	variable PersistedVariable,
	requiredVersion int,
) error {
	if err := validateVariableOwnershipScope(scope); err != nil {
		return err
	}
	meta := variable.Meta
	if meta.Workspace != scope.Workspace ||
		meta.Project != scope.Project ||
		meta.Environment != scope.Environment ||
		meta.Service != scope.Service {
		return ErrVariableScopeIntegrity
	}
	if strings.TrimSpace(string(meta.ID)) == "" ||
		strings.TrimSpace(string(meta.ID)) != string(meta.ID) ||
		!ValidVariableName(meta.Name) {
		return ErrInvalidVariableName
	}
	switch meta.Scope {
	case ScopePlain, ScopeSecret, ScopeSealed:
	default:
		return ErrInvalidScope
	}
	if meta.ValueKind != "literal" && meta.ValueKind != "reference" {
		return errors.New("postgres variable store: prepared value kind is invalid")
	}
	if meta.Version != requiredVersion {
		return &VariableVersionConflict{
			Expected: requiredVersion - 1,
			Actual:   meta.Version,
		}
	}
	if meta.LastRotatedAt.IsZero() ||
		meta.LengthBucket == "" ||
		meta.KEKFingerprint == "" ||
		variable.Envelope.KEKID == "" ||
		len(variable.Envelope.WrappedDEK) == 0 ||
		len(variable.Envelope.Ciphertext) == 0 ||
		variable.Envelope.CanonicalForm != envelopeCanonicalV1 {
		return errors.New("postgres variable store: prepared encrypted envelope is incomplete")
	}
	return nil
}

func encodeVariableReferences(references []VariableReference) ([]byte, error) {
	encoded, err := json.Marshal(references)
	if err != nil {
		return nil, fmt.Errorf("postgres variable store: encode reference metadata: %w", err)
	}
	return encoded, nil
}

func validateVariableOwnershipScopeInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	lock bool,
) error {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF workspace, project, environment"
		if scope.Service != "" {
			lockClause += ", service"
		}
	}
	var one int
	var err error
	if scope.Service == "" {
		err = tx.QueryRow(ctx, `
			SELECT 1
			  FROM workspaces workspace
			  JOIN projects project
			    ON project.workspace_id = workspace.id
			  JOIN environments environment
			    ON environment.project_id = project.id
			 WHERE workspace.id = $1
			   AND project.id = $2
			   AND environment.id = $3
			   AND workspace.deleted_at IS NULL
			   AND project.deleted_at IS NULL
			   AND environment.deleted_at IS NULL`+lockClause,
			scope.Workspace,
			scope.Project,
			scope.Environment,
		).Scan(&one)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT 1
			  FROM workspaces workspace
			  JOIN projects project
			    ON project.workspace_id = workspace.id
			  JOIN environments environment
			    ON environment.project_id = project.id
			  JOIN services service
			    ON service.project_id = project.id
			 WHERE workspace.id = $1
			   AND project.id = $2
			   AND environment.id = $3
			   AND service.id = $4
			   AND workspace.deleted_at IS NULL
			   AND project.deleted_at IS NULL
			   AND environment.deleted_at IS NULL
			   AND service.deleted_at IS NULL`+lockClause,
			scope.Workspace,
			scope.Project,
			scope.Environment,
			scope.Service,
		).Scan(&one)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVariableScopeIntegrity
	}
	if err != nil {
		return fmt.Errorf("postgres variable store: validate exact ownership scope: %w", err)
	}
	return nil
}

func activeVersionForNameInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	name string,
) int {
	var actual int
	if err := tx.QueryRow(ctx, `
		SELECT version
		  FROM service_variables
		 WHERE workspace_id = $1
		   AND project_id = $2
		   AND environment_id = $3
		   AND service_id = $4
		   AND name = $5
		   AND deleted_at IS NULL`,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
		name,
	).Scan(&actual); err != nil {
		return 0
	}
	return actual
}

func activeVersionForIDInTx(
	ctx context.Context,
	tx *pg.Tx,
	scope VariableOwnershipScope,
	id VariableID,
) int {
	var actual int
	if err := tx.QueryRow(ctx, `
		SELECT version
		  FROM service_variables
		 WHERE id = $1
		   AND workspace_id = $2
		   AND project_id = $3
		   AND environment_id = $4
		   AND service_id = $5
		   AND deleted_at IS NULL`,
		id,
		scope.Workspace,
		scope.Project,
		scope.Environment,
		scope.Service,
	).Scan(&actual); err != nil {
		return 0
	}
	return actual
}

func (s *PostgresEnvelopeStore) validateScope(ctx context.Context, meta VariableMeta) error {
	if strings.TrimSpace(meta.Project) == "" || strings.TrimSpace(meta.Environment) == "" {
		return ErrVariableScopeIntegrity
	}
	var environmentValid bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM environments
			WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL
		)`,
		meta.Environment, meta.Project,
	).Scan(&environmentValid); err != nil {
		return fmt.Errorf("postgres variable store: validate environment scope: %w", err)
	}
	if !environmentValid {
		return ErrVariableScopeIntegrity
	}
	if strings.TrimSpace(meta.Service) == "" {
		return nil
	}
	var serviceValid bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM services
			WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL
		)`,
		meta.Service, meta.Project,
	).Scan(&serviceValid); err != nil {
		return fmt.Errorf("postgres variable store: validate service scope: %w", err)
	}
	if !serviceValid {
		return ErrVariableScopeIntegrity
	}
	return nil
}

func (s *PostgresEnvelopeStore) activeVersion(ctx context.Context, id VariableID, projectID, environmentID, serviceID, name string) int {
	var actual int
	var err error
	if id != "" {
		err = s.Pool.QueryRow(ctx,
			`SELECT version FROM service_variables WHERE id = $1 AND deleted_at IS NULL`,
			id,
		).Scan(&actual)
	} else {
		err = s.Pool.QueryRow(ctx,
			`SELECT version FROM service_variables
			 WHERE project_id = $1 AND environment_id = $2 AND service_id = $3
			   AND name = $4 AND deleted_at IS NULL`,
			projectID, environmentID, serviceID, name,
		).Scan(&actual)
	}
	if err != nil {
		return 0
	}
	return actual
}

// LookupServiceID resolves a human-facing service name inside a proven
// project/environment scope. It is used only for `${{Service.VAR}}`
// resolution at deploy time.
func (s *PostgresEnvelopeStore) LookupServiceID(ctx context.Context, projectID, environmentID, name string) (string, bool, error) {
	if s == nil || s.Pool == nil {
		return "", false, errors.New("postgres variable store: pool is required")
	}
	var id string
	err := s.Pool.QueryRow(ctx,
		`SELECT s.id
		 FROM services s
		 JOIN environments e ON e.project_id = s.project_id
		 WHERE s.project_id = $1
		   AND e.id = $2
		   AND s.deleted_at IS NULL
		   AND e.deleted_at IS NULL
		   AND (s.slug = $3 OR s.display_name = $3)
		 ORDER BY CASE WHEN s.slug = $3 THEN 0 ELSE 1 END, s.id
		 LIMIT 1`,
		projectID, environmentID, name,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("postgres variable store: resolve service name: %w", err)
	}
	return id, true, nil
}
