ALTER TABLE artifact_promotions
    DROP CONSTRAINT IF EXISTS artifact_promotions_source_environment_project_fk;
ALTER TABLE artifact_promotions
    DROP CONSTRAINT IF EXISTS artifact_promotions_dest_environment_project_fk;
ALTER TABLE artifact_promotions
    DROP CONSTRAINT IF EXISTS artifact_promotions_service_project_fk;
ALTER TABLE service_variables
    DROP CONSTRAINT IF EXISTS service_variables_environment_project_fk;
ALTER TABLE volumes
    DROP CONSTRAINT IF EXISTS volumes_environment_project_fk;
ALTER TABLE volumes
    DROP CONSTRAINT IF EXISTS volumes_service_project_fk;
ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_environment_project_fk;
ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_service_project_fk;

DROP INDEX IF EXISTS idx_environments_id_project_scope;
DROP INDEX IF EXISTS idx_services_id_project_scope;

-- The audit snapshots are migration-owned. Deterministic project_id repairs
-- are intentionally retained: reverting them would reintroduce known-invalid
-- resource relationships.
DROP TABLE IF EXISTS resource_scope_quarantine;
