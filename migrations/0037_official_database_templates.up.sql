-- 0037 — verified runtime provisioning for all official single-node databases.
--
-- PostgreSQL established the durable intake and restart-persistence evidence
-- contract. Redis, MySQL, and MongoDB now use that same transaction and proof
-- record; no engine may reach succeeded without connectivity, replacement,
-- persistence, and cleanup evidence.

ALTER TABLE template_provision_runs
    DROP CONSTRAINT IF EXISTS template_provision_runs_template_chk;

ALTER TABLE template_provision_runs
    ADD CONSTRAINT template_provision_runs_template_chk
    CHECK (template_slug IN ('postgres', 'redis', 'mysql', 'mongodb'));

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (37, 'official_database_templates', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
