// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// Default budget caps. Operators override via environment.
const (
	// DefaultConversationTokenCap caps the total tokens billed
	// against a single conversation. 200k matches the
	// largest default context window the chat surface is expected to use.
	DefaultConversationTokenCap int64 = 200_000

	// DefaultConversationCostCapCents caps the total cost billed
	// against a single conversation, in USD cents (so $5.00).
	DefaultConversationCostCapCents int64 = 500

	// DefaultHostDailyCostCapCents caps total daily spend across
	// every conversation on a single host, in USD cents ($50.00).
	DefaultHostDailyCostCapCents int64 = 5_000

	// EnvConversationTokenCap overrides DefaultConversationTokenCap.
	EnvConversationTokenCap = "MIDNIGHT_AGENT_CONVERSATION_TOKEN_CAP"
	// EnvConversationCostCapCents overrides DefaultConversationCostCapCents.
	EnvConversationCostCapCents = "MIDNIGHT_AGENT_CONVERSATION_COST_CENTS"
	// EnvHostDailyCostCapCents overrides DefaultHostDailyCostCapCents.
	EnvHostDailyCostCapCents = "MIDNIGHT_AGENT_HOST_DAILY_COST_CENTS"
)

// Usage is the agent-core-reported token + cost delta for a single
// turn. Callers MUST supply non-negative deltas.
type Usage struct {
	// InputTokens billed to the user.
	InputTokens int64
	// OutputTokens billed to the user.
	OutputTokens int64
	// CacheCreationTokens billed to the user (prompt caching writes).
	CacheCreationTokens int64
	// CacheReadTokens billed to the user (prompt caching reads).
	CacheReadTokens int64
	// CostCents is the upstream-reported cost in USD cents.
	CostCents int64
	// Model is the canonical provider model id (e.g. "claude-opus-4-7").
	Model string
}

// TotalTokens returns the sum of every token bucket.
func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

// Decision is the budget gate's verdict for a single turn.
type Decision struct {
	// Allow is true when the session may proceed with another
	// turn. false signals an exhausted cap.
	Allow bool
	// Reason carries a stable error code when Allow is false.
	// Recognized values: "budget_exhausted" (any cap), "revoked"
	// (set by the chat-backend after a revoke), "".
	Reason string
	// RetryAt is set when the failing cap rolls over at a known
	// future time (the host-daily cap rolls at UTC midnight). Zero
	// when the failure is permanent for this session.
	RetryAt time.Time
	// Limit + Used carry the cap and the current consumption so
	// the chat-backend's `agent:budget_exhausted` event can
	// surface the breach to the user.
	Limit BudgetLimit
	Used  BudgetUsage
}

// BudgetReasonExhausted is the canonical Reason string the chat
// backend matches on. Keeping it a constant avoids drift between
// emit / consume sites.
const BudgetReasonExhausted = "budget_exhausted"

// BudgetLimit describes the active caps at the time of a Decision.
type BudgetLimit struct {
	ConversationTokens    int64
	ConversationCostCents int64
	HostDailyCostCents    int64
}

// BudgetUsage describes the accumulated consumption at the time of
// a Decision. The chat-backend surfaces these to the user.
type BudgetUsage struct {
	ConversationTokens    int64
	ConversationCostCents int64
	HostDailyCostCents    int64
}

// SessionBudget tracks cumulative usage per conversation + per
// host-day and decides whether to allow another turn. Allow MUST
// be cheap — it runs on every turn — and Record MUST be durable:
// the chat-backend invokes Record after each turn so a process
// restart does not reset the conversation cap.
type SessionBudget interface {
	// Allow returns the verdict for one more turn. The check is
	// read-only; callers persist the resulting consumption via
	// Record.
	Allow(ctx context.Context, session SessionContext) (Decision, error)
	// Record persists the supplied Usage against the session +
	// host-day buckets. Implementations MUST be idempotent on
	// repeated calls with the same (session, usage) pair only
	// when the caller threads a stable turn id through; the
	// default chat-backend does not, so naive callers should not
	// retry Record on transient failures.
	Record(ctx context.Context, session SessionContext, usage Usage) error
	// Limits returns the active caps. Used by the chat-backend's
	// budget-status endpoint.
	Limits(ctx context.Context) BudgetLimit
	// Usage returns the accumulated consumption for a session +
	// the host-day bucket. Used for both the gate verdict and the
	// status endpoint.
	Usage(ctx context.Context, session SessionContext) (BudgetUsage, error)
}

// BudgetConfig is the constructor input for the default budgets.
// Zero-value fields fall back to the Default* / Env* sources.
type BudgetConfig struct {
	ConversationTokens    int64
	ConversationCostCents int64
	HostDailyCostCents    int64
	Now                   func() time.Time
}

// LoadBudgetConfig reads the operator env overrides on top of the
// Default* constants. Callers MUST treat the returned struct as
// immutable.
func LoadBudgetConfig() BudgetConfig {
	cfg := BudgetConfig{
		ConversationTokens:    DefaultConversationTokenCap,
		ConversationCostCents: DefaultConversationCostCapCents,
		HostDailyCostCents:    DefaultHostDailyCostCapCents,
	}
	if v := os.Getenv(EnvConversationTokenCap); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.ConversationTokens = n
		}
	}
	if v := os.Getenv(EnvConversationCostCapCents); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.ConversationCostCents = n
		}
	}
	if v := os.Getenv(EnvHostDailyCostCapCents); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.HostDailyCostCents = n
		}
	}
	return cfg
}

// MemoryBudget is the in-memory SessionBudget used by tests and
// stacks without a Postgres pool. Concurrent-safe.
type MemoryBudget struct {
	cfg       BudgetConfig
	mu        sync.Mutex
	bySession map[string]*sessionBucket
	byHostDay map[string]int64
	hostKnown map[string]struct{}
	nowFn     func() time.Time
}

type sessionBucket struct {
	Tokens int64
	Cents  int64
}

// NewMemoryBudget constructs a budget tracker.
func NewMemoryBudget(cfg BudgetConfig) *MemoryBudget {
	if cfg.ConversationTokens <= 0 {
		cfg.ConversationTokens = DefaultConversationTokenCap
	}
	if cfg.ConversationCostCents <= 0 {
		cfg.ConversationCostCents = DefaultConversationCostCapCents
	}
	if cfg.HostDailyCostCents <= 0 {
		cfg.HostDailyCostCents = DefaultHostDailyCostCapCents
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MemoryBudget{
		cfg:       cfg,
		bySession: map[string]*sessionBucket{},
		byHostDay: map[string]int64{},
		hostKnown: map[string]struct{}{},
		nowFn:     now,
	}
}

// SetClock overrides the wall clock used to compute the host-day
// bucket key. Tests use this to roll the bucket without sleeping.
func (b *MemoryBudget) SetClock(fn func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if fn != nil {
		b.nowFn = fn
	}
}

// Allow consults the cumulative session + host-day buckets.
func (b *MemoryBudget) Allow(ctx context.Context, session SessionContext) (Decision, error) {
	if b == nil {
		return Decision{Allow: false, Reason: BudgetReasonExhausted}, errors.New("memory budget: not initialized")
	}
	key := session.SessionKey()
	b.mu.Lock()
	defer b.mu.Unlock()
	sb := b.bySession[key]
	if sb == nil {
		sb = &sessionBucket{}
	}
	hostKey := hostDayKey(session.HostID, b.nowFn())
	hostCents := b.byHostDay[hostKey]
	limit := b.limitsLocked()
	usage := BudgetUsage{
		ConversationTokens:    sb.Tokens,
		ConversationCostCents: sb.Cents,
		HostDailyCostCents:    hostCents,
	}
	if dec, exhausted := evaluate(limit, usage, b.nowFn()); exhausted {
		dec.Limit = limit
		dec.Used = usage
		return dec, nil
	}
	return Decision{Allow: true, Limit: limit, Used: usage}, nil
}

// Record persists the supplied usage.
func (b *MemoryBudget) Record(ctx context.Context, session SessionContext, usage Usage) error {
	if b == nil {
		return errors.New("memory budget: not initialized")
	}
	key := session.SessionKey()
	if key == "" {
		return errors.New("memory budget: session has no id")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sb := b.bySession[key]
	if sb == nil {
		sb = &sessionBucket{}
		b.bySession[key] = sb
	}
	sb.Tokens += usage.TotalTokens()
	sb.Cents += usage.CostCents
	hostKey := hostDayKey(session.HostID, b.nowFn())
	b.byHostDay[hostKey] += usage.CostCents
	if session.HostID != "" {
		b.hostKnown[session.HostID] = struct{}{}
	}
	return nil
}

// Limits returns the active caps.
func (b *MemoryBudget) Limits(ctx context.Context) BudgetLimit {
	if b == nil {
		return BudgetLimit{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limitsLocked()
}

// Usage returns the accumulated consumption.
func (b *MemoryBudget) Usage(ctx context.Context, session SessionContext) (BudgetUsage, error) {
	if b == nil {
		return BudgetUsage{}, errors.New("memory budget: not initialized")
	}
	key := session.SessionKey()
	b.mu.Lock()
	defer b.mu.Unlock()
	sb := b.bySession[key]
	if sb == nil {
		sb = &sessionBucket{}
	}
	hostKey := hostDayKey(session.HostID, b.nowFn())
	hostCents := b.byHostDay[hostKey]
	return BudgetUsage{
		ConversationTokens:    sb.Tokens,
		ConversationCostCents: sb.Cents,
		HostDailyCostCents:    hostCents,
	}, nil
}

func (b *MemoryBudget) limitsLocked() BudgetLimit {
	return BudgetLimit{
		ConversationTokens:    b.cfg.ConversationTokens,
		ConversationCostCents: b.cfg.ConversationCostCents,
		HostDailyCostCents:    b.cfg.HostDailyCostCents,
	}
}

func hostDayKey(host string, t time.Time) string {
	day := t.UTC().Format("2006-01-02")
	return fmt.Sprintf("%s\x00%s", host, day)
}

// evaluate is the shared verdict logic used by every SessionBudget
// implementation. Returns a Decision + a bool reporting whether
// any cap has tripped.
func evaluate(limit BudgetLimit, used BudgetUsage, now time.Time) (Decision, bool) {
	if limit.ConversationTokens > 0 && used.ConversationTokens >= limit.ConversationTokens {
		return Decision{Allow: false, Reason: BudgetReasonExhausted}, true
	}
	if limit.ConversationCostCents > 0 && used.ConversationCostCents >= limit.ConversationCostCents {
		return Decision{Allow: false, Reason: BudgetReasonExhausted}, true
	}
	if limit.HostDailyCostCents > 0 && used.HostDailyCostCents >= limit.HostDailyCostCents {
		// Host-daily caps roll at UTC midnight; surface the retry
		// time so the chat-backend can defer rather than bail.
		next := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day()+1, 0, 0, 0, 0, time.UTC)
		return Decision{Allow: false, Reason: BudgetReasonExhausted, RetryAt: next}, true
	}
	return Decision{}, false
}
