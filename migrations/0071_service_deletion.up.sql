-- 0071_service_deletion.up.sql — make service deletion a real lifecycle.
--
-- `services` already carried `deleted_at` (0001) and the workspace count
-- triggers (0069) already treat a non-null `deleted_at` as "not a live
-- service". Two schema facts still blocked exposing deletion over the API:
--
--   1. UNIQUE (project_id, slug) counted deleted rows, so a slug could never
--      be reused after its service was removed. A failed template run left
--      the slug permanently unusable inside that project. The constraint
--      becomes a partial unique index over live rows only.
--   2. The delete precondition asks, on every attempt, whether a service
--      still has live deployment intent. That predicate had no index, so it
--      degraded to a scan of the service's whole deployment history.
--
-- Nothing here touches the deployment FSM trigger attached in 0016: the
-- deletion path never inserts or updates a `deployments` row.
--
-- Schema bumps to schema_version = 71.

ALTER TABLE services
    DROP CONSTRAINT services_project_id_slug_key;

CREATE UNIQUE INDEX services_project_slug_live_uq
    ON services (project_id, slug)
    WHERE deleted_at IS NULL;

CREATE INDEX deployments_service_live_intent_idx
    ON deployments (service_id)
    WHERE state IN ('queued', 'building', 'deploying')
       OR (active AND runtime_desired_state = 'running');

CREATE INDEX idx_services_project_live
    ON services (project_id)
    WHERE deleted_at IS NULL;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (71, 'service_deletion', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
