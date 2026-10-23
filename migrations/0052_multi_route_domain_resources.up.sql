-- 0052 — tenant-managed generated domains and multi-route deployment ownership.
--
-- A domain's resource_version is product mutation CAS state. It is deliberately
-- independent from desired_revision, which belongs to the environment-wide
-- route controller and changes as publication intent converges.
ALTER TABLE domains
    ADD COLUMN resource_version BIGINT NOT NULL DEFAULT 1;

ALTER TABLE domains
    ADD CONSTRAINT domains_resource_version_chk
    CHECK (resource_version > 0);

-- Product-domain identity retirement survives deletion. The row deliberately
-- has no FK to domains: it is an ABA fence for an identity after the live
-- domain and all of its dependent route state have been removed.
CREATE TABLE retired_product_domain_ids (
    domain_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    retired_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_retired_product_domain_ids_scope
    ON retired_product_domain_ids (
        project_id, environment_id, service_id, retired_at, domain_id
    );

-- Generated product domains are a singleton per exact service/environment.
-- Existing generated identities are deterministic, so an unexpected duplicate
-- fails with actionable scope evidence instead of choosing one and silently
-- dropping intent. This can happen after an operator rotates the apps domain
-- and creates a second recovery-owned generated hostname.
DO $$
DECLARE
    duplicate_scope RECORD;
BEGIN
    SELECT project_id, environment_id, service_id, count(*) AS domain_count
      INTO duplicate_scope
      FROM domains
     WHERE kind = 'generated'
     GROUP BY project_id, environment_id, service_id
    HAVING count(*) > 1
     ORDER BY project_id, environment_id, service_id
     LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot enable generated-domain singleton: project %, environment %, service % has % generated domains; remove or convert obsolete recovery intent before retrying migration 0052',
            duplicate_scope.project_id,
            duplicate_scope.environment_id,
            duplicate_scope.service_id,
            duplicate_scope.domain_count;
    END IF;
END
$$;

CREATE UNIQUE INDEX idx_domains_one_generated_per_service_environment
    ON domains (project_id, environment_id, service_id)
    WHERE kind = 'generated';

-- This exact composite key lets a service-level attachment prove that its
-- route belongs to its domain and immutable tenant scope in one FK.
CREATE UNIQUE INDEX idx_http_routes_id_domain_scope
    ON http_routes (
        id, domain_id, project_id, environment_id, service_id
    );

-- Only rows in this table are inherited by future deployments. Historical
-- deployment-local route opt-ins remain in deployment_route_intents and are
-- intentionally not backfilled here.
CREATE TABLE service_domain_bindings (
    route_id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT service_domain_bindings_route_domain_scope_fk
        FOREIGN KEY (
            route_id, domain_id, project_id, environment_id, service_id
        )
        REFERENCES http_routes (
            id, domain_id, project_id, environment_id, service_id
        )
        ON DELETE CASCADE
);

CREATE INDEX idx_service_domain_bindings_scope
    ON service_domain_bindings (
        project_id, environment_id, service_id, route_id
    );

-- Upgrade existing generated HTTP domains into the service-owned product
-- model. The generated-domain singleton plus the historical
-- (domain_id,path_prefix) uniqueness makes this backfill deterministic. Custom,
-- TLS, private, and non-root recovery intent remains deployment-local.
INSERT INTO service_domain_bindings (
    route_id, domain_id, project_id, environment_id, service_id, created_at
)
SELECT r.id,
       d.id,
       d.project_id,
       d.environment_id,
       d.service_id,
       LEAST(d.created_at, r.created_at)
  FROM domains d
  JOIN http_routes r
    ON r.domain_id = d.id
   AND r.project_id = d.project_id
   AND r.environment_id = d.environment_id
   AND r.service_id = d.service_id
 WHERE d.kind = 'generated'
   AND d.tls_desired = FALSE
   AND d.tls_state = 'disabled'
   AND r.path_prefix = '/'
   AND r.public_enabled = TRUE;

-- One canonical deployment can serve every generated hostname attached to the
-- service. Preserve every existing row while widening ownership to route sets.
ALTER TABLE deployment_route_intents
    DROP CONSTRAINT deployment_route_intents_pkey;

ALTER TABLE deployment_route_intents
    ADD CONSTRAINT deployment_route_intents_pkey
    PRIMARY KEY (deployment_id, route_id);

CREATE INDEX idx_deployment_route_intents_scope_deployment
    ON deployment_route_intents (
        project_id, environment_id, service_id, deployment_id
    );

-- Existing open deployments predate the service-owned product model, so carry
-- every eligible managed route into their exact immutable scope. This runs
-- only after the composite primary key exists: a deployment-local route that
-- occupied the legacy one-row key is preserved while the managed route is
-- added alongside it. Terminal deployments remain historical and unchanged.
INSERT INTO deployment_route_intents (
    deployment_id, route_id, domain_id, project_id,
    environment_id, service_id, created_at
)
SELECT dep.id,
       b.route_id,
       b.domain_id,
       b.project_id,
       b.environment_id,
       b.service_id,
       CURRENT_TIMESTAMP
  FROM deployments dep
  JOIN service_domain_bindings b
    ON b.project_id = dep.project_id
   AND b.environment_id = dep.environment_id
   AND b.service_id = dep.service_id
  JOIN http_routes r
    ON r.id = b.route_id
   AND r.domain_id = b.domain_id
   AND r.project_id = b.project_id
   AND r.environment_id = b.environment_id
   AND r.service_id = b.service_id
  JOIN domains d
    ON d.id = b.domain_id
   AND d.project_id = b.project_id
   AND d.environment_id = b.environment_id
   AND d.service_id = b.service_id
 WHERE dep.state IN ('queued', 'building', 'deploying', 'active')
   AND d.kind = 'generated'
   AND d.tls_desired = FALSE
   AND d.tls_state = 'disabled'
   AND r.public_enabled = TRUE
ON CONFLICT (deployment_id, route_id) DO NOTHING;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (52, 'multi_route_domain_resources', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
