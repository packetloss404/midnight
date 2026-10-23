package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLoadSchemaEvidenceLoadsManifest(t *testing.T) {
	fsys := fstest.MapFS{
		"manifest.yaml":            {Data: []byte("schema_version: 1\nmigrations:\n  - version: 1\n    name: alpha_core\n    up: 0001_alpha_core.up.sql\n    down: 0001_alpha_core.down.sql\n")},
		"0001_alpha_core.up.sql":   {Data: []byte("CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);\nCREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);")},
		"0001_alpha_core.down.sql": {Data: []byte("DROP TABLE operations;")},
	}

	got, err := LoadSchemaEvidence(fsys, "manifest.yaml", []int{1})
	if err != nil {
		t.Fatalf("LoadSchemaEvidence() error = %v", err)
	}
	if got.AppliedCount != 1 || got.PendingCount != 0 || got.LatestVersion != 1 || got.Status != SchemaEvidenceStatusReady {
		t.Fatalf("LoadSchemaEvidence() = %#v", got)
	}
	if len(got.DetectedTables) != 1 || got.DetectedTables[0] != "operations" || len(got.DetectedObjects) != 2 {
		t.Fatalf("SQL evidence = tables %#v objects %#v", got.DetectedTables, got.DetectedObjects)
	}
	if len(got.SourceFiles) != 2 {
		t.Fatalf("source files = %#v", got.SourceFiles)
	}
	if got.SourceFiles[0].Filename != "0001_alpha_core.down.sql" || got.SourceFiles[0].Kind != "down" || got.SourceFiles[0].ByteCount != len("DROP TABLE operations;") {
		t.Fatalf("down source file = %#v", got.SourceFiles[0])
	}
	if got.SourceFiles[1].Filename != "0001_alpha_core.up.sql" || got.SourceFiles[1].Kind != "up" || got.SourceFiles[1].ByteCount != len("CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);\nCREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);") {
		t.Fatalf("up source file = %#v", got.SourceFiles[1])
	}
	if got.SourceFiles[1].SHA256 != sha256Hex([]byte("CREATE TABLE IF NOT EXISTS operations (id TEXT PRIMARY KEY);\nCREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state);")) {
		t.Fatalf("up source sha256 = %q", got.SourceFiles[1].SHA256)
	}
}

func TestBuildSchemaEvidenceSummarizesAppliedAndPending(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 2,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
		},
	}

	got, err := BuildSchemaEvidence(manifest, []int{1})
	if err != nil {
		t.Fatalf("BuildSchemaEvidence() error = %v", err)
	}
	if got.SchemaVersion != 2 || got.LatestVersion != 2 || got.MigrationCount != 2 || got.AppliedCount != 1 || got.PendingCount != 1 || got.Status != SchemaEvidenceStatusNeedsReview {
		t.Fatalf("evidence summary = %#v", got)
	}
	if len(got.Migrations) != 2 || !got.Migrations[0].Applied || got.Migrations[1].Applied || got.Migrations[0].ChecksumPlaceholder || got.Migrations[1].Name != "environment_locks" {
		t.Fatalf("migration evidence = %#v", got.Migrations)
	}
}

func TestBuildSchemaEvidenceMarksGappedAppliedVersionsBlocked(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 2,
		Migrations: []Migration{
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
		},
	}

	got, err := BuildSchemaEvidence(manifest, []int{2})
	if err != nil {
		t.Fatalf("BuildSchemaEvidence() error = %v", err)
	}
	if got.Status != SchemaEvidenceStatusBlocked {
		t.Fatalf("status = %q, want %q", got.Status, SchemaEvidenceStatusBlocked)
	}
}

func TestSchemaEvidenceCountsUnsupportedAndInvalidSQL(t *testing.T) {
	report := SchemaEvidence{Status: SchemaEvidenceStatusReady}
	report.AddSQLSubsetEvidence("CREATE TABLE IF NOT EXISTS operations (id TEXT); ALTER TABLE operations ADD COLUMN state TEXT; CREATE TABLE ;")

	if report.UnsupportedStatementCount != 1 || report.InvalidStatementCount != 1 || report.Status != SchemaEvidenceStatusBlocked {
		t.Fatalf("SQL counts/status = unsupported %d invalid %d status %q", report.UnsupportedStatementCount, report.InvalidStatementCount, report.Status)
	}
	if len(report.DetectedTables) != 1 || report.DetectedTables[0] != "operations" {
		t.Fatalf("detected tables = %#v", report.DetectedTables)
	}
}

func TestSchemaEvidenceSourceFileManifestIsDeterministic(t *testing.T) {
	report := SchemaEvidence{}
	report.AddSourceFileEvidence("migrations/0002_environment_locks.down.sql", []byte("DROP TABLE environment_locks;"))
	report.AddSourceFileEvidence("migrations/0001_alpha_core.up.sql", []byte("CREATE TABLE alpha_core (id TEXT);"))
	report.AddSourceFileEvidence("inline.sql", []byte("SELECT 1;"))

	if len(report.SourceFiles) != 3 {
		t.Fatalf("source files = %#v", report.SourceFiles)
	}
	if report.SourceFiles[0].Filename != "inline.sql" || report.SourceFiles[0].Kind != "" {
		t.Fatalf("first source file = %#v", report.SourceFiles[0])
	}
	if report.SourceFiles[1].Filename != "migrations/0001_alpha_core.up.sql" || report.SourceFiles[1].Kind != "up" {
		t.Fatalf("second source file = %#v", report.SourceFiles[1])
	}
	if report.SourceFiles[2].Filename != "migrations/0002_environment_locks.down.sql" || report.SourceFiles[2].Kind != "down" {
		t.Fatalf("third source file = %#v", report.SourceFiles[2])
	}
	if report.SourceFiles[0].ByteCount != len("SELECT 1;") || report.SourceFiles[0].SHA256 != sha256Hex([]byte("SELECT 1;")) {
		t.Fatalf("first source metadata = %#v", report.SourceFiles[0])
	}
}

func TestBuildSchemaEvidenceRejectsOutOfOrderManifest(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 2,
		Migrations: []Migration{
			{Version: 2, Name: "environment_locks", Up: "0002_environment_locks.up.sql", Down: "0002_environment_locks.down.sql"},
			{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"},
		},
	}

	if _, err := BuildSchemaEvidence(manifest, nil); !errors.Is(err, ErrInvalidMigrationManifest) {
		t.Fatalf("BuildSchemaEvidence() error = %v, want ErrInvalidMigrationManifest", err)
	}
}

func TestBuildSchemaEvidenceRejectsUnknownAppliedVersion(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 1,
		Migrations:    []Migration{{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql"}},
	}

	if _, err := BuildSchemaEvidence(manifest, []int{2}); !errors.Is(err, ErrInvalidMigrationManifest) {
		t.Fatalf("BuildSchemaEvidence() error = %v, want ErrInvalidMigrationManifest", err)
	}
}

func TestRenderSchemaEvidenceJSONIsDeterministic(t *testing.T) {
	report := SchemaEvidence{SchemaVersion: 1, LatestVersion: 1, MigrationCount: 1, AppliedCount: 1, Migrations: []SchemaMigrationEvidence{{Version: 1, Name: "alpha_core", Up: "0001_alpha_core.up.sql", Down: "0001_alpha_core.down.sql", Applied: true}}}

	first, err := RenderSchemaEvidenceJSON(report)
	if err != nil {
		t.Fatalf("RenderSchemaEvidenceJSON() error = %v", err)
	}
	second, err := RenderSchemaEvidenceJSON(report)
	if err != nil {
		t.Fatalf("RenderSchemaEvidenceJSON() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("JSON is not deterministic:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), "\"schema_version\": 1") || !strings.HasSuffix(string(first), "\n") {
		t.Fatalf("unexpected JSON: %q", string(first))
	}
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
