-- 0069 — complete the evidence-first usage loop without adding commerce.
--
-- Source deployments gain an exact build-phase boundary, the usage ledger
-- admits replay-safe build lifecycle evidence, and logical volume counts can
-- be admitted with the same workspace lock used by atomic changesets. Volume
-- bytes remain informational because this migration does not claim a kernel
-- quota driver.

ALTER TABLE deployments
    ADD COLUMN build_started_at TIMESTAMPTZ,
    ADD COLUMN build_completed_at TIMESTAMPTZ,
    ADD CONSTRAINT deployments_build_window_chk CHECK (
        build_completed_at IS NULL OR (
            build_started_at IS NOT NULL AND
            build_completed_at >= build_started_at
        )
    );

CREATE INDEX deployments_build_completed_idx
    ON deployments (build_completed_at, id)
    WHERE build_completed_at IS NOT NULL;

ALTER TABLE usage_policy_limits
    DROP CONSTRAINT usage_policy_limits_dimension_chk,
    ADD CONSTRAINT usage_policy_limits_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'volume_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    ));

ALTER TABLE usage_events
    DROP CONSTRAINT usage_events_source_chk,
    ADD CONSTRAINT usage_events_source_chk CHECK (source IN (
        'resource_reconciler', 'runtime_metrics', 'traefik_access', 'build_lifecycle'
    )),
    DROP CONSTRAINT usage_events_dimension_chk,
    ADD CONSTRAINT usage_events_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'volume_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    ));

ALTER TABLE usage_reservations
    DROP CONSTRAINT usage_reservations_dimension_chk,
    ADD CONSTRAINT usage_reservations_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'volume_count'
    ));

INSERT INTO usage_policy_limits (
    profile_key, dimension, limit_value, enforceable, evidence_source
) VALUES (
    'self_hosted', 'volume_count', NULL, FALSE, 'volumes'
) ON CONFLICT (profile_key, dimension) DO NOTHING;

UPDATE usage_policy_limits
   SET evidence_source = 'deployments_build_lifecycle'
 WHERE dimension = 'build_seconds'
   AND evidence_source = 'unavailable';

CREATE OR REPLACE FUNCTION midnight_enforce_workspace_count_limit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    new_row JSONB;
    old_row JSONB;
    target_workspace TEXT;
    target_dimension TEXT;
    current_count BIGINT;
    configured_limit BIGINT;
    old_workspace TEXT;
    new_is_active BOOLEAN;
    old_is_active BOOLEAN;
BEGIN
    new_row := to_jsonb(NEW);
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    IF TG_TABLE_NAME = 'projects' THEN
        target_workspace := new_row->>'workspace_id';
        target_dimension := 'project_count';
        new_is_active := new_row->>'deleted_at' IS NULL;
        old_is_active := TG_OP = 'UPDATE' AND old_row->>'deleted_at' IS NULL;
    ELSIF TG_TABLE_NAME = 'services' THEN
        SELECT workspace_id INTO target_workspace
          FROM projects WHERE id = new_row->>'project_id';
        target_dimension := 'service_count';
        new_is_active := new_row->>'deleted_at' IS NULL;
        old_is_active := TG_OP = 'UPDATE' AND old_row->>'deleted_at' IS NULL;
    ELSIF TG_TABLE_NAME = 'environments' THEN
        SELECT workspace_id INTO target_workspace
          FROM projects WHERE id = new_row->>'project_id';
        target_dimension := 'environment_count';
        new_is_active := new_row->>'deleted_at' IS NULL;
        old_is_active := TG_OP = 'UPDATE' AND old_row->>'deleted_at' IS NULL;
    ELSIF TG_TABLE_NAME = 'volumes' THEN
        SELECT workspace_id INTO target_workspace
          FROM projects WHERE id = new_row->>'project_id';
        target_dimension := 'volume_count';
        new_is_active := new_row->>'status' IN ('provisioning','ready','attached');
        old_is_active := TG_OP = 'UPDATE' AND old_row->>'status' IN ('provisioning','ready','attached');
    ELSE
        RAISE EXCEPTION 'MIDNIGHT_USAGE_SCOPE_INVALID: unsupported count-limit table %', TG_TABLE_NAME
            USING ERRCODE = 'P0001';
    END IF;

    IF NOT new_is_active THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND old_is_active THEN
        IF TG_TABLE_NAME = 'projects' THEN
            old_workspace := old_row->>'workspace_id';
        ELSE
            SELECT workspace_id INTO old_workspace
              FROM projects WHERE id = old_row->>'project_id';
        END IF;
        IF old_workspace = target_workspace THEN
            RETURN NEW;
        END IF;
    END IF;

    IF target_workspace IS NULL THEN
        RAISE EXCEPTION 'MIDNIGHT_USAGE_SCOPE_INVALID: workspace could not be resolved'
            USING ERRCODE = 'P0001';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtext('midnight:usage-admission:' || target_workspace));

    IF target_dimension = 'project_count' THEN
        SELECT COUNT(*) INTO current_count FROM projects
         WHERE workspace_id = target_workspace AND deleted_at IS NULL;
    ELSIF target_dimension = 'service_count' THEN
        SELECT COUNT(*) INTO current_count FROM services s
          JOIN projects p ON p.id = s.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL AND s.deleted_at IS NULL;
    ELSIF target_dimension = 'environment_count' THEN
        SELECT COUNT(*) INTO current_count FROM environments e
          JOIN projects p ON p.id = e.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL AND e.deleted_at IS NULL;
    ELSE
        SELECT COUNT(*) INTO current_count FROM volumes v
          JOIN projects p ON p.id = v.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL
           AND v.status IN ('provisioning','ready','attached');
    END IF;

    SELECT l.limit_value INTO configured_limit
      FROM usage_policy_limits l
     WHERE l.profile_key = COALESCE(
             (SELECT a.profile_key FROM workspace_usage_policy_profiles a
               WHERE a.workspace_id = target_workspace),
             'self_hosted'
           )
       AND l.dimension = target_dimension
       AND l.enforceable = TRUE;

    IF configured_limit IS NOT NULL AND current_count + 1 > configured_limit THEN
        RAISE EXCEPTION 'MIDNIGHT_USAGE_LIMIT_EXCEEDED: % limit %', target_dimension, configured_limit
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER volumes_usage_limit_before_write
BEFORE INSERT OR UPDATE ON volumes FOR EACH ROW
EXECUTE FUNCTION midnight_enforce_workspace_count_limit();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (69, 'usage_reconciliation_reservations', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
