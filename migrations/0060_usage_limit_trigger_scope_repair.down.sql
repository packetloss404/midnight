-- 0060_usage_limit_trigger_scope_repair.down.sql
-- Restore the migration-0059 function exactly; trigger bindings remain in
-- place in both directions.

CREATE OR REPLACE FUNCTION midnight_enforce_workspace_count_limit()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    target_workspace TEXT;
    target_dimension TEXT;
    current_count BIGINT;
    configured_limit BIGINT;
	old_workspace TEXT;
BEGIN
    IF TG_TABLE_NAME = 'projects' THEN
        target_workspace := NEW.workspace_id;
        target_dimension := 'project_count';
    ELSIF TG_TABLE_NAME = 'services' THEN
        SELECT workspace_id INTO target_workspace FROM projects WHERE id = NEW.project_id;
        target_dimension := 'service_count';
    ELSE
        SELECT workspace_id INTO target_workspace FROM projects WHERE id = NEW.project_id;
        target_dimension := 'environment_count';
    END IF;

	-- Inserts of already-deleted records and updates that do not make a row
	-- newly active in another count scope cannot increase live usage.
	IF NEW.deleted_at IS NOT NULL THEN
		RETURN NEW;
	END IF;
	IF TG_OP = 'UPDATE' AND OLD.deleted_at IS NULL THEN
		IF TG_TABLE_NAME = 'projects' AND OLD.workspace_id = NEW.workspace_id THEN
			RETURN NEW;
		ELSIF TG_TABLE_NAME IN ('services','environments') THEN
			SELECT workspace_id INTO old_workspace FROM projects WHERE id = OLD.project_id;
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
          JOIN projects p ON p.id=s.project_id
		 WHERE p.workspace_id=target_workspace AND p.deleted_at IS NULL AND s.deleted_at IS NULL;
    ELSE
		SELECT COUNT(*) INTO current_count FROM environments e
          JOIN projects p ON p.id=e.project_id
		 WHERE p.workspace_id=target_workspace AND p.deleted_at IS NULL AND e.deleted_at IS NULL;
    END IF;

    SELECT l.limit_value INTO configured_limit
      FROM usage_policy_limits l
     WHERE l.profile_key=COALESCE(
             (SELECT a.profile_key FROM workspace_usage_policy_profiles a WHERE a.workspace_id=target_workspace),
             'self_hosted'
           )
       AND l.dimension=target_dimension
       AND l.enforceable=TRUE;

    IF configured_limit IS NOT NULL AND current_count + 1 > configured_limit THEN
        RAISE EXCEPTION 'MIDNIGHT_USAGE_LIMIT_EXCEEDED: % limit %', target_dimension, configured_limit
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END
$$;

DELETE FROM schema_migrations
 WHERE version = 60 AND name = 'usage_limit_trigger_scope_repair';
