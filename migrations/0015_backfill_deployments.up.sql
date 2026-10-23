-- 0015_backfill_deployments.up.sql — best-effort, idempotent historical backfill.
--
-- GATED / STANDALONE: this is a separate migration a human runs deliberately.
-- It only INSERTs terminal (never 'active') canonical deployment rows, so it is
-- safe with respect to the DEFERRABLE one-active-per-service/environment
-- constraint from 0014.
--
-- SCOPE + LIMITATION: historical deploy operations do not carry a *structured*
-- environment_id (it lived in operation-event message strings, e.g.
-- deployIntakeQueuedEventMessage), so a faithful reconstruction is not possible
-- in SQL. This backfill therefore only synthesizes rows for deploy operations of
-- services whose project has EXACTLY ONE environment (env unambiguous). It never
-- marks a row 'active' (which would risk the one-active constraint and pick a
-- wrong active without route-observation evidence); the current active is
-- established by going-forward dual-write. Multi-environment services and
-- unrecoverable operations are intentionally skipped — the guarded read-flip
-- (internal/api/deployments.go) renders those via the operation-event fallback.
--
-- Idempotent: skips any operation that already has a linked deployment (mirror
-- from dual-write, or a prior backfill run). Reversible: the down migration
-- deletes only the backfilled rows (id prefix dep_bf_).
--
-- ORDERING: must run BEFORE the FSM trigger migration (0016) because it inserts
-- terminal states directly, which the trigger would otherwise reject.
--
-- Schema bumps to schema_version = 15.

INSERT INTO deployments (
    id, project_id, service_id, environment_id, action, state, active,
    operation_id, failure_code, created_at, updated_at, completed_at)
SELECT
    'dep_bf_' || o.id,
    s.project_id,
    s.id,
    (SELECT e.id FROM environments e
       WHERE e.project_id = s.project_id AND e.deleted_at IS NULL
       LIMIT 1),
    'deploy_image',
    CASE
        WHEN o.state = 'failed'   THEN 'failed'
        WHEN o.state = 'canceled' THEN 'canceled'
        ELSE 'superseded'
    END,
    FALSE,
    o.id,
    CASE WHEN o.state = 'failed' THEN 'backfilled_failure' ELSE '' END,
    o.created_at,
    COALESCE(o.updated_at, o.created_at),
    o.completed_at
FROM operations o
JOIN services s
  ON s.id = o.target_resource_id
 AND o.target_resource_kind = 'service'
WHERE o.kind = 'deploy'
  AND s.deleted_at IS NULL
  -- exactly one (non-deleted) environment in the project → unambiguous env
  AND (SELECT count(*) FROM environments e2
         WHERE e2.project_id = s.project_id AND e2.deleted_at IS NULL) = 1
  -- idempotent: skip operations that already have a linked deployment
  AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.operation_id = o.id)
ON CONFLICT (id) DO NOTHING;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (15, 'backfill_deployments', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
