-- Phase 1 / Lane 5 (WIREUP): drop webhook_deliveries dedupe table
-- introduced by 0005_webhook_deliveries.up.sql.

DROP INDEX IF EXISTS webhook_deliveries_outcome_idx;
DROP INDEX IF EXISTS webhook_deliveries_received_at_idx;
DROP TABLE IF EXISTS webhook_deliveries;
