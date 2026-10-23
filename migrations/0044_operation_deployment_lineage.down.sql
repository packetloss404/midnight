-- Reverses only the structural operation-lineage uniqueness guarantee. No
-- deployment row is changed or removed.

DROP INDEX IF EXISTS deployments_operation_id_uniq;

DELETE FROM schema_migrations
WHERE version = 44
  AND name = 'operation_deployment_lineage';
