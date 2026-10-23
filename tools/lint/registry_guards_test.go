package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const passingRegistrySource = `// Package failurecodes owns the additive error-code registry.
package failurecodes

type Code string

const (
	AuthTokenHeaderInvalid Code = "AUTH_TOKEN_HEADER_INVALID"
	InternalError          Code = "INTERNAL_ERROR"
)
`

const passingErrorCodesDoc = "# Error Codes\n\n| Code | Owner | Notes | Recovery hint |\n|---|---|---|---|\n| `AUTH_TOKEN_HEADER_INVALID` | S03 | Bad token transport. | Resend with Bearer. |\n| `INTERNAL_ERROR` | S03 | Fallback. | File an issue. |\n"

func TestRunRejectsRegistryConstantMissingFromDoc(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/failurecodes/codes.go", `// Package failurecodes owns the additive error-code registry.
package failurecodes

type Code string

const (
	AuthTokenHeaderInvalid Code = "AUTH_TOKEN_HEADER_INVALID"
	InternalError          Code = "INTERNAL_ERROR"
	NewlyAdded             Code = "NEWLY_ADDED"
)
`)
	writeFile(t, root, "docs/error-codes.md", passingErrorCodesDoc)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "error-code NEWLY_ADDED is declared in internal/failurecodes/codes.go but missing from docs/error-codes.md") {
		t.Fatalf("stderr = %q, want missing NEWLY_ADDED", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_REGISTRY_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_REGISTRY_GUARD_FAILED reason", got)
	}
}

func TestRunRejectsErrorCodesDocOutOfOrder(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/failurecodes/codes.go", passingRegistrySource)
	writeFile(t, root, "docs/error-codes.md", "# Error Codes\n\n| Code | Owner | Notes | Recovery hint |\n|---|---|---|---|\n| `INTERNAL_ERROR` | S03 | Fallback. | File an issue. |\n| `AUTH_TOKEN_HEADER_INVALID` | S03 | Bad token transport. | Resend with Bearer. |\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "docs/error-codes.md rows must be sorted alphabetically by code") {
		t.Fatalf("stderr = %q, want unsorted error-codes failure", got)
	}
}

func TestRunPassesWhenRegistryAndDocMatchAndAreSorted(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/failurecodes/codes.go", passingRegistrySource)
	writeFile(t, root, "docs/error-codes.md", passingErrorCodesDoc)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunSkipsRegistryCheckWhenSourceMissing(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestDeepRegistryGuardAcceptsRegisteredAllowlistedAndAnnotatedFixtures(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/error-codes.md", passingErrorCodesDoc)
	writeFile(t, root, "internal/failurecodes/codes.go", passingRegistrySource)
	// The fixture file uses .go.txt so go build never sees it. Copy it into
	// the synthetic repo as a real .go file under internal/example/ to
	// exercise the deep-registry guard on a real Go AST.
	src := readTestFixture(t, "deepregistry/registered_codes.go.txt")
	writeFile(t, root, "internal/example/doc.go", "// Package example exercises the deep-registry guard.\npackage example\n")
	writeFile(t, root, "internal/example/registered.go", src)
	// Mirror the production allowlist so the fixture's CapabilityName
	// literal resolves the same way it does in the real repo.
	writeFile(t, root, "tools/lint/non_failure_code_literals.txt", "DAC_OVERRIDE\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestDeepRegistryGuardRejectsUnregisteredLiteralFixture(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/error-codes.md", passingErrorCodesDoc)
	writeFile(t, root, "internal/failurecodes/codes.go", passingRegistrySource)
	src := readTestFixture(t, "deepregistry/unregistered_code.go.txt")
	writeFile(t, root, "internal/example/doc.go", "// Package example exercises the deep-registry guard.\npackage example\n")
	writeFile(t, root, "internal/example/unregistered.go", src)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "failure-code-shaped literal \"TOTALLY_NEW_CODE\"") {
		t.Fatalf("stderr = %q, want unregistered literal failure", got)
	}
	if got := stderr.String(); !strings.Contains(got, "internal/example/unregistered.go:") {
		t.Fatalf("stderr = %q, want fixture file path in diagnostic", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_REGISTRY_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_REGISTRY_GUARD_FAILED reason", got)
	}
}

func TestDeepRegistryGuardAcceptsAnnotatedAndAllowlistedLiterals(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/error-codes.md", passingErrorCodesDoc)
	writeFile(t, root, "internal/failurecodes/codes.go", passingRegistrySource)
	writeFile(t, root, "internal/example/doc.go", "// Package example exercises the annotation form.\npackage example\n")
	writeFile(t, root, "internal/example/example.go", "package example\n\n// EnvName is allowlisted in tools/lint/non_failure_code_literals.txt.\nconst EnvName = \"MY_ALLOWLISTED_TOKEN\"\n\n// FixtureCode is annotated inline.\nconst FixtureCode = \"FOO_BAR_BAZ\" // not-a-failure-code\n")
	writeFile(t, root, "tools/lint/non_failure_code_literals.txt", "MY_ALLOWLISTED_TOKEN\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

// readTestFixture reads a fixture file under tools/lint/testdata/ and returns
// its contents. The fixtures live under testdata/ so go build/test never
// walks them as real Go source.
func readTestFixture(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return string(data)
}
