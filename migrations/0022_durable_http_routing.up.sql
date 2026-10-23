-- 0022 — durable generated/custom domain intent and restart-safe HTTP routing.
--
-- This migration stores desired and observed file-publication revisions. It
-- deliberately does not claim certificate issuance, Traefik acknowledgement,
-- or HTTP(S) reachability: `file_applied` means only that a validated dynamic
-- configuration artifact was atomically written.

CREATE TABLE route_controller_groups (
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    route_group TEXT NOT NULL UNIQUE,
    desired_revision BIGINT NOT NULL DEFAULT 0 CHECK (desired_revision >= 0),
    observed_revision BIGINT NOT NULL DEFAULT 0 CHECK (observed_revision >= 0),
    applied_revision BIGINT NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    apply_state TEXT NOT NULL DEFAULT 'pending',
    artifact_hash TEXT NOT NULL DEFAULT '',
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_detail TEXT NOT NULL DEFAULT '',
    apply_attempts BIGINT NOT NULL DEFAULT 0 CHECK (apply_attempts >= 0),
    last_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, environment_id),
    CONSTRAINT route_controller_groups_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT route_controller_groups_apply_state_chk CHECK (
        apply_state IN (
            'pending', 'private', 'blocked_tls', 'blocked_backend',
            'file_applied', 'failed'
        )
    ),
    CONSTRAINT route_controller_groups_revision_order_chk CHECK (
        observed_revision <= desired_revision
        AND applied_revision <= desired_revision
    ),
    CONSTRAINT route_controller_groups_artifact_hash_chk CHECK (
        artifact_hash = '' OR artifact_hash ~ '^[a-f0-9]{64}$'
    ),
    CONSTRAINT route_controller_groups_name_chk CHECK (
        route_group ~ '^[a-z0-9][a-z0-9.-]{0,127}$'
    )
);

CREATE TABLE domains (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    hostname TEXT NOT NULL,
    normalized_hostname TEXT NOT NULL UNIQUE,
    apps_domain TEXT NOT NULL DEFAULT '',
    tls_desired BOOLEAN NOT NULL DEFAULT FALSE,
    tls_state TEXT NOT NULL DEFAULT 'disabled',
    desired_revision BIGINT NOT NULL CHECK (desired_revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT domains_kind_chk CHECK (kind IN ('generated', 'custom')),
    CONSTRAINT domains_tls_state_chk CHECK (
        tls_state IN ('disabled', 'unavailable', 'ready')
    ),
    CONSTRAINT domains_tls_intent_chk CHECK (
        (tls_desired AND tls_state IN ('unavailable', 'ready'))
        OR (NOT tls_desired AND tls_state = 'disabled')
    ),
    CONSTRAINT domains_normalized_hostname_chk CHECK (
        normalized_hostname = lower(normalized_hostname)
        AND normalized_hostname = btrim(normalized_hostname)
        AND length(normalized_hostname) BETWEEN 3 AND 253
        AND normalized_hostname !~ '[*:/\\`[:space:]]'
        AND normalized_hostname ~
            '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
    ),
    CONSTRAINT domains_apps_domain_chk CHECK (
        apps_domain = ''
        OR (
            apps_domain = lower(apps_domain)
            AND apps_domain ~
                '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
        )
    ),
    CONSTRAINT domains_generated_shape_chk CHECK (
        (kind = 'custom' AND apps_domain = '')
        OR (
            kind = 'generated'
            AND apps_domain <> ''
            AND right(normalized_hostname, length(apps_domain) + 1) = '.' || apps_domain
            AND length(normalized_hostname) - length(apps_domain) - 1 BETWEEN 1 AND 63
            AND position(
                '.' IN left(
                    normalized_hostname,
                    length(normalized_hostname) - length(apps_domain) - 1
                )
            ) = 0
        )
    ),
    CONSTRAINT domains_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT domains_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    UNIQUE (id, project_id, environment_id, service_id)
);

CREATE INDEX idx_domains_scope
    ON domains (project_id, environment_id, service_id);

CREATE TABLE http_routes (
    id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    name TEXT NOT NULL,
    path_prefix TEXT NOT NULL DEFAULT '/',
    target_port INTEGER NOT NULL CHECK (target_port BETWEEN 1 AND 65535),
    public_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    desired_revision BIGINT NOT NULL CHECK (desired_revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT http_routes_path_chk CHECK (
        path_prefix LIKE '/%'
        AND path_prefix !~ '[`\\*?#[:cntrl:]]'
    ),
    CONSTRAINT http_routes_name_chk CHECK (
        name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'
    ),
    CONSTRAINT http_routes_domain_scope_fk
        FOREIGN KEY (domain_id, project_id, environment_id, service_id)
        REFERENCES domains (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT http_routes_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT http_routes_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    UNIQUE (domain_id, path_prefix),
    UNIQUE (project_id, environment_id, name),
    UNIQUE (id, project_id, environment_id, service_id)
);

CREATE INDEX idx_http_routes_scope_public
    ON http_routes (project_id, environment_id, public_enabled, name);

CREATE UNIQUE INDEX idx_deployments_id_full_scope
    ON deployments (id, project_id, environment_id, service_id);

CREATE TABLE route_backends (
    id TEXT PRIMARY KEY,
    route_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    service_url TEXT NOT NULL,
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    desired_revision BIGINT NOT NULL CHECK (desired_revision > 0),
    observed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT route_backends_route_scope_fk
        FOREIGN KEY (route_id, project_id, environment_id, service_id)
        REFERENCES http_routes (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT route_backends_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, environment_id, service_id)
        REFERENCES deployments (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT route_backends_service_url_chk CHECK (
        service_url ~ '^https?://[^[:space:]/?#]+:[0-9]{1,5}(/[^[:space:]?#]*)?$'
    ),
    UNIQUE (route_id, deployment_id, instance_id)
);

CREATE INDEX idx_route_backends_route_ready
    ON route_backends (route_id, ready, service_url);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (22, 'durable_http_routing', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
