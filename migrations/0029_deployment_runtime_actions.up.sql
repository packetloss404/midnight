-- 0029_deployment_runtime_actions.up.sql
--
-- Durable Railway-style deployment actions. Runtime control is deliberately
-- separate from the immutable deployment FSM: stop/restart/remove mutate the
-- desired state of one existing runtime instance, while redeploy/rollback
-- create a new canonical deployment and reuse an immutable image/config
-- snapshot.

ALTER TABLE deployments
    ADD COLUMN runtime_desired_state TEXT NOT NULL DEFAULT 'running',
    ADD COLUMN runtime_observed_state TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN runtime_generation BIGINT NOT NULL DEFAULT 1;

ALTER TABLE deployments
    ADD CONSTRAINT deployments_runtime_desired_state_chk
        CHECK (runtime_desired_state IN ('running','stopped','removed')),
    ADD CONSTRAINT deployments_runtime_observed_state_chk
        CHECK (runtime_observed_state IN ('unknown','running','stopped','removed')),
    ADD CONSTRAINT deployments_runtime_generation_chk
        CHECK (runtime_generation > 0),
    ADD CONSTRAINT deployments_removed_not_active_chk
        CHECK (runtime_desired_state <> 'removed' OR active = FALSE);

CREATE TABLE deployment_runtime_actions (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('stop','restart','remove')),
    state TEXT NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued','running','succeeded','failed','canceled')),
    step TEXT NOT NULL DEFAULT ''
        CHECK (step IN ('','route_withdrawn','stopped','started','removed')),
    trigger_key TEXT NOT NULL UNIQUE,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    resource_version BIGINT NOT NULL DEFAULT 1
);

CREATE INDEX deployment_runtime_actions_deployment_created_idx
    ON deployment_runtime_actions (deployment_id, created_at DESC);

ALTER TABLE staged_work_outbox
    DROP CONSTRAINT staged_work_outbox_stage_chk;

ALTER TABLE staged_work_outbox
    ADD CONSTRAINT staged_work_outbox_stage_chk
        CHECK (stage IN (
            'github_build_intake',
            'build_deploy_handoff',
            'deployment_image_replay',
            'deployment_runtime_action'
        ));

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (29, 'deployment_runtime_actions', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
