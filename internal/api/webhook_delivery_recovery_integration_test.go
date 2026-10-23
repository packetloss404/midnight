//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresWebhookDeliveryRedriveCommitsInboxIdempotencyAndAuditAtomically(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	chain := audit.NewPostgresChain(pool)
	inbox := webhook.NewPostgresDeliveryStore(pool)
	inbox.Audit = chain
	idempotencyStore := idempotency.NewPostgresStore(pool)
	coordinator := NewPostgresWebhookDeliveryRedriveCoordinator(
		pool,
		inbox,
		idempotencyStore,
		chain,
		nil,
	)

	body := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	accepted, err := inbox.Accept(ctx, webhook.InboxAcceptance{
		Provider:           webhook.ProviderGitHub,
		EndpointID:         "github",
		ProviderDeliveryID: "delivery-atomic-redrive",
		EventType:          "push",
		EventHash:          webhook.PayloadHashSHA256(body),
		PayloadSHA256:      webhook.PayloadHashSHA256(body),
		Payload:            body,
		SignatureKeyID:     "github-app",
	})
	if err != nil {
		t.Fatalf("accept delivery: %v", err)
	}
	delivery, err := inbox.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	if delivery.ID != accepted.ID {
		t.Fatalf("claimed delivery = %q, want %q", delivery.ID, accepted.ID)
	}
	authorityAt := delivery.ReceivedAt.Add(-time.Minute).UTC()
	dispatchPayload := []byte(`{"service_id":"svc_webhook","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	if err := inbox.StorePlan(ctx, delivery, webhook.DispatchPlan{
		Targets: []webhook.DispatchTarget{{
			TargetKey:                        strings.Repeat("d", 64),
			WorkspaceID:                      "wrk_webhook",
			ProjectID:                        "prj_webhook",
			ServiceID:                        "svc_webhook",
			InstallationID:                   71,
			InstallationGrantResourceVersion: 1,
			RepositoryID:                     81,
			Repository:                       "acme/webhook",
			RepositoryGrantResourceVersion:   1,
			LinkResourceVersion:              1,
			InstallationGrantCreatedAt:       authorityAt,
			InstallationGrantUpdatedAt:       authorityAt,
			RepositoryGrantCreatedAt:         authorityAt,
			RepositoryGrantUpdatedAt:         authorityAt,
			RepositoryVerifiedAt:             authorityAt,
			LinkRepositoryVerifiedAt:         authorityAt,
			LinkCreatedAt:                    authorityAt,
			LinkUpdatedAt:                    authorityAt,
			Branch:                           "main",
			Ref:                              "refs/heads/main",
			CommitSHA:                        strings.Repeat("a", 40),
			CloneURL:                         "https://github.com/acme/webhook.git",
			TriggerReason:                    "github_push",
			Payload:                          dispatchPayload,
		}},
	}); err != nil {
		t.Fatalf("store plan: %v", err)
	}
	target, err := inbox.ClaimTarget(ctx, delivery)
	if err != nil {
		t.Fatalf("claim target: %v", err)
	}
	if err := inbox.DeadLetterTarget(
		ctx,
		delivery,
		target,
		"WEBHOOK_TARGET_DISPATCH_FAILED",
		errors.New("synthetic terminal failure"),
	); err != nil {
		t.Fatalf("dead-letter target: %v", err)
	}
	if err := inbox.AggregateDelivery(ctx, delivery); err != nil {
		t.Fatalf("aggregate dead-letter delivery: %v", err)
	}

	request := WebhookDeliveryRedriveRequest{
		DeliveryID: accepted.ID,
		ProjectID:  "prj_webhook",
		Key:        idempotency.Key("atomic-webhook-redrive"),
		Actor:      idempotency.ActorRecoveryOperator,
		Tenant:     idempotency.TenantInstallation,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       "/api/v1alpha/webhook-deliveries/{delivery_id}/redrive",
			BodySHA256: idempotency.BodyHashSHA256(nil),
		},
		RequestID: "req_atomic_webhook_redrive",
		SourceIP:  "127.0.0.1:8443",
		UserAgent: "midnight-integration-test",
	}

	// Corrupting a dead target's opaque dispatch bytes must reject the
	// recovery before any inbox/idempotency/audit state can commit.
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET dispatch_payload=$2
		 WHERE id=$1`,
		target.ID, []byte("tampered"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RedriveWebhookDelivery(ctx, request); !errors.Is(err, webhook.ErrInboxPayloadCorrupt) {
		t.Fatalf("corrupt target redrive error = %v, want ErrInboxPayloadCorrupt", err)
	}
	assertWebhookRedriveDatabaseState(t, ctx, pool, accepted.ID, target.ID, "dead_letter", 0, 0)
	assertWebhookRedriveIdempotencyCount(t, ctx, pool, request.Key, 0)
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET dispatch_payload=$2
		 WHERE id=$1`,
		target.ID, dispatchPayload,
	); err != nil {
		t.Fatal(err)
	}

	// Force the last transaction step to fail. The preceding inbox reset and
	// idempotency completion must roll back with the rejected audit insert.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION fail_webhook_redrive_audit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'webhook.delivery.redrive' THEN
				RAISE EXCEPTION 'synthetic webhook redrive audit failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_webhook_redrive_audit_trigger
		BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION fail_webhook_redrive_audit()`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RedriveWebhookDelivery(ctx, request); err == nil {
		t.Fatal("redrive with failing audit returned nil")
	}
	assertWebhookRedriveDatabaseState(t, ctx, pool, accepted.ID, target.ID, "dead_letter", 0, 0)
	assertWebhookRedriveIdempotencyCount(t, ctx, pool, request.Key, 0)
	if _, err := pool.Exec(ctx, `
		DROP TRIGGER fail_webhook_redrive_audit_trigger ON audit_logs;
		DROP FUNCTION fail_webhook_redrive_audit()`,
	); err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.RedriveWebhookDelivery(ctx, request)
	if err != nil {
		t.Fatalf("successful redrive: %v", err)
	}
	if first.Replayed || first.Response.StatusCode != http.StatusAccepted {
		t.Fatalf("first result = %#v", first)
	}
	assertWebhookRecoveryResponseIsMetadataOnly(t, string(first.Response.Body))
	assertWebhookRedriveDatabaseState(t, ctx, pool, accepted.ID, target.ID, "pending", 1, 1)
	assertWebhookRedriveIdempotencyCount(t, ctx, pool, request.Key, 1)

	replay, err := coordinator.RedriveWebhookDelivery(ctx, request)
	if err != nil {
		t.Fatalf("replay redrive: %v", err)
	}
	if !replay.Replayed ||
		replay.Response.StatusCode != first.Response.StatusCode ||
		string(replay.Response.Body) != string(first.Response.Body) {
		t.Fatalf("replay = %#v, want exact stored response", replay)
	}
	assertWebhookRedriveDatabaseState(t, ctx, pool, accepted.ID, target.ID, "pending", 1, 1)

	different := request
	different.Fingerprint.BodySHA256 = idempotency.BodyHashSHA256([]byte("different"))
	if _, err := coordinator.RedriveWebhookDelivery(ctx, different); !errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		t.Fatalf("different fingerprint error = %v, want body conflict", err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM audit_logs
		 WHERE action='webhook.delivery.redrive'
		   AND resource_id=$1`,
		accepted.ID,
	).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("redrive audit rows = %d, want 1", auditCount)
	}
}

func assertWebhookRedriveDatabaseState(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	deliveryID string,
	targetID string,
	wantState string,
	wantDeliveryRetries int,
	wantTargetRetries int,
) {
	t.Helper()
	var deliveryState string
	var deliveryRetries int
	if err := pool.QueryRow(ctx, `
		SELECT state,manual_retries
		  FROM webhook_deliveries
		 WHERE id=$1`,
		deliveryID,
	).Scan(&deliveryState, &deliveryRetries); err != nil {
		t.Fatal(err)
	}
	var targetState string
	var targetRetries int
	if err := pool.QueryRow(ctx, `
		SELECT state,manual_retries
		  FROM webhook_delivery_targets
		 WHERE id=$1`,
		targetID,
	).Scan(&targetState, &targetRetries); err != nil {
		t.Fatal(err)
	}
	if deliveryState != wantState ||
		targetState != wantState ||
		deliveryRetries != wantDeliveryRetries ||
		targetRetries != wantTargetRetries {
		t.Fatalf(
			"delivery=(%s,%d) target=(%s,%d), want state=%s retries=(%d,%d)",
			deliveryState,
			deliveryRetries,
			targetState,
			targetRetries,
			wantState,
			wantDeliveryRetries,
			wantTargetRetries,
		)
	}
}

func assertWebhookRedriveIdempotencyCount(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	key idempotency.Key,
	want int,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM idempotency_keys
		 WHERE key=$1 AND state <> 'EXPIRED'`,
		string(key),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("active idempotency rows for %q = %d, want %d", key, count, want)
	}
}
