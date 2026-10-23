-- 0025_staged_work_outbox.up.sql
--
-- Durable work handoffs beyond HTTP deploy intake. GitHub build intake and
-- build-to-deploy handoff use stable stage-specific dedupe keys, so operation
-- and deployment identities may safely participate in more than one stage.

CREATE TABLE IF NOT EXISTS staged_work_outbox (
    id TEXT PRIMARY KEY,
    dedupe_key TEXT NOT NULL UNIQUE,
    stage TEXT NOT NULL,
    operation_id TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    queue TEXT NOT NULL,
    partition_key TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 8,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,
    last_error TEXT NOT NULL DEFAULT '',
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT staged_work_outbox_stage_chk
        CHECK (stage IN ('github_build_intake','build_deploy_handoff')),
    CONSTRAINT staged_work_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed')),
    CONSTRAINT staged_work_outbox_queue_chk
        CHECK (queue IN ('build','deploy'))
);

CREATE INDEX IF NOT EXISTS staged_work_outbox_dispatch_idx
    ON staged_work_outbox (state, available_at, created_at)
    WHERE state IN ('pending','delivering');
CREATE INDEX IF NOT EXISTS staged_work_outbox_operation_stage_idx
    ON staged_work_outbox (operation_id, stage);
CREATE INDEX IF NOT EXISTS staged_work_outbox_deployment_stage_idx
    ON staged_work_outbox (deployment_id, stage);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (25, 'staged_work_outbox', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
