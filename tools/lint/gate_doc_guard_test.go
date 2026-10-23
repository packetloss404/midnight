package main

import (
	"bytes"
	"strings"
	"testing"
)

const phase0GateDocSample = "# Phase 0 Gate Reference\n\n## Flags\n\n| Flag | Default | Purpose |\n|---|---|---|\n| `--format=<text|markdown|json>` | `text` | Report format. |\n| `--execute-drills` | `false` | Run the drill adapter. |\n"

const phase0GateMainSample = `package main

import "flag"

type inputs struct {
	Format        string
	ExecuteDrills bool
}

func parseFlags() inputs {
	var got inputs
	fs := flag.NewFlagSet("phase0-gate", flag.ContinueOnError)
	fs.StringVar(&got.Format, "format", "text", "report format")
	fs.BoolVar(&got.ExecuteDrills, "execute-drills", false, "execute drills")
	return got
}
`

const phase0GateMainSampleMissingFlag = `package main

import "flag"

type inputs struct {
	Format string
}

func parseFlags() inputs {
	var got inputs
	fs := flag.NewFlagSet("phase0-gate", flag.ContinueOnError)
	fs.StringVar(&got.Format, "format", "text", "report format")
	return got
}
`

func TestGateDocGuardAcceptsAlignedDocAndMain(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/phase0-gate.md", phase0GateDocSample)
	writeFile(t, root, "tools/phase0-gate/main.go", phase0GateMainSample)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}

func TestGateDocGuardRejectsDocFlagAbsentFromMain(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	writeFile(t, root, "docs/governance/phase0-gate.md", phase0GateDocSample)
	writeFile(t, root, "tools/phase0-gate/main.go", phase0GateMainSampleMissingFlag)

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err == nil {
		t.Fatal("run returned nil error, want failure")
	}
	if got := stderr.String(); !strings.Contains(got, "documents flag --execute-drills") {
		t.Fatalf("stderr = %q, want execute-drills drift failure", got)
	}
	if got := stderr.String(); !strings.Contains(got, "lint reason: LINT_GATE_DOC_DRIFT") {
		t.Fatalf("stderr = %q, want LINT_GATE_DOC_DRIFT reason", got)
	}
}

func TestGateDocGuardSkipsWhenSourcesMissing(t *testing.T) {
	root := t.TempDir()
	writeScaffold(t, root)
	// No docs/governance/phase0-gate.md, no tools/phase0-gate/main.go.
	// The guard must stay quiet rather than fabricate a failure on a
	// scaffold that does not yet exist.

	var stdout, stderr bytes.Buffer
	if err := run(root, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v\nstderr: %s", err, stderr.String())
	}
}
