-- 0065 — durable, service-focused GitHub pull-request preview lifecycle.
--
-- Preview rows retain the exact installation/repository/link authority that
-- existed when a signed delivery was routed. Fork classification, isolation,
-- expiry, and cleanup state are durable product truth rather than webhook-log
-- inference. Runtime history remains in canonical deployments/environments.

CREATE TABLE github_pull_request_previews (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    service_id TEXT NOT NULL,
    environment_id TEXT NOT NULL,
    source_environment_id TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    installation_grant_resource_version BIGINT NOT NULL,
    installation_grant_created_at TIMESTAMPTZ NOT NULL,
    installation_grant_updated_at TIMESTAMPTZ NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_full_name TEXT NOT NULL,
    repository_grant_resource_version BIGINT NOT NULL,
    repository_grant_created_at TIMESTAMPTZ NOT NULL,
    repository_grant_updated_at TIMESTAMPTZ NOT NULL,
    repository_access_verified_at TIMESTAMPTZ NOT NULL,
    link_resource_version BIGINT NOT NULL,
    link_created_at TIMESTAMPTZ NOT NULL,
    link_updated_at TIMESTAMPTZ NOT NULL,
    link_repository_access_verified_at TIMESTAMPTZ NOT NULL,
    clone_url TEXT NOT NULL,
    root_directory TEXT NOT NULL DEFAULT '',
    config_path TEXT NOT NULL DEFAULT '',
    pull_request_number INTEGER NOT NULL,
    base_branch TEXT NOT NULL,
    head_repository_id BIGINT,
    head_repository_full_name TEXT NOT NULL DEFAULT '',
    head_branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    from_fork BOOLEAN NOT NULL,
    secret_policy TEXT NOT NULL,
    cache_policy TEXT NOT NULL,
    quota_profile TEXT NOT NULL,
    state TEXT NOT NULL,
    preview_url TEXT NOT NULL DEFAULT '',
    deployment_id TEXT,
    last_delivery_id TEXT NOT NULL,
    last_comment_id BIGINT,
    expires_at TIMESTAMPTZ NOT NULL,
    delete_after TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource_version BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT github_pull_request_previews_identity_uniq
        UNIQUE (installation_id, repository_id, pull_request_number, service_id),
    CONSTRAINT github_pull_request_previews_environment_uniq
        UNIQUE (environment_id),
    CONSTRAINT github_pull_request_previews_deployment_uniq
        UNIQUE (deployment_id),
    CONSTRAINT github_pull_request_previews_id_chk
        CHECK (id ~ '^prv_[0-9a-f]{32}$'),
    CONSTRAINT github_pull_request_previews_project_workspace_fk
        FOREIGN KEY (project_id, workspace_id)
        REFERENCES projects (id, workspace_id)
        ON DELETE RESTRICT,
    CONSTRAINT github_pull_request_previews_service_project_fk
        FOREIGN KEY (service_id, project_id)
        REFERENCES services (id, project_id)
        ON DELETE RESTRICT,
    CONSTRAINT github_pull_request_previews_environment_project_fk
        FOREIGN KEY (environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE RESTRICT,
    CONSTRAINT github_pull_request_previews_source_environment_project_fk
        FOREIGN KEY (source_environment_id, project_id)
        REFERENCES environments (id, project_id)
        ON DELETE RESTRICT,
    CONSTRAINT github_pull_request_previews_deployment_scope_fk
        FOREIGN KEY (deployment_id, project_id, service_id, environment_id)
        REFERENCES deployments (id, project_id, service_id, environment_id)
        ON DELETE SET NULL (deployment_id),
    CONSTRAINT github_pull_request_previews_source_chk
        CHECK (
            installation_id > 0
            AND installation_grant_resource_version > 0
            AND repository_id > 0
            AND repository_grant_resource_version > 0
            AND link_resource_version > 0
            AND repository_full_name ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
            AND clone_url = 'https://github.com/' || repository_full_name || '.git'
            AND pull_request_number > 0
            AND BTRIM(base_branch) <> ''
            AND BTRIM(head_branch) <> ''
            AND head_sha ~ '^[0-9a-f]{40}$'
        ),
    CONSTRAINT github_pull_request_previews_isolation_chk
        CHECK (
            (from_fork AND secret_policy = 'no-shared-secrets'
                AND cache_policy = 'ephemeral' AND quota_profile = 'preview-fork-restricted')
            OR
            (NOT from_fork AND secret_policy = 'non-sealed'
                AND cache_policy = 'service-preview' AND quota_profile = 'preview-default')
        ),
    CONSTRAINT github_pull_request_previews_state_chk
        CHECK (state IN ('creating','deploying','ready','refreshing','deleting','deleted','failed')),
    CONSTRAINT github_pull_request_previews_lifecycle_chk
        CHECK (
            expires_at > created_at
            AND expires_at <= created_at + INTERVAL '30 days'
            AND updated_at >= created_at
            AND (delete_after IS NULL OR delete_after >= updated_at)
            AND (closed_at IS NULL OR closed_at >= created_at)
            AND ((state IN ('deleting','deleted')) = (closed_at IS NOT NULL))
            AND ((state = 'deleted') = (delete_after IS NOT NULL))
        ),
    CONSTRAINT github_pull_request_previews_comment_chk
        CHECK (last_comment_id IS NULL OR last_comment_id > 0)
);

CREATE INDEX github_pull_request_previews_service_idx
    ON github_pull_request_previews (service_id, updated_at DESC, id)
    WHERE state <> 'deleted';

CREATE INDEX github_pull_request_previews_due_idx
    ON github_pull_request_previews (expires_at, updated_at, id)
    WHERE state NOT IN ('deleting','deleted');

CREATE INDEX github_pull_request_previews_cleanup_idx
    ON github_pull_request_previews (state, updated_at, id)
    WHERE state = 'deleting';

-- Comment publication is an independent retry boundary. A rate limit or
-- missing App permission must never roll back preview creation or cleanup.
CREATE TABLE github_pull_request_preview_comments (
    id TEXT PRIMARY KEY,
    preview_id TEXT NOT NULL REFERENCES github_pull_request_previews(id) ON DELETE CASCADE,
    dedupe_key TEXT NOT NULL UNIQUE,
    installation_id BIGINT NOT NULL,
    repository_id BIGINT NOT NULL,
    repository_full_name TEXT NOT NULL,
    pull_request_number INTEGER NOT NULL,
    preview_state TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    deployment_id TEXT,
    preview_url TEXT NOT NULL DEFAULT '',
    delivery_state TEXT NOT NULL DEFAULT 'pending',
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 8,
    available_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    posted_at TIMESTAMPTZ,
    terminal_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT github_pull_request_preview_comments_id_chk
        CHECK (id ~ '^ghc_[0-9a-f]{32}$'),
    CONSTRAINT github_pull_request_preview_comments_source_chk
        CHECK (
            installation_id > 0
            AND repository_id > 0
            AND repository_full_name ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
            AND pull_request_number > 0
            AND head_sha ~ '^[0-9a-f]{40}$'
        ),
    CONSTRAINT github_pull_request_preview_comments_state_chk
        CHECK (preview_state IN ('deploying','ready','refreshing','deleting','deleted','failed')),
    CONSTRAINT github_pull_request_preview_comments_delivery_chk
        CHECK (delivery_state IN ('pending','delivering','posted','dead_letter')),
    CONSTRAINT github_pull_request_preview_comments_attempt_chk
        CHECK (attempt >= 0 AND max_attempts BETWEEN 1 AND 64),
    CONSTRAINT github_pull_request_preview_comments_lease_chk
        CHECK (
            (delivery_state = 'delivering' AND lease_token IS NOT NULL
                AND BTRIM(lease_token) <> '' AND lease_expires_at IS NOT NULL)
            OR
            (delivery_state <> 'delivering' AND lease_token IS NULL AND lease_expires_at IS NULL)
        ),
    CONSTRAINT github_pull_request_preview_comments_terminal_chk
        CHECK (
            (delivery_state IN ('posted','dead_letter') AND terminal_at IS NOT NULL)
            OR
            (delivery_state NOT IN ('posted','dead_letter') AND terminal_at IS NULL)
        ),
    CONSTRAINT github_pull_request_preview_comments_posted_chk
        CHECK (
            (delivery_state = 'posted' AND posted_at IS NOT NULL)
            OR
            (delivery_state <> 'posted' AND posted_at IS NULL)
        ),
    CONSTRAINT github_pull_request_preview_comments_timestamps_chk
        CHECK (
            available_at >= created_at
            AND updated_at >= created_at
            AND (lease_expires_at IS NULL OR lease_expires_at > updated_at)
            AND (posted_at IS NULL OR posted_at >= created_at)
            AND (terminal_at IS NULL OR terminal_at >= created_at)
        ),
    CONSTRAINT github_pull_request_preview_comments_error_chk
        CHECK (last_error !~ '[[:cntrl:]]' AND CHAR_LENGTH(last_error) <= 512)
);

CREATE INDEX github_pull_request_preview_comments_due_idx
    ON github_pull_request_preview_comments (available_at, created_at, id)
    WHERE delivery_state IN ('pending','delivering');

CREATE OR REPLACE FUNCTION midnight_enqueue_preview_deployment_comment()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    next_preview_state TEXT;
    event_time TIMESTAMPTZ;
BEGIN
    IF OLD.state IS NOT DISTINCT FROM NEW.state THEN
        RETURN NEW;
    END IF;
    CASE NEW.state
        WHEN 'active' THEN next_preview_state := 'ready';
        WHEN 'failed' THEN next_preview_state := 'failed';
        WHEN 'canceled' THEN next_preview_state := 'failed';
        ELSE RETURN NEW;
    END CASE;
    event_time := COALESCE(NEW.completed_at, NEW.updated_at, CURRENT_TIMESTAMP);

    WITH updated_preview AS (
        UPDATE github_pull_request_previews preview
           SET state=next_preview_state,
               updated_at=event_time,
               resource_version=resource_version+1
         WHERE preview.deployment_id=NEW.id
           AND preview.state IN ('deploying','refreshing')
        RETURNING preview.*
    )
    INSERT INTO github_pull_request_preview_comments (
        id,preview_id,dedupe_key,installation_id,repository_id,
        repository_full_name,pull_request_number,preview_state,head_sha,
        deployment_id,preview_url,available_at,created_at,updated_at
    )
    SELECT
        'ghc_' || md5(preview.id || ':' || NEW.id || ':' || next_preview_state),
        preview.id,
        preview.id || ':' || NEW.id || ':' || next_preview_state,
        preview.installation_id,preview.repository_id,preview.repository_full_name,
        preview.pull_request_number,next_preview_state,preview.head_sha,
        NEW.id,preview.preview_url,event_time,event_time,event_time
      FROM updated_preview preview
    ON CONFLICT (dedupe_key) DO NOTHING;

    RETURN NEW;
END
$$;

CREATE TRIGGER trg_enqueue_preview_deployment_comment
AFTER UPDATE OF state ON deployments
FOR EACH ROW
EXECUTE FUNCTION midnight_enqueue_preview_deployment_comment();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (65, 'github_pull_request_previews', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
