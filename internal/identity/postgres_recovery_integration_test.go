//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

// TestPostgresAdministratorRecoveryResetsCredentialsAndClosesSessions proves
// the durable half of operator password recovery: the new credential lands,
// the requested TOTP disposition is honoured, every live session is closed,
// and an ineligible account is refused without being touched.
func TestPostgresAdministratorRecoveryResetsCredentialsAndClosesSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_IDENTITY_TEST_DSN")) // not-a-failure-code
	if dsn == "" {
		t.Skip("MIDNIGHT_IDENTITY_TEST_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	adminID := "usr_recover_admin_" + suffix
	memberID := "usr_recover_member_" + suffix
	disabledID := "usr_recover_disabled_" + suffix
	adminEmail := "recover-admin-" + suffix + "@example.com"
	memberEmail := "recover-member-" + suffix + "@example.com"
	disabledEmail := "recover-disabled-" + suffix + "@example.com"
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		for _, user := range []struct {
			id, email string
			admin     bool
			disabled  bool
		}{
			{adminID, adminEmail, true, false},
			{memberID, memberEmail, false, false},
			{disabledID, disabledEmail, true, true},
		} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO users(
					id, email, display_name, password_hash, totp_secret_encrypted,
					totp_enrolled_at, is_admin, is_disabled, session_generation,
					created_at, updated_at
				)
				VALUES($1,$2,$1,'original-hash','original-envelope'::bytea,$3,$4,$5,1,$3,$3)`,
				user.id, user.email, now, user.admin, user.disabled,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO auth_sessions(
				id, user_id, token_hash, token_prefix, client_kind,
				session_generation, user_agent_hash, created_at, last_seen_at,
				expires_at, idle_expires_at, revoked_reason
			)
			VALUES($1,$2,$3,'midn_user_recover','cli',1,'',$4,$4,$5,$5,'')`,
			"ses_recover_"+suffix, adminID, []byte("recover-token-hash-"+suffix),
			now, now.Add(24*time.Hour),
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_sessions WHERE user_id = ANY($1)`,
			[]string{adminID, memberID, disabledID})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = ANY($1)`,
			[]string{adminID, memberID, disabledID})
	})

	store := NewPostgresStore(pool)
	target, err := store.RecoverableAdministrator(ctx, strings.ToUpper(adminEmail))
	if err != nil {
		t.Fatalf("resolve administrator: %v", err)
	}
	if target.UserID != adminID || !target.TOTPEnrolled {
		t.Fatalf("resolved administrator = %#v", target)
	}
	for name, email := range map[string]string{
		"non-administrator": memberEmail,
		"disabled":          disabledEmail,
		"unknown":           "absent-" + suffix + "@example.com",
	} {
		if _, err := store.RecoverableAdministrator(ctx, email); !errors.Is(err, ErrAdministratorNotRecoverable) {
			t.Fatalf("%s resolution error = %v", name, err)
		}
	}

	result, err := store.ResetAdministratorCredentials(ctx, ResetAdministratorCredentialsRequest{
		UserID:             adminID,
		Email:              adminEmail,
		PasswordHash:       "replacement-hash",
		TOTPSecretEnvelope: []byte("replacement-envelope"),
		TOTPEnrolledAt:     now.Add(time.Hour),
		Now:                now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("reset administrator credentials: %v", err)
	}
	if !result.TOTPReenrolled || result.SessionsRevoked != 1 || result.User.ID != adminID {
		t.Fatalf("reset result = %#v", result)
	}

	var (
		passwordHash      string
		envelope          []byte
		generation        int64
		mustRotate        bool
		revokedReason     string
		liveSessionsAfter int
	)
	if err := pool.QueryRow(ctx, `
		SELECT password_hash, totp_secret_encrypted, session_generation, must_rotate_password
		FROM users WHERE id=$1`, adminID,
	).Scan(&passwordHash, &envelope, &generation, &mustRotate); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "replacement-hash" || string(envelope) != "replacement-envelope" ||
		generation != 2 || mustRotate {
		t.Fatalf("recovered user: hash=%q envelope=%q generation=%d must_rotate=%v",
			passwordHash, envelope, generation, mustRotate)
	}
	if err := pool.QueryRow(ctx, `
		SELECT revoked_reason, COUNT(*) FILTER (WHERE revoked_at IS NULL)
		FROM auth_sessions WHERE user_id=$1 GROUP BY revoked_reason`, adminID,
	).Scan(&revokedReason, &liveSessionsAfter); err != nil {
		t.Fatal(err)
	}
	if revokedReason != "administrator-password-reset" || liveSessionsAfter != 0 {
		t.Fatalf("sessions after reset: reason=%q live=%d", revokedReason, liveSessionsAfter)
	}

	// Preserving keeps the envelope that is already stored.
	preserved, err := store.ResetAdministratorCredentials(ctx, ResetAdministratorCredentialsRequest{
		UserID:       adminID,
		Email:        adminEmail,
		PasswordHash: "third-hash",
		PreserveTOTP: true,
		Now:          now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("preserve reset: %v", err)
	}
	if preserved.TOTPReenrolled {
		t.Fatalf("preserve reset result = %#v", preserved)
	}
	if err := pool.QueryRow(ctx, `SELECT totp_secret_encrypted FROM users WHERE id=$1`, adminID).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope) != "replacement-envelope" {
		t.Fatalf("preserved envelope = %q", envelope)
	}

	// A stale resolution — the address now belongs to a different account —
	// must not install a credential sealed against the previous user.
	if _, err := store.ResetAdministratorCredentials(ctx, ResetAdministratorCredentialsRequest{
		UserID:             memberID,
		Email:              adminEmail,
		PasswordHash:       "mismatched-hash",
		TOTPSecretEnvelope: []byte("mismatched-envelope"),
		Now:                now.Add(3 * time.Hour),
	}); !errors.Is(err, ErrAdministratorNotRecoverable) {
		t.Fatalf("mismatched-target reset error = %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, adminID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "third-hash" {
		t.Fatalf("password after refused reset = %q", passwordHash)
	}
}

// TestPostgresAdministratorRecoveryRefusesPreservingAbsentEnrollment proves a
// password-only reset cannot silently drop an administrator to one factor.
func TestPostgresAdministratorRecoveryRefusesPreservingAbsentEnrollment(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_IDENTITY_TEST_DSN")) // not-a-failure-code
	if dsn == "" {
		t.Skip("MIDNIGHT_IDENTITY_TEST_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	userID := "usr_recover_nomfa_" + suffix
	email := "recover-nomfa-" + suffix + "@example.com"
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id,email,display_name,password_hash,is_admin,session_generation,created_at,updated_at)
		VALUES($1,$2,$1,'original-hash',TRUE,1,$3,$3)`, userID, email, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
	})

	store := NewPostgresStore(pool)
	if _, err := store.ResetAdministratorCredentials(ctx, ResetAdministratorCredentialsRequest{
		UserID:       userID,
		Email:        email,
		PasswordHash: "replacement-hash",
		PreserveTOTP: true,
		Now:          now.Add(time.Hour),
	}); !errors.Is(err, ErrTOTPEnrollmentMissing) {
		t.Fatalf("preserve-without-enrollment error = %v", err)
	}
	var passwordHash string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "original-hash" {
		t.Fatalf("password after refused reset = %q", passwordHash)
	}
}
