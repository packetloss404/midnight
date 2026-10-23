DROP TABLE IF EXISTS route_backends;
DROP INDEX IF EXISTS idx_deployments_id_full_scope;
DROP TABLE IF EXISTS http_routes;
DROP TABLE IF EXISTS domains;
DROP TABLE IF EXISTS route_controller_groups;

DELETE FROM schema_migrations WHERE version = 22;
