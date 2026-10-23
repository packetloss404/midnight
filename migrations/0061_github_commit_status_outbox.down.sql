DROP TRIGGER IF EXISTS trg_enqueue_terminal_github_commit_status ON deployments;
DROP FUNCTION IF EXISTS midnight_enqueue_terminal_github_commit_status();
DROP TABLE IF EXISTS github_commit_statuses;

DELETE FROM schema_migrations
WHERE version = 61 AND name = 'github_commit_status_outbox';
