// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"testing"
	"time"

	auth "github.com/midnight/midnight/internal/agent/auth"
)

func TestMemorySessionRevoker_IsRevokedFalseByDefault(t *testing.T) {
	t.Parallel()
	r := auth.NewMemorySessionRevoker()
	ok, err := r.IsRevoked(context.Background(), "conv_1")
	if err != nil || ok {
		t.Fatalf("IsRevoked = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestMemorySessionRevoker_RevokeFlipsState(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	r := auth.NewMemorySessionRevoker()
	r.SetClock(func() time.Time { return fixed })
	ctx := context.Background()
	if err := r.Revoke(ctx, "conv_1", "op_1", "abuse"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	ok, _ := r.IsRevoked(ctx, "conv_1")
	if !ok {
		t.Fatalf("IsRevoked after revoke must be true")
	}
	rec, _ := r.RevokedAt(ctx, "conv_1")
	if !rec.Revoked {
		t.Fatalf("RevokedAt must report Revoked=true")
	}
	if !rec.RevokedAt.Equal(fixed) {
		t.Fatalf("RevokedAt = %v, want %v", rec.RevokedAt, fixed)
	}
	if rec.ActorID != "op_1" {
		t.Fatalf("actor = %q, want op_1", rec.ActorID)
	}
	if rec.Reason != "abuse" {
		t.Fatalf("reason = %q, want abuse", rec.Reason)
	}
}

func TestMemorySessionRevoker_RevokeIsIdempotent(t *testing.T) {
	t.Parallel()
	first := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	second := time.Date(2026, 5, 17, 13, 0, 0, 0, time.UTC)
	current := first
	r := auth.NewMemorySessionRevoker()
	r.SetClock(func() time.Time { return current })
	ctx := context.Background()
	if err := r.Revoke(ctx, "conv_idem", "op_1", "abuse"); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	current = second
	if err := r.Revoke(ctx, "conv_idem", "op_2", "second"); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	rec, _ := r.RevokedAt(ctx, "conv_idem")
	if rec.ActorID != "op_1" {
		t.Fatalf("idempotent revoke: actor = %q, want op_1", rec.ActorID)
	}
	if rec.Reason != "abuse" {
		t.Fatalf("idempotent revoke: reason = %q, want abuse", rec.Reason)
	}
	if !rec.RevokedAt.Equal(first) {
		t.Fatalf("idempotent revoke: timestamp = %v, want %v", rec.RevokedAt, first)
	}
}

func TestMemorySessionRevoker_EmptySessionRejected(t *testing.T) {
	t.Parallel()
	r := auth.NewMemorySessionRevoker()
	if err := r.Revoke(context.Background(), "", "op_1", "x"); err == nil {
		t.Fatalf("empty session id must error")
	}
}

func TestCompositeRevoker_AnyRevokerReportsRevoked(t *testing.T) {
	t.Parallel()
	r1 := auth.NewMemorySessionRevoker()
	r2 := auth.NewMemorySessionRevoker()
	c := auth.CompositeRevoker{Revokers: []auth.SessionRevoker{r1, r2}}
	ctx := context.Background()
	if err := r2.Revoke(ctx, "conv_z", "op_1", "test"); err != nil {
		t.Fatalf("revoke r2: %v", err)
	}
	ok, _ := c.IsRevoked(ctx, "conv_z")
	if !ok {
		t.Fatalf("composite IsRevoked must be true when any inner is")
	}
	rec, _ := c.RevokedAt(ctx, "conv_z")
	if !rec.Revoked {
		t.Fatalf("composite RevokedAt must return revoked record")
	}
}

func TestCompositeRevoker_FanOutRevoke(t *testing.T) {
	t.Parallel()
	r1 := auth.NewMemorySessionRevoker()
	r2 := auth.NewMemorySessionRevoker()
	c := auth.CompositeRevoker{Revokers: []auth.SessionRevoker{r1, r2}}
	ctx := context.Background()
	if err := c.Revoke(ctx, "conv_w", "op_1", "fanout"); err != nil {
		t.Fatalf("composite revoke: %v", err)
	}
	if ok, _ := r1.IsRevoked(ctx, "conv_w"); !ok {
		t.Fatalf("r1 should see fanout revoke")
	}
	if ok, _ := r2.IsRevoked(ctx, "conv_w"); !ok {
		t.Fatalf("r2 should see fanout revoke")
	}
}

// TestKillSwitchLatency_BoundIsDocumented anchors the latency
// guarantee so a downstream refactor that changes the constant
// fails this test and forces a re-read of ADR-0027.
func TestKillSwitchLatency_BoundIsDocumented(t *testing.T) {
	t.Parallel()
	if auth.KillSwitchLatencyBound != 60*time.Second {
		t.Fatalf("kill-switch latency bound changed: %v — update ADR-0027", auth.KillSwitchLatencyBound)
	}
}
