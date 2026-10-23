-- 0010_service_github_index.up.sql - service.spec.github lookup index.
--
-- Powers the GitHub-App webhook router (ADR 0022). The webhook
-- receiver looks up every service whose spec.github.repo + branch
-- matches an inbound `push` event and enqueues one deploy per match.
-- Without this index the lookup is a linear scan over `services`.
--
-- The index is a partial index on `(spec->>github,repo, spec->>github,branch)`
-- only when spec ? 'github' is true, so services without a link don't
-- bloat the index. The expression-index trick keeps it usable from
-- both the JSON-path accessor (->>'repo') and the operator (jsonb_path_query).
--
-- Schema bumps to schema_version = 10.

CREATE INDEX IF NOT EXISTS idx_services_github_repo_branch
    ON services (
        (spec->'github'->>'repo'),
        (spec->'github'->>'branch')
    )
    WHERE spec ? 'github';

-- Companion GIN index supports queries that filter on installation_id
-- (used by the link-list endpoint and the maintenance reconciler).
CREATE INDEX IF NOT EXISTS idx_services_github_jsonb
    ON services USING GIN ((spec->'github'))
    WHERE spec ? 'github';

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (10, 'service_github_index', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
