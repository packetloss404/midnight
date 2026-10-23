-- 0064 — signed, expiring, one-time GitHub App installation setup state.
--
-- Only the SHA-256 digest of the random nonce is persisted. OAuth codes,
-- GitHub user tokens, App credentials, and the signed browser state are never
-- stored. A state is bound to the exact initiating Midnight workspace, user,
-- and currently valid human session.
CREATE TABLE github_app_setup_states (
    nonce_sha256 BYTEA PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    installation_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT github_app_setup_states_nonce_chk
        CHECK (OCTET_LENGTH(nonce_sha256) = 32),
    CONSTRAINT github_app_setup_states_workspace_fk
        FOREIGN KEY (workspace_id)
        REFERENCES workspaces (id)
        ON DELETE CASCADE,
    CONSTRAINT github_app_setup_states_user_fk
        FOREIGN KEY (user_id)
        REFERENCES users (id)
        ON DELETE CASCADE,
    CONSTRAINT github_app_setup_states_session_fk
        FOREIGN KEY (session_id)
        REFERENCES auth_sessions (id)
        ON DELETE CASCADE,
    CONSTRAINT github_app_setup_states_lifetime_chk
        CHECK (
            issued_at <= created_at
            AND expires_at > issued_at
            AND expires_at <= issued_at + INTERVAL '1 hour'
        ),
    CONSTRAINT github_app_setup_states_consumption_chk
        CHECK (
            (consumed_at IS NULL AND installation_id IS NULL)
            OR (
                consumed_at IS NOT NULL
                AND consumed_at >= issued_at
                AND installation_id > 0
            )
        )
);

CREATE INDEX github_app_setup_states_expiry_idx
    ON github_app_setup_states (expires_at, issued_at)
    WHERE consumed_at IS NULL;

CREATE INDEX github_app_setup_states_actor_idx
    ON github_app_setup_states (workspace_id, user_id, session_id, issued_at DESC);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (64, 'github_app_setup_states', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
