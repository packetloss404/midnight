-- 0020 — preserve the complete metadata-only variable view across restarts.
--
-- Ciphertext and wrapped DEKs already live in service_variables. These two
-- redaction-safe fields complete VariableMeta persistence without adding any
-- plaintext value column.

ALTER TABLE service_variables
    ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';

ALTER TABLE service_variables
    ADD COLUMN IF NOT EXISTS kek_fingerprint TEXT NOT NULL DEFAULT '';

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (20, 'service_variable_metadata', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
