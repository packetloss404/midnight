-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Invitation history is security and membership-attribution evidence. Refuse
-- a lossy rollback instead of silently deleting it.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM identity_invitations) THEN
        RAISE EXCEPTION
            'cannot roll back identity_invitations while invitation history exists';
    END IF;
END
$$;

DROP INDEX IF EXISTS identity_invitations_pending_expiry_idx;
DROP INDEX IF EXISTS identity_invitations_pending_lookup_idx;
DROP INDEX IF EXISTS identity_invitations_project_created_idx;
DROP INDEX IF EXISTS identity_invitations_workspace_created_idx;
DROP TABLE identity_invitations;
