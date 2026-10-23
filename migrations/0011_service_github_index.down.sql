-- 0010_service_github_index.down.sql - drop service.spec.github indices.

DROP INDEX IF EXISTS idx_services_github_jsonb;
DROP INDEX IF EXISTS idx_services_github_repo_branch;

DELETE FROM schema_migrations WHERE version = 10;
