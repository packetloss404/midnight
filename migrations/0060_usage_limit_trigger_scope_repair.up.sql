-- 0060_usage_limit_trigger_scope_repair.up.sql
--
-- Migration 0059 deliberately shares one admission function across projects,
-- services, and environments. PL/pgSQL still resolves static OLD/NEW column
-- references against each trigger's concrete row type, so the project-only
-- workspace field on OLD fails with SQLSTATE 42703 for service and environment
-- updates. Read the polymorphic trigger rows through JSON instead; this keeps
-- the existing trigger bindings and admission semantics intact.

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
        SELECT workspace_id INTO target_workspace
          FROM projects
         WHERE id = new_row->>'project_id';
        target_dimension := 'service_count';
    ELSIF TG_TABLE_NAME = 'environments' THEN
        SELECT workspace_id INTO target_workspace
          FROM projects
         WHERE id = new_row->>'project_id';
        target_dimension := 'environment_count';
    ELSE
        RAISE EXCEPTION 'MIDNIGHT_USAGE_SCOPE_INVALID: unsupported count-limit table %', TG_TABLE_NAME
            USING ERRCODE = 'P0001';
    END IF;

    -- Inserts of already-deleted records and updates that do not make a row
    -- newly active in another count scope cannot increase live usage.
    IF new_row->>'deleted_at' IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'UPDATE' AND old_row->>'deleted_at' IS NULL THEN
        IF TG_TABLE_NAME = 'projects'
           AND old_row->>'workspace_id' = new_row->>'workspace_id' THEN
            RETURN NEW;
        ELSIF TG_TABLE_NAME IN ('services', 'environments') THEN
            SELECT workspace_id INTO old_workspace
              FROM projects
             WHERE id = old_row->>'project_id';
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
        SELECT COUNT(*) INTO current_count
          FROM projects
         WHERE workspace_id = target_workspace AND deleted_at IS NULL;
    ELSIF target_dimension = 'service_count' THEN
        SELECT COUNT(*) INTO current_count
          FROM services s
          JOIN projects p ON p.id = s.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL
           AND s.deleted_at IS NULL;
    ELSE
        SELECT COUNT(*) INTO current_count
          FROM environments e
          JOIN projects p ON p.id = e.project_id
         WHERE p.workspace_id = target_workspace
           AND p.deleted_at IS NULL
           AND e.deleted_at IS NULL;
    END IF;

    SELECT l.limit_value INTO configured_limit
      FROM usage_policy_limits l
     WHERE l.profile_key = COALESCE(
             (SELECT a.profile_key
                FROM workspace_usage_policy_profiles a
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

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (60, 'usage_limit_trigger_scope_repair', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
