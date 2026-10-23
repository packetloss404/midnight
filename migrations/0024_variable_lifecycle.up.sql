-- 0024 — optimistic variable lifecycle and metadata-only tombstones.
--
-- Active names remain unique inside their exact ownership scope. Deleted
-- variables retain audit-safe identity/version metadata and their active-row
-- ciphertext/key material is erased before deleted_at is set. Immutable
-- encrypted versions remain in service_variable_versions so an already-bound
-- deployment snapshot can be retried without falling forward to a newer value.

ALTER TABLE service_variables
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE service_variables
    DROP CONSTRAINT IF EXISTS service_variables_project_id_environment_id_service_id_name_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_service_variables_active_scope_name
    ON service_variables (project_id, environment_id, service_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_service_variables_deleted_at
    ON service_variables (deleted_at)
    WHERE deleted_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS service_variable_versions (
    variable_id TEXT NOT NULL REFERENCES service_variables(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version > 0),
    workspace_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    scope TEXT NOT NULL,
    value_kind TEXT NOT NULL CHECK (value_kind IN ('literal','reference')),
    value_ciphertext BYTEA NOT NULL,
    kek_id TEXT NOT NULL,
    wrapped_dek BYTEA NOT NULL,
    canonical_form TEXT NOT NULL,
    "references" JSONB NOT NULL DEFAULT '[]'::jsonb,
    length_bucket TEXT NOT NULL,
    kek_fingerprint TEXT NOT NULL DEFAULT '',
    last_rotated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (variable_id, version)
);

INSERT INTO service_variable_versions (
    variable_id, version, workspace_id, project_id, environment_id, service_id,
    name, scope, value_kind, value_ciphertext, kek_id, wrapped_dek,
    canonical_form, "references", length_bucket, kek_fingerprint,
    last_rotated_at, created_at
)
SELECT
    id, version, workspace_id, project_id, environment_id, service_id,
    name, scope, value_kind, value_ciphertext, kek_id, wrapped_dek,
    canonical_form, "references", length_bucket, kek_fingerprint,
    last_rotated_at, updated_at
FROM service_variables
WHERE deleted_at IS NULL
ON CONFLICT (variable_id, version) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_service_variable_versions_scope
    ON service_variable_versions (
        project_id, environment_id, service_id, name, variable_id, version
    );

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (24, 'variable_lifecycle', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
