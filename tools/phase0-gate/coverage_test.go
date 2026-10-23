package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCoverageReportListsDrillsAndReferences(t *testing.T) {
	root := writeReferenceFixture(t)
	manifestData := []byte(validDrillManifest())
	report, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	if len(report.Drills) != 19 {
		t.Fatalf("Drills = %d, want 19", len(report.Drills))
	}
	for i, d := range report.Drills {
		if d.ID == "" || d.Status == "" {
			t.Fatalf("Drills[%d] = %+v, want populated row", i, d)
		}
	}
	if len(report.ReasonCodes) == 0 || len(report.Runbooks) == 0 || len(report.Events) == 0 {
		t.Fatalf("report = %+v, want populated reason codes, runbooks, events", report)
	}
	text := report.Text()
	for _, want := range []string{"Phase 0 Gate Coverage", "Drill rows: 19", "## Drills", "## Reason Codes", "## Runbooks", "## Events"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Text() = %s, want %q", text, want)
		}
	}
}

func TestBuildCoverageReportSurfacesMissingReferences(t *testing.T) {
	root := writeReferenceFixture(t)
	if err := os.Remove(filepath.Join(root, "docs", "runbooks", "support-bundle.md")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	manifestData := []byte(validDrillManifest())
	report, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	if len(report.MissingReferences) == 0 {
		t.Fatal("MissingReferences = 0, want populated list")
	}
	if !strings.Contains(report.Text(), "Missing References") {
		t.Fatal("Text() missing the Missing References section")
	}
}

func TestBuildCoverageReportRejectsBadManifest(t *testing.T) {
	root := writeReferenceFixture(t)
	if _, err := buildCoverageReport([]byte("version: 2\nowner:\n"), root); err == nil {
		t.Fatal("buildCoverageReport() error = nil, want manifest error")
	}
}

func TestCoverageReportMarkdownRendersAllSections(t *testing.T) {
	root := writeReferenceFixture(t)
	manifestData := []byte(validDrillManifest())
	report, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	md := report.Markdown()
	for _, want := range []string{
		"# Phase 0 Gate Coverage",
		"## Summary",
		"| Metric | Count |",
		"| Drill rows | 19 |",
		"## Drills",
		"| ID | Status | Title | Reason Code | Event | Runbook |",
		"## Reason Codes",
		"## Runbooks",
		"## Events",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("Markdown() missing %q:\n%s", want, md)
		}
	}
}

func TestCoverageReportMarkdownIsStableAcrossInvocations(t *testing.T) {
	root := writeReferenceFixture(t)
	manifestData := []byte(validDrillManifest())
	first, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	second, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	if first.Markdown() != second.Markdown() {
		t.Fatalf("Markdown() is not deterministic")
	}
}

func TestCoverageReportMarkdownSurfacesMissingReferences(t *testing.T) {
	root := writeReferenceFixture(t)
	if err := os.Remove(filepath.Join(root, "docs", "runbooks", "support-bundle.md")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	manifestData := []byte(validDrillManifest())
	report, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	md := report.Markdown()
	if !strings.Contains(md, "## Missing References") {
		t.Fatalf("Markdown() missing missing-references section:\n%s", md)
	}
}

// TestCoverageReportMarkdownAndTextAgree verifies the two renderings cover
// the same drill IDs and reason codes (round-trip consistency).
func TestCoverageReportMarkdownAndTextAgree(t *testing.T) {
	root := writeReferenceFixture(t)
	manifestData := []byte(validDrillManifest())
	report, err := buildCoverageReport(manifestData, root)
	if err != nil {
		t.Fatalf("buildCoverageReport() error = %v", err)
	}
	text := report.Text()
	md := report.Markdown()
	for _, d := range report.Drills {
		if !strings.Contains(text, d.ID) {
			t.Fatalf("Text() missing drill ID %s", d.ID)
		}
		if !strings.Contains(md, d.ID) {
			t.Fatalf("Markdown() missing drill ID %s", d.ID)
		}
	}
	for _, code := range report.ReasonCodes {
		if !strings.Contains(text, code) {
			t.Fatalf("Text() missing reason code %s", code)
		}
		if !strings.Contains(md, code) {
			t.Fatalf("Markdown() missing reason code %s", code)
		}
	}
}
