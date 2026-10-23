-- 0035_stateless_deployment_instances.up.sql
-- Durable per-replica intent/evidence for stateless Docker deployments.
CREATE TABLE deployment_instances (
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    instance_id TEXT NOT NULL,
    replica_ordinal INTEGER NOT NULL CHECK (replica_ordinal >= 0),
    desired_state TEXT NOT NULL DEFAULT 'running' CHECK (desired_state IN ('running','stopped','removed')),
    observed_state TEXT NOT NULL DEFAULT 'pending',
    container_id TEXT NOT NULL DEFAULT '',
    container_ip TEXT NOT NULL DEFAULT '',
    ready BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (deployment_id, instance_id),
    UNIQUE (deployment_id, replica_ordinal)
);
CREATE INDEX deployment_instances_deployment_ready_idx ON deployment_instances(deployment_id, ready, replica_ordinal);
INSERT INTO schema_migrations(version,name,applied_at) VALUES (35,'stateless_deployment_instances',CURRENT_TIMESTAMP) ON CONFLICT (version) DO NOTHING;
