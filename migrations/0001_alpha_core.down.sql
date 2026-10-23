-- Rollback for the alpha_core migration. Order matters: drop dependents
-- before parents.

DROP INDEX IF EXISTS idx_operations_target;
DROP INDEX IF EXISTS idx_operations_state;
DROP INDEX IF EXISTS idx_jobs_queue_state;

DROP TABLE IF EXISTS audit_checkpoints;
DROP TABLE IF EXISTS audit_logs;

DROP INDEX IF EXISTS idx_timeline_events_type;
DROP INDEX IF EXISTS idx_timeline_events_deployment;
DROP TABLE IF EXISTS timeline_events;

DROP INDEX IF EXISTS idx_host_heartbeats_observed_at;
DROP TABLE IF EXISTS host_heartbeats;
DROP TABLE IF EXISTS hosts;

DROP INDEX IF EXISTS idx_services_project;
DROP TABLE IF EXISTS services;

DROP INDEX IF EXISTS idx_environments_project;
DROP TABLE IF EXISTS environments;

DROP TABLE IF EXISTS projects;

DROP INDEX IF EXISTS idx_api_tokens_token_class;
DROP TABLE IF EXISTS api_tokens;

DROP INDEX IF EXISTS idx_idempotency_keys_expiry;
DROP INDEX IF EXISTS idx_idempotency_keys_operation_id;
DROP TABLE IF EXISTS idempotency_keys;

DROP INDEX IF EXISTS jobs_partition_key_idx;
DROP INDEX IF EXISTS jobs_reaper_idx;
DROP INDEX IF EXISTS jobs_dequeue_idx;
DROP INDEX IF EXISTS idx_jobs_operation_id;
DROP TABLE IF EXISTS jobs;

DROP TABLE IF EXISTS operation_events;
DROP TABLE IF EXISTS operations;

DROP TABLE IF EXISTS schema_migrations;
