// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestMultiResourceChangesetsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0068_multi_resource_changesets.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ALTER COLUMN expected_version TYPE BIGINT",
		"ADD COLUMN payload JSONB NOT NULL",
		"environment_changeset_items_payload_shape_chk",
		"environment_changeset_items_kind_payload_chk",
		"kind = 'variable' AND payload = '{}'::JSONB",
		"kind IN ('runtime', 'networking', 'storage')",
		"VALUES (68, 'multi_resource_changesets'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0068_multi_resource_changesets.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP COLUMN IF EXISTS payload",
		"ALTER COLUMN expected_version TYPE INTEGER",
		"version = 68",
		"name = 'multi_resource_changesets'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
}
