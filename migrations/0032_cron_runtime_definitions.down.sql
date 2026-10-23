DROP TABLE IF EXISTS cron_occurrence_runs;
DROP TABLE IF EXISTS cron_schedule_cursors;
ALTER TABLE service_environment_configs
    DROP CONSTRAINT IF EXISTS service_environment_configs_cron_shape_chk,
    DROP COLUMN IF EXISTS cron_retry_delay_seconds,
    DROP COLUMN IF EXISTS cron_max_attempts,
    DROP COLUMN IF EXISTS cron_timeout_seconds,
    DROP COLUMN IF EXISTS cron_schedule,
    DROP COLUMN IF EXISTS runtime_kind;
