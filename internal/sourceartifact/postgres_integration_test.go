//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/db/pg"
)

func TestReleaseTerminalReferencesIsIdempotentAndFailSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		if dockerUnavailable(err) {
			t.Skipf("docker unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE source_artifacts (
		digest TEXT PRIMARY KEY,
		compressed_bytes BIGINT NOT NULL,
		uncompressed_bytes BIGINT NOT NULL,
		entry_count INTEGER NOT NULL,
		storage_key TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE source_artifact_refs (
		operation_id TEXT PRIMARY KEY,
		deployment_id TEXT NOT NULL DEFAULT 'dep',
		artifact_digest TEXT NOT NULL DEFAULT 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		project_id TEXT NOT NULL DEFAULT 'prj',
		service_id TEXT NOT NULL DEFAULT 'svc',
		environment_id TEXT NOT NULL DEFAULT 'env',
		message TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		released_at TIMESTAMPTZ
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE source_artifact_workspace_charges (
		workspace_id TEXT NOT NULL,
		artifact_digest TEXT NOT NULL,
		compressed_bytes BIGINT NOT NULL,
		reconcile_after TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (workspace_id, artifact_digest)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE jobs (
		queue TEXT NOT NULL,
		state TEXT NOT NULL,
		operation_id TEXT,
		parent_operation_id TEXT,
		payload JSONB NOT NULL DEFAULT '{}'::jsonb
	)`); err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []string{
		"op-parent-succeeded",
		"op-direct-failed",
		"op-parent-canceled",
		"op-active",
		"op-absent",
		"op-terminal-and-active",
		"op-terminal-mismatch",
		"op-wrong-queue",
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO source_artifact_refs(operation_id) VALUES ($1)`,
			operationID,
		); err != nil {
			t.Fatal(err)
		}
	}
	jobs := []struct {
		queue, state, operationID, parentOperationID string
	}{
		{"build", "succeeded", "", "op-parent-succeeded"},
		{"build", "failed", "op-direct-failed", "op-direct-failed"},
		{"build", "canceled", "", "op-parent-canceled"},
		{"build", "leased", "", "op-active"},
		{"build", "succeeded", "", "op-terminal-and-active"},
		{"build", "pending", "", "op-terminal-and-active"},
		{"deploy", "succeeded", "", "op-wrong-queue"},
	}
	for _, job := range jobs {
		payload, err := json.Marshal(map[string]any{
			"schema_version": "midnight.dev/deploy-job/v1alpha1",
			"operation_id":   job.parentOperationID,
			"project_id":     "prj",
			"service_id":     "svc",
			"environment_id": "env",
			"request": map[string]any{
				"deployment_id": "dep",
				"source": map[string]any{
					"provider": "artifact",
					"repo": "sourceartifact://sha256:" +
						strings.Repeat("a", 64),
					"ref": "sha256:" + strings.Repeat("a", 64),
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO jobs(queue,state,operation_id,parent_operation_id,payload)
			 VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),$5::jsonb)`,
			job.queue,
			job.state,
			job.operationID,
			job.parentOperationID,
			payload,
		); err != nil {
			t.Fatal(err)
		}
	}
	mismatchPayload, err := json.Marshal(map[string]any{
		"schema_version": "midnight.dev/deploy-job/v1alpha1",
		"operation_id":   "op-terminal-mismatch",
		"project_id":     "other-project",
		"service_id":     "svc",
		"environment_id": "env",
		"request": map[string]any{
			"deployment_id": "dep",
			"source": map[string]any{
				"provider": "artifact",
				"repo":     "sourceartifact://sha256:" + strings.Repeat("a", 64),
				"ref":      "sha256:" + strings.Repeat("a", 64),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO jobs(queue,state,parent_operation_id,payload)
		 VALUES ('build','succeeded','op-terminal-mismatch',$1::jsonb)`,
		mismatchPayload,
	); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	releasedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	released, err := store.ReleaseTerminalReferences(ctx, releasedAt)
	if err != nil {
		t.Fatal(err)
	}
	if released != 3 {
		t.Fatalf("released = %d, want 3", released)
	}
	released, err = store.ReleaseTerminalReferences(ctx, releasedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatalf("idempotent released = %d, want 0", released)
	}

	rows, err := pool.Query(ctx, `SELECT operation_id, released_at IS NOT NULL
		FROM source_artifact_refs ORDER BY operation_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var operationID string
		var isReleased bool
		if err := rows.Scan(&operationID, &isReleased); err != nil {
			t.Fatal(err)
		}
		got[operationID] = isReleased
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []string{"op-parent-succeeded", "op-direct-failed", "op-parent-canceled"} {
		if !got[operationID] {
			t.Errorf("%s remains active despite terminal build truth", operationID)
		}
	}
	for _, operationID := range []string{"op-active", "op-absent", "op-terminal-and-active", "op-terminal-mismatch", "op-wrong-queue"} {
		if got[operationID] {
			t.Errorf("%s released without exclusive terminal build truth", operationID)
		}
	}

	t.Run("janitor skips lifecycle lock contention", func(t *testing.T) {
		filesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		lockTx, err := pool.Inner().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, LifecycleAdvisoryLockKey); err != nil {
			_ = lockTx.Rollback(ctx)
			t.Fatal(err)
		}
		result, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig)
		if err != nil {
			_ = lockTx.Rollback(ctx)
			t.Fatal(err)
		}
		if !result.Skipped {
			_ = lockTx.Rollback(ctx)
			t.Fatal("janitor queued behind lifecycle lock instead of skipping")
		}
		if err := lockTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		result, err = store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig)
		if err != nil || result.Skipped {
			t.Fatalf("janitor after lock release result=%#v err=%v", result, err)
		}
	})

	t.Run("verified bind-held lifecycle lock makes janitor skip", func(t *testing.T) {
		filesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		payload := makeArchive(t, archiveEntry{
			name: "Dockerfile", body: "FROM scratch\n", typeflag: tar.TypeReg,
		})
		staged, err := filesystem.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := filesystem.Commit(ctx, staged)
		if err != nil {
			t.Fatal(err)
		}
		blobPath := filepath.Join(filesystem.root, filepath.FromSlash(artifact.StorageKey))
		old := releasedAt.Add(-48 * time.Hour)
		if err := os.Chtimes(blobPath, old, old); err != nil {
			t.Fatal(err)
		}
		bound := make(chan struct{})
		commit := make(chan struct{})
		bindErr := make(chan error, 1)
		go func() {
			bindErr <- pool.WithTx(ctx, func(tx *pg.Tx) error {
				if err := store.BindVerifiedInTx(ctx, tx, filesystem, artifact, Reference{
					OperationID:   "op-bind-race",
					DeploymentID:  "dep-bind-race",
					Digest:        artifact.Digest,
					ProjectID:     "prj-bind-race",
					ServiceID:     "svc-bind-race",
					EnvironmentID: "env-bind-race",
					CreatedAt:     old,
				}); err != nil {
					return err
				}
				close(bound)
				<-commit
				return nil
			})
		}()
		select {
		case <-bound:
		case <-time.After(10 * time.Second):
			t.Fatal("BindInTx did not acquire lifecycle lock")
		}
		result, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig)
		if err != nil || !result.Skipped {
			t.Fatalf("contending janitor result=%#v err=%v", result, err)
		}
		close(commit)
		if err := <-bindErr; err != nil {
			t.Fatal(err)
		}
		for pass := 0; pass < 4; pass++ {
			if _, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig); err != nil {
				t.Fatal(err)
			}
		}
		if err := verifyFileDigest(ctx, blobPath, artifact.Digest, -1); err != nil {
			t.Fatalf("accepted bind lost its only canonical blob: %v", err)
		}
	})

	t.Run("janitor win before final verified bind fails closed", func(t *testing.T) {
		filesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		artifact := commitJanitorArtifact(t, filesystem, "pre-bind-gap")
		blobPath := filepath.Join(filesystem.root, filepath.FromSlash(artifact.StorageKey))
		old := releasedAt.Add(-48 * time.Hour)
		if err := os.Chtimes(blobPath, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filesystem.quarantine)
		if err != nil || len(entries) != 1 {
			t.Fatalf("first janitor pass did not quarantine unclaimed blob: entries=%v err=%v", entries, err)
		}
		if err := os.Chtimes(filepath.Join(filesystem.quarantine, entries[0].Name()), old, old); err != nil {
			t.Fatal(err)
		}
		for pass := 0; pass < 4; pass++ {
			if _, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig); err != nil {
				t.Fatal(err)
			}
			entries, err = os.ReadDir(filesystem.quarantine)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) == 0 {
				break
			}
		}
		if len(entries) != 0 {
			t.Fatal("janitor did not finalize the unclaimed publication")
		}
		bindErr := pool.WithTx(ctx, func(tx *pg.Tx) error {
			return store.BindVerifiedInTx(ctx, tx, filesystem, artifact, Reference{
				OperationID:   "op-pre-bind-gap",
				DeploymentID:  "dep-pre-bind-gap",
				Digest:        artifact.Digest,
				ProjectID:     "prj-pre-bind-gap",
				ServiceID:     "svc-pre-bind-gap",
				EnvironmentID: "env-pre-bind-gap",
			})
		})
		if bindErr == nil {
			t.Fatal("verified bind accepted a publication already removed by janitor")
		}
		var count int
		if err := pool.QueryRow(
			ctx,
			`SELECT COUNT(*) FROM source_artifact_refs WHERE operation_id='op-pre-bind-gap'`,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("failed verified bind left an accepted ownership reference")
		}
	})

	t.Run("database failure restores quarantined canonical blob", func(t *testing.T) {
		filesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		artifact := commitJanitorArtifact(t, filesystem, "rollback")
		blobPath := filepath.Join(filesystem.root, filepath.FromSlash(artifact.StorageKey))
		old := releasedAt.Add(-48 * time.Hour)
		if err := os.Chtimes(blobPath, old, old); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO source_artifacts (
			    digest,compressed_bytes,uncompressed_bytes,entry_count,storage_key,created_at
			 ) VALUES ($1,$2,$3,$4,$5,$6)`,
			artifact.Digest,
			artifact.CompressedBytes,
			artifact.UncompressedBytes,
			artifact.EntryCount,
			artifact.StorageKey,
			old,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_source_artifact_delete()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    RAISE EXCEPTION 'forced janitor rollback';
			END
			$$`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `CREATE TRIGGER reject_source_artifact_delete_trigger
			BEFORE DELETE ON source_artifacts
			FOR EACH ROW EXECUTE FUNCTION reject_source_artifact_delete()`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_source_artifact_delete_trigger ON source_artifacts`)
			_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_source_artifact_delete()`)
		}()
		if _, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig); err == nil {
			t.Fatal("forced metadata deletion failure did not fail janitor")
		}
		if err := verifyFileDigest(ctx, blobPath, artifact.Digest, -1); err != nil {
			t.Fatalf("failed janitor transaction did not restore canonical blob: %v", err)
		}
		var count int
		if err := pool.QueryRow(
			ctx,
			`SELECT COUNT(*) FROM source_artifacts WHERE digest=$1`,
			artifact.Digest,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("failed janitor transaction removed metadata")
		}
		entries, err := os.ReadDir(filesystem.quarantine)
		if err != nil || len(entries) != 0 {
			t.Fatalf("failed janitor left inconsistent quarantine: entries=%v err=%v", entries, err)
		}
	})

	t.Run("absent metadata cleanup is idempotent", func(t *testing.T) {
		digest := "sha256:" + strings.Repeat("d", 64)
		storageKey, err := StorageKey(digest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO source_artifacts (
			    digest,compressed_bytes,uncompressed_bytes,entry_count,storage_key,created_at
			 ) VALUES ($1,1,1,1,$2,$3)`,
			digest,
			storageKey,
			releasedAt.Add(-48*time.Hour),
		); err != nil {
			t.Fatal(err)
		}
		filesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		result, err := store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig)
		if err != nil {
			t.Fatal(err)
		}
		if result.MetadataRemoved < 1 {
			t.Fatalf("metadata removed=%d, want at least the absent target", result.MetadataRemoved)
		}
		var count int
		if err := pool.QueryRow(
			ctx,
			`SELECT COUNT(*) FROM source_artifacts WHERE digest=$1`,
			digest,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("absent metadata target remains after cleanup")
		}
		result, err = store.RunJanitor(ctx, filesystem, releasedAt, DefaultJanitorConfig)
		if err != nil || result.MetadataRemoved != 0 {
			t.Fatalf("idempotent result=%#v err=%v", result, err)
		}
	})
}

func dockerUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "docker") &&
		(strings.Contains(message, "not running") ||
			strings.Contains(message, "cannot connect") ||
			strings.Contains(message, "no such file") ||
			strings.Contains(message, "permission denied"))
}
