-- Make the durable environment lock enforce one active row per
-- (service, environment). PostgreSQL UNIQUE constraints treat NULL values as
-- distinct, so the original UNIQUE(service_id, environment_id, released_at)
-- declaration did not protect unreleased rows.

WITH ranked_active_locks AS (
    SELECT
        id,
        ROW_NUMBER() OVER (
            PARTITION BY service_id, environment_id
            ORDER BY held_since DESC, id DESC
        ) AS active_rank
    FROM environment_locks
    WHERE released_at IS NULL
)
UPDATE environment_locks
SET released_at = CURRENT_TIMESTAMP
WHERE id IN (
    SELECT id
    FROM ranked_active_locks
    WHERE active_rank > 1
);

DROP INDEX IF EXISTS idx_environment_locks_active;
CREATE UNIQUE INDEX idx_environment_locks_active
    ON environment_locks (service_id, environment_id)
    WHERE released_at IS NULL;
