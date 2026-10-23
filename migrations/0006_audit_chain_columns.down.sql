-- Phase 1 / Lane 5 (WIREUP): rollback the audit chain column additions.

ALTER TABLE audit_checkpoints
    DROP COLUMN IF EXISTS partition_label;

ALTER TABLE audit_logs
    DROP COLUMN IF EXISTS user_agent;
ALTER TABLE audit_logs
    DROP COLUMN IF EXISTS source_ip;
ALTER TABLE audit_logs
    DROP COLUMN IF EXISTS trace_id;
