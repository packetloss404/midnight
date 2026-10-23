// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package migrations

import (
	"strings"
	"testing"
)

func TestOperationCancellationMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0047_operation_cancellation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, want := range []string{
		"ALTER TABLE deploy_intake_outbox",
		"ALTER TABLE staged_work_outbox",
		"'canceled'",
		"VALUES (47, 'operation_cancellation'",
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("up migration missing %q", want)
		}
	}

	downBytes, err := FS.ReadFile("0047_operation_cancellation.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, want := range []string{
		"UPDATE deploy_intake_outbox",
		"UPDATE staged_work_outbox",
		"SET state='failed'",
		"version=47",
	} {
		if !strings.Contains(down, want) {
			t.Fatalf("down migration missing %q", want)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(
		manifest,
		"- version: 47\n    name: operation_cancellation",
	) {
		t.Fatal("migration manifest does not register operation cancellation")
	}
}
