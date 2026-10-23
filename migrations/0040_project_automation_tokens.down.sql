-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Token history is security and revocation evidence. Refuse a lossy rollback
-- instead of silently deleting it or making a formerly revoked credential
-- impossible to account for.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM project_automation_tokens) THEN
        RAISE EXCEPTION
            'cannot roll back project_automation_tokens while token history exists';
    END IF;
END
$$;

DROP INDEX IF EXISTS project_automation_tokens_active_expiry_idx;
DROP INDEX IF EXISTS project_automation_tokens_project_created_idx;
DROP TABLE project_automation_tokens;

ALTER TABLE service_accounts
    DROP CONSTRAINT service_accounts_id_workspace_uniq;
