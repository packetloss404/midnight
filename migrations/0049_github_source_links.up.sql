-- 0049 — workspace-scoped GitHub App installations, exact repository grants,
-- canonical service links, and fail-closed quarantine of legacy JSONB links.
--
-- A GitHub App installation is globally exclusive to one Midnight workspace.
-- Repository access is narrower: recovery operators verify and grant each
-- exact immutable GitHub repository id before a product link can reference it.

CREATE TABLE github_workspace_installation_grants (
    workspace_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    account_login TEXT NOT NULL,
    target_type TEXT NOT NULL,
    granted_by_actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (workspace_id, installation_id),
    CONSTRAINT github_workspace_installation_grants_installation_uniq
        UNIQUE (installation_id),
    CONSTRAINT github_workspace_installation_grants_workspace_fk
        FOREIGN KEY (workspace_id)
        REFERENCES workspaces (id)
        ON DELETE RESTRICT,
    CONSTRAINT github_workspace_installation_grants_id_chk
        CHECK (installation_id > 0),
    CONSTRAINT github_workspace_installation_grants_account_chk
        CHECK (
            BTRIM(account_login) <> ''
            AND CHAR_LENGTH(account_login) <= 255
        ),
    CONSTRAINT github_workspace_installation_grants_target_chk
        CHECK (
            target_type IN ('Organization', 'User')
        ),
    CONSTRAINT github_workspace_installation_grants_actor_chk
        CHECK (
            BTRIM(granted_by_actor) <> ''
            AND CHAR_LENGTH(granted_by_actor) <= 255
        ),
    CONSTRAINT github_workspace_installation_grants_version_chk
        CHECK (resource_version >= 1)
);

CREATE INDEX github_workspace_installation_grants_workspace_idx
    ON github_workspace_installation_grants (
        workspace_id,
        account_login,
        installation_id
    );

CREATE TABLE github_workspace_repository_grants (
    workspace_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_full_name TEXT NOT NULL,
    clone_url TEXT NOT NULL,
    default_branch TEXT NOT NULL,
    repository_private BOOLEAN NOT NULL,
    repository_access_verified_at TIMESTAMPTZ NOT NULL,
    granted_by_actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (workspace_id, installation_id, repository_id),
    CONSTRAINT github_workspace_repository_grants_repository_uniq
        UNIQUE (repository_id),
    CONSTRAINT github_workspace_repository_grants_installation_fk
        FOREIGN KEY (workspace_id, installation_id)
        REFERENCES github_workspace_installation_grants (
            workspace_id,
            installation_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT github_workspace_repository_grants_exact_repo_uniq
        UNIQUE (
            workspace_id,
            installation_id,
            repository_id,
            repository_full_name
        ),
    CONSTRAINT github_workspace_repository_grants_exact_name_uniq
        UNIQUE (
            workspace_id,
            installation_id,
            repository_full_name
        ),
    CONSTRAINT github_workspace_repository_grants_ids_chk
        CHECK (installation_id > 0 AND repository_id > 0),
    CONSTRAINT github_workspace_repository_grants_repo_chk
        CHECK (
            BTRIM(repository_full_name) = repository_full_name
            AND repository_full_name = LOWER(repository_full_name)
            AND repository_full_name ~ '^[^/[:space:]]+/[^/[:space:]]+$'
            AND CHAR_LENGTH(repository_full_name) <= 512
        ),
    CONSTRAINT github_workspace_repository_grants_clone_url_chk
        CHECK (
            clone_url ~ '^https://[^[:space:]@/?#]+/[^[:space:]?#]+\.git$'
            AND CHAR_LENGTH(clone_url) <= 2048
        ),
    CONSTRAINT github_workspace_repository_grants_default_branch_chk
        CHECK (
            BTRIM(default_branch) <> ''
            AND default_branch !~ '^refs/'
            AND default_branch !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(default_branch) <= 255
        ),
    CONSTRAINT github_workspace_repository_grants_verified_chk
        CHECK (repository_access_verified_at <= updated_at),
    CONSTRAINT github_workspace_repository_grants_actor_chk
        CHECK (
            BTRIM(granted_by_actor) <> ''
            AND CHAR_LENGTH(granted_by_actor) <= 255
        ),
    CONSTRAINT github_workspace_repository_grants_version_chk
        CHECK (resource_version >= 1)
);

CREATE INDEX github_workspace_repository_grants_installation_idx
    ON github_workspace_repository_grants (
        workspace_id,
        installation_id,
        repository_full_name,
        repository_id
    );

CREATE TABLE service_github_links (
    service_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_full_name TEXT NOT NULL,
    branch TEXT NOT NULL,
    watch_paths TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    root_directory TEXT NOT NULL DEFAULT '',
    config_path TEXT NOT NULL DEFAULT '',
    wait_for_ci BOOLEAN NOT NULL DEFAULT FALSE,
    required_checks TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    auto_deploy BOOLEAN NOT NULL DEFAULT TRUE,
    linked_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    repository_access_verified_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT service_github_links_service_scope_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE CASCADE,
    CONSTRAINT service_github_links_project_scope_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT service_github_links_repository_grant_fk
        FOREIGN KEY (
            workspace_id,
            installation_id,
            repository_id,
            repository_full_name
        )
        REFERENCES github_workspace_repository_grants (
            workspace_id,
            installation_id,
            repository_id,
            repository_full_name
        )
        ON DELETE RESTRICT,
    CONSTRAINT service_github_links_ids_chk
        CHECK (installation_id > 0 AND repository_id > 0),
    CONSTRAINT service_github_links_repo_chk
        CHECK (
            BTRIM(repository_full_name) = repository_full_name
            AND repository_full_name = LOWER(repository_full_name)
            AND repository_full_name ~ '^[^/[:space:]]+/[^/[:space:]]+$'
            AND CHAR_LENGTH(repository_full_name) <= 512
        ),
    CONSTRAINT service_github_links_branch_chk
        CHECK (
            BTRIM(branch) <> ''
            AND branch !~ '^refs/'
            AND branch !~ '[[:cntrl:][:space:]]'
            AND CHAR_LENGTH(branch) <= 255
        ),
    CONSTRAINT service_github_links_watch_paths_chk
        CHECK (
            CARDINALITY(watch_paths) <= 128
            AND ARRAY_POSITION(watch_paths, '') IS NULL
        ),
    CONSTRAINT service_github_links_required_checks_chk
        CHECK (
            CARDINALITY(required_checks) <= 128
            AND ARRAY_POSITION(required_checks, '') IS NULL
            AND (wait_for_ci OR CARDINALITY(required_checks) = 0)
        ),
    CONSTRAINT service_github_links_root_directory_chk
        CHECK (
            root_directory !~ '^/'
            AND root_directory !~ '(^|/)\.\.(/|$)'
            AND CHAR_LENGTH(root_directory) <= 1024
        ),
    CONSTRAINT service_github_links_config_path_chk
        CHECK (
            config_path !~ '^/'
            AND config_path !~ '(^|/)\.\.(/|$)'
            AND CHAR_LENGTH(config_path) <= 1024
        ),
    CONSTRAINT service_github_links_verified_chk
        CHECK (repository_access_verified_at <= updated_at),
    CONSTRAINT service_github_links_version_chk
        CHECK (resource_version >= 1)
);

CREATE INDEX service_github_links_webhook_lookup_idx
    ON service_github_links (
        installation_id,
        repository_id,
        branch,
        service_id
    )
    WHERE auto_deploy = TRUE;

CREATE INDEX service_github_links_workspace_repo_idx
    ON service_github_links (
        workspace_id,
        repository_full_name,
        branch,
        service_id
    );

-- Preserve every legacy JSON value verbatim. Legacy rows lack an immutable
-- repository id and upstream access proof, so migration 0049 intentionally
-- does not copy any of them into an active canonical link.
CREATE TABLE github_source_link_quarantine (
    service_id TEXT PRIMARY KEY,
    project_id TEXT,
    workspace_id TEXT,
    legacy_link JSONB NOT NULL,
    reason TEXT NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at TIMESTAMPTZ,
    CONSTRAINT github_source_link_quarantine_reason_chk
        CHECK (
            reason IN (
                'legacy_source_link_requires_reverification',
                'legacy_source_link_scope_missing'
            )
        )
);

INSERT INTO github_source_link_quarantine (
    service_id,
    project_id,
    workspace_id,
    legacy_link,
    reason
)
SELECT
    service.id,
    service.project_id,
    project.workspace_id,
    service.spec->'github',
    CASE
        WHEN project.id IS NULL OR project.workspace_id IS NULL
            THEN 'legacy_source_link_scope_missing'
        ELSE 'legacy_source_link_requires_reverification'
    END
FROM services service
LEFT JOIN projects project
  ON project.id = service.project_id
WHERE service.spec ? 'github'
ON CONFLICT (service_id) DO NOTHING;

UPDATE services
SET spec = spec - 'github',
    updated_at = CURRENT_TIMESTAMP,
    resource_version = resource_version + 1
WHERE spec ? 'github';

DROP INDEX IF EXISTS idx_services_github_jsonb;
DROP INDEX IF EXISTS idx_services_github_repo_branch;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (49, 'github_source_links', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
