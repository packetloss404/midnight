-- 0027 — durable evidence for real encrypted volume backup and staged restore.
--
-- Passphrases and plaintext never enter PostgreSQL. A succeeded backup row is
-- valid only when both source and encrypted-object SHA-256 evidence is present.
-- Restore rows always target a distinct replacement volume and retain the
-- source volume through the staged attachment-swap boundary.

CREATE UNIQUE INDEX IF NOT EXISTS volumes_id_scope_backup_uq
    ON volumes (id, project_id, environment_id, service_id);

ALTER TABLE volumes DROP CONSTRAINT IF EXISTS volumes_status_chk;
ALTER TABLE volumes
    ADD CONSTRAINT volumes_status_chk
    CHECK (status IN (
        'provisioning','ready','attached','restore_staged',
        'restore_retained','soft_deleted','deleted'
    ));

CREATE TABLE IF NOT EXISTS volume_backup_policies (
    id TEXT PRIMARY KEY,
    volume_id TEXT NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    schedule_kind TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    retention_seconds BIGINT NOT NULL,
    next_run_at TIMESTAMPTZ NOT NULL,
    last_run_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (volume_id, schedule_kind),
    CONSTRAINT volume_backup_policy_schedule_chk
        CHECK (schedule_kind IN ('daily','weekly','monthly')),
    CONSTRAINT volume_backup_policy_retention_chk
        CHECK (retention_seconds > 0),
    CONSTRAINT volume_backup_policy_scope_fk
        FOREIGN KEY (volume_id, project_id, environment_id, service_id)
        REFERENCES volumes (id, project_id, environment_id, service_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS volume_backup_policies_due_idx
    ON volume_backup_policies (next_run_at, id)
    WHERE enabled;

CREATE TABLE IF NOT EXISTS volume_backup_runs (
    id TEXT PRIMARY KEY,
    volume_id TEXT NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    policy_id TEXT REFERENCES volume_backup_policies(id) ON DELETE SET NULL,
    trigger_kind TEXT NOT NULL DEFAULT 'manual',
    state TEXT NOT NULL DEFAULT 'pending',
    object_key TEXT NOT NULL UNIQUE,
    object_sha256 TEXT NOT NULL DEFAULT '',
    source_sha256 TEXT NOT NULL DEFAULT '',
    object_bytes BIGINT NOT NULL DEFAULT 0,
    source_bytes BIGINT NOT NULL DEFAULT 0,
    archive_format TEXT NOT NULL DEFAULT '',
    locked BOOLEAN NOT NULL DEFAULT FALSE,
    retention_until TIMESTAMPTZ,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (id, volume_id, project_id, environment_id),
    CONSTRAINT volume_backup_runs_trigger_chk
        CHECK (trigger_kind IN ('manual','daily','weekly','monthly')),
    CONSTRAINT volume_backup_runs_state_chk
        CHECK (state IN ('pending','running','succeeded','failed','deleted')),
    CONSTRAINT volume_backup_runs_bytes_chk
        CHECK (object_bytes >= 0 AND source_bytes >= 0),
    CONSTRAINT volume_backup_runs_sha_chk
        CHECK (
            (state <> 'succeeded')
            OR (
                object_sha256 ~ '^[0-9a-f]{64}$'
                AND source_sha256 ~ '^[0-9a-f]{64}$'
                AND object_bytes > 0
                AND source_bytes >= 0
                AND archive_format <> ''
                AND completed_at IS NOT NULL
            )
        ),
    CONSTRAINT volume_backup_runs_scope_fk
        FOREIGN KEY (volume_id, project_id, environment_id, service_id)
        REFERENCES volumes (id, project_id, environment_id, service_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS volume_backup_runs_volume_idx
    ON volume_backup_runs (volume_id, created_at DESC, id);
CREATE INDEX IF NOT EXISTS volume_backup_runs_retention_idx
    ON volume_backup_runs (retention_until, id)
    WHERE state = 'succeeded' AND NOT locked AND retention_until IS NOT NULL;

CREATE TABLE IF NOT EXISTS volume_restore_runs (
    id TEXT PRIMARY KEY,
    backup_id TEXT NOT NULL,
    source_volume_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    replacement_volume_id TEXT,
    replacement_volume_name TEXT NOT NULL DEFAULT '',
    source_retained BOOLEAN NOT NULL DEFAULT TRUE,
    attachment_swap_staged BOOLEAN NOT NULL DEFAULT FALSE,
    restored_sha256 TEXT NOT NULL DEFAULT '',
    restored_bytes BIGINT NOT NULL DEFAULT 0,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_detail TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    UNIQUE (backup_id, id),
    CONSTRAINT volume_restore_runs_state_chk
        CHECK (state IN ('pending','running','verified','staged','failed','applied')),
    CONSTRAINT volume_restore_runs_bytes_chk
        CHECK (restored_bytes >= 0),
    CONSTRAINT volume_restore_runs_source_retained_chk
        CHECK (source_retained),
    CONSTRAINT volume_restore_runs_staged_chk
        CHECK (
            state NOT IN ('verified','staged','applied')
            OR (
                replacement_volume_name <> ''
                AND replacement_volume_id IS NOT NULL
                AND restored_sha256 ~ '^[0-9a-f]{64}$'
                AND completed_at IS NOT NULL
            )
        ),
    CONSTRAINT volume_restore_runs_backup_scope_fk
        FOREIGN KEY (backup_id, source_volume_id, project_id, environment_id)
        REFERENCES volume_backup_runs (id, volume_id, project_id, environment_id)
        ON DELETE RESTRICT,
    CONSTRAINT volume_restore_runs_service_scope_fk
        FOREIGN KEY (source_volume_id, project_id, environment_id, service_id)
        REFERENCES volumes (id, project_id, environment_id, service_id)
        ON DELETE RESTRICT,
    CONSTRAINT volume_restore_runs_replacement_scope_fk
        FOREIGN KEY (replacement_volume_id, project_id, environment_id, service_id)
        REFERENCES volumes (id, project_id, environment_id, service_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS volume_restore_runs_backup_idx
    ON volume_restore_runs (backup_id, created_at DESC, id);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (27, 'volume_backup_execution', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
