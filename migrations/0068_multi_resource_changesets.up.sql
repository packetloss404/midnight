-- 0068 — add redaction-safe kind payloads to the canonical changeset item.
--
-- Variable values remain envelope encrypted in their existing columns. Runtime,
-- networking, and storage values are non-secret desired-state documents stored
-- as strict JSON objects. expected_version becomes BIGINT so the changeset can
-- fence every canonical resource version without narrowing it to Go int/SQL
-- INTEGER.

ALTER TABLE environment_changeset_items
    ALTER COLUMN expected_version TYPE BIGINT
        USING expected_version::BIGINT,
    ADD COLUMN payload JSONB NOT NULL DEFAULT '{}'::JSONB;

ALTER TABLE environment_changeset_items
    ADD CONSTRAINT environment_changeset_items_payload_shape_chk CHECK (
        jsonb_typeof(payload) = 'object'
    ),
    ADD CONSTRAINT environment_changeset_items_kind_payload_chk CHECK (
        (kind = 'variable' AND payload = '{}'::JSONB) OR
        (kind IN ('runtime', 'networking', 'storage') AND payload <> '{}'::JSONB)
    );

COMMENT ON COLUMN environment_changeset_items.payload IS
    'Strict redaction-safe runtime, networking, or storage desired state. Variable values remain envelope encrypted and keep an empty payload.';

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (68, 'multi_resource_changesets', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
