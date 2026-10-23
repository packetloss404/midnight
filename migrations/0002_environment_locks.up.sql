-- 0002_environment_locks.up.sql
--
-- Adds the environment_locks table consumed by the promotion gate. The lock
-- identity is (service_id, environment_id), matching the durable
-- single-flight contract:
--
--     "Atomic single-flight per (service, environment): every
--      deployment-mutation path acquires
--      pg_advisory_xact_lock(hashtext('svc:'||service_id||':'||env_id))."
--
-- Postgres advisory locks are session-scoped and do NOT survive a crash;
-- this table is the durable mirror used to surface the lock state in
-- the API, dashboard, CLI, and audit.

CREATE TABLE IF NOT EXISTS environment_locks (
    id              BIGSERIAL PRIMARY KEY,
    service_id      TEXT NOT NULL,
    environment_id  TEXT NOT NULL,
    operation_id    TEXT NOT NULL,
    actor_id        TEXT,
    reason          TEXT,
    held_since      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at     TIMESTAMPTZ,
    UNIQUE (service_id, environment_id, released_at)
);

-- A row with released_at IS NULL is the live lock; the UNIQUE
-- constraint above forbids two unreleased rows for the same
-- (service, env) tuple. Once released_at is set, the row becomes a
-- historical record that can be audited.
CREATE INDEX IF NOT EXISTS idx_environment_locks_active
    ON environment_locks (service_id, environment_id)
    WHERE released_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_environment_locks_held_since
    ON environment_locks (held_since);
