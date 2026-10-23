// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package queue

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LeaseLifecycleState enumerates the lifecycle states of a worker lease.
// They mirror the panel-locked state diagram in
// agile/engineering/schemas/SCHEMA_QUEUE_CONTRACT.md section 2.
type LeaseLifecycleState string

const (
	// LeaseStateAcquired indicates the worker holds an active lease.
	LeaseStateAcquired LeaseLifecycleState = "ACQUIRED"
	// LeaseStateHeartbeatDue indicates the next heartbeat must run before
	// the lease expiry is reached.
	LeaseStateHeartbeatDue LeaseLifecycleState = "HEARTBEAT_DUE"
	// LeaseStateAtRisk indicates the lease will expire within the
	// HeartbeatInterval if the worker does not heartbeat now.
	LeaseStateAtRisk LeaseLifecycleState = "AT_RISK"
	// LeaseStateExpired indicates the reaper will reclaim the lease.
	LeaseStateExpired LeaseLifecycleState = "EXPIRED"
)

// Lease is the in-memory mirror of the row-level lease metadata persisted
// in the `jobs` table. It is owned by the worker library; the API layer
// never mutates it directly.
type Lease struct {
	JobID            string
	LeaseOwner       string
	Queue            Name
	LeaseTTL         time.Duration
	AcquiredAt       time.Time
	HeartbeatAt      time.Time
	LeaseExpiresAt   time.Time
	Attempt          int
	HeartbeatBackoff time.Duration
}

// NewLeaseID returns a mutation token for one lease attempt. It preserves the
// worker id for observability but adds entropy so stale Complete/Fail calls
// from a prior lease attempt cannot match a later lease held by the same
// worker process.
func NewLeaseID(workerID string, now time.Time) string {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		workerID = "worker"
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return workerID + "/" + hex.EncodeToString(b[:])
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("%s/%d", workerID, now.UTC().UnixNano())
}

// NewLease constructs a Lease using `now` as the acquisition time. The
// LeaseTTL must be positive; HeartbeatBackoff defaults to LeaseTTL/3.
func NewLease(jobID, owner string, queue Name, leaseTTL time.Duration, now time.Time) (Lease, error) {
	if jobID == "" || owner == "" || leaseTTL <= 0 || now.IsZero() {
		return Lease{}, fmt.Errorf("%w: invalid lease parameters", ErrInvalidLeaseHeartbeat)
	}
	interval, err := HeartbeatInterval(leaseTTL)
	if err != nil {
		return Lease{}, err
	}
	return Lease{
		JobID:            jobID,
		LeaseOwner:       owner,
		Queue:            queue,
		LeaseTTL:         leaseTTL,
		AcquiredAt:       now.UTC(),
		HeartbeatAt:      now.UTC(),
		LeaseExpiresAt:   now.UTC().Add(leaseTTL),
		HeartbeatBackoff: interval,
	}, nil
}

// Renew advances the lease by one heartbeat. The renewal is rejected when
// `now` is past the *original* lease window (`AcquiredAt + LeaseTTL`); this
// gives every lease a hard upper bound on its lifetime so a misbehaving
// worker cannot keep extending its lease forever.
func (l *Lease) Renew(now time.Time) error {
	if l == nil || now.IsZero() {
		return ErrInvalidLeaseHeartbeat
	}
	if !now.Before(l.LeaseExpiresAt) {
		return fmt.Errorf("%w: lease %s already expired at %s", ErrInvalidLeaseHeartbeat, l.JobID, l.LeaseExpiresAt)
	}
	if !l.AcquiredAt.IsZero() && !now.Before(l.AcquiredAt.Add(l.LeaseTTL)) {
		return fmt.Errorf("%w: lease %s past original window (acquired %s, ttl %s)", ErrInvalidLeaseHeartbeat, l.JobID, l.AcquiredAt, l.LeaseTTL)
	}
	l.HeartbeatAt = now.UTC()
	l.LeaseExpiresAt = now.UTC().Add(l.LeaseTTL)
	return nil
}

// Classify reports the current lifecycle state of the lease at the
// supplied timestamp. The classifier is the basis for the reaper /
// reclaim policy.
func (l Lease) Classify(now time.Time) LeaseLifecycleState {
	if !now.Before(l.LeaseExpiresAt) {
		return LeaseStateExpired
	}
	remaining := l.LeaseExpiresAt.Sub(now)
	if remaining <= l.HeartbeatBackoff {
		return LeaseStateAtRisk
	}
	if l.HeartbeatAt.IsZero() {
		return LeaseStateHeartbeatDue
	}
	if now.Sub(l.HeartbeatAt) >= l.HeartbeatBackoff {
		return LeaseStateHeartbeatDue
	}
	return LeaseStateAcquired
}

// Reclaim returns true when the reaper should reclaim the lease and false
// otherwise. The decision is identical to Classify == LeaseStateExpired
// but exposed separately so callers can document intent.
func (l Lease) Reclaim(now time.Time) bool {
	return l.Classify(now) == LeaseStateExpired
}

// ReclaimDecision describes the next reaper transition for an expired
// lease per the panel reclaim policy.
type ReclaimDecision struct {
	NextState   JobState
	NextAttempt int
	BackoffFor  time.Duration
	ReasonCode  string
}

// PlanReclaim computes the post-reaper state for an expired lease.
//
//   - When `attempt < maxAttempts`, the reaper requeues the job with an
//     exponential backoff (`30s * 2^(attempt-1)`, clamped to one hour) per
//     section 3 of the contract. The attempt value is the leased row's
//     persisted attempt count after Lease increments it.
//   - When `attempt >= maxAttempts`, the reaper transitions the job to
//     `dead_letter` with reason `LEASE_EXPIRED_MAX_ATTEMPTS`.
func PlanReclaim(attempt, maxAttempts int) ReclaimDecision {
	if attempt >= maxAttempts {
		return ReclaimDecision{
			NextState:  JobFailed, // dead-letter is modeled as a terminal failure for Phase 0
			ReasonCode: ReasonLeaseExpiredMaxAttempts,
		}
	}
	return ReclaimDecision{
		NextState:   JobQueued,
		NextAttempt: attempt + 1,
		BackoffFor:  ReclaimBackoff(attempt),
		ReasonCode:  ReasonLeaseExpired,
	}
}

// ReclaimBackoff returns the retry delay for an expired leased attempt.
// Attempt values are one-based in storage because Lease increments the row
// before workers execute. Zero is accepted for helper-level callers that plan
// before a lease row exists.
func ReclaimBackoff(attempt int) time.Duration {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	backoff := time.Duration(30) * time.Second
	for i := 0; i < exponent; i++ {
		backoff *= 2
		if backoff >= time.Hour {
			return time.Hour
		}
	}
	return backoff
}

// PoolBudget returns the canonical concurrency pool sizes per panel
// section 4. Unknown queue names return ErrInvalidLeaseHeartbeat.
func PoolBudget(queue Name) (poolSize int, leaseTTL time.Duration, err error) {
	switch queue {
	case Build:
		return 4, 5 * time.Minute, nil
	case Deploy:
		return 4, 60 * time.Second, nil
	case Reconcile:
		return 2, 30 * time.Second, nil
	case Webhook:
		return 8, 30 * time.Second, nil
	case Cleanup:
		return 2, 2 * time.Minute, nil
	default:
		return 0, 0, errors.New("unknown queue name")
	}
}
