-- Phase 0 alpha core schema, extended in Phase 1 / Lane 1 (DATA) to add the
-- resource graph, audit chain, deployment timeline, and the operation +
-- idempotency columns the panel-locked contract requires. The order of
-- statements here matters: schema_migrations is created first because
-- internal/db/pg/migrate.go writes a row to it after this file applies.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    up_sha256 TEXT NOT NULL DEFAULT '',
    applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ---------------------------------------------------------------
-- 1. Operations / jobs / idempotency triad (panel: OPERATION_AND_
--    IDEMPOTENCY_CONTRACT.md).
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS operations (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL DEFAULT '',
    target_resource_kind TEXT NOT NULL DEFAULT '',
    target_resource_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    code TEXT,
    reason TEXT,
    actor TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT,
    result JSONB,
    error JSONB,
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (CURRENT_TIMESTAMP + INTERVAL '24 hours'),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMPTZ,
    resource_version BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS operation_events (
    id TEXT PRIMARY KEY,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    state TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (operation_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_operation_events_operation_seq ON operation_events(operation_id, seq);

CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    queue TEXT NOT NULL,
    state TEXT NOT NULL,
    operation_id TEXT REFERENCES operations(id) ON DELETE SET NULL,
    partition_key TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    heartbeat_at TIMESTAMPTZ,
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    reason_code TEXT,
    dead_letter_reason TEXT,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Hot-path dequeue index. Per SCHEMA_NOTES.md and SCHEMA_QUEUE_CONTRACT.md
-- we use state='pending' as the indexable equivalent of the panel's
-- runtime predicate (Postgres rejects mutable functions in index WHERE
-- clauses; the queue store applies the runtime lease check at SELECT
-- time, which is correct because the reaper transitions expired-leased
-- rows back to pending).
CREATE INDEX IF NOT EXISTS jobs_dequeue_idx
    ON jobs (queue, priority DESC, available_at ASC)
    WHERE state = 'pending';
CREATE INDEX IF NOT EXISTS jobs_reaper_idx
    ON jobs (state, lease_expires_at)
    WHERE state = 'leased';
CREATE INDEX IF NOT EXISTS jobs_partition_key_idx
    ON jobs (partition_key, queue, state);
CREATE INDEX IF NOT EXISTS idx_jobs_operation_id ON jobs (operation_id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '',
    method TEXT NOT NULL DEFAULT '',
    path_template TEXT NOT NULL DEFAULT '',
    body_hash TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'RESERVED',
    operation_id TEXT REFERENCES operations(id) ON DELETE SET NULL,
    response_status INTEGER,
    response_body BYTEA,
    response_headers JSONB,
    -- legacy columns kept for compatibility with the Phase 0 demo path:
    scope TEXT NOT NULL DEFAULT '',
    request_hash TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (key)
);
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_operation_id ON idempotency_keys (operation_id);
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expiry ON idempotency_keys (expires_at);

CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    token_class TEXT NOT NULL,
    name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_token_class ON api_tokens (token_class);

-- ---------------------------------------------------------------
-- 2. Resource graph (projects → environments → services).
--    Phase 1 / Lane 1: minimum tables to back the API surface.
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS environments (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    name TEXT NOT NULL,
    tier TEXT NOT NULL DEFAULT 'development',
    is_protected BOOLEAN NOT NULL DEFAULT FALSE,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (project_id, slug),
    CONSTRAINT environments_tier_chk
        CHECK (tier IN ('production','staging','preview','development'))
);
CREATE INDEX IF NOT EXISTS idx_environments_project ON environments (project_id);

CREATE TABLE IF NOT EXISTS services (
    id TEXT PRIMARY KEY,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    display_name TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'app',
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    spec JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (project_id, slug)
);
CREATE INDEX IF NOT EXISTS idx_services_project ON services (project_id);

-- ---------------------------------------------------------------
-- 3. Hosts + heartbeats.
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS hosts (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS host_heartbeats (
    host_id TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    observed_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_host_heartbeats_observed_at ON host_heartbeats (observed_at);

-- ---------------------------------------------------------------
-- 4. Deployment timeline events (replaces the in-memory store; ADR-006
--    routing telemetry).
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS timeline_events (
    deployment_id TEXT NOT NULL,
    event_seq BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    tenant_id TEXT NOT NULL DEFAULT '',
    route_name TEXT NOT NULL DEFAULT '',
    host TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (deployment_id, event_seq)
);
CREATE INDEX IF NOT EXISTS idx_timeline_events_deployment ON timeline_events (deployment_id, event_seq);
CREATE INDEX IF NOT EXISTS idx_timeline_events_type ON timeline_events (event_type);

-- ---------------------------------------------------------------
-- 5. Audit chain (panel ADR-011).
-- ---------------------------------------------------------------

CREATE TABLE IF NOT EXISTS audit_logs (
    id BIGSERIAL PRIMARY KEY,
    prev_hash BYTEA NOT NULL,
    row_hash BYTEA NOT NULL,
    canonical_form_version TEXT NOT NULL DEFAULT 'v1',
    request_id TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    actor_id TEXT,
    actor_type TEXT NOT NULL,
    auth_method TEXT,
    workspace_id TEXT,
    project_id TEXT,
    environment_id TEXT,
    resource_kind TEXT NOT NULL,
    resource_id TEXT,
    action TEXT NOT NULL,
    before JSONB,
    after JSONB,
    reason_code TEXT,
    policy_decision TEXT,
    attrs JSONB,
    checkpoint_id BIGINT,
    CONSTRAINT audit_logs_actor_type_chk
        CHECK (actor_type IN ('user','service_account','system','mcp_client'))
);
CREATE INDEX IF NOT EXISTS audit_logs_subject_idx
    ON audit_logs (resource_kind, resource_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS audit_logs_actor_idx
    ON audit_logs (actor_id, occurred_at DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_logs_request_idx
    ON audit_logs (request_id);

CREATE TABLE IF NOT EXISTS audit_checkpoints (
    id BIGSERIAL PRIMARY KEY,
    started_at TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ NOT NULL,
    first_row_id BIGINT NOT NULL,
    last_row_id BIGINT NOT NULL,
    last_row_hash BYTEA NOT NULL,
    signature_alg TEXT NOT NULL DEFAULT '',
    signing_key_fingerprint TEXT NOT NULL DEFAULT '',
    signature BYTEA,
    prev_checkpoint_id BIGINT REFERENCES audit_checkpoints(id) ON DELETE RESTRICT,
    is_final BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ---------------------------------------------------------------
-- 6. Helper indexes.
-- ---------------------------------------------------------------
CREATE INDEX IF NOT EXISTS idx_jobs_queue_state ON jobs(queue, state);
CREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);
CREATE INDEX IF NOT EXISTS idx_operations_kind_state ON operations(kind, state);
CREATE INDEX IF NOT EXISTS idx_operations_target ON operations(target_resource_kind, target_resource_id);
