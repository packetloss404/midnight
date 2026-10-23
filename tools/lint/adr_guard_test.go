package main

import (
	"bytes"
	"strings"
	"testing"
)

const adrSampleOne = "# ADR-0001: Sample Decision\n\n- Status: accepted\n- Date: 2026-04-26\n\n## Context\n\nWhy.\n\n## Decision\n\nWhat.\n\n## Consequences\n\nSo.\n"

const adrSampleTwo = "# ADR-0002: Other Decision\n\n- Status: proposed\n- Date: 2026-04-26\n\n## Context\n\nWhy.\n\n## Decision\n\nWhat.\n\n## Consequences\n\nSo.\n"

const matchingADRIndex = "# Architecture Decision Records\n\n| ADR | Status | Title |\n|---|---|---|\n| [0001](0001-sample-decision.md) | accepted | Sample Decision |\n| [0002](0002-other-decision.md) | proposed | Other Decision |\n"

func TestRunRejectsADRIndexDrift(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/adr/0001-sample-decision.md", adrSampleOne)
	writeFile(t, root, "docs/adr/0002-other-decision.md", adrSampleTwo)
	writeFile(t, root, "docs/adr/README.md", "# Architecture Decision Records\n\nNothing here.\n")

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "docs/adr/README.md drifted from front-matter") {
		t.Fatalf("stderr = %q, want ADR drift failure", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_ADR_INDEX_DRIFT") {
		t.Fatalf("stderr = %q, want LINT_ADR_INDEX_DRIFT reason", got)
	}
}

func TestRunPassesWhenADRIndexMatchesFrontMatter(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/adr/0001-sample-decision.md", adrSampleOne)
	writeFile(t, root, "docs/adr/0002-other-decision.md", adrSampleTwo)
	writeFile(t, root, "docs/adr/README.md", matchingADRIndex)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestRunRejectsADRWithoutStatusFrontMatter(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/adr/0001-bad-front-matter.md", "# ADR-0001: Bad Front Matter\n\n## Context\n\nMissing status.\n")
	writeFile(t, root, "docs/adr/README.md", matchingADRIndex)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "missing \"- Status:\" line") {
		t.Fatalf("stderr = %q, want missing status line", got)
	}
}

func TestADRIndexWriteRegeneratesDriftedIndex(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/adr/0001-sample-decision.md", adrSampleOne)
	writeFile(t, root, "docs/adr/0002-other-decision.md", adrSampleTwo)
	writeFile(t, root, "docs/adr/README.md", "# Architecture Decision Records\n\nNothing here.\n")

	var stdout, stderr bytes.Buffer
	exit := runCLI([]string{"--adr-index-write", root}, &stdout, &stderr)
	if exit != ExitDriftWritten {
		t.Fatalf("exit = %d, want %d (drift written)\nstderr: %s", exit, ExitDriftWritten, stderr.String())
	}
	got := readFile(t, root, "docs/adr/README.md")
	if got != matchingADRIndex {
		t.Fatalf("regenerated index = %q, want %q", got, matchingADRIndex)
	}
	// A second invocation should report ok and exit with ExitOK because the
	// index now matches the front-matter projection.
	stdout.Reset()
	stderr.Reset()
	exit = runCLI([]string{"--adr-index-write", root}, &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("second exit = %d, want %d (ok)\nstderr: %s", exit, ExitOK, stderr.String())
	}
}

func TestADRIndexWriteIsNoOpOnFreshTreeWithoutADRs(t *testing.T) {
	root := t.TempDir()

	var stdout, stderr bytes.Buffer
	exit := runCLI([]string{"--adr-index-write", root}, &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("exit = %d, want %d (ok)\nstderr: %s", exit, ExitOK, stderr.String())
	}
}

func TestRunCLIRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runCLI([]string{"--no-such-flag"}, &stdout, &stderr)
	if exit != ExitInvocation {
		t.Fatalf("exit = %d, want %d (invocation)\nstderr: %s", exit, ExitInvocation, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_INVOCATION_FAILED") {
		t.Fatalf("stderr = %q, want LINT_INVOCATION_FAILED reason", got)
	}
}

func TestRunCLIReturnsGuardFailedExitOnDrift(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/adr/0001-sample-decision.md", adrSampleOne)
	writeFile(t, root, "docs/adr/0002-other-decision.md", adrSampleTwo)
	writeFile(t, root, "docs/adr/README.md", "# Architecture Decision Records\n\nNothing here.\n")

	var stdout, stderr bytes.Buffer
	exit := runCLI([]string{root}, &stdout, &stderr)
	if exit != ExitGuardFailed {
		t.Fatalf("exit = %d, want %d (guard failed)\nstderr: %s", exit, ExitGuardFailed, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_ADR_INDEX_DRIFT") {
		t.Fatalf("stderr = %q, want LINT_ADR_INDEX_DRIFT reason", got)
	}
}
