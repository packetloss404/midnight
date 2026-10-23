DROP INDEX IF EXISTS idx_environment_locks_active;
CREATE INDEX idx_environment_locks_active
    ON environment_locks (service_id, environment_id)
    WHERE released_at IS NULL;
