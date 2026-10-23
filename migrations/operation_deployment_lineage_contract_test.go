// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestOperationDeploymentLineageMigrationContract(t *testing.T) {
	upBytes, err := fs.ReadFile(FS, "0044_operation_deployment_lineage.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"LOCK TABLE deployments IN SHARE ROW EXCLUSIVE MODE",
		"WHERE operation_id IS NOT NULL",
		"GROUP BY operation_id",
		"HAVING COUNT(*) > 1",
		"operation deployment lineage audit found",
		"CREATE UNIQUE INDEX deployments_operation_id_uniq",
		"ON deployments (operation_id)",
		"VALUES (44, 'operation_deployment_lineage'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(up), "DELETE FROM DEPLOYMENTS") ||
		strings.Contains(strings.ToUpper(up), "UPDATE DEPLOYMENTS") {
		t.Fatal("up migration must not silently delete or reassign ambiguous deployments")
	}

	downBytes, err := fs.ReadFile(FS, "0044_operation_deployment_lineage.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	if !strings.Contains(down, "DROP INDEX IF EXISTS deployments_operation_id_uniq") ||
		!strings.Contains(down, "WHERE version = 44") {
		t.Fatal("down migration does not reversibly remove only the lineage constraint")
	}

	manifestBytes, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 44\n    name: operation_deployment_lineage") {
		t.Fatal("migration 0044 is not registered in the manifest")
	}
}
