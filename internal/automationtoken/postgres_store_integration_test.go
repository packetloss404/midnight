//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package automationtoken

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresAutomationTokenLifecycleAndScope(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
			t.Skipf("docker not available: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	runner := &dbpg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES ('prj_token', 'wrk_default', 'token-project', 'Token project');
		INSERT INTO service_accounts (
			id, workspace_id, slug, display_name
		)
		VALUES ('sa_token_ci', 'wrk_default', 'token-ci', 'Token CI');
		INSERT INTO workspaces (id, slug, display_name)
		VALUES ('wrk_other', 'other-workspace', 'Other workspace');
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES ('prj_other', 'wrk_other', 'other-project', 'Other project');
	`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(ManagerConfig{
		Store:  NewPostgresStore(pool),
		MACKey: testMACKey,
		Clock:  func() time.Time { return now },
		Random: deterministicRandom(),
	})
	if err != nil {
		t.Fatal(err)
	}
	minted, err := manager.Mint(ctx, MintRequest{
		ServiceAccountID: "sa_token_ci",
		WorkspaceID:      "wrk_default",
		ProjectID:        "prj_token",
		Name:             "production deploy",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_migration_default",
		ExpiresAt:        now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	var (
		storedHash   []byte
		storedPrefix string
	)
	if err := pool.QueryRow(ctx, `
		SELECT token_hash, token_prefix
		FROM project_automation_tokens
		WHERE id = $1`,
		minted.Token.ID,
	).Scan(&storedHash, &storedPrefix); err != nil {
		t.Fatal(err)
	}
	if len(storedHash) != 32 ||
		bytes.Equal(storedHash, []byte(minted.AccessToken)) ||
		strings.Contains(string(storedHash), minted.AccessToken) ||
		storedPrefix != minted.AccessToken[:publicPrefixSize] {
		t.Fatalf(
			"persisted credential is not hash-only: hash=%x prefix=%q",
			storedHash,
			storedPrefix,
		)
	}

	principal, err := manager.Authenticate(ctx, "Bearer "+minted.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ProjectID != "prj_token" || principal.WorkspaceID != "wrk_default" {
		t.Fatalf("principal = %#v", principal)
	}

	// A service account from another workspace cannot be rebound to this
	// project, even when every individual ID exists.
	if _, err := manager.Mint(ctx, MintRequest{
		ServiceAccountID: "sa_token_ci",
		WorkspaceID:      "wrk_other",
		ProjectID:        "prj_other",
		Name:             "cross-workspace",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_migration_default",
		ExpiresAt:        now.Add(time.Hour),
	}); err == nil {
		t.Fatal("composite service-account/workspace scope accepted a mismatch")
	}

	// A syntactically valid service-account token present only in the legacy
	// global table must remain non-authenticating.
	legacySecret := bytes.Repeat([]byte{0x7f}, tokenSecretBytes)
	legacyRaw := TokenPrefix + base64.RawURLEncoding.EncodeToString(legacySecret)
	legacyHash := hex.EncodeToString(manager.tokenMAC(legacyRaw))
	if _, err := pool.Exec(ctx, `
		INSERT INTO api_tokens (
			id, token_hash, token_class, name, expires_at
		)
		VALUES ('legacy_sa', $1, 'sa', 'legacy', $2)`,
		legacyHash,
		now.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(
		ctx,
		"Bearer "+legacyRaw,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("legacy api_tokens row authenticated: %v", err)
	}

	raceMinted, err := manager.Mint(ctx, MintRequest{
		ServiceAccountID: "sa_token_ci", WorkspaceID: "wrk_default", ProjectID: "prj_token",
		Name: "conditional race", Profile: ProfileCIDeployV1,
		CreatedByUserID: "usr_migration_default", ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := raceMinted.Token.ResourceVersion
	raceErrors := make(chan error, 2)
	var raceWait sync.WaitGroup
	for range 2 {
		raceWait.Add(1)
		go func() {
			defer raceWait.Done()
			_, revokeErr := manager.Revoke(ctx, RevokeRequest{
				TokenID: raceMinted.Token.ID, WorkspaceID: "wrk_default", ProjectID: "prj_token",
				RevokedByUserID: "usr_migration_default", ExpectedVersion: &expected,
			})
			raceErrors <- revokeErr
		}()
	}
	raceWait.Wait()
	close(raceErrors)
	successes, conflicts := 0, 0
	for raceErr := range raceErrors {
		switch {
		case raceErr == nil:
			successes++
		case errors.Is(raceErr, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("conditional revoke error=%v", raceErr)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("conditional revoke success/conflict=%d/%d", successes, conflicts)
	}
	durableRace, err := manager.Get(ctx, "wrk_default", "prj_token", raceMinted.Token.ID)
	if err != nil || durableRace.RevokedAt == nil || durableRace.ResourceVersion != 2 {
		t.Fatalf("durable conditional token=%+v err=%v", durableRace, err)
	}

	now = now.Add(time.Minute)
	revoked, err := manager.Revoke(ctx, RevokeRequest{
		TokenID:         minted.Token.ID,
		WorkspaceID:     "wrk_default",
		ProjectID:       "prj_token",
		RevokedByUserID: "usr_migration_default",
		Reason:          "rotation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == nil || revoked.ResourceVersion != 2 {
		t.Fatalf("revoked metadata = %#v", revoked)
	}
	if _, err := manager.Authenticate(
		ctx,
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token authenticated: %v", err)
	}

	if _, err := runner.Down(ctx); err == nil ||
		!strings.Contains(err.Error(), "token history exists") {
		t.Fatalf("history-bearing down migration = %v", err)
	}
}

func TestPostgresAutomationTokenRejectsSoftDeletedScope(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
			t.Skipf("docker not available: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := (&dbpg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES ('prj_deleted_scope', 'wrk_default', 'deleted-scope', 'Deleted scope');
		INSERT INTO service_accounts (id, workspace_id, slug, display_name)
		VALUES ('sa_deleted_scope', 'wrk_default', 'deleted-scope', 'Deleted scope');
	`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(ManagerConfig{
		Store:  NewPostgresStore(pool),
		MACKey: testMACKey,
		Clock:  func() time.Time { return now },
		Random: deterministicRandom(),
	})
	if err != nil {
		t.Fatal(err)
	}
	minted, err := manager.Mint(ctx, MintRequest{
		ServiceAccountID: "sa_deleted_scope",
		WorkspaceID:      "wrk_default",
		ProjectID:        "prj_deleted_scope",
		Name:             "deleted scope",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_migration_default",
		ExpiresAt:        now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE projects
		SET deleted_at = CURRENT_TIMESTAMP
		WHERE id = 'prj_deleted_scope'`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(
		ctx,
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("soft-deleted project token authenticated: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE projects SET deleted_at = NULL WHERE id = 'prj_deleted_scope';
		UPDATE service_accounts
		SET deleted_at = CURRENT_TIMESTAMP
		WHERE id = 'sa_deleted_scope'`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Authenticate(
		ctx,
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("soft-deleted service-account token authenticated: %v", err)
	}
}

func dockerUnavailable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "docker") &&
		(strings.Contains(message, "not running") ||
			strings.Contains(message, "cannot connect") ||
			strings.Contains(message, "no such file") ||
			strings.Contains(message, "permission denied"))
}
