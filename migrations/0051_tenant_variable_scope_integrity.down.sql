ALTER TABLE service_variable_versions
    DROP CONSTRAINT IF EXISTS service_variable_versions_parent_scope_fk;

DROP TRIGGER IF EXISTS service_variable_versions_derive_scope
ON service_variable_versions;

DROP FUNCTION IF EXISTS midnight_derive_service_variable_version_scope();

DROP INDEX IF EXISTS service_variables_id_ownership_uniq;

ALTER TABLE service_variables
    DROP CONSTRAINT IF EXISTS service_variables_service_project_fk;

ALTER TABLE service_variables
    DROP CONSTRAINT IF EXISTS service_variables_project_workspace_fk;

DROP TRIGGER IF EXISTS service_variables_derive_workspace
ON service_variables;

DROP FUNCTION IF EXISTS midnight_derive_service_variable_workspace();

ALTER TABLE service_variables
    DROP COLUMN IF EXISTS service_scope_id;

DELETE FROM schema_migrations
WHERE version = 51
  AND name = 'tenant_variable_scope_integrity';
