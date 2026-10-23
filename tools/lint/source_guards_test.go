package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsPanicOutsideCmdMain(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/example/example.go", "// Package example demonstrates a guard violation.\npackage example\n\nfunc Boom() {\n\tpanic(\"nope\")\n}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "forbidden panic() at internal/example/example.go") {
		t.Fatalf("stderr = %q, want forbidden panic", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_SOURCE_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_SOURCE_GUARD_FAILED reason", got)
	}
}

func TestRunAllowsPanicInsideCmdMain(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "cmd/example/main.go", "package main\n\nfunc main() {\n\tpanic(\"boot\")\n}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunAllowsPanicInsideTests(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/example/doc.go", "// Package example exercises the test-file exemption.\npackage example\n")
	writeFile(t, root, "internal/example/example_test.go", "package example\n\nimport \"testing\"\n\nfunc TestBoom(t *testing.T) {\n\tdefer func() { _ = recover() }()\n\tpanic(\"intentional\")\n}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunRejectsOSExitOutsideCmdMainAndTools(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/example/example.go", "// Package example demonstrates a guard violation.\npackage example\n\nimport \"os\"\n\nfunc Bail() {\n\tos.Exit(1)\n}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "forbidden os.Exit at internal/example/example.go") {
		t.Fatalf("stderr = %q, want forbidden os.Exit", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_SOURCE_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_SOURCE_GUARD_FAILED reason", got)
	}
}

func TestRunAllowsOSExitInToolsAndCmdMain(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "cmd/example/main.go", "package main\n\nimport \"os\"\n\nfunc main() {\n\tos.Exit(0)\n}\n")
	writeFile(t, root, "tools/audit/main.go", "// Package main runs the audit helper.\npackage main\n\nimport \"os\"\n\nfunc main() {\n\tos.Exit(0)\n}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunRejectsPackageMissingDoc(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/example/example.go", "package example\n\nfunc Hello() string { return \"hi\" }\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "package internal/example missing canonical \"// Package example\" doc comment") {
		t.Fatalf("stderr = %q, want missing package doc", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_SOURCE_GUARD_FAILED") {
		t.Fatalf("stderr = %q, want LINT_SOURCE_GUARD_FAILED reason", got)
	}
}

func TestRunAllowsPackageWithCanonicalDoc(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/example/doc.go", "// Package example owns example primitives.\npackage example\n")
	writeFile(t, root, "internal/example/example.go", "package example\n\nfunc Hello() string { return \"hi\" }\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunSkipsPackageDocAllowlist(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "internal/logs/logs.go", "package logs\n\nfunc Sink() {}\n")
	writeFile(t, root, "test/drills/harness/result.go", "package harness\n\nfunc Pass() {}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunSkipsCmdMainPackageDocCheck(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "cmd/example/main.go", "package main\n\nfunc main() {}\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}
