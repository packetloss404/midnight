-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Durable verifier storage for project-scoped automation credentials. This
-- migration deliberately does not modify the legacy, installation-scoped
-- api_tokens table. No product authenticator may consult that legacy table.

ALTER TABLE service_accounts
    ADD CONSTRAINT service_accounts_id_workspace_uniq
        UNIQUE (id, workspace_id);

CREATE TABLE project_automation_tokens (
    id TEXT PRIMARY KEY,
    service_account_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash BYTEA NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    profile TEXT NOT NULL,
    created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    revoked_reason TEXT NOT NULL DEFAULT '',
    resource_version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT project_automation_tokens_service_account_scope_fk
        FOREIGN KEY (service_account_id, workspace_id)
        REFERENCES service_accounts (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT project_automation_tokens_project_scope_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT project_automation_tokens_name_chk
        CHECK (BTRIM(name) <> '' AND CHAR_LENGTH(name) <= 128),
    CONSTRAINT project_automation_tokens_hash_chk
        CHECK (OCTET_LENGTH(token_hash) = 32),
    CONSTRAINT project_automation_tokens_prefix_chk
        CHECK (
            CHAR_LENGTH(token_prefix) = 18
            AND token_prefix LIKE 'midn\_sa\_%' ESCAPE '\'
        ),
    CONSTRAINT project_automation_tokens_profile_chk
        CHECK (profile = 'ci-deploy-v1'),
    CONSTRAINT project_automation_tokens_expiry_chk
        CHECK (expires_at > created_at),
    CONSTRAINT project_automation_tokens_last_used_chk
        CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CONSTRAINT project_automation_tokens_revocation_chk
        CHECK (
            (
                revoked_at IS NULL
                AND revoked_by_user_id IS NULL
                AND revoked_reason = ''
            )
            OR (
                revoked_at IS NOT NULL
                AND BTRIM(revoked_reason) <> ''
                AND CHAR_LENGTH(revoked_reason) <= 128
            )
        ),
    CONSTRAINT project_automation_tokens_resource_version_chk
        CHECK (resource_version >= 1)
);

CREATE INDEX project_automation_tokens_project_created_idx
    ON project_automation_tokens (workspace_id, project_id, created_at DESC, id);

CREATE INDEX project_automation_tokens_active_expiry_idx
    ON project_automation_tokens (expires_at, id)
    WHERE revoked_at IS NULL;
