-- 0016_deployment_fsm_trigger.up.sql — enforce the deployment FSM in the DB.
--
-- GATED / STANDALONE: attaches the enforcing trigger for the canonical
-- deployment finite state machine defined (inert) in 0014. Once attached, the
-- database rejects: inserts in any state other than 'queued', illegal state
-- transitions, and mutation of a set image_ref/image_digest. This is the
-- write-behavior-changing step — apply it only AFTER every deployment-state
-- writer has been audited/adapted (done: intake dual-write, worker reconcile,
-- redeploy/rollback/restart mirror — all go through the store's queued-insert +
-- CAS transitions) and after the historical backfill (0015), which inserts
-- terminal rows directly and therefore MUST run before this trigger exists.
-- Rehearse on a prod clone; any out-of-order writer will start erroring.
--
-- The validate_deployment_contract() function itself already exists (0014); this
-- migration only binds it as a row trigger.
--
-- Schema bumps to schema_version = 16.

CREATE TRIGGER trg_validate_deployment_contract
    BEFORE INSERT OR UPDATE ON deployments
    FOR EACH ROW EXECUTE FUNCTION validate_deployment_contract();

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (16, 'deployment_fsm_trigger', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
