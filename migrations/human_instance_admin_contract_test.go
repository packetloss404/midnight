package migrations

import (
	"strings"
	"testing"
)

func TestHumanInstanceAdminMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0056_human_instance_admin.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"wm.workspace_id = 'wrk_default'",
		"wm.role = 'admin'",
		"u.is_disabled = FALSE",
		"no_active_admin",
		"SET is_admin = TRUE",
		"VALUES (56, 'human_instance_admin'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0056 up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0056_human_instance_admin.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if strings.Contains(down, "SET is_admin = FALSE") ||
		!strings.Contains(down, "version = 56") ||
		!strings.Contains(down, "name = 'human_instance_admin'") {
		t.Fatalf("0056 down migration is not privilege-preserving: %s", down)
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 56\n    name: human_instance_admin") {
		t.Fatal("manifest does not register migration 56")
	}
}
