//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresIdentityClaimCoordinatorAtomicityAndExactReplay(t *testing.T) {
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
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	stack := postgresIdentityIntegrationStack(t, pool, now)
	stack.APIBearerToken = "operator-recovery-token"
	claimCoordinator, ok := stack.IdentityClaim.(*PostgresIdentityClaimCoordinator)
	if !ok {
		t.Fatalf("IdentityClaim = %T, want *PostgresIdentityClaimCoordinator", stack.IdentityClaim)
	}
	handler := NewRouterWithDataStack(stack)

	secretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := bootstrap.DecodeTOTPSecret(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	code, err := bootstrap.GenerateTOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(identityClaimRequest{
		Email:                "atomic-owner@example.com",
		DisplayName:          "Atomic Owner",
		Password:             "atomic correct horse battery staple",
		TOTPSecret:           secretBase32,
		TOTPCode:             code,
		WorkspaceSlug:        "atomic-platform",
		WorkspaceDisplayName: "Atomic Platform",
	})
	if err != nil {
		t.Fatal(err)
	}

	var workspaceSlugBefore string
	if err := pool.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id=$1`, identity.DefaultWorkspaceID).Scan(&workspaceSlugBefore); err != nil {
		t.Fatal(err)
	}
	var migrationMembershipsBefore int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM workspace_members WHERE user_id='usr_migration_default') +
			(SELECT COUNT(*) FROM project_members WHERE user_id='usr_migration_default')`,
	).Scan(&migrationMembershipsBefore); err != nil {
		t.Fatal(err)
	}
	claimCoordinator.setBeforeCommitForTest(func() error {
		return errors.New("injected identity claim pre-commit failure")
	})
	faulted := performIdentityClaim(handler, body, "atomic-claim")
	claimCoordinator.setBeforeCommitForTest(nil)
	if faulted.Code != http.StatusInternalServerError {
		t.Fatalf("faulted claim status=%d body=%s", faulted.Code, faulted.Body.String())
	}

	var partialUsers, partialAudit, partialIdempotency, migrationMembershipsAfter int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE email='atomic-owner@example.com'`).Scan(&partialUsers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action='identity.default-workspace.claim'`).Scan(&partialAudit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM idempotency_keys WHERE key=$1`, identityClaimIdempotencyKey("atomic-claim")).Scan(&partialIdempotency); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM workspace_members WHERE user_id='usr_migration_default') +
			(SELECT COUNT(*) FROM project_members WHERE user_id='usr_migration_default')`,
	).Scan(&migrationMembershipsAfter); err != nil {
		t.Fatal(err)
	}
	var workspaceSlugAfter string
	if err := pool.QueryRow(ctx, `SELECT slug FROM workspaces WHERE id=$1`, identity.DefaultWorkspaceID).Scan(&workspaceSlugAfter); err != nil {
		t.Fatal(err)
	}
	if partialUsers != 0 || partialAudit != 0 || partialIdempotency != 0 ||
		workspaceSlugAfter != workspaceSlugBefore || migrationMembershipsAfter != migrationMembershipsBefore {
		t.Fatalf(
			"faulted claim partially committed users=%d audit=%d idempotency=%d slug=%q/%q migration_memberships=%d/%d",
			partialUsers,
			partialAudit,
			partialIdempotency,
			workspaceSlugBefore,
			workspaceSlugAfter,
			migrationMembershipsBefore,
			migrationMembershipsAfter,
		)
	}

	first := performIdentityClaim(handler, body, "atomic-claim")
	if first.Code != http.StatusCreated || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("claim status=%d cache=%q body=%s", first.Code, first.Header().Get("Cache-Control"), first.Body.String())
	}
	replay := performIdentityClaim(handler, body, "atomic-claim")
	assertExactIdentityClaimReplay(t, first, replay)

	var responseStorage string
	var responseStatus *int
	var responseBody, responseCiphertext []byte
	if err := pool.QueryRow(ctx, `
		SELECT response_storage, response_status, response_body, response_ciphertext
		FROM idempotency_keys
		WHERE key=$1`,
		identityClaimIdempotencyKey("atomic-claim"),
	).Scan(&responseStorage, &responseStatus, &responseBody, &responseCiphertext); err != nil {
		t.Fatal(err)
	}
	if responseStorage != "envelope-v1" || responseStatus != nil || responseBody != nil || len(responseCiphertext) == 0 {
		t.Fatalf(
			"claim response storage=%q status=%v plaintext=%d ciphertext=%d, want encrypted-only terminal response",
			responseStorage,
			responseStatus,
			len(responseBody),
			len(responseCiphertext),
		)
	}

	conflict := performIdentityClaim(handler, body, "atomic-claim-conflict")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("claim conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	conflictReplay := performIdentityClaim(handler, body, "atomic-claim-conflict")
	assertExactIdentityClaimReplay(t, conflict, conflictReplay)

	var users, allowedAudit, deniedAudit, encryptedAttempts int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE email='atomic-owner@example.com'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action='identity.default-workspace.claim'`).Scan(&allowedAudit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action='identity.default-workspace.claim.denied'`).Scan(&deniedAudit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM idempotency_keys
		WHERE key IN ($1, $2)
		  AND response_storage='envelope-v1'
		  AND response_status IS NULL
		  AND response_body IS NULL
		  AND response_ciphertext IS NOT NULL`,
		identityClaimIdempotencyKey("atomic-claim"),
		identityClaimIdempotencyKey("atomic-claim-conflict"),
	).Scan(&encryptedAttempts); err != nil {
		t.Fatal(err)
	}
	if users != 1 || allowedAudit != 1 || deniedAudit != 1 || encryptedAttempts != 2 {
		t.Fatalf(
			"terminal claim state users=%d allowed_audit=%d denied_audit=%d encrypted_attempts=%d",
			users,
			allowedAudit,
			deniedAudit,
			encryptedAttempts,
		)
	}
}

func assertExactIdentityClaimReplay(t *testing.T, first, replay *httptest.ResponseRecorder) {
	t.Helper()
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() ||
		replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") ||
		replay.Header().Get("Cache-Control") != first.Header().Get("Cache-Control") ||
		replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"identity claim replay=%d/%q/%q/%q/%s want exact %d/%q/%q/%s",
			replay.Code,
			replay.Header().Get("Idempotent-Replayed"),
			replay.Header().Get("Content-Type"),
			replay.Header().Get("Cache-Control"),
			replay.Body.String(),
			first.Code,
			first.Header().Get("Content-Type"),
			first.Header().Get("Cache-Control"),
			first.Body.String(),
		)
	}
}
