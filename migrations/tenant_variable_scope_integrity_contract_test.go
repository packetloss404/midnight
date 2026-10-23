// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestTenantVariableScopeIntegrityMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0051_tenant_variable_scope_integrity.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"migration 0051 requires every non-shared variable to have canonical service/project lineage",
		"UPDATE service_variables variable\nSET workspace_id = project.workspace_id",
		"UPDATE service_variable_versions versioned",
		"CREATE OR REPLACE FUNCTION midnight_derive_service_variable_workspace()",
		"CREATE TRIGGER service_variables_derive_workspace",
		"BEFORE INSERT OR UPDATE OF project_id, workspace_id",
		"CREATE OR REPLACE FUNCTION midnight_derive_service_variable_version_scope()",
		"CREATE TRIGGER service_variable_versions_derive_scope",
		"service variable version parent lineage is unavailable",
		"GENERATED ALWAYS AS (NULLIF(service_id, '')) STORED",
		"service_variables_project_workspace_fk",
		"FOREIGN KEY (project_id, workspace_id)",
		"REFERENCES projects (id, workspace_id)",
		"service_variables_service_project_fk",
		"FOREIGN KEY (service_scope_id, project_id)",
		"REFERENCES services (id, project_id)",
		"service_variables_id_ownership_uniq",
		"service_variable_versions_parent_scope_fk",
		"VALIDATE CONSTRAINT service_variables_project_workspace_fk",
		"VALIDATE CONSTRAINT service_variables_service_project_fk",
		"VALIDATE CONSTRAINT service_variable_versions_parent_scope_fk",
		"VALUES (51, 'tenant_variable_scope_integrity'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0051 up migration missing %q", required)
		}
	}
	if strings.Contains(up, "SET value_ciphertext") ||
		strings.Contains(up, "SET wrapped_dek") {
		t.Fatal("0051 must not rewrite encrypted variable material")
	}

	downBytes, err := FS.ReadFile("0051_tenant_variable_scope_integrity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP CONSTRAINT IF EXISTS service_variable_versions_parent_scope_fk",
		"DROP TRIGGER IF EXISTS service_variable_versions_derive_scope",
		"DROP FUNCTION IF EXISTS midnight_derive_service_variable_version_scope()",
		"DROP INDEX IF EXISTS service_variables_id_ownership_uniq",
		"DROP CONSTRAINT IF EXISTS service_variables_service_project_fk",
		"DROP CONSTRAINT IF EXISTS service_variables_project_workspace_fk",
		"DROP TRIGGER IF EXISTS service_variables_derive_workspace",
		"DROP FUNCTION IF EXISTS midnight_derive_service_variable_workspace()",
		"DROP COLUMN IF EXISTS service_scope_id",
		"version = 51",
		"name = 'tenant_variable_scope_integrity'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("0051 down migration missing %q", required)
		}
	}
}

func TestTenantVariableScopeIntegrityMigrationIsLatest(t *testing.T) {
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 51\n    name: tenant_variable_scope_integrity") {
		t.Fatal("manifest does not register tenant_variable_scope_integrity before the current schema version")
	}
}
