DROP TABLE IF EXISTS deployment_predeploy_runs;

DELETE FROM schema_migrations WHERE version = 30;
