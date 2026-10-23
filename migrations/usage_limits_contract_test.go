// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestUsageLimitsMigrationContract(t *testing.T) {
	raw, err := fs.ReadFile(FS, "0059_usage_limits.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	for _, required := range []string{
		"CREATE TABLE usage_policy_profiles", "not a paid entitlement",
		"CREATE TABLE usage_events", "BIGSERIAL PRIMARY KEY",
		"UNIQUE (source, source_instance, source_event_id)",
		"CREATE TABLE usage_source_cursors", "CREATE TABLE usage_evidence_gaps",
		"CREATE TABLE usage_reservations", "CREATE TABLE usage_admission_decisions",
		"pg_advisory_xact_lock", "midnight:usage-admission:", "midnight_enforce_workspace_count_limit",
		"usage_events_opaque_identity_chk", "usage_events_attributes_chk",
		"BEFORE INSERT OR UPDATE ON projects", "BEFORE INSERT OR UPDATE ON services",
		"BEFORE INSERT OR UPDATE ON environments", "p.deleted_at IS NULL",
		"source_artifact_workspace_charges", "observability_metric_samples",
		"traefik_access_logs", "VALUES (59, 'usage_limits'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}
	for _, prohibited := range []string{"stripe", "customer_id", "subscription_id", "docker_network", "volume_bytes_enforced"} {
		if strings.Contains(strings.ToLower(up), prohibited) {
			t.Errorf("up migration contains prohibited entitlement/evidence term %q", prohibited)
		}
	}
	manifest, err := fs.ReadFile(FS, "manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "- version: 59\n    name: usage_limits") {
		t.Fatal("migration 0059 is not registered")
	}
	downRaw, err := fs.ReadFile(FS, "0059_usage_limits.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downRaw)
	ordered := []string{
		"DROP TRIGGER IF EXISTS environments_usage_limit_before_write",
		"DROP TABLE IF EXISTS usage_admission_decisions",
		"DROP TABLE IF EXISTS usage_reservations",
		"DROP TABLE IF EXISTS usage_events",
		"DROP TABLE IF EXISTS usage_policy_profiles",
	}
	previous := -1
	for _, statement := range ordered {
		index := strings.Index(down, statement)
		if index < 0 || index <= previous {
			t.Fatalf("0059 down migration is missing or misorders %q", statement)
		}
		previous = index
	}
}
