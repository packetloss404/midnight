-- 0059_usage_limits.up.sql
--
-- Durable, tenant-scoped usage evidence and admission policy. Donations and
-- payment providers are intentionally absent: a contribution never changes
-- an entitlement. Public traffic is sourced only from Traefik access records;
-- Docker network counters are deliberately not billable evidence.

CREATE TABLE usage_policy_profiles (
    profile_key TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT usage_policy_profiles_key_chk CHECK (profile_key ~ '^[a-z][a-z0-9_-]{1,63}$')
);

INSERT INTO usage_policy_profiles (profile_key, display_name, description)
VALUES ('self_hosted', 'Self-hosted', 'Installation policy; not a paid entitlement')
ON CONFLICT (profile_key) DO NOTHING;

CREATE TABLE usage_policy_limits (
    profile_key TEXT NOT NULL REFERENCES usage_policy_profiles(profile_key) ON DELETE CASCADE,
    dimension TEXT NOT NULL,
    limit_value BIGINT,
    enforceable BOOLEAN NOT NULL DEFAULT FALSE,
    evidence_source TEXT NOT NULL,
    PRIMARY KEY (profile_key, dimension),
    CONSTRAINT usage_policy_limits_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    )),
    CONSTRAINT usage_policy_limits_value_chk CHECK (limit_value IS NULL OR limit_value > 0),
    CONSTRAINT usage_policy_limits_enforceable_chk CHECK (NOT enforceable OR limit_value IS NOT NULL)
);

-- Profiles start informational/unlimited. Runtime wiring overlays the actual
-- configured source-artifact quota when uploads are enabled. Count limits are
-- not silently imposed; an installation owner must configure a policy before
-- the trigger or internal reservation seam enforces one.
INSERT INTO usage_policy_limits (profile_key, dimension, limit_value, enforceable, evidence_source)
VALUES
    ('self_hosted', 'source_artifact_bytes', NULL, FALSE, 'source_artifact_workspace_charges'),
    ('self_hosted', 'project_count', NULL, FALSE, 'projects'),
    ('self_hosted', 'service_count', NULL, FALSE, 'services'),
    ('self_hosted', 'environment_count', NULL, FALSE, 'environments'),
    ('self_hosted', 'build_seconds', NULL, FALSE, 'unavailable'),
    ('self_hosted', 'cpu_seconds', NULL, FALSE, 'observability_metric_samples'),
    ('self_hosted', 'memory_byte_seconds', NULL, FALSE, 'observability_metric_samples'),
    ('self_hosted', 'public_http_requests', NULL, FALSE, 'traefik_access_logs'),
    ('self_hosted', 'public_http_response_bytes', NULL, FALSE, 'traefik_access_logs')
ON CONFLICT (profile_key, dimension) DO NOTHING;

CREATE TABLE workspace_usage_policy_profiles (
    workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    profile_key TEXT NOT NULL REFERENCES usage_policy_profiles(profile_key) ON DELETE RESTRICT DEFAULT 'self_hosted',
    assigned_by TEXT NOT NULL DEFAULT 'system',
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT workspace_usage_policy_profiles_actor_chk CHECK (octet_length(assigned_by) BETWEEN 1 AND 255)
);

INSERT INTO workspace_usage_policy_profiles (workspace_id, profile_key)
SELECT id, 'self_hosted' FROM workspaces
ON CONFLICT (workspace_id) DO NOTHING;

CREATE TABLE usage_events (
    sequence BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    source TEXT NOT NULL,
    source_instance TEXT NOT NULL,
    source_event_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    project_id TEXT,
    environment_id TEXT,
    dimension TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    value_kind TEXT NOT NULL DEFAULT 'increment',
    interval_start TIMESTAMPTZ,
    observed_at TIMESTAMPTZ NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (source, source_instance, source_event_id),
    CONSTRAINT usage_events_identity_chk CHECK (
        octet_length(event_id) BETWEEN 1 AND 255 AND
        octet_length(source) BETWEEN 1 AND 128 AND
        octet_length(source_instance) BETWEEN 1 AND 255 AND
        octet_length(source_event_id) BETWEEN 1 AND 255
    ),
	CONSTRAINT usage_events_opaque_identity_chk CHECK (
		event_id ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$' AND
		source_instance ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$' AND
		source_event_id ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'
	),
	CONSTRAINT usage_events_source_chk CHECK (source IN (
		'resource_reconciler', 'runtime_metrics', 'traefik_access'
	)),
    CONSTRAINT usage_events_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count',
        'environment_count', 'build_seconds', 'cpu_seconds',
        'memory_byte_seconds', 'public_http_requests', 'public_http_response_bytes'
    )),
    CONSTRAINT usage_events_quantity_chk CHECK (quantity >= 0),
    CONSTRAINT usage_events_kind_chk CHECK (value_kind IN ('increment', 'gauge')),
    CONSTRAINT usage_events_interval_chk CHECK (interval_start IS NULL OR interval_start <= observed_at),
	CONSTRAINT usage_events_attributes_chk CHECK (
		jsonb_typeof(attributes) = 'object' AND
		attributes - ARRAY['sample_id','previous_sample_id']::text[] = '{}'::jsonb AND
		(attributes->'sample_id' IS NULL OR (
			jsonb_typeof(attributes->'sample_id') = 'string' AND
			attributes->>'sample_id' ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'
		)) AND
		(attributes->'previous_sample_id' IS NULL OR (
			jsonb_typeof(attributes->'previous_sample_id') = 'string' AND
			attributes->>'previous_sample_id' ~ '^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$'
		)) AND
		octet_length(attributes::text) <= 1024
	),
    -- Project/environment IDs are immutable evidence labels rather than
    -- lifecycle foreign keys: deleting a child resource must not erase its
    -- workspace's historical ledger.
    CONSTRAINT usage_events_environment_requires_project_chk
        CHECK (environment_id IS NULL OR project_id IS NOT NULL)
);

CREATE INDEX usage_events_workspace_time_idx
    ON usage_events (workspace_id, observed_at, sequence);
CREATE INDEX usage_events_workspace_dimension_idx
    ON usage_events (workspace_id, dimension, observed_at DESC, sequence DESC);

CREATE TABLE usage_source_cursors (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    source_instance TEXT NOT NULL,
    cursor TEXT NOT NULL DEFAULT '',
    last_sequence BIGINT NOT NULL DEFAULT 0,
    last_observed_at TIMESTAMPTZ,
    expected_interval_seconds INTEGER,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, source, source_instance),
    CONSTRAINT usage_source_cursors_sequence_chk CHECK (last_sequence >= 0),
    CONSTRAINT usage_source_cursors_interval_chk CHECK (
        expected_interval_seconds IS NULL OR expected_interval_seconds > 0
    )
);

CREATE TABLE usage_evidence_gaps (
    id BIGSERIAL PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    source_instance TEXT NOT NULL,
    gap_start TIMESTAMPTZ NOT NULL,
    gap_end TIMESTAMPTZ NOT NULL,
    reason TEXT NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ,
    UNIQUE (workspace_id, source, source_instance, gap_start, gap_end),
    CONSTRAINT usage_evidence_gaps_window_chk CHECK (gap_end > gap_start),
    CONSTRAINT usage_evidence_gaps_resolution_chk CHECK (resolved_at IS NULL OR resolved_at >= detected_at)
);

CREATE INDEX usage_evidence_gaps_open_idx
    ON usage_evidence_gaps (workspace_id, gap_start, gap_end) WHERE resolved_at IS NULL;

CREATE TABLE usage_reservations (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    dimension TEXT NOT NULL,
    quantity BIGINT NOT NULL,
    actor_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    intent_hash TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'active',
    expires_at TIMESTAMPTZ NOT NULL,
    committed_event_id TEXT REFERENCES usage_events(event_id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (workspace_id, actor_id, idempotency_key),
    CONSTRAINT usage_reservations_id_chk CHECK (id ~ '^usrsv_[0-9a-f]{32}$'),
    CONSTRAINT usage_reservations_dimension_chk CHECK (dimension IN (
        'source_artifact_bytes', 'project_count', 'service_count', 'environment_count'
    )),
    CONSTRAINT usage_reservations_quantity_chk CHECK (quantity > 0),
    CONSTRAINT usage_reservations_hash_chk CHECK (intent_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT usage_reservations_state_chk CHECK (state IN ('active', 'committed', 'released', 'expired')),
    CONSTRAINT usage_reservations_expiry_chk CHECK (expires_at > created_at)
);

CREATE INDEX usage_reservations_active_idx
    ON usage_reservations (workspace_id, dimension, expires_at)
    WHERE state = 'active';

CREATE TABLE usage_admission_decisions (
    id BIGSERIAL PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
	reservation_id TEXT REFERENCES usage_reservations(id) ON DELETE SET NULL,
    actor_id TEXT NOT NULL,
    dimension TEXT NOT NULL,
    requested BIGINT NOT NULL,
    used BIGINT NOT NULL,
    reserved BIGINT NOT NULL,
    limit_value BIGINT,
    decision TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT usage_admission_decisions_decision_chk CHECK (decision IN ('admit', 'deny', 'replay'))
	,CONSTRAINT usage_admission_decisions_values_chk CHECK (
		requested > 0 AND used >= 0 AND reserved >= 0 AND
		(limit_value IS NULL OR limit_value > 0)
	)
);

CREATE INDEX usage_admission_decisions_workspace_time_idx
    ON usage_admission_decisions (workspace_id, occurred_at DESC, id DESC);

-- Count-resource writes are themselves the authoritative transaction, so a
-- trigger closes bypasses from API, CLI, workers, or future code paths. Soft
-- observations never participate in this admission gate.
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

CREATE TRIGGER projects_usage_limit_before_write
BEFORE INSERT OR UPDATE ON projects FOR EACH ROW
EXECUTE FUNCTION midnight_enforce_workspace_count_limit();
CREATE TRIGGER services_usage_limit_before_write
BEFORE INSERT OR UPDATE ON services FOR EACH ROW
EXECUTE FUNCTION midnight_enforce_workspace_count_limit();
CREATE TRIGGER environments_usage_limit_before_write
BEFORE INSERT OR UPDATE ON environments FOR EACH ROW
EXECUTE FUNCTION midnight_enforce_workspace_count_limit();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (59, 'usage_limits', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
