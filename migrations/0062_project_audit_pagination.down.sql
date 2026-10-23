DROP INDEX IF EXISTS audit_logs_project_id_desc_idx;

DELETE FROM schema_migrations
WHERE version = 62 AND name = 'project_audit_pagination';
