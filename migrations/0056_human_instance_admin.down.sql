-- Do not silently revoke a human administrator during rollback. The is_admin
-- column predates this migration and may have been changed deliberately after
-- upgrade; its provenance cannot be inferred safely.
DELETE FROM schema_migrations
WHERE version = 56 AND name = 'human_instance_admin';
