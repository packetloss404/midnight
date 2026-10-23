-- 0008_volumes.down.sql — drop volume + attachment tables.

DROP INDEX IF EXISTS idx_volume_attachments_status;
DROP INDEX IF EXISTS idx_volume_attachments_volume;
DROP TABLE IF EXISTS volume_attachments;

DROP INDEX IF EXISTS idx_volumes_hard_delete_at;
DROP INDEX IF EXISTS idx_volumes_service_env;
DROP INDEX IF EXISTS idx_volumes_status;
DROP TABLE IF EXISTS volumes;

DELETE FROM schema_migrations WHERE version = 8;
