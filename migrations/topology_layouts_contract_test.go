package migrations

import (
	"strings"
	"testing"
)

func TestTopologyLayoutsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0058_topology_layouts.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE topology_layouts",
		"PRIMARY KEY (project_id, environment_id)",
		"FOREIGN KEY (project_id, workspace_id)",
		"FOREIGN KEY (environment_id, project_id)",
		"jsonb_array_length(nodes) <= 500",
		"resource_version BIGINT NOT NULL DEFAULT 1",
		"VALUES (58, 'topology_layouts'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0058 up migration missing %q", required)
		}
	}
	downBytes, err := FS.ReadFile("0058_topology_layouts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "DROP TABLE IF EXISTS topology_layouts") ||
		!strings.Contains(down, "version = 58") ||
		!strings.Contains(down, "name = 'topology_layouts'") {
		t.Fatalf("0058 down migration is incomplete: %s", down)
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifestBytes), "- version: 58\n    name: topology_layouts") {
		t.Fatal("manifest does not register migration 58")
	}
}
