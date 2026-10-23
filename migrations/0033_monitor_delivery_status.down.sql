ALTER TABLE monitor_delivery_outbox
    DROP CONSTRAINT IF EXISTS monitor_delivery_manual_retries_chk;

ALTER TABLE monitor_delivery_outbox
    DROP COLUMN IF EXISTS manual_retries;

DELETE FROM schema_migrations
WHERE version = 33 AND name = 'monitor_delivery_status';
