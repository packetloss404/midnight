-- Restore quarantined legacy values before canonical links. A source that was
-- explicitly reverified after migration wins over its quarantined predecessor.
UPDATE services service
SET spec = jsonb_set(
        service.spec,
        '{github}',
        quarantine.legacy_link,
        TRUE
    ),
    updated_at = CURRENT_TIMESTAMP,
    resource_version = service.resource_version + 1
FROM github_source_link_quarantine quarantine
WHERE quarantine.service_id = service.id;

UPDATE services service
SET spec = jsonb_set(
        service.spec,
        '{github}',
        jsonb_strip_nulls(jsonb_build_object(
            'installation_id', link.installation_id,
            'repository_id', link.repository_id,
            'repo', link.repository_full_name,
            'branch', link.branch,
            'watch_paths', CASE
                WHEN CARDINALITY(link.watch_paths) = 0 THEN NULL
                ELSE to_jsonb(link.watch_paths)
            END,
            'root_directory', NULLIF(link.root_directory, ''),
            'config_path', NULLIF(link.config_path, ''),
            'clone_url', repository.clone_url,
            'wait_for_ci', CASE WHEN link.wait_for_ci THEN TRUE ELSE NULL END,
            'required_checks', CASE
                WHEN CARDINALITY(link.required_checks) = 0 THEN NULL
                ELSE to_jsonb(link.required_checks)
            END
        )),
        TRUE
    ),
    updated_at = CURRENT_TIMESTAMP,
    resource_version = service.resource_version + 1
FROM service_github_links link
JOIN github_workspace_repository_grants repository
  ON repository.workspace_id = link.workspace_id
 AND repository.installation_id = link.installation_id
 AND repository.repository_id = link.repository_id
WHERE link.service_id = service.id;

DROP INDEX IF EXISTS service_github_links_workspace_repo_idx;
DROP INDEX IF EXISTS service_github_links_webhook_lookup_idx;
DROP TABLE IF EXISTS service_github_links;

DROP INDEX IF EXISTS github_workspace_repository_grants_installation_idx;
DROP TABLE IF EXISTS github_workspace_repository_grants;

DROP INDEX IF EXISTS github_workspace_installation_grants_workspace_idx;
DROP TABLE IF EXISTS github_workspace_installation_grants;

DROP TABLE IF EXISTS github_source_link_quarantine;

CREATE INDEX IF NOT EXISTS idx_services_github_repo_branch
    ON services (
        (spec->'github'->>'repo'),
        (spec->'github'->>'branch')
    )
    WHERE spec ? 'github';

CREATE INDEX IF NOT EXISTS idx_services_github_jsonb
    ON services USING GIN ((spec->'github'))
    WHERE spec ? 'github';

DELETE FROM schema_migrations
WHERE version = 49
  AND name = 'github_source_links';
