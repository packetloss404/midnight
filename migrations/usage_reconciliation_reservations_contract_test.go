// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"strings"
	"testing"
)

func TestUsageReconciliationReservationsMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0069_usage_reconciliation_reservations.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"ADD COLUMN build_started_at TIMESTAMPTZ",
		"ADD COLUMN build_completed_at TIMESTAMPTZ",
		"deployments_build_window_chk",
		"deployments_build_completed_idx",
		"'build_lifecycle'",
		"'volume_count'",
		"deployments_build_lifecycle",
		"CREATE TRIGGER volumes_usage_limit_before_write",
		"v.status IN ('provisioning','ready','attached')",
		"VALUES (69, 'usage_reconciliation_reservations'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0069_usage_reconciliation_reservations.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{
		"DROP TRIGGER IF EXISTS volumes_usage_limit_before_write",
		"DELETE FROM usage_policy_limits WHERE dimension = 'volume_count'",
		"DROP COLUMN IF EXISTS build_completed_at",
		"DROP COLUMN IF EXISTS build_started_at",
		"version = 69",
		"name = 'usage_reconciliation_reservations'",
	} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration missing %q", required)
		}
	}
}
