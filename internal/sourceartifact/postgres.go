// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// MaxMessageBytes is shared with the tenant up route and CLI contract.
const MaxMessageBytes = 256

var (
	ErrReferenceConflict = errors.New("source artifact: ownership reference conflict")
)

// Reference binds one artifact to the operation/deployment that owns its
// lifecycle. Released references remain as history until an explicit GC
// transaction removes them alongside unreferenced artifact metadata.
type Reference struct {
	OperationID   string
	DeploymentID  string
	Digest        string
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	Message       string
	CreatedAt     time.Time
	ReleasedAt    *time.Time
}

// PostgresStore owns source_artifacts/source_artifact_refs persistence. It
// accepts a caller-owned transaction so tenant authorization, operation,
// deployment, outbox, idempotency, and artifact ownership commit atomically.
type PostgresStore struct {
	Pool *pg.Pool
	Now  func() time.Time
}

func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{
		Pool: pool,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

// BindVerifiedInTx is the production tenant-upload boundary. It acquires the
// same lifecycle lock used by garbage collection, verifies the canonical
// filesystem publication while that lock is held, and only then persists
// metadata and exact ownership. A janitor can therefore win before this
// transaction, but accepted work can never bind a blob it already removed.
func (s *PostgresStore) BindVerifiedInTx(
	ctx context.Context,
	tx *pg.Tx,
	filesystem *FSStore,
	artifact Artifact,
	reference Reference,
) error {
	if filesystem == nil {
		return errors.New("source artifact: filesystem verifier is required")
	}
	if tx == nil {
		return errors.New("source artifact: transaction is required")
	}
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	if err := validateReference(reference, artifact.Digest); err != nil {
		return err
	}
	if err := acquireLifecycleLock(ctx, tx); err != nil {
		return err
	}
	if err := filesystem.VerifyArtifact(ctx, artifact); err != nil {
		return err
	}
	createdAt := reference.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = s.now()
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO source_artifacts (
			digest, compressed_bytes, uncompressed_bytes, entry_count,
			storage_key, created_at
		 ) VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (digest) DO NOTHING`,
		artifact.Digest,
		artifact.CompressedBytes,
		artifact.UncompressedBytes,
		artifact.EntryCount,
		artifact.StorageKey,
		createdAt,
	); err != nil {
		return fmt.Errorf("source artifact: insert metadata: %w", err)
	}
	var compressedBytes, uncompressedBytes int64
	var entryCount int
	var storageKey string
	if err := tx.QueryRow(
		ctx,
		`SELECT compressed_bytes, uncompressed_bytes, entry_count, storage_key
		   FROM source_artifacts
		  WHERE digest=$1`,
		artifact.Digest,
	).Scan(&compressedBytes, &uncompressedBytes, &entryCount, &storageKey); err != nil {
		return fmt.Errorf("source artifact: verify metadata: %w", err)
	}
	if compressedBytes != artifact.CompressedBytes ||
		uncompressedBytes != artifact.UncompressedBytes ||
		entryCount != artifact.EntryCount ||
		storageKey != artifact.StorageKey {
		return fmt.Errorf("%w: immutable artifact metadata differs", ErrReferenceConflict)
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO source_artifact_refs (
			operation_id, deployment_id, artifact_digest, project_id,
			service_id, environment_id, message, created_at
		 ) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)
		 ON CONFLICT (operation_id) DO NOTHING`,
		reference.OperationID,
		reference.DeploymentID,
		artifact.Digest,
		reference.ProjectID,
		reference.ServiceID,
		reference.EnvironmentID,
		reference.Message,
		createdAt,
	); err != nil {
		return fmt.Errorf("source artifact: insert ownership reference: %w", err)
	}
	var existing Reference
	var persistedMessage *string
	if err := tx.QueryRow(
		ctx,
		`SELECT operation_id, deployment_id, artifact_digest, project_id,
		        service_id, environment_id, message, created_at
		   FROM source_artifact_refs
		  WHERE operation_id=$1`,
		reference.OperationID,
	).Scan(
		&existing.OperationID,
		&existing.DeploymentID,
		&existing.Digest,
		&existing.ProjectID,
		&existing.ServiceID,
		&existing.EnvironmentID,
		&persistedMessage,
		&existing.CreatedAt,
	); err != nil {
		return fmt.Errorf("source artifact: verify ownership reference: %w", err)
	}
	if persistedMessage != nil {
		existing.Message = *persistedMessage
	}
	if existing.OperationID != reference.OperationID ||
		existing.DeploymentID != reference.DeploymentID ||
		existing.Digest != artifact.Digest ||
		existing.ProjectID != reference.ProjectID ||
		existing.ServiceID != reference.ServiceID ||
		existing.EnvironmentID != reference.EnvironmentID ||
		existing.Message != reference.Message {
		return ErrReferenceConflict
	}
	return nil
}

// ReleaseTerminalReferences converges references left active by a worker crash
// between queue terminalization and Release. It trusts only durable terminal
// build-job rows linked by operation_id or parent_operation_id. If terminal
// truth is absent, or any matching build job remains nonterminal, the
// reference stays active so a reclaimed attempt can still materialize it.
//
// The update is idempotent and also covers build work canceled before lease.
func (s *PostgresStore) ReleaseTerminalReferences(ctx context.Context, releasedAt time.Time) (int64, error) {
	if s == nil || s.Pool == nil {
		return 0, errors.New("source artifact: Postgres pool is required")
	}
	if releasedAt.IsZero() {
		releasedAt = s.now()
	}
	var released int64
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if err := acquireLifecycleLock(ctx, tx); err != nil {
			return err
		}
		var err error
		released, err = releaseTerminalReferencesInTx(ctx, tx, releasedAt.UTC(), 1024)
		return err
	})
	return released, err
}

// ActiveDigests returns the current do-not-prune set for an age-gated
// filesystem sweep. Production GC should take a conservative cutoff and
// serialize binding/sweeping before scheduling Prune.
func (s *PostgresStore) ActiveDigests(ctx context.Context) (map[string]struct{}, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("source artifact: Postgres pool is required")
	}
	rows, err := s.Pool.Query(
		ctx,
		`SELECT DISTINCT artifact_digest
		   FROM source_artifact_refs
		  WHERE released_at IS NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("source artifact: list active digests: %w", err)
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("source artifact: scan active digest: %w", err)
		}
		if !digestRE.MatchString(digest) {
			return nil, fmt.Errorf("%w: database contains malformed digest", ErrIntegrity)
		}
		result[digest] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source artifact: list active digests: %w", err)
	}
	return result, nil
}

// GetReference returns one ownership record for worker cleanup/recovery.
func (s *PostgresStore) GetReference(ctx context.Context, operationID string) (Reference, bool, error) {
	if s == nil || s.Pool == nil {
		return Reference{}, false, errors.New("source artifact: Postgres pool is required")
	}
	var reference Reference
	var message *string
	var releasedAt *time.Time
	err := s.Pool.QueryRow(
		ctx,
		`SELECT operation_id, deployment_id, artifact_digest, project_id,
		        service_id, environment_id, message, created_at, released_at
		   FROM source_artifact_refs
		  WHERE operation_id=$1`,
		operationID,
	).Scan(
		&reference.OperationID,
		&reference.DeploymentID,
		&reference.Digest,
		&reference.ProjectID,
		&reference.ServiceID,
		&reference.EnvironmentID,
		&message,
		&reference.CreatedAt,
		&releasedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reference{}, false, nil
	}
	if err != nil {
		return Reference{}, false, fmt.Errorf("source artifact: get ownership reference: %w", err)
	}
	if message != nil {
		reference.Message = *message
	}
	if releasedAt != nil {
		value := releasedAt.UTC()
		reference.ReleasedAt = &value
	}
	return reference, true, nil
}

// ValidateMessage enforces the persisted product-label boundary. Callers must
// not place this text in artifact URIs or worker event logs.
func ValidateMessage(message string) error {
	if !utf8.ValidString(message) {
		return errors.New("source artifact: deployment message must be valid UTF-8")
	}
	if len(message) > MaxMessageBytes {
		return fmt.Errorf("source artifact: deployment message exceeds %d bytes", MaxMessageBytes)
	}
	for _, character := range message {
		if unicode.IsControl(character) {
			return errors.New("source artifact: deployment message contains control characters")
		}
	}
	return nil
}

func validateArtifact(artifact Artifact) error {
	if !digestRE.MatchString(artifact.Digest) ||
		artifact.CompressedBytes <= 0 ||
		artifact.UncompressedBytes < 0 ||
		artifact.EntryCount <= 0 {
		return errors.New("source artifact: invalid artifact metadata")
	}
	expectedStorageKey, err := StorageKey(artifact.Digest)
	if err != nil || artifact.StorageKey != expectedStorageKey {
		return errors.New("source artifact: invalid artifact storage key")
	}
	expectedURI, err := URI(artifact.Digest)
	if err != nil || artifact.URI != expectedURI {
		return errors.New("source artifact: invalid artifact URI")
	}
	return nil
}

func validateReference(reference Reference, digest string) error {
	if strings.TrimSpace(reference.OperationID) == "" ||
		strings.TrimSpace(reference.DeploymentID) == "" ||
		strings.TrimSpace(reference.ProjectID) == "" ||
		strings.TrimSpace(reference.ServiceID) == "" ||
		strings.TrimSpace(reference.EnvironmentID) == "" ||
		(reference.Digest != "" && reference.Digest != digest) {
		return errors.New("source artifact: incomplete ownership reference")
	}
	return ValidateMessage(reference.Message)
}

func (s *PostgresStore) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
