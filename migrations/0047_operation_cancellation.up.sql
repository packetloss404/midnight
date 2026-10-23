-- 0047_operation_cancellation.up.sql
--
-- A user cancellation must be able to terminalize durable work before an
-- outbox dispatcher publishes it to the worker queue. Canceled is distinct
-- from failed: dispatchers ignore it, operators can explain the terminal
-- reason accurately, and rollback does not need to delete delivery history.

ALTER TABLE deploy_intake_outbox
    DROP CONSTRAINT deploy_intake_outbox_state_chk;

ALTER TABLE deploy_intake_outbox
    ADD CONSTRAINT deploy_intake_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed','canceled'));

ALTER TABLE staged_work_outbox
    DROP CONSTRAINT staged_work_outbox_state_chk;

ALTER TABLE staged_work_outbox
    ADD CONSTRAINT staged_work_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed','canceled'));

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (47, 'operation_cancellation', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
