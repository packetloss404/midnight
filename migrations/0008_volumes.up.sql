-- 0008_volumes.up.sql - volumes + single-writer enforcement.
--
-- Introduces the `volumes` and `volume_attachments` tables. The runtime
-- adapter manages on-host Docker volumes; this schema is the storage-graph
-- counterpart the deploy orchestrator consults under
-- pg_advisory_xact_lock(hashtext('midnight_volume:<svc>:<env>')).
--
-- ULID prefix taxonomy:
--   * vol_  — volumes.id
--   * vat_  — volume_attachments.id
--
-- Schema bumps to schema_version = 8.

CREATE TABLE IF NOT EXISTS volumes (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'provisioning',
    -- HostFSType records what the agent observed at create time so a
    -- future migration knows whether prjquota is enforceable.
    host_fs_type TEXT NOT NULL DEFAULT '',
    -- Linux project-quota project ID; allocated by the agent at
    -- volume create time on xfs/ext4 with quota mounts.
    quota_project_id INTEGER,
    -- 7-day soft-delete grace.
    soft_deleted_at TIMESTAMPTZ,
    hard_delete_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (service_id, environment_id, name),
    CONSTRAINT volumes_status_chk
        CHECK (status IN ('provisioning','ready','attached','soft_deleted','deleted'))
);

CREATE INDEX IF NOT EXISTS idx_volumes_status ON volumes (status);
CREATE INDEX IF NOT EXISTS idx_volumes_service_env ON volumes (service_id, environment_id);
CREATE INDEX IF NOT EXISTS idx_volumes_hard_delete_at ON volumes (hard_delete_at)
    WHERE status = 'soft_deleted';

CREATE TABLE IF NOT EXISTS volume_attachments (
    id TEXT PRIMARY KEY,
    volume_id TEXT NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL,
    instance_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    mount_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    -- monotonic per-(service,environment) sequence used for
    -- supersede arbitration. deployment_sequence flows through here
    -- so the loser side records DEPLOY_SUPERSEDED.
    sequence BIGINT NOT NULL,
    attached_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    detached_at TIMESTAMPTZ,
    UNIQUE (deployment_id, instance_id, volume_id),
    CONSTRAINT volume_attachments_status_chk
        CHECK (status IN ('pending','active','detached'))
);

CREATE INDEX IF NOT EXISTS idx_volume_attachments_volume ON volume_attachments (volume_id);
CREATE INDEX IF NOT EXISTS idx_volume_attachments_status ON volume_attachments (status);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (8, 'volumes', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
