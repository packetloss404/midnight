// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestProjectAutomationTokenMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0040_project_automation_tokens.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	required := []string{
		"CREATE TABLE project_automation_tokens",
		"token_hash BYTEA NOT NULL UNIQUE",
		"expires_at TIMESTAMPTZ NOT NULL",
		"FOREIGN KEY (service_account_id, workspace_id)",
		"REFERENCES service_accounts (id, workspace_id)",
		"FOREIGN KEY (project_id, workspace_id)",
		"REFERENCES projects (id, workspace_id)",
		"CHECK (OCTET_LENGTH(token_hash) = 32)",
		"CHECK (profile = 'ci-deploy-v1')",
		"project_automation_tokens_revocation_chk",
	}
	for _, fragment := range required {
		if !strings.Contains(up, fragment) {
			t.Errorf("up migration missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"ALTER TABLE api_tokens",
		"INSERT INTO api_tokens",
		"UPDATE api_tokens",
		"DELETE FROM api_tokens",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("legacy api_tokens became mutable: found %q", forbidden)
		}
	}

	downBytes, err := fs.ReadFile(FS, "0040_project_automation_tokens.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "IF EXISTS (SELECT 1 FROM project_automation_tokens)") ||
		!strings.Contains(down, "cannot roll back project_automation_tokens while token history exists") {
		t.Fatal("down migration does not refuse token-history loss")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 40\n    name: project_automation_tokens") {
		t.Fatal("migration 0040 is not registered")
	}
}
