-- 0066 — allow a closed PR to be reopened after the original 30-day window.
--
-- The lifecycle coordinator owns the bounded seven-day idle TTL and refreshes
-- it on each synchronize event. Tying expiry to the row's original creation
-- time made a legitimate old PR impossible to reopen.

ALTER TABLE github_pull_request_previews
    DROP CONSTRAINT github_pull_request_previews_lifecycle_chk;

ALTER TABLE github_pull_request_previews
    ADD CONSTRAINT github_pull_request_previews_lifecycle_chk
    CHECK (
        expires_at > created_at
        AND updated_at >= created_at
        AND (delete_after IS NULL OR delete_after >= updated_at)
        AND (closed_at IS NULL OR closed_at >= created_at)
        AND ((state IN ('deleting','deleted')) = (closed_at IS NOT NULL))
        AND ((state = 'deleted') = (delete_after IS NOT NULL))
    );

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (66, 'github_preview_reopen_ttl', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
