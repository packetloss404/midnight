-- 0016_deployment_fsm_trigger.down.sql — detach the FSM trigger.
-- Leaves the validate_deployment_contract() function in place (owned by 0014).

DROP TRIGGER IF EXISTS trg_validate_deployment_contract ON deployments;

DELETE FROM schema_migrations WHERE version = 16;
