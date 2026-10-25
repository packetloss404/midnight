//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresDeliveryStoreLeaseFencingAndSchemaAlignment(t *testing.T) {
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
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		if webhookDockerUnavailable(err) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	applied, err := (&dbpg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx)
	if err != nil {
		t.Fatalf("migrations up: %v", err)
	}
	foundWebhookInboxMigration := false
	for _, migration := range applied {
		if migration.Version == 50 && migration.Name == "durable_webhook_inbox" {
			foundWebhookInboxMigration = true
			break
		}
	}
	if !foundWebhookInboxMigration {
		t.Fatalf("applied migrations do not contain durable webhook inbox v50: %#v", applied)
	}

	idSequence := 0
	store := NewPostgresDeliveryStore(pool)
	store.LeaseTime = 5 * time.Second
	store.IDFactory = func(prefix string) string {
		idSequence++
		return fmt.Sprintf("%s_%03d", prefix, idSequence)
	}

	t.Run("claimed target preserves snapshot and fences expired owners", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "delivery-fencing")
		target := validIntegrationDispatchTarget(delivery.ReceivedAt)
		if err := store.StorePlan(ctx, delivery, DispatchPlan{Targets: []DispatchTarget{target}}); err != nil {
			t.Fatalf("store target plan: %v", err)
		}

		// Pending result identifiers are NULL in PostgreSQL. Operator reads and
		// claimed-target scans must normalize them instead of failing.
		detail, err := store.GetDelivery(ctx, delivery.ID)
		if err != nil {
			t.Fatalf("get pending delivery: %v", err)
		}
		if len(detail.Targets) != 1 ||
			detail.Targets[0].OperationID != "" ||
			detail.Targets[0].DeploymentID != "" ||
			detail.Targets[0].StagedOutboxID != "" {
			t.Fatalf("pending target detail = %#v", detail.Targets)
		}

		claimed, err := store.ClaimTarget(ctx, delivery)
		if err != nil {
			t.Fatalf("claim target: %v", err)
		}
		gotSnapshot := claimed.DispatchSnapshot()
		if gotSnapshot.Ref != target.Ref ||
			gotSnapshot.CloneURL != target.CloneURL ||
			gotSnapshot.RootDirectory != target.RootDirectory ||
			gotSnapshot.ConfigPath != target.ConfigPath ||
			gotSnapshot.TriggerReason != target.TriggerReason ||
			!gotSnapshot.RepositoryVerifiedAt.Equal(target.RepositoryVerifiedAt) ||
			!gotSnapshot.LinkRepositoryVerifiedAt.Equal(target.LinkRepositoryVerifiedAt) {
			t.Fatalf("claimed target snapshot = %#v", gotSnapshot)
		}
		if err := store.RenewTargetLease(ctx, delivery, claimed); err != nil {
			t.Fatalf("renew target lease: %v", err)
		}
		staleToken := claimed
		staleToken.LeaseToken = "stale-target-token"
		if err := store.CompleteTarget(ctx, delivery, staleToken, TargetResult{}); !errors.Is(err, ErrInboxLeaseLost) {
			t.Fatalf("stale target completion = %v", err)
		}

		if _, err := pool.Exec(ctx, `
			UPDATE webhook_delivery_targets
			   SET updated_at=created_at,
			       lease_expires_at=created_at+INTERVAL '1 microsecond'
			 WHERE id=$1`,
			claimed.ID,
		); err != nil {
			t.Fatalf("expire target lease: %v", err)
		}
		if err := store.CompleteTarget(ctx, delivery, claimed, TargetResult{}); !errors.Is(err, ErrInboxLeaseLost) {
			t.Fatalf("expired target completion = %v", err)
		}
		reclaimed, err := store.ClaimTarget(ctx, delivery)
		if err != nil {
			t.Fatalf("reclaim target: %v", err)
		}
		if reclaimed.LeaseToken == claimed.LeaseToken || reclaimed.Attempt != claimed.Attempt+1 {
			t.Fatalf("reclaimed target = %#v, first = %#v", reclaimed, claimed)
		}
		result := TargetResult{
			OperationID:    "op_target",
			DeploymentID:   "dep_target",
			StagedOutboxID: "wox_target",
		}
		if err := store.CompleteTarget(ctx, delivery, reclaimed, result); err != nil {
			t.Fatalf("complete reclaimed target: %v", err)
		}

		if _, err := pool.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET route_attempt=max_route_attempts,
			       updated_at=received_at,
			       lease_expires_at=received_at+INTERVAL '1 microsecond'
			 WHERE id=$1`,
			delivery.ID,
		); err != nil {
			t.Fatalf("expire delivery lease: %v", err)
		}
		// A provider replay after a crashed/expired claim must remain a valid
		// replay and must not violate the lease timestamp constraint.
		replay, err := store.Accept(ctx, integrationAcceptance("delivery-fencing"))
		if err != nil || replay.Disposition != AcceptReplayed {
			t.Fatalf("replay expired delivery = %#v, %v", replay, err)
		}
		if err := store.RenewDeliveryLease(ctx, delivery); !errors.Is(err, ErrInboxLeaseLost) {
			t.Fatalf("expired delivery renewal = %v", err)
		}
		reclaimedDelivery, err := store.ClaimDelivery(ctx)
		if err != nil {
			t.Fatalf("reclaim delivery: %v", err)
		}
		if reclaimedDelivery.ID != delivery.ID ||
			reclaimedDelivery.LeaseToken == delivery.LeaseToken ||
			reclaimedDelivery.RouteAttempt != reclaimedDelivery.MaxRouteAttempts {
			t.Fatalf("reclaimed delivery = %#v, first = %#v", reclaimedDelivery, delivery)
		}
		if err := store.RenewDeliveryLease(ctx, reclaimedDelivery); err != nil {
			t.Fatalf("renew replacement delivery: %v", err)
		}
		if err := store.AggregateDelivery(ctx, reclaimedDelivery); err != nil {
			t.Fatalf("aggregate delivery: %v", err)
		}
		detail, err = store.GetDelivery(ctx, delivery.ID)
		if err != nil || detail.State != InboxSucceeded ||
			len(detail.Targets) != 1 ||
			detail.Targets[0].DeploymentID != result.DeploymentID {
			t.Fatalf("terminal delivery = %#v, %v", detail, err)
		}

		conflict := integrationAcceptance("delivery-fencing")
		conflict.EventType = "ping"
		if _, err := store.Accept(ctx, conflict); !errors.Is(err, ErrDeliveryIdentityReuse) {
			t.Fatalf("event-type identity reuse = %v", err)
		}
	})

	t.Run("expired final delivery attempt becomes dead letter", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "delivery-exhausted")
		if _, err := pool.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET route_attempt=max_route_attempts,
			       updated_at=received_at,
			       lease_expires_at=received_at+INTERVAL '1 microsecond'
			 WHERE id=$1`,
			delivery.ID,
		); err != nil {
			t.Fatalf("expire exhausted delivery: %v", err)
		}
		if _, err := store.ClaimDelivery(ctx); !errors.Is(err, ErrInboxNoWork) {
			t.Fatalf("claim after exhausted delivery = %v", err)
		}
		detail, err := store.GetDelivery(ctx, delivery.ID)
		if err != nil ||
			detail.State != InboxDeadLetter ||
			detail.ReasonCode != "WEBHOOK_ROUTE_ATTEMPTS_EXHAUSTED" {
			t.Fatalf("exhausted delivery = %#v, %v", detail, err)
		}
	})

	t.Run("expired final target attempt becomes dead letter", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "target-exhausted")
		if err := store.StorePlan(
			ctx,
			delivery,
			DispatchPlan{Targets: []DispatchTarget{validIntegrationDispatchTarget(delivery.ReceivedAt)}},
		); err != nil {
			t.Fatalf("store exhausted target plan: %v", err)
		}
		target, err := store.ClaimTarget(ctx, delivery)
		if err != nil {
			t.Fatalf("claim exhausted target: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE webhook_delivery_targets
			   SET attempt=max_attempts,
			       updated_at=created_at,
			       lease_expires_at=created_at+INTERVAL '1 microsecond'
			 WHERE id=$1`,
			target.ID,
		); err != nil {
			t.Fatalf("expire exhausted target: %v", err)
		}
		if _, err := store.ClaimTarget(ctx, delivery); !errors.Is(err, ErrInboxNoWork) {
			t.Fatalf("claim after exhausted target = %v", err)
		}
		detail, err := store.GetDelivery(ctx, delivery.ID)
		if err != nil ||
			len(detail.Targets) != 1 ||
			detail.Targets[0].State != InboxDeadLetter ||
			detail.Targets[0].ReasonCode != "WEBHOOK_TARGET_ATTEMPTS_EXHAUSTED" {
			t.Fatalf("exhausted target = %#v, %v", detail.Targets, err)
		}
		if err := store.AggregateDelivery(ctx, delivery); err != nil {
			t.Fatalf("aggregate exhausted target: %v", err)
		}
	})

	t.Run("corrupt delivery payload is atomically dead lettered", func(t *testing.T) {
		result, err := store.Accept(ctx, integrationAcceptance("delivery-corrupt"))
		if err != nil {
			t.Fatalf("accept corrupt delivery fixture: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET payload=repeat('x',payload_bytes::INTEGER)::BYTEA
			 WHERE id=$1`,
			result.ID,
		); err != nil {
			t.Fatalf("corrupt delivery payload: %v", err)
		}
		if _, err := store.ClaimDelivery(ctx); !errors.Is(err, ErrInboxPayloadCorrupt) {
			t.Fatalf("claim corrupt delivery = %v", err)
		}
		detail, err := store.GetDelivery(ctx, result.ID)
		if err != nil ||
			detail.State != InboxDeadLetter ||
			detail.ReasonCode != "WEBHOOK_PAYLOAD_CORRUPT" {
			t.Fatalf("corrupt delivery = %#v, %v", detail, err)
		}
		var leaseCleared bool
		if err := pool.QueryRow(ctx, `
			SELECT lease_token IS NULL AND lease_expires_at IS NULL
			  FROM webhook_deliveries
			 WHERE id=$1`,
			result.ID,
		).Scan(&leaseCleared); err != nil || !leaseCleared {
			t.Fatalf("corrupt delivery lease cleared=%v, error=%v", leaseCleared, err)
		}
	})

	t.Run("corrupt target payload is atomically dead lettered", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "target-corrupt")
		if err := store.StorePlan(
			ctx,
			delivery,
			DispatchPlan{Targets: []DispatchTarget{validIntegrationDispatchTarget(delivery.ReceivedAt)}},
		); err != nil {
			t.Fatalf("store corrupt target fixture: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE webhook_delivery_targets
			   SET dispatch_payload=repeat('x',OCTET_LENGTH(dispatch_payload))::BYTEA
			 WHERE delivery_id=$1`,
			delivery.ID,
		); err != nil {
			t.Fatalf("corrupt target payload: %v", err)
		}
		if _, err := store.ClaimTarget(ctx, delivery); !errors.Is(err, ErrInboxPayloadCorrupt) {
			t.Fatalf("claim corrupt target = %v", err)
		}
		detail, err := store.GetDelivery(ctx, delivery.ID)
		if err != nil ||
			len(detail.Targets) != 1 ||
			detail.Targets[0].State != InboxDeadLetter ||
			detail.Targets[0].ReasonCode != "WEBHOOK_TARGET_PAYLOAD_CORRUPT" {
			t.Fatalf("corrupt target = %#v, %v", detail.Targets, err)
		}
		var leaseCleared bool
		if err := pool.QueryRow(ctx, `
			SELECT lease_token IS NULL AND lease_expires_at IS NULL
			  FROM webhook_delivery_targets
			 WHERE delivery_id=$1`,
			delivery.ID,
		).Scan(&leaseCleared); err != nil || !leaseCleared {
			t.Fatalf("corrupt target lease cleared=%v, error=%v", leaseCleared, err)
		}
		if err := store.AggregateDelivery(ctx, delivery); err != nil {
			t.Fatalf("aggregate corrupt target: %v", err)
		}
	})

	t.Run("duplicate target keys leave the immutable plan empty", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "duplicate-target-key")
		first := validIntegrationDispatchTarget(delivery.ReceivedAt)
		second := first
		second.ServiceID = "svc_webhook_duplicate"
		second.Payload = []byte(`{"intent":"different-deploy"}`)
		err := store.StorePlan(ctx, delivery, DispatchPlan{
			Targets: []DispatchTarget{first, second},
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate target key") {
			t.Fatalf("duplicate-key plan error = %v", err)
		}

		var targetCount int
		var routingCompleted bool
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM webhook_delivery_targets WHERE delivery_id=$1),
				routing_completed_at IS NOT NULL
			  FROM webhook_deliveries
			 WHERE id=$1`,
			delivery.ID,
		).Scan(&targetCount, &routingCompleted); err != nil {
			t.Fatalf("read duplicate-key plan state: %v", err)
		}
		if targetCount != 0 || routingCompleted {
			t.Fatalf(
				"duplicate-key plan persisted targets=%d routing_completed=%v",
				targetCount,
				routingCompleted,
			)
		}
	})

	t.Run("terminal payload retention preserves metadata and fails closed on redrive", func(t *testing.T) {
		terminal := acceptAndClaimDelivery(t, ctx, store, "terminal-payload-purge")
		terminalTarget := validIntegrationDispatchTarget(terminal.ReceivedAt)
		terminalTarget.TargetKey = strings.Repeat("b", 64)
		if err := store.StorePlan(
			ctx,
			terminal,
			DispatchPlan{Targets: []DispatchTarget{terminalTarget}},
		); err != nil {
			t.Fatalf("store terminal purge plan: %v", err)
		}
		claimedTarget, err := store.ClaimTarget(ctx, terminal)
		if err != nil {
			t.Fatalf("claim terminal purge target: %v", err)
		}
		if err := store.DeadLetterTarget(
			ctx,
			terminal,
			claimedTarget,
			"WEBHOOK_TARGET_INVALID",
			errors.New("terminal purge fixture"),
		); err != nil {
			t.Fatalf("dead-letter terminal purge target: %v", err)
		}
		if err := store.AggregateDelivery(ctx, terminal); err != nil {
			t.Fatalf("aggregate terminal purge delivery: %v", err)
		}

		active := acceptAndClaimDelivery(t, ctx, store, "active-payload-retained")
		activeTarget := validIntegrationDispatchTarget(active.ReceivedAt)
		activeTarget.TargetKey = strings.Repeat("c", 64)
		if err := store.StorePlan(
			ctx,
			active,
			DispatchPlan{Targets: []DispatchTarget{activeTarget}},
		); err != nil {
			t.Fatalf("store active retention plan: %v", err)
		}

		var terminalAt time.Time
		if err := pool.QueryRow(ctx, `
			SELECT terminal_at
			  FROM webhook_deliveries
			 WHERE id=$1`,
			terminal.ID,
		).Scan(&terminalAt); err != nil {
			t.Fatalf("read terminal purge cutoff: %v", err)
		}
		cutoff := terminalAt.Add(time.Microsecond)
		purged, err := store.PurgeTerminalPayloads(ctx, cutoff, DefaultPayloadPurgeBatch)
		if err != nil {
			t.Fatalf("purge terminal payloads: %v", err)
		}
		if purged.Deliveries < 1 || purged.Targets < 1 {
			t.Fatalf("purge result = %#v, want at least one delivery and target", purged)
		}

		var (
			terminalPayloadPurged bool
			terminalPayloadSHA    string
			terminalPayloadBytes  int64
			targetPayloadPurged   bool
			targetPayloadSHA      string
			targetKey             string
			activePayloadPresent  bool
			activeTargetPresent   bool
		)
		if err := pool.QueryRow(ctx, `
			SELECT
				delivery.payload IS NULL,
				delivery.payload_sha256,
				delivery.payload_bytes,
				target.dispatch_payload IS NULL,
				target.dispatch_payload_sha256,
				target.target_key,
				(SELECT payload IS NOT NULL
				   FROM webhook_deliveries
				  WHERE id=$2),
				(SELECT dispatch_payload IS NOT NULL
				   FROM webhook_delivery_targets
				  WHERE delivery_id=$2)
			  FROM webhook_deliveries delivery
			  JOIN webhook_delivery_targets target
			    ON target.delivery_id=delivery.id
			 WHERE delivery.id=$1`,
			terminal.ID,
			active.ID,
		).Scan(
			&terminalPayloadPurged,
			&terminalPayloadSHA,
			&terminalPayloadBytes,
			&targetPayloadPurged,
			&targetPayloadSHA,
			&targetKey,
			&activePayloadPresent,
			&activeTargetPresent,
		); err != nil {
			t.Fatalf("read retained payload metadata: %v", err)
		}
		if !terminalPayloadPurged ||
			terminalPayloadSHA != terminal.PayloadSHA256 ||
			terminalPayloadBytes != terminal.PayloadBytes ||
			!targetPayloadPurged ||
			targetPayloadSHA != PayloadHashSHA256(terminalTarget.Payload) ||
			targetKey != terminalTarget.TargetKey {
			t.Fatalf(
				"terminal purge metadata payload_null=%v payload_sha=%q payload_bytes=%d target_null=%v target_sha=%q target_key=%q",
				terminalPayloadPurged,
				terminalPayloadSHA,
				terminalPayloadBytes,
				targetPayloadPurged,
				targetPayloadSHA,
				targetKey,
			)
		}
		if !activePayloadPresent || !activeTargetPresent {
			t.Fatalf(
				"nonterminal payloads delivery_present=%v target_present=%v",
				activePayloadPresent,
				activeTargetPresent,
			)
		}

		if _, err := store.RedriveDelivery(ctx, terminal.ID); !errors.Is(err, ErrInboxPayloadCorrupt) {
			t.Fatalf("redrive purged delivery error = %v, want %v", err, ErrInboxPayloadCorrupt)
		}
		after, err := store.GetDelivery(ctx, terminal.ID)
		if err != nil {
			t.Fatalf("get purged delivery after redrive refusal: %v", err)
		}
		if after.State != InboxDeadLetter ||
			after.ManualRetries != 0 ||
			len(after.Targets) != 1 ||
			after.Targets[0].State != InboxDeadLetter ||
			after.Targets[0].ManualRetries != 0 {
			t.Fatalf("purged redrive mutated delivery = %#v", after)
		}
	})

	t.Run("project filtering precedes the page limit", func(t *testing.T) {
		inScope := acceptAndClaimDelivery(t, ctx, store, "project-filter-in-scope")
		inScopeTarget := validIntegrationDispatchTarget(inScope.ReceivedAt)
		inScopeTarget.TargetKey = strings.Repeat("d", 64)
		inScopeTarget.ProjectID = "prj_scope_filter"
		if err := store.StorePlan(ctx, inScope, DispatchPlan{Targets: []DispatchTarget{inScopeTarget}}); err != nil {
			t.Fatalf("store in-scope plan: %v", err)
		}

		foreign := acceptAndClaimDelivery(t, ctx, store, "project-filter-newer-foreign")
		foreignTarget := validIntegrationDispatchTarget(foreign.ReceivedAt)
		foreignTarget.TargetKey = strings.Repeat("e", 64)
		foreignTarget.ProjectID = "prj_scope_foreign"
		if err := store.StorePlan(ctx, foreign, DispatchPlan{Targets: []DispatchTarget{foreignTarget}}); err != nil {
			t.Fatalf("store foreign plan: %v", err)
		}

		page, err := store.ListDeliveries(ctx, DeliveryFilter{ProjectID: "prj_scope_filter", Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) != 1 || page[0].ID != inScope.ID {
			t.Fatalf("scoped page = %#v, want only %q", page, inScope.ID)
		}
	})

	t.Run("project redrive revalidates exclusive ownership under transaction lock", func(t *testing.T) {
		delivery := acceptAndClaimDelivery(t, ctx, store, "project-redrive-mixed")
		first := validIntegrationDispatchTarget(delivery.ReceivedAt)
		first.TargetKey = strings.Repeat("f", 64)
		first.ProjectID = "prj_scope_a"
		second := validIntegrationDispatchTarget(delivery.ReceivedAt)
		second.TargetKey = strings.Repeat("0", 64)
		second.ProjectID = "prj_scope_b"
		second.ServiceID = "svc_foreign"
		second.RepositoryID = 43
		if err := store.StorePlan(ctx, delivery, DispatchPlan{Targets: []DispatchTarget{first, second}}); err != nil {
			t.Fatalf("store mixed plan: %v", err)
		}
		for range 2 {
			target, err := store.ClaimTarget(ctx, delivery)
			if err != nil {
				t.Fatalf("claim mixed target: %v", err)
			}
			if err := store.DeadLetterTarget(ctx, delivery, target, "WEBHOOK_TARGET_DISPATCH_FAILED", errors.New("synthetic failure")); err != nil {
				t.Fatalf("dead-letter mixed target: %v", err)
			}
		}
		if err := store.AggregateDelivery(ctx, delivery); err != nil {
			t.Fatalf("aggregate mixed delivery: %v", err)
		}
		err := pool.WithTx(ctx, func(tx *dbpg.Tx) error {
			_, _, redriveErr := store.RedriveProjectDeliveryInTx(ctx, tx, delivery.ID, "prj_scope_a")
			return redriveErr
		})
		if !errors.Is(err, ErrInboxDeliveryScopeMismatch) {
			t.Fatalf("mixed project redrive error = %v, want scope mismatch", err)
		}
		after, err := store.GetDelivery(ctx, delivery.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.State != InboxDeadLetter || after.ManualRetries != 0 || len(after.Targets) != 2 {
			t.Fatalf("mixed redrive mutated delivery = %#v", after)
		}
		for _, target := range after.Targets {
			if target.State != InboxDeadLetter || target.ManualRetries != 0 {
				t.Fatalf("mixed redrive mutated target = %#v", target)
			}
		}
	})

	t.Run("down migration refuses durable delivery loss", func(t *testing.T) {
		result, err := store.Accept(ctx, integrationAcceptance("rollback-refusal"))
		if err != nil {
			t.Fatalf("accept rollback-refusal fixture: %v", err)
		}
		runner := &dbpg.MigrationRunner{
			Pool:         pool,
			FS:           migrations.FS,
			ManifestPath: "manifest.yaml",
		}
		migrationHistory, err := dbpg.ListApplied(ctx, pool)
		if err != nil || len(migrationHistory) == 0 {
			t.Fatalf("list applied migrations: %v", err)
		}
		for index := len(migrationHistory) - 1; index >= 0 && migrationHistory[index].Version > 50; index-- {
			down, err := runner.Down(ctx)
			if err != nil || down == nil || down.Version != migrationHistory[index].Version {
				t.Fatalf("rollback later migration %d: result=%#v err=%v", migrationHistory[index].Version, down, err)
			}
		}
		rolledBack, err := runner.Down(ctx)
		if err == nil || !strings.Contains(err.Error(), "rollback refused") {
			t.Fatalf("down migration result=%#v error=%v, want refusal", rolledBack, err)
		}

		var (
			deliveryStillPresent bool
			versionStillApplied  bool
		)
		if err := pool.QueryRow(ctx, `
			SELECT
				EXISTS (SELECT 1 FROM webhook_deliveries WHERE id=$1),
				EXISTS (SELECT 1 FROM schema_migrations WHERE version=50)`,
			result.ID,
		).Scan(&deliveryStillPresent, &versionStillApplied); err != nil {
			t.Fatalf("verify refused rollback: %v", err)
		}
		if !deliveryStillPresent || !versionStillApplied {
			t.Fatalf(
				"refused rollback preserved delivery=%v migration=%v",
				deliveryStillPresent,
				versionStillApplied,
			)
		}
	})
}

func acceptAndClaimDelivery(
	t *testing.T,
	ctx context.Context,
	store *PostgresDeliveryStore,
	deliveryID string,
) ClaimedDelivery {
	t.Helper()
	result, err := store.Accept(ctx, integrationAcceptance(deliveryID))
	if err != nil || result.Disposition != AcceptCreated {
		t.Fatalf("accept %s = %#v, %v", deliveryID, result, err)
	}
	delivery, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("claim %s: %v", deliveryID, err)
	}
	if delivery.ProviderDeliveryID != deliveryID {
		t.Fatalf("claimed delivery %s, want %s", delivery.ProviderDeliveryID, deliveryID)
	}
	return delivery
}

func integrationAcceptance(deliveryID string) InboxAcceptance {
	payload := []byte(`{"ref":"refs/heads/main"}`)
	hash := PayloadHashSHA256(payload)
	return InboxAcceptance{
		Provider:           ProviderGitHub,
		EndpointID:         "github-app",
		ProviderDeliveryID: deliveryID,
		EventType:          "push",
		EventHash:          hash,
		PayloadSHA256:      hash,
		Payload:            payload,
		SignatureKeyID:     "github-app-current",
	}
}

func validIntegrationDispatchTarget(receivedAt time.Time) DispatchTarget {
	authorityAt := receivedAt.Add(-time.Minute)
	return DispatchTarget{
		TargetKey:                        strings.Repeat("a", 64),
		WorkspaceID:                      "wrk_webhook",
		ProjectID:                        "prj_webhook",
		ServiceID:                        "svc_webhook",
		InstallationID:                   41,
		InstallationGrantResourceVersion: 2,
		InstallationGrantCreatedAt:       authorityAt.Add(-7 * time.Minute),
		InstallationGrantUpdatedAt:       authorityAt.Add(-6 * time.Minute),
		RepositoryID:                     42,
		Repository:                       "acme/api",
		RepositoryGrantResourceVersion:   3,
		RepositoryGrantCreatedAt:         authorityAt.Add(-5 * time.Minute),
		RepositoryGrantUpdatedAt:         authorityAt.Add(-4 * time.Minute),
		RepositoryVerifiedAt:             authorityAt.Add(-4 * time.Minute),
		LinkResourceVersion:              4,
		LinkCreatedAt:                    authorityAt.Add(-3 * time.Minute),
		LinkUpdatedAt:                    authorityAt.Add(-2 * time.Minute),
		LinkRepositoryVerifiedAt:         authorityAt.Add(-3 * time.Minute),
		Branch:                           "main",
		Ref:                              "refs/heads/main",
		CommitSHA:                        "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		CloneURL:                         "https://github.com/acme/api.git",
		RootDirectory:                    "services/api",
		ConfigPath:                       "midnight.json",
		TriggerReason:                    "matched_watch_path",
		Payload:                          []byte(`{"intent":"deploy"}`),
	}
}

func webhookDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "docker") &&
		(strings.Contains(message, "not running") ||
			strings.Contains(message, "cannot connect") ||
			strings.Contains(message, "connection refused"))
}
