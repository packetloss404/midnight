DROP INDEX IF EXISTS uq_service_variables_active_scope_name;

DROP TABLE IF EXISTS service_variable_versions;

DELETE FROM service_variables
WHERE deleted_at IS NOT NULL;

ALTER TABLE service_variables
    DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE service_variables
    ADD CONSTRAINT service_variables_project_id_environment_id_service_id_name_key
    UNIQUE (project_id, environment_id, service_id, name);

DELETE FROM schema_migrations
WHERE version = 24;
