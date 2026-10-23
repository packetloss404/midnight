-- 0021 — make volume mount intent durable and attachment ownership structural.
--
-- Existing installations may have volumes created before mount_path lived on
-- the volume row. Recover the newest known attachment path when one exists;
-- otherwise retain an empty path so the worker fails closed instead of
-- inventing a container path.

ALTER TABLE volumes
    ADD COLUMN IF NOT EXISTS mount_path TEXT NOT NULL DEFAULT '';

UPDATE volumes v
SET mount_path = (
        SELECT va.mount_path
        FROM volume_attachments va
        WHERE va.volume_id = v.id
          AND va.mount_path <> ''
        ORDER BY va.sequence DESC, va.attached_at DESC, va.id DESC
        LIMIT 1
    ),
    updated_at = CURRENT_TIMESTAMP,
    resource_version = resource_version + 1
WHERE v.mount_path = ''
  AND EXISTS (
      SELECT 1
      FROM volume_attachments va
      WHERE va.volume_id = v.id
        AND va.mount_path <> ''
  );

ALTER TABLE volumes
    DROP CONSTRAINT IF EXISTS volumes_mount_path_chk;

ALTER TABLE volumes
    ADD CONSTRAINT volumes_mount_path_chk
    CHECK (
        mount_path = ''
        OR (
            mount_path <> '/'
            AND mount_path ~ '^/[^[:cntrl:]]*$'
        )
    );

ALTER TABLE volume_attachments
    ADD COLUMN IF NOT EXISTS project_id TEXT NOT NULL DEFAULT '';

UPDATE volume_attachments va
SET project_id = v.project_id
FROM volumes v
WHERE v.id = va.volume_id
  AND va.project_id = '';

ALTER TABLE volume_attachments
    DROP CONSTRAINT IF EXISTS volume_attachments_service_project_fk;
ALTER TABLE volume_attachments
    ADD CONSTRAINT volume_attachments_service_project_fk
    FOREIGN KEY (service_id, project_id)
    REFERENCES services (id, project_id)
    ON DELETE CASCADE NOT VALID;

ALTER TABLE volume_attachments
    DROP CONSTRAINT IF EXISTS volume_attachments_environment_project_fk;
ALTER TABLE volume_attachments
    ADD CONSTRAINT volume_attachments_environment_project_fk
    FOREIGN KEY (environment_id, project_id)
    REFERENCES environments (id, project_id)
    ON DELETE CASCADE NOT VALID;

-- A Docker local volume is ReadWriteOnce. PostgreSQL owns the lease: no two
-- pending/active attachment rows may claim the same durable volume.
CREATE UNIQUE INDEX IF NOT EXISTS volume_attachments_single_writer_uq
    ON volume_attachments (volume_id)
    WHERE status IN ('pending', 'active');

CREATE INDEX IF NOT EXISTS idx_volume_attachments_instance
    ON volume_attachments (deployment_id, instance_id, status);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (21, 'volume_runtime_lifecycle', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
