// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const (
	defaultDeployOutboxLease       = 30 * time.Second
	defaultDeployOutboxPoll        = 250 * time.Millisecond
	defaultDeployOutboxMaxAttempts = 8
)

var (
	// ErrDeployOutboxNoWork is returned when no delivery intent is eligible.
	ErrDeployOutboxNoWork = errors.New("deploy outbox: no work available")
	// ErrDeployOutboxLeaseLost means another dispatcher reclaimed the intent.
	ErrDeployOutboxLeaseLost = errors.New("deploy outbox: lease lost")
)

// DurableDeployIntake is the production seam that closes the crash window
// between committing deploy product truth and enqueueing worker work.
type DurableDeployIntake interface {
	CommitDeployIntake(ctx context.Context, request DeployIntakeCommit) (DeployIntakeCommitResult, error)
	DispatchDeployIntake(ctx context.Context, limit int) (int, error)
}

// DeployIntakeCommit contains only server-derived, already-validated data.
// Implementations must commit every field atomically or commit nothing.
type DeployIntakeCommit struct {
	Operation      orailtypes.Operation
	OperationActor string
	TargetKind     string
	TargetID       string
	Deployment     DeploymentRecord
	IdempotencyKey idempotency.Key
	IdempotencyRef idempotency.Reference
	// Response is the exact accepted representation committed with the
	// idempotency attempt. Compatibility callers may leave it empty, in which
	// case the historical status-only 202 response is retained.
	Response       idempotency.StoredResponse
	Enqueue        queue.EnqueueRequest
	InitialMessage string
	Now            time.Time
}

// DeployIntakeCommitResult identifies the durable intent. JobID is deliberately
// absent: at commit time the outbox, not the worker queue, owns the intent.
type DeployIntakeCommitResult struct {
	Operation  orailtypes.Operation
	Deployment DeploymentRecord
	OutboxID   string
}

// PostgresDeployOutbox atomically accepts deploy intents and idempotently
// delivers them to the Postgres jobs queue.
type PostgresDeployOutbox struct {
	Pool      *pg.Pool
	Now       func() time.Time
	LeaseTime time.Duration
	WorkerID  string
	IDFactory func(prefix string) string
}

// NewPostgresDeployOutbox constructs the production deploy intake/outbox.
func NewPostgresDeployOutbox(pool *pg.Pool) *PostgresDeployOutbox {
	return &PostgresDeployOutbox{
		Pool:      pool,
		Now:       func() time.Time { return time.Now().UTC() },
		LeaseTime: defaultDeployOutboxLease,
		WorkerID:  "api-" + randomOutboxID("dispatcher"),
		IDFactory: randomOutboxID,
	}
}

func (s *PostgresDeployOutbox) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *PostgresDeployOutbox) ready() error {
	if s == nil || s.Pool == nil {
		return errors.New("PostgresDeployOutbox: pool not initialized")
	}
	return nil
}

func (s *PostgresDeployOutbox) newID(prefix string) string {
	if s != nil && s.IDFactory != nil {
		return s.IDFactory(prefix)
	}
	return randomOutboxID(prefix)
}

// CommitDeployIntake commits the complete intake boundary in one transaction:
// operation, canonical deployment, initial ledgers, idempotency response, and
// outbox intent. No worker can observe partial state.
func (s *PostgresDeployOutbox) CommitDeployIntake(ctx context.Context, request DeployIntakeCommit) (DeployIntakeCommitResult, error) {
	if err := ctx.Err(); err != nil {
		return DeployIntakeCommitResult{}, err
	}
	if err := s.ready(); err != nil {
		return DeployIntakeCommitResult{}, err
	}
	request = s.prepareDeployIntakeCommit(request)
	if err := validateDeployIntakeCommit(request); err != nil {
		return DeployIntakeCommitResult{}, err
	}

	outboxID := s.newID("dox")
	deploymentEventID := s.newID("dev")

	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		_, err := s.CommitDeployIntakeInTx(ctx, tx, request, outboxID, deploymentEventID)
		return err
	})
	if err != nil {
		return DeployIntakeCommitResult{}, err
	}
	return DeployIntakeCommitResult{
		Operation:  request.Operation,
		Deployment: request.Deployment,
		OutboxID:   outboxID,
	}, nil
}

// CommitDeployIntakeInTx writes the complete durable intake through a
// caller-owned transaction. It never begins or commits a transaction. Tenant
// coordinators use this seam after locking and revalidating current authority,
// reserving actor/project-scoped idempotency, and resolving canonical active
// state in the same SERIALIZABLE transaction.
//
// outboxID and deploymentEventID are caller-supplied so a transaction retry
// retains one logical identity for every accepted artifact.
func (s *PostgresDeployOutbox) CommitDeployIntakeInTx(
	ctx context.Context,
	tx *pg.Tx,
	request DeployIntakeCommit,
	outboxID string,
	deploymentEventID string,
) (DeployIntakeCommitResult, error) {
	return s.commitDeployIntakeInTx(ctx, tx, request, outboxID, deploymentEventID, true)
}

// CommitGroupedDeployIntakeInTx writes the same canonical operation,
// deployment, event, and durable queue handoff as ordinary intake, but leaves
// the caller-owned idempotency reservation open. Aggregate workflows use this
// seam to admit multiple service builds and then commit one exact group
// response beside the parent request.
func (s *PostgresDeployOutbox) CommitGroupedDeployIntakeInTx(
	ctx context.Context,
	tx *pg.Tx,
	request DeployIntakeCommit,
	outboxID string,
	deploymentEventID string,
) (DeployIntakeCommitResult, error) {
	return s.commitDeployIntakeInTx(ctx, tx, request, outboxID, deploymentEventID, false)
}

func (s *PostgresDeployOutbox) commitDeployIntakeInTx(
	ctx context.Context,
	tx *pg.Tx,
	request DeployIntakeCommit,
	outboxID string,
	deploymentEventID string,
	completeIdempotency bool,
) (DeployIntakeCommitResult, error) {
	if err := ctx.Err(); err != nil {
		return DeployIntakeCommitResult{}, err
	}
	if err := s.ready(); err != nil {
		return DeployIntakeCommitResult{}, err
	}
	if tx == nil {
		return DeployIntakeCommitResult{}, errors.New("deploy intake commit: transaction is required")
	}
	request = s.prepareDeployIntakeCommit(request)
	if err := validateDeployIntakeCommit(request); err != nil {
		return DeployIntakeCommitResult{}, err
	}
	if strings.TrimSpace(outboxID) == "" || strings.TrimSpace(deploymentEventID) == "" {
		return DeployIntakeCommitResult{}, errors.New("deploy intake commit: stable outbox and event ids are required")
	}

	operation := request.Operation
	deployment := request.Deployment
	now := request.Now
	expiresAt := now.Add(24 * time.Hour)
	if _, err := tx.Exec(ctx,
		`INSERT INTO operations
		    (id, kind, target_resource_kind, target_resource_id, state, code, reason,
		     actor, idempotency_key, expires_at, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,'QUEUED',NULL,NULL,$5,$6,$7,$8,$8)`,
		string(operation.ID), operation.Kind, request.TargetKind, request.TargetID,
		request.OperationActor, string(request.IdempotencyKey), expiresAt, now,
	); err != nil {
		return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake operation insert: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO deployments (
		    id, project_id, service_id, environment_id, action, source_type, repo, ref, sha,
		    root_directory, dockerfile_path, builder, image_ref, image_digest, state, active,
		    operation_id, trigger_key, rollback_source_deployment_id, expected_active_deployment_id,
		    created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'queued',FALSE,
		    $15,$16,$17,$18,$19,$19)`,
		deployment.ID, deployment.ProjectID, deployment.ServiceID, deployment.EnvironmentID,
		string(deployment.Action), deployment.SourceType, deployment.Repo, deployment.Ref,
		deployment.SHA, deployment.RootDirectory, deployment.DockerfilePath, deployment.Builder,
		deployment.ImageRef, deployment.ImageDigest, string(operation.ID), deployment.TriggerKey,
		nullableString(deployment.RollbackSourceID), nullableString(deployment.ExpectedActiveID), now,
	); err != nil {
		return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake deployment insert: %w", err)
	}

	message := strings.TrimSpace(request.InitialMessage)
	if message == "" {
		message = "deploy intent durably accepted"
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO operation_events (id, operation_id, seq, state, message, created_at)
		 VALUES ($1,$2,1,'QUEUED',$3,$4)`,
		fmt.Sprintf("%s-%d", operation.ID, 1), string(operation.ID), message, now,
	); err != nil {
		return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake operation event insert: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO deployment_events
		    (id, deployment_id, seq, event_type, state, message, data, created_at)
		 VALUES ($1,$2,1,'state','queued',$3,'{}'::jsonb,$4)`,
		deploymentEventID, deployment.ID, message, now,
	); err != nil {
		return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake deployment event insert: %w", err)
	}

	if completeIdempotency {
		idempotencyStore := idempotency.NewPostgresStore(s.Pool)
		idempotencyStore.Now = s.Now
		if _, err := idempotencyStore.AttachOperationInTx(ctx, tx, request.IdempotencyRef, string(operation.ID)); err != nil {
			return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake idempotency attach: %w", err)
		}
		if _, err := idempotencyStore.CompleteSuccessInTx(ctx, tx, request.IdempotencyRef, request.Response); err != nil {
			return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake idempotency complete: %w", err)
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO deploy_intake_outbox
		    (id, trigger_key, operation_id, deployment_id, queue, partition_key,
		     priority, available_at, payload, state, max_attempts, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,'pending',$10,$11,$11)`,
		outboxID, deployment.TriggerKey, string(operation.ID), deployment.ID,
		string(request.Enqueue.Queue), request.Enqueue.PartitionKey, request.Enqueue.Priority,
		request.Enqueue.AvailableAt.UTC(), string(request.Enqueue.Payload),
		defaultDeployOutboxMaxAttempts, now,
	); err != nil {
		return DeployIntakeCommitResult{}, fmt.Errorf("deploy intake outbox insert: %w", err)
	}
	return DeployIntakeCommitResult{
		Operation:  operation,
		Deployment: deployment,
		OutboxID:   outboxID,
	}, nil
}

func (s *PostgresDeployOutbox) prepareDeployIntakeCommit(request DeployIntakeCommit) DeployIntakeCommit {
	now := request.Now.UTC()
	if now.IsZero() {
		now = s.now()
	}
	request.Now = now
	request.Operation.State = orailtypes.OperationPending
	request.Deployment.State = deploy.DeploymentQueued
	request.Deployment.Active = false
	request.Deployment.CreatedAt = now
	request.Deployment.UpdatedAt = now
	if request.Response.StatusCode == 0 {
		request.Response = idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Header:     make(http.Header),
		}
	} else {
		request.Response = request.Response.Clone()
		if request.Response.Header == nil {
			request.Response.Header = make(http.Header)
		}
	}
	return request
}

func validateDeployIntakeCommit(request DeployIntakeCommit) error {
	if request.Operation.ID == "" || strings.TrimSpace(request.Operation.Kind) == "" {
		return errors.New("deploy intake commit: operation id and kind are required")
	}
	operation := request.Operation
	operation.State = orailtypes.OperationQueued
	if err := operation.Validate(); err != nil {
		return fmt.Errorf("deploy intake commit: invalid operation: %w", err)
	}
	if request.Deployment.ID == "" ||
		request.Deployment.ProjectID == "" ||
		request.Deployment.ServiceID == "" ||
		request.Deployment.EnvironmentID == "" ||
		request.Deployment.TriggerKey == "" {
		return errors.New("deploy intake commit: deployment identity, scope, and trigger are required")
	}
	if !deploy.ValidAction(request.Deployment.Action) {
		return fmt.Errorf("deploy intake commit: invalid deployment action %q", request.Deployment.Action)
	}
	if request.IdempotencyKey == "" {
		return errors.New("deploy intake commit: idempotency key is required")
	}
	if err := idempotency.ValidateKey(request.IdempotencyKey); err != nil {
		return fmt.Errorf("deploy intake commit: invalid idempotency key: %w", err)
	}
	if request.Enqueue.Queue != queue.Build && request.Enqueue.Queue != queue.Deploy {
		return fmt.Errorf("deploy intake commit: unsupported queue %q", request.Enqueue.Queue)
	}
	if len(request.Enqueue.Payload) == 0 || !json.Valid(request.Enqueue.Payload) {
		return errors.New("deploy intake commit: enqueue payload must be valid JSON")
	}
	if request.Enqueue.ParentOperationID != string(request.Operation.ID) {
		return errors.New("deploy intake commit: parent operation does not match")
	}
	if request.Deployment.OperationID != string(request.Operation.ID) {
		return errors.New("deploy intake commit: deployment operation does not match")
	}
	if request.IdempotencyRef.Key != request.IdempotencyKey {
		return errors.New("deploy intake commit: idempotency reference does not match key")
	}
	if (strings.TrimSpace(request.TargetKind) == "") != (strings.TrimSpace(request.TargetID) == "") {
		return errors.New("deploy intake commit: operation target kind and id must be supplied together")
	}
	if request.Response.StatusCode < http.StatusOK || request.Response.StatusCode >= http.StatusMultipleChoices {
		return errors.New("deploy intake commit: accepted response must be a 2xx status")
	}
	return nil
}

type deployOutboxClaim struct {
	ID           string
	LeaseOwner   string
	OperationID  string
	DeploymentID string
	Queue        queue.Name
	PartitionKey string
	Priority     int
	AvailableAt  time.Time
	Payload      []byte
	Attempt      int
	MaxAttempts  int
}

// DispatchDeployIntake delivers up to limit pending intents. Claim and
// delivery are separate transactions; insertion into jobs and marking the
// outbox delivered are one transaction. Thus both crashes before insert and
// crashes after insert are retry-safe.
func (s *PostgresDeployOutbox) DispatchDeployIntake(ctx context.Context, limit int) (int, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 1
	}
	delivered := 0
	for delivered < limit {
		claim, err := s.claim(ctx)
		if errors.Is(err, ErrDeployOutboxNoWork) {
			return delivered, nil
		}
		if err != nil {
			return delivered, err
		}
		if err := s.deliver(ctx, claim); err != nil {
			_ = s.recordDeliveryFailure(ctx, claim, err)
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

func (s *PostgresDeployOutbox) claim(ctx context.Context) (deployOutboxClaim, error) {
	now := s.now()
	leaseTime := s.LeaseTime
	if leaseTime <= 0 {
		leaseTime = defaultDeployOutboxLease
	}
	leaseOwner := strings.TrimSpace(s.WorkerID) + ":" + randomOutboxID("lease")
	var claim deployOutboxClaim
	var queueName string
	err := s.Pool.QueryRow(ctx,
		`WITH candidate AS (
		    SELECT id
		      FROM deploy_intake_outbox
		     WHERE attempt < max_attempts
		       AND available_at <= $1
		       AND (
		            state = 'pending'
		            OR (state = 'delivering' AND lease_expires_at < $1)
		       )
		     ORDER BY available_at ASC, created_at ASC
		     FOR UPDATE SKIP LOCKED
		     LIMIT 1
		 )
		 UPDATE deploy_intake_outbox o
		    SET state = 'delivering',
		        attempt = o.attempt + 1,
		        lease_owner = $2,
		        lease_expires_at = $3,
		        updated_at = $1
		   FROM candidate
		  WHERE o.id = candidate.id
		 RETURNING o.id, o.lease_owner, o.operation_id, o.deployment_id, o.queue,
		           o.partition_key, o.priority, o.available_at, o.payload, o.attempt, o.max_attempts`,
		now, leaseOwner, now.Add(leaseTime),
	).Scan(
		&claim.ID, &claim.LeaseOwner, &claim.OperationID, &claim.DeploymentID,
		&queueName, &claim.PartitionKey, &claim.Priority, &claim.AvailableAt,
		&claim.Payload, &claim.Attempt, &claim.MaxAttempts,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return deployOutboxClaim{}, ErrDeployOutboxNoWork
	}
	if err != nil {
		return deployOutboxClaim{}, fmt.Errorf("deploy outbox claim: %w", err)
	}
	claim.Queue = queue.Name(queueName)
	return claim, nil
}

func (s *PostgresDeployOutbox) deliver(ctx context.Context, claim deployOutboxClaim) error {
	now := s.now()
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		jobID := randomOutboxID("job")
		if _, err := tx.Exec(ctx,
			`INSERT INTO jobs
			    (id, queue, state, partition_key, parent_operation_id, priority,
			     available_at, payload, outbox_id, created_at, updated_at)
			 VALUES ($1,$2,'pending',$3,$4,$5,$6,$7::jsonb,$8,$9,$9)
			 ON CONFLICT (outbox_id) WHERE outbox_id IS NOT NULL DO NOTHING`,
			jobID, string(claim.Queue), claim.PartitionKey, claim.OperationID,
			claim.Priority, claim.AvailableAt.UTC(), string(claim.Payload), claim.ID, now,
		); err != nil {
			return fmt.Errorf("deploy outbox job insert: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT id FROM jobs WHERE outbox_id = $1`, claim.ID).Scan(&jobID); err != nil {
			return fmt.Errorf("deploy outbox job lookup: %w", err)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE deploy_intake_outbox
			    SET state = 'delivered',
			        job_id = $1,
			        delivered_at = $2,
			        lease_owner = NULL,
			        lease_expires_at = NULL,
			        last_error = '',
			        updated_at = $2
			  WHERE id = $3
			    AND state = 'delivering'
			    AND lease_owner = $4`,
			jobID, now, claim.ID, claim.LeaseOwner,
		)
		if err != nil {
			return fmt.Errorf("deploy outbox delivery mark: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrDeployOutboxLeaseLost
		}
		if err := appendOperationEventTx(ctx, tx, orailtypes.ID(claim.OperationID), orailtypes.OperationPending,
			fmt.Sprintf("%s job delivered from durable outbox: job_id=%s outbox_id=%s", claim.Queue, jobID, claim.ID), now); err != nil {
			return err
		}
		return nil
	})
}

func (s *PostgresDeployOutbox) recordDeliveryFailure(ctx context.Context, claim deployOutboxClaim, deliveryErr error) error {
	now := s.now()
	message := boundedOutboxError(deliveryErr)
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		state := "pending"
		availableAt := now.Add(outboxRetryDelay(claim.Attempt))
		if claim.Attempt >= claim.MaxAttempts {
			state = "failed"
			availableAt = now
		}
		tag, err := tx.Exec(ctx,
			`UPDATE deploy_intake_outbox
			    SET state = $1,
			        available_at = $2,
			        lease_owner = NULL,
			        lease_expires_at = NULL,
			        last_error = $3,
			        updated_at = $4
			  WHERE id = $5
			    AND state = 'delivering'
			    AND lease_owner = $6`,
			state, availableAt, message, now, claim.ID, claim.LeaseOwner,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeployOutboxLeaseLost
		}
		eventMessage := fmt.Sprintf("durable outbox delivery attempt %d/%d failed: %s", claim.Attempt, claim.MaxAttempts, message)
		eventState := orailtypes.OperationPending
		if state == "failed" {
			eventState = orailtypes.OperationFailed
			if _, err := tx.Exec(ctx,
				`UPDATE operations
				    SET state = 'FAILED', code = $1, reason = $2, finished_at = $3,
				        updated_at = $3, resource_version = resource_version + 1
				  WHERE id = $4
				    AND state NOT IN ('SUCCEEDED','FAILED','CANCELED')`,
				string(failurecodes.DeployJobEnqueueFailed), message, now, claim.OperationID,
			); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE deployments
				    SET state = 'failed', failure_code = $1, failure_message = $2,
				        completed_at = $3, updated_at = $3, resource_version = resource_version + 1
				  WHERE id = $4
				    AND state = 'queued'`,
				string(failurecodes.DeployJobEnqueueFailed), message, now, claim.DeploymentID,
			); err != nil {
				return err
			}
		}
		return appendOperationEventTx(ctx, tx, orailtypes.ID(claim.OperationID), eventState, eventMessage, now)
	})
}

func appendOperationEventTx(ctx context.Context, tx *pg.Tx, operationID orailtypes.ID, state orailtypes.OperationState, message string, now time.Time) error {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM operations WHERE id = $1 FOR UPDATE`, string(operationID)).Scan(&locked); err != nil {
		return fmt.Errorf("deploy outbox operation lock: %w", err)
	}
	var seq int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM operation_events WHERE operation_id = $1`, string(operationID)).Scan(&seq); err != nil {
		return fmt.Errorf("deploy outbox event sequence: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO operation_events (id, operation_id, seq, state, message, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		fmt.Sprintf("%s-%d", operationID, seq), string(operationID), seq, string(state), message, now,
	); err != nil {
		return fmt.Errorf("deploy outbox event insert: %w", err)
	}
	return nil
}

func outboxRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 8))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func boundedOutboxError(err error) string {
	if err == nil {
		return "unknown delivery error"
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func randomOutboxID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

// RunDeployOutboxDispatcher drains durable intents until ctx is canceled.
// Transient errors are reported and retried; they never terminate delivery.
func RunDeployOutboxDispatcher(ctx context.Context, outbox DurableDeployIntake, pollInterval time.Duration, report func(error)) {
	if outbox == nil {
		return
	}
	if pollInterval <= 0 {
		pollInterval = defaultDeployOutboxPoll
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		delivered, err := outbox.DispatchDeployIntake(ctx, 32)
		if err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil && delivered == 32 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
