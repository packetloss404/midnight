// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// RevokedReason is the canonical stream-close reason the chat-
// backend emits when a revoked session is detected mid-stream.
const RevokedReason = "revoked"

// SessionRevoker is the kill switch. The chat-backend's revoke
// endpoint flips a session to revoked; the stream loop calls
// IsRevoked between agent turns and closes the stream with
// RevokedReason when the flag is set.
//
// The state lives in this package (not in the chat-backend
// in-memory store) because:
//   - the same kill switch must reach MCP sessions which the chat
//     backend doesn't own;
//   - the state survives a process restart so an operator who
//     revoked at T can be confident the agent does not resume at
//     T+1 after a crash;
//   - it composes with the audit sink so every revoke is logged.
type SessionRevoker interface {
	// Revoke marks the supplied session id as revoked. Idempotent
	// — a re-revoke succeeds without error. ActorID identifies
	// the operator that issued the revoke (for audit).
	Revoke(ctx context.Context, sessionID, actorID, reason string) error
	// IsRevoked reports whether the session has been revoked.
	// Cheap (no I/O for in-memory; single indexed lookup for
	// Postgres). The chat-backend stream loop calls it between
	// turns so the operator's revoke takes effect within
	// `KillSwitchLatencyBound` of the next turn boundary.
	IsRevoked(ctx context.Context, sessionID string) (bool, error)
	// RevokedAt returns the revocation timestamp + reason for a
	// session. Used by the chat-backend to surface "this
	// conversation was revoked by <operator> at <t>" to the user.
	// (Revoked, time.Time{}, "", nil) when the session is not
	// revoked.
	RevokedAt(ctx context.Context, sessionID string) (RevokedRecord, error)
}

// KillSwitchLatencyBound documents the upper bound on the time
// between a successful Revoke call and the next IsRevoked check
// in the chat-backend's stream loop. The chat-backend track owns
// the actual polling cadence; the bound here is contractual.
//
// The chat-backend polls IsRevoked between agent turns; a "turn"
// is bounded by the upstream model's stream-stop event. Typical
// turn duration is 1-30s for short tool calls, up to 60s for
// long completions. The kill switch therefore takes effect
// within ~30s of revoke under typical load and within ~60s in
// the worst case. Operators that need synchronous revoke (e.g.
// emergency stop) call the chat-backend's `disconnect` endpoint
// in parallel.
const KillSwitchLatencyBound = 60 * time.Second

// RevokedRecord describes a single revocation event.
type RevokedRecord struct {
	Revoked   bool
	SessionID string
	ActorID   string
	Reason    string
	RevokedAt time.Time
}

// MemorySessionRevoker is the in-memory SessionRevoker used by
// tests and stacks without a Postgres pool. Concurrent-safe.
type MemorySessionRevoker struct {
	mu    sync.RWMutex
	rows  map[string]RevokedRecord
	nowFn func() time.Time
}

// NewMemorySessionRevoker constructs an empty revoker.
func NewMemorySessionRevoker() *MemorySessionRevoker {
	return &MemorySessionRevoker{
		rows:  map[string]RevokedRecord{},
		nowFn: func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the wall clock used to stamp revocations.
func (r *MemorySessionRevoker) SetClock(fn func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fn != nil {
		r.nowFn = fn
	}
}

// Revoke marks the session id as revoked.
func (r *MemorySessionRevoker) Revoke(ctx context.Context, sessionID, actorID, reason string) error {
	if r == nil {
		return errors.New("memory session revoker: not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("memory session revoker: session id required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rows[sessionID]; exists {
		// idempotent: the first revoke wins so the original
		// actor / reason are preserved.
		return nil
	}
	r.rows[sessionID] = RevokedRecord{
		Revoked:   true,
		SessionID: sessionID,
		ActorID:   actorID,
		Reason:    reason,
		RevokedAt: r.nowFn(),
	}
	return nil
}

// IsRevoked reports the kill-switch state.
func (r *MemorySessionRevoker) IsRevoked(ctx context.Context, sessionID string) (bool, error) {
	if r == nil {
		return false, errors.New("memory session revoker: not initialized")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.rows[strings.TrimSpace(sessionID)]
	return ok, nil
}

// RevokedAt returns the revocation record (or a zero
// RevokedRecord with Revoked=false when the session is not
// revoked).
func (r *MemorySessionRevoker) RevokedAt(ctx context.Context, sessionID string) (RevokedRecord, error) {
	if r == nil {
		return RevokedRecord{}, errors.New("memory session revoker: not initialized")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.rows[strings.TrimSpace(sessionID)]
	if !ok {
		return RevokedRecord{}, nil
	}
	return rec, nil
}

// CompositeRevoker fans out Revoke calls to multiple inner
// revokers (useful when an MCP-side revoker and a chat-side
// revoker need to stay in sync during a migration). IsRevoked
// returns true when ANY inner reports the session revoked.
type CompositeRevoker struct {
	Revokers []SessionRevoker
}

// Revoke fan-outs the revoke to every inner revoker. The first
// error wins; remaining revokers still receive the call.
func (c CompositeRevoker) Revoke(ctx context.Context, sessionID, actorID, reason string) error {
	var firstErr error
	for _, r := range c.Revokers {
		if r == nil {
			continue
		}
		if err := r.Revoke(ctx, sessionID, actorID, reason); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// IsRevoked returns true when any inner revoker reports the
// session revoked.
func (c CompositeRevoker) IsRevoked(ctx context.Context, sessionID string) (bool, error) {
	for _, r := range c.Revokers {
		if r == nil {
			continue
		}
		ok, err := r.IsRevoked(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// RevokedAt returns the first non-empty record from any inner
// revoker.
func (c CompositeRevoker) RevokedAt(ctx context.Context, sessionID string) (RevokedRecord, error) {
	for _, r := range c.Revokers {
		if r == nil {
			continue
		}
		rec, err := r.RevokedAt(ctx, sessionID)
		if err != nil {
			return RevokedRecord{}, err
		}
		if rec.Revoked {
			return rec, nil
		}
	}
	return RevokedRecord{}, nil
}
