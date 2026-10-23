-- 0010 — variable references (Sprint MVP: reference-variable resolver).
--
-- Adds a service_variables table that backs the encrypted Vault (Lane 4
-- ships the in-memory primary; this table is the Postgres-backed
-- store-of-record once the Vault gains a database driver). The forward
-- compatibility story is:
--
--   * Existing deployments that have never persisted variables to
--     Postgres see this migration as a pure CREATE TABLE — no rows are
--     touched.
--   * The encrypted `value_ciphertext` column carries the **template
--     string** ciphertext, not the resolved value, so the at-rest
--     contract for sealed/secret variables is unchanged.
--   * `value_kind` is one of `literal` | `reference`. A row is
--     `reference` when its decrypted plaintext contains at least one
--     `${{...}}` token; the Sprint MVP encodes the parse result in the
--     `references` JSONB column for read APIs (names only — values are
--     never duplicated outside the encrypted blob).
--
-- The migration is **additive only** so the 0009 → 0010 forward and
-- 0010 → 0009 backward paths are reversible without data loss. Existing
-- audit / dashboard surfaces that ignore the new columns continue to
-- work; the new columns default to `'literal'` and `'[]'` for any row
-- written by an older code path.

CREATE TABLE IF NOT EXISTS service_variables (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'secret',
    value_kind TEXT NOT NULL DEFAULT 'literal'
        CHECK (value_kind IN ('literal','reference')),
    value_ciphertext BYTEA NOT NULL,
    kek_id TEXT NOT NULL DEFAULT '',
    wrapped_dek BYTEA NOT NULL DEFAULT ''::bytea,
    canonical_form TEXT NOT NULL DEFAULT 'midnight-envelope-v1',
    "references" JSONB NOT NULL DEFAULT '[]'::jsonb,
    length_bucket TEXT NOT NULL DEFAULT 'empty',
    version INTEGER NOT NULL DEFAULT 1,
    last_rotated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (project_id, environment_id, service_id, name)
);

CREATE INDEX IF NOT EXISTS idx_service_variables_scope
    ON service_variables (project_id, environment_id, service_id);
CREATE INDEX IF NOT EXISTS idx_service_variables_kind
    ON service_variables (value_kind)
    WHERE value_kind = 'reference';
