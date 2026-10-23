-- 0047_operation_cancellation.down.sql

ALTER TABLE deploy_intake_outbox
    DROP CONSTRAINT deploy_intake_outbox_state_chk;

UPDATE deploy_intake_outbox
   SET state='failed',
       last_error=CASE
           WHEN last_error='' THEN 'canceled before migration rollback'
           ELSE last_error
       END,
       lease_owner=NULL,
       lease_expires_at=NULL,
       updated_at=CURRENT_TIMESTAMP
 WHERE state='canceled';

ALTER TABLE deploy_intake_outbox
    ADD CONSTRAINT deploy_intake_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed'));

ALTER TABLE staged_work_outbox
    DROP CONSTRAINT staged_work_outbox_state_chk;

UPDATE staged_work_outbox
   SET state='failed',
       last_error=CASE
           WHEN last_error='' THEN 'canceled before migration rollback'
           ELSE last_error
       END,
       lease_owner=NULL,
       lease_expires_at=NULL,
       updated_at=CURRENT_TIMESTAMP
 WHERE state='canceled';

ALTER TABLE staged_work_outbox
    ADD CONSTRAINT staged_work_outbox_state_chk
        CHECK (state IN ('pending','delivering','delivered','failed'));

DELETE FROM schema_migrations
 WHERE version=47
   AND name='operation_cancellation';
