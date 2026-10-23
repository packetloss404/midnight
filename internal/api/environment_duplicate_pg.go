// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	environmentDuplicatePathTemplate = "/api/v1alpha/environments/{id}/duplicate"
	environmentDuplicateTTL          = 24 * time.Hour
)

// PostgresEnvironmentDuplicator performs a config-bearing environment copy.
// The target environment, service configs, re-encrypted variables + immutable
// versions, and terminal idempotency response commit atomically.
type PostgresEnvironmentDuplicator struct {
	Pool        *pg.Pool
	KeyProvider secrets.KeyProvider
	Now         func() time.Time
	IDFactory   func(orailtypes.IDPrefix) orailtypes.ID
}

// NewPostgresEnvironmentDuplicator constructs the durable copier.
func NewPostgresEnvironmentDuplicator(pool *pg.Pool, keyProvider secrets.KeyProvider) *PostgresEnvironmentDuplicator {
	return &PostgresEnvironmentDuplicator{
		Pool:        pool,
		KeyProvider: keyProvider,
		Now:         func() time.Time { return time.Now().UTC() },
		IDFactory:   defaultResourceID,
	}
}

type duplicateVariableRecord struct {
	ID             secrets.VariableID
	Workspace      string
	Project        string
	Environment    string
	Service        string
	Name           string
	Scope          secrets.Scope
	ValueKind      string
	Envelope       secrets.Envelope
	References     []byte
	LengthBucket   string
	KEKFingerprint string
	Version        int
	LastRotatedAt  time.Time
}

// DuplicateEnvironment implements EnvironmentDuplicator.
func (s *PostgresEnvironmentDuplicator) DuplicateEnvironment(ctx context.Context, request EnvironmentDuplicateRequest) (EnvironmentDuplicateResult, error) {
	if s == nil || s.Pool == nil || s.KeyProvider == nil {
		return EnvironmentDuplicateResult{}, errors.New("environment duplicate: postgres pool and key provider are required")
	}
	if err := ctx.Err(); err != nil {
		return EnvironmentDuplicateResult{}, err
	}
	request.Slug = strings.TrimSpace(request.Slug)
	request.Name = strings.TrimSpace(request.Name)
	request.Tier = strings.TrimSpace(request.Tier)
	if request.SourceID == "" || request.Slug == "" {
		return EnvironmentDuplicateResult{}, fmt.Errorf("%w: source environment and target slug are required", ErrResourceInvalid)
	}
	if err := idempotency.ValidateKey(request.IdempotencyKey); err != nil {
		return EnvironmentDuplicateResult{}, fmt.Errorf("%w: Idempotency-Key is required for config-bearing duplication", ErrResourceInvalid)
	}
	requestHash, err := environmentDuplicateRequestHash(request)
	if err != nil {
		return EnvironmentDuplicateResult{}, fmt.Errorf("environment duplicate: fingerprint request: %w", err)
	}
	fingerprint := idempotency.RequestFingerprint{
		Method:     http.MethodPost,
		Path:       environmentDuplicatePathTemplate,
		BodySHA256: requestHash,
	}

	now := s.now()
	idempotencyStore := idempotency.NewPostgresStore(s.Pool)
	idempotencyStore.Now = s.Now
	var result EnvironmentDuplicateResult
	err = s.Pool.WithTxOpts(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx *pg.Tx) error {
		reservation, err := idempotencyStore.ReserveInTx(ctx, tx, request.IdempotencyKey, fingerprint, environmentDuplicateTTL)
		if errors.Is(err, idempotency.ErrKeyReused) || errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
			return ErrEnvironmentDuplicateIdempotencyConflict
		}
		if err != nil {
			return err
		}
		if reservation.IsTerminal() {
			if reservation.Response == nil ||
				reservation.Response.StatusCode != http.StatusCreated ||
				len(reservation.Response.Body) == 0 {
				return idempotency.ErrReservationFenced
			}
			if err := json.Unmarshal(reservation.Response.Body, &result); err != nil {
				return fmt.Errorf("environment duplicate: decode idempotency response: %w", err)
			}
			result.Replayed = true
			return nil
		}

		source, err := lockSourceEnvironment(ctx, tx, request.SourceID)
		if err != nil {
			return err
		}
		if request.ProjectID != "" && request.ProjectID != source.ProjectID {
			return fmt.Errorf("%w: duplicate must stay in the source project", ErrResourceInvalid)
		}
		if request.Name == "" {
			request.Name = source.Name + " copy"
		}
		if request.Tier == "" {
			request.Tier = source.Tier
		}
		if !validEnvironmentTier(request.Tier) {
			return fmt.Errorf("%w: invalid environment tier %q", ErrResourceInvalid, request.Tier)
		}
		protected := false
		if request.IsProtected != nil {
			protected = *request.IsProtected
		}

		target := Environment{
			ID:              s.id(orailtypes.IDPrefixEnvironment),
			ProjectID:       source.ProjectID,
			Slug:            request.Slug,
			Name:            request.Name,
			Tier:            request.Tier,
			IsProtected:     protected,
			ResourceVersion: 1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO environments (id, project_id, slug, name, tier, is_protected, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
			target.ID, target.ProjectID, target.Slug, target.Name, target.Tier, target.IsProtected, now,
		); err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("%w: environment slug %q already exists in project %q", ErrResourceConflict, target.Slug, target.ProjectID)
			}
			return fmt.Errorf("environment duplicate: insert target: %w", err)
		}

		if err := validateDuplicateSourceScope(ctx, tx, source); err != nil {
			return err
		}
		configCount, err := copyEnvironmentServiceConfigs(ctx, tx, source, target, now)
		if err != nil {
			return err
		}
		variableCount, versionCount, err := s.copyEnvironmentVariables(ctx, tx, source, target, now)
		if err != nil {
			return err
		}
		omissions, err := loadEnvironmentDuplicateOmissions(ctx, tx, source.ID)
		if err != nil {
			return err
		}

		result = EnvironmentDuplicateResult{
			Environment:            target,
			CopyConfig:             true,
			CopiedServiceConfigs:   configCount,
			CopiedVariables:        variableCount,
			CopiedVariableVersions: versionCount,
			OmittedResources:       omissions,
		}
		body, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("environment duplicate: encode replay response: %w", err)
		}
		if _, err := idempotencyStore.CompleteSuccessInTx(ctx, tx, reservation.Reference(), idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
		}); err != nil {
			return fmt.Errorf("environment duplicate: commit idempotency response: %w", err)
		}
		return nil
	})
	if err != nil {
		return EnvironmentDuplicateResult{}, err
	}
	return result, nil
}

func environmentDuplicateRequestHash(request EnvironmentDuplicateRequest) (string, error) {
	body, err := json.Marshal(struct {
		SourceID    orailtypes.ID `json:"source_id"`
		ProjectID   orailtypes.ID `json:"project_id,omitempty"`
		Slug        string        `json:"slug"`
		Name        string        `json:"name,omitempty"`
		Tier        string        `json:"tier,omitempty"`
		IsProtected *bool         `json:"is_protected,omitempty"`
		CopyConfig  bool          `json:"copy_config"`
	}{
		SourceID: request.SourceID, ProjectID: request.ProjectID, Slug: request.Slug,
		Name: request.Name, Tier: request.Tier, IsProtected: request.IsProtected, CopyConfig: true,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func lockSourceEnvironment(ctx context.Context, tx *pg.Tx, sourceID orailtypes.ID) (Environment, error) {
	var source Environment
	err := tx.QueryRow(ctx,
		`SELECT id, project_id, slug, name, tier, is_protected, created_at, updated_at
		 FROM environments WHERE id=$1 AND deleted_at IS NULL FOR SHARE`,
		sourceID,
	).Scan(&source.ID, &source.ProjectID, &source.Slug, &source.Name, &source.Tier, &source.IsProtected, &source.CreatedAt, &source.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Environment{}, ErrResourceNotFound
	}
	if err != nil {
		return Environment{}, fmt.Errorf("environment duplicate: lock source: %w", err)
	}
	source.CreatedAt = source.CreatedAt.UTC()
	source.UpdatedAt = source.UpdatedAt.UTC()
	return source, nil
}

func validateDuplicateSourceScope(ctx context.Context, tx *pg.Tx, source Environment) error {
	var invalidConfigs, invalidVariables bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM service_environment_configs c
			LEFT JOIN services s ON s.id=c.service_id AND s.project_id=c.project_id
			WHERE c.environment_id=$1 AND c.project_id=$2 AND s.id IS NULL
		)`,
		source.ID, source.ProjectID,
	).Scan(&invalidConfigs); err != nil {
		return fmt.Errorf("environment duplicate: validate service config scope: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM service_variables v
			LEFT JOIN services s ON s.id=v.service_id AND s.project_id=v.project_id
			WHERE v.environment_id=$1 AND v.project_id=$2 AND v.deleted_at IS NULL
			  AND v.service_id <> '' AND s.id IS NULL
		)`,
		source.ID, source.ProjectID,
	).Scan(&invalidVariables); err != nil {
		return fmt.Errorf("environment duplicate: validate variable scope: %w", err)
	}
	if invalidConfigs || invalidVariables {
		return fmt.Errorf("%w: source environment contains cross-project service ownership", ErrResourceConflict)
	}
	return nil
}

func copyEnvironmentServiceConfigs(ctx context.Context, tx *pg.Tx, source, target Environment, now time.Time) (int, error) {
	tag, err := tx.Exec(ctx,
		`INSERT INTO service_environment_configs (
			project_id, service_id, environment_id, version, root_directory, builder,
			dockerfile_path, build_command, start_command, predeploy_command, target_port,
			health_kind, health_path, health_port, health_timeout_seconds,
			health_interval_seconds, restart_policy, restart_max_retries, replicas,
			overlap_enabled, drain_grace_seconds, created_at, updated_at
		)
		SELECT project_id, service_id, $1, version, root_directory, builder,
			dockerfile_path, build_command, start_command, predeploy_command, target_port,
			health_kind, health_path, health_port, health_timeout_seconds,
			health_interval_seconds, restart_policy, restart_max_retries, replicas,
			overlap_enabled, drain_grace_seconds, $2, $2
		FROM service_environment_configs
		WHERE environment_id=$3 AND project_id=$4`,
		target.ID, now, source.ID, source.ProjectID,
	)
	if err != nil {
		return 0, fmt.Errorf("environment duplicate: copy service configs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *PostgresEnvironmentDuplicator) copyEnvironmentVariables(ctx context.Context, tx *pg.Tx, source, target Environment, now time.Time) (int, int, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, workspace_id, project_id, environment_id, service_id, name, scope,
			value_kind, value_ciphertext, kek_id, wrapped_dek, canonical_form,
			"references", length_bucket, kek_fingerprint, version, last_rotated_at
		 FROM service_variables
		 WHERE environment_id=$1 AND project_id=$2 AND deleted_at IS NULL
		 ORDER BY id FOR SHARE`,
		source.ID, source.ProjectID,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("environment duplicate: list variables: %w", err)
	}
	var variables []duplicateVariableRecord
	for rows.Next() {
		var variable duplicateVariableRecord
		if err := rows.Scan(
			&variable.ID, &variable.Workspace, &variable.Project, &variable.Environment,
			&variable.Service, &variable.Name, &variable.Scope, &variable.ValueKind,
			&variable.Envelope.Ciphertext, &variable.Envelope.KEKID, &variable.Envelope.WrappedDEK,
			&variable.Envelope.CanonicalForm, &variable.References, &variable.LengthBucket,
			&variable.KEKFingerprint, &variable.Version, &variable.LastRotatedAt,
		); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("environment duplicate: scan variable metadata: %w", err)
		}
		variables = append(variables, variable)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, fmt.Errorf("environment duplicate: iterate variables: %w", err)
	}
	rows.Close()

	fingerprint, err := s.KeyProvider.Fingerprint(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("environment duplicate: resolve active key fingerprint: %w", err)
	}
	versionCount := 0
	for _, variable := range variables {
		newID := secrets.VariableID(s.id(orailtypes.IDPrefixVariable))
		sealed, err := resealDuplicateVariable(ctx, s.KeyProvider, variable.ID, newID, variable.Envelope)
		if err != nil {
			return 0, 0, fmt.Errorf("environment duplicate: re-encrypt active variable %s: %w", variable.ID, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO service_variables (
				id, workspace_id, project_id, environment_id, service_id, name, scope,
				value_kind, value_ciphertext, kek_id, wrapped_dek, canonical_form,
				"references", length_bucket, kek_fingerprint, version,
				last_rotated_at, created_at, updated_at, deleted_at
			 ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18,NULL)`,
			newID, variable.Workspace, target.ProjectID, target.ID, variable.Service,
			variable.Name, variable.Scope, variable.ValueKind, sealed.Ciphertext, sealed.KEKID,
			sealed.WrappedDEK, sealed.CanonicalForm, variable.References, variable.LengthBucket,
			fingerprint, variable.Version, variable.LastRotatedAt, now,
		); err != nil {
			return 0, 0, fmt.Errorf("environment duplicate: insert variable metadata: %w", err)
		}

		copied, err := s.copyVariableVersions(ctx, tx, variable.ID, newID, target, fingerprint, now)
		if err != nil {
			return 0, 0, err
		}
		versionCount += copied
	}
	return len(variables), versionCount, nil
}

func (s *PostgresEnvironmentDuplicator) copyVariableVersions(ctx context.Context, tx *pg.Tx, sourceID, targetID secrets.VariableID, target Environment, fingerprint string, now time.Time) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT version, workspace_id, service_id, name, scope, value_kind,
			value_ciphertext, kek_id, wrapped_dek, canonical_form, "references",
			length_bucket, last_rotated_at
		 FROM service_variable_versions
		 WHERE variable_id=$1 ORDER BY version`,
		sourceID,
	)
	if err != nil {
		return 0, fmt.Errorf("environment duplicate: list variable versions: %w", err)
	}
	var versions []duplicateVariableRecord
	for rows.Next() {
		var version duplicateVariableRecord
		version.ID = sourceID
		if err := rows.Scan(
			&version.Version, &version.Workspace, &version.Service, &version.Name,
			&version.Scope, &version.ValueKind, &version.Envelope.Ciphertext,
			&version.Envelope.KEKID, &version.Envelope.WrappedDEK,
			&version.Envelope.CanonicalForm, &version.References,
			&version.LengthBucket, &version.LastRotatedAt,
		); err != nil {
			rows.Close()
			return 0, fmt.Errorf("environment duplicate: scan variable version metadata: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("environment duplicate: iterate variable versions: %w", err)
	}
	rows.Close()

	for _, version := range versions {
		sealed, err := resealDuplicateVariable(ctx, s.KeyProvider, sourceID, targetID, version.Envelope)
		if err != nil {
			return 0, fmt.Errorf("environment duplicate: re-encrypt variable version %d: %w", version.Version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO service_variable_versions (
				variable_id, version, workspace_id, project_id, environment_id,
				service_id, name, scope, value_kind, value_ciphertext, kek_id,
				wrapped_dek, canonical_form, "references", length_bucket,
				kek_fingerprint, last_rotated_at, created_at
			 ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			targetID, version.Version, version.Workspace, target.ProjectID, target.ID,
			version.Service, version.Name, version.Scope, version.ValueKind,
			sealed.Ciphertext, sealed.KEKID, sealed.WrappedDEK, sealed.CanonicalForm,
			version.References, version.LengthBucket, fingerprint, version.LastRotatedAt, now,
		); err != nil {
			return 0, fmt.Errorf("environment duplicate: insert variable version metadata: %w", err)
		}
	}
	return len(versions), nil
}

func resealDuplicateVariable(ctx context.Context, keyProvider secrets.KeyProvider, sourceID, targetID secrets.VariableID, envelope secrets.Envelope) (secrets.Envelope, error) {
	plaintext, err := secrets.OpenForVariable(ctx, keyProvider, sourceID, envelope)
	if err != nil {
		return secrets.Envelope{}, err
	}
	sealed, sealErr := secrets.SealForVariable(ctx, keyProvider, targetID, plaintext)
	zeroDuplicatePlaintext(plaintext)
	if sealErr != nil {
		return secrets.Envelope{}, sealErr
	}
	return sealed, nil
}

func zeroDuplicatePlaintext(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func loadEnvironmentDuplicateOmissions(ctx context.Context, tx *pg.Tx, sourceID orailtypes.ID) ([]EnvironmentDuplicateOmission, error) {
	queries := []struct {
		resource string
		query    string
		reason   string
	}{
		{"domains", `SELECT count(*) FROM domains WHERE environment_id=$1`, "domain names and route publication are not cloned; create them explicitly in the target environment"},
		{"volumes", `SELECT count(*) FROM volumes WHERE environment_id=$1 AND deleted_at IS NULL`, "persistent data is never cloned implicitly; provision or restore a target volume explicitly"},
		{"deployments", `SELECT count(*) FROM deployments WHERE environment_id=$1`, "deployment history and running instances remain bound to the source environment"},
	}
	out := make([]EnvironmentDuplicateOmission, 0, len(queries))
	for _, item := range queries {
		var count int
		if err := tx.QueryRow(ctx, item.query, sourceID).Scan(&count); err != nil {
			return nil, fmt.Errorf("environment duplicate: count omitted %s: %w", item.resource, err)
		}
		out = append(out, EnvironmentDuplicateOmission{Resource: item.resource, Count: count, Reason: item.reason})
	}
	return out, nil
}

func (s *PostgresEnvironmentDuplicator) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresEnvironmentDuplicator) id(prefix orailtypes.IDPrefix) orailtypes.ID {
	if s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return defaultResourceID(prefix)
}
