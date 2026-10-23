-- 0019_deploy_intake_outbox.up.sql
--
-- Makes REST deploy/up intake durable before work is made visible to a worker.
-- The API commits the operation, canonical deployment, initial event,
-- idempotency linkage, and this outbox row in one transaction. A lease-safe
-- dispatcher then copies the intent into jobs. jobs.outbox_id makes delivery
-- idempotent when a dispatcher loses its lease or crashes after inserting.

ALTER TABLE jobs
    ADD COLUMN IF NOT EXISTS outbox_id TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS jobs_outbox_id_uq
    ON jobs (outbox_id)
    WHERE outbox_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS deploy_intake_outbox (
    id TEXT PRIMARY KEY,
    trigger_key TEXT NOT NULL UNIQUE,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL UNIQUE REFERENCES deployments(id) ON DELETE CASCADE,
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
    CONSTRAINT deploy_intake_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed')),
    CONSTRAINT deploy_intake_outbox_queue_chk
        CHECK (queue IN ('build','deploy'))
);

CREATE INDEX IF NOT EXISTS deploy_intake_outbox_dispatch_idx
    ON deploy_intake_outbox (state, available_at, created_at)
    WHERE state IN ('pending','delivering');

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (19, 'deploy_intake_outbox', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
