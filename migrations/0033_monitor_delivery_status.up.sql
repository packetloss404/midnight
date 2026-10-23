-- 0033_monitor_delivery_status.up.sql — retain safe manual retry history.

ALTER TABLE monitor_delivery_outbox
    ADD COLUMN IF NOT EXISTS manual_retries INTEGER NOT NULL DEFAULT 0;

ALTER TABLE monitor_delivery_outbox
    DROP CONSTRAINT IF EXISTS monitor_delivery_manual_retries_chk;

ALTER TABLE monitor_delivery_outbox
    ADD CONSTRAINT monitor_delivery_manual_retries_chk
    CHECK (manual_retries >= 0);

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (33, 'monitor_delivery_status', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
