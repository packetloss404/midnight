// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/webhook"
)

// auditChainSink is the panel-locked bridge from
// `webhook.AuditSink.EmitDelivery` to Lane 4's `audit.AuditChain.Append`
// seam. Every inbound webhook delivery — accepted, replayed, or
// rejected — produces one canonical audit row so verifiers see a
// continuous chain.
//
// This adapter intentionally lives on the API side rather than in the
// `internal/webhook` package so Lane 4's webhook receiver stays
// audit-agnostic and the API package owns the mapping from
// receiver-side outcome strings to the audit Row schema.
type auditChainSink struct {
	chain audit.AuditChain
	now   func() time.Time
}

// newAuditChainSink constructs an auditChainSink writing into chain.
// When clock is nil, the system UTC clock is used.
func newAuditChainSink(chain audit.AuditChain, clock func() time.Time) *auditChainSink {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &auditChainSink{chain: chain, now: clock}
}

// EmitDelivery implements webhook.AuditSink. The mapping intentionally
// drops plaintext body fields and surfaces only the redaction-safe
// shape DeliveryAudit already provides (provider / delivery id /
// event hash / outcome / reason code).
func (s *auditChainSink) EmitDelivery(ctx context.Context, ev webhook.DeliveryAudit) {
	if s == nil || s.chain == nil {
		return
	}
	now := ev.ReceivedAt
	if now.IsZero() {
		now = s.now()
	}
	resourceID := ev.EndpointID
	row := audit.Row{
		RequestID:    deriveRequestID(ev),
		OccurredAt:   now.UTC(),
		RecordedAt:   s.now().UTC(),
		ActorType:    "system",
		ResourceKind: "webhook_delivery",
		ResourceID:   resourceID,
		Action:       "webhook." + string(ev.Provider) + "." + ev.Outcome,
		ReasonCode:   ev.ReasonCode,
		Attrs: map[string]any{
			"provider":    string(ev.Provider),
			"endpoint_id": ev.EndpointID,
			"delivery_id": ev.DeliveryID,
			"event_hash":  ev.EventHash,
			"event_type":  ev.EventType,
			"event_id":    ev.EventID,
			"kid":         ev.KID,
			"outcome":     ev.Outcome,
		},
	}
	// Append failure here is recorded as a no-op for Phase 1 — the
	// receiver has already committed the dedupe row, and audit
	// strictness toggles at the chain level (commit E adds the
	// Postgres-backed chain that surfaces persistence failures
	// upstream). For the in-memory chain Append never errors.
	_, _ = s.chain.Append(ctx, row)
}

// deriveRequestID extracts a stable request id for the audit row from
// a webhook DeliveryAudit. The chain schema requires a non-empty
// request_id; we prefer the panel-locked Midnight-Delivery-Id when
// present and fall back to event_hash, then a synthetic sentinel so
// the column is never empty.
func deriveRequestID(ev webhook.DeliveryAudit) string {
	if ev.DeliveryID != "" {
		return "whd_" + ev.DeliveryID
	}
	if ev.EventHash != "" {
		return "wh_" + ev.EventHash
	}
	return "wh_unknown"
}
