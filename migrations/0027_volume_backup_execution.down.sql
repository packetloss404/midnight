DROP TABLE IF EXISTS volume_restore_runs;
DROP TABLE IF EXISTS volume_backup_runs;
DROP TABLE IF EXISTS volume_backup_policies;

DELETE FROM volumes WHERE status = 'restore_staged';
UPDATE volumes SET status = 'ready' WHERE status = 'restore_retained';
ALTER TABLE volumes DROP CONSTRAINT IF EXISTS volumes_status_chk;
ALTER TABLE volumes
    ADD CONSTRAINT volumes_status_chk
    CHECK (status IN ('provisioning','ready','attached','soft_deleted','deleted'));

DROP INDEX IF EXISTS volumes_id_scope_backup_uq;

DELETE FROM schema_migrations WHERE version = 27;
