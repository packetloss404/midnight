package db

import (
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildMigrationExecutionPlanReturnsOrderedPending(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 3,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
			{Version: 3, Name: "artifact_promotions", Up: "0003_artifact_promotions.up.sql", Down: "0003_artifact_promotions.down.sql"},
		},
	}

	got, err := BuildMigrationExecutionPlan(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildMigrationExecutionPlan() error = %v", err)
	}
	if got.Status != MigrationPlanStatusNeedsReview || got.SchemaVersion != 3 || got.LatestVersion != 3 {
		t.Fatalf("plan summary = %#v", got)
	}
	if len(got.AppliedVersions) != 1 || got.AppliedVersions[0] != 1 {
		t.Fatalf("applied versions = %#v", got.AppliedVersions)
	}
	if len(got.PendingMigrations) != 2 ||
		got.PendingMigrations[0].Version != 2 ||
		got.PendingMigrations[0].Name != "environment_locks" ||
		got.PendingMigrations[0].ChecksumPlaceholder ||
		got.PendingMigrations[1].Version != 3 ||
		got.PendingMigrations[1].Name != "artifact_promotions" {
		t.Fatalf("pending migrations = %#v", got.PendingMigrations)
	}
	if len(got.BlockedGaps) != 0 {
		t.Fatalf("blocked gaps = %#v, want none", got.BlockedGaps)
	}
}

func TestBuildMigrationExecutionPlanReportsBlockedGaps(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 3,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
			{Version: 3, Name: "artifact_promotions", Up: "0003_artifact_promotions.up.sql", Down: "0003_artifact_promotions.down.sql"},
		},
	}

	got, err := BuildMigrationExecutionPlan(manifest, []int{3})
	if err != nil {
		t.Fatalf("BuildMigrationExecutionPlan() error = %v", err)
	}
	if got.Status != MigrationPlanStatusBlocked {
		t.Fatalf("status = %q, want %q", got.Status, MigrationPlanStatusBlocked)
	}
	if len(got.BlockedGaps) != 2 || got.BlockedGaps[0].Version != 1 || got.BlockedGaps[0].BlockedByVersion != 3 || got.BlockedGaps[1].Version != 2 {
		t.Fatalf("blocked gaps = %#v", got.BlockedGaps)
	}
}

func TestLoadMigrationExecutionPlanRequiresManifestFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/manifest.yaml":          {Data: []byte("schema_version: 1\nmigrations:\n  - version: 1\n    name: alpha_core\n    up: 0001_alpha_core.up.sql\n    down: 0001_alpha_core.down.sql\n")},
		"migrations/0001_alpha_core.up.sql": {Data: []byte("CREATE TABLE operations (id TEXT);")},
	}

	if _, err := LoadMigrationExecutionPlan(fsys, "migrations/manifest.yaml", nil); err == nil {
		t.Fatal("LoadMigrationExecutionPlan() error = nil, want missing down file error")
	}
}

func TestRenderMigrationExecutionPlanJSONIsDeterministic(t *testing.T) {
	plan := MigrationExecutionPlan{SchemaVersion: 1, LatestVersion: 1, Status: MigrationPlanStatusReady, AppliedVersions: []int{1}}

	first, err := RenderMigrationExecutionPlanJSON(plan)
	if err != nil {
		t.Fatalf("RenderMigrationExecutionPlanJSON() error = %v", err)
	}
	second, err := RenderMigrationExecutionPlanJSON(plan)
	if err != nil {
		t.Fatalf("RenderMigrationExecutionPlanJSON() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("JSON is not deterministic:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), "\"status\": \"ready\"") || !strings.HasSuffix(string(first), "\n") {
		t.Fatalf("unexpected JSON: %q", string(first))
	}
}
