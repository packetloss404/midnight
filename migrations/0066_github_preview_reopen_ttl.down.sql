DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM github_pull_request_previews
         WHERE expires_at > created_at + INTERVAL '30 days'
    ) THEN
        RAISE EXCEPTION
            'cannot restore the original preview TTL constraint while a reopened preview exceeds it';
    END IF;
END
$$;

ALTER TABLE github_pull_request_previews
    DROP CONSTRAINT github_pull_request_previews_lifecycle_chk;

ALTER TABLE github_pull_request_previews
    ADD CONSTRAINT github_pull_request_previews_lifecycle_chk
    CHECK (
        expires_at > created_at
        AND expires_at <= created_at + INTERVAL '30 days'
        AND updated_at >= created_at
        AND (delete_after IS NULL OR delete_after >= updated_at)
        AND (closed_at IS NULL OR closed_at >= created_at)
        AND ((state IN ('deleting','deleted')) = (closed_at IS NOT NULL))
        AND ((state = 'deleted') = (delete_after IS NOT NULL))
    );

DELETE FROM schema_migrations
 WHERE version = 66 AND name = 'github_preview_reopen_ttl';
