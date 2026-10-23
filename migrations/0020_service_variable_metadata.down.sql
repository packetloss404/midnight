-- 0020 — rollback redaction-safe service variable metadata fields.

ALTER TABLE service_variables
    DROP COLUMN IF EXISTS kek_fingerprint;

ALTER TABLE service_variables
    DROP COLUMN IF EXISTS workspace_id;

DELETE FROM schema_migrations WHERE version = 20;
