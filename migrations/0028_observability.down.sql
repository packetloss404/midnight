DROP TABLE IF EXISTS observability_deployment_markers;
DROP TABLE IF EXISTS observability_metric_samples;
DROP TABLE IF EXISTS observability_collectors;
DROP TABLE IF EXISTS observability_collector_checkpoints;
DROP TABLE IF EXISTS observability_ingestion_windows;
DROP TABLE IF EXISTS observability_log_records;

DELETE FROM schema_migrations WHERE version = 28 AND name = 'observability';
