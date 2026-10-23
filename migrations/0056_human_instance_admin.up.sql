-- 0056 — make the existing users.is_admin authority available to human
-- sessions and promote the claimed installation owner on upgraded systems.
--
-- Migration 0038 kept the disabled migration sentinel as an admin and created
-- the first real claimed owner with is_admin=FALSE. That made normal human
-- sessions unable to administer the installation even though the schema
-- already had an installation-wide role. Promote exactly one deterministic,
-- active administrator of the default workspace only when no active instance
-- administrator already exists. Additional administrators remain an explicit
-- operator decision.
WITH candidate AS (
    SELECT wm.user_id
    FROM workspace_members wm
    JOIN users u ON u.id = wm.user_id
    WHERE wm.workspace_id = 'wrk_default'
      AND wm.role = 'admin'
      AND wm.accepted_at IS NOT NULL
      AND u.deleted_at IS NULL
      AND u.is_disabled = FALSE
      AND u.id <> 'usr_migration_default'
    ORDER BY wm.accepted_at ASC, u.created_at ASC, u.id ASC
    LIMIT 1
), no_active_admin AS (
    SELECT NOT EXISTS (
        SELECT 1
        FROM users
        WHERE is_admin = TRUE
          AND is_disabled = FALSE
          AND deleted_at IS NULL
    ) AS value
)
UPDATE users u
SET is_admin = TRUE,
    updated_at = CURRENT_TIMESTAMP,
    resource_version = resource_version + 1
FROM candidate, no_active_admin
WHERE no_active_admin.value
  AND u.id = candidate.user_id;

INSERT INTO schema_migrations(version, name, applied_at)
VALUES (56, 'human_instance_admin', CURRENT_TIMESTAMP)
ON CONFLICT (version) DO NOTHING;
