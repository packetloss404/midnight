-- 0048 — bind a human deployment to its generated public-route intent.
--
-- A service/environment route is durable independently of any deployment, but
-- product reads must not infer that every deployment in that scope requested
-- public traffic. This row is committed in the same SERIALIZABLE transaction
-- as deployment admission, outbox work, idempotency, and audit evidence.

CREATE TABLE deployment_route_intents (
    deployment_id TEXT PRIMARY KEY,
    route_id TEXT NOT NULL,
    domain_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT deployment_route_intents_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, environment_id, service_id)
        REFERENCES deployments (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT deployment_route_intents_route_scope_fk
        FOREIGN KEY (route_id, project_id, environment_id, service_id)
        REFERENCES http_routes (id, project_id, environment_id, service_id)
        ON DELETE CASCADE,
    CONSTRAINT deployment_route_intents_domain_scope_fk
        FOREIGN KEY (domain_id, project_id, environment_id, service_id)
        REFERENCES domains (id, project_id, environment_id, service_id)
        ON DELETE CASCADE
);

CREATE INDEX idx_deployment_route_intents_route
    ON deployment_route_intents (route_id, deployment_id);

CREATE INDEX idx_route_backends_deployment_route
    ON route_backends (deployment_id, route_id);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (48, 'deployment_route_intents', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
