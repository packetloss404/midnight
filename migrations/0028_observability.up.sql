-- 0028_observability.up.sql — durable, scoped logs and runtime metrics.
--
-- Log cursor positions are internal BIGSERIAL ids. API clients only receive
-- the authenticated/encrypted cursor envelope from internal/api/log_cursor.go.

CREATE TABLE IF NOT EXISTS observability_log_records (
    id              BIGSERIAL PRIMARY KEY,
    observed_at     TIMESTAMPTZ NOT NULL,
    ingested_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    project_id      TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id  TEXT,
    service_id      TEXT,
    deployment_id   TEXT,
    operation_id    TEXT REFERENCES operations(id) ON DELETE SET NULL,
    replica_id      TEXT NOT NULL DEFAULT '',
    surface         TEXT NOT NULL,
    stream          TEXT NOT NULL DEFAULT '',
    level           TEXT NOT NULL DEFAULT 'info',
    component       TEXT NOT NULL,
    message         TEXT NOT NULL,
    attributes      JSONB NOT NULL DEFAULT '{}'::jsonb,
    source_key      TEXT NOT NULL,
    source_sequence TEXT NOT NULL,
    CONSTRAINT observability_log_surface_chk
        CHECK (surface IN ('build','runtime','http','platform')),
    CONSTRAINT observability_log_stream_chk
        CHECK (stream IN ('','stdout','stderr','access')),
    CONSTRAINT observability_log_message_size_chk
        CHECK (octet_length(message) <= 65536),
    CONSTRAINT observability_log_attributes_size_chk
        CHECK (octet_length(attributes::text) <= 16384),
    CONSTRAINT observability_log_deployment_scope_chk
        CHECK (deployment_id IS NULL OR (environment_id IS NOT NULL AND service_id IS NOT NULL)),
    CONSTRAINT observability_log_operation_scope_chk
        CHECK (operation_id IS NULL OR deployment_id IS NOT NULL),
    CONSTRAINT observability_log_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_log_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_log_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, environment_id, service_id)
        REFERENCES deployments (id, project_id, environment_id, service_id) ON DELETE CASCADE,
    UNIQUE (source_key, source_sequence)
);

CREATE INDEX IF NOT EXISTS observability_logs_project_time_idx
    ON observability_log_records (project_id, observed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS observability_logs_environment_time_idx
    ON observability_log_records (environment_id, observed_at DESC, id DESC)
    WHERE environment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_logs_service_time_idx
    ON observability_log_records (service_id, observed_at DESC, id DESC)
    WHERE service_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_logs_deployment_time_idx
    ON observability_log_records (deployment_id, observed_at DESC, id DESC)
    WHERE deployment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_logs_operation_time_idx
    ON observability_log_records (operation_id, observed_at DESC, id DESC)
    WHERE operation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_logs_replica_time_idx
    ON observability_log_records (replica_id, observed_at DESC, id DESC)
    WHERE replica_id <> '';

CREATE TABLE IF NOT EXISTS observability_ingestion_windows (
    source_key       TEXT NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL,
    project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id   TEXT,
    service_id       TEXT,
    accepted_count   BIGINT NOT NULL DEFAULT 0,
    dropped_count    BIGINT NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_key, window_started_at),
    CONSTRAINT observability_ingestion_counts_chk
        CHECK (accepted_count >= 0 AND dropped_count >= 0),
    CONSTRAINT observability_ingestion_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_ingestion_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS observability_ingestion_window_time_idx
    ON observability_ingestion_windows (window_started_at DESC);
CREATE INDEX IF NOT EXISTS observability_ingestion_scope_time_idx
    ON observability_ingestion_windows (project_id, environment_id, service_id, window_started_at DESC);

CREATE TABLE IF NOT EXISTS observability_collector_checkpoints (
    source_key       TEXT PRIMARY KEY,
    last_observed_at TIMESTAMPTZ NOT NULL,
    cursor           JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS observability_collectors (
    collector_id   TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    status         TEXT NOT NULL,
    last_error     TEXT NOT NULL DEFAULT '',
    last_seen_at   TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT observability_collector_status_chk
        CHECK (status IN ('active','degraded'))
);
CREATE INDEX IF NOT EXISTS observability_collectors_seen_idx
    ON observability_collectors (last_seen_at DESC);

CREATE TABLE IF NOT EXISTS observability_metric_samples (
    id                  BIGSERIAL PRIMARY KEY,
    observed_at         TIMESTAMPTZ NOT NULL,
    ingested_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    project_id          TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id      TEXT,
    service_id          TEXT NOT NULL,
    deployment_id       TEXT,
    replica_id          TEXT NOT NULL DEFAULT '',
    source              TEXT NOT NULL DEFAULT 'docker',
    sample_resolution   TEXT NOT NULL DEFAULT 'raw',
    cpu_percent         DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_bytes        BIGINT NOT NULL DEFAULT 0,
    memory_limit_bytes  BIGINT NOT NULL DEFAULT 0,
    network_rx_bytes    BIGINT NOT NULL DEFAULT 0,
    network_tx_bytes    BIGINT NOT NULL DEFAULT 0,
    block_read_bytes    BIGINT NOT NULL DEFAULT 0,
    block_write_bytes   BIGINT NOT NULL DEFAULT 0,
    disk_usage_bytes    BIGINT,
    restart_count       BIGINT NOT NULL DEFAULT 0,
    marker              TEXT NOT NULL DEFAULT '',
    CONSTRAINT observability_metric_values_chk CHECK (
        cpu_percent >= 0 AND memory_bytes >= 0 AND memory_limit_bytes >= 0 AND
        network_rx_bytes >= 0 AND network_tx_bytes >= 0 AND
        block_read_bytes >= 0 AND block_write_bytes >= 0 AND
        (disk_usage_bytes IS NULL OR disk_usage_bytes >= 0) AND restart_count >= 0
    ),
    CONSTRAINT observability_metric_resolution_chk
        CHECK (sample_resolution IN ('raw','1m','15m')),
    CONSTRAINT observability_metric_deployment_scope_chk
        CHECK (deployment_id IS NULL OR environment_id IS NOT NULL),
    CONSTRAINT observability_metric_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_metric_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_metric_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, environment_id, service_id)
        REFERENCES deployments (id, project_id, environment_id, service_id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS observability_metrics_source_sequence_idx
    ON observability_metric_samples (
        source, project_id, service_id, COALESCE(deployment_id, ''),
        replica_id, observed_at, sample_resolution
    );
CREATE INDEX IF NOT EXISTS observability_metrics_service_time_idx
    ON observability_metric_samples (service_id, observed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS observability_metrics_environment_time_idx
    ON observability_metric_samples (environment_id, observed_at DESC, id DESC)
    WHERE environment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_metrics_deployment_time_idx
    ON observability_metric_samples (deployment_id, observed_at DESC, id DESC)
    WHERE deployment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS observability_metrics_replica_time_idx
    ON observability_metric_samples (replica_id, observed_at DESC, id DESC)
    WHERE replica_id <> '';

CREATE TABLE IF NOT EXISTS observability_deployment_markers (
    deployment_id  TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL,
    service_id     TEXT NOT NULL,
    marker         TEXT NOT NULL,
    observed_at    TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT observability_marker_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_marker_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE,
    CONSTRAINT observability_marker_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, environment_id, service_id)
        REFERENCES deployments (id, project_id, environment_id, service_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS observability_markers_service_time_idx
    ON observability_deployment_markers (service_id, observed_at DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (28, 'observability', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
