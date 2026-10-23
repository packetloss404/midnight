DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM github_pull_request_previews
         WHERE state <> 'deleted'
         LIMIT 1
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade github_pull_request_previews while a preview lifecycle is active';
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_enqueue_preview_deployment_comment ON deployments;
DROP FUNCTION IF EXISTS midnight_enqueue_preview_deployment_comment();
DROP INDEX IF EXISTS github_pull_request_preview_comments_due_idx;
DROP TABLE github_pull_request_preview_comments;
DROP INDEX IF EXISTS github_pull_request_previews_cleanup_idx;
DROP INDEX IF EXISTS github_pull_request_previews_due_idx;
DROP INDEX IF EXISTS github_pull_request_previews_service_idx;
DROP TABLE github_pull_request_previews;

DELETE FROM schema_migrations
 WHERE version = 65 AND name = 'github_pull_request_previews';
