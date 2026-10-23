package migrations

import (
	"strings"
	"testing"
)

func TestOperationKeysetPaginationMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0054_operation_keyset_pagination.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"idx_operations_created_id",
		"idx_operations_kind_created_id",
		"idx_operations_state_created_id",
		"idx_operations_kind_state_created_id",
		"VALUES (54, 'operation_keyset_pagination'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0054 up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0054_operation_keyset_pagination.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{"DROP INDEX IF EXISTS", "version = 54", "name = 'operation_keyset_pagination'"} {
		if !strings.Contains(down, required) {
			t.Fatalf("0054 down migration missing %q", required)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 54\n    name: operation_keyset_pagination") {
		t.Fatal("manifest does not register migration 54")
	}
}
