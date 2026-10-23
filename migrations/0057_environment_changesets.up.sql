-- 0057 — durable, tenant-scoped environment changesets.
--
-- A changeset is the canonical review boundary for configuration mutations.
-- Secret-bearing variable values are envelope-encrypted before they enter
-- these tables. Applied/discarded rows keep only redaction-safe review and
-- audit metadata; their transient ciphertext is erased in the same commit
-- that changes state.

CREATE TABLE environment_changesets (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    created_by TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    state TEXT NOT NULL DEFAULT 'draft'
        CHECK (state IN ('draft', 'applied', 'discarded')),
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    applied_at TIMESTAMPTZ,
    discarded_at TIMESTAMPTZ,
    CONSTRAINT environment_changesets_project_workspace_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE CASCADE,
    CONSTRAINT environment_changesets_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT environment_changesets_creator_fk
        FOREIGN KEY (created_by)
        REFERENCES users (id)
        ON DELETE RESTRICT,
    CONSTRAINT environment_changesets_state_timestamp_check CHECK (
        (state = 'draft' AND applied_at IS NULL AND discarded_at IS NULL) OR
        (state = 'applied' AND applied_at IS NOT NULL AND discarded_at IS NULL) OR
        (state = 'discarded' AND applied_at IS NULL AND discarded_at IS NOT NULL)
    ),
    UNIQUE (id, project_id, environment_id)
);

CREATE UNIQUE INDEX environment_changesets_one_draft_per_environment
    ON environment_changesets (environment_id)
    WHERE state = 'draft';

CREATE INDEX environment_changesets_environment_history
    ON environment_changesets (environment_id, updated_at DESC, id DESC);

CREATE TABLE environment_changeset_items (
    id TEXT PRIMARY KEY,
    changeset_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind TEXT NOT NULL CHECK (kind IN ('variable', 'networking', 'runtime', 'storage')),
    operation TEXT NOT NULL CHECK (operation IN ('add', 'edit', 'remove')),
    service_id TEXT NOT NULL,
    target_id TEXT NOT NULL DEFAULT '',
    variable_name TEXT NOT NULL DEFAULT '',
    variable_scope TEXT NOT NULL DEFAULT ''
        CHECK (variable_scope IN ('', 'plain', 'secret', 'sealed')),
    expected_version INTEGER,
    value_kind TEXT NOT NULL DEFAULT ''
        CHECK (value_kind IN ('', 'literal', 'reference')),
    "references" JSONB NOT NULL DEFAULT '[]'::jsonb,
    value_ciphertext BYTEA NOT NULL DEFAULT ''::bytea,
    kek_id TEXT NOT NULL DEFAULT '',
    wrapped_dek BYTEA NOT NULL DEFAULT ''::bytea,
    canonical_form TEXT NOT NULL DEFAULT '',
    length_bucket TEXT NOT NULL DEFAULT '',
    kek_fingerprint TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT environment_changeset_items_changeset_lineage_fk
        FOREIGN KEY (changeset_id, project_id, environment_id)
        REFERENCES environment_changesets (id, project_id, environment_id)
        ON DELETE CASCADE,
    CONSTRAINT environment_changeset_items_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id),
    UNIQUE (changeset_id, ordinal),
    UNIQUE (changeset_id, service_id, kind, variable_name)
);

CREATE INDEX environment_changeset_items_changeset
    ON environment_changeset_items (changeset_id, ordinal);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (57, 'environment_changesets', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
