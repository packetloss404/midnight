-- 0009_queue_parent_operation_id.down.sql

DROP INDEX IF EXISTS idx_jobs_parent_operation_id;
ALTER TABLE jobs DROP COLUMN IF EXISTS parent_operation_id;

DELETE FROM schema_migrations WHERE version = 9;
