// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"testing"
	"time"

	auth "github.com/midnight/midnight/internal/agent/auth"
)

func TestMemoryBudget_Defaults(t *testing.T) {
	t.Parallel()
	b := auth.NewMemoryBudget(auth.BudgetConfig{})
	limits := b.Limits(context.Background())
	if limits.ConversationTokens != auth.DefaultConversationTokenCap {
		t.Fatalf("default conv tokens = %d, want %d", limits.ConversationTokens, auth.DefaultConversationTokenCap)
	}
	if limits.ConversationCostCents != auth.DefaultConversationCostCapCents {
		t.Fatalf("default conv cost = %d, want %d", limits.ConversationCostCents, auth.DefaultConversationCostCapCents)
	}
	if limits.HostDailyCostCents != auth.DefaultHostDailyCostCapCents {
		t.Fatalf("default host daily = %d, want %d", limits.HostDailyCostCents, auth.DefaultHostDailyCostCapCents)
	}
}

func TestMemoryBudget_AllowsUntilCap(t *testing.T) {
	t.Parallel()
	cfg := auth.BudgetConfig{
		ConversationTokens:    1000,
		ConversationCostCents: 200,
		HostDailyCostCents:    500,
	}
	b := auth.NewMemoryBudget(cfg)
	ctx := context.Background()
	session := auth.SessionContext{ConversationID: "conv_1", HostID: "host_a"}

	dec, err := b.Allow(ctx, session)
	if err != nil || !dec.Allow {
		t.Fatalf("initial allow = (%+v, %v)", dec, err)
	}

	// Consume tokens but stay under cap.
	if err := b.Record(ctx, session, auth.Usage{InputTokens: 500}); err != nil {
		t.Fatalf("record: %v", err)
	}
	dec, _ = b.Allow(ctx, session)
	if !dec.Allow {
		t.Fatalf("after 500 tokens (cap=1000) should still allow, got %+v", dec)
	}
}

func TestMemoryBudget_TokenCapExhaustion(t *testing.T) {
	t.Parallel()
	b := auth.NewMemoryBudget(auth.BudgetConfig{
		ConversationTokens:    1000,
		ConversationCostCents: 999_999,
		HostDailyCostCents:    999_999,
	})
	session := auth.SessionContext{ConversationID: "conv_x"}
	ctx := context.Background()
	if err := b.Record(ctx, session, auth.Usage{InputTokens: 600, OutputTokens: 500}); err != nil {
		t.Fatalf("record: %v", err)
	}
	dec, err := b.Allow(ctx, session)
	if err != nil {
		t.Fatalf("allow: %v", err)
	}
	if dec.Allow {
		t.Fatalf("token cap exhaustion should deny, got %+v", dec)
	}
	if dec.Reason != auth.BudgetReasonExhausted {
		t.Fatalf("reason = %q, want %q", dec.Reason, auth.BudgetReasonExhausted)
	}
	if dec.Used.ConversationTokens != 1100 {
		t.Fatalf("used tokens = %d, want 1100", dec.Used.ConversationTokens)
	}
}

func TestMemoryBudget_ConversationCostCap(t *testing.T) {
	t.Parallel()
	b := auth.NewMemoryBudget(auth.BudgetConfig{
		ConversationTokens:    9_000_000,
		ConversationCostCents: 100, // $1
		HostDailyCostCents:    9_999_999,
	})
	session := auth.SessionContext{ConversationID: "conv_cost"}
	ctx := context.Background()
	if err := b.Record(ctx, session, auth.Usage{CostCents: 100}); err != nil {
		t.Fatalf("record: %v", err)
	}
	dec, _ := b.Allow(ctx, session)
	if dec.Allow {
		t.Fatalf("$1 spent on $1 cap must deny, got %+v", dec)
	}
}

func TestMemoryBudget_HostDailyCapRetryAt(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 5, 17, 15, 0, 0, 0, time.UTC)
	b := auth.NewMemoryBudget(auth.BudgetConfig{
		ConversationTokens:    9_000_000,
		ConversationCostCents: 9_999_999,
		HostDailyCostCents:    100,
		Now:                   func() time.Time { return fixed },
	})
	session := auth.SessionContext{ConversationID: "conv_h", HostID: "host_a"}
	ctx := context.Background()
	if err := b.Record(ctx, session, auth.Usage{CostCents: 100}); err != nil {
		t.Fatalf("record: %v", err)
	}
	dec, _ := b.Allow(ctx, session)
	if dec.Allow {
		t.Fatalf("host daily cap should deny")
	}
	wantRetry := time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC)
	if !dec.RetryAt.Equal(wantRetry) {
		t.Fatalf("retry at = %v, want %v", dec.RetryAt, wantRetry)
	}
}

func TestMemoryBudget_HostDailyCapRollsOver(t *testing.T) {
	t.Parallel()
	day1 := time.Date(2026, 5, 17, 23, 59, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 18, 0, 1, 0, 0, time.UTC)
	current := day1
	b := auth.NewMemoryBudget(auth.BudgetConfig{
		ConversationTokens:    9_000_000,
		ConversationCostCents: 9_999_999,
		HostDailyCostCents:    100,
		Now:                   func() time.Time { return current },
	})
	session := auth.SessionContext{ConversationID: "conv_r", HostID: "host_a"}
	ctx := context.Background()
	if err := b.Record(ctx, session, auth.Usage{CostCents: 100}); err != nil {
		t.Fatalf("record: %v", err)
	}
	current = day2
	usage, _ := b.Usage(ctx, session)
	if usage.HostDailyCostCents != 0 {
		t.Fatalf("after rollover host bucket = %d, want 0", usage.HostDailyCostCents)
	}
}

func TestMemoryBudget_RecordRejectsEmptySession(t *testing.T) {
	t.Parallel()
	b := auth.NewMemoryBudget(auth.BudgetConfig{})
	err := b.Record(context.Background(), auth.SessionContext{}, auth.Usage{InputTokens: 10})
	if err == nil {
		t.Fatalf("Record with empty session id must error")
	}
}

func TestUsage_TotalTokens(t *testing.T) {
	t.Parallel()
	u := auth.Usage{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 5, CacheReadTokens: 1}
	if got := u.TotalTokens(); got != 36 {
		t.Fatalf("total = %d, want 36", got)
	}
}
