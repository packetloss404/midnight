//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package pg_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/webhook"
)

func TestPostgresWebhookDurableInboxAcceptanceReplayAndIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	payload := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	request := webhookInboxAcceptance("delivery-accept-1", payload)
	accepted, err := store.Accept(ctx, request)
	if err != nil {
		t.Fatalf("accept verified delivery: %v", err)
	}
	if accepted.Disposition != webhook.AcceptCreated ||
		accepted.ID == "" ||
		accepted.ProviderDeliveryID != request.ProviderDeliveryID ||
		accepted.ReceivedAt.IsZero() {
		t.Fatalf("created acceptance = %#v", accepted)
	}

	detail, err := store.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("get accepted delivery: %v", err)
	}
	if detail.State != webhook.InboxPending ||
		detail.Provider != request.Provider ||
		detail.EndpointID != request.EndpointID ||
		detail.ProviderDeliveryID != request.ProviderDeliveryID ||
		detail.EventHash != request.EventHash ||
		detail.PayloadSHA256 != request.PayloadSHA256 ||
		detail.PayloadBytes != int64(len(payload)) ||
		detail.ReplayCount != 0 ||
		len(detail.Targets) != 0 {
		t.Fatalf("accepted delivery detail = %#v", detail)
	}
	var retainedPayload []byte
	var signatureKeyID string
	if err := pool.QueryRow(ctx, `
		SELECT payload,signature_key_id
		  FROM webhook_deliveries
		 WHERE id=$1`,
		accepted.ID,
	).Scan(&retainedPayload, &signatureKeyID); err != nil {
		t.Fatalf("read retained verified payload: %v", err)
	}
	if !bytes.Equal(retainedPayload, payload) || signatureKeyID != request.SignatureKeyID {
		t.Fatalf(
			"retained payload=%q signature_key_id=%q",
			retainedPayload,
			signatureKeyID,
		)
	}

	replayed, err := store.Accept(ctx, request)
	if err != nil {
		t.Fatalf("replay exact delivery: %v", err)
	}
	if replayed.Disposition != webhook.AcceptReplayed ||
		replayed.ID != accepted.ID ||
		!replayed.ReceivedAt.Equal(accepted.ReceivedAt) {
		t.Fatalf("replayed acceptance = %#v, created = %#v", replayed, accepted)
	}
	rotatedKey := request
	rotatedKey.SignatureKeyID = "github-secret-previous"
	replayedAfterKeyRotation, err := store.Accept(ctx, rotatedKey)
	if err != nil {
		t.Fatalf("replay exact envelope after signature-key rotation: %v", err)
	}
	if replayedAfterKeyRotation.Disposition != webhook.AcceptReplayed ||
		replayedAfterKeyRotation.ID != accepted.ID ||
		!replayedAfterKeyRotation.ReceivedAt.Equal(accepted.ReceivedAt) {
		t.Fatalf(
			"rotated-key replay = %#v, created = %#v",
			replayedAfterKeyRotation,
			accepted,
		)
	}

	sameEvent := request
	sameEvent.ProviderDeliveryID = "delivery-accept-2"
	independent, err := store.Accept(ctx, sameEvent)
	if err != nil {
		t.Fatalf("accept distinct provider delivery with same event hash: %v", err)
	}
	if independent.Disposition != webhook.AcceptCreated || independent.ID == accepted.ID {
		t.Fatalf("independent same-event acceptance = %#v", independent)
	}

	conflictingPayload := []byte(`{"ref":"refs/heads/main","after":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	conflict := webhookInboxAcceptance(request.ProviderDeliveryID, conflictingPayload)
	conflicted, err := store.Accept(ctx, conflict)
	if !errors.Is(err, webhook.ErrDeliveryIdentityReuse) {
		t.Fatalf("identity-reuse error = %v, want %v", err, webhook.ErrDeliveryIdentityReuse)
	}
	if conflicted.ID != accepted.ID ||
		conflicted.Disposition != webhook.AcceptReplayed ||
		!conflicted.ReceivedAt.Equal(accepted.ReceivedAt) {
		t.Fatalf("identity-conflict result = %#v, created = %#v", conflicted, accepted)
	}

	metadataConflicts := []struct {
		name   string
		mutate func(*webhook.InboxAcceptance)
	}{
		{
			name: "event type",
			mutate: func(candidate *webhook.InboxAcceptance) {
				candidate.EventType = "pull_request"
			},
		},
		{
			name: "event id",
			mutate: func(candidate *webhook.InboxAcceptance) {
				candidate.EventID = "different-event-id"
			},
		},
		{
			name: "event hash",
			mutate: func(candidate *webhook.InboxAcceptance) {
				candidate.EventHash = webhook.PayloadHashSHA256([]byte("different canonical event"))
			},
		},
	}
	for _, test := range metadataConflicts {
		t.Run("identity conflict "+test.name, func(t *testing.T) {
			candidate := request
			test.mutate(&candidate)
			got, err := store.Accept(ctx, candidate)
			if !errors.Is(err, webhook.ErrDeliveryIdentityReuse) {
				t.Fatalf(
					"same-body %s conflict error = %v, want %v",
					test.name,
					err,
					webhook.ErrDeliveryIdentityReuse,
				)
			}
			if got.ID != accepted.ID ||
				got.Disposition != webhook.AcceptReplayed ||
				!got.ReceivedAt.Equal(accepted.ReceivedAt) {
				t.Fatalf(
					"same-body %s conflict = %#v, created = %#v",
					test.name,
					got,
					accepted,
				)
			}
		})
	}

	var rowCount, replayCount, mismatchCount int64
	var (
		persistedSHA            string
		persistedEventType      string
		persistedEventID        string
		persistedEventHash      string
		persistedSignatureKeyID string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*),
			MAX(replay_count),
			MAX(identity_mismatch_count),
			MAX(payload_sha256),
			MAX(event_type),
			MAX(event_id),
			MAX(event_hash),
			MAX(signature_key_id)
		  FROM webhook_deliveries
		 WHERE provider=$1
		   AND endpoint_id=$2
		   AND provider_delivery_id=$3`,
		string(request.Provider),
		request.EndpointID,
		request.ProviderDeliveryID,
	).Scan(
		&rowCount,
		&replayCount,
		&mismatchCount,
		&persistedSHA,
		&persistedEventType,
		&persistedEventID,
		&persistedEventHash,
		&persistedSignatureKeyID,
	); err != nil {
		t.Fatalf("read durable identity counters: %v", err)
	}
	if rowCount != 1 ||
		replayCount != 2 ||
		mismatchCount != 1+int64(len(metadataConflicts)) ||
		persistedSHA != request.PayloadSHA256 ||
		persistedEventType != request.EventType ||
		persistedEventID != request.EventID ||
		persistedEventHash != request.EventHash ||
		persistedSignatureKeyID != request.SignatureKeyID {
		t.Fatalf(
			"identity row count=%d replay_count=%d mismatch_count=%d payload_sha=%q event_type=%q event_id=%q event_hash=%q signature_key_id=%q",
			rowCount,
			replayCount,
			mismatchCount,
			persistedSHA,
			persistedEventType,
			persistedEventID,
			persistedEventHash,
			persistedSignatureKeyID,
		)
	}
}

func TestPostgresWebhookDurableInboxReceiverPersistsBeforeAcknowledgement(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)
	store.Audit = audit.NewPostgresChain(pool)

	const (
		endpointID = "github-app-persist-before-ack"
		deliveryID = "delivery-persist-before-ack"
		lockKey    = int64(7_217_330_941)
	)
	key := webhook.Key{
		KID:      "github-secret-current",
		Material: []byte("persist-before-ack-secret"),
	}
	resolver := webhook.NewMemoryEndpointResolver()
	resolver.Set(endpointID, webhook.EndpointConfig{
		Provider: webhook.ProviderGitHub,
		Keys:     []webhook.Key{key},
	})
	receiver := webhook.NewReceiver(webhook.Options{
		Resolver: resolver,
		Inbox:    store,
	})
	handler := receiver.HandleInbound(webhook.ProviderGitHub)
	payload := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)

	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION fail_webhook_acceptance_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action = 'webhook.delivery.accepted' THEN
				PERFORM pg_advisory_xact_lock(%d);
				RAISE EXCEPTION 'forced webhook acceptance audit failure';
			END IF;
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql`,
		lockKey,
	)); err != nil {
		t.Fatalf("install audit failure function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER fail_webhook_acceptance_audit
			BEFORE INSERT ON audit_logs
			FOR EACH ROW EXECUTE FUNCTION fail_webhook_acceptance_audit()`); err != nil {
		t.Fatalf("install acceptance-audit failure trigger: %v", err)
	}

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire audit blocker connection: %v", err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin audit blocker transaction: %v", err)
	}
	blockerFinished := false
	defer func() {
		if !blockerFinished {
			_ = blockerTx.Rollback(context.Background())
		}
	}()
	if _, err := blockerTx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		t.Fatalf("hold audit trigger advisory lock: %v", err)
	}

	failedResponse := httptest.NewRecorder()
	failedDone := make(chan struct{})
	go func() {
		defer close(failedDone)
		handler.ServeHTTP(
			failedResponse,
			signedGitHubInboxRequest(endpointID, deliveryID, key.Material, payload),
		)
	}()

	waitForPostgresCondition(t, ctx, pool, `
		SELECT EXISTS (
			SELECT 1
			  FROM pg_locks
			 WHERE locktype='advisory'
			   AND NOT granted
		)`,
		"receiver waiting for the acceptance-audit transaction gate",
	)
	select {
	case <-failedDone:
		t.Fatalf(
			"receiver returned status %d before its inbox transaction could commit",
			failedResponse.Code,
		)
	default:
	}

	var visibleDeliveries int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM webhook_deliveries
		 WHERE provider=$1
		   AND endpoint_id=$2
		   AND provider_delivery_id=$3`,
		string(webhook.ProviderGitHub),
		endpointID,
		deliveryID,
	).Scan(&visibleDeliveries); err != nil {
		t.Fatalf("read externally visible delivery while audit is blocked: %v", err)
	}
	if visibleDeliveries != 0 {
		t.Fatalf(
			"receiver exposed %d durable delivery rows before acceptance audit committed",
			visibleDeliveries,
		)
	}

	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release audit trigger advisory lock: %v", err)
	}
	blockerFinished = true
	select {
	case <-failedDone:
	case <-time.After(10 * time.Second):
		t.Fatal("receiver did not return after forced audit failure")
	}
	if failedResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"forced audit failure status = %d body=%s, want %d",
			failedResponse.Code,
			failedResponse.Body.String(),
			http.StatusServiceUnavailable,
		)
	}

	var deliveryRows, auditRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
			   FROM webhook_deliveries
			  WHERE provider=$1
			    AND endpoint_id=$2
			    AND provider_delivery_id=$3),
			(SELECT COUNT(*)
			   FROM audit_logs
			  WHERE resource_kind='webhook_delivery'
			    AND action='webhook.delivery.accepted')`,
		string(webhook.ProviderGitHub),
		endpointID,
		deliveryID,
	).Scan(&deliveryRows, &auditRows); err != nil {
		t.Fatalf("read rolled-back admission: %v", err)
	}
	if deliveryRows != 0 || auditRows != 0 {
		t.Fatalf(
			"failed acceptance left delivery_rows=%d audit_rows=%d",
			deliveryRows,
			auditRows,
		)
	}

	if _, err := pool.Exec(ctx, `
		DROP TRIGGER fail_webhook_acceptance_audit ON audit_logs`); err != nil {
		t.Fatalf("remove audit failure trigger: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DROP FUNCTION fail_webhook_acceptance_audit()`); err != nil {
		t.Fatalf("remove audit failure function: %v", err)
	}

	created := httptest.NewRecorder()
	handler.ServeHTTP(
		created,
		signedGitHubInboxRequest(endpointID, deliveryID, key.Material, payload),
	)
	if created.Code != http.StatusAccepted {
		t.Fatalf(
			"retry after rolled-back acceptance status = %d body=%s",
			created.Code,
			created.Body.String(),
		)
	}
	createdInboxID := created.Header().Get("Midnight-Inbox-Id")
	if createdInboxID == "" {
		t.Fatal("successful retry omitted Midnight-Inbox-Id")
	}

	replayed := httptest.NewRecorder()
	handler.ServeHTTP(
		replayed,
		signedGitHubInboxRequest(endpointID, deliveryID, key.Material, payload),
	)
	if replayed.Code != http.StatusOK ||
		replayed.Header().Get("Idempotent-Replayed") != "true" ||
		replayed.Header().Get("Midnight-Inbox-Id") != createdInboxID {
		t.Fatalf(
			"exact retry status=%d replay=%q inbox_id=%q, created inbox_id=%q body=%s",
			replayed.Code,
			replayed.Header().Get("Idempotent-Replayed"),
			replayed.Header().Get("Midnight-Inbox-Id"),
			createdInboxID,
			replayed.Body.String(),
		)
	}

	var persistedPayload []byte
	var replayCount int64
	if err := pool.QueryRow(ctx, `
		SELECT payload,replay_count
		  FROM webhook_deliveries
		 WHERE provider=$1
		   AND endpoint_id=$2
		   AND provider_delivery_id=$3`,
		string(webhook.ProviderGitHub),
		endpointID,
		deliveryID,
	).Scan(&persistedPayload, &replayCount); err != nil {
		t.Fatalf("read successfully retried admission: %v", err)
	}
	if !bytes.Equal(persistedPayload, payload) || replayCount != 1 {
		t.Fatalf(
			"successfully retried admission payload=%q replay_count=%d",
			persistedPayload,
			replayCount,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM audit_logs
		 WHERE resource_kind='webhook_delivery'
		   AND resource_id=$1
		   AND action IN ('webhook.delivery.accepted','webhook.delivery.replayed')`,
		createdInboxID,
	).Scan(&auditRows); err != nil {
		t.Fatalf("read acceptance audit evidence: %v", err)
	}
	if auditRows != 2 {
		t.Fatalf("successful acceptance audit rows = %d, want 2", auditRows)
	}
}

func TestPostgresWebhookDurableInboxConcurrentAcceptanceRaces(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)
	var nextID atomic.Int64
	store.IDFactory = func(prefix string) string {
		return fmt.Sprintf("%s_concurrent_%d", prefix, nextID.Add(1))
	}

	t.Run("identical envelope", func(t *testing.T) {
		const contenders = 16
		request := webhookInboxAcceptance(
			"delivery-concurrent-identical",
			[]byte(`{"ref":"refs/heads/main","after":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
		)
		results := runConcurrentInboxAcceptances(
			ctx,
			store,
			repeatedInboxAcceptances(request, contenders),
		)

		var created, replayed int
		var winnerID string
		var winnerReceivedAt time.Time
		for i, outcome := range results {
			if outcome.err != nil {
				t.Fatalf("contender %d accept error: %v", i, outcome.err)
			}
			switch outcome.result.Disposition {
			case webhook.AcceptCreated:
				created++
				winnerID = outcome.result.ID
				winnerReceivedAt = outcome.result.ReceivedAt
			case webhook.AcceptReplayed:
				replayed++
			default:
				t.Fatalf(
					"contender %d disposition = %q",
					i,
					outcome.result.Disposition,
				)
			}
		}
		if created != 1 || replayed != contenders-1 || winnerID == "" {
			t.Fatalf(
				"identical race created=%d replayed=%d winner_id=%q",
				created,
				replayed,
				winnerID,
			)
		}
		for i, outcome := range results {
			if outcome.result.ID != winnerID ||
				!outcome.result.ReceivedAt.Equal(winnerReceivedAt) {
				t.Fatalf(
					"contender %d result=%#v, winner id=%q received_at=%s",
					i,
					outcome.result,
					winnerID,
					winnerReceivedAt,
				)
			}
		}

		var rowCount, replayCount, mismatchCount int64
		var persistedPayload []byte
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) OVER (),replay_count,identity_mismatch_count,payload
			  FROM webhook_deliveries
			 WHERE provider=$1
			   AND endpoint_id=$2
			   AND provider_delivery_id=$3`,
			string(request.Provider),
			request.EndpointID,
			request.ProviderDeliveryID,
		).Scan(
			&rowCount,
			&replayCount,
			&mismatchCount,
			&persistedPayload,
		); err != nil {
			t.Fatalf("read identical-race winner: %v", err)
		}
		if rowCount != 1 ||
			replayCount != contenders-1 ||
			mismatchCount != 0 ||
			!bytes.Equal(persistedPayload, request.Payload) {
			t.Fatalf(
				"identical-race row_count=%d replay_count=%d mismatch_count=%d payload=%q",
				rowCount,
				replayCount,
				mismatchCount,
				persistedPayload,
			)
		}
	})

	t.Run("conflicting immutable envelopes", func(t *testing.T) {
		const contendersPerEnvelope = 8
		left := webhookInboxAcceptance(
			"delivery-concurrent-conflict",
			[]byte(`{"ref":"refs/heads/main","after":"cccccccccccccccccccccccccccccccccccccccc"}`),
		)
		left.EventID = "event-concurrent-left"
		right := webhookInboxAcceptance(
			left.ProviderDeliveryID,
			[]byte(`{"action":"opened","number":42}`),
		)
		right.EventType = "pull_request"
		right.EventID = "event-concurrent-right"

		requests := make([]webhook.InboxAcceptance, 0, 2*contendersPerEnvelope)
		for i := 0; i < contendersPerEnvelope; i++ {
			requests = append(requests, left, right)
		}
		results := runConcurrentInboxAcceptances(ctx, store, requests)

		var created int
		var winner webhook.InboxAcceptance
		var winnerID string
		for i, outcome := range results {
			if outcome.result.Disposition == webhook.AcceptCreated {
				if outcome.err != nil {
					t.Fatalf("created contender %d error: %v", i, outcome.err)
				}
				created++
				winner = requests[i]
				winnerID = outcome.result.ID
			}
		}
		if created != 1 || winnerID == "" {
			t.Fatalf("conflicting race created=%d winner_id=%q", created, winnerID)
		}

		var winnerReplays, identityConflicts int
		for i, outcome := range results {
			candidateWon := sameImmutableInboxEnvelope(requests[i], winner)
			if candidateWon {
				if outcome.err != nil {
					t.Fatalf("winner-envelope contender %d error: %v", i, outcome.err)
				}
				if outcome.result.Disposition == webhook.AcceptReplayed {
					winnerReplays++
				}
			} else {
				if !errors.Is(outcome.err, webhook.ErrDeliveryIdentityReuse) {
					t.Fatalf(
						"loser-envelope contender %d error = %v, want %v",
						i,
						outcome.err,
						webhook.ErrDeliveryIdentityReuse,
					)
				}
				if outcome.result.Disposition != webhook.AcceptReplayed {
					t.Fatalf(
						"loser-envelope contender %d disposition = %q",
						i,
						outcome.result.Disposition,
					)
				}
				identityConflicts++
			}
			if outcome.result.ID != winnerID {
				t.Fatalf(
					"contender %d inbox id = %q, want winner %q",
					i,
					outcome.result.ID,
					winnerID,
				)
			}
		}
		if winnerReplays != contendersPerEnvelope-1 ||
			identityConflicts != contendersPerEnvelope {
			t.Fatalf(
				"conflicting race winner_replays=%d identity_conflicts=%d",
				winnerReplays,
				identityConflicts,
			)
		}

		var (
			rowCount, replayCount, mismatchCount      int64
			eventType, eventID, eventHash, payloadSHA string
			persistedPayload                          []byte
		)
		if err := pool.QueryRow(ctx, `
			SELECT
				COUNT(*) OVER (),
				replay_count,
				identity_mismatch_count,
				event_type,
				event_id,
				event_hash,
				payload_sha256,
				payload
			  FROM webhook_deliveries
			 WHERE provider=$1
			   AND endpoint_id=$2
			   AND provider_delivery_id=$3`,
			string(winner.Provider),
			winner.EndpointID,
			winner.ProviderDeliveryID,
		).Scan(
			&rowCount,
			&replayCount,
			&mismatchCount,
			&eventType,
			&eventID,
			&eventHash,
			&payloadSHA,
			&persistedPayload,
		); err != nil {
			t.Fatalf("read conflicting-race winner: %v", err)
		}
		if rowCount != 1 ||
			replayCount != contendersPerEnvelope-1 ||
			mismatchCount != contendersPerEnvelope ||
			eventType != winner.EventType ||
			eventID != winner.EventID ||
			eventHash != winner.EventHash ||
			payloadSHA != winner.PayloadSHA256 ||
			!bytes.Equal(persistedPayload, winner.Payload) {
			t.Fatalf(
				"conflicting-race row_count=%d replay_count=%d mismatch_count=%d event_type=%q event_id=%q event_hash=%q payload_sha=%q payload=%q winner=%#v",
				rowCount,
				replayCount,
				mismatchCount,
				eventType,
				eventID,
				eventHash,
				payloadSHA,
				persistedPayload,
				winner,
			)
		}
	})
}

func TestPostgresWebhookDurableInboxPayloadIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	payload := []byte(`{"zen":"durable before acknowledgement"}`)
	invalid := webhookInboxAcceptance("delivery-invalid-sha", payload)
	invalid.PayloadSHA256 = webhook.PayloadHashSHA256([]byte("different payload"))
	if _, err := store.Accept(ctx, invalid); err == nil {
		t.Fatal("accept admitted payload whose declared sha256 did not match")
	}
	var invalidRows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM webhook_deliveries
		 WHERE provider=$1
		   AND endpoint_id=$2
		   AND provider_delivery_id=$3`,
		string(invalid.Provider),
		invalid.EndpointID,
		invalid.ProviderDeliveryID,
	).Scan(&invalidRows); err != nil {
		t.Fatal(err)
	}
	if invalidRows != 0 {
		t.Fatalf("invalid payload left %d durable rows", invalidRows)
	}

	request := webhookInboxAcceptance("delivery-integrity-1", payload)
	accepted, err := store.Accept(ctx, request)
	if err != nil {
		t.Fatalf("accept integrity fixture: %v", err)
	}
	claimed, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("claim integrity fixture: %v", err)
	}
	if claimed.ID != accepted.ID ||
		!bytes.Equal(claimed.Payload, payload) ||
		webhook.PayloadHashSHA256(claimed.Payload) != claimed.PayloadSHA256 {
		t.Fatalf("claimed delivery payload integrity = %#v", claimed)
	}
	if err := store.DeadLetterDelivery(
		ctx,
		claimed,
		"WEBHOOK_PAYLOAD_INVALID",
		errors.New("fixture dead letter"),
	); err != nil {
		t.Fatalf("dead-letter integrity fixture: %v", err)
	}

	corrupt := bytes.Repeat([]byte{'x'}, len(payload))
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET payload=$2
		 WHERE id=$1`,
		accepted.ID,
		corrupt,
	); err != nil {
		t.Fatalf("corrupt retained payload fixture: %v", err)
	}
	if _, err := store.RedriveDelivery(ctx, accepted.ID); !errors.Is(err, webhook.ErrInboxPayloadCorrupt) {
		t.Fatalf("redrive corrupt retained payload error = %v", err)
	}
	detail, err := store.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("get corrupt dead letter: %v", err)
	}
	if detail.State != webhook.InboxDeadLetter ||
		detail.ManualRetries != 0 ||
		detail.TerminalAt == nil {
		t.Fatalf("corrupt redrive mutated dead letter = %#v", detail)
	}
}

func TestPostgresWebhookDurableInboxLeaseFencing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	request := webhookInboxAcceptance(
		"delivery-lease-1",
		[]byte(`{"ref":"refs/heads/main","after":"cccccccccccccccccccccccccccccccccccccccc"}`),
	)
	accepted, err := store.Accept(ctx, request)
	if err != nil {
		t.Fatalf("accept lease fixture: %v", err)
	}
	first, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.ID != accepted.ID || first.LeaseToken == "" || first.RouteAttempt != 1 {
		t.Fatalf("first lease = %#v", first)
	}
	if _, err := store.ClaimDelivery(ctx); !errors.Is(err, webhook.ErrInboxNoWork) {
		t.Fatalf("concurrent claim error = %v, want %v", err, webhook.ErrInboxNoWork)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET updated_at=received_at,
		       lease_expires_at=received_at+INTERVAL '1 microsecond'
		 WHERE id=$1`,
		accepted.ID,
	); err != nil {
		t.Fatalf("expire first lease fixture: %v", err)
	}
	second, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("reclaim expired delivery: %v", err)
	}
	if second.ID != first.ID ||
		second.LeaseToken == first.LeaseToken ||
		second.RouteAttempt != 2 {
		t.Fatalf("reclaimed lease = %#v, first = %#v", second, first)
	}

	if err := store.IgnoreDelivery(ctx, first, "stale_worker"); !errors.Is(err, webhook.ErrInboxLeaseLost) {
		t.Fatalf("stale lease mutation error = %v, want %v", err, webhook.ErrInboxLeaseLost)
	}
	if err := store.IgnoreDelivery(ctx, second, "verified_no_targets"); err != nil {
		t.Fatalf("active lease mutation: %v", err)
	}
	if err := store.IgnoreDelivery(ctx, second, "duplicate_completion"); !errors.Is(err, webhook.ErrInboxLeaseLost) {
		t.Fatalf("completed lease reuse error = %v, want %v", err, webhook.ErrInboxLeaseLost)
	}

	detail, err := store.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("get fenced delivery: %v", err)
	}
	if detail.State != webhook.InboxIgnored ||
		detail.RouteAttempt != 2 ||
		detail.ReasonCode != "verified_no_targets" ||
		detail.TerminalAt == nil {
		t.Fatalf("fenced delivery detail = %#v", detail)
	}
}

func TestPostgresWebhookDurableInboxTargetLeaseFencing(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	payload := []byte(`{"ref":"refs/heads/main","after":"ffffffffffffffffffffffffffffffffffffffff"}`)
	if _, err := store.Accept(
		ctx,
		webhookInboxAcceptance("delivery-target-lease", payload),
	); err != nil {
		t.Fatalf("accept target lease fixture: %v", err)
	}
	delivery, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("claim target lease delivery: %v", err)
	}
	target := webhookInboxDispatchTarget(
		delivery,
		[]byte(`{"service_id":"svc_corrupt_target","commit_sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`),
	)
	if err := store.StorePlan(ctx, delivery, webhook.DispatchPlan{
		Targets: []webhook.DispatchTarget{target},
	}); err != nil {
		t.Fatalf("store target lease plan: %v", err)
	}

	first, err := store.ClaimTarget(ctx, delivery)
	if err != nil {
		t.Fatalf("first target claim: %v", err)
	}
	if first.LeaseToken == "" || first.Attempt != 1 || first.TargetKey != target.TargetKey {
		t.Fatalf("first target lease = %#v", first)
	}
	if _, err := store.ClaimTarget(ctx, delivery); !errors.Is(err, webhook.ErrInboxNoWork) {
		t.Fatalf("concurrent target claim error = %v, want %v", err, webhook.ErrInboxNoWork)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET updated_at=created_at,
		       lease_expires_at=created_at+INTERVAL '1 microsecond'
		 WHERE id=$1`,
		first.ID,
	); err != nil {
		t.Fatalf("expire first target lease fixture: %v", err)
	}
	second, err := store.ClaimTarget(ctx, delivery)
	if err != nil {
		t.Fatalf("reclaim expired target: %v", err)
	}
	if second.ID != first.ID ||
		second.LeaseToken == first.LeaseToken ||
		second.Attempt != 2 {
		t.Fatalf("reclaimed target lease = %#v, first = %#v", second, first)
	}

	result := webhook.TargetResult{
		OperationID:    "op_target_lease",
		DeploymentID:   "dep_target_lease",
		StagedOutboxID: "stg_target_lease",
	}
	if err := store.CompleteTarget(ctx, delivery, first, result); !errors.Is(err, webhook.ErrInboxLeaseLost) {
		t.Fatalf("stale target lease mutation error = %v, want %v", err, webhook.ErrInboxLeaseLost)
	}
	if err := store.CompleteTarget(ctx, delivery, second, result); err != nil {
		t.Fatalf("active target lease mutation: %v", err)
	}
	if err := store.CompleteTarget(ctx, delivery, second, result); !errors.Is(err, webhook.ErrInboxLeaseLost) {
		t.Fatalf("completed target lease reuse error = %v, want %v", err, webhook.ErrInboxLeaseLost)
	}
	if err := store.AggregateDelivery(ctx, delivery); err != nil {
		t.Fatalf("aggregate completed target: %v", err)
	}

	detail, err := store.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("get target-fenced delivery: %v", err)
	}
	if detail.State != webhook.InboxSucceeded ||
		detail.TerminalAt == nil ||
		len(detail.Targets) != 1 ||
		detail.Targets[0].State != webhook.InboxSucceeded ||
		detail.Targets[0].Attempt != 2 ||
		detail.Targets[0].OperationID != result.OperationID ||
		detail.Targets[0].DeploymentID != result.DeploymentID ||
		detail.Targets[0].StagedOutboxID != result.StagedOutboxID {
		t.Fatalf("target-fenced delivery detail = %#v", detail)
	}
}

func TestPostgresWebhookDurableInboxRejectsCorruptClaimedDeliveryPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	payload := []byte(`{"ref":"refs/heads/main","after":"dddddddddddddddddddddddddddddddddddddddd"}`)
	accepted, err := store.Accept(
		ctx,
		webhookInboxAcceptance("delivery-corrupt-claim", payload),
	)
	if err != nil {
		t.Fatalf("accept corrupt-claim fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET payload=$2
		 WHERE id=$1`,
		accepted.ID,
		bytes.Repeat([]byte{'x'}, len(payload)),
	); err != nil {
		t.Fatalf("corrupt retained delivery payload: %v", err)
	}

	if _, err := store.ClaimDelivery(ctx); !errors.Is(err, webhook.ErrInboxPayloadCorrupt) {
		t.Fatalf("claim corrupt delivery error = %v, want %v", err, webhook.ErrInboxPayloadCorrupt)
	}
	detail, err := store.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("get rejected corrupt delivery: %v", err)
	}
	if detail.State != webhook.InboxDeadLetter ||
		detail.ReasonCode != "WEBHOOK_PAYLOAD_CORRUPT" ||
		detail.TerminalAt == nil {
		t.Fatalf("corrupt claimed delivery = %#v", detail)
	}
}

func TestPostgresWebhookDurableInboxRejectsCorruptClaimedTargetPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	store, pool := newPostgresWebhookInbox(t, ctx)

	payload := []byte(`{"ref":"refs/heads/main","after":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`)
	if _, err := store.Accept(
		ctx,
		webhookInboxAcceptance("delivery-corrupt-target", payload),
	); err != nil {
		t.Fatalf("accept corrupt-target fixture: %v", err)
	}
	delivery, err := store.ClaimDelivery(ctx)
	if err != nil {
		t.Fatalf("claim corrupt-target delivery: %v", err)
	}
	targetPayload := []byte(`{"service_id":"svc_corrupt_target","commit_sha":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}`)
	target := webhookInboxDispatchTarget(delivery, targetPayload)
	if err := store.StorePlan(ctx, delivery, webhook.DispatchPlan{
		Targets: []webhook.DispatchTarget{target},
	}); err != nil {
		t.Fatalf("store corrupt-target plan: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET dispatch_payload=$2
		 WHERE delivery_id=$1
		   AND target_key=$3`,
		delivery.ID,
		bytes.Repeat([]byte{'x'}, len(targetPayload)),
		target.TargetKey,
	); err != nil {
		t.Fatalf("corrupt retained target payload: %v", err)
	}

	if _, err := store.ClaimTarget(ctx, delivery); !errors.Is(err, webhook.ErrInboxPayloadCorrupt) {
		t.Fatalf("claim corrupt target error = %v, want %v", err, webhook.ErrInboxPayloadCorrupt)
	}
	detail, err := store.GetDelivery(ctx, delivery.ID)
	if err != nil {
		t.Fatalf("get rejected corrupt target: %v", err)
	}
	if len(detail.Targets) != 1 ||
		detail.Targets[0].State != webhook.InboxDeadLetter ||
		detail.Targets[0].ReasonCode != "WEBHOOK_TARGET_PAYLOAD_CORRUPT" ||
		detail.Targets[0].TerminalAt == nil {
		t.Fatalf("corrupt claimed target detail = %#v", detail)
	}
}

func newPostgresWebhookInbox(
	t *testing.T,
	ctx context.Context,
) (*webhook.PostgresDeliveryStore, *pg.Pool) {
	t.Helper()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	store := webhook.NewPostgresDeliveryStore(pool)
	nextID := 0
	store.IDFactory = func(prefix string) string {
		nextID++
		return fmt.Sprintf("%s_integration_%d", prefix, nextID)
	}
	return store, pool
}

func webhookInboxAcceptance(deliveryID string, payload []byte) webhook.InboxAcceptance {
	hash := webhook.PayloadHashSHA256(payload)
	return webhook.InboxAcceptance{
		Provider:           webhook.ProviderGitHub,
		EndpointID:         "github-app-42",
		ProviderDeliveryID: deliveryID,
		EventType:          "push",
		EventID:            "event-" + deliveryID,
		EventHash:          hash,
		PayloadSHA256:      hash,
		Payload:            append([]byte(nil), payload...),
		SignatureKeyID:     "github-secret-current",
	}
}

func webhookInboxDispatchTarget(
	delivery webhook.ClaimedDelivery,
	payload []byte,
) webhook.DispatchTarget {
	grantCreatedAt := delivery.ReceivedAt.Add(-3 * time.Minute)
	verifiedAt := delivery.ReceivedAt.Add(-2 * time.Minute)
	grantUpdatedAt := delivery.ReceivedAt.Add(-time.Minute)
	return webhook.DispatchTarget{
		TargetKey:                        webhook.PayloadHashSHA256([]byte("svc_corrupt_target")),
		WorkspaceID:                      "wrk_default",
		ProjectID:                        "prj_corrupt_target",
		ServiceID:                        "svc_corrupt_target",
		InstallationID:                   42,
		InstallationGrantResourceVersion: 3,
		RepositoryID:                     4201,
		Repository:                       "acme/api",
		RepositoryGrantResourceVersion:   7,
		LinkResourceVersion:              11,
		LinkCreatedAt:                    grantCreatedAt,
		LinkUpdatedAt:                    grantUpdatedAt,
		RepositoryGrantCreatedAt:         grantCreatedAt,
		RepositoryGrantUpdatedAt:         grantUpdatedAt,
		RepositoryVerifiedAt:             verifiedAt,
		LinkRepositoryVerifiedAt:         verifiedAt,
		InstallationGrantCreatedAt:       grantCreatedAt,
		InstallationGrantUpdatedAt:       grantUpdatedAt,
		Branch:                           "main",
		Ref:                              "refs/heads/main",
		CommitSHA:                        "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		CloneURL:                         "https://github.com/acme/api.git",
		TriggerReason:                    "github_push",
		Payload:                          append([]byte(nil), payload...),
	}
}

func signedGitHubInboxRequest(
	endpointID string,
	deliveryID string,
	secret []byte,
	payload []byte,
) *http.Request {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/webhooks/inbound/github/"+endpointID,
		bytes.NewReader(payload),
	)
	request.Header.Set(
		"X-Hub-Signature-256",
		"sha256="+hex.EncodeToString(mac.Sum(nil)),
	)
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func waitForPostgresCondition(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	query string,
	description string,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ready bool
		if err := pool.QueryRow(ctx, query).Scan(&ready); err != nil {
			t.Fatalf("wait for %s: %v", description, err)
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type inboxAcceptanceOutcome struct {
	result webhook.AcceptResult
	err    error
}

func runConcurrentInboxAcceptances(
	ctx context.Context,
	store *webhook.PostgresDeliveryStore,
	requests []webhook.InboxAcceptance,
) []inboxAcceptanceOutcome {
	start := make(chan struct{})
	outcomes := make([]inboxAcceptanceOutcome, len(requests))
	var group sync.WaitGroup
	group.Add(len(requests))
	for i := range requests {
		go func(index int) {
			defer group.Done()
			<-start
			outcomes[index].result, outcomes[index].err = store.Accept(ctx, requests[index])
		}(i)
	}
	close(start)
	group.Wait()
	return outcomes
}

func repeatedInboxAcceptances(
	request webhook.InboxAcceptance,
	count int,
) []webhook.InboxAcceptance {
	requests := make([]webhook.InboxAcceptance, count)
	for i := range requests {
		requests[i] = request
	}
	return requests
}

func sameImmutableInboxEnvelope(
	left webhook.InboxAcceptance,
	right webhook.InboxAcceptance,
) bool {
	return left.EventType == right.EventType &&
		left.EventID == right.EventID &&
		left.EventHash == right.EventHash &&
		left.PayloadSHA256 == right.PayloadSHA256 &&
		bytes.Equal(left.Payload, right.Payload)
}
