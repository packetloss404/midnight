package db

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func TestParseMigrationFilename(t *testing.T) {
	got, err := ParseMigrationFilename("0001_alpha_core.up.sql")
	if err != nil {
		t.Fatalf("ParseMigrationFilename() error = %v", err)
	}
	if got.Version != 1 || got.Name != "alpha_core" || got.Direction != MigrationDirectionUp {
		t.Fatalf("ParseMigrationFilename() = %#v", got)
	}
}

func TestParseMigrationFilenameRejectsInvalidNames(t *testing.T) {
	for _, filename := range []string{"1_alpha_core.up.sql", "0001_alpha-core.up.sql", "0001_alpha_core.forward.sql", "0000_alpha_core.up.sql"} {
		if _, err := ParseMigrationFilename(filename); !errors.Is(err, ErrInvalidMigrationManifest) {
			t.Fatalf("ParseMigrationFilename(%q) error = %v, want ErrInvalidMigrationManifest", filename, err)
		}
	}
}

func TestLoadMigrationManifest(t *testing.T) {
	fsys := fstest.MapFS{
		"manifest.yaml": {Data: []byte("schema_version: 1\nmigrations:\n  - version: 1\n    name: alpha_core\n    up: 0001_alpha_core.up.sql\n    down: 0001_alpha_core.down.sql\n")},
	}

	got, err := LoadMigrationManifest(fsys, "manifest.yaml")
	if err != nil {
		t.Fatalf("LoadMigrationManifest() error = %v", err)
	}
	if got.SchemaVersion != 1 || len(got.Migrations) != 1 || got.Migrations[0].Name != "alpha_core" {
		t.Fatalf("LoadMigrationManifest() = %#v", got)
	}
}

func TestValidateMigrationManifestRejectsMismatchedSchemaVersion(t *testing.T) {
	manifest := MigrationManifest{
		SchemaVersion: 2,
		Migrations: []Migration{{
			Version: 1,
			Name:    "alpha_core",
			Up:      "0001_alpha_core.up.sql",
			Down:    "0001_alpha_core.down.sql",
		}},
	}
	if err := ValidateMigrationManifest(manifest); !errors.Is(err, ErrInvalidMigrationManifest) {
		t.Fatalf("ValidateMigrationManifest() error = %v, want ErrInvalidMigrationManifest", err)
	}
}

func TestAlphaCoreMigrationIncludesRequiredTables(t *testing.T) {
	sql := readAlphaCoreMigration(t)
	for _, table := range []string{"schema_migrations", "operations", "operation_events", "jobs", "idempotency_keys", "api_tokens", "hosts", "host_heartbeats"} {
		pattern := regexp.MustCompile(`(?is)create\s+table\s+(?:if\s+not\s+exists\s+)?` + regexp.QuoteMeta(table) + `\b`)
		if !pattern.MatchString(sql) {
			t.Fatalf("0001_alpha_core.up.sql does not create required table %q", table)
		}
	}
}

func TestAlphaCoreMigrationSatisfiesOperationStoreSchemaContract(t *testing.T) {
	report, err := LoadSchemaEvidence(os.DirFS(filepath.Join("..", "..")), "migrations/manifest.yaml", []int{1})
	if err != nil {
		t.Fatalf("LoadSchemaEvidence() error = %v", err)
	}
	if err := ValidateSchemaContract(report, OperationStoreSchemaContract()); err != nil {
		t.Fatalf("ValidateSchemaContract(operation store) = %v", err)
	}
}

func TestAlphaCoreMigrationAvoidsNowInPartialIndexPredicates(t *testing.T) {
	sql := readAlphaCoreMigration(t)
	for _, statement := range strings.Split(sql, ";") {
		lower := strings.ToLower(statement)
		if strings.Contains(lower, "create index") && strings.Contains(lower, " where ") && strings.Contains(lower, "now()") {
			t.Fatalf("partial index predicate must not use now(): %s", strings.TrimSpace(statement))
		}
	}
}

func readAlphaCoreMigration(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "migrations", "0001_alpha_core.up.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(data)
}
