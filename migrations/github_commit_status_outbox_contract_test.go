// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestGitHubCommitStatusOutboxMigrationContract(t *testing.T) {
	upRaw, err := fs.ReadFile(FS, "0061_github_commit_status_outbox.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upRaw)
	for _, required := range []string{
		"CREATE TABLE github_commit_statuses",
		"UNIQUE (deployment_id, state, context)",
		"delivery_state IN ('pending', 'delivering', 'posted', 'dead_letter')",
		"attempt >= 0 AND max_attempts BETWEEN 1 AND 64",
		"CREATE INDEX github_commit_statuses_due_idx",
		"CREATE OR REPLACE FUNCTION midnight_enqueue_terminal_github_commit_status()",
		"AFTER UPDATE OF state ON deployments",
		"WHEN 'active' THEN",
		"WHEN 'failed' THEN",
		"WHEN 'canceled' THEN",
		"ON CONFLICT (deployment_id, state, context) DO NOTHING",
		"VALUES (61, 'github_commit_status_outbox'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}

	downRaw, err := fs.ReadFile(FS, "0061_github_commit_status_outbox.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downRaw)
	for _, required := range []string{
		"DROP TRIGGER IF EXISTS trg_enqueue_terminal_github_commit_status ON deployments",
		"DROP FUNCTION IF EXISTS midnight_enqueue_terminal_github_commit_status()",
		"DROP TABLE IF EXISTS github_commit_statuses",
	} {
		if !strings.Contains(down, required) {
			t.Errorf("down migration missing %q", required)
		}
	}

	manifestRaw, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestRaw)
	if !strings.Contains(manifest, "- version: 61\n    name: github_commit_status_outbox") {
		t.Fatal("manifest does not register migration 61 before the current schema version")
	}
}
