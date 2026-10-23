-- 0030_deployment_predeploy_runs.up.sql
--
-- Crash-safe, one-shot pre-deploy command execution. The row contains only
-- redaction-safe execution metadata: the command and resolved variable
-- version identities remain in the immutable deployment snapshot and are
-- bound here by spec_sha256. Plaintext environment values are never stored.

CREATE TABLE deployment_predeploy_runs (
    deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    spec_sha256 TEXT NOT NULL CHECK (spec_sha256 ~ '^[a-f0-9]{64}$'),
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','running','succeeded','failed')),
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
    CONSTRAINT deployment_predeploy_runs_scope_fk
        FOREIGN KEY (deployment_id, project_id, service_id, environment_id)
        REFERENCES deployments (id, project_id, service_id, environment_id)
        ON DELETE CASCADE,
    CONSTRAINT deployment_predeploy_runs_terminal_chk
        CHECK (
            (state IN ('pending','running') AND completed_at IS NULL)
            OR
            (state IN ('succeeded','failed') AND completed_at IS NOT NULL)
        ),
    CONSTRAINT deployment_predeploy_runs_result_chk
        CHECK (
            (
                state IN ('pending','running')
                AND exit_code IS NULL
                AND failure_code = ''
                AND failure_message = ''
            )
            OR (
                state = 'succeeded'
                AND exit_code IS NOT NULL
                AND exit_code = 0
                AND failure_code = ''
                AND failure_message = ''
            )
            OR (
                state = 'failed'
                AND exit_code IS NOT NULL
                AND failure_code <> ''
            )
        )
);

CREATE INDEX deployment_predeploy_runs_scope_updated_idx
    ON deployment_predeploy_runs (project_id, environment_id, service_id, updated_at DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (30, 'deployment_predeploy_runs', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
