-- 0029_deployment_runtime_actions.down.sql

DELETE FROM staged_work_outbox
 WHERE stage IN ('deployment_image_replay','deployment_runtime_action');

ALTER TABLE staged_work_outbox
    DROP CONSTRAINT staged_work_outbox_stage_chk;

ALTER TABLE staged_work_outbox
    ADD CONSTRAINT staged_work_outbox_stage_chk
        CHECK (stage IN ('github_build_intake','build_deploy_handoff'));

DROP TABLE IF EXISTS deployment_runtime_actions;

ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_removed_not_active_chk,
    DROP CONSTRAINT IF EXISTS deployments_runtime_generation_chk,
    DROP CONSTRAINT IF EXISTS deployments_runtime_observed_state_chk,
    DROP CONSTRAINT IF EXISTS deployments_runtime_desired_state_chk,
    DROP COLUMN IF EXISTS runtime_generation,
    DROP COLUMN IF EXISTS runtime_observed_state,
    DROP COLUMN IF EXISTS runtime_desired_state;

DELETE FROM schema_migrations WHERE version = 29;
