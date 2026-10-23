-- 0014_canonical_deployments.down.sql — drop canonical deployment records.

-- The enforcing trigger is created in a later gated migration; drop it here too
-- if present so a down from that later state is clean.
DROP TRIGGER IF EXISTS trg_validate_deployment_contract ON deployments;
DROP FUNCTION IF EXISTS validate_deployment_contract();

DROP INDEX IF EXISTS idx_deployment_events_deployment_seq;
DROP TABLE IF EXISTS deployment_events;

DROP TABLE IF EXISTS service_environment_state;

DROP INDEX IF EXISTS idx_deployments_operation;
DROP INDEX IF EXISTS idx_deployments_service_env_created;
DROP TABLE IF EXISTS deployments;

DELETE FROM schema_migrations WHERE version = 14;
