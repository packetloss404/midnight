// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestServiceDeletionMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0071_service_deletion.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"DROP CONSTRAINT services_project_id_slug_key",
		"CREATE UNIQUE INDEX services_project_slug_live_uq",
		"WHERE deleted_at IS NULL",
		"CREATE INDEX deployments_service_live_intent_idx",
		"state IN ('queued', 'building', 'deploying')",
		"active AND runtime_desired_state = 'running'",
		"VALUES (71, 'service_deletion'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}
	// The delete path never writes a deployment row, so this migration must
	// not touch the FSM trigger attached in 0016.
	for _, forbidden := range []string{
		"trg_validate_deployment_contract",
		"validate_deployment_contract",
		"DELETE FROM deployments",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("up migration unexpectedly contains %q", forbidden)
		}
	}

	downBytes, err := FS.ReadFile("0071_service_deletion.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP INDEX IF EXISTS deployments_service_live_intent_idx",
		"DROP INDEX IF EXISTS services_project_slug_live_uq",
		"ADD CONSTRAINT services_project_id_slug_key UNIQUE (project_id, slug)",
		"version = 71",
		"name = 'service_deletion'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
	// Reverting must not cascade a service subtree away. Colliding deleted
	// slugs are renamed so every deployment and audit row stays reachable.
	if strings.Contains(down, "DELETE FROM services") {
		t.Fatal("down migration must not delete service rows")
	}
	if !strings.Contains(down, "UPDATE services") {
		t.Fatal("down migration must rename colliding deleted slugs")
	}
}
