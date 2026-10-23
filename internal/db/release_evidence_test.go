package db

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildReleaseEvidencePacketRollsUpStatusAndIssues(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 3,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
			{Version: 3, Name: "artifact_promotions", Up: "0003_artifact_promotions.up.sql", Down: "0003_artifact_promotions.down.sql"},
		},
	}
	schema, err := BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildSchemaEvidence() error = %v", err)
	}
	plan, err := BuildMigrationExecutionPlan(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildMigrationExecutionPlan() error = %v", err)
	}

	got := BuildReleaseEvidencePacket(schema, plan)

	if got.Status != ReleaseEvidenceStatusNeedsReview || got.SchemaVersion != 3 || got.LatestVersion != 3 {
		t.Fatalf("packet summary = %#v", got)
	}
	wantCodes := []string{ReleaseEvidenceIssuePendingMigration, ReleaseEvidenceIssuePendingMigration}
	if strings.Join(got.IssueCodes, ",") != strings.Join(wantCodes, ",") {
		t.Fatalf("issue codes = %#v, want %#v", got.IssueCodes, wantCodes)
	}
	if got.Schema.MigrationCount != 3 || len(got.MigrationPlan.PendingMigrations) != 2 {
		t.Fatalf("packet evidence = %#v", got)
	}
}

func TestBuildReleaseEvidencePacketReportsBlockedIssues(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 2,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
		},
	}
	schema, err := BuildSchemaEvidence(manifest, []int{2})
	if err != nil {
		t.Fatalf("BuildSchemaEvidence() error = %v", err)
	}
	schema.AddSQLSubsetEvidence("ALTER TABLE operations ADD COLUMN state TEXT; CREATE TABLE ;")
	plan, err := BuildMigrationExecutionPlan(manifest, []int{2})
	if err != nil {
		t.Fatalf("BuildMigrationExecutionPlan() error = %v", err)
	}

	got := BuildReleaseEvidencePacket(schema, plan)

	if got.Status != ReleaseEvidenceStatusBlocked {
		t.Fatalf("status = %q, want %q", got.Status, ReleaseEvidenceStatusBlocked)
	}
	for _, code := range []string{ReleaseEvidenceIssueUnsupportedSQL, ReleaseEvidenceIssueInvalidSQL, ReleaseEvidenceIssueMigrationGap, ReleaseEvidenceIssueSchemaStatusBlocked, ReleaseEvidenceIssueMigrationStatusBlocked} {
		if !containsString(got.IssueCodes, code) {
			t.Fatalf("issue codes %#v missing %q", got.IssueCodes, code)
		}
	}
}

func TestBuildReleaseEvidencePacketMarksExplicitDemoChecksumUnavailable(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 1,
		Migrations: []Migration{{
			Version:    1,
			Name:       "demo_unavailable_checksum",
			Up:         "0001_demo_unavailable_checksum.up.sql",
			Down:       "0001_demo_unavailable_checksum.down.sql",
			UpChecksum: "sha256:demo-unavailable",
		}},
	}
	schema, err := BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildSchemaEvidence() error = %v", err)
	}
	plan, err := BuildMigrationExecutionPlan(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildMigrationExecutionPlan() error = %v", err)
	}

	got := BuildReleaseEvidencePacket(schema, plan)

	if got.Status != ReleaseEvidenceStatusNeedsReview {
		t.Fatalf("status = %q, want explicit needs-review for demo checksum evidence", got.Status)
	}
	if strings.Join(got.IssueCodes, ",") != ReleaseEvidenceIssueChecksumPlaceholder {
		t.Fatalf("issue codes = %#v, want checksum placeholder issue", got.IssueCodes)
	}
}

func TestLoadReleaseEvidencePacketCombinesSchemaAndPlan(t *testing.T) {
	fsys := fstest.MapFS{
		"migrations/manifest.yaml":            {Data: []byte("schema_version: 1\nmigrations:\n  - version: 1\n    name: alpha_core\n    up: 0001_alpha_core.up.sql\n    down: 0001_alpha_core.down.sql\n")},
		"migrations/0001_alpha_core.up.sql":   {Data: []byte("CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);")},
		"migrations/0001_alpha_core.down.sql": {Data: []byte("DROP TABLE operations;")},
	}

	got, err := LoadReleaseEvidencePacket(fsys, "migrations/manifest.yaml", []int{1})
	if err != nil {
		t.Fatalf("LoadReleaseEvidencePacket() error = %v", err)
	}
	if got.Status != ReleaseEvidenceStatusReady || got.Schema.AppliedCount != 1 || len(got.MigrationPlan.PendingMigrations) != 0 {
		t.Fatalf("packet = %#v", got)
	}
}

func TestReleaseEvidencePacketJSONRoundTripRequiresJSONPath(t *testing.T) {
	packet := BuildReleaseEvidencePacket(
		SchemaEvidence{SchemaVersion: 1, LatestVersion: 1, Status: SchemaEvidenceStatusReady},
		MigrationExecutionPlan{SchemaVersion: 1, LatestVersion: 1, Status: MigrationPlanStatusReady},
	)
	path := filepath.Join(t.TempDir(), "release-evidence.json")

	if err := WriteReleaseEvidencePacketJSON(path, packet); err != nil {
		t.Fatalf("WriteReleaseEvidencePacketJSON() error = %v", err)
	}
	got, err := ReadReleaseEvidencePacketJSON(path)
	if err != nil {
		t.Fatalf("ReadReleaseEvidencePacketJSON() error = %v", err)
	}
	if got.Status != ReleaseEvidenceStatusReady || got.SchemaVersion != 1 || len(got.IssueCodes) != 0 {
		t.Fatalf("packet = %#v", got)
	}
	if err := WriteReleaseEvidencePacketJSON(filepath.Join(t.TempDir(), "release-evidence.txt"), packet); !errors.Is(err, ErrInvalidReleaseEvidencePacket) {
		t.Fatalf("WriteReleaseEvidencePacketJSON() error = %v, want ErrInvalidReleaseEvidencePacket", err)
	}
}

func TestDecodeReleaseEvidencePacketJSONIsStrict(t *testing.T) {
	unknown := []byte(`{"schema_version":1,"latest_version":1,"status":"ready","issue_codes":[],"issues":[],"schema":{"schema_version":1,"latest_version":1,"migration_count":0,"applied_count":0,"pending_count":0,"status":"ready","detected_tables":[],"detected_objects":[],"unsupported_statement_count":0,"invalid_statement_count":0,"migrations":[]},"migration_plan":{"schema_version":1,"latest_version":1,"status":"ready","applied_versions":[],"pending_migrations":[],"blocked_gaps":[]},"extra":true}`)
	if _, err := DecodeReleaseEvidencePacketJSON(unknown); !errors.Is(err, ErrInvalidReleaseEvidencePacket) {
		t.Fatalf("DecodeReleaseEvidencePacketJSON() error = %v, want ErrInvalidReleaseEvidencePacket", err)
	}

	trailing := []byte(`{"schema_version":1,"latest_version":1,"status":"ready","issue_codes":[],"issues":[],"schema":{"schema_version":1,"latest_version":1,"migration_count":0,"applied_count":0,"pending_count":0,"status":"ready","detected_tables":[],"detected_objects":[],"unsupported_statement_count":0,"invalid_statement_count":0,"migrations":[]},"migration_plan":{"schema_version":1,"latest_version":1,"status":"ready","applied_versions":[],"pending_migrations":[],"blocked_gaps":[]}} {}`)
	if _, err := DecodeReleaseEvidencePacketJSON(trailing); !errors.Is(err, ErrInvalidReleaseEvidencePacket) {
		t.Fatalf("DecodeReleaseEvidencePacketJSON() error = %v, want ErrInvalidReleaseEvidencePacket", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
