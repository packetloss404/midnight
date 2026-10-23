-- 0032_cron_runtime_definitions.up.sql
--
-- A cron deployment is an immutable active definition, not a long-running
-- service container. Cursors select due UTC occurrences and each occurrence
-- retains a redaction-safe one-off result so worker restarts never rerun a
-- command that has already reached a terminal Docker state.

ALTER TABLE service_environment_configs
    ADD COLUMN runtime_kind TEXT NOT NULL DEFAULT 'service'
        CHECK (runtime_kind IN ('service','background','cron')),
    ADD COLUMN cron_schedule TEXT NOT NULL DEFAULT '',
    ADD COLUMN cron_timeout_seconds INTEGER NOT NULL DEFAULT 600
        CHECK (cron_timeout_seconds BETWEEN 1 AND 86400),
    ADD COLUMN cron_max_attempts INTEGER NOT NULL DEFAULT 1
        CHECK (cron_max_attempts BETWEEN 1 AND 10),
    ADD COLUMN cron_retry_delay_seconds INTEGER NOT NULL DEFAULT 60
        CHECK (cron_retry_delay_seconds BETWEEN 1 AND 86400),
    ADD CONSTRAINT service_environment_configs_cron_shape_chk CHECK (
        (runtime_kind = 'cron' AND cron_schedule <> '')
        OR (runtime_kind <> 'cron' AND cron_schedule = '')
    );

CREATE TABLE cron_schedule_cursors (
    deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    snapshot_sha256 TEXT NOT NULL CHECK (snapshot_sha256 ~ '^[a-f0-9]{64}$'),
    image_ref TEXT NOT NULL,
    schedule TEXT NOT NULL,
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 86400),
    max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
    retry_delay_seconds INTEGER NOT NULL CHECK (retry_delay_seconds BETWEEN 1 AND 86400),
    last_evaluated_at TIMESTAMPTZ NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1 CHECK (resource_version > 0),
    CONSTRAINT cron_schedule_cursors_scope_fk FOREIGN KEY
        (deployment_id, project_id, service_id, environment_id)
        REFERENCES deployments (id, project_id, service_id, environment_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX cron_schedule_cursors_active_scope_uq
    ON cron_schedule_cursors (project_id, service_id, environment_id)
    WHERE enabled;

CREATE TABLE cron_occurrence_runs (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    due_at TIMESTAMPTZ NOT NULL,
    snapshot_sha256 TEXT NOT NULL CHECK (snapshot_sha256 ~ '^[a-f0-9]{64}$'),
    image_ref TEXT NOT NULL,
    spec_sha256 TEXT NOT NULL CHECK (spec_sha256 ~ '^[a-f0-9]{64}$'),
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','running','retry_wait','succeeded','failed')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    max_attempts INTEGER NOT NULL CHECK (max_attempts BETWEEN 1 AND 10),
    timeout_seconds INTEGER NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 86400),
    retry_at TIMESTAMPTZ,
	attempt_started_at TIMESTAMPTZ,
    container_name TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    exit_code INTEGER,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1 CHECK (resource_version > 0),
    CONSTRAINT cron_occurrence_runs_scope_fk FOREIGN KEY
        (deployment_id, project_id, service_id, environment_id)
        REFERENCES deployments (id, project_id, service_id, environment_id) ON DELETE CASCADE,
    CONSTRAINT cron_occurrence_runs_unique_due UNIQUE (deployment_id, due_at),
    CONSTRAINT cron_occurrence_runs_terminal_chk CHECK (
        (state IN ('pending','running','retry_wait') AND completed_at IS NULL)
        OR (state IN ('succeeded','failed') AND completed_at IS NOT NULL)
    )
);

-- Forbid overlap at the database boundary. A retry remains the same occurrence
-- and uses the same deterministic one-off resource; a later due occurrence is
-- held pending until no active occurrence exists for the definition.
CREATE UNIQUE INDEX cron_occurrence_runs_no_overlap_uq
    ON cron_occurrence_runs (project_id, service_id, environment_id)
    WHERE state IN ('pending','running','retry_wait');
CREATE INDEX cron_occurrence_runs_due_idx
    ON cron_occurrence_runs (state, retry_at, due_at);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (32, 'cron_runtime_definitions', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
