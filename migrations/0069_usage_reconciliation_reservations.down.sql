DROP TRIGGER IF EXISTS volumes_usage_limit_before_write ON volumes;

DELETE FROM usage_admission_decisions WHERE dimension = 'volume_count';
DELETE FROM usage_reservations WHERE dimension = 'volume_count';
DELETE FROM usage_events
 WHERE dimension = 'volume_count' OR source = 'build_lifecycle';
DELETE FROM usage_source_cursors WHERE source = 'build_lifecycle';
DELETE FROM usage_evidence_gaps WHERE source = 'build_lifecycle';

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
BEGIN
    new_row := to_jsonb(NEW);
    IF TG_OP = 'UPDATE' THEN
        old_row := to_jsonb(OLD);
    END IF;

    IF TG_TABLE_NAME = 'projects' THEN
        target_workspace := new_row->>'workspace_id';
        target_dimension := 'project_count';
    ELSIF TG_TABLE_NAME = 'services' THEN
        SELECT workspace_id INTO target_workspace FROM projects WHERE id = new_row->>'project_id';
        target_dimension := 'service_count';
    ELSIF TG_TABLE_NAME = 'environments' THEN
        SELECT workspace_id INTO target_workspace FROM projects WHERE id = new_row->>'project_id';
        target_dimension := 'environment_count';
    ELSE
        RAISE EXCEPTION 'MIDNIGHT_USAGE_SCOPE_INVALID: unsupported count-limit table %', TG_TABLE_NAME
            USING ERRCODE = 'P0001';
    END IF;

    IF new_row->>'deleted_at' IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND old_row->>'deleted_at' IS NULL THEN
        IF TG_TABLE_NAME = 'projects' AND old_row->>'workspace_id' = new_row->>'workspace_id' THEN
            RETURN NEW;
        ELSIF TG_TABLE_NAME IN ('services', 'environments') THEN
            SELECT workspace_id INTO old_workspace FROM projects WHERE id = old_row->>'project_id';
            IF old_workspace = target_workspace THEN
                RETURN NEW;
            END IF;
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
    ELSE
        SELECT COUNT(*) INTO current_count FROM environments e
          JOIN projects p ON p.id = e.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL AND e.deleted_at IS NULL;
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

DELETE FROM usage_policy_limits WHERE dimension = 'volume_count';

UPDATE usage_policy_limits
   SET evidence_source = 'unavailable'
 WHERE dimension = 'build_seconds'
   AND evidence_source = 'deployments_build_lifecycle';

ALTER TABLE usage_reservations
    DROP CONSTRAINT usage_reservations_dimension_chk,
    ADD CONSTRAINT usage_reservations_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count', 'environment_count'
    ));

ALTER TABLE usage_events
    DROP CONSTRAINT usage_events_source_chk,
    ADD CONSTRAINT usage_events_source_chk CHECK (source IN (
        'resource_reconciler', 'runtime_metrics', 'traefik_access'
    )),
    DROP CONSTRAINT usage_events_dimension_chk,
    ADD CONSTRAINT usage_events_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    ));

ALTER TABLE usage_policy_limits
    DROP CONSTRAINT usage_policy_limits_dimension_chk,
    ADD CONSTRAINT usage_policy_limits_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    ));

DROP INDEX IF EXISTS deployments_build_completed_idx;

ALTER TABLE deployments
    DROP CONSTRAINT IF EXISTS deployments_build_window_chk,
    DROP COLUMN IF EXISTS build_completed_at,
    DROP COLUMN IF EXISTS build_started_at;

DELETE FROM schema_migrations
 WHERE version = 69
   AND name = 'usage_reconciliation_reservations';
