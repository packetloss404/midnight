-- 0070 — truthful environment configuration revisions and grouped deploy intake.
--
-- Configuration revisions are environment-scoped monotonic facts. A service
-- records the latest applied revision and the latest revision proven by an
-- active deployment. Apply-and-deploy groups retain per-service intake truth;
-- a failed intake never erases an already-applied configuration revision.

ALTER TABLE environment_changesets
    ADD COLUMN applied_configuration_revision BIGINT
        CHECK (applied_configuration_revision IS NULL OR applied_configuration_revision > 0);

ALTER TABLE deployments
    ADD COLUMN configuration_revision BIGINT NOT NULL DEFAULT 0
        CHECK (configuration_revision >= 0);

-- The immutable snapshot carries the same revision as its deployment. Legacy
-- snapshots predate configuration revisions and truthfully remain revision 0.
ALTER TABLE deployment_config_snapshots
    ADD COLUMN configuration_revision BIGINT NOT NULL DEFAULT 0
        CHECK (configuration_revision >= 0);

CREATE TABLE environment_configuration_revisions (
    environment_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    applied_revision BIGINT NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT environment_configuration_revisions_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT environment_configuration_revisions_project_workspace_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id) ON DELETE CASCADE
);

CREATE TABLE service_configuration_revisions (
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    applied_revision BIGINT NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    deployed_revision BIGINT NOT NULL DEFAULT 0 CHECK (deployed_revision >= 0),
    deployed_deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    pending_deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (environment_id, service_id),
    CONSTRAINT service_configuration_revisions_order_chk
        CHECK (deployed_revision <= applied_revision),
    CONSTRAINT service_configuration_revisions_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT service_configuration_revisions_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE
);

CREATE TABLE environment_operation_groups (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    changeset_id TEXT NOT NULL,
    configuration_revision BIGINT NOT NULL CHECK (configuration_revision > 0),
    state TEXT NOT NULL CHECK (state IN ('accepted', 'partial')),
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT environment_operation_groups_changeset_fk
        FOREIGN KEY (changeset_id, project_id, environment_id)
        REFERENCES environment_changesets (id, project_id, environment_id) ON DELETE RESTRICT,
    CONSTRAINT environment_operation_groups_project_workspace_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id) ON DELETE CASCADE
);

CREATE INDEX environment_operation_groups_environment_created
    ON environment_operation_groups (environment_id, created_at DESC, id DESC);

CREATE TABLE environment_operation_group_items (
    group_id TEXT NOT NULL REFERENCES environment_operation_groups(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    resource_kinds TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    requires_deploy BOOLEAN NOT NULL,
    deployment_action TEXT NOT NULL CHECK (deployment_action IN ('none', 'redeploy', 'rebuild')),
    intake_state TEXT NOT NULL CHECK (intake_state IN ('not_required', 'queued', 'failed')),
    deployment_id TEXT REFERENCES deployments(id) ON DELETE SET NULL,
    operation_id TEXT REFERENCES operations(id) ON DELETE SET NULL,
    failure_code TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, service_id),
    CONSTRAINT environment_operation_group_items_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id) ON DELETE CASCADE,
    CONSTRAINT environment_operation_group_items_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id) ON DELETE CASCADE,
    CONSTRAINT environment_operation_group_items_shape_chk CHECK (
        (requires_deploy AND deployment_action IN ('redeploy', 'rebuild') AND intake_state IN ('queued', 'failed')) OR
        (NOT requires_deploy AND deployment_action = 'none' AND intake_state = 'not_required')
    ),
    CONSTRAINT environment_operation_group_items_queued_shape_chk CHECK (
        intake_state <> 'queued' OR (deployment_id IS NOT NULL AND operation_id IS NOT NULL)
    )
);

CREATE OR REPLACE FUNCTION record_deployed_configuration_revision()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    current_deployment_id TEXT;
    current_revision BIGINT;
BEGIN
    -- A queued candidate is no longer pending once it terminates without
    -- activation. This is intentionally independent from active-deployment
    -- reconciliation because a previous release may still be serving.
    IF NEW.state IN ('failed', 'canceled') THEN
        UPDATE service_configuration_revisions
           SET pending_deployment_id = NULL,
               updated_at = NEW.updated_at
         WHERE project_id = NEW.project_id
           AND environment_id = NEW.environment_id
           AND service_id = NEW.service_id
           AND pending_deployment_id = NEW.id;
    END IF;

    -- Derive the exact currently serving deployment after every relevant
    -- transition. GREATEST is deliberately not used for deployed_revision:
    -- rollbacks may serve an older revision (including legacy revision zero),
    -- and no active deployment means no deployed revision or deployment id.
    SELECT id, configuration_revision
      INTO current_deployment_id, current_revision
      FROM deployments
     WHERE project_id = NEW.project_id
       AND environment_id = NEW.environment_id
       AND service_id = NEW.service_id
       AND active = TRUE
       AND state = 'active'
     ORDER BY updated_at DESC, id DESC
     LIMIT 1;

    IF FOUND THEN
        INSERT INTO service_configuration_revisions (
            project_id, environment_id, service_id, applied_revision,
            deployed_revision, deployed_deployment_id, pending_deployment_id, updated_at
        ) VALUES (
            NEW.project_id, NEW.environment_id, NEW.service_id, current_revision,
            current_revision, current_deployment_id, NULL, NEW.updated_at
        )
        ON CONFLICT (environment_id, service_id) DO UPDATE SET
            applied_revision = GREATEST(service_configuration_revisions.applied_revision, EXCLUDED.applied_revision),
            deployed_revision = EXCLUDED.deployed_revision,
            deployed_deployment_id = EXCLUDED.deployed_deployment_id,
            pending_deployment_id = CASE
                WHEN service_configuration_revisions.pending_deployment_id = current_deployment_id THEN NULL
                ELSE service_configuration_revisions.pending_deployment_id
            END,
            updated_at = EXCLUDED.updated_at;
    ELSE
        UPDATE service_configuration_revisions
           SET deployed_revision = 0,
               deployed_deployment_id = NULL,
               updated_at = NEW.updated_at
         WHERE project_id = NEW.project_id
           AND environment_id = NEW.environment_id
           AND service_id = NEW.service_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER deployments_configuration_revision_active_trg
AFTER UPDATE OF state, active, configuration_revision ON deployments
FOR EACH ROW
EXECUTE FUNCTION record_deployed_configuration_revision();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (70, 'changeset_apply_deploy_groups', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
