// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestIdentityInvitationReplacementsMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0043_identity_invitation_replacements.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ADD COLUMN replaces_invitation_id TEXT",
		"identity_invitations_replaces_not_self_chk",
		"FOREIGN KEY (replaces_invitation_id)",
		"REFERENCES identity_invitations (id)",
		"ON DELETE RESTRICT",
		"CREATE UNIQUE INDEX identity_invitations_replaces_uniq",
		"WHERE replaces_invitation_id IS NOT NULL",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}

	downBytes, err := fs.ReadFile(FS, "0043_identity_invitation_replacements.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "WHERE replaces_invitation_id IS NOT NULL") ||
		!strings.Contains(down, "cannot roll back identity invitation replacements while replacement history exists") {
		t.Fatal("down migration does not preserve replacement history")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 43\n    name: identity_invitation_replacements") {
		t.Fatal("migration 0043 is not registered")
	}
}
