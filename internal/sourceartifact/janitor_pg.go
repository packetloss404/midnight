// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
)

// LifecycleAdvisoryLockKey serializes artifact binding, release, terminal
// reconciliation, and filesystem/database janitor decisions on one Postgres
// transaction connection.
const LifecycleAdvisoryLockKey int64 = 60718768232145222

var DefaultJanitorConfig = JanitorConfig{
	BlobRetention:       24 * time.Hour,
	ReferenceRetention:  24 * time.Hour,
	WorkRetention:       24 * time.Hour,
	QuarantineRetention: time.Hour,
	MaxItems:            128,
	MaxVerifyBytes:      128 << 20,
}

type JanitorConfig struct {
	BlobRetention       time.Duration
	ReferenceRetention  time.Duration
	WorkRetention       time.Duration
	QuarantineRetention time.Duration
	MaxItems            int
	MaxVerifyBytes      int64
}

func (config JanitorConfig) Validate() error {
	if config.BlobRetention <= 0 ||
		config.ReferenceRetention <= 0 ||
		config.WorkRetention <= 0 ||
		config.QuarantineRetention <= 0 ||
		config.MaxItems < 2 ||
		config.MaxVerifyBytes < 2*DefaultLimits.MaxCompressedBytes {
		return errors.New("source artifact: janitor requires positive retention, at least two items, and two maximum-sized blobs of verification budget")
	}
	return nil
}

type JanitorResult struct {
	Skipped                    bool
	TerminalReferencesReleased int64
	ReleasedReferencesRemoved  int64
	MetadataRemoved            int64
	BlobsQuarantined           int
	StagingQuarantined         int
	CheckoutsQuarantined       int
	QuarantineRestored         int
	QuarantineFinalized        int
}

// RunJanitor serializes lifecycle truth and filesystem quarantine in one
// transaction-level advisory lock. Newly quarantined content is never deleted
// in the same pass; a later pass must re-check durable reference/metadata truth
// before finalization. Filesystem moves are restored if the transaction fails.
func (s *PostgresStore) RunJanitor(
	ctx context.Context,
	filesystem *FSStore,
	now time.Time,
	config JanitorConfig,
) (JanitorResult, error) {
	if s == nil || s.Pool == nil {
		return JanitorResult{}, errors.New("source artifact: Postgres pool is required")
	}
	if filesystem == nil {
		return JanitorResult{}, errors.New("source artifact: filesystem store is required")
	}
	if err := config.Validate(); err != nil {
		return JanitorResult{}, err
	}
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()
	blobCutoff := now.Add(-config.BlobRetention)
	referenceCutoff := now.Add(-config.ReferenceRetention)
	workCutoff := now.Add(-config.WorkRetention)
	quarantineCutoff := now.Add(-config.QuarantineRetention)

	discovery, err := filesystem.discoverJanitorCandidates(ctx, config.MaxItems)
	if err != nil {
		return JanitorResult{}, err
	}
	var result JanitorResult
	var sweep *preparedJanitorSweep
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		locked, err := tryAcquireLifecycleLock(ctx, tx)
		if err != nil {
			return err
		}
		if !locked {
			result.Skipped = true
			return nil
		}
		released, err := releaseTerminalReferencesInTx(ctx, tx, now, config.MaxItems)
		if err != nil {
			return err
		}
		result.TerminalReferencesReleased = released

		tag, err := tx.Exec(
			ctx,
			`WITH candidates AS (
			    SELECT operation_id
			      FROM source_artifact_refs
			     WHERE released_at IS NOT NULL
			       AND released_at < $1
			     ORDER BY released_at,operation_id
			     LIMIT $2
			     FOR UPDATE SKIP LOCKED
			 )
			 DELETE FROM source_artifact_refs AS artifact_ref
			  USING candidates
			  WHERE artifact_ref.operation_id=candidates.operation_id`,
			referenceCutoff,
			config.MaxItems,
		)
		if err != nil {
			return fmt.Errorf("source artifact: remove released ownership references: %w", err)
		}
		result.ReleasedReferencesRemoved = tag.RowsAffected()

		metadataCandidates, err := metadataJanitorCandidatesInTx(
			ctx,
			tx,
			blobCutoff,
			config.MaxItems,
		)
		if err != nil {
			return err
		}
		candidateDigests := make(map[string]struct{}, len(discovery.digests)+len(metadataCandidates))
		for digest := range discovery.digests {
			candidateDigests[digest] = struct{}{}
		}
		for digest := range metadataCandidates {
			candidateDigests[digest] = struct{}{}
		}
		required, err := requiredCandidateDigestsInTx(
			ctx,
			tx,
			candidateDigests,
			blobCutoff,
			referenceCutoff,
		)
		if err != nil {
			return err
		}
		sweep, err = filesystem.prepareJanitorSweep(
			ctx,
			required,
			candidateDigests,
			metadataCandidates,
			discovery,
			blobCutoff,
			workCutoff,
			quarantineCutoff,
			config.MaxItems,
			config.MaxVerifyBytes,
		)
		if err != nil {
			return err
		}
		result.BlobsQuarantined = sweep.result.BlobsQuarantined
		result.StagingQuarantined = sweep.result.StagingQuarantined
		result.CheckoutsQuarantined = sweep.result.CheckoutsQuarantined
		result.QuarantineRestored = sweep.result.QuarantineRestored

		eligibleDigests := make([]string, 0, len(sweep.eligibleMetadataDigests))
		for digest := range sweep.eligibleMetadataDigests {
			eligibleDigests = append(eligibleDigests, digest)
		}
		sort.Strings(eligibleDigests)
		if len(eligibleDigests) > 0 {
			tag, err = tx.Exec(
				ctx,
				`WITH candidates AS (
				    SELECT artifact.digest
				      FROM source_artifacts AS artifact
				     WHERE artifact.created_at < $1
				       AND artifact.digest=ANY($2::text[])
				       AND NOT EXISTS (
				           SELECT 1
				             FROM source_artifact_refs AS artifact_ref
				            WHERE artifact_ref.artifact_digest=artifact.digest
				       )
				       AND NOT EXISTS (
				           SELECT 1
				             FROM source_artifact_workspace_charges AS charge
				            WHERE charge.artifact_digest=artifact.digest
				       )
				     ORDER BY artifact.created_at,artifact.digest
				     LIMIT $3
				     FOR UPDATE SKIP LOCKED
				 )
				 DELETE FROM source_artifacts AS artifact
				  USING candidates
				  WHERE artifact.digest=candidates.digest`,
				blobCutoff,
				eligibleDigests,
				config.MaxItems,
			)
			if err != nil {
				return fmt.Errorf("source artifact: remove unowned metadata: %w", err)
			}
			result.MetadataRemoved = tag.RowsAffected()
		}
		return nil
	})
	if err != nil {
		if sweep != nil {
			err = errors.Join(err, sweep.Restore())
		}
		return result, err
	}
	if sweep != nil {
		if err := sweep.Finalize(); err != nil {
			return result, err
		}
		result.QuarantineFinalized = sweep.result.QuarantineFinalized
	}
	return result, nil
}

func acquireLifecycleLock(ctx context.Context, connection pg.Conn) error {
	if connection == nil {
		return errors.New("source artifact: lifecycle lock connection is required")
	}
	if _, err := connection.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock($1)`,
		LifecycleAdvisoryLockKey,
	); err != nil {
		return fmt.Errorf("source artifact: acquire lifecycle lock: %w", err)
	}
	return nil
}

func tryAcquireLifecycleLock(ctx context.Context, connection pg.Conn) (bool, error) {
	if connection == nil {
		return false, errors.New("source artifact: lifecycle lock connection is required")
	}
	var locked bool
	if err := connection.QueryRow(
		ctx,
		`SELECT pg_try_advisory_xact_lock($1)`,
		LifecycleAdvisoryLockKey,
	).Scan(&locked); err != nil {
		return false, fmt.Errorf("source artifact: try lifecycle lock: %w", err)
	}
	return locked, nil
}

func releaseTerminalReferencesInTx(
	ctx context.Context,
	tx *pg.Tx,
	releasedAt time.Time,
	maxItems int,
) (int64, error) {
	tag, err := tx.Exec(
		ctx,
		`WITH candidates AS (
		    SELECT artifact_ref.operation_id
		      FROM source_artifact_refs AS artifact_ref
		     WHERE artifact_ref.released_at IS NULL
		       AND EXISTS (
		           SELECT 1
		             FROM jobs AS terminal_job
		            WHERE terminal_job.queue='build'
		              AND terminal_job.state IN ('succeeded','failed','canceled')
		              AND terminal_job.parent_operation_id=artifact_ref.operation_id
		              AND terminal_job.payload->>'schema_version'='midnight.dev/deploy-job/v1alpha1'
		              AND terminal_job.payload->>'operation_id'=artifact_ref.operation_id
		              AND terminal_job.payload->>'project_id'=artifact_ref.project_id
		              AND terminal_job.payload->>'service_id'=artifact_ref.service_id
		              AND terminal_job.payload->>'environment_id'=artifact_ref.environment_id
		              AND terminal_job.payload #>> '{request,source,provider}'='artifact'
		              AND terminal_job.payload #>> '{request,source,repo}'=
		                  'sourceartifact://' || artifact_ref.artifact_digest
		              AND terminal_job.payload #>> '{request,source,ref}'=
		                  artifact_ref.artifact_digest
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{request,deployment_id}',''),
		                  terminal_job.payload #>> '{candidate,deployment_id}'
		              )=artifact_ref.deployment_id
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{request,environment_id}',''),
		                  artifact_ref.environment_id
		              )=artifact_ref.environment_id
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{candidate,project_id}',''),
		                  artifact_ref.project_id
		              )=artifact_ref.project_id
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{candidate,service_id}',''),
		                  artifact_ref.service_id
		              )=artifact_ref.service_id
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{candidate,environment_id}',''),
		                  artifact_ref.environment_id
		              )=artifact_ref.environment_id
		              AND COALESCE(
		                  NULLIF(terminal_job.payload #>> '{candidate,deployment_id}',''),
		                  artifact_ref.deployment_id
		              )=artifact_ref.deployment_id
		       )
		       AND NOT EXISTS (
		           SELECT 1
		             FROM jobs AS active_job
		            WHERE active_job.queue='build'
		              AND active_job.state NOT IN ('succeeded','failed','canceled')
		              AND (
		                  active_job.operation_id=artifact_ref.operation_id
		                  OR active_job.parent_operation_id=artifact_ref.operation_id
		              )
		       )
		     ORDER BY artifact_ref.created_at,artifact_ref.operation_id
		     LIMIT $2
		     FOR UPDATE SKIP LOCKED
		 )
		 UPDATE source_artifact_refs AS artifact_ref
		    SET released_at=GREATEST($1,artifact_ref.created_at)
		   FROM candidates
		  WHERE artifact_ref.operation_id=candidates.operation_id`,
		releasedAt.UTC(),
		maxItems,
	)
	if err != nil {
		return 0, fmt.Errorf("source artifact: reconcile terminal ownership references: %w", err)
	}
	return tag.RowsAffected(), nil
}

func metadataJanitorCandidatesInTx(
	ctx context.Context,
	tx *pg.Tx,
	metadataCutoff time.Time,
	maxItems int,
) (map[string]struct{}, error) {
	rows, err := tx.Query(
		ctx,
		`SELECT artifact.digest
		   FROM source_artifacts AS artifact
		  WHERE artifact.created_at < $1
		    AND NOT EXISTS (
		        SELECT 1
		          FROM source_artifact_refs AS artifact_ref
		         WHERE artifact_ref.artifact_digest=artifact.digest
		    )
		  ORDER BY artifact.created_at,artifact.digest
		  LIMIT $2`,
		metadataCutoff,
		maxItems,
	)
	if err != nil {
		return nil, fmt.Errorf("source artifact: list janitor metadata candidates: %w", err)
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("source artifact: scan janitor metadata candidate: %w", err)
		}
		if !digestRE.MatchString(digest) {
			return nil, fmt.Errorf("%w: database contains malformed digest", ErrIntegrity)
		}
		result[digest] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source artifact: list janitor metadata candidates: %w", err)
	}
	return result, nil
}

func requiredCandidateDigestsInTx(
	ctx context.Context,
	tx *pg.Tx,
	candidateDigests map[string]struct{},
	metadataCutoff time.Time,
	referenceCutoff time.Time,
) (map[string]struct{}, error) {
	if len(candidateDigests) == 0 {
		return map[string]struct{}{}, nil
	}
	candidates := make([]string, 0, len(candidateDigests))
	for digest := range candidateDigests {
		candidates = append(candidates, digest)
	}
	sort.Strings(candidates)
	rows, err := tx.Query(
		ctx,
		`SELECT artifact.digest
		   FROM source_artifacts AS artifact
		  WHERE artifact.digest=ANY($1::text[])
		    AND artifact.created_at >= $2
		 UNION
		 SELECT artifact_ref.artifact_digest
		   FROM source_artifact_refs AS artifact_ref
		  WHERE artifact_ref.artifact_digest=ANY($1::text[])
		    AND (
		        artifact_ref.released_at IS NULL
		        OR artifact_ref.released_at >= $3
		    )`,
		candidates,
		metadataCutoff,
		referenceCutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("source artifact: list protected candidate digests: %w", err)
	}
	defer rows.Close()
	result := map[string]struct{}{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, fmt.Errorf("source artifact: scan protected candidate digest: %w", err)
		}
		if !digestRE.MatchString(digest) {
			return nil, fmt.Errorf("%w: database contains malformed digest", ErrIntegrity)
		}
		result[digest] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source artifact: list protected candidate digests: %w", err)
	}
	return result, nil
}
