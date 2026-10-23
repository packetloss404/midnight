// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
)

// auditEmitter is the small wrapper Lane 5 threads through every
// mutating handler so a single Append call lands on Lane 4's panel-
// locked audit hash chain after a successful operation.
//
// The emit path is intentionally fail-soft for Phase 1: a non-strict
// chain (the default in-memory MemoryChain) cannot fail; a strict
// Postgres-backed chain (commit E) will surface persistence failures
// upstream via slog so operators see the chain break without the API
// surface losing its 2xx semantics. The Phase 1 prompt explicitly
// permits "log + warn is acceptable on a per-call basis if the audit
// chain is configured non-strict" — we adopt that posture here. Once
// the strict-mode flag is wired the call sites will return 5xx
// instead.
type auditEmitter struct {
	chain audit.AuditChain
	now   func() time.Time
}

// newAuditEmitter constructs an auditEmitter writing into chain. When
// clock is nil, the system UTC clock is used.
func newAuditEmitter(chain audit.AuditChain, clock func() time.Time) *auditEmitter {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &auditEmitter{chain: chain, now: clock}
}

// emit appends row to the chain, decorating timestamp / actor_type /
// request_id with sensible defaults when the caller has not set them.
// Append failures are logged at WARN level rather than surfaced —
// Phase 1's per-call fail-soft posture per ADR-011 §"Operational
// Posture" item 3.
func (e *auditEmitter) emit(ctx context.Context, row audit.Row) {
	if e == nil || e.chain == nil {
		return
	}
	now := e.now()
	if row.RecordedAt.IsZero() {
		row.RecordedAt = now
	}
	if row.OccurredAt.IsZero() {
		row.OccurredAt = row.RecordedAt
	}
	if row.ActorType == "" {
		row.ActorType = "system"
	}
	if row.RequestID == "" {
		if reqID := middleware.FromContext(ctx); reqID != "" {
			row.RequestID = reqID
		} else {
			row.RequestID = "system"
		}
	}
	if _, err := e.chain.Append(ctx, row); err != nil {
		slog.WarnContext(ctx, "audit chain append failed",
			slog.String("resource_kind", row.ResourceKind),
			slog.String("resource_id", row.ResourceID),
			slog.String("action", row.Action),
			slog.String("error", err.Error()),
		)
	}
}
