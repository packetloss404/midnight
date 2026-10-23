package preflight

import (
	"fmt"
	"strings"
	"testing"
)

func TestParsePackageEnvStrictSystemdValuesWithoutExpansion(t *testing.T) {
	t.Parallel()
	values, err := ParsePackageEnvStrict(strings.Join([]string{
		"# comment",
		"; another comment",
		"PLAIN=  value with spaces  ",
		`SINGLE='literal $HOME $(touch /tmp/nope)'`,
		"DOUBLE=\"literal \\$HOME \\" + "`" + "cmd\\" + "`" + " and \\\"quote\\\"\"",
		"CONTINUED=one\\",
		"two",
		"MULTILINE='first",
		"second'",
	}, "\n"))
	if err != nil {
		t.Fatalf("ParsePackageEnvStrict() error = %v", err)
	}
	want := map[string]string{
		"PLAIN":     "value with spaces",
		"SINGLE":    "literal $HOME $(touch /tmp/nope)",
		"DOUBLE":    "literal $HOME `cmd` and \"quote\"",
		"CONTINUED": "onetwo",
		"MULTILINE": "first\nsecond",
	}
	for key, expected := range want {
		if got := values[key]; got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}

func TestParsePackageEnvStrictErrorsDoNotIncludeValues(t *testing.T) {
	t.Parallel()
	const secret = "SECRET-SENTINEL-DO-NOT-PRINT"
	_, err := ParsePackageEnvStrict("MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS='" + secret + "\n")
	if err == nil {
		t.Fatal("ParsePackageEnvStrict() error = nil, want unterminated quote")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error disclosed value: %v", err)
	}
	if !strings.Contains(err.Error(), "MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS") {
		t.Fatalf("error = %v, want safe variable name", err)
	}
}

func TestParsePackageEnvStrictRejectsInvalidNameAndNUL(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"1BAD=value\n", "GOOD=value\x00bad\n"} {
		if _, err := ParsePackageEnvStrict(input); err == nil {
			t.Fatalf("ParsePackageEnvStrict(%q) error = nil", input)
		}
	}
}

func TestParsePackageEnvRetainsLenientCompatibility(t *testing.T) {
	t.Parallel()
	values := ParsePackageEnv("VALID=value\n1INVALID=ignored\n")
	if got := values["VALID"]; got != "value" {
		t.Fatalf("VALID = %q, want value", got)
	}
}

func TestParsePackageReadinessEnvAllowListsAndRedactsDSN(t *testing.T) {
	t.Parallel()
	const secret = "postgres://user:SECRET-SENTINEL@db/midnight"
	values, configured, backupHint, err := ParsePackageReadinessEnv(strings.Join([]string{
		"MIDNIGHT_DB_DSN=" + secret,
		"MIDNIGHT_API_BEARER_TOKEN=another-secret",
		"MIDNIGHT_WORKER_RUNTIME_BACKEND=docker",
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"MIDNIGHT_BACKUP_DESTINATION_URI=sftp://user:BACKUP-SECRET@backup.example/midnight",
	}, "\n"))
	if err != nil {
		t.Fatalf("ParsePackageReadinessEnv() error = %v", err)
	}
	if !configured {
		t.Fatal("configured = false, want true")
	}
	if got := values["MIDNIGHT_WORKER_RUNTIME_BACKEND"]; got != "docker" {
		t.Fatalf("worker backend = %q, want docker", got)
	}
	if got := values["DOCKER_HOST"]; got != "unix:///var/run/docker.sock" {
		t.Fatalf("Docker host = %q, want package DOCKER_HOST", got)
	}
	if !backupHint.Configured || !backupHint.URIValid || backupHint.Scheme != "sftp" {
		t.Fatalf("backup hint = %#v, want sanitized sftp", backupHint)
	}
	for key, value := range values {
		if strings.Contains(key, "BEARER") || strings.Contains(key, "BACKUP") || strings.Contains(value, "SECRET") {
			t.Fatalf("readiness values disclosed secret: %s=%q", key, value)
		}
	}
}

func TestSanitizeBackupDestinationNeverRetainsCredentialBearingURI(t *testing.T) {
	t.Parallel()
	const secret = "BACKUP-SECRET-SENTINEL"
	hint := SanitizeBackupDestination("sftp://user:" + secret + "@backup.example/midnight")
	if !hint.Configured || !hint.URIValid || hint.Scheme != "sftp" {
		t.Fatalf("hint = %#v, want configured valid sftp", hint)
	}
	if strings.Contains(fmt.Sprintf("%#v", hint), secret) || strings.Contains(fmt.Sprintf("%#v", hint), "backup.example") {
		t.Fatalf("sanitized hint retained credential-bearing URI: %#v", hint)
	}
}
