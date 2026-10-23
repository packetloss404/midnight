package migrations

import (
	"strings"
	"testing"
)

func TestProjectAuditPaginationMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0062_project_audit_pagination.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"audit_logs_project_id_desc_idx",
		"ON audit_logs (project_id, id DESC)",
		"VALUES (62, 'project_audit_pagination'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0062 up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0062_project_audit_pagination.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP INDEX IF EXISTS audit_logs_project_id_desc_idx",
		"version = 62",
		"name = 'project_audit_pagination'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("0062 down migration missing %q", required)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 62\n    name: project_audit_pagination") {
		t.Fatal("manifest does not register migration 62")
	}
	if strings.Index(manifest, "- version: 62\n") > strings.Index(manifest, "- version: 63\n") {
		t.Fatal("manifest does not register migration 62 before the current schema version")
	}
}
