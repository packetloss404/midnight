-- Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
--
-- Records an immutable, one-to-one replacement lineage. The unique link
-- prevents an old invitation from branching into multiple replacement
-- credentials after the first replacement becomes terminal.

ALTER TABLE identity_invitations
    ADD COLUMN replaces_invitation_id TEXT,
    ADD CONSTRAINT identity_invitations_replaces_not_self_chk
        CHECK (
            replaces_invitation_id IS NULL
            OR replaces_invitation_id <> id
        ),
    ADD CONSTRAINT identity_invitations_replaces_fk
        FOREIGN KEY (replaces_invitation_id)
        REFERENCES identity_invitations (id)
        ON DELETE RESTRICT;

CREATE UNIQUE INDEX identity_invitations_replaces_uniq
    ON identity_invitations (replaces_invitation_id)
    WHERE replaces_invitation_id IS NOT NULL;
