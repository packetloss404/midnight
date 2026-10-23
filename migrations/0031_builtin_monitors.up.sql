-- 0031_builtin_monitors.up.sql — built-in metric monitors and durable delivery.
--
-- Notification endpoint and signing plaintext never enters these tables.
-- Channels retain only sealed variable identifiers; the worker resolves them
-- immediately before delivery.

CREATE TABLE IF NOT EXISTS monitor_definitions (
    id                         TEXT PRIMARY KEY,
    project_id                 TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id             TEXT NOT NULL,
    service_id                 TEXT NOT NULL,
    name                       TEXT NOT NULL,
    signal                     TEXT NOT NULL,
    comparison_operator        TEXT NOT NULL,
    threshold                  DOUBLE PRECISION NOT NULL,
    window_seconds             INTEGER NOT NULL,
    evaluation_interval_seconds INTEGER NOT NULL,
    consecutive_failures       INTEGER NOT NULL DEFAULT 1,
    consecutive_recoveries     INTEGER NOT NULL DEFAULT 1,
    enabled                    BOOLEAN NOT NULL DEFAULT TRUE,
    state                      TEXT NOT NULL DEFAULT 'unknown',
    failure_streak             INTEGER NOT NULL DEFAULT 0,
    recovery_streak            INTEGER NOT NULL DEFAULT 0,
    last_value                 DOUBLE PRECISION,
    last_evaluated_at          TIMESTAMPTZ,
    next_evaluation_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner                TEXT,
    lease_expires_at           TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT monitor_signal_chk
        CHECK (signal IN ('cpu_percent','memory_percent','restart_count')),
    CONSTRAINT monitor_operator_chk
        CHECK (comparison_operator IN ('gt','gte')),
    CONSTRAINT monitor_threshold_chk CHECK (threshold >= 0),
    CONSTRAINT monitor_window_chk CHECK (window_seconds >= 60),
    CONSTRAINT monitor_interval_chk CHECK (evaluation_interval_seconds >= 60),
    CONSTRAINT monitor_streak_config_chk
        CHECK (consecutive_failures >= 1 AND consecutive_recoveries >= 1),
    CONSTRAINT monitor_streak_state_chk
        CHECK (failure_streak >= 0 AND recovery_streak >= 0),
    CONSTRAINT monitor_state_chk CHECK (state IN ('unknown','healthy','firing')),
    CONSTRAINT monitor_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments(id, project_id) ON DELETE CASCADE,
    CONSTRAINT monitor_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services(id, project_id) ON DELETE CASCADE,
    UNIQUE (service_id, environment_id, name)
);

CREATE INDEX IF NOT EXISTS monitor_definitions_due_idx
    ON monitor_definitions(next_evaluation_at, id)
    WHERE enabled = TRUE;

CREATE TABLE IF NOT EXISTS monitor_channels (
    id                      TEXT PRIMARY KEY,
    project_id              TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    display_name            TEXT NOT NULL,
    channel_type            TEXT NOT NULL DEFAULT 'webhook',
    endpoint_variable_id    TEXT NOT NULL REFERENCES service_variables(id) ON DELETE RESTRICT,
    signing_variable_id     TEXT NOT NULL REFERENCES service_variables(id) ON DELETE RESTRICT,
    signing_key_id          TEXT NOT NULL DEFAULT 'monitor',
    allow_insecure_loopback BOOLEAN NOT NULL DEFAULT FALSE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT monitor_channel_type_chk CHECK (channel_type = 'webhook'),
    UNIQUE (project_id, display_name)
);

CREATE TABLE IF NOT EXISTS monitor_subscriptions (
    monitor_id TEXT NOT NULL REFERENCES monitor_definitions(id) ON DELETE CASCADE,
    channel_id TEXT NOT NULL REFERENCES monitor_channels(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (monitor_id, channel_id)
);

CREATE TABLE IF NOT EXISTS monitor_incidents (
    id          TEXT PRIMARY KEY,
    monitor_id  TEXT NOT NULL REFERENCES monitor_definitions(id) ON DELETE CASCADE,
    opened_at   TIMESTAMPTZ NOT NULL,
    resolved_at TIMESTAMPTZ,
    open_value  DOUBLE PRECISION,
    close_value DOUBLE PRECISION,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS monitor_one_open_incident_idx
    ON monitor_incidents(monitor_id) WHERE resolved_at IS NULL;

CREATE TABLE IF NOT EXISTS monitor_delivery_outbox (
    id             TEXT PRIMARY KEY,
    incident_id    TEXT NOT NULL REFERENCES monitor_incidents(id) ON DELETE CASCADE,
    monitor_id     TEXT NOT NULL REFERENCES monitor_definitions(id) ON DELETE CASCADE,
    channel_id     TEXT NOT NULL REFERENCES monitor_channels(id) ON DELETE CASCADE,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL,
    state          TEXT NOT NULL DEFAULT 'pending',
    attempts       INTEGER NOT NULL DEFAULT 0,
    available_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_owner    TEXT,
    lease_expires_at TIMESTAMPTZ,
    response_status INTEGER,
    last_error     TEXT NOT NULL DEFAULT '',
    delivered_at   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT monitor_delivery_event_chk
        CHECK (event_type IN ('monitor.firing','monitor.resolved')),
    CONSTRAINT monitor_delivery_state_chk
        CHECK (state IN ('pending','leased','delivered','dead')),
    CONSTRAINT monitor_delivery_attempts_chk CHECK (attempts >= 0),
    UNIQUE (incident_id, channel_id, event_type)
);
CREATE INDEX IF NOT EXISTS monitor_delivery_due_idx
    ON monitor_delivery_outbox(available_at, id)
    WHERE state IN ('pending','leased');

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (31, 'builtin_monitors', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
