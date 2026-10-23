package migrations

import (
	"strings"
	"testing"
)

func TestDeploymentRouteIntentsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0048_deployment_route_intents.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE deployment_route_intents",
		"deployment_id TEXT PRIMARY KEY",
		"REFERENCES deployments (id, project_id, environment_id, service_id)",
		"REFERENCES http_routes (id, project_id, environment_id, service_id)",
		"REFERENCES domains (id, project_id, environment_id, service_id)",
		"idx_route_backends_deployment_route",
		"VALUES (48, 'deployment_route_intents'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	downBytes, err := FS.ReadFile("0048_deployment_route_intents.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP TABLE IF EXISTS deployment_route_intents",
		"version = 48",
		"name = 'deployment_route_intents'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 48\n    name: deployment_route_intents") {
		t.Fatalf("manifest does not register migration 48:\n%s", manifest)
	}
}
