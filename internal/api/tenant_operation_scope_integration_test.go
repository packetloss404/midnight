//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresTenantOperationAuthorizerRequiresOneExactCanonicalDeployment(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, session_generation, created_at, updated_at
		) VALUES (
			'usr_scope_test','scope-test@example.com','Scope Test',1,
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO workspaces (
			id, slug, display_name, created_at, updated_at
		) VALUES (
			'wrk_scope_test','scope-test','Scope Test',
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES (
			'wrk_scope_test','usr_scope_test','member',
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,1
		);
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES (
			'prj_scope_test','wrk_scope_test','app','App',
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted,
			created_at, updated_at
		) VALUES (
			'env_scope_test','prj_scope_test','production','Production',
			'production',FALSE,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES (
			'svc_scope_test','prj_scope_test','api','API',
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO operations (
			id, kind, state, created_at, updated_at
		) VALUES (
			'op_scope_test','deploy','RUNNING',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state,
			operation_id, created_at, updated_at
		) VALUES (
			'dep_scope_test','prj_scope_test','svc_scope_test','env_scope_test',
			'deploy_source','queued','op_scope_test',
			CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		INSERT INTO auth_sessions (
			id, user_id, token_hash, token_prefix, client_kind,
			session_generation, created_at, last_seen_at, expires_at,
			idle_expires_at
		) VALUES (
			'ses_scope_test','usr_scope_test',decode(repeat('ab',32),'hex'),
			'midn_scope','dashboard',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP,
			CURRENT_TIMESTAMP + INTERVAL '1 hour',
			CURRENT_TIMESTAMP + INTERVAL '1 hour'
		)`); err != nil {
		t.Fatalf("seed operation scope: %v", err)
	}

	operations := NewPostgresOperationStore(pool)
	deployments := NewPostgresDeploymentStore(pool)
	access := identity.NewPostgresStore(pool)
	authorizer, ok := newPostgresTenantOperationAuthorizer(
		operations,
		deployments,
		access,
	)
	if !ok {
		t.Fatal("postgres tenant operation authorizer was not constructed")
	}
	principal := identity.Principal{
		Kind:      "user",
		UserID:    "usr_scope_test",
		SessionID: "ses_scope_test",
	}
	session := identity.Session{
		ID:     "ses_scope_test",
		UserID: "usr_scope_test",
	}
	for _, capability := range []identity.Capability{
		identity.CapabilityServiceRead,
		identity.CapabilityLogsRead,
	} {
		scope, err := authorizer.AuthorizeOperation(
			ctx,
			principal,
			session,
			orailtypes.ID("op_scope_test"),
			capability,
			now,
		)
		if err != nil {
			t.Fatalf("authorize %q: %v", capability, err)
		}
		want := tenantOperationScope{
			OperationID:   "op_scope_test",
			DeploymentID:  "dep_scope_test",
			ServiceID:     "svc_scope_test",
			ProjectID:     "prj_scope_test",
			EnvironmentID: "env_scope_test",
		}
		if scope != want {
			t.Fatalf("scope=%#v want=%#v", scope, want)
		}
	}

	// Migration 0044 prevents this state. Drop the test database's index so the
	// application-level fail-closed behavior remains proven for a legacy or
	// tampered schema.
	if _, err := pool.Exec(ctx, `DROP INDEX deployments_operation_id_uniq`); err != nil {
		t.Fatalf("drop operation lineage index: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action, state,
			operation_id, created_at, updated_at
		) VALUES (
			'dep_scope_ambiguous','prj_scope_test','svc_scope_test',
			'env_scope_test','deploy_source','queued','op_scope_test',$1,$1
		)`,
		now.Add(time.Minute),
	); err != nil {
		t.Fatalf("inject ambiguous operation scope: %v", err)
	}
	if scope, err := authorizer.AuthorizeOperation(
		ctx,
		principal,
		session,
		orailtypes.ID("op_scope_test"),
		identity.CapabilityLogsRead,
		now,
	); !errors.Is(err, ErrOperationNotFound) || scope.valid() {
		t.Fatalf("ambiguous scope=%#v error=%v, want concealed denial", scope, err)
	}
}
