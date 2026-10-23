// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package webhook

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/redaction"
)

// PostgresDeliveryStore is Midnight's durable persist-before-ack inbox.
// The historical name is retained for API compatibility; migration 0050
// replaces the old dedupe-only table with the leased delivery/target schema.
type PostgresDeliveryStore struct {
	Pool *dbpg.Pool
	// AcceptanceFence optionally acquires a provider-specific transaction
	// fence before the receipt timestamp is assigned. Production GitHub
	// admission uses the same installation advisory lock as source-link
	// mutation, making receipt-time authority follow commit order.
	AcceptanceFence func(context.Context, *dbpg.Tx, InboxAcceptance) error
	Audit           *audit.PostgresChain
	LeaseTime       time.Duration
	WorkerID        string
	IDFactory       func(prefix string) string
}

func NewPostgresDeliveryStore(pool *dbpg.Pool) *PostgresDeliveryStore {
	return &PostgresDeliveryStore{
		Pool:      pool,
		LeaseTime: DefaultInboxLease,
		WorkerID:  "webhook-" + randomInboxID("worker"),
		IDFactory: randomInboxID,
	}
}

func randomInboxID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}

func (s *PostgresDeliveryStore) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("webhook.PostgresDeliveryStore: pool not initialized")
	}
	return nil
}

func (s *PostgresDeliveryStore) newID(prefix string) string {
	if s != nil && s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return randomInboxID(prefix)
}

func (s *PostgresDeliveryStore) lease() time.Duration {
	if s == nil || s.LeaseTime <= 0 {
		return DefaultInboxLease
	}
	return s.LeaseTime
}

func (s *PostgresDeliveryStore) leaseSeconds() int64 {
	seconds := int64(s.lease() / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func (s *PostgresDeliveryStore) LeaseHeartbeatInterval() time.Duration {
	interval := s.lease() / 3
	if interval < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	return interval
}

func (s *PostgresDeliveryStore) RenewDeliveryLease(
	ctx context.Context,
	delivery ClaimedDelivery,
) error {
	if err := s.ready(); err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET lease_expires_at=clock_timestamp()+make_interval(secs => $1),
		       updated_at=clock_timestamp()
		 WHERE id=$2
		   AND state='processing'
		   AND lease_token=$3
		   AND lease_expires_at > clock_timestamp()`,
		s.leaseSeconds(), delivery.ID, delivery.LeaseToken,
	)
	if err != nil {
		return fmt.Errorf("webhook inbox renew delivery lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxLeaseLost
	}
	return nil
}

func (s *PostgresDeliveryStore) RenewTargetLease(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
) error {
	if err := s.ready(); err != nil {
		return err
	}
	return s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET lease_expires_at=clock_timestamp()+make_interval(secs => $1),
			       updated_at=clock_timestamp()
			 WHERE id=$2
			   AND state='processing'
			   AND lease_token=$3
			   AND lease_expires_at > clock_timestamp()`,
			s.leaseSeconds(), delivery.ID, delivery.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox renew target parent lease: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		tag, err = tx.Exec(ctx, `
			UPDATE webhook_delivery_targets
			   SET lease_expires_at=clock_timestamp()+make_interval(secs => $1),
			       updated_at=clock_timestamp()
			 WHERE id=$2
			   AND delivery_id=$3
			   AND state='processing'
			   AND lease_token=$4
			   AND lease_expires_at > clock_timestamp()`,
			s.leaseSeconds(), target.ID, delivery.ID, target.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox renew target lease: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		return nil
	})
}

func (s *PostgresDeliveryStore) Accept(ctx context.Context, request InboxAcceptance) (AcceptResult, error) {
	if err := s.ready(); err != nil {
		return AcceptResult{}, err
	}
	request.Provider = Provider(strings.TrimSpace(string(request.Provider)))
	request.EndpointID = strings.TrimSpace(request.EndpointID)
	request.ProviderDeliveryID = strings.TrimSpace(request.ProviderDeliveryID)
	request.EventType = strings.TrimSpace(request.EventType)
	request.EventID = strings.TrimSpace(request.EventID)
	request.EventHash = strings.TrimSpace(request.EventHash)
	request.SignatureKeyID = strings.TrimSpace(request.SignatureKeyID)
	if request.PayloadSHA256 == "" {
		request.PayloadSHA256 = PayloadHashSHA256(request.Payload)
	}
	if request.EventHash == "" {
		request.EventHash = request.PayloadSHA256
	}
	if request.Provider == "" || request.EndpointID == "" ||
		request.ProviderDeliveryID == "" || request.EventType == "" ||
		len(request.Payload) > int(MaxInboundBodyBytes) ||
		!validSHA256(request.PayloadSHA256) ||
		!validSHA256(request.EventHash) {
		return AcceptResult{}, errors.New("webhook inbox: provider, endpoint, delivery identity, event type, bounded payload, and sha256 are required")
	}
	if PayloadHashSHA256(request.Payload) != request.PayloadSHA256 {
		return AcceptResult{}, errors.New("webhook inbox: payload sha256 does not match payload")
	}

	id := s.newID("wbd")
	var result AcceptResult
	var dispositionErr error
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		if s.AcceptanceFence != nil {
			if err := s.AcceptanceFence(ctx, tx, request); err != nil {
				return fmt.Errorf("webhook inbox acceptance fence: %w", err)
			}
		}
		var receivedAt time.Time
		insertErr := tx.QueryRow(ctx, `
			WITH stamp AS (SELECT clock_timestamp() AS at)
			INSERT INTO webhook_deliveries (
				id,provider,endpoint_id,provider_delivery_id,event_type,event_id,
				event_hash,payload_sha256,payload_bytes,payload,signature_key_id,
				state,route_attempt,max_route_attempts,manual_retries,
				available_at,received_at,last_received_at,updated_at
			)
			SELECT
				$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
				'pending',0,$12,0,
				stamp.at,stamp.at,stamp.at,stamp.at
			FROM stamp
			ON CONFLICT (provider,endpoint_id,provider_delivery_id) DO NOTHING
			RETURNING received_at`,
			id, string(request.Provider), request.EndpointID,
			request.ProviderDeliveryID, request.EventType, request.EventID,
			request.EventHash, request.PayloadSHA256, len(request.Payload),
			request.Payload, request.SignatureKeyID, DefaultInboxMaxAttempts,
		).Scan(&receivedAt)
		switch {
		case insertErr == nil:
			result = AcceptResult{
				ID:                 id,
				ProviderDeliveryID: request.ProviderDeliveryID,
				ReceivedAt:         receivedAt.UTC(),
				Disposition:        AcceptCreated,
			}
			return s.appendAcceptanceAudit(ctx, tx, result, request, "webhook.delivery.accepted", "")
		case !errors.Is(insertErr, pgx.ErrNoRows):
			return fmt.Errorf("webhook inbox insert: %w", insertErr)
		}

		var existingID, eventType, eventID, eventHash, payloadSHA string
		var payloadBytes int64
		var payload []byte
		if err := tx.QueryRow(ctx, `
			SELECT id,event_type,event_id,event_hash,
			       payload_sha256,payload_bytes,payload,received_at
			  FROM webhook_deliveries
			 WHERE provider=$1 AND endpoint_id=$2 AND provider_delivery_id=$3
			 FOR UPDATE`,
			string(request.Provider), request.EndpointID, request.ProviderDeliveryID,
		).Scan(
			&existingID,
			&eventType,
			&eventID,
			&eventHash,
			&payloadSHA,
			&payloadBytes,
			&payload,
			&receivedAt,
		); err != nil {
			return fmt.Errorf("webhook inbox read identity conflict: %w", err)
		}
		result = AcceptResult{
			ID:                 existingID,
			ProviderDeliveryID: request.ProviderDeliveryID,
			ReceivedAt:         receivedAt.UTC(),
			Disposition:        AcceptReplayed,
		}
		if eventType != request.EventType ||
			eventID != request.EventID ||
			eventHash != request.EventHash ||
			payloadSHA != request.PayloadSHA256 ||
			payloadBytes != int64(len(request.Payload)) ||
			(payload != nil && !bytes.Equal(payload, request.Payload)) {
			if _, err := tx.Exec(ctx, `
				UPDATE webhook_deliveries
				   SET identity_mismatch_count=identity_mismatch_count+1,
				       last_received_at=clock_timestamp(),
				       updated_at=CASE WHEN state='processing'
				           THEN LEAST(
				               clock_timestamp(),
				               lease_expires_at-INTERVAL '1 microsecond'
				           )
				           ELSE clock_timestamp() END
				 WHERE id=$1`,
				existingID,
			); err != nil {
				return fmt.Errorf("webhook inbox record identity mismatch: %w", err)
			}
			if err := s.appendAcceptanceAudit(
				ctx,
				tx,
				result,
				request,
				"webhook.delivery.identity_mismatch",
				"WEBHOOK_DELIVERY_IDENTITY_MISMATCH",
			); err != nil {
				return err
			}
			dispositionErr = ErrDeliveryIdentityReuse
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET replay_count=replay_count+1,
			       last_received_at=clock_timestamp(),
			       updated_at=CASE WHEN state='processing'
			           THEN LEAST(
			               clock_timestamp(),
			               lease_expires_at-INTERVAL '1 microsecond'
			           )
			           ELSE clock_timestamp() END
			 WHERE id=$1`,
			existingID,
		); err != nil {
			return fmt.Errorf("webhook inbox record replay: %w", err)
		}
		return s.appendAcceptanceAudit(ctx, tx, result, request, "webhook.delivery.replayed", "")
	})
	if err != nil {
		return AcceptResult{}, err
	}
	if dispositionErr != nil {
		return result, dispositionErr
	}
	return result, nil
}

func (s *PostgresDeliveryStore) appendAcceptanceAudit(
	ctx context.Context,
	tx *dbpg.Tx,
	result AcceptResult,
	request InboxAcceptance,
	action string,
	reasonCode string,
) error {
	if s.Audit == nil {
		return nil
	}
	_, err := s.Audit.AppendInTx(ctx, tx, audit.Row{
		ActorType:    "system",
		AuthMethod:   "signed-webhook",
		ResourceKind: "webhook_delivery",
		ResourceID:   result.ID,
		Action:       action,
		ReasonCode:   reasonCode,
		OccurredAt:   result.ReceivedAt,
		Attrs: map[string]any{
			"provider":             string(request.Provider),
			"endpoint_id":          request.EndpointID,
			"provider_delivery_id": request.ProviderDeliveryID,
			"event_type":           request.EventType,
			"payload_sha256":       request.PayloadSHA256,
			"payload_bytes":        len(request.Payload),
		},
	})
	if err != nil {
		return fmt.Errorf("webhook inbox append audit: %w", err)
	}
	return nil
}

func (s *PostgresDeliveryStore) ClaimDelivery(ctx context.Context) (ClaimedDelivery, error) {
	if err := s.ready(); err != nil {
		return ClaimedDelivery{}, err
	}
	leaseToken := s.newID("wbl")
	leaseSeconds := s.leaseSeconds()
	var delivery ClaimedDelivery
	corrupt := false
	noWork := false
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		row := tx.QueryRow(ctx, `
			WITH exhausted AS (
				UPDATE webhook_deliveries
				   SET state='dead_letter',
				       reason_code='WEBHOOK_ROUTE_ATTEMPTS_EXHAUSTED',
				       lease_token=NULL,
				       lease_expires_at=NULL,
				       terminal_at=clock_timestamp(),
				       updated_at=clock_timestamp()
				 WHERE state='processing'
				   AND routing_completed_at IS NULL
				   AND lease_expires_at <= clock_timestamp()
				   AND route_attempt >= max_route_attempts
				RETURNING id
			), candidate AS (
				SELECT id
				  FROM webhook_deliveries
				 WHERE available_at <= clock_timestamp()
				   AND (
				        routing_completed_at IS NOT NULL
				        OR route_attempt < max_route_attempts
				   )
				   AND (
					    state IN ('pending','retry_wait')
					    OR (state='processing' AND lease_expires_at <= clock_timestamp())
				   )
				 ORDER BY available_at,received_at,id
				 FOR UPDATE SKIP LOCKED
				 LIMIT 1
			)
			UPDATE webhook_deliveries delivery
			   SET state='processing',
			       route_attempt=CASE
			           WHEN delivery.routing_completed_at IS NULL
			           THEN delivery.route_attempt+1
			           ELSE delivery.route_attempt
			       END,
			       lease_token=$1,
			       lease_expires_at=clock_timestamp()+make_interval(secs => $2),
			       updated_at=clock_timestamp()
			  FROM candidate
			 WHERE delivery.id=candidate.id
			RETURNING `+deliverySummaryColumns("delivery")+`,
			          delivery.lease_token,delivery.payload`,
			leaseToken, leaseSeconds,
		)
		if err := scanClaimedDelivery(row, &delivery); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				noWork = true
				return nil
			}
			return fmt.Errorf("webhook inbox claim delivery: %w", err)
		}
		if int64(len(delivery.Payload)) == delivery.PayloadBytes &&
			PayloadHashSHA256(delivery.Payload) == delivery.PayloadSHA256 {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET state='dead_letter',
			       reason_code='WEBHOOK_PAYLOAD_CORRUPT',
			       last_error='',
			       lease_token=NULL,
			       lease_expires_at=NULL,
			       terminal_at=clock_timestamp(),
			       updated_at=clock_timestamp()
			 WHERE id=$1
			   AND state='processing'
			   AND lease_token=$2`,
			delivery.ID, delivery.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox dead-letter corrupt delivery: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		corrupt = true
		return nil
	})
	if err != nil {
		return ClaimedDelivery{}, err
	}
	if noWork {
		return ClaimedDelivery{}, ErrInboxNoWork
	}
	if corrupt {
		return ClaimedDelivery{}, ErrInboxPayloadCorrupt
	}
	return delivery, nil
}

func (s *PostgresDeliveryStore) StorePlan(ctx context.Context, delivery ClaimedDelivery, plan DispatchPlan) error {
	if err := s.ready(); err != nil {
		return err
	}
	if len(plan.Targets) == 0 {
		return errors.New("webhook inbox: non-empty target plan required")
	}
	targetKeys := make(map[string]struct{}, len(plan.Targets))
	for _, target := range plan.Targets {
		if err := validateDispatchTarget(target); err != nil {
			return err
		}
		if _, exists := targetKeys[target.TargetKey]; exists {
			return errors.New("webhook inbox: duplicate target key in immutable plan")
		}
		targetKeys[target.TargetKey] = struct{}{}
	}
	return s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var fencedID string
		if err := tx.QueryRow(ctx, `
			SELECT id FROM webhook_deliveries
			 WHERE id=$1 AND state='processing' AND lease_token=$2
			   AND lease_expires_at > clock_timestamp()
			 FOR UPDATE`,
			delivery.ID, delivery.LeaseToken,
		).Scan(&fencedID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInboxLeaseLost
			}
			return fmt.Errorf("webhook inbox fence plan: %w", err)
		}
		for _, target := range plan.Targets {
			if _, err := tx.Exec(ctx, `
				INSERT INTO webhook_delivery_targets (
					id,delivery_id,delivery_received_at,target_key,
					workspace_id,project_id,service_id,
					installation_id,installation_grant_resource_version,
					repository_id,repository_full_name,repository_grant_resource_version,
					link_resource_version,
					installation_grant_created_at,installation_grant_updated_at,
					repository_grant_created_at,repository_grant_updated_at,
					repository_access_verified_at,link_created_at,link_updated_at,
					link_repository_access_verified_at,
					branch,ref,commit_sha,clone_url,root_directory,config_path,
					trigger_reason,dispatch_payload,dispatch_payload_sha256,
					state,attempt,max_attempts,available_at,created_at,updated_at
				) VALUES (
					$1,$2,$3,$4,
					$5,$6,$7,
					$8,$9,
					$10,$11,$12,
					$13,
					$14,$15,
					$16,$17,
					$18,$19,$20,
					$21,
					$22,$23,$24,$25,$26,$27,
					$28,$29,$30,
					'pending',0,$31,clock_timestamp(),clock_timestamp(),clock_timestamp()
				)`,
				s.newID("wbt"), delivery.ID, delivery.ReceivedAt.UTC(), target.TargetKey,
				target.WorkspaceID, target.ProjectID, target.ServiceID,
				target.InstallationID, target.InstallationGrantResourceVersion,
				target.RepositoryID, target.Repository, target.RepositoryGrantResourceVersion,
				target.LinkResourceVersion,
				target.InstallationGrantCreatedAt.UTC(), target.InstallationGrantUpdatedAt.UTC(),
				target.RepositoryGrantCreatedAt.UTC(), target.RepositoryGrantUpdatedAt.UTC(),
				target.RepositoryVerifiedAt.UTC(), target.LinkCreatedAt.UTC(), target.LinkUpdatedAt.UTC(),
				target.LinkRepositoryVerifiedAt.UTC(),
				target.Branch, target.Ref, target.CommitSHA, target.CloneURL,
				target.RootDirectory, target.ConfigPath, target.TriggerReason, target.Payload,
				PayloadHashSHA256(target.Payload),
				DefaultTargetMaxAttempts,
			); err != nil {
				return fmt.Errorf("webhook inbox insert target: %w", err)
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET routing_completed_at=clock_timestamp(),updated_at=clock_timestamp()
			 WHERE id=$1 AND state='processing' AND lease_token=$2
			   AND lease_expires_at > clock_timestamp()`,
			delivery.ID, delivery.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox mark plan complete: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		return nil
	})
}

func validateDispatchTarget(target DispatchTarget) error {
	if !validSHA256(strings.TrimSpace(target.TargetKey)) ||
		strings.TrimSpace(target.WorkspaceID) == "" ||
		strings.TrimSpace(target.ProjectID) == "" ||
		strings.TrimSpace(target.ServiceID) == "" ||
		target.InstallationID <= 0 ||
		target.InstallationGrantResourceVersion <= 0 ||
		target.RepositoryID <= 0 ||
		strings.TrimSpace(target.Repository) == "" ||
		target.RepositoryGrantResourceVersion <= 0 ||
		target.LinkResourceVersion <= 0 ||
		strings.TrimSpace(target.Branch) == "" ||
		strings.TrimSpace(target.CommitSHA) == "" ||
		len(target.Payload) == 0 ||
		len(target.Payload) > 256<<10 {
		return errors.New("webhook inbox: complete bounded target snapshot required")
	}
	timestamps := []time.Time{
		target.InstallationGrantCreatedAt,
		target.InstallationGrantUpdatedAt,
		target.RepositoryGrantCreatedAt,
		target.RepositoryGrantUpdatedAt,
		target.RepositoryVerifiedAt,
		target.LinkRepositoryVerifiedAt,
		target.LinkCreatedAt,
		target.LinkUpdatedAt,
	}
	for _, value := range timestamps {
		if value.IsZero() {
			return errors.New("webhook inbox: target authority timestamps required")
		}
	}
	return nil
}

func (s *PostgresDeliveryStore) ClaimTarget(ctx context.Context, delivery ClaimedDelivery) (ClaimedTarget, error) {
	if err := s.ready(); err != nil {
		return ClaimedTarget{}, err
	}
	leaseToken := s.newID("wtl")
	leaseSeconds := s.leaseSeconds()
	var target ClaimedTarget
	corrupt := false
	noWork := false
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		row := tx.QueryRow(ctx, `
			WITH exhausted AS (
				UPDATE webhook_delivery_targets
				   SET state='dead_letter',
				       reason_code='WEBHOOK_TARGET_ATTEMPTS_EXHAUSTED',
				       lease_token=NULL,
				       lease_expires_at=NULL,
				       terminal_at=clock_timestamp(),
				       updated_at=clock_timestamp()
				 WHERE delivery_id=$1
				   AND state='processing'
				   AND lease_expires_at <= clock_timestamp()
				   AND attempt >= max_attempts
				RETURNING id
			), candidate AS (
				SELECT target.id
				  FROM webhook_delivery_targets target
				  JOIN webhook_deliveries delivery ON delivery.id=target.delivery_id
				 WHERE target.delivery_id=$1
				   AND delivery.state='processing'
				   AND delivery.lease_token=$2
				   AND delivery.lease_expires_at > clock_timestamp()
				   AND target.attempt < target.max_attempts
				   AND target.available_at <= clock_timestamp()
				   AND (
					    target.state IN ('pending','retry_wait')
					    OR (target.state='processing' AND target.lease_expires_at <= clock_timestamp())
				   )
				 ORDER BY target.available_at,target.created_at,target.id
				 FOR UPDATE OF target SKIP LOCKED
				 LIMIT 1
			)
			UPDATE webhook_delivery_targets target
			   SET state='processing',
			       attempt=target.attempt+1,
			       lease_token=$3,
			       lease_expires_at=clock_timestamp()+make_interval(secs => $4),
			       updated_at=clock_timestamp()
			  FROM candidate
			 WHERE target.id=candidate.id
			RETURNING `+targetSummaryColumns("target")+`,
			          `+claimedTargetSnapshotColumns("target")+`,
			          target.lease_token,target.dispatch_payload,
			          target.dispatch_payload_sha256`,
			delivery.ID, delivery.LeaseToken, leaseToken, leaseSeconds,
		)
		if err := scanClaimedTarget(row, &target); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				noWork = true
				return nil
			}
			return fmt.Errorf("webhook inbox claim target: %w", err)
		}
		if PayloadHashSHA256(target.Payload) == target.PayloadSHA256 {
			return nil
		}
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_delivery_targets
			   SET state='dead_letter',
			       reason_code='WEBHOOK_TARGET_PAYLOAD_CORRUPT',
			       last_error='',
			       lease_token=NULL,
			       lease_expires_at=NULL,
			       terminal_at=clock_timestamp(),
			       updated_at=clock_timestamp()
			 WHERE id=$1
			   AND delivery_id=$2
			   AND state='processing'
			   AND lease_token=$3`,
			target.ID, delivery.ID, target.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox dead-letter corrupt target: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		corrupt = true
		return nil
	})
	if err != nil {
		return ClaimedTarget{}, err
	}
	if noWork {
		return ClaimedTarget{}, ErrInboxNoWork
	}
	if corrupt {
		return ClaimedTarget{}, ErrInboxPayloadCorrupt
	}
	return target, nil
}

func (s *PostgresDeliveryStore) CompleteTarget(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
	result TargetResult,
) error {
	return s.finishTarget(ctx, delivery, target, InboxSucceeded, "", "", result)
}

func (s *PostgresDeliveryStore) IgnoreTarget(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
	reasonCode string,
) error {
	return s.finishTarget(ctx, delivery, target, InboxIgnored, reasonCode, "", TargetResult{})
}

func (s *PostgresDeliveryStore) DeadLetterTarget(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
	reasonCode string,
	cause error,
) error {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	return s.finishTarget(
		ctx,
		delivery,
		target,
		InboxDeadLetter,
		reasonCode,
		lastError,
		TargetResult{},
	)
}

func (s *PostgresDeliveryStore) finishTarget(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
	state InboxState,
	reasonCode string,
	lastError string,
	result TargetResult,
) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE webhook_delivery_targets target
		   SET state=$1,reason_code=$2,last_error=$3,
		       operation_id=$4,deployment_id=$5,staged_outbox_id=$6,
		       lease_token=NULL,lease_expires_at=NULL,
		       terminal_at=clock_timestamp(),updated_at=clock_timestamp()
		 WHERE target.id=$7 AND target.delivery_id=$8
		   AND target.state='processing' AND target.lease_token=$9
		   AND target.lease_expires_at > clock_timestamp()
		   AND EXISTS (
		       SELECT 1 FROM webhook_deliveries delivery
		        WHERE delivery.id=$8 AND delivery.state='processing'
		          AND delivery.lease_token=$10
		          AND delivery.lease_expires_at > clock_timestamp()
		   )`,
		string(state), safeInboxReason(reasonCode), safeInboxError(lastError),
		strings.TrimSpace(result.OperationID), strings.TrimSpace(result.DeploymentID),
		strings.TrimSpace(result.StagedOutboxID),
		target.ID, delivery.ID, target.LeaseToken, delivery.LeaseToken,
	)
	if err != nil {
		return fmt.Errorf("webhook inbox finish target: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxLeaseLost
	}
	return nil
}

func (s *PostgresDeliveryStore) RetryTarget(
	ctx context.Context,
	delivery ClaimedDelivery,
	target ClaimedTarget,
	reasonCode string,
	cause error,
) error {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE webhook_delivery_targets target
		   SET state=CASE WHEN target.attempt >= target.max_attempts
		                  THEN 'dead_letter' ELSE 'retry_wait' END,
		       reason_code=$1,last_error=$2,
		       available_at=CASE WHEN target.attempt >= target.max_attempts
		                         THEN target.available_at
		                         ELSE clock_timestamp()+make_interval(
		                             secs => LEAST(3600, power(2,target.attempt)::INTEGER)
		                         ) END,
		       lease_token=NULL,lease_expires_at=NULL,
		       terminal_at=CASE WHEN target.attempt >= target.max_attempts
		                        THEN clock_timestamp() ELSE NULL END,
		       updated_at=clock_timestamp()
		 WHERE target.id=$3 AND target.delivery_id=$4
		   AND target.state='processing' AND target.lease_token=$5
		   AND target.lease_expires_at > clock_timestamp()
		   AND EXISTS (
		       SELECT 1 FROM webhook_deliveries delivery
		        WHERE delivery.id=$4 AND delivery.state='processing'
		          AND delivery.lease_token=$6
		          AND delivery.lease_expires_at > clock_timestamp()
		   )`,
		safeInboxReason(reasonCode), safeInboxError(lastError),
		target.ID, delivery.ID, target.LeaseToken, delivery.LeaseToken,
	)
	if err != nil {
		return fmt.Errorf("webhook inbox retry target: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxLeaseLost
	}
	return nil
}

func (s *PostgresDeliveryStore) AggregateDelivery(ctx context.Context, delivery ClaimedDelivery) error {
	return s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var succeeded, ignored, dead, retryable int
		var nextAvailable *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE state='succeeded'),
				COUNT(*) FILTER (WHERE state='ignored'),
				COUNT(*) FILTER (WHERE state='dead_letter'),
				COUNT(*) FILTER (WHERE state IN ('pending','retry_wait','processing')),
				MIN(
					CASE WHEN state='processing' THEN lease_expires_at
					     WHEN state IN ('pending','retry_wait') THEN available_at END
				)
			  FROM webhook_delivery_targets
			 WHERE delivery_id=$1`,
			delivery.ID,
		).Scan(&succeeded, &ignored, &dead, &retryable, &nextAvailable); err != nil {
			return fmt.Errorf("webhook inbox aggregate targets: %w", err)
		}
		state := InboxIgnored
		reasonCode := "all_targets_ignored"
		terminal := true
		availableAt := time.Time{}
		switch {
		case retryable > 0:
			state = InboxRetryWait
			reasonCode = ""
			terminal = false
			if nextAvailable != nil {
				availableAt = nextAvailable.UTC()
			}
		case dead > 0:
			state = InboxDeadLetter
			reasonCode = "WEBHOOK_TARGET_DEAD_LETTER"
		case succeeded > 0:
			state = InboxSucceeded
			reasonCode = ""
		case ignored > 0:
			state = InboxIgnored
		default:
			state = InboxDeadLetter
			reasonCode = "WEBHOOK_TARGETS_MISSING"
		}
		if availableAt.IsZero() {
			availableAt = delivery.AvailableAt.UTC()
		}
		tag, err := tx.Exec(ctx, `
			UPDATE webhook_deliveries
			   SET state=$1,reason_code=$2,last_error='',
			       available_at=$3,
			       lease_token=NULL,lease_expires_at=NULL,
			       terminal_at=CASE WHEN $4 THEN clock_timestamp() ELSE NULL END,
			       updated_at=clock_timestamp()
			 WHERE id=$5 AND state='processing' AND lease_token=$6
			   AND lease_expires_at > clock_timestamp()`,
			string(state), safeInboxReason(reasonCode), availableAt, terminal,
			delivery.ID, delivery.LeaseToken,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox aggregate delivery: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrInboxLeaseLost
		}
		return nil
	})
}

func (s *PostgresDeliveryStore) IgnoreDelivery(
	ctx context.Context,
	delivery ClaimedDelivery,
	reasonCode string,
) error {
	return s.finishDelivery(ctx, delivery, InboxIgnored, reasonCode, "")
}

func (s *PostgresDeliveryStore) DeadLetterDelivery(
	ctx context.Context,
	delivery ClaimedDelivery,
	reasonCode string,
	cause error,
) error {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	return s.finishDelivery(ctx, delivery, InboxDeadLetter, reasonCode, lastError)
}

func (s *PostgresDeliveryStore) finishDelivery(
	ctx context.Context,
	delivery ClaimedDelivery,
	state InboxState,
	reasonCode string,
	lastError string,
) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET state=$1,reason_code=$2,last_error=$3,
		       lease_token=NULL,lease_expires_at=NULL,
		       terminal_at=clock_timestamp(),updated_at=clock_timestamp()
		 WHERE id=$4 AND state='processing' AND lease_token=$5
		   AND lease_expires_at > clock_timestamp()`,
		string(state), safeInboxReason(reasonCode), safeInboxError(lastError),
		delivery.ID, delivery.LeaseToken,
	)
	if err != nil {
		return fmt.Errorf("webhook inbox finish delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxLeaseLost
	}
	return nil
}

func (s *PostgresDeliveryStore) RetryDelivery(
	ctx context.Context,
	delivery ClaimedDelivery,
	reasonCode string,
	cause error,
) error {
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET state=CASE WHEN route_attempt >= max_route_attempts
		                  THEN 'dead_letter' ELSE 'retry_wait' END,
		       reason_code=$1,last_error=$2,
		       available_at=CASE WHEN route_attempt >= max_route_attempts
		                         THEN available_at
		                         ELSE clock_timestamp()+make_interval(
		                             secs => LEAST(3600, power(2,route_attempt)::INTEGER)
		                         ) END,
		       lease_token=NULL,lease_expires_at=NULL,
		       terminal_at=CASE WHEN route_attempt >= max_route_attempts
		                        THEN clock_timestamp() ELSE NULL END,
		       updated_at=clock_timestamp()
		 WHERE id=$3 AND state='processing' AND lease_token=$4
		   AND lease_expires_at > clock_timestamp()`,
		safeInboxReason(reasonCode), safeInboxError(lastError),
		delivery.ID, delivery.LeaseToken,
	)
	if err != nil {
		return fmt.Errorf("webhook inbox retry delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInboxLeaseLost
	}
	return nil
}

func (s *PostgresDeliveryStore) PurgeTerminalPayloads(
	ctx context.Context,
	terminalBefore time.Time,
	limit int,
) (PayloadPurgeResult, error) {
	if err := s.ready(); err != nil {
		return PayloadPurgeResult{}, err
	}
	terminalBefore = terminalBefore.UTC()
	if terminalBefore.IsZero() {
		return PayloadPurgeResult{}, errors.New("webhook inbox: terminal payload purge cutoff is required")
	}
	if limit <= 0 || limit > 10_000 {
		limit = DefaultPayloadPurgeBatch
	}
	result := PayloadPurgeResult{}
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		deliveryTag, err := tx.Exec(ctx, `
			WITH candidates AS (
				SELECT id
				  FROM webhook_deliveries
				 WHERE state IN ('succeeded','ignored','dead_letter')
				   AND terminal_at <= $1
				   AND payload IS NOT NULL
				 ORDER BY terminal_at,id
				 FOR UPDATE SKIP LOCKED
				 LIMIT $2
			)
			UPDATE webhook_deliveries delivery
			   SET payload=NULL,updated_at=clock_timestamp()
			  FROM candidates
			 WHERE delivery.id=candidates.id`,
			terminalBefore, limit,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox purge terminal delivery payloads: %w", err)
		}
		targetTag, err := tx.Exec(ctx, `
			WITH candidates AS (
				SELECT id
				  FROM webhook_delivery_targets
				 WHERE state IN ('succeeded','ignored','dead_letter')
				   AND terminal_at <= $1
				   AND dispatch_payload IS NOT NULL
				 ORDER BY terminal_at,id
				 FOR UPDATE SKIP LOCKED
				 LIMIT $2
			)
			UPDATE webhook_delivery_targets target
			   SET dispatch_payload=NULL,updated_at=clock_timestamp()
			  FROM candidates
			 WHERE target.id=candidates.id`,
			terminalBefore, limit,
		)
		if err != nil {
			return fmt.Errorf("webhook inbox purge terminal target payloads: %w", err)
		}
		result.Deliveries = int(deliveryTag.RowsAffected())
		result.Targets = int(targetTag.RowsAffected())
		return nil
	})
	if err != nil {
		return PayloadPurgeResult{}, err
	}
	return result, nil
}

func (s *PostgresDeliveryStore) ListDeliveries(
	ctx context.Context,
	filter DeliveryFilter,
) ([]DeliverySummary, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+deliverySummaryColumns("delivery")+`
		  FROM webhook_deliveries delivery
		 WHERE ($1='' OR delivery.state=$1)
		   AND ($2='' OR delivery.provider=$2)
		   AND ($3='' OR EXISTS (
		       SELECT 1
		         FROM webhook_delivery_targets target
		        WHERE target.delivery_id=delivery.id
		          AND target.project_id=$3
		   ))
		 ORDER BY delivery.received_at DESC,delivery.id DESC
		 LIMIT $4`,
		string(filter.State), string(filter.Provider), strings.TrimSpace(filter.ProjectID), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("webhook inbox list deliveries: %w", err)
	}
	defer rows.Close()
	var out []DeliverySummary
	for rows.Next() {
		var value DeliverySummary
		if err := scanDeliverySummary(rows, &value); err != nil {
			return nil, fmt.Errorf("webhook inbox scan delivery: %w", err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("webhook inbox read deliveries: %w", err)
	}
	return out, nil
}

func (s *PostgresDeliveryStore) GetDelivery(ctx context.Context, id string) (DeliveryDetail, error) {
	if err := s.ready(); err != nil {
		return DeliveryDetail{}, err
	}
	return s.getDelivery(ctx, s.Pool, id)
}

type deliveryDetailQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *PostgresDeliveryStore) getDelivery(
	ctx context.Context,
	querier deliveryDetailQuerier,
	id string,
) (DeliveryDetail, error) {
	detail := DeliveryDetail{Targets: []TargetSummary{}}
	if err := scanDeliverySummary(querier.QueryRow(ctx, `
		SELECT `+deliverySummaryColumns("delivery")+`
		  FROM webhook_deliveries delivery
		 WHERE delivery.id=$1`,
		strings.TrimSpace(id),
	), &detail.DeliverySummary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryDetail{}, ErrInboxDeliveryNotFound
		}
		return DeliveryDetail{}, fmt.Errorf("webhook inbox get delivery: %w", err)
	}
	rows, err := querier.Query(ctx, `
		SELECT `+targetSummaryColumns("target")+`
		  FROM webhook_delivery_targets target
		 WHERE target.delivery_id=$1
		 ORDER BY target.created_at,target.id`,
		detail.ID,
	)
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("webhook inbox list targets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target TargetSummary
		if err := scanTargetSummary(rows, &target); err != nil {
			return DeliveryDetail{}, fmt.Errorf("webhook inbox scan target: %w", err)
		}
		detail.Targets = append(detail.Targets, target)
	}
	if err := rows.Err(); err != nil {
		return DeliveryDetail{}, fmt.Errorf("webhook inbox read targets: %w", err)
	}
	return detail, nil
}

func (s *PostgresDeliveryStore) RedriveDelivery(ctx context.Context, id string) (DeliveryDetail, error) {
	if err := s.ready(); err != nil {
		return DeliveryDetail{}, err
	}
	var updated DeliveryDetail
	err := s.Pool.WithTx(ctx, func(tx *dbpg.Tx) error {
		var txErr error
		_, updated, txErr = s.RedriveDeliveryInTx(ctx, tx, id)
		return txErr
	})
	if err != nil {
		return DeliveryDetail{}, err
	}
	return updated, nil
}

// RedriveDeliveryInTx validates retained payload integrity, resets only the
// dead-letter work in one caller-owned transaction, and returns metadata-only
// before/after projections. API coordinators use this seam so the mutation,
// idempotent response, and audit row commit or roll back together.
func (s *PostgresDeliveryStore) RedriveDeliveryInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	id string,
) (DeliveryDetail, DeliveryDetail, error) {
	return s.redriveDeliveryInTx(ctx, tx, id, "")
}

// RedriveProjectDeliveryInTx is the project-scoped recovery boundary. Target
// ownership is checked from rows locked by the same transaction that resets
// the delivery, so a handler-side projection can never authorize a mixed or
// indeterminate delivery by itself.
func (s *PostgresDeliveryStore) RedriveProjectDeliveryInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	id string,
	projectID string,
) (DeliveryDetail, DeliveryDetail, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return DeliveryDetail{}, DeliveryDetail{}, ErrInboxDeliveryScopeMismatch
	}
	return s.redriveDeliveryInTx(ctx, tx, id, projectID)
}

func (s *PostgresDeliveryStore) redriveDeliveryInTx(
	ctx context.Context,
	tx *dbpg.Tx,
	id string,
	projectID string,
) (DeliveryDetail, DeliveryDetail, error) {
	if err := s.ready(); err != nil {
		return DeliveryDetail{}, DeliveryDetail{}, err
	}
	if tx == nil {
		return DeliveryDetail{}, DeliveryDetail{}, errors.New("webhook inbox redrive transaction is required")
	}
	id = strings.TrimSpace(id)
	var state string
	var routingCompletedAt *time.Time
	var payload []byte
	var payloadSHA256 string
	if err := tx.QueryRow(ctx, `
		SELECT state,routing_completed_at,payload,payload_sha256
		  FROM webhook_deliveries
		 WHERE id=$1
		 FOR UPDATE`,
		id,
	).Scan(&state, &routingCompletedAt, &payload, &payloadSHA256); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryDetail{}, DeliveryDetail{}, ErrInboxDeliveryNotFound
		}
		return DeliveryDetail{}, DeliveryDetail{}, fmt.Errorf("webhook inbox lock redrive: %w", err)
	}
	if state != string(InboxDeadLetter) {
		return DeliveryDetail{}, DeliveryDetail{}, ErrInboxDeliveryTerminal
	}
	if payload == nil || PayloadHashSHA256(payload) != payloadSHA256 {
		return DeliveryDetail{}, DeliveryDetail{}, ErrInboxPayloadCorrupt
	}
	targetRows, err := tx.Query(ctx, `
		SELECT id,dispatch_payload,dispatch_payload_sha256
		  FROM webhook_delivery_targets
		 WHERE delivery_id=$1 AND state='dead_letter'
		 ORDER BY id
		 FOR UPDATE`,
		id,
	)
	if err != nil {
		return DeliveryDetail{}, DeliveryDetail{},
			fmt.Errorf("webhook inbox lock dead target payloads: %w", err)
	}
	for targetRows.Next() {
		var targetID string
		var targetPayload []byte
		var targetPayloadSHA256 string
		if err := targetRows.Scan(
			&targetID,
			&targetPayload,
			&targetPayloadSHA256,
		); err != nil {
			targetRows.Close()
			return DeliveryDetail{}, DeliveryDetail{},
				fmt.Errorf("webhook inbox scan dead target payload: %w", err)
		}
		if targetPayload == nil ||
			PayloadHashSHA256(targetPayload) != targetPayloadSHA256 {
			targetRows.Close()
			return DeliveryDetail{}, DeliveryDetail{},
				fmt.Errorf("%w: target %s", ErrInboxPayloadCorrupt, targetID)
		}
	}
	if err := targetRows.Err(); err != nil {
		targetRows.Close()
		return DeliveryDetail{}, DeliveryDetail{},
			fmt.Errorf("webhook inbox read dead target payloads: %w", err)
	}
	targetRows.Close()
	before, err := s.getDelivery(ctx, tx, id)
	if err != nil {
		return DeliveryDetail{}, DeliveryDetail{}, err
	}
	if projectID != "" && !deliveryTargetsExclusiveToProject(before, projectID) {
		return DeliveryDetail{}, DeliveryDetail{}, ErrInboxDeliveryScopeMismatch
	}
	tag, err := tx.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET state='pending',attempt=0,manual_retries=manual_retries+1,
		       available_at=clock_timestamp(),
		       lease_token=NULL,lease_expires_at=NULL,
		       reason_code='',last_error='',terminal_at=NULL,
		       updated_at=clock_timestamp()
		 WHERE delivery_id=$1 AND state='dead_letter'`,
		id,
	)
	if err != nil {
		return DeliveryDetail{}, DeliveryDetail{}, fmt.Errorf("webhook inbox reset dead targets: %w", err)
	}
	if routingCompletedAt != nil && tag.RowsAffected() == 0 {
		// A planner-level dead letter has no targets and must be replanned.
		routingCompletedAt = nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET state='pending',route_attempt=0,manual_retries=manual_retries+1,
		       available_at=clock_timestamp(),
		       lease_token=NULL,lease_expires_at=NULL,
		       routing_completed_at=$2,
		       reason_code='',last_error='',terminal_at=NULL,
		       updated_at=clock_timestamp()
		 WHERE id=$1`,
		id, routingCompletedAt,
	); err != nil {
		return DeliveryDetail{}, DeliveryDetail{}, fmt.Errorf("webhook inbox reset delivery: %w", err)
	}
	after, err := s.getDelivery(ctx, tx, id)
	if err != nil {
		return DeliveryDetail{}, DeliveryDetail{}, err
	}
	return before, after, nil
}

func deliveryTargetsExclusiveToProject(detail DeliveryDetail, projectID string) bool {
	if strings.TrimSpace(projectID) == "" || len(detail.Targets) == 0 {
		return false
	}
	for _, target := range detail.Targets {
		if target.ProjectID != projectID {
			return false
		}
	}
	return true
}

func deliverySummaryColumns(alias string) string {
	return strings.Join([]string{
		alias + ".id",
		alias + ".provider",
		alias + ".endpoint_id",
		alias + ".provider_delivery_id",
		alias + ".event_type",
		alias + ".event_id",
		alias + ".event_hash",
		alias + ".payload_sha256",
		alias + ".payload_bytes",
		alias + ".state",
		alias + ".route_attempt",
		alias + ".max_route_attempts",
		alias + ".manual_retries",
		alias + ".reason_code",
		alias + ".last_error",
		alias + ".replay_count",
		alias + ".received_at",
		alias + ".last_received_at",
		alias + ".available_at",
		alias + ".routing_completed_at",
		alias + ".terminal_at",
		alias + ".updated_at",
	}, ",")
}

func scanDeliverySummary(row interface{ Scan(...any) error }, value *DeliverySummary) error {
	var provider, state string
	if err := row.Scan(
		&value.ID,
		&provider,
		&value.EndpointID,
		&value.ProviderDeliveryID,
		&value.EventType,
		&value.EventID,
		&value.EventHash,
		&value.PayloadSHA256,
		&value.PayloadBytes,
		&state,
		&value.RouteAttempt,
		&value.MaxRouteAttempts,
		&value.ManualRetries,
		&value.ReasonCode,
		&value.LastError,
		&value.ReplayCount,
		&value.ReceivedAt,
		&value.LastReceivedAt,
		&value.AvailableAt,
		&value.RoutingCompletedAt,
		&value.TerminalAt,
		&value.UpdatedAt,
	); err != nil {
		return err
	}
	value.Provider = Provider(provider)
	value.State = InboxState(state)
	value.ReceivedAt = value.ReceivedAt.UTC()
	value.LastReceivedAt = value.LastReceivedAt.UTC()
	value.AvailableAt = value.AvailableAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if value.RoutingCompletedAt != nil {
		at := value.RoutingCompletedAt.UTC()
		value.RoutingCompletedAt = &at
	}
	if value.TerminalAt != nil {
		at := value.TerminalAt.UTC()
		value.TerminalAt = &at
	}
	return nil
}

func scanClaimedDelivery(row interface{ Scan(...any) error }, value *ClaimedDelivery) error {
	var provider, state string
	if err := row.Scan(
		&value.ID,
		&provider,
		&value.EndpointID,
		&value.ProviderDeliveryID,
		&value.EventType,
		&value.EventID,
		&value.EventHash,
		&value.PayloadSHA256,
		&value.PayloadBytes,
		&state,
		&value.RouteAttempt,
		&value.MaxRouteAttempts,
		&value.ManualRetries,
		&value.ReasonCode,
		&value.LastError,
		&value.ReplayCount,
		&value.ReceivedAt,
		&value.LastReceivedAt,
		&value.AvailableAt,
		&value.RoutingCompletedAt,
		&value.TerminalAt,
		&value.UpdatedAt,
		&value.LeaseToken,
		&value.Payload,
	); err != nil {
		return err
	}
	value.Provider = Provider(provider)
	value.State = InboxState(state)
	return nil
}

func targetSummaryColumns(alias string) string {
	return strings.Join([]string{
		alias + ".id",
		alias + ".target_key",
		alias + ".workspace_id",
		alias + ".project_id",
		alias + ".service_id",
		alias + ".installation_id",
		alias + ".installation_grant_resource_version",
		alias + ".repository_id",
		alias + ".repository_full_name",
		alias + ".repository_grant_resource_version",
		alias + ".link_resource_version",
		alias + ".branch",
		alias + ".commit_sha",
		alias + ".state",
		alias + ".attempt",
		alias + ".max_attempts",
		alias + ".manual_retries",
		alias + ".reason_code",
		alias + ".last_error",
		"COALESCE(" + alias + ".operation_id,'')",
		"COALESCE(" + alias + ".deployment_id,'')",
		"COALESCE(" + alias + ".staged_outbox_id,'')",
		alias + ".available_at",
		alias + ".terminal_at",
		alias + ".created_at",
		alias + ".updated_at",
	}, ",")
}

func claimedTargetSnapshotColumns(alias string) string {
	return strings.Join([]string{
		alias + ".installation_grant_created_at",
		alias + ".installation_grant_updated_at",
		alias + ".repository_grant_created_at",
		alias + ".repository_grant_updated_at",
		alias + ".repository_access_verified_at",
		alias + ".link_created_at",
		alias + ".link_updated_at",
		alias + ".link_repository_access_verified_at",
		alias + ".ref",
		alias + ".clone_url",
		alias + ".root_directory",
		alias + ".config_path",
		alias + ".trigger_reason",
	}, ",")
}

func scanTargetSummary(row interface{ Scan(...any) error }, value *TargetSummary) error {
	var state string
	if err := row.Scan(
		&value.ID,
		&value.TargetKey,
		&value.WorkspaceID,
		&value.ProjectID,
		&value.ServiceID,
		&value.InstallationID,
		&value.InstallationGrantResourceVersion,
		&value.RepositoryID,
		&value.Repository,
		&value.RepositoryGrantResourceVersion,
		&value.LinkResourceVersion,
		&value.Branch,
		&value.CommitSHA,
		&state,
		&value.Attempt,
		&value.MaxAttempts,
		&value.ManualRetries,
		&value.ReasonCode,
		&value.LastError,
		&value.OperationID,
		&value.DeploymentID,
		&value.StagedOutboxID,
		&value.AvailableAt,
		&value.TerminalAt,
		&value.CreatedAt,
		&value.UpdatedAt,
	); err != nil {
		return err
	}
	value.State = InboxState(state)
	value.AvailableAt = value.AvailableAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if value.TerminalAt != nil {
		at := value.TerminalAt.UTC()
		value.TerminalAt = &at
	}
	return nil
}

func scanClaimedTarget(row interface{ Scan(...any) error }, value *ClaimedTarget) error {
	var state string
	if err := row.Scan(
		&value.ID,
		&value.TargetKey,
		&value.WorkspaceID,
		&value.ProjectID,
		&value.ServiceID,
		&value.InstallationID,
		&value.InstallationGrantResourceVersion,
		&value.RepositoryID,
		&value.Repository,
		&value.RepositoryGrantResourceVersion,
		&value.LinkResourceVersion,
		&value.Branch,
		&value.CommitSHA,
		&state,
		&value.Attempt,
		&value.MaxAttempts,
		&value.ManualRetries,
		&value.ReasonCode,
		&value.LastError,
		&value.OperationID,
		&value.DeploymentID,
		&value.StagedOutboxID,
		&value.AvailableAt,
		&value.TerminalAt,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.InstallationGrantCreatedAt,
		&value.InstallationGrantUpdatedAt,
		&value.RepositoryGrantCreatedAt,
		&value.RepositoryGrantUpdatedAt,
		&value.RepositoryVerifiedAt,
		&value.LinkCreatedAt,
		&value.LinkUpdatedAt,
		&value.LinkRepositoryVerifiedAt,
		&value.Ref,
		&value.CloneURL,
		&value.RootDirectory,
		&value.ConfigPath,
		&value.TriggerReason,
		&value.LeaseToken,
		&value.Payload,
		&value.PayloadSHA256,
	); err != nil {
		return err
	}
	value.State = InboxState(state)
	value.InstallationGrantCreatedAt = value.InstallationGrantCreatedAt.UTC()
	value.InstallationGrantUpdatedAt = value.InstallationGrantUpdatedAt.UTC()
	value.RepositoryGrantCreatedAt = value.RepositoryGrantCreatedAt.UTC()
	value.RepositoryGrantUpdatedAt = value.RepositoryGrantUpdatedAt.UTC()
	value.RepositoryVerifiedAt = value.RepositoryVerifiedAt.UTC()
	value.LinkCreatedAt = value.LinkCreatedAt.UTC()
	value.LinkUpdatedAt = value.LinkUpdatedAt.UTC()
	value.LinkRepositoryVerifiedAt = value.LinkRepositoryVerifiedAt.UTC()
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func PayloadHashSHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func safeInboxError(value string) string {
	value = redaction.DefaultRedactor().RedactString(strings.TrimSpace(value))
	var builder strings.Builder
	for _, current := range value {
		if current < 0x20 || current == 0x7f {
			builder.WriteByte(' ')
		} else {
			builder.WriteRune(current)
		}
		if builder.Len() >= 512 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func safeInboxReason(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, current := range value {
		if current <= 0x20 || current == 0x7f {
			builder.WriteByte('_')
		} else {
			builder.WriteRune(current)
		}
		if builder.Len() >= 128 {
			break
		}
	}
	return strings.Trim(builder.String(), "_")
}

var _ DurableInbox = (*PostgresDeliveryStore)(nil)
