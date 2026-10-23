DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM github_app_setup_states
        WHERE consumed_at IS NULL
          AND expires_at > CURRENT_TIMESTAMP
        LIMIT 1
    ) THEN
        RAISE EXCEPTION
            'cannot downgrade github_app_setup_states while a live installation setup is pending';
    END IF;
END
$$;

DROP INDEX IF EXISTS github_app_setup_states_actor_idx;
DROP INDEX IF EXISTS github_app_setup_states_expiry_idx;
DROP TABLE github_app_setup_states;

DELETE FROM schema_migrations
 WHERE version = 64 AND name = 'github_app_setup_states';
