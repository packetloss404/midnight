-- 0015_backfill_deployments.down.sql — remove backfilled deployment rows.
-- Only deletes rows this backfill created (id prefix dep_bf_); dual-write and
-- runtime rows are untouched.

DELETE FROM deployments WHERE id LIKE 'dep\_bf\_%';

DELETE FROM schema_migrations WHERE version = 15;
