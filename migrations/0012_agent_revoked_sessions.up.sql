-- 0010_agent_revoked_sessions.up.sql — agent session kill-switch
-- and per-session / per-host budget tables (ADR-0027).
--
-- COORDINATION NOTE: this worktree branched from main before
-- migrations 10 (variable_references) and 11 (service_github_index)
-- and before the chat-backend track's expected 0012_agent_conversations
-- migration landed. The target slot in the per-track plan is 0013;
-- on merge into main, renumber this file (and the schema_version
-- bump below) to whatever is next after the conversations migration
-- — the schema is independent (no FK to conversations) so the
-- renumber is mechanical.
--
-- agent_revoked_sessions is the kill-switch table. INSERT-only so
-- the original revoker / reason / timestamp survive a re-revoke.
CREATE TABLE IF NOT EXISTS agent_revoked_sessions (
    session_id TEXT PRIMARY KEY,
    actor_id   TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL DEFAULT '',
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agent_revoked_sessions_revoked_at
    ON agent_revoked_sessions (revoked_at DESC);

-- agent_session_budgets accumulates per-conversation token + cost
-- consumption. The chat-backend's stream loop upserts after each
-- turn so a process restart does not reset the cap.
CREATE TABLE IF NOT EXISTS agent_session_budgets (
    session_id       TEXT PRIMARY KEY,
    tokens_used      BIGINT NOT NULL DEFAULT 0,
    cost_cents_used  BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT agent_session_budgets_tokens_nonneg CHECK (tokens_used >= 0),
    CONSTRAINT agent_session_budgets_cost_nonneg CHECK (cost_cents_used >= 0)
);

-- agent_host_budget is the per-host-day cost bucket. The
-- (host_id, bucket_day) primary key lets the chat-backend upsert
-- with ON CONFLICT and the bucket rolls at UTC midnight.
CREATE TABLE IF NOT EXISTS agent_host_budget (
    host_id          TEXT NOT NULL,
    bucket_day       DATE NOT NULL,
    cost_cents_used  BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (host_id, bucket_day),
    CONSTRAINT agent_host_budget_cost_nonneg CHECK (cost_cents_used >= 0)
);

CREATE INDEX IF NOT EXISTS idx_agent_host_budget_day
    ON agent_host_budget (bucket_day DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (10, 'agent_revoked_sessions', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
