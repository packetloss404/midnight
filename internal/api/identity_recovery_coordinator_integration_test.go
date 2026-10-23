//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresIdentityRecoveryCoordinatorAtomicityAndExactReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	stack := postgresIdentityIntegrationStack(t, pool, now)
	stack.APIBearerToken = "operator-recovery-token"
	handler := NewRouterWithDataStack(stack)
	secret, code := recoveryEnrollment(t, now)
	claimBody, _ := json.Marshal(identityClaimRequest{
		Email: "recovery@example.com", DisplayName: "Recovery Owner",
		Password: "old correct horse battery staple", TOTPSecret: secret, TOTPCode: code,
		WorkspaceSlug: "recovery-platform", WorkspaceDisplayName: "Recovery Platform",
	})
	claim := performIdentityClaim(handler, claimBody, "recovery-claim")
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "recovery@example.com", Password: "old correct horse battery staple", TOTPCode: code,
	})
	if err != nil {
		t.Fatal(err)
	}
	newSecret, newCode := recoveryEnrollment(t, now)
	body, _ := json.Marshal(identityRecoveryRequest{
		Email: "recovery@example.com", Password: "new correct horse battery staple",
		TOTPSecret: newSecret, TOTPCode: newCode,
	})
	var originalHash string
	var originalGeneration int64
	if err := pool.QueryRow(ctx, `SELECT password_hash, session_generation FROM users WHERE email='recovery@example.com'`).Scan(&originalHash, &originalGeneration); err != nil {
		t.Fatal(err)
	}
	// Fail after the credential update, at the audit boundary, to prove all
	// mutations and the encrypted idempotency response roll back together.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF NEW.action='identity.administrator.credentials.reset' THEN RAISE EXCEPTION 'injected recovery audit failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	faulted := performIdentityRecovery(handler, body, "atomic-recovery", "operator-recovery-token")
	if faulted.Code != http.StatusInternalServerError {
		t.Fatalf("faulted recovery status=%d body=%s", faulted.Code, faulted.Body.String())
	}
	var hash string
	var generation int64
	var revoked, auditRows, idemRows int
	if err := pool.QueryRow(ctx, `SELECT password_hash, session_generation FROM users WHERE email='recovery@example.com'`).Scan(&hash, &generation); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM auth_sessions WHERE revoked_at IS NOT NULL),
		(SELECT count(*) FROM audit_logs WHERE action='identity.administrator.credentials.reset'),
		(SELECT count(*) FROM idempotency_keys WHERE key=$1)`, identityRecoveryIdempotencyKey("atomic-recovery")).Scan(&revoked, &auditRows, &idemRows); err != nil {
		t.Fatal(err)
	}
	if hash != originalHash || generation != originalGeneration || revoked != 0 || auditRows != 0 || idemRows != 0 {
		t.Fatalf("reset partially committed: password changed=%v generation=%d/%d revoked=%d audit=%d idempotency=%d", hash != originalHash, generation, originalGeneration, revoked, auditRows, idemRows)
	}
	if _, _, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+login.AccessToken); err != nil {
		t.Fatalf("rolled-back reset invalidated the original session: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_recovery_audit ON audit_logs; DROP FUNCTION reject_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	first := performIdentityRecovery(handler, body, "atomic-recovery", "operator-recovery-token")
	if first.Code != http.StatusOK || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reset status=%d body=%s", first.Code, first.Body.String())
	}
	replay := performIdentityRecovery(handler, body, "atomic-recovery", "operator-recovery-token")
	assertExactIdentityClaimReplay(t, first, replay)
	if err := pool.QueryRow(ctx, `SELECT password_hash, session_generation FROM users WHERE email='recovery@example.com'`).Scan(&hash, &generation); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.VerifyPassword("new correct horse battery staple", hash); err != nil {
		t.Fatalf("new password does not authenticate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM auth_sessions WHERE revoked_at IS NOT NULL),
		(SELECT count(*) FROM audit_logs WHERE action='identity.administrator.credentials.reset'),
		(SELECT count(*) FROM idempotency_keys WHERE key=$1 AND response_storage='envelope-v1' AND response_status IS NULL AND response_body IS NULL AND response_ciphertext IS NOT NULL)`, identityRecoveryIdempotencyKey("atomic-recovery")).Scan(&revoked, &auditRows, &idemRows); err != nil {
		t.Fatal(err)
	}
	if generation != originalGeneration+1 || revoked != 1 || auditRows != 1 || idemRows != 1 {
		t.Fatalf("reset/replay state generation=%d/%d revoked=%d audit=%d encrypted responses=%d", generation, originalGeneration, revoked, auditRows, idemRows)
	}
	if _, _, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+login.AccessToken); err == nil {
		t.Fatal("session issued before reset still authenticates")
	}
	if _, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "recovery@example.com", Password: "new correct horse battery staple", TOTPCode: newCode,
	}); err != nil {
		t.Fatalf("replacement credentials cannot sign in: %v", err)
	}
}
