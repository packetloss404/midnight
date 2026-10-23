DROP TABLE IF EXISTS deployment_config_snapshots;
ALTER TABLE deployments DROP CONSTRAINT IF EXISTS deployments_id_config_scope_uq;
DROP TABLE IF EXISTS service_environment_configs;
DELETE FROM schema_migrations WHERE version = 23;
