-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Replacement lineage is security and credential-invalidation evidence.
-- Refuse a lossy rollback once any invitation has been reissued.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM identity_invitations
        WHERE replaces_invitation_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'cannot roll back identity invitation replacements while replacement history exists';
    END IF;
END
$$;

DROP INDEX IF EXISTS identity_invitations_replaces_uniq;

ALTER TABLE identity_invitations
    DROP CONSTRAINT identity_invitations_replaces_fk,
    DROP CONSTRAINT identity_invitations_replaces_not_self_chk,
    DROP COLUMN replaces_invitation_id;
