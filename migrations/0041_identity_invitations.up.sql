-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Durable, single-use human membership invitations. Only a domain-separated
-- HMAC-SHA-256 verifier is stored; raw invitation secrets are returned once by
-- the identity service and never enter this table.

CREATE TABLE identity_invitations (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
    project_id TEXT,
    workspace_role TEXT,
    project_role TEXT,
    token_hash BYTEA NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    invited_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
    revoked_reason TEXT NOT NULL DEFAULT '',
    accepted_at TIMESTAMPTZ,
    accepted_by_user_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
    resource_version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT identity_invitations_project_scope_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT identity_invitations_email_chk
        CHECK (
            email = LOWER(BTRIM(email))
            AND CHAR_LENGTH(email) BETWEEN 3 AND 254
            AND POSITION('@' IN email) > 1
            AND email !~ '[[:space:]]'
        ),
    CONSTRAINT identity_invitations_id_chk
        CHECK (id LIKE 'inv\_%' ESCAPE '\'),
    CONSTRAINT identity_invitations_scope_role_chk
        CHECK (
            (
                project_id IS NULL
                AND workspace_role IS NOT NULL
                AND project_role IS NULL
            )
            OR
            (
                project_id IS NOT NULL
                AND workspace_role IS NULL
                AND project_role IS NOT NULL
            )
        ),
    CONSTRAINT identity_invitations_workspace_role_chk
        CHECK (
            workspace_role IS NULL
            OR workspace_role IN ('admin', 'member', 'deployer', 'viewer')
        ),
    CONSTRAINT identity_invitations_project_role_chk
        CHECK (
            project_role IS NULL
            OR project_role IN ('owner', 'editor', 'viewer')
        ),
    CONSTRAINT identity_invitations_hash_chk
        CHECK (OCTET_LENGTH(token_hash) = 32),
    CONSTRAINT identity_invitations_prefix_chk
        CHECK (
            CHAR_LENGTH(token_prefix) = 18
            AND token_prefix LIKE 'midn\_inv\_%' ESCAPE '\'
        ),
    CONSTRAINT identity_invitations_expiry_chk
        CHECK (
            expires_at >= created_at + INTERVAL '5 minutes'
            AND expires_at <= created_at + INTERVAL '30 days'
        ),
    CONSTRAINT identity_invitations_update_time_chk
        CHECK (updated_at >= created_at),
    CONSTRAINT identity_invitations_terminal_state_chk
        CHECK (NOT (revoked_at IS NOT NULL AND accepted_at IS NOT NULL)),
    CONSTRAINT identity_invitations_revocation_audit_chk
        CHECK (
            (
                revoked_at IS NULL
                AND revoked_by_user_id IS NULL
                AND revoked_reason = ''
            )
            OR
            (
                revoked_at IS NOT NULL
                AND revoked_by_user_id IS NOT NULL
                AND BTRIM(revoked_reason) <> ''
                AND CHAR_LENGTH(revoked_reason) <= 128
                AND revoked_at >= created_at
            )
        ),
    CONSTRAINT identity_invitations_acceptance_audit_chk
        CHECK (
            (
                accepted_at IS NULL
                AND accepted_by_user_id IS NULL
            )
            OR
            (
                accepted_at IS NOT NULL
                AND accepted_by_user_id IS NOT NULL
                AND accepted_at >= created_at
            )
        ),
    CONSTRAINT identity_invitations_resource_version_chk
        CHECK (resource_version >= 1)
);

CREATE INDEX identity_invitations_workspace_created_idx
    ON identity_invitations (workspace_id, created_at DESC, id);

CREATE INDEX identity_invitations_project_created_idx
    ON identity_invitations (workspace_id, project_id, created_at DESC, id)
    WHERE project_id IS NOT NULL;

CREATE INDEX identity_invitations_pending_lookup_idx
    ON identity_invitations (workspace_id, project_id, email, expires_at, id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;

CREATE INDEX identity_invitations_pending_expiry_idx
    ON identity_invitations (expires_at, id)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
