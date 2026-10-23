// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestUsageLimitTriggerScopeRepairMigrationContract(t *testing.T) {
	upRaw, err := fs.ReadFile(FS, "0060_usage_limit_trigger_scope_repair.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upRaw)
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION midnight_enforce_workspace_count_limit()",
		"new_row := to_jsonb(NEW)",
		"old_row := to_jsonb(OLD)",
		"new_row->>'workspace_id'",
		"new_row->>'project_id'",
		"old_row->>'project_id'",
		"pg_advisory_xact_lock",
		"MIDNIGHT_USAGE_LIMIT_EXCEEDED",
		"VALUES (60, 'usage_limit_trigger_scope_repair'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}
	for _, prohibited := range []string{
		"OLD.workspace_id", "NEW.workspace_id", "OLD.project_id", "NEW.project_id",
		"DROP TRIGGER", "CREATE TRIGGER",
	} {
		if strings.Contains(up, prohibited) {
			t.Errorf("up migration contains unsafe or disruptive statement %q", prohibited)
		}
	}

	manifest, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "- version: 60\n    name: usage_limit_trigger_scope_repair") {
		t.Fatal("manifest does not retain migration 0060")
	}

	downRaw, err := fs.ReadFile(FS, "0060_usage_limit_trigger_scope_repair.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downRaw)
	if !strings.Contains(down, "CREATE OR REPLACE FUNCTION midnight_enforce_workspace_count_limit()") ||
		!strings.Contains(down, "DELETE FROM schema_migrations") ||
		strings.Contains(down, "DROP TRIGGER") {
		t.Fatal("down migration must restore the function without rebinding triggers")
	}
}
