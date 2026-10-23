DROP INDEX IF EXISTS idx_volume_attachments_instance;
DROP INDEX IF EXISTS volume_attachments_single_writer_uq;

ALTER TABLE volumes
    DROP CONSTRAINT IF EXISTS volumes_mount_path_chk;

ALTER TABLE volumes
    DROP COLUMN IF EXISTS mount_path;

ALTER TABLE volume_attachments
    DROP CONSTRAINT IF EXISTS volume_attachments_environment_project_fk;
ALTER TABLE volume_attachments
    DROP CONSTRAINT IF EXISTS volume_attachments_service_project_fk;
ALTER TABLE volume_attachments
    DROP COLUMN IF EXISTS project_id;

DELETE FROM schema_migrations WHERE version = 21;
