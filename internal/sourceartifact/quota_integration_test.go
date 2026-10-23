//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestQuotaLedgerSurvivesCascadeAndConvergesAfterFilesystemProof(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := startQuotaIntegrationDatabase(t, ctx)
	defer pool.Close()

	base := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	seedQuotaIntegrationScopes(t, ctx, pool, base)
	filesystem, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	quota := NewQuotaStore(pool, DefaultQuotaConfig)
	quota.Now = func() time.Time { return base }
	references := NewPostgresStore(pool)

	archive := quotaIntegrationArchive(t, "cascade and filesystem proof")
	staged, err := filesystem.Stage(ctx, bytes.NewReader(archive), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	request := quotaIntegrationRequest("a", "accepted-cascade", staged.Digest, staged.CompressedBytes)
	reservation, err := reserveQuotaIntegration(ctx, pool, quota, request)
	if err != nil {
		t.Fatalf("reserve accepted body: %v", err)
	}
	var artifact Artifact
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var publishErr error
		artifact, publishErr = quota.PublishInTx(
			ctx,
			tx,
			filesystem,
			staged,
			reservation.ID,
			request,
		)
		return publishErr
	}); err != nil {
		t.Fatalf("publish accepted body: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id,kind,state,created_at,updated_at)
		VALUES ('op_quota_cascade','build','QUEUED',$1,$1)`,
		base,
	); err != nil {
		t.Fatalf("seed accepted operation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,operation_id,
			created_at,updated_at
		) VALUES (
			'dep_quota_cascade','prj_quota_a','svc_quota_a','env_quota_a',
			'deploy_source','queued','op_quota_cascade',$1,$1
		)`,
		base,
	); err != nil {
		t.Fatalf("seed accepted deployment: %v", err)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := quota.ValidateInTx(ctx, tx, reservation.ID, request); err != nil {
			return err
		}
		if err := references.BindVerifiedInTx(ctx, tx, filesystem, artifact, Reference{
			OperationID:   "op_quota_cascade",
			DeploymentID:  "dep_quota_cascade",
			Digest:        artifact.Digest,
			ProjectID:     "prj_quota_a",
			ServiceID:     "svc_quota_a",
			EnvironmentID: "env_quota_a",
			CreatedAt:     base,
		}); err != nil {
			return err
		}
		if err := quota.ChargeWorkspaceDigestInTx(
			ctx,
			tx,
			"wrk_quota_a",
			artifact.Digest,
			artifact.CompressedBytes,
		); err != nil {
			return err
		}
		return quota.ConsumeInTx(ctx, tx, reservation.ID)
	}); err != nil {
		t.Fatalf("bind and transfer accepted charge: %v", err)
	}
	assertQuotaIntegrationCounts(t, ctx, pool, artifact.Digest, 1, 1, 1, 0)

	if _, err := pool.Exec(ctx, `DELETE FROM operations WHERE id='op_quota_cascade'`); err != nil {
		t.Fatalf("cascade accepted operation: %v", err)
	}
	assertQuotaIntegrationCounts(t, ctx, pool, artifact.Digest, 1, 0, 1, 0)

	sweepAt := base.Add(72 * time.Hour)
	old := base.Add(-48 * time.Hour)
	blobPath := filepath.Join(filesystem.Root(), filepath.FromSlash(artifact.StorageKey))
	if err := os.Chtimes(blobPath, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE source_artifacts SET created_at=$2 WHERE digest=$1`,
		artifact.Digest,
		old,
	); err != nil {
		t.Fatalf("age accepted metadata: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE source_artifact_workspace_charges
		   SET reconcile_after=$2
		 WHERE workspace_id='wrk_quota_a'
		   AND artifact_digest=$1`,
		artifact.Digest,
		base.Add(time.Hour),
	); err != nil {
		t.Fatalf("age accepted charge: %v", err)
	}
	janitorConfig := JanitorConfig{
		BlobRetention:       time.Hour,
		ReferenceRetention:  time.Hour,
		WorkRetention:       time.Hour,
		QuarantineRetention: time.Hour,
		MaxItems:            32,
		MaxVerifyBytes:      2 * DefaultLimits.MaxCompressedBytes,
	}
	first, err := references.RunJanitor(ctx, filesystem, sweepAt, janitorConfig)
	if err != nil {
		t.Fatal(err)
	}
	if first.BlobsQuarantined != 1 || first.MetadataRemoved != 0 {
		t.Fatalf("first janitor result=%#v, want one quarantine and retained metadata", first)
	}
	removed, err := quota.ReconcileExpired(ctx, filesystem, sweepAt, 16)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("reconciled %d charges while quarantine still owned the bytes", removed)
	}
	assertQuotaIntegrationCounts(t, ctx, pool, artifact.Digest, 1, 0, 1, 0)

	quarantineRoot := filepath.Join(filesystem.Root(), "quarantine")
	quarantines, err := os.ReadDir(quarantineRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantines) != 1 {
		t.Fatalf("quarantine entries=%d, want 1", len(quarantines))
	}
	quarantineOld := sweepAt.Add(-2 * time.Hour)
	if err := os.Chtimes(
		filepath.Join(quarantineRoot, quarantines[0].Name()),
		quarantineOld,
		quarantineOld,
	); err != nil {
		t.Fatal(err)
	}
	finalizeAt := sweepAt.Add(2 * time.Hour)
	second, err := references.RunJanitor(ctx, filesystem, finalizeAt, janitorConfig)
	if err != nil {
		t.Fatal(err)
	}
	if second.QuarantineFinalized != 1 || second.MetadataRemoved != 0 {
		t.Fatalf("second janitor result=%#v, want finalized bytes and retained metadata", second)
	}
	removed, err = quota.ReconcileExpired(ctx, filesystem, finalizeAt, 16)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("proof reconciliation removed=%d, want last workspace charge", removed)
	}
	assertQuotaIntegrationCounts(t, ctx, pool, artifact.Digest, 0, 0, 1, 0)

	third, err := references.RunJanitor(
		ctx,
		filesystem,
		finalizeAt.Add(time.Hour),
		janitorConfig,
	)
	if err != nil {
		t.Fatal(err)
	}
	if third.MetadataRemoved != 1 {
		t.Fatalf("third janitor result=%#v, want stale metadata removal", third)
	}
	assertQuotaIntegrationCounts(t, ctx, pool, artifact.Digest, 0, 0, 0, 0)
}

func TestQuotaConcurrentAdmissionFairnessAndPublicationLockOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := startQuotaIntegrationDatabase(t, ctx)
	defer pool.Close()

	clockNow := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	seedQuotaIntegrationScopes(t, ctx, pool, clockNow)
	config := DefaultQuotaConfig
	config.WorkspaceBytes = 200 << 20
	config.WorkspaceInflightBytes = 50 << 20
	config.WorkspaceInflightCount = 2
	config.GlobalInflightBytes = 70 << 20
	config.GlobalInflightCount = 3
	quota := NewQuotaStore(pool, config)
	quota.Now = func() time.Time { return clockNow }

	t.Run("renewal mints a new crash marker and proof removes it", func(t *testing.T) {
		renewalNow := clockNow
		renewalQuota := NewQuotaStore(pool, config)
		renewalQuota.Now = func() time.Time { return renewalNow }
		request := quotaIntegrationRequest(
			"a",
			"renewed-reservation",
			quotaIntegrationDigest("renewed-reservation"),
			1<<20,
		)
		first, err := reserveQuotaIntegration(ctx, pool, renewalQuota, request)
		if err != nil {
			t.Fatal(err)
		}
		renewalNow = renewalNow.Add(config.UploadLease + time.Minute)
		renewed, err := reserveQuotaIntegration(ctx, pool, renewalQuota, request)
		if err != nil {
			t.Fatal(err)
		}
		if renewed.ID == first.ID ||
			renewed.PublishedAt == nil ||
			renewed.ChargeExpiresAt.Before(first.ChargeExpiresAt) {
			t.Fatalf("unsafe renewal first=%#v renewed=%#v", first, renewed)
		}
		if err := renewalQuota.Finish(ctx, first.ID, false); err != nil {
			t.Fatal(err)
		}
		if err := renewalQuota.Finish(ctx, renewed.ID, false); err != nil {
			t.Fatal(err)
		}
		var conservativeRows int
		if err := pool.QueryRow(
			ctx,
			`SELECT COUNT(*) FROM source_artifact_upload_reservations
			  WHERE id=$1 AND published_at IS NOT NULL`,
			renewed.ID,
		).Scan(&conservativeRows); err != nil {
			t.Fatal(err)
		}
		if conservativeRows != 1 {
			t.Fatalf("renewed conservative rows=%d, want 1", conservativeRows)
		}
		emptyFilesystem, err := NewFSStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		removed, err := renewalQuota.ReconcileExpired(
			ctx,
			emptyFilesystem,
			renewed.ChargeExpiresAt.Add(time.Minute),
			1,
		)
		if err != nil || removed != 1 {
			t.Fatalf("renewal proof removed=%d err=%v, want 1", removed, err)
		}
	})

	sharedDigest := quotaIntegrationDigest("shared-across-workspaces")
	for _, workspaceID := range []string{"wrk_quota_a", "wrk_quota_b"} {
		if err := chargeQuotaIntegration(
			ctx,
			pool,
			quota,
			workspaceID,
			sharedDigest,
			40<<20,
		); err != nil {
			t.Fatal(err)
		}
	}
	var sharedCharges int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM source_artifact_workspace_charges
		  WHERE artifact_digest=$1`,
		sharedDigest,
	).Scan(&sharedCharges); err != nil {
		t.Fatal(err)
	}
	if sharedCharges != 2 {
		t.Fatalf("shared physical digest charges=%d, want one logical charge per workspace", sharedCharges)
	}

	lowQuotaConfig := config
	lowQuotaConfig.WorkspaceBytes = 80 << 20
	lowQuota := NewQuotaStore(pool, lowQuotaConfig)
	lowQuota.Now = quota.Now
	sameDigestReservation, err := reserveQuotaIntegration(
		ctx,
		pool,
		lowQuota,
		quotaIntegrationRequest("a", "same-digest", sharedDigest, 40<<20),
	)
	if err != nil {
		t.Fatalf("same-workspace digest de-duplication rejected: %v", err)
	}
	if _, err := reserveQuotaIntegration(
		ctx,
		pool,
		lowQuota,
		quotaIntegrationRequest(
			"a",
			"unique-over-logical-quota",
			quotaIntegrationDigest("unique-over-logical-quota"),
			50<<20,
		),
	); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("unique logical overage error=%v, want ErrQuotaExceeded", err)
	}
	if err := lowQuota.Finish(ctx, sameDigestReservation.ID, false); err != nil {
		t.Fatal(err)
	}

	t.Run("workspace count boundary is serialized", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 3)
		reservations := make(chan Reservation, 3)
		for index := range 3 {
			index := index
			go func() {
				<-start
				reservation, err := reserveQuotaIntegration(
					ctx,
					pool,
					quota,
					quotaIntegrationRequest(
						"a",
						fmt.Sprintf("workspace-count-%d", index),
						quotaIntegrationDigest(fmt.Sprintf("workspace-count-%d", index)),
						1<<20,
					),
				)
				if err == nil {
					reservations <- reservation
				}
				results <- err
			}()
		}
		close(start)
		var admitted int
		var rejected int
		for range 3 {
			switch err := <-results; {
			case err == nil:
				admitted++
			case errors.Is(err, ErrUploadBackpressure):
				rejected++
			default:
				t.Fatalf("workspace concurrent admission error=%v", err)
			}
		}
		close(reservations)
		if admitted != 2 || rejected != 1 {
			t.Fatalf("workspace concurrent admitted/rejected=%d/%d, want 2/1", admitted, rejected)
		}
		for reservation := range reservations {
			if err := quota.Finish(ctx, reservation.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("global count boundary is serialized", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 4)
		reservations := make(chan Reservation, 4)
		for index := range 4 {
			index := index
			scope := "a"
			if index%2 == 1 {
				scope = "b"
			}
			go func() {
				<-start
				reservation, err := reserveQuotaIntegration(
					ctx,
					pool,
					quota,
					quotaIntegrationRequest(
						scope,
						fmt.Sprintf("global-count-%d", index),
						quotaIntegrationDigest(fmt.Sprintf("global-count-%d", index)),
						1<<20,
					),
				)
				if err == nil {
					reservations <- reservation
				}
				results <- err
			}()
		}
		close(start)
		var admitted int
		var rejected int
		for range 4 {
			switch err := <-results; {
			case err == nil:
				admitted++
			case errors.Is(err, ErrUploadBackpressure):
				rejected++
			default:
				t.Fatalf("global concurrent admission error=%v", err)
			}
		}
		close(reservations)
		if admitted != 3 || rejected != 1 {
			t.Fatalf("global concurrent admitted/rejected=%d/%d, want 3/1", admitted, rejected)
		}
		for reservation := range reservations {
			if err := quota.Finish(ctx, reservation.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("global byte boundary is serialized", func(t *testing.T) {
		first, err := reserveQuotaIntegration(
			ctx,
			pool,
			quota,
			quotaIntegrationRequest(
				"a",
				"global-bytes-a",
				quotaIntegrationDigest("global-bytes-a"),
				50<<20,
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		second, err := reserveQuotaIntegration(
			ctx,
			pool,
			quota,
			quotaIntegrationRequest(
				"b",
				"global-bytes-b",
				quotaIntegrationDigest("global-bytes-b"),
				20<<20,
			),
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reserveQuotaIntegration(
			ctx,
			pool,
			quota,
			quotaIntegrationRequest(
				"b",
				"global-bytes-over",
				quotaIntegrationDigest("global-bytes-over"),
				1<<20,
			),
		); !errors.Is(err, ErrUploadBackpressure) {
			t.Fatalf("global byte overage error=%v, want ErrUploadBackpressure", err)
		}
		for _, reservation := range []Reservation{first, second} {
			if err := quota.Finish(ctx, reservation.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	})

	filesystem, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blockedArchive := quotaIntegrationArchive(t, "fairness blocker")
	blockedStage, err := filesystem.Stage(ctx, bytes.NewReader(blockedArchive), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	blockedArtifact, err := filesystem.Commit(ctx, blockedStage)
	if err != nil {
		t.Fatal(err)
	}
	absentDigest := quotaIntegrationDigest("fairness absent")
	for _, value := range []struct {
		digest string
		bytes  int64
	}{
		{digest: blockedArtifact.Digest, bytes: blockedArtifact.CompressedBytes},
		{digest: absentDigest, bytes: 1 << 20},
	} {
		if err := chargeQuotaIntegration(
			ctx,
			pool,
			quota,
			"wrk_quota_a",
			value.digest,
			value.bytes,
		); err != nil {
			t.Fatal(err)
		}
	}
	reconcileAt := clockNow.Add(30 * time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE source_artifact_workspace_charges
		   SET reconcile_after=CASE artifact_digest
		       WHEN $1 THEN $3
		       WHEN $2 THEN $4
		       ELSE reconcile_after
		   END
		 WHERE workspace_id='wrk_quota_a'
		   AND artifact_digest IN ($1,$2)`,
		blockedArtifact.Digest,
		absentDigest,
		clockNow.Add(time.Hour),
		clockNow.Add(2*time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	removed, err := quota.ReconcileExpired(ctx, filesystem, reconcileAt, 1)
	if err != nil || removed != 0 {
		t.Fatalf("blocked fairness candidate removed=%d err=%v", removed, err)
	}
	removed, err = quota.ReconcileExpired(ctx, filesystem, reconcileAt, 1)
	if err != nil || removed != 1 {
		t.Fatalf("second fairness candidate removed=%d err=%v, want absent row", removed, err)
	}
	var blockedCharges, absentCharges int
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE artifact_digest=$1),
			COUNT(*) FILTER (WHERE artifact_digest=$2)
		FROM source_artifact_workspace_charges
		WHERE workspace_id='wrk_quota_a'
		  AND artifact_digest IN ($1,$2)`,
		blockedArtifact.Digest,
		absentDigest,
	).Scan(&blockedCharges, &absentCharges); err != nil {
		t.Fatal(err)
	}
	if blockedCharges != 1 || absentCharges != 0 {
		t.Fatalf("fairness charges blocked/absent=%d/%d, want 1/0", blockedCharges, absentCharges)
	}

	t.Run("publication waits in quota-to-lifecycle order", func(t *testing.T) {
		archive := quotaIntegrationArchive(t, "publication waits on lifecycle")
		staged, err := filesystem.Stage(ctx, bytes.NewReader(archive), DefaultLimits)
		if err != nil {
			t.Fatal(err)
		}
		request := quotaIntegrationRequest(
			"a",
			"publish-lifecycle-order",
			staged.Digest,
			staged.CompressedBytes,
		)
		reservation, err := reserveQuotaIntegration(ctx, pool, quota, request)
		if err != nil {
			t.Fatal(err)
		}
		lifecycleTx, err := pool.Inner().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lifecycleTx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock($1)`,
			LifecycleAdvisoryLockKey,
		); err != nil {
			_ = lifecycleTx.Rollback(ctx)
			t.Fatal(err)
		}
		published := make(chan Artifact, 1)
		publishErrors := make(chan error, 1)
		go func() {
			var artifact Artifact
			err := pool.WithTx(ctx, func(tx *pg.Tx) error {
				var publishErr error
				artifact, publishErr = quota.PublishInTx(
					ctx,
					tx,
					filesystem,
					staged,
					reservation.ID,
					request,
				)
				return publishErr
			})
			published <- artifact
			publishErrors <- err
		}()
		waitForQuotaIntegrationLock(t, ctx, pool, QuotaAdvisoryLockKey)

		reconcileErrors := make(chan error, 1)
		go func() {
			_, reconcileErr := quota.ReconcileExpired(ctx, filesystem, clockNow, 8)
			reconcileErrors <- reconcileErr
		}()
		select {
		case err := <-reconcileErrors:
			t.Fatalf("reconciler bypassed publication quota lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := lifecycleTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-publishErrors; err != nil {
			t.Fatal(err)
		}
		artifact := <-published
		if artifact.Digest != request.ExpectedDigest {
			t.Fatalf("published digest=%q want %q", artifact.Digest, request.ExpectedDigest)
		}
		if err := <-reconcileErrors; err != nil {
			t.Fatal(err)
		}
		clockNow = clockNow.Add(time.Hour)
		if err := quota.Finish(ctx, reservation.ID, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("janitor does not invert lifecycle-to-quota order", func(t *testing.T) {
		archive := quotaIntegrationArchive(t, "publication waits on quota")
		staged, err := filesystem.Stage(ctx, bytes.NewReader(archive), DefaultLimits)
		if err != nil {
			t.Fatal(err)
		}
		request := quotaIntegrationRequest(
			"b",
			"publish-quota-order",
			staged.Digest,
			staged.CompressedBytes,
		)
		reservation, err := reserveQuotaIntegration(ctx, pool, quota, request)
		if err != nil {
			t.Fatal(err)
		}
		quotaTx, err := pool.Inner().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := quotaTx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock($1)`,
			QuotaAdvisoryLockKey,
		); err != nil {
			_ = quotaTx.Rollback(ctx)
			t.Fatal(err)
		}
		publishErrors := make(chan error, 1)
		go func() {
			publishErrors <- pool.WithTx(ctx, func(tx *pg.Tx) error {
				_, publishErr := quota.PublishInTx(
					ctx,
					tx,
					filesystem,
					staged,
					reservation.ID,
					request,
				)
				return publishErr
			})
		}()
		select {
		case err := <-publishErrors:
			t.Fatalf("publication bypassed held quota lock: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		result, err := NewPostgresStore(pool).RunJanitor(
			ctx,
			filesystem,
			clockNow,
			DefaultJanitorConfig,
		)
		if err != nil || result.Skipped {
			t.Fatalf("janitor under held quota lock result=%#v err=%v", result, err)
		}
		if err := quotaTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-publishErrors; err != nil {
			t.Fatal(err)
		}
		clockNow = clockNow.Add(time.Hour)
		if err := quota.Finish(ctx, reservation.ID, true); err != nil {
			t.Fatal(err)
		}
	})
}

func startQuotaIntegrationDatabase(t *testing.T, ctx context.Context) *pg.Pool {
	t.Helper()
	container, err := tcpostgres.Run(
		ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		if dockerUnavailable(err) {
			t.Skipf("docker unavailable: %v", err)
		}
		t.Fatalf("start Postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		pool.Close()
		t.Fatalf("migrate quota integration database: %v", err)
	}
	return pool
}

func seedQuotaIntegrationScopes(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	now time.Time,
) {
	t.Helper()
	for _, statement := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "users",
			query: `INSERT INTO users (
				id,email,display_name,session_generation,created_at,updated_at
			) VALUES
				('usr_quota_a','quota-a@example.com','Quota A',1,$1,$1),
				('usr_quota_b','quota-b@example.com','Quota B',1,$1,$1)`,
			args: []any{now},
		},
		{
			name: "workspaces",
			query: `INSERT INTO workspaces (id,slug,display_name,created_at,updated_at)
			VALUES
				('wrk_quota_a','quota-a','Quota A',$1,$1),
				('wrk_quota_b','quota-b','Quota B',$1,$1)`,
			args: []any{now},
		},
		{
			name: "projects",
			query: `INSERT INTO projects (
				id,workspace_id,slug,display_name,created_at,updated_at
			) VALUES
				('prj_quota_a','wrk_quota_a','quota-a','Quota A',$1,$1),
				('prj_quota_b','wrk_quota_b','quota-b','Quota B',$1,$1)`,
			args: []any{now},
		},
		{
			name: "services",
			query: `INSERT INTO services (
				id,project_id,slug,display_name,created_at,updated_at
			) VALUES
				('svc_quota_a','prj_quota_a','web-a','Web A',$1,$1),
				('svc_quota_b','prj_quota_b','web-b','Web B',$1,$1)`,
			args: []any{now},
		},
		{
			name: "environments",
			query: `INSERT INTO environments (
				id,project_id,slug,name,tier,created_at,updated_at
			) VALUES
				('env_quota_a','prj_quota_a','production','Production','production',$1,$1),
				('env_quota_b','prj_quota_b','production','Production','production',$1,$1)`,
			args: []any{now},
		},
		{
			name: "sessions",
			query: `INSERT INTO auth_sessions (
				id,user_id,token_hash,token_prefix,client_kind,session_generation,
				created_at,last_seen_at,expires_at,idle_expires_at
			) VALUES
				(
					'ses_quota_a','usr_quota_a',decode(repeat('11',32),'hex'),
					'quota-a','api',1,$1,$1,$2,$2
				),
				(
					'ses_quota_b','usr_quota_b',decode(repeat('22',32),'hex'),
					'quota-b','api',1,$1,$1,$2,$2
				)`,
			args: []any{now, now.Add(48 * time.Hour)},
		},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed quota %s: %v", statement.name, err)
		}
	}
}

func quotaIntegrationRequest(
	scope string,
	key string,
	digest string,
	compressedBytes int64,
) ReservationRequest {
	intent := sha256.Sum256([]byte("intent:" + scope + ":" + key))
	return ReservationRequest{
		WorkspaceID:     "wrk_quota_" + scope,
		ProjectID:       "prj_quota_" + scope,
		ServiceID:       "svc_quota_" + scope,
		EnvironmentID:   "env_quota_" + scope,
		ActorUserID:     "usr_quota_" + scope,
		SessionID:       "ses_quota_" + scope,
		IdempotencyKey:  key,
		IntentHash:      fmt.Sprintf("%x", intent),
		ExpectedDigest:  digest,
		CompressedBytes: compressedBytes,
	}
}

func reserveQuotaIntegration(
	ctx context.Context,
	pool *pg.Pool,
	quota *QuotaStore,
	request ReservationRequest,
) (Reservation, error) {
	var reservation Reservation
	err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var reserveErr error
		reservation, reserveErr = quota.ReserveInTx(ctx, tx, request)
		return reserveErr
	})
	return reservation, err
}

func chargeQuotaIntegration(
	ctx context.Context,
	pool *pg.Pool,
	quota *QuotaStore,
	workspaceID string,
	digest string,
	compressedBytes int64,
) error {
	return pool.WithTx(ctx, func(tx *pg.Tx) error {
		if err := acquireQuotaLocks(ctx, tx, workspaceID, quota.now()); err != nil {
			return err
		}
		return quota.ChargeWorkspaceDigestInTx(
			ctx,
			tx,
			workspaceID,
			digest,
			compressedBytes,
		)
	})
}

func waitForQuotaIntegrationLock(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	key int64,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var acquired bool
		if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
			return tx.QueryRow(
				ctx,
				`SELECT pg_try_advisory_xact_lock($1)`,
				key,
			).Scan(&acquired)
		}); err != nil {
			t.Fatal(err)
		}
		if !acquired {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("advisory lock %d was not observed", key)
}

func assertQuotaIntegrationCounts(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	digest string,
	wantCharges int,
	wantReferences int,
	wantMetadata int,
	wantReservations int,
) {
	t.Helper()
	var charges, references, metadata, reservations int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM source_artifact_workspace_charges
			 WHERE artifact_digest=$1),
			(SELECT COUNT(*) FROM source_artifact_refs
			 WHERE artifact_digest=$1),
			(SELECT COUNT(*) FROM source_artifacts
			 WHERE digest=$1),
			(SELECT COUNT(*) FROM source_artifact_upload_reservations
			 WHERE expected_digest=$1)`,
		digest,
	).Scan(&charges, &references, &metadata, &reservations); err != nil {
		t.Fatal(err)
	}
	if charges != wantCharges ||
		references != wantReferences ||
		metadata != wantMetadata ||
		reservations != wantReservations {
		t.Fatalf(
			"quota counts charges/references/metadata/reservations=%d/%d/%d/%d want %d/%d/%d/%d",
			charges,
			references,
			metadata,
			reservations,
			wantCharges,
			wantReferences,
			wantMetadata,
			wantReservations,
		)
	}
}

func quotaIntegrationDigest(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("sha256:%x", digest)
}

func quotaIntegrationArchive(t *testing.T, content string) []byte {
	t.Helper()
	var body bytes.Buffer
	gzipWriter := gzip.NewWriter(&body)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "README.txt",
		Mode:     0o644,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
