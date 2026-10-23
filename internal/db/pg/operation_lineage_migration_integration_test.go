//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOperationDeploymentLineageMigrationAuditsEnforcesAndReverses(t *testing.T) {
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
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 44 {
		t.Fatalf("down migration 44=%#v err=%v", down, err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name
		) VALUES (
			'prj_lineage_test','wrk_default','lineage-test','Lineage Test'
		);
		INSERT INTO environments (
			id, project_id, slug, name, tier
		) VALUES (
			'env_lineage_test','prj_lineage_test','production','Production','production'
		);
		INSERT INTO services (
			id, project_id, slug, display_name
		) VALUES (
			'svc_lineage_test','prj_lineage_test','api','API'
		);
		INSERT INTO operations (
			id, kind, state
		) VALUES (
			'op_lineage_test','deploy','QUEUED'
		);
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES
			(
				'dep_lineage_one','prj_lineage_test','svc_lineage_test',
				'env_lineage_test','deploy_source','queued','op_lineage_test'
			),
			(
				'dep_lineage_two','prj_lineage_test','svc_lineage_test',
				'env_lineage_test','deploy_source','queued','op_lineage_test'
			)`); err != nil {
		t.Fatalf("seed ambiguous lineage: %v", err)
	}

	if _, err := runner.Up(ctx); err == nil ||
		!strings.Contains(err.Error(), "operation deployment lineage audit found 1 duplicate operation_id value(s)") ||
		!strings.Contains(err.Error(), "op_lineage_test") {
		t.Fatalf("ambiguous lineage migration error=%v", err)
	}
	var applied44 bool
	if err := pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=44)`,
	).Scan(&applied44); err != nil {
		t.Fatal(err)
	}
	if applied44 {
		t.Fatal("migration 44 recorded despite failed duplicate audit")
	}

	if _, err := pool.Exec(
		ctx,
		`DELETE FROM deployments WHERE id='dep_lineage_two'`,
	); err != nil {
		t.Fatal(err)
	}
	applied, err = runner.Up(ctx)
	if err != nil ||
		len(applied) != 7 ||
		applied[0].Version != 44 ||
		applied[1].Version != 45 ||
		applied[2].Version != 46 ||
		applied[3].Version != 47 ||
		applied[4].Version != 48 ||
		applied[5].Version != 49 ||
		applied[6].Version != 50 {
		t.Fatalf("migration after repair=%#v err=%v", applied, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES (
			'dep_lineage_blocked','prj_lineage_test','svc_lineage_test',
			'env_lineage_test','deploy_source','queued','op_lineage_test'
		)`); err == nil {
		t.Fatal("duplicate non-null operation_id accepted after migration 44")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES
			(
				'dep_lineage_null_one','prj_lineage_test','svc_lineage_test',
				'env_lineage_test','deploy_source','queued',NULL
			),
			(
				'dep_lineage_null_two','prj_lineage_test','svc_lineage_test',
				'env_lineage_test','deploy_source','queued',NULL
			)`); err != nil {
		t.Fatalf("multiple NULL operation_id rows rejected: %v", err)
	}

	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 50 {
		t.Fatalf("reversible down migration 50=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 49 {
		t.Fatalf("reversible down migration 49=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 48 {
		t.Fatalf("reversible down migration 48=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 47 {
		t.Fatalf("reversible down migration 47=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 46 {
		t.Fatalf("reversible down migration 46=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 45 {
		t.Fatalf("reversible down migration 45=%#v err=%v", down, err)
	}
	down, err = runner.Down(ctx)
	if err != nil || down == nil || down.Version != 44 {
		t.Fatalf("reversible down migration=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state, operation_id
		) VALUES (
			'dep_lineage_after_down','prj_lineage_test','svc_lineage_test',
			'env_lineage_test','deploy_source','queued','op_lineage_test'
		)`); err != nil {
		t.Fatalf("duplicate lineage remained constrained after down: %v", err)
	}
}
