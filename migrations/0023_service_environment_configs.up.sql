-- 0023 — durable per-service/environment config and immutable deployment snapshots.
--
-- Queue payloads continue to contain only identity/source/image handoff data.
-- Build and deploy workers resolve this table at execution time and bind the
-- first observed effective config plus redaction-safe variable version refs to
-- the canonical deployment. No plaintext variable value is stored here.

CREATE TABLE service_environment_configs (
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    root_directory TEXT NOT NULL DEFAULT '',
    builder TEXT NOT NULL DEFAULT 'auto'
        CHECK (builder IN ('auto','dockerfile','railpack')),
    dockerfile_path TEXT NOT NULL DEFAULT 'Dockerfile',
    build_command TEXT NOT NULL DEFAULT '',
    start_command TEXT NOT NULL DEFAULT '',
    predeploy_command TEXT NOT NULL DEFAULT '',
    target_port INTEGER NOT NULL DEFAULT 8080
        CHECK (target_port BETWEEN 1 AND 65535),
    -- A normal service proves its target port is accepting connections before
    -- promotion. `none` is an explicit background-worker/no-port opt-out.
    health_kind TEXT NOT NULL DEFAULT 'tcp'
        CHECK (health_kind IN ('none','http','tcp')),
    health_path TEXT NOT NULL DEFAULT '/',
    health_port INTEGER NOT NULL DEFAULT 0
        CHECK (health_port BETWEEN 0 AND 65535),
    health_timeout_seconds INTEGER NOT NULL DEFAULT 300
        CHECK (health_timeout_seconds BETWEEN 1 AND 86400),
    health_interval_seconds INTEGER NOT NULL DEFAULT 1
        CHECK (health_interval_seconds > 0 AND health_interval_seconds < health_timeout_seconds),
    restart_policy TEXT NOT NULL DEFAULT 'unless-stopped'
        CHECK (restart_policy IN ('no','always','unless-stopped','on-failure')),
    restart_max_retries INTEGER NOT NULL DEFAULT 0
        CHECK (restart_max_retries BETWEEN 0 AND 1000),
    replicas INTEGER NOT NULL DEFAULT 1 CHECK (replicas BETWEEN 1 AND 100),
    overlap_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    drain_grace_seconds INTEGER NOT NULL DEFAULT 30
        CHECK (drain_grace_seconds BETWEEN 1 AND 3600),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (service_id, environment_id),
    CONSTRAINT service_environment_configs_scope_uq
        UNIQUE (service_id, environment_id, project_id),
    CONSTRAINT service_environment_configs_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT service_environment_configs_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT service_environment_configs_restart_retries_chk
        CHECK (restart_policy = 'on-failure' OR restart_max_retries = 0),
    CONSTRAINT service_environment_configs_health_path_chk
        CHECK (health_kind <> 'http' OR left(health_path, 1) = '/')
);

ALTER TABLE deployments
    ADD CONSTRAINT deployments_id_config_scope_uq
    UNIQUE (id, project_id, service_id, environment_id);

CREATE TABLE deployment_config_snapshots (
    deployment_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    config_version BIGINT NOT NULL CHECK (config_version >= 0),
    effective_config JSONB NOT NULL,
    variable_versions JSONB NOT NULL DEFAULT '[]'::jsonb,
    snapshot_sha256 TEXT NOT NULL
        CHECK (snapshot_sha256 ~ '^[a-f0-9]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT deployment_config_snapshots_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, service_id, environment_id)
        REFERENCES deployments (id, project_id, service_id, environment_id)
        ON DELETE CASCADE,
    CONSTRAINT deployment_config_snapshots_config_shape_chk
        CHECK (jsonb_typeof(effective_config) = 'object'),
    CONSTRAINT deployment_config_snapshots_variable_shape_chk
        CHECK (jsonb_typeof(variable_versions) = 'array')
);

CREATE INDEX idx_deployment_config_snapshots_scope
    ON deployment_config_snapshots (project_id, environment_id, service_id, created_at DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (23, 'service_environment_configs', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
