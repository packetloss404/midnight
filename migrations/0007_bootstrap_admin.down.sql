-- 0007_bootstrap_admin.down.sql — drop bootstrap-admin tables. Down
-- migrations remove tables in reverse-FK order. workspaces is
-- referenced by workspace_members and service_accounts.

DROP TABLE IF EXISTS service_accounts;
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;
DROP INDEX IF EXISTS users_admin_idx;
DROP INDEX IF EXISTS users_email_uniq;
DROP TABLE IF EXISTS users;

DELETE FROM schema_migrations WHERE version = 7;
