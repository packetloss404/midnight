package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactDSNStripsPassword(t *testing.T) {
	t.Parallel()

	got := redactDSN("postgres://user:supersecret@host:5432/midnight")
	if got != "postgres://user:***@host:5432/midnight" {
		t.Fatalf("redactDSN = %q", got)
	}
}

func TestAdminMigratePromptUsesEmbeddedManifestVersion(t *testing.T) {
	clearAdminDSNEnv(t)
	wantVersion, err := adminManifestSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminMigrate([]string{"--dry-run", "--dsn", "postgres://user:secret@db:5432/midnight"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	wantVersionText := fmt.Sprintf("schema_version %d", wantVersion)
	if !strings.Contains(stdout.String(), wantVersionText) {
		t.Fatalf("stdout = %q, want %s", stdout.String(), wantVersionText)
	}
	if strings.Contains(stdout.String(), "secret") {
		t.Fatalf("stdout leaked password: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "postgres://user:***@db:5432/midnight") {
		t.Fatalf("stdout = %q, want redacted DSN", stdout.String())
	}
}

func TestAdminMigrateLoadsDSNFromCanonicalEnv(t *testing.T) {
	clearAdminDSNEnv(t)
	t.Setenv("MIDNIGHT_DB_DSN", "postgres://canonical:secret@db:5432/midnight")
	t.Setenv("MIDNIGHT_DATABASE_URL", "postgres://legacy:secret@db:5432/midnight")

	loaded, err := loadAdminDSN("", filepath.Join(t.TempDir(), "missing.dsn"))
	if err != nil {
		t.Fatalf("loadAdminDSN() error = %v", err)
	}
	if !strings.Contains(loaded.DSN, "canonical") {
		t.Fatalf("DSN = %q, want canonical MIDNIGHT_DB_DSN to win", loaded.DSN)
	}
}

func TestAdminMigrateLoadsDSNFromFile(t *testing.T) {
	clearAdminDSNEnv(t)
	path := filepath.Join(t.TempDir(), "db.dsn")
	if err := os.WriteFile(path, []byte("postgres://file:secret@db:5432/midnight\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminMigrate([]string{"--dry-run", "--dsn-file", path}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "postgres://file:***@db:5432/midnight") {
		t.Fatalf("stdout = %q, want file DSN redacted", stdout.String())
	}
}

func TestAdminMigrateRequiresExplicitNonInteractiveMode(t *testing.T) {
	clearAdminDSNEnv(t)

	var stdout, stderr bytes.Buffer
	code := runAdminMigrateWithInput(
		[]string{"--dsn", "postgres://user:secret@db:5432/midnight"},
		strings.NewReader("yes\n"),
		&stdout,
		&stderr,
		false,
	)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "use --yes for automation") {
		t.Fatalf("stderr = %q, want --yes guidance", stderr.String())
	}
	if strings.Contains(stderr.String(), "secret") {
		t.Fatalf("stderr leaked password: %q", stderr.String())
	}
}

func TestAdminMigrateInteractiveDeclineDoesNotApply(t *testing.T) {
	clearAdminDSNEnv(t)

	var stdout, stderr bytes.Buffer
	code := runAdminMigrateWithInput(
		[]string{"--dsn", "postgres://user:secret@db:5432/midnight"},
		strings.NewReader("no\n"),
		&stdout,
		&stderr,
		true,
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "[y/N]") || !strings.Contains(stderr.String(), "cancelled") {
		t.Fatalf("stderr = %q, want prompt and cancellation", stderr.String())
	}
	if strings.Contains(stderr.String(), "secret") {
		t.Fatalf("stderr leaked password: %q", stderr.String())
	}
}

func TestAdminMigrateDryRunRejectsApplyFlags(t *testing.T) {
	clearAdminDSNEnv(t)

	var stderr bytes.Buffer
	code := runAdminMigrate(
		[]string{"--dry-run", "--yes", "--dsn", "postgres://db/midnight"},
		&bytes.Buffer{},
		&stderr,
	)
	if code != 64 || !strings.Contains(stderr.String(), "cannot be combined") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
}

func TestReadAdminMigrationConfirmation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  bool
	}{
		{input: "yes\n", want: true},
		{input: "Y\n", want: true},
		{input: "no\n", want: false},
		{input: "\n", want: false},
		{input: "yes please\n", want: false},
	} {
		got, err := readAdminMigrationConfirmation(strings.NewReader(test.input))
		if err != nil {
			t.Fatalf("readAdminMigrationConfirmation(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("readAdminMigrationConfirmation(%q) = %t, want %t", test.input, got, test.want)
		}
	}
}

func TestAdminMigrateMissingDSNReturnsUsage(t *testing.T) {
	clearAdminDSNEnv(t)

	stderr := &bytes.Buffer{}
	code := runAdminMigrate([]string{"--dsn-file", filepath.Join(t.TempDir(), "missing.dsn")}, &bytes.Buffer{}, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "MIDNIGHT_DB_DSN") {
		t.Fatalf("stderr = %q, want canonical DSN hint", stderr.String())
	}
}

func clearAdminDSNEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MIDNIGHT_DB_DSN", "")
	t.Setenv("MIDNIGHT_DB_DSN_FILE", "")
	t.Setenv("MIDNIGHT_DATABASE_URL", "")
}
