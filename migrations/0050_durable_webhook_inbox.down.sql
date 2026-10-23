-- Restore the exact migration-0005 delivery table and its preserved rows.
-- Deliveries accepted by schema 0050 cannot be represented safely without
-- their payload/state machine. Refuse to erase acknowledged work implicitly;
-- an operator must drain/archive it through a separately explicit destructive
-- recovery procedure before schema rollback.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM webhook_deliveries LIMIT 1) THEN
        RAISE EXCEPTION
            'migration 0050 rollback refused: durable webhook deliveries exist';
    END IF;
END
$$;

DROP TABLE IF EXISTS webhook_delivery_targets;

DROP INDEX IF EXISTS webhook_inbox_terminal_idx;
DROP INDEX IF EXISTS webhook_inbox_endpoint_received_idx;
DROP INDEX IF EXISTS webhook_inbox_due_idx;
DROP TABLE IF EXISTS webhook_deliveries;

ALTER TABLE webhook_deliveries_v5_history
    RENAME TO webhook_deliveries;

DELETE FROM schema_migrations
WHERE version = 50;
