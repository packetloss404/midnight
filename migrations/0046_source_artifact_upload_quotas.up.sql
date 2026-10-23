-- 0046_source_artifact_upload_quotas.up.sql
--
-- Durable, replica-safe admission reservations for human source uploads.
-- Reservations account quota before the request body is read. A short lease
-- bounds concurrent staging while a longer charge survives a process crash or
-- failed PostgreSQL admission after the immutable filesystem blob is
-- published.

ALTER TABLE auth_sessions
    ADD CONSTRAINT auth_sessions_id_user_uniq
    UNIQUE (id, user_id);

CREATE TABLE source_artifact_workspace_quota_state (
    workspace_id TEXT PRIMARY KEY
        REFERENCES workspaces(id) ON DELETE CASCADE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- This ledger is independent of project/service/session/operation lifecycle.
-- A cascade may remove product lineage, but it cannot make still-present CAS
-- bytes disappear from workspace quota accounting.
CREATE TABLE source_artifact_workspace_charges (
    workspace_id TEXT NOT NULL
        REFERENCES workspaces(id) ON DELETE RESTRICT,
    artifact_digest TEXT NOT NULL,
    compressed_bytes BIGINT NOT NULL,
    reconcile_after TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, artifact_digest),
    CONSTRAINT source_artifact_workspace_charges_digest_chk
        CHECK (artifact_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT source_artifact_workspace_charges_size_chk
        CHECK (compressed_bytes > 0),
    CONSTRAINT source_artifact_workspace_charges_reconcile_chk
        CHECK (reconcile_after >= created_at)
);

CREATE INDEX source_artifact_workspace_charges_digest_idx
    ON source_artifact_workspace_charges (artifact_digest, workspace_id);

CREATE INDEX source_artifact_workspace_charges_reconcile_idx
    ON source_artifact_workspace_charges (reconcile_after, workspace_id, artifact_digest);

-- Migration 0045 references predate the ledger. Backfill every retained or
-- released reference before quota enforcement can claim coverage.
INSERT INTO source_artifact_workspace_charges (
    workspace_id,
    artifact_digest,
    compressed_bytes,
    reconcile_after,
    created_at,
    updated_at
)
SELECT project.workspace_id,
       artifact_ref.artifact_digest,
       MAX(artifact.compressed_bytes),
       CURRENT_TIMESTAMP,
       MIN(artifact_ref.created_at),
       CURRENT_TIMESTAMP
  FROM source_artifact_refs AS artifact_ref
  JOIN source_artifacts AS artifact
    ON artifact.digest=artifact_ref.artifact_digest
  JOIN projects AS project
    ON project.id=artifact_ref.project_id
 GROUP BY project.workspace_id,artifact_ref.artifact_digest
ON CONFLICT (workspace_id, artifact_digest) DO NOTHING;

CREATE TABLE source_artifact_upload_reservations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    intent_hash TEXT NOT NULL,
    expected_digest TEXT NOT NULL,
    compressed_bytes BIGINT NOT NULL,
    lease_expires_at TIMESTAMPTZ NOT NULL,
    charge_expires_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT source_artifact_upload_reservations_project_scope_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects(id, workspace_id) ON DELETE RESTRICT,
    CONSTRAINT source_artifact_upload_reservations_service_scope_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT source_artifact_upload_reservations_environment_scope_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments(id, project_id) ON DELETE RESTRICT,
    CONSTRAINT source_artifact_upload_reservations_session_actor_fk
        FOREIGN KEY (session_id, actor_user_id)
        REFERENCES auth_sessions(id, user_id) ON DELETE RESTRICT,
    CONSTRAINT source_artifact_upload_reservations_id_chk
        CHECK (id ~ '^suar_[0-9a-f]{32}$'),
    CONSTRAINT source_artifact_upload_reservations_digest_chk
        CHECK (expected_digest ~ '^sha256:[0-9a-f]{64}$'),
    CONSTRAINT source_artifact_upload_reservations_intent_hash_chk
        CHECK (intent_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT source_artifact_upload_reservations_key_chk
        CHECK (
            octet_length(idempotency_key) BETWEEN 1 AND 255
            AND idempotency_key !~ '[[:space:][:cntrl:]]'
        ),
    CONSTRAINT source_artifact_upload_reservations_size_chk
        CHECK (compressed_bytes > 0),
    CONSTRAINT source_artifact_upload_reservations_deadline_chk
        CHECK (
            lease_expires_at > created_at
            AND charge_expires_at >= lease_expires_at
            AND (published_at IS NULL OR published_at >= created_at)
        ),
    CONSTRAINT source_artifact_upload_reservations_scope_uniq
        UNIQUE (actor_user_id, project_id, service_id, idempotency_key)
);

CREATE INDEX source_artifact_upload_reservations_workspace_charge_idx
    ON source_artifact_upload_reservations (
        workspace_id,
        charge_expires_at,
        expected_digest
    );

CREATE INDEX source_artifact_upload_reservations_workspace_lease_idx
    ON source_artifact_upload_reservations (
        workspace_id,
        lease_expires_at
    );

CREATE INDEX source_artifact_upload_reservations_lease_idx
    ON source_artifact_upload_reservations (lease_expires_at, id);

CREATE INDEX source_artifact_upload_reservations_charge_idx
    ON source_artifact_upload_reservations (charge_expires_at, id);

CREATE INDEX source_artifact_refs_project_digest_idx
    ON source_artifact_refs (project_id, artifact_digest);

CREATE INDEX source_artifact_refs_digest_idx
    ON source_artifact_refs (artifact_digest);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (46, 'source_artifact_upload_quotas', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
