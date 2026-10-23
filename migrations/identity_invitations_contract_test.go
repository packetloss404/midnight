// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestIdentityInvitationsMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0041_identity_invitations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	required := []string{
		"CREATE TABLE identity_invitations",
		"token_hash BYTEA NOT NULL UNIQUE",
		"CHECK (OCTET_LENGTH(token_hash) = 32)",
		"identity_invitations_scope_role_chk",
		"expires_at >= created_at + INTERVAL '5 minutes'",
		"expires_at <= created_at + INTERVAL '30 days'",
		"identity_invitations_terminal_state_chk",
		"identity_invitations_revocation_audit_chk",
		"identity_invitations_acceptance_audit_chk",
		"FOREIGN KEY (project_id, workspace_id)",
		"REFERENCES projects (id, workspace_id)",
		"invited_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT",
		"identity_invitations_pending_lookup_idx",
	}
	for _, fragment := range required {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"invitation_token TEXT",
		"raw_token",
		"token_secret",
		"ON DELETE CASCADE",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("invitation migration contains unsafe fragment %q", forbidden)
		}
	}

	downBytes, err := fs.ReadFile(FS, "0041_identity_invitations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "IF EXISTS (SELECT 1 FROM identity_invitations)") ||
		!strings.Contains(down, "cannot roll back identity_invitations while invitation history exists") {
		t.Fatal("down migration does not refuse invitation-history loss")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 41\n    name: identity_invitations") {
		t.Fatal("migration 0041 is not registered")
	}
}
