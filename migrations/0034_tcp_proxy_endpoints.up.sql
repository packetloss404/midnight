-- 0034_tcp_proxy_endpoints.up.sql — durable single-host TCP publication.
--
-- A row separates desired exposure from observed listener/backend evidence.
-- The partial uniqueness constraints retain withdrawn history while allowing
-- its port/hostname to be reused only after the controller records the old
-- listener as closed.

CREATE TABLE tcp_proxy_endpoints (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    host TEXT NOT NULL,
    external_port INTEGER NOT NULL CHECK (external_port BETWEEN 1024 AND 65535),
    backend_port INTEGER NOT NULL CHECK (backend_port BETWEEN 1 AND 65535),
    allowed_source_cidrs TEXT[] NOT NULL,
    desired_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    apply_state TEXT NOT NULL DEFAULT 'pending',
    listener_bound BOOLEAN NOT NULL DEFAULT FALSE,
    backend_reachable BOOLEAN NOT NULL DEFAULT FALSE,
    controller_id TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_detail TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT tcp_proxy_endpoints_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT tcp_proxy_endpoints_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT tcp_proxy_endpoints_host_chk CHECK (
        host = lower(btrim(host))
        AND length(host) BETWEEN 1 AND 253
        AND host !~ '[/:`[:space:]]'
    ),
    CONSTRAINT tcp_proxy_endpoints_state_chk CHECK (
        apply_state IN ('pending', 'active', 'withdrawn', 'failed')
    ),
    CONSTRAINT tcp_proxy_endpoints_evidence_chk CHECK (
        (apply_state <> 'active')
        OR (desired_enabled AND listener_bound AND backend_reachable)
    )
);

CREATE UNIQUE INDEX idx_tcp_proxy_active_scope
    ON tcp_proxy_endpoints (project_id, environment_id, service_id)
    WHERE desired_enabled;

CREATE UNIQUE INDEX idx_tcp_proxy_reserved_host
    ON tcp_proxy_endpoints (host)
    WHERE desired_enabled OR listener_bound;

CREATE UNIQUE INDEX idx_tcp_proxy_reserved_port
    ON tcp_proxy_endpoints (external_port)
    WHERE desired_enabled OR listener_bound;

CREATE INDEX idx_tcp_proxy_scope
    ON tcp_proxy_endpoints (project_id, environment_id, service_id, created_at);

CREATE INDEX idx_tcp_proxy_runtime
    ON tcp_proxy_endpoints (external_port)
    WHERE desired_enabled OR listener_bound OR apply_state <> 'withdrawn';
