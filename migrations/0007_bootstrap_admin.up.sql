-- 0007_bootstrap_admin.up.sql - admin first-login MFA support.
--
-- Introduces the `users` table that the bootstrap-admin flow reads and writes
-- through. The baseline shipped only `api_tokens`; this migration adds
-- `users`, `workspaces`, `workspace_members`, `service_accounts`, and the
-- bootstrap-admin first-login MFA columns.
--
-- Schema bumps to schema_version = 7 (manifest.yaml).

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    -- PBKDF2-SHA256 PHC-style hash. NULL until the operator sets a
    -- password through the bootstrap flow.
    password_hash TEXT,
    -- Envelope-encrypted TOTP secret produced by internal/secrets
    -- (SealForVariable). NULL until the operator runs enroll-totp.
    totp_secret_encrypted BYTEA,
    totp_enrolled_at TIMESTAMPTZ,
    must_rotate_password BOOLEAN NOT NULL DEFAULT FALSE,
    -- Hex SHA-256 of the one-shot bootstrap token. Set by the
    -- installer's postinst when the token file is written; cleared
    -- by ConfirmTOTP on success. The raw token is NEVER stored.
    bootstrap_token_hash TEXT,
    is_admin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,
    resource_version BIGINT NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX IF NOT EXISTS users_email_uniq
    ON users (LOWER(email))
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS users_admin_idx
    ON users (is_admin)
    WHERE is_admin = TRUE AND deleted_at IS NULL;

-- Workspaces + workspace_members are required by the control-plane identity
-- model; bootstrap-admin work needs them only as foreign-key targets, but
-- landing them here keeps the identity schema in one migration.
CREATE TABLE IF NOT EXISTS workspaces (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,
    resource_version BIGINT NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS workspace_members (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'developer',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, user_id),
    CONSTRAINT workspace_members_role_chk
        CHECK (role IN ('owner','project_admin','developer','deployer','viewer'))
);

-- service_accounts mirrors the scoped-token surface so the resource graph can
-- carry a stable foreign-key target for audit_logs.actor_type='service_account'.
CREATE TABLE IF NOT EXISTS service_accounts (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMPTZ,
    UNIQUE (workspace_id, slug)
);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (7, 'bootstrap_admin', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
