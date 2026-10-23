-- 0046_source_artifact_upload_quotas.down.sql

DROP INDEX IF EXISTS source_artifact_refs_project_digest_idx;
DROP INDEX IF EXISTS source_artifact_refs_digest_idx;
DROP TABLE IF EXISTS source_artifact_upload_reservations;
DROP TABLE IF EXISTS source_artifact_workspace_charges;
DROP TABLE IF EXISTS source_artifact_workspace_quota_state;

ALTER TABLE auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_id_user_uniq;

DELETE FROM schema_migrations
WHERE version = 46
  AND name = 'source_artifact_upload_quotas';
