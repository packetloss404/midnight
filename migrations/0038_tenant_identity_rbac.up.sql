-- 0038_tenant_identity_rbac.up.sql
--
-- Turns the identity scaffolding from migration 0007 into a durable tenant
-- boundary. Existing single-operator installs are placed in one explicit
-- default workspace; no project is left with an implicit/global owner.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS session_generation BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS is_disabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ;

-- Any credential or account-validity transition invalidates every bearer
-- minted from the previous generation, including changes made by older admin
-- code paths that do not yet call the identity session coordinator.
CREATE OR REPLACE FUNCTION midnight_invalidate_identity_sessions()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.password_hash IS DISTINCT FROM OLD.password_hash
       OR NEW.totp_secret_encrypted IS DISTINCT FROM OLD.totp_secret_encrypted
       OR NEW.totp_enrolled_at IS DISTINCT FROM OLD.totp_enrolled_at
       OR NEW.must_rotate_password IS DISTINCT FROM OLD.must_rotate_password
       OR NEW.is_disabled IS DISTINCT FROM OLD.is_disabled
       OR NEW.deleted_at IS DISTINCT FROM OLD.deleted_at
    THEN
        NEW.session_generation := OLD.session_generation + 1;
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS users_invalidate_identity_sessions
ON users;

CREATE TRIGGER users_invalidate_identity_sessions
BEFORE UPDATE OF
    password_hash,
    totp_secret_encrypted,
    totp_enrolled_at,
    must_rotate_password,
    is_disabled,
    deleted_at
ON users
FOR EACH ROW
EXECUTE FUNCTION midnight_invalidate_identity_sessions();

ALTER TABLE workspace_members
    ADD COLUMN IF NOT EXISTS invited_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS invited_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ADD COLUMN IF NOT EXISTS resource_version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS legacy_role TEXT;

-- A membership that predates the invitation lifecycle was already effective.
-- Preserve that fact rather than converting every upgraded operator into a
-- pending invitation.
UPDATE workspace_members
SET invited_at = created_at,
    accepted_at = created_at,
    updated_at = created_at
WHERE accepted_at IS NULL;

ALTER TABLE workspace_members
    DROP CONSTRAINT IF EXISTS workspace_members_role_chk;

UPDATE workspace_members
SET legacy_role = COALESCE(legacy_role, role),
    role = CASE role
    WHEN 'owner' THEN 'admin'
    WHEN 'project_admin' THEN 'admin'
    WHEN 'developer' THEN 'member'
    ELSE role
END;

ALTER TABLE workspace_members
    ADD CONSTRAINT workspace_members_role_chk
        CHECK (role IN ('admin', 'member', 'deployer', 'viewer'));

-- Fixed bootstrap IDs are reserved and must be absent before this migration.
-- Never reinterpret an existing account or workspace as the migration owner:
-- that would silently grant it every legacy project.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM users
        WHERE id = 'usr_migration_default'
    ) THEN
        RAISE EXCEPTION 'reserved migration user id already exists';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM users
        WHERE LOWER(email) = 'migration-default@midnight.invalid'
          AND id <> 'usr_migration_default'
          AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'reserved migration user email belongs to another account';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM workspaces
        WHERE id = 'wrk_default'
    ) THEN
        RAISE EXCEPTION 'reserved default workspace id already exists';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM workspaces
        WHERE slug = 'migration-default'
          AND id <> 'wrk_default'
          AND deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'reserved default workspace slug belongs to another workspace';
    END IF;
END
$$;

-- This disabled identity is a migration owner for pre-RBAC resources. It
-- cannot authenticate, but keeps every backfilled membership and project
-- foreign-key complete until an instance admin transfers ownership.
INSERT INTO users (
    id, email, display_name, is_admin, is_disabled, must_rotate_password,
    created_at, updated_at
)
VALUES (
    'usr_migration_default',
    'migration-default@midnight.invalid',
    'Migrated instance owner',
    TRUE,
    TRUE,
    FALSE,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
);

INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
VALUES (
    'wrk_default',
    'migration-default',
    'Default workspace',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
);

INSERT INTO workspace_members (
    workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
)
VALUES (
    'wrk_default',
    'usr_migration_default',
    'admin',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
);

-- Tenant ownership cannot be layered over ambiguous legacy project
-- relationships. Migration 0018 retained those rows for operator repair; this
-- gate deliberately stops until every unresolved row is cleared.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM resource_scope_quarantine
        WHERE reason = 'cross_project_relationship'
          AND resolved_at IS NULL
    ) THEN
        RAISE EXCEPTION 'tenant identity migration requires every cross-project quarantine row to be resolved';
    END IF;
    IF EXISTS (
        SELECT 1
        FROM services
        WHERE project_id IS NULL
    ) THEN
        RAISE EXCEPTION 'tenant identity migration cannot assign parentless services';
    END IF;
END
$$;

ALTER TABLE deployments
    VALIDATE CONSTRAINT deployments_service_project_fk;
ALTER TABLE deployments
    VALIDATE CONSTRAINT deployments_environment_project_fk;
ALTER TABLE volumes
    VALIDATE CONSTRAINT volumes_service_project_fk;
ALTER TABLE volumes
    VALIDATE CONSTRAINT volumes_environment_project_fk;
ALTER TABLE service_variables
    VALIDATE CONSTRAINT service_variables_environment_project_fk;
ALTER TABLE artifact_promotions
    VALIDATE CONSTRAINT artifact_promotions_service_project_fk;
ALTER TABLE artifact_promotions
    VALIDATE CONSTRAINT artifact_promotions_dest_environment_project_fk;
ALTER TABLE artifact_promotions
    VALIDATE CONSTRAINT artifact_promotions_source_environment_project_fk;

ALTER TABLE services
    ALTER COLUMN project_id SET NOT NULL;

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS workspace_id TEXT
        REFERENCES workspaces(id) ON DELETE RESTRICT;

UPDATE projects
SET workspace_id = 'wrk_default'
WHERE workspace_id IS NULL OR workspace_id = '';

ALTER TABLE projects
    ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE projects
    DROP CONSTRAINT IF EXISTS projects_slug_key;

CREATE UNIQUE INDEX IF NOT EXISTS projects_id_workspace_uniq
    ON projects (id, workspace_id);

CREATE UNIQUE INDEX IF NOT EXISTS projects_workspace_slug_uniq
    ON projects (workspace_id, slug)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS projects_workspace_idx
    ON projects (workspace_id, created_at, id)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS project_members (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL,
    invited_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    invited_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accepted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (project_id, user_id),
    CONSTRAINT project_members_role_chk
        CHECK (role IN ('owner', 'editor', 'viewer'))
);

CREATE INDEX IF NOT EXISTS project_members_user_idx
    ON project_members (user_id, project_id)
    WHERE accepted_at IS NOT NULL;

INSERT INTO project_members (
    project_id, user_id, role, invited_at, accepted_at, created_at, updated_at
)
SELECT
    id,
    'usr_migration_default',
    'owner',
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP,
    CURRENT_TIMESTAMP
FROM projects;

-- Access restriction is distinct from is_protected: protection prevents
-- destructive lifecycle operations, while restriction hides environment
-- resources from non-admin human sessions.
ALTER TABLE environments
    ADD COLUMN IF NOT EXISTS access_restricted BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS auth_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    client_kind TEXT NOT NULL DEFAULT 'dashboard',
    session_generation BIGINT NOT NULL,
    user_agent_hash TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,
    idle_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_reason TEXT NOT NULL DEFAULT '',
    CONSTRAINT auth_sessions_client_kind_chk
        CHECK (client_kind IN ('dashboard', 'cli', 'api')),
    CONSTRAINT auth_sessions_expiry_chk
        CHECK (idle_expires_at <= expires_at)
);

CREATE INDEX IF NOT EXISTS auth_sessions_user_active_idx
    ON auth_sessions (user_id, created_at DESC)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS auth_sessions_expiry_idx
    ON auth_sessions (expires_at, idle_expires_at)
    WHERE revoked_at IS NULL;

-- HMAC-pseudonymized account/network buckets bound public password
-- verification work across replicas without persisting email or IP values.
CREATE TABLE IF NOT EXISTS auth_login_limits (
    bucket_hash BYTEA PRIMARY KEY,
    window_started_at TIMESTAMPTZ NOT NULL,
    attempt_count INTEGER NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT auth_login_limits_count_chk CHECK (attempt_count > 0)
);

CREATE INDEX IF NOT EXISTS auth_login_limits_updated_idx
    ON auth_login_limits (updated_at);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (38, 'tenant_identity_rbac', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
