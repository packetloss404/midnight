-- Align the audit_logs / audit_checkpoints schema with the audit Row +
-- Checkpoint shapes so the Postgres-backed audit.AuditChain in
-- internal/audit/chain_pg.go round-trips every integrity-protected field
-- cleanly.
--
-- The baseline schema in 0001_alpha_core.up.sql created the minimum columns;
-- the audit Row schema additionally includes trace_id, source_ip, and
-- user_agent. The canonical_row encoder hashes these fields, so a write that
-- populates them MUST persist them or the verifier fails after a restart.
--
-- CheckpointSigningInput in internal/audit/keys.go also signs over
-- partition_label, so audit_checkpoints carries that column explicitly.

ALTER TABLE audit_logs
    ADD COLUMN IF NOT EXISTS trace_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_logs
    ADD COLUMN IF NOT EXISTS source_ip  TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_logs
    ADD COLUMN IF NOT EXISTS user_agent TEXT NOT NULL DEFAULT '';

ALTER TABLE audit_checkpoints
    ADD COLUMN IF NOT EXISTS partition_label TEXT NOT NULL DEFAULT '';
