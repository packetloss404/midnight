DROP INDEX IF EXISTS idx_route_backends_deployment_route;
DROP TABLE IF EXISTS deployment_route_intents;

DELETE FROM schema_migrations
WHERE version = 48
  AND name = 'deployment_route_intents';
