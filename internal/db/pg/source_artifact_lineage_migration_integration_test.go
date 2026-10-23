//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
)

func TestSourceArtifactMigrationEnforcesExactLineageAndCascades(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runner := migrationRunnerThrough(t, pool, 50)
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("migrations up: %v", err)
	}
	if len(applied) == 0 || applied[len(applied)-1].Version != 50 {
		t.Fatalf("latest migration=%#v want version 50", applied)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES
			('prj_source_lineage_a','wrk_default','source-lineage-a','Source Lineage A'),
			('prj_source_lineage_b','wrk_default','source-lineage-b','Source Lineage B');

		INSERT INTO services (id, project_id, slug, display_name)
		VALUES
			('svc_source_lineage_a','prj_source_lineage_a','api','API'),
			('svc_source_lineage_alt','prj_source_lineage_a','worker','Worker'),
			('svc_source_lineage_b','prj_source_lineage_b','api','API');

		INSERT INTO environments (id, project_id, slug, name, tier)
		VALUES
			('env_source_lineage_a','prj_source_lineage_a','production','Production','production'),
			('env_source_lineage_alt','prj_source_lineage_a','staging','Staging','staging'),
			('env_source_lineage_b','prj_source_lineage_b','production','Production','production');

		INSERT INTO operations (id, kind, state)
		VALUES
			('op_source_lineage_a','build','QUEUED'),
			('op_source_lineage_alt','build','QUEUED'),
			('op_source_lineage_b','build','QUEUED');

		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES
			(
				'dep_source_lineage_a','prj_source_lineage_a','svc_source_lineage_a',
				'env_source_lineage_a','deploy_source','queued','op_source_lineage_a'
			),
			(
				'dep_source_lineage_alt','prj_source_lineage_a','svc_source_lineage_alt',
				'env_source_lineage_alt','deploy_source','queued','op_source_lineage_alt'
			),
			(
				'dep_source_lineage_b','prj_source_lineage_b','svc_source_lineage_b',
				'env_source_lineage_b','deploy_source','queued','op_source_lineage_b'
			);

		INSERT INTO source_artifacts (
			digest, compressed_bytes, uncompressed_bytes, entry_count, storage_key
		) VALUES (
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			128,
			512,
			2,
			'blobs/sha256/aa/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.tar.gz'
		)`); err != nil {
		t.Fatalf("seed exact lineage fixture: %v", err)
	}

	const insertReference = `
		INSERT INTO source_artifact_refs (
			operation_id, deployment_id, artifact_digest,
			project_id, service_id, environment_id
		) VALUES (
			$1,
			'dep_source_lineage_a',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			$2,
			$3,
			$4
		)`
	for _, test := range []struct {
		name          string
		operationID   string
		projectID     string
		serviceID     string
		environmentID string
	}{
		{
			name:          "operation",
			operationID:   "op_source_lineage_alt",
			projectID:     "prj_source_lineage_a",
			serviceID:     "svc_source_lineage_a",
			environmentID: "env_source_lineage_a",
		},
		{
			name:          "project",
			operationID:   "op_source_lineage_a",
			projectID:     "prj_source_lineage_b",
			serviceID:     "svc_source_lineage_a",
			environmentID: "env_source_lineage_a",
		},
		{
			name:          "service",
			operationID:   "op_source_lineage_a",
			projectID:     "prj_source_lineage_a",
			serviceID:     "svc_source_lineage_alt",
			environmentID: "env_source_lineage_a",
		},
		{
			name:          "environment",
			operationID:   "op_source_lineage_a",
			projectID:     "prj_source_lineage_a",
			serviceID:     "svc_source_lineage_a",
			environmentID: "env_source_lineage_alt",
		},
	} {
		t.Run("reject cross-"+test.name, func(t *testing.T) {
			if _, err := pool.Exec(
				ctx,
				insertReference,
				test.operationID,
				test.projectID,
				test.serviceID,
				test.environmentID,
			); err == nil {
				t.Fatalf("cross-%s source ownership tuple was accepted", test.name)
			}
		})
	}

	if _, err := pool.Exec(
		ctx,
		insertReference,
		"op_source_lineage_a",
		"prj_source_lineage_a",
		"svc_source_lineage_a",
		"env_source_lineage_a",
	); err != nil {
		t.Fatalf("insert exact source ownership tuple: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM deployments WHERE id='dep_source_lineage_a'`,
	); err != nil {
		t.Fatalf("delete owned deployment: %v", err)
	}
	assertSourceArtifactReferenceCount(t, ctx, pool, "op_source_lineage_a", 0)

	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, kind, state)
		VALUES ('op_source_lineage_cascade','build','QUEUED');
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES (
			'dep_source_lineage_cascade','prj_source_lineage_a','svc_source_lineage_a',
			'env_source_lineage_a','deploy_source','queued','op_source_lineage_cascade'
		);
		INSERT INTO source_artifact_refs (
			operation_id, deployment_id, artifact_digest,
			project_id, service_id, environment_id
		) VALUES (
			'op_source_lineage_cascade',
			'dep_source_lineage_cascade',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'prj_source_lineage_a',
			'svc_source_lineage_a',
			'env_source_lineage_a'
		)`); err != nil {
		t.Fatalf("seed operation-cascade fixture: %v", err)
	}
	if _, err := pool.Exec(
		ctx,
		`DELETE FROM operations WHERE id='op_source_lineage_cascade'`,
	); err != nil {
		t.Fatalf("delete owning operation: %v", err)
	}
	assertSourceArtifactReferenceCount(t, ctx, pool, "op_source_lineage_cascade", 0)
	var deploymentOperationID *string
	if err := pool.QueryRow(
		ctx,
		`SELECT operation_id FROM deployments WHERE id='dep_source_lineage_cascade'`,
	).Scan(&deploymentOperationID); err != nil {
		t.Fatal(err)
	}
	if deploymentOperationID != nil {
		t.Fatalf("deployment operation_id=%q after operation cascade, want NULL", *deploymentOperationID)
	}

	down, err := runner.Down(ctx)
	if err != nil || down == nil || down.Version != 50 {
		t.Fatalf("down migration 50=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 49 {
		t.Fatalf("down migration 49=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 48 {
		t.Fatalf("down migration 48=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 47 {
		t.Fatalf("down migration 47=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 46 {
		t.Fatalf("down migration 46=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 45 {
		t.Fatalf("down migration 45=%#v err=%v", down, err)
	}
	var lineageConstraintExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid='deployments'::regclass
			  AND conname='deployments_source_artifact_lineage_uq'
		)`).Scan(&lineageConstraintExists); err != nil {
		t.Fatal(err)
	}
	if lineageConstraintExists {
		t.Fatal("deployment source-artifact lineage constraint remained after migration down")
	}
}

func assertSourceArtifactReferenceCount(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	operationID string,
	want int,
) {
	t.Helper()
	var got int
	if err := pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM source_artifact_refs WHERE operation_id=$1`,
		operationID,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("source artifact references for %s=%d want %d", operationID, got, want)
	}
}
