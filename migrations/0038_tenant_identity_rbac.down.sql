-- 0038_tenant_identity_rbac.down.sql
--
-- Refuse lossy rollback once human session or non-bootstrap project access
-- data exists. Operators must export/transfer that identity state explicitly.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM auth_sessions) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac while auth_sessions contains rows';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM project_members
        WHERE user_id <> 'usr_migration_default'
           OR role <> 'owner'
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac while non-bootstrap project memberships exist';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM workspace_members
        WHERE workspace_id = 'wrk_default'
          AND user_id <> 'usr_migration_default'
    ) OR EXISTS (
        SELECT 1
        FROM workspaces
        WHERE id = 'wrk_default'
          AND (
              slug <> 'migration-default'
              OR display_name <> 'Default workspace'
          )
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac after the default workspace has been claimed';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM workspace_members
        WHERE resource_version <> 1
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac after workspace memberships have changed';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM workspace_members
        WHERE legacy_role IS NULL
          AND NOT (
              workspace_id = 'wrk_default'
              AND user_id = 'usr_migration_default'
          )
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac after new workspace memberships were created';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM projects
        WHERE workspace_id <> 'wrk_default'
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac while projects belong to non-default workspaces';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM environments
        WHERE access_restricted = TRUE
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac while restricted environments exist';
    END IF;
    IF EXISTS (
        SELECT slug
        FROM projects
        GROUP BY slug
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot roll back tenant_identity_rbac while project slugs collide across workspaces';
    END IF;
END
$$;

DROP INDEX IF EXISTS auth_login_limits_updated_idx;
DROP TABLE IF EXISTS auth_login_limits;

DROP INDEX IF EXISTS auth_sessions_expiry_idx;
DROP INDEX IF EXISTS auth_sessions_user_active_idx;
DROP TABLE IF EXISTS auth_sessions;

ALTER TABLE environments
    DROP COLUMN IF EXISTS access_restricted;

DROP INDEX IF EXISTS project_members_user_idx;
DROP TABLE IF EXISTS project_members;

DROP INDEX IF EXISTS projects_workspace_idx;
DROP INDEX IF EXISTS projects_workspace_slug_uniq;
DROP INDEX IF EXISTS projects_id_workspace_uniq;

ALTER TABLE projects
    DROP COLUMN IF EXISTS workspace_id;

ALTER TABLE projects
    ADD CONSTRAINT projects_slug_key UNIQUE (slug);

ALTER TABLE services
    ALTER COLUMN project_id DROP NOT NULL;

DELETE FROM workspace_members
WHERE workspace_id = 'wrk_default'
  AND user_id = 'usr_migration_default';

DELETE FROM workspaces
WHERE id = 'wrk_default'
  AND slug = 'migration-default'
  AND display_name = 'Default workspace';

DELETE FROM users
WHERE id = 'usr_migration_default'
  AND email = 'migration-default@midnight.invalid'
  AND is_disabled = TRUE;

ALTER TABLE workspace_members
    DROP CONSTRAINT IF EXISTS workspace_members_role_chk;

UPDATE workspace_members
SET role = COALESCE(
    legacy_role,
    CASE role
        WHEN 'admin' THEN 'project_admin'
        WHEN 'member' THEN 'developer'
        ELSE role
    END
);

ALTER TABLE workspace_members
    ADD CONSTRAINT workspace_members_role_chk
        CHECK (role IN ('owner', 'project_admin', 'developer', 'deployer', 'viewer')),
    DROP COLUMN IF EXISTS invited_by,
    DROP COLUMN IF EXISTS invited_at,
    DROP COLUMN IF EXISTS accepted_at,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS resource_version,
    DROP COLUMN IF EXISTS legacy_role;

DROP TRIGGER IF EXISTS users_invalidate_identity_sessions
ON users;
DROP FUNCTION IF EXISTS midnight_invalidate_identity_sessions();

ALTER TABLE users
    DROP COLUMN IF EXISTS session_generation,
    DROP COLUMN IF EXISTS is_disabled,
    DROP COLUMN IF EXISTS last_login_at;

DELETE FROM schema_migrations
WHERE version = 38 AND name = 'tenant_identity_rbac';
