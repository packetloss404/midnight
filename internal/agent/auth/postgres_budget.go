// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresBudget is the Postgres-backed SessionBudget that
// persists per-conversation token + cost tallies plus the
// per-host daily cost bucket. It piggy-backs on the
// agent_revoked_sessions migration (0013) which also creates
// agent_session_budgets + agent_host_budget tables.
//
// Schema:
//
//	CREATE TABLE agent_session_budgets (
//	    session_id TEXT PRIMARY KEY,
//	    tokens_used BIGINT NOT NULL DEFAULT 0,
//	    cost_cents_used BIGINT NOT NULL DEFAULT 0,
//	    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
//	);
//	CREATE TABLE agent_host_budget (
//	    host_id TEXT NOT NULL,
//	    bucket_day DATE NOT NULL,
//	    cost_cents_used BIGINT NOT NULL DEFAULT 0,
//	    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
//	    PRIMARY KEY (host_id, bucket_day)
//	);
//
// PostgresBudget falls back to defaults when no row exists for
// the session / host-day bucket; absence is not an error.
type PostgresBudget struct {
	Pool *pg.Pool
	cfg  BudgetConfig
}

// NewPostgresBudget constructs the budget tracker.
func NewPostgresBudget(pool *pg.Pool, cfg BudgetConfig) *PostgresBudget {
	if cfg.ConversationTokens <= 0 {
		cfg.ConversationTokens = DefaultConversationTokenCap
	}
	if cfg.ConversationCostCents <= 0 {
		cfg.ConversationCostCents = DefaultConversationCostCapCents
	}
	if cfg.HostDailyCostCents <= 0 {
		cfg.HostDailyCostCents = DefaultHostDailyCostCapCents
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &PostgresBudget{Pool: pool, cfg: cfg}
}

// SetClock overrides the wall clock used to compute the host-day
// bucket key.
func (b *PostgresBudget) SetClock(fn func() time.Time) {
	if b == nil || fn == nil {
		return
	}
	b.cfg.Now = fn
}

// Allow consults the persisted buckets.
func (b *PostgresBudget) Allow(ctx context.Context, session SessionContext) (Decision, error) {
	if b == nil || b.Pool == nil {
		return Decision{Allow: false, Reason: BudgetReasonExhausted}, errors.New("postgres budget: pool not initialized")
	}
	usage, err := b.Usage(ctx, session)
	if err != nil {
		return Decision{Allow: false, Reason: BudgetReasonExhausted}, err
	}
	limit := b.cfg.toLimit()
	if dec, exhausted := evaluate(limit, usage, b.cfg.Now()); exhausted {
		dec.Limit = limit
		dec.Used = usage
		return dec, nil
	}
	return Decision{Allow: true, Limit: limit, Used: usage}, nil
}

// Record persists the supplied usage delta into both the per-
// session and the per-host-day rows. The two upserts run in a
// single transaction so a crash cannot leave the host-day bucket
// behind the per-session bucket.
func (b *PostgresBudget) Record(ctx context.Context, session SessionContext, usage Usage) error {
	if b == nil || b.Pool == nil {
		return errors.New("postgres budget: pool not initialized")
	}
	key := session.SessionKey()
	if key == "" {
		return errors.New("postgres budget: session has no id")
	}
	tokens := usage.TotalTokens()
	cents := usage.CostCents
	now := b.cfg.Now().UTC()
	day := now.Format("2006-01-02")
	return b.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_session_budgets (session_id, tokens_used, cost_cents_used, updated_at)
             VALUES ($1, $2, $3, $4)
             ON CONFLICT (session_id) DO UPDATE
                SET tokens_used = agent_session_budgets.tokens_used + EXCLUDED.tokens_used,
                    cost_cents_used = agent_session_budgets.cost_cents_used + EXCLUDED.cost_cents_used,
                    updated_at = EXCLUDED.updated_at`,
			key, tokens, cents, now,
		); err != nil {
			return fmt.Errorf("postgres budget: upsert session: %w", err)
		}
		if session.HostID != "" {
			if _, err := tx.Exec(ctx,
				`INSERT INTO agent_host_budget (host_id, bucket_day, cost_cents_used, updated_at)
                 VALUES ($1, $2::date, $3, $4)
                 ON CONFLICT (host_id, bucket_day) DO UPDATE
                    SET cost_cents_used = agent_host_budget.cost_cents_used + EXCLUDED.cost_cents_used,
                        updated_at = EXCLUDED.updated_at`,
				session.HostID, day, cents, now,
			); err != nil {
				return fmt.Errorf("postgres budget: upsert host: %w", err)
			}
		}
		return nil
	})
}

// Limits returns the active caps.
func (b *PostgresBudget) Limits(ctx context.Context) BudgetLimit {
	if b == nil {
		return BudgetLimit{}
	}
	return b.cfg.toLimit()
}

// Usage returns the accumulated consumption.
func (b *PostgresBudget) Usage(ctx context.Context, session SessionContext) (BudgetUsage, error) {
	if b == nil || b.Pool == nil {
		return BudgetUsage{}, errors.New("postgres budget: pool not initialized")
	}
	var usage BudgetUsage
	key := session.SessionKey()
	if key != "" {
		row := b.Pool.QueryRow(ctx,
			`SELECT tokens_used, cost_cents_used FROM agent_session_budgets WHERE session_id = $1`,
			key,
		)
		if err := row.Scan(&usage.ConversationTokens, &usage.ConversationCostCents); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return BudgetUsage{}, fmt.Errorf("postgres budget: read session: %w", err)
			}
		}
	}
	if session.HostID != "" {
		day := b.cfg.Now().UTC().Format("2006-01-02")
		row := b.Pool.QueryRow(ctx,
			`SELECT cost_cents_used FROM agent_host_budget WHERE host_id = $1 AND bucket_day = $2::date`,
			session.HostID, day,
		)
		if err := row.Scan(&usage.HostDailyCostCents); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return BudgetUsage{}, fmt.Errorf("postgres budget: read host: %w", err)
			}
		}
	}
	return usage, nil
}

func (c BudgetConfig) toLimit() BudgetLimit {
	return BudgetLimit{
		ConversationTokens:    c.ConversationTokens,
		ConversationCostCents: c.ConversationCostCents,
		HostDailyCostCents:    c.HostDailyCostCents,
	}
}
