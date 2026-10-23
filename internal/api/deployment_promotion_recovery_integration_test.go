//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestDeploymentPromotionCommittedTakesSnapshotAfterAdvisoryLock(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 29, 21, 0, 0, 0, time.UTC)
	for _, statement := range []string{
		`INSERT INTO workspaces (
			id,slug,display_name,created_at,updated_at
		) VALUES (
			'wrk_recovery_lock','recovery-lock','Recovery lock',$1,$1
		)`,
		`INSERT INTO projects (
			id,workspace_id,slug,display_name,created_at,updated_at
		) VALUES (
			'prj_recovery_lock','wrk_recovery_lock','recovery-lock',
			'Recovery lock',$1,$1
		)`,
		`INSERT INTO environments (
			id,project_id,slug,name,tier,is_protected,created_at,updated_at
		) VALUES (
			'env_recovery_lock','prj_recovery_lock','production','Production',
			'production',FALSE,$1,$1
		)`,
		`INSERT INTO services (
			id,project_id,slug,display_name,created_at,updated_at
		) VALUES (
			'svc_recovery_lock','prj_recovery_lock','api','API',$1,$1
		)`,
		`INSERT INTO operations (
			id,kind,state,actor,created_at,updated_at
		) VALUES (
			'op_recovery_lock','deploy','RUNNING','system',$1,$1
		)`,
		`INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,active,
			operation_id,trigger_key,created_at,updated_at
		) VALUES (
			'dep_recovery_lock','prj_recovery_lock','svc_recovery_lock',
			'env_recovery_lock','deploy_image','queued',FALSE,
			'op_recovery_lock','trigger:recovery-lock',$1,$1
		)`,
		`UPDATE deployments
		    SET state='deploying',updated_at=$1
		  WHERE id='dep_recovery_lock'`,
		`INSERT INTO service_environment_state (
			service_id,environment_id,active_deployment_id,
			last_deployment_seq,version,updated_at
		) VALUES (
			'svc_recovery_lock','env_recovery_lock',NULL,0,1,$1
		)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed recovery lock graph: %v", err)
		}
	}

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockerTx.Rollback(context.Background()) }()
	lockKey := "service-environment-deploy:svc_recovery_lock:env_recovery_lock"
	if _, err := blockerTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		lockKey,
	); err != nil {
		t.Fatalf("hold promotion lock: %v", err)
	}

	type proofResult struct {
		matched bool
		err     error
	}
	result := make(chan proofResult, 1)
	store := NewPostgresDeploymentStore(pool)
	go func() {
		matched, proofErr := store.DeploymentPromotionCommitted(
			ctx,
			"op_recovery_lock",
			"dep_recovery_lock",
			"",
		)
		result <- proofResult{matched: matched, err: proofErr}
	}()

	waitUntil := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM pg_locks
			 WHERE locktype='advisory'
			   AND granted=FALSE`,
		).Scan(&waiting); err != nil {
			t.Fatalf("inspect waiting advisory proof lock: %v", err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(waitUntil) {
			t.Fatal("commit recovery proof did not block on the promotion lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Commit the exact same candidate while recovery is visibly waiting. The
	// old implementation had already established its REPEATABLE READ snapshot
	// before this point and therefore returned a stale false result.
	if _, err := blockerTx.Exec(ctx, `
		UPDATE deployments
		   SET state='active',active=TRUE,updated_at=$2,completed_at=$2
		 WHERE id=$1
		   AND state='deploying'`,
		"dep_recovery_lock",
		now.Add(time.Second),
	); err != nil {
		t.Fatalf("commit concurrent candidate state: %v", err)
	}
	if _, err := blockerTx.Exec(ctx, `
		UPDATE service_environment_state
		   SET active_deployment_id=$3,
		       last_deployment_seq=last_deployment_seq+1,
		       version=version+1,
		       updated_at=$4
		 WHERE service_id=$1
		   AND environment_id=$2`,
		"svc_recovery_lock",
		"env_recovery_lock",
		"dep_recovery_lock",
		now.Add(time.Second),
	); err != nil {
		t.Fatalf("commit concurrent active pointer: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release promotion lock with winner: %v", err)
	}

	select {
	case got := <-result:
		if got.err != nil || !got.matched {
			t.Fatalf(
				"post-lock committed promotion proof=%v err=%v, want true",
				got.matched,
				got.err,
			)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("commit recovery proof did not finish after promotion lock released")
	}

	var heldSessionLocks int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_locks
		 WHERE locktype='advisory'
		   AND granted=TRUE`,
	).Scan(&heldSessionLocks); err != nil {
		t.Fatalf("inspect released advisory proof lock: %v", err)
	}
	if heldSessionLocks != 0 {
		t.Fatalf(
			"commit recovery left %d advisory locks on pooled sessions",
			heldSessionLocks,
		)
	}
}
