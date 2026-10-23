ALTER TABLE environment_changeset_items
    DROP CONSTRAINT IF EXISTS environment_changeset_items_kind_payload_chk,
    DROP CONSTRAINT IF EXISTS environment_changeset_items_payload_shape_chk,
    DROP COLUMN IF EXISTS payload,
    ALTER COLUMN expected_version TYPE INTEGER
        USING expected_version::INTEGER;

DELETE FROM schema_migrations
 WHERE version = 68
   AND name = 'multi_resource_changesets';
