DROP TABLE IF EXISTS environment_changeset_items;
DROP TABLE IF EXISTS environment_changesets;

DELETE FROM schema_migrations
WHERE version = 57
  AND name = 'environment_changesets';
