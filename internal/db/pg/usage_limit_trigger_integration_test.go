//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

const usageLimitExceededMarker = "MIDNIGHT_USAGE_LIMIT_EXCEEDED" // not-a-failure-code

func TestUsageLimitTriggerScopeRepair(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	now := time.Now().UTC()
	for _, statement := range []string{
		`INSERT INTO workspaces (id,slug,display_name,created_at,updated_at) VALUES
			('wrk_limit_a','limit-a','Limit A',$1,$1),
			('wrk_limit_b','limit-b','Limit B',$1,$1)`,
		`INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at) VALUES
			('prj_limit_a','wrk_limit_a','limit-a','Limit A',$1,$1),
			('prj_limit_b','wrk_limit_b','limit-b','Limit B',$1,$1)`,
		`INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
			VALUES ('svc_limit_a','prj_limit_a','web','Web',$1,$1)`,
		`INSERT INTO environments (id,project_id,slug,name,tier,created_at,updated_at)
			VALUES ('env_limit_a','prj_limit_a','production','Production','production',$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed usage-limit scope: %v", err)
		}
	}

	// These resource-store-shaped updates raised SQLSTATE 42703 after migration
	// 0059 because its shared trigger referenced project-only OLD.workspace_id.
	if _, err := pool.Exec(ctx, `UPDATE services SET display_name='Web renamed', updated_at=$1 WHERE id='svc_limit_a'`, now.Add(time.Second)); err != nil {
		t.Fatalf("update service through shared count trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE environments SET name='Production renamed', updated_at=$1 WHERE id='env_limit_a'`, now.Add(time.Second)); err != nil {
		t.Fatalf("update environment through shared count trigger: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE usage_policy_limits SET limit_value=1,enforceable=TRUE WHERE profile_key='self_hosted' AND dimension='service_count'`); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, `INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
		VALUES ('svc_limit_denied','prj_limit_a','denied','Denied',$1,$1)`, now)
	if err == nil || !strings.Contains(err.Error(), usageLimitExceededMarker) {
		t.Fatalf("second active service error = %v, want usage-limit denial", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE services SET deleted_at=$1,updated_at=$1 WHERE id='svc_limit_a'`, now.Add(2*time.Second)); err != nil {
		t.Fatalf("soft-delete service: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
		VALUES ('svc_limit_b','prj_limit_a','worker','Worker',$1,$1)`, now); err != nil {
		t.Fatalf("replace soft-deleted service: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE services SET deleted_at=NULL,updated_at=$1 WHERE id='svc_limit_a'`, now.Add(3*time.Second))
	if err == nil || !strings.Contains(err.Error(), usageLimitExceededMarker) {
		t.Fatalf("restore service error = %v, want usage-limit denial", err)
	}

	// Moving an active row into an empty workspace is admitted and counted in
	// the destination, proving OLD and NEW project scopes are still compared.
	if _, err := pool.Exec(ctx, `UPDATE services SET project_id='prj_limit_b',updated_at=$1 WHERE id='svc_limit_b'`, now.Add(4*time.Second)); err != nil {
		t.Fatalf("move service across workspace scope: %v", err)
	}
	_, err = pool.Exec(ctx, `UPDATE services SET deleted_at=NULL,project_id='prj_limit_b',updated_at=$1 WHERE id='svc_limit_a'`, now.Add(5*time.Second))
	if err == nil || !strings.Contains(err.Error(), usageLimitExceededMarker) {
		t.Fatalf("move restored service into full workspace error = %v, want usage-limit denial", err)
	}
}
