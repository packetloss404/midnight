DROP TABLE IF EXISTS deploy_intake_outbox;
DROP INDEX IF EXISTS jobs_outbox_id_uq;
ALTER TABLE jobs DROP COLUMN IF EXISTS outbox_id;
