-- Agent registration and heartbeat persistence backing the agent API
-- endpoints (register, heartbeat, desired state, and status reports). This
-- migration keeps the SQL-backed agent repository aligned with the existing
-- 0001_alpha_core baseline.

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    host_id TEXT REFERENCES hosts(id) ON DELETE SET NULL,
    hostname TEXT,
    agent_version TEXT NOT NULL,
    runtime TEXT NOT NULL DEFAULT 'docker',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_heartbeat_at TIMESTAMPTZ,
    last_status_at TIMESTAMPTZ,
    last_sequence BIGINT NOT NULL DEFAULT 0,
    last_drift JSONB,
    revoked_at TIMESTAMPTZ
);

-- desired_state holds the most recent JSON document published to a given
-- agent. The control plane bumps etag whenever payload changes so the
-- agent's GET ... /desired can short-circuit on If-None-Match.
CREATE TABLE IF NOT EXISTS agent_desired_state (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    etag TEXT NOT NULL,
    payload JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- agent_status_reports records the latest StatusReport posted by each agent.
-- The table is intentionally single-row-per-agent until high-write status
-- history is promoted into a separate retention-managed store.
CREATE TABLE IF NOT EXISTS agent_status_reports (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    instances JSONB NOT NULL DEFAULT '[]'::jsonb,
    drift JSONB,
    received_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agents_host_id ON agents(host_id);
CREATE INDEX IF NOT EXISTS idx_agents_last_heartbeat_at ON agents(last_heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_agent_status_reports_observed_at ON agent_status_reports(observed_at);
