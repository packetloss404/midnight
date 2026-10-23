-- 0061 — durable GitHub deployment-status delivery with atomic terminal
-- enqueueing, expiring leases, bounded retries, and dead-letter evidence.

CREATE TABLE github_commit_statuses (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    provider_host TEXT NOT NULL,
    installation_id BIGINT NOT NULL,
    repository_id BIGINT NOT NULL,
    repo_full_name TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    state TEXT NOT NULL,
    context TEXT NOT NULL DEFAULT 'midnight/deploy',
    target_url TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
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
    CONSTRAINT github_commit_statuses_deployment_state_uniq
        UNIQUE (deployment_id, state, context),
    CONSTRAINT github_commit_statuses_id_chk
        CHECK (id ~ '^ghs_[0-9a-f]{32}$'),
    CONSTRAINT github_commit_statuses_provider_chk
        CHECK (
            provider_host = LOWER(provider_host)
            AND provider_host ~ '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$'
            AND CHAR_LENGTH(provider_host) <= 255
        ),
    CONSTRAINT github_commit_statuses_source_chk
        CHECK (
            installation_id > 0
            AND repository_id > 0
            AND repo_full_name ~ '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'
            AND CHAR_LENGTH(repo_full_name) <= 255
            AND commit_sha ~ '^([a-f0-9]{40}|[a-f0-9]{64})$'
        ),
    CONSTRAINT github_commit_statuses_metadata_chk
        CHECK (
            state IN ('pending', 'success', 'error', 'failure')
            AND BTRIM(context) <> ''
            AND context !~ '[[:cntrl:]]'
            AND CHAR_LENGTH(context) <= 100
            AND target_url ~ '^https?://[^[:space:]@]+$'
            AND CHAR_LENGTH(target_url) <= 2048
            AND description !~ '[[:cntrl:]]'
            AND CHAR_LENGTH(description) <= 140
        ),
    CONSTRAINT github_commit_statuses_delivery_state_chk
        CHECK (delivery_state IN ('pending', 'delivering', 'posted', 'dead_letter')),
    CONSTRAINT github_commit_statuses_attempts_chk
        CHECK (attempt >= 0 AND max_attempts BETWEEN 1 AND 64),
    CONSTRAINT github_commit_statuses_lease_chk
        CHECK (
            (
                delivery_state = 'delivering'
                AND lease_token IS NOT NULL
                AND BTRIM(lease_token) <> ''
                AND CHAR_LENGTH(lease_token) <= 255
                AND lease_expires_at IS NOT NULL
            )
            OR (
                delivery_state <> 'delivering'
                AND lease_token IS NULL
                AND lease_expires_at IS NULL
            )
        ),
    CONSTRAINT github_commit_statuses_terminal_chk
        CHECK (
            (delivery_state IN ('posted', 'dead_letter') AND terminal_at IS NOT NULL)
            OR (delivery_state NOT IN ('posted', 'dead_letter') AND terminal_at IS NULL)
        ),
    CONSTRAINT github_commit_statuses_posted_chk
        CHECK (
            (delivery_state = 'posted' AND posted_at IS NOT NULL)
            OR (delivery_state <> 'posted' AND posted_at IS NULL)
        ),
    CONSTRAINT github_commit_statuses_timestamps_chk
        CHECK (
            available_at >= created_at
            AND updated_at >= created_at
            AND (lease_expires_at IS NULL OR lease_expires_at > updated_at)
            AND (posted_at IS NULL OR posted_at >= created_at)
            AND (terminal_at IS NULL OR terminal_at >= created_at)
        ),
    CONSTRAINT github_commit_statuses_error_chk
        CHECK (last_error !~ '[[:cntrl:]]' AND CHAR_LENGTH(last_error) <= 512)
);

CREATE INDEX github_commit_statuses_due_idx
    ON github_commit_statuses (available_at, created_at, id)
    WHERE delivery_state IN ('pending', 'delivering');

CREATE INDEX github_commit_statuses_deployment_idx
    ON github_commit_statuses (deployment_id, created_at, id);

CREATE INDEX github_commit_statuses_dead_letter_idx
    ON github_commit_statuses (terminal_at DESC, id)
    WHERE delivery_state = 'dead_letter';

CREATE OR REPLACE FUNCTION midnight_enqueue_terminal_github_commit_status()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    github_state TEXT;
    github_description TEXT;
    event_at TIMESTAMPTZ;
BEGIN
    IF OLD.state IS NOT DISTINCT FROM NEW.state THEN
        RETURN NEW;
    END IF;

    CASE NEW.state
        WHEN 'active' THEN
            github_state := 'success';
            github_description := 'Deployment succeeded';
        WHEN 'failed' THEN
            github_state := 'failure';
            github_description := 'Deployment failed';
        WHEN 'canceled' THEN
            github_state := 'error';
            github_description := 'Deployment canceled';
        ELSE
            RETURN NEW;
    END CASE;
    event_at := COALESCE(NEW.completed_at, NEW.updated_at, CURRENT_TIMESTAMP);

    INSERT INTO github_commit_statuses (
        id, deployment_id, provider_host, installation_id, repository_id,
        repo_full_name, commit_sha, state, context, target_url, description,
        delivery_state, attempt, max_attempts, available_at, created_at, updated_at
    )
    SELECT
        'ghs_' || md5(NEW.id || ':' || github_state || ':' || source.context),
        NEW.id, source.provider_host, source.installation_id,
        source.repository_id, source.repo_full_name, source.commit_sha,
        github_state, source.context, source.target_url, github_description,
        'pending', 0, source.max_attempts, event_at, event_at, event_at
      FROM github_commit_statuses source
     WHERE source.deployment_id = NEW.id
       AND source.state = 'pending'
    ON CONFLICT (deployment_id, state, context) DO NOTHING;

    RETURN NEW;
END
$$;

CREATE TRIGGER trg_enqueue_terminal_github_commit_status
AFTER UPDATE OF state ON deployments
FOR EACH ROW
EXECUTE FUNCTION midnight_enqueue_terminal_github_commit_status();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (61, 'github_commit_status_outbox', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
