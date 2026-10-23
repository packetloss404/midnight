-- 0036 — durable official-template provisioning state and smoke evidence.
--
-- The template intake transaction creates this row together with its service,
-- environment config, encrypted variables, volume, canonical deployment,
-- operation, and deploy-intake outbox intent. Workers may only mark a run
-- succeeded after the database-specific connectivity and restart-persistence
-- probes pass. Evidence is deliberately metadata-only.

CREATE TABLE IF NOT EXISTS template_provision_runs (
    id TEXT PRIMARY KEY,
    template_slug TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL UNIQUE REFERENCES services(id) ON DELETE CASCADE,
    volume_id TEXT NOT NULL UNIQUE REFERENCES volumes(id) ON DELETE RESTRICT,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL UNIQUE REFERENCES deployments(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'queued',
    connectivity_verified BOOLEAN NOT NULL DEFAULT FALSE,
    persistence_verified BOOLEAN NOT NULL DEFAULT FALSE,
    write_probe_container_id TEXT NOT NULL DEFAULT '',
    read_probe_container_id TEXT NOT NULL DEFAULT '',
    marker_sha256 TEXT NOT NULL DEFAULT '',
    cleanup_completed BOOLEAN NOT NULL DEFAULT FALSE,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT template_provision_runs_state_chk
        CHECK (state IN ('queued','running','succeeded','failed','canceled')),
    CONSTRAINT template_provision_runs_template_chk
        CHECK (template_slug = 'postgres'),
    CONSTRAINT template_provision_runs_marker_chk
        CHECK (marker_sha256 = '' OR marker_sha256 ~ '^[a-f0-9]{64}$'),
    CONSTRAINT template_provision_runs_evidence_chk CHECK (
        state <> 'succeeded'
        OR (
            connectivity_verified
            AND persistence_verified
            AND write_probe_container_id <> ''
            AND read_probe_container_id <> ''
            AND marker_sha256 <> ''
            AND cleanup_completed
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_template_provision_runs_scope
    ON template_provision_runs (project_id, environment_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_template_provision_runs_state
    ON template_provision_runs (state, updated_at);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (36, 'template_provision_runs', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
