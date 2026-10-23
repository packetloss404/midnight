//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"sync"
	"testing"
	"time"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresWebhookAcceptanceSerializesAfterGitHubAuthorityCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	t.Cleanup(pool.Close)
	if _, err := (&dbpg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AgentHMACSecret:       []byte("webhook-fence-agent-secret"),
		AutomationTokenMACKey: []byte("webhook-fence-automation-token-key"),
		InvitationMACKey:      []byte("webhook-fence-invitation-mac-key"),
		VariableIntentMACKey:  []byte("webhook-fence-variable-intent-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
	})
	if err != nil {
		t.Fatalf("NewPostgresDataStack: %v", err)
	}
	inbox, ok := stack.WebhookInbox.(*webhook.PostgresDeliveryStore)
	if !ok || inbox.AcceptanceFence == nil {
		t.Fatalf("WebhookInbox = %T with fence=%v", stack.WebhookInbox, ok && inbox.AcceptanceFence != nil)
	}

	const installationID = int64(42)
	lockHeld := make(chan time.Time, 1)
	releaseAuthority := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseAuthority) })
	}
	defer release()
	authorityDone := make(chan error, 1)
	go func() {
		authorityDone <- pool.WithTx(ctx, func(tx *dbpg.Tx) error {
			if err := githubapp.LockInstallationInTx(ctx, tx, installationID); err != nil {
				return err
			}
			var authorityChangedAt time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).
				Scan(&authorityChangedAt); err != nil {
				return err
			}
			lockHeld <- authorityChangedAt.UTC()
			<-releaseAuthority
			return nil
		})
	}()

	var authorityChangedAt time.Time
	select {
	case authorityChangedAt = <-lockHeld:
	case <-time.After(10 * time.Second):
		t.Fatal("source-link transaction did not acquire the installation lock")
	}

	payload := []byte(`{"installation":{"id":42},"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	payloadHash := webhook.PayloadHashSHA256(payload)
	acceptDone := make(chan struct {
		result webhook.AcceptResult
		err    error
	}, 1)
	go func() {
		result, acceptErr := inbox.Accept(ctx, webhook.InboxAcceptance{
			Provider:           webhook.ProviderGitHub,
			EndpointID:         "github-app",
			ProviderDeliveryID: "delivery-authority-fence",
			EventType:          string(githubapp.EventPush),
			EventHash:          payloadHash,
			PayloadSHA256:      payloadHash,
			Payload:            payload,
			SignatureKeyID:     "github-app-current",
		})
		acceptDone <- struct {
			result webhook.AcceptResult
			err    error
		}{result: result, err: acceptErr}
	}()

	waitForBlockedWebhookAcceptance(t, ctx, pool)
	select {
	case outcome := <-acceptDone:
		t.Fatalf("accept returned before authority commit: result=%#v err=%v", outcome.result, outcome.err)
	default:
	}

	release()
	select {
	case err := <-authorityDone:
		if err != nil {
			t.Fatalf("commit source-link transaction: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("source-link transaction did not commit")
	}

	select {
	case outcome := <-acceptDone:
		if outcome.err != nil {
			t.Fatalf("accept after authority commit: %v", outcome.err)
		}
		if outcome.result.Disposition != webhook.AcceptCreated {
			t.Fatalf("accept disposition = %q, want %q", outcome.result.Disposition, webhook.AcceptCreated)
		}
		if !outcome.result.ReceivedAt.After(authorityChangedAt) {
			t.Fatalf(
				"delivery receipt %s did not order after pre-commit authority timestamp %s",
				outcome.result.ReceivedAt,
				authorityChangedAt,
			)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("accept remained blocked after authority commit")
	}
}

func waitForBlockedWebhookAcceptance(
	t *testing.T,
	ctx context.Context,
	pool *dbpg.Pool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				  FROM pg_locks
				 WHERE locktype='advisory'
				   AND NOT granted
			)`,
		).Scan(&blocked); err != nil {
			t.Fatalf("inspect blocked webhook acceptance: %v", err)
		}
		if blocked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook acceptance did not wait on the installation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
