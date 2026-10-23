// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// ErrJobNotFound is returned when a queue mutation cannot find the
// referenced row (or a heartbeat/complete races with a lease loss).
var ErrJobNotFound = errors.New("job not found")

// ErrLeaseLost is returned when a worker's mutation predicate matches
// zero rows because the lease was reclaimed by the reaper or replaced
// by another worker.
var ErrLeaseLost = errors.New("queue lease lost")

// ErrNoWork is returned by Lease when no row matches the dequeue
// predicate. Workers translate this to a jittered backoff (panel
// section 1).
var ErrNoWork = errors.New("queue: no work available")

// PostgresStore implements queue.Store backed by the `jobs` table. The
// dequeue SQL follows SCHEMA_QUEUE_CONTRACT.md section 1 verbatim
// (modulo column name differences between Lane 1's bootstrap schema
// and the panel-locked partitioned schema).
type PostgresStore struct {
	Pool *pg.Pool
}

// NewPostgresStore wires a PostgresStore.
func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool}
}

// stateToDB / stateFromDB translate the existing uppercase Job state
// constants to the lowercase strings the Postgres index/dequeue SQL
// expects. Keeping the public Go API uppercase preserves backward
// compatibility with Lane 2's worker code.
func stateToDB(state JobState) string {
	switch state {
	case JobQueued:
		return "pending"
	case JobLeased:
		return "leased"
	case JobSucceeded:
		return "succeeded"
	case JobFailed:
		return "failed"
	case JobCanceled:
		return "canceled"
	default:
		return ""
	}
}

func stateFromDB(s string) JobState {
	switch s {
	case "pending":
		return JobQueued
	case "leased":
		return JobLeased
	case "succeeded":
		return JobSucceeded
	case "failed":
		return JobFailed
	case "canceled":
		return JobCanceled
	default:
		return JobState(s)
	}
}

// Enqueue inserts a new pending job.
func (s *PostgresStore) Enqueue(ctx context.Context, request EnqueueRequest) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if request.Queue == "" {
		return Job{}, errors.New("queue.Enqueue: queue is required")
	}
	id := generateJobID()
	availableAt := request.AvailableAt
	if availableAt.IsZero() {
		availableAt = time.Now().UTC()
	}
	parentOperationID := parentOperationIDFromRequest(request)
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO jobs (id, queue, state, partition_key, parent_operation_id, priority, available_at, payload, created_at, updated_at)
		 VALUES ($1, $2, 'pending', $3, $4, $5, $6, $7, $8, $8)`,
		id, string(request.Queue), request.PartitionKey, nullable(parentOperationID), request.Priority, availableAt, request.Payload, time.Now().UTC(),
	)
	if err != nil {
		return Job{}, fmt.Errorf("queue enqueue: %w", err)
	}
	return Job{
		ID:                id,
		Queue:             request.Queue,
		State:             JobQueued,
		Priority:          request.Priority,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: parentOperationID,
		AvailableAt:       availableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}, nil
}

// Lease implements the panel-locked dequeue contract: a CTE that
// SELECTs FOR UPDATE SKIP LOCKED a single eligible row, then UPDATEs
// it to 'leased' atomically. The runtime predicate
// (lease_expires_at IS NULL OR lease_expires_at < now()) is applied
// inside the SELECT so an expired-leased row that the reaper has not
// yet reclaimed remains invisible to fresh dequeues.
func (s *PostgresStore) Lease(ctx context.Context, request LeaseRequest) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if request.Queue == "" || request.WorkerID == "" {
		return Job{}, errors.New("queue.Lease: queue and worker_id are required")
	}
	if request.LeaseTime <= 0 {
		return Job{}, ErrInvalidLeaseHeartbeat
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	leaseExpires := now.Add(request.LeaseTime)
	leaseID := NewLeaseID(request.WorkerID, now)
	row := s.Pool.QueryRow(ctx,
		`WITH cte AS (
		    SELECT id
		    FROM   jobs
		    WHERE  state = 'pending'
		      AND  queue = $1
		      AND  available_at <= $2
		      AND  (lease_expires_at IS NULL OR lease_expires_at < $2)
		    ORDER  BY priority DESC, available_at ASC
		    FOR    UPDATE SKIP LOCKED
		    LIMIT  1
		 )
		 UPDATE jobs j
		 SET    state            = 'leased',
		        lease_owner      = $3,
		        lease_expires_at = $4,
		        heartbeat_at     = $2,
		        attempt          = j.attempt + 1,
		        updated_at       = $2
		 FROM   cte
		 WHERE  j.id = cte.id
		 RETURNING j.id, j.queue, j.state, j.priority, j.attempt, j.partition_key, COALESCE(j.parent_operation_id, ''), j.available_at, j.lease_owner, j.lease_expires_at, j.payload`,
		string(request.Queue), now, leaseID, leaseExpires,
	)
	var (
		jobID, queueName, stateRaw, partition, parentOperationID, leaseOwner string
		priority, attempt                                                    int
		availableAt, leaseExpiresAt                                          time.Time
		payload                                                              []byte
	)
	err := row.Scan(&jobID, &queueName, &stateRaw, &priority, &attempt, &partition, &parentOperationID, &availableAt, &leaseOwner, &leaseExpiresAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoWork
	}
	if err != nil {
		return Job{}, fmt.Errorf("queue lease: %w", err)
	}
	return Job{
		ID:                jobID,
		Queue:             Name(queueName),
		State:             stateFromDB(stateRaw),
		Priority:          priority,
		Attempt:           attempt,
		PartitionKey:      partition,
		ParentOperationID: parentOperationID,
		AvailableAt:       availableAt.UTC(),
		LeaseID:           leaseOwner,
		LeasedUntil:       leaseExpiresAt.UTC(),
		Payload:           append([]byte(nil), payload...),
	}, nil
}

// Heartbeat extends the lease so long as the worker still owns it.
func (s *PostgresStore) Heartbeat(ctx context.Context, jobID string, leaseID string, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if jobID == "" || leaseID == "" {
		return ErrInvalidLeaseHeartbeat
	}
	now := time.Now().UTC()
	tag, err := s.Pool.Exec(ctx,
		`UPDATE jobs
		 SET    lease_expires_at = $1,
		        heartbeat_at     = $2,
		        updated_at       = $2
		 WHERE  id = $3
		   AND  lease_owner = $4
		   AND  state = 'leased'
		   AND  lease_expires_at > $2`,
		until.UTC(), now, jobID, leaseID,
	)
	if err != nil {
		return fmt.Errorf("queue heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Complete marks the job succeeded.
func (s *PostgresStore) Complete(ctx context.Context, jobID string, leaseID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if jobID == "" || leaseID == "" {
		return ErrInvalidLeaseHeartbeat
	}
	now := time.Now().UTC()
	tag, err := s.Pool.Exec(ctx,
		`UPDATE jobs
		 SET    state            = 'succeeded',
		        lease_owner      = NULL,
		        lease_expires_at = NULL,
		        heartbeat_at     = NULL,
		        finished_at      = $1,
		        updated_at       = $1
		 WHERE  id = $2
		   AND  lease_owner = $3
		   AND  state = 'leased'
		   AND  lease_expires_at > $1`,
		now, jobID, leaseID,
	)
	if err != nil {
		return fmt.Errorf("queue complete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Fail marks the job failed with a reason code.
func (s *PostgresStore) Fail(ctx context.Context, jobID string, leaseID string, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if jobID == "" || leaseID == "" {
		return ErrInvalidLeaseHeartbeat
	}
	now := time.Now().UTC()
	tag, err := s.Pool.Exec(ctx,
		`UPDATE jobs
		 SET    state            = 'failed',
		        reason_code      = $1,
		        lease_owner      = NULL,
		        lease_expires_at = NULL,
		        heartbeat_at     = NULL,
		        finished_at      = $2,
		        updated_at       = $2
		 WHERE  id = $3
		   AND  lease_owner = $4
		   AND  state = 'leased'
		   AND  lease_expires_at > $2`,
		nullable(reason), now, jobID, leaseID,
	)
	if err != nil {
		return fmt.Errorf("queue fail: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// CancelQueued cancels deploy work that has not yet been leased. If the job is
// already leased, callers get an AlreadyLeased result and must not mark the
// parent operation canceled until the worker reaches a safe point.
func (s *PostgresStore) CancelQueued(ctx context.Context, request CancelQueuedRequest) (CancelQueuedResult, error) {
	if err := ctx.Err(); err != nil {
		return CancelQueuedResult{}, err
	}
	if request.Queue == "" || strings.TrimSpace(request.ParentOperationID) == "" {
		return CancelQueuedResult{}, errors.New("queue.CancelQueued: queue and parent_operation_id are required")
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		reason = ReasonDeployCanceledBeforeLease
	}
	row := s.Pool.QueryRow(ctx,
		`UPDATE jobs
		 SET    state            = 'canceled',
		        reason_code      = $1,
		        lease_owner      = NULL,
		        lease_expires_at = NULL,
		        heartbeat_at     = NULL,
		        finished_at      = $2,
		        updated_at       = $2
		 WHERE  queue = $3
		   AND  state = 'pending'
		   AND  (parent_operation_id = $4 OR operation_id = $4)
		 RETURNING id, queue, state, priority, attempt, partition_key, COALESCE(parent_operation_id, ''), available_at, COALESCE(lease_owner, ''), COALESCE(lease_expires_at, '0001-01-01'::timestamptz), payload`,
		reason, now.UTC(), string(request.Queue), strings.TrimSpace(request.ParentOperationID),
	)
	job, err := scanQueueJobRow(row)
	if err == nil {
		return CancelQueuedResult{Job: job, Canceled: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return CancelQueuedResult{}, fmt.Errorf("queue cancel queued: %w", err)
	}

	job, err = s.getJobByParentOperation(ctx, request.Queue, strings.TrimSpace(request.ParentOperationID))
	if errors.Is(err, ErrJobNotFound) {
		return CancelQueuedResult{}, ErrJobNotFound
	}
	if err != nil {
		return CancelQueuedResult{}, err
	}
	result := CancelQueuedResult{Job: job}
	switch job.State {
	case JobLeased:
		result.AlreadyLeased = true
	case JobSucceeded, JobFailed, JobCanceled:
		result.AlreadyTerminal = true
	}
	return result, nil
}

// Reclaim sweeps expired-leased rows back to 'pending' (or to a
// failed/dead-letter equivalent when attempt >= max_attempts). Returns
// the number of rows reclaimed; called by a reaper goroutine.
func (s *PostgresStore) Reclaim(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tag, err := s.Pool.Exec(ctx,
		`UPDATE jobs
		 SET    state =
		           CASE WHEN attempt >= max_attempts THEN 'failed' ELSE 'pending' END,
		        lease_owner      = NULL,
		        lease_expires_at = NULL,
		        heartbeat_at     = NULL,
		        reason_code      = CASE WHEN attempt >= max_attempts THEN 'LEASE_EXPIRED_MAX_ATTEMPTS' ELSE 'LEASE_EXPIRED' END,
		        dead_letter_reason = CASE WHEN attempt >= max_attempts THEN 'LEASE_EXPIRED_MAX_ATTEMPTS' ELSE NULL END,
		        available_at = CASE
		          WHEN attempt >= max_attempts THEN available_at
		          ELSE $1 + LEAST((interval '30 seconds' * POWER(2, GREATEST(attempt - 1, 0))), interval '1 hour')
		        END,
		        updated_at       = $1
		 WHERE  state = 'leased'
		   AND  lease_expires_at < $1`,
		now,
	)
	if err != nil {
		return 0, fmt.Errorf("queue reclaim: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Get returns a job row (admin/inspect path).
func (s *PostgresStore) Get(ctx context.Context, jobID string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	var (
		queueName, stateRaw, partition, parentOperationID, leaseOwner string
		priority, attempt                                             int
		availableAt, leaseExpiresAt                                   time.Time
		payload                                                       []byte
	)
	err := s.Pool.QueryRow(ctx,
		`SELECT queue, state, priority, attempt, partition_key, COALESCE(parent_operation_id, ''), available_at, COALESCE(lease_owner, ''), COALESCE(lease_expires_at, '0001-01-01'::timestamptz), payload
		 FROM jobs WHERE id = $1`,
		jobID,
	).Scan(&queueName, &stateRaw, &priority, &attempt, &partition, &parentOperationID, &availableAt, &leaseOwner, &leaseExpiresAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("queue get: %w", err)
	}
	return Job{
		ID:                jobID,
		Queue:             Name(queueName),
		State:             stateFromDB(stateRaw),
		Priority:          priority,
		Attempt:           attempt,
		PartitionKey:      partition,
		ParentOperationID: parentOperationID,
		AvailableAt:       availableAt.UTC(),
		LeaseID:           leaseOwner,
		LeasedUntil:       leaseExpiresAt.UTC(),
		Payload:           append([]byte(nil), payload...),
	}, nil
}

func (s *PostgresStore) getJobByParentOperation(ctx context.Context, queueName Name, parentOperationID string) (Job, error) {
	row := s.Pool.QueryRow(ctx,
		`SELECT id, queue, state, priority, attempt, partition_key, COALESCE(parent_operation_id, ''), available_at, COALESCE(lease_owner, ''), COALESCE(lease_expires_at, '0001-01-01'::timestamptz), payload
		 FROM jobs
		 WHERE queue = $1
		   AND (parent_operation_id = $2 OR operation_id = $2)
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		string(queueName), parentOperationID,
	)
	job, err := scanQueueJobRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrJobNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("queue get by parent operation: %w", err)
	}
	return job, nil
}

type queueJobScanner interface {
	Scan(dest ...any) error
}

func scanQueueJobRow(row queueJobScanner) (Job, error) {
	var (
		jobID, queueName, stateRaw, partition, parentOperationID, leaseOwner string
		priority, attempt                                                    int
		availableAt, leaseExpiresAt                                          time.Time
		payload                                                              []byte
	)
	if err := row.Scan(&jobID, &queueName, &stateRaw, &priority, &attempt, &partition, &parentOperationID, &availableAt, &leaseOwner, &leaseExpiresAt, &payload); err != nil {
		return Job{}, err
	}
	return Job{
		ID:                jobID,
		Queue:             Name(queueName),
		State:             stateFromDB(stateRaw),
		Priority:          priority,
		Attempt:           attempt,
		PartitionKey:      partition,
		ParentOperationID: parentOperationID,
		AvailableAt:       availableAt.UTC(),
		LeaseID:           leaseOwner,
		LeasedUntil:       leaseExpiresAt.UTC(),
		Payload:           append([]byte(nil), payload...),
	}, nil
}

func parentOperationIDFromRequest(request EnqueueRequest) string {
	if request.ParentOperationID != "" {
		return request.ParentOperationID
	}
	if len(request.Payload) == 0 {
		return ""
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		return ""
	}
	return payload.OperationID
}

func generateJobID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("job_%d", time.Now().UnixNano())
	}
	return "job_" + hex.EncodeToString(b[:])
}

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
