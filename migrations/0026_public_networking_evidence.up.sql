-- 0026 — evidence-backed public networking workflow.
--
-- Durable route intent landed in 0022. This migration adds the observations
-- required to distinguish "a Traefik file was written" from "the configured
-- ingress acknowledged the route and an HTTP probe reached it". It also
-- persists the deliberately separate DNS-ownership and certificate-order
-- workflows. Neither a pending challenge nor a pending order claims that
-- public DNS or HTTPS is live.

ALTER TABLE route_controller_groups
    DROP CONSTRAINT route_controller_groups_apply_state_chk;

ALTER TABLE route_controller_groups
    ADD CONSTRAINT route_controller_groups_apply_state_chk CHECK (
        apply_state IN (
            'pending', 'private', 'blocked_tls', 'blocked_backend',
            'file_applied', 'ready', 'failed'
        )
    );

CREATE TABLE ingress_environment_enrollments (
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    ingress_id TEXT NOT NULL,
    network_name TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    last_error TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, environment_id),
    CONSTRAINT ingress_environment_enrollments_environment_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT ingress_environment_enrollments_state_chk CHECK (
        state IN ('pending', 'attached', 'verified', 'failed')
    ),
    CONSTRAINT ingress_environment_enrollments_identity_chk CHECK (
        ingress_id = btrim(ingress_id)
        AND ingress_id <> ''
        AND network_name = btrim(network_name)
        AND network_name <> ''
    )
);

CREATE TABLE route_publication_observations (
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    desired_revision BIGINT NOT NULL CHECK (desired_revision > 0),
    artifact_hash TEXT NOT NULL,
    traefik_acknowledged BOOLEAN NOT NULL DEFAULT FALSE,
    ingress_probe_succeeded BOOLEAN NOT NULL DEFAULT FALSE,
    probe_url TEXT NOT NULL DEFAULT '',
    probe_status INTEGER,
    state TEXT NOT NULL DEFAULT 'pending',
    last_error TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, environment_id, desired_revision),
    CONSTRAINT route_publication_observations_group_fk
        FOREIGN KEY (project_id, environment_id)
        REFERENCES route_controller_groups (project_id, environment_id)
        ON DELETE CASCADE,
    CONSTRAINT route_publication_observations_state_chk CHECK (
        state IN ('pending', 'acknowledged', 'ready', 'failed')
    ),
    CONSTRAINT route_publication_observations_hash_chk CHECK (
        artifact_hash ~ '^[a-f0-9]{64}$'
    ),
    CONSTRAINT route_publication_observations_ready_chk CHECK (
        state <> 'ready'
        OR (traefik_acknowledged AND ingress_probe_succeeded)
    )
);

CREATE TABLE domain_dns_challenges (
    domain_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    record_name TEXT NOT NULL,
    record_value TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    observed_values JSONB NOT NULL DEFAULT '[]'::jsonb,
    attempts BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT domain_dns_challenges_domain_scope_fk
        FOREIGN KEY (domain_id, project_id, environment_id, service_id)
        REFERENCES domains (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT domain_dns_challenges_state_chk CHECK (
        state IN ('pending', 'verified', 'failed')
    ),
    CONSTRAINT domain_dns_challenges_record_chk CHECK (
        record_name = lower(btrim(record_name))
        AND record_name <> ''
        AND record_value = btrim(record_value)
        AND record_value <> ''
    )
);

CREATE TABLE domain_certificate_orders (
    domain_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    issuer TEXT NOT NULL,
    issuer_order_ref TEXT NOT NULL DEFAULT '',
    certificate_ref TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending',
    attempts BIGINT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT domain_certificate_orders_domain_scope_fk
        FOREIGN KEY (domain_id, project_id, environment_id, service_id)
        REFERENCES domains (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT domain_certificate_orders_state_chk CHECK (
        state IN ('pending', 'issuing', 'ready', 'failed')
    ),
    CONSTRAINT domain_certificate_orders_ready_chk CHECK (
        state <> 'ready'
        OR (issuer_order_ref <> '' AND certificate_ref <> '' AND expires_at IS NOT NULL)
    )
);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (26, 'public_networking_evidence', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
