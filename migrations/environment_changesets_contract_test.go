// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestEnvironmentChangesetsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0057_environment_changesets.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"CREATE TABLE environment_changesets",
		"CHECK (state IN ('draft', 'applied', 'discarded'))",
		"revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)",
		"environment_changesets_one_draft_per_environment",
		"environment_changesets_state_timestamp_check",
		"CHECK (char_length(note) <= 500)",
		"WHERE state = 'draft'",
		"CREATE TABLE environment_changeset_items",
		"CHECK (kind IN ('variable', 'networking', 'runtime', 'storage'))",
		"value_ciphertext BYTEA NOT NULL",
		"wrapped_dek BYTEA NOT NULL",
		"environment_changeset_items_changeset_lineage_fk",
		"FOREIGN KEY (changeset_id, project_id, environment_id)",
		"environment_changeset_items_service_project_fk",
		"FOREIGN KEY (service_id, project_id)",
		"UNIQUE (changeset_id, service_id, kind, variable_name)",
		"VALUES (57, 'environment_changesets'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0057 up migration missing %q", required)
		}
	}
	if strings.Contains(up, " value TEXT") || strings.Contains(up, "plaintext") {
		t.Fatal("0057 must not define a plaintext value column")
	}
	downBytes, err := FS.ReadFile("0057_environment_changesets.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{"DROP TABLE IF EXISTS environment_changeset_items", "DROP TABLE IF EXISTS environment_changesets", "version = 57", "name = 'environment_changesets'"} {
		if !strings.Contains(down, required) {
			t.Fatalf("0057 down migration missing %q", required)
		}
	}
	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifestBytes), "- version: 57\n    name: environment_changesets") {
		t.Fatal("manifest does not register 0057")
	}
}
