// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresSessionRevoker is the Postgres-backed SessionRevoker
// powered by the agent_revoked_sessions table (migration 0013).
// The table is intentionally narrow — one row per revoked
// (conversation_id | mcp_session_id) — and INSERT-only so the
// audit trail is preserved.
//
// The schema is:
//
//	CREATE TABLE agent_revoked_sessions (
//	    session_id TEXT PRIMARY KEY,
//	    actor_id TEXT NOT NULL DEFAULT '',
//	    reason TEXT NOT NULL DEFAULT '',
//	    revoked_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
//	);
//
// Idempotency: Revoke uses INSERT ... ON CONFLICT DO NOTHING so a
// re-revoke succeeds without rewriting the original
// (actor, reason, revoked_at) triple.
type PostgresSessionRevoker struct {
	Pool *pg.Pool
	Now  func() time.Time
}

// NewPostgresSessionRevoker constructs a revoker against the
// supplied pool.
func NewPostgresSessionRevoker(pool *pg.Pool) *PostgresSessionRevoker {
	return &PostgresSessionRevoker{
		Pool: pool,
		Now:  func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the wall clock.
func (r *PostgresSessionRevoker) SetClock(fn func() time.Time) {
	if r == nil || fn == nil {
		return
	}
	r.Now = fn
}

// Revoke marks the supplied session id as revoked.
func (r *PostgresSessionRevoker) Revoke(ctx context.Context, sessionID, actorID, reason string) error {
	if r == nil || r.Pool == nil {
		return errors.New("postgres session revoker: pool not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("postgres session revoker: session id required")
	}
	_, err := r.Pool.Exec(ctx,
		`INSERT INTO agent_revoked_sessions (session_id, actor_id, reason, revoked_at)
         VALUES ($1, $2, $3, $4)
         ON CONFLICT (session_id) DO NOTHING`,
		sessionID, actorID, reason, r.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres session revoker: revoke: %w", err)
	}
	return nil
}

// IsRevoked reports the kill-switch state via an indexed lookup.
func (r *PostgresSessionRevoker) IsRevoked(ctx context.Context, sessionID string) (bool, error) {
	if r == nil || r.Pool == nil {
		return false, errors.New("postgres session revoker: pool not initialized")
	}
	var found bool
	err := r.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_revoked_sessions WHERE session_id = $1)`,
		strings.TrimSpace(sessionID),
	).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("postgres session revoker: lookup: %w", err)
	}
	return found, nil
}

// RevokedAt returns the revocation record.
func (r *PostgresSessionRevoker) RevokedAt(ctx context.Context, sessionID string) (RevokedRecord, error) {
	if r == nil || r.Pool == nil {
		return RevokedRecord{}, errors.New("postgres session revoker: pool not initialized")
	}
	rec := RevokedRecord{SessionID: strings.TrimSpace(sessionID)}
	err := r.Pool.QueryRow(ctx,
		`SELECT actor_id, reason, revoked_at FROM agent_revoked_sessions WHERE session_id = $1`,
		rec.SessionID,
	).Scan(&rec.ActorID, &rec.Reason, &rec.RevokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RevokedRecord{}, nil
		}
		return RevokedRecord{}, fmt.Errorf("postgres session revoker: scan: %w", err)
	}
	rec.Revoked = true
	rec.RevokedAt = rec.RevokedAt.UTC()
	return rec, nil
}
