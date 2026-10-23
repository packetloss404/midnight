package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPhase0ReleaseEvidenceArchiveContractFromData(t *testing.T) {
	contract, err := buildPhase0ReleaseEvidenceArchiveContractFromData([]byte(validPhase0ReleaseEvidenceArchive("ready")))
	if err != nil {
		t.Fatalf("buildPhase0ReleaseEvidenceArchiveContractFromData() error = %v", err)
	}
	if contract.ID != phase0ReleaseEvidenceArchiveID || contract.Sprint != "Phase 0 Beta Sprint B9" || contract.Status != "ready" {
		t.Fatalf("contract identity = %+v, want B9 ready release evidence archive contract", contract)
	}
	if !contract.LocalOnly || contract.LiveHostProbes {
		t.Fatalf("contract locality = %+v, want local-only without live host probes", contract)
	}
	if contract.ReleaseEvidence != phase0ReleaseEvidencePath || contract.ReleaseEvidenceIndex != phase0ReleaseEvidenceIndexPath || contract.ReleaseSummary != phase0ReleaseSummaryPath {
		t.Fatalf("contract paths = %+v, want canonical phase0-prefixed archive paths", contract)
	}
	if contract.VerifyReleaseEvidenceIndex != phase0ReleaseEvidenceIndexPath || !contract.VerifyEvidenceFiles {
		t.Fatalf("verify flags = %q/%t, want required index and file verification", contract.VerifyReleaseEvidenceIndex, contract.VerifyEvidenceFiles)
	}
	for _, ref := range []string{contract.OperatorNotes, contract.RunnerOS} {
		if filepath.IsAbs(ref) || strings.Contains(ref, `\`) || strings.Contains(ref, ":") || strings.HasPrefix(ref, "../") {
			t.Fatalf("contract ref %q is not workspace-relative portable", ref)
		}
	}
	if strings.Join(contract.RequiredFlags, ",") != "release-evidence,release-evidence-index,release-summary,verify-evidence-files,verify-release-evidence-index" {
		t.Fatalf("RequiredFlags = %v, want canonical command flags", contract.RequiredFlags)
	}
}

func TestPhase0ReleaseEvidenceArchiveRejectsUnsafeOrNonCanonicalContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "wrong sprint",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "Phase 0 Beta Sprint B9", "Phase 0 Beta Sprint B8", 1),
			want: "Sprint B9",
		},
		{
			name: "not local only",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "local-only: true", "local-only: false", 1),
			want: "local-only",
		},
		{
			name: "non canonical evidence path",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), phase0ReleaseEvidencePath, "artifacts/phase0/release-evidence.json", 1),
			want: phase0ReleaseEvidencePath,
		},
		{
			name: "unsafe operator notes",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "artifacts/phase0/operator-notes.md", "../operator-notes.md", 1),
			want: "must not escape workspace",
		},
		{
			name: "missing verify files flag",
			data: strings.Replace(strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "verify-evidence-files: true", "verify-evidence-files: false", 1), "  - verify-evidence-files\n", "", 1),
			want: "",
		},
		{
			name: "missing required flag",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "  - verify-release-evidence-index\n", "", 1),
			want: "",
		},
		{
			name: "live probes",
			data: strings.Replace(validPhase0ReleaseEvidenceArchive("ready"), "live-host-probes: false", "live-host-probes: true", 1),
			want: "live host probes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract, err := buildPhase0ReleaseEvidenceArchiveContractFromData([]byte(tc.data))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("buildPhase0ReleaseEvidenceArchiveContractFromData() error = %v, want not-ready contract", err)
				}
				if contract.Status != "not-ready" {
					t.Fatalf("contract status = %q, want not-ready", contract.Status)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("buildPhase0ReleaseEvidenceArchiveContractFromData() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPhase0ReleaseEvidenceArchiveSupportsReadinessOnlyShape(t *testing.T) {
	contract, err := buildPhase0ReleaseEvidenceArchiveContractFromData([]byte(validPhase0ReleaseEvidenceArchiveReadinessOnly()))
	if err != nil {
		t.Fatalf("buildPhase0ReleaseEvidenceArchiveContractFromData() error = %v", err)
	}
	if contract.Status != "not-ready" {
		t.Fatalf("Status = %q, want not-ready for pending readiness rows and missing verify-release command flag", contract.Status)
	}
	if contract.ReleaseEvidence != phase0ReleaseEvidencePath || contract.ReleaseEvidenceIndex != phase0ReleaseEvidenceIndexPath || contract.ReleaseSummary != phase0ReleaseSummaryPath {
		t.Fatalf("canonical paths = %+v, want B9 archive paths", contract)
	}
	if !contract.VerifyEvidenceFiles || contract.VerifyReleaseEvidenceIndex != "" {
		t.Fatalf("verify command = %q/%t, want partial readiness-only command captured", contract.VerifyReleaseEvidenceIndex, contract.VerifyEvidenceFiles)
	}
}

func TestBuildGateReportIncludesB9ReleaseEvidenceArchiveWhenFilePresent(t *testing.T) {
	root := t.TempDir()
	drillsDir := filepath.Join(root, "test", "drills")
	if err := os.MkdirAll(drillsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(drillsDir, "phase0-release-evidence-archive.yaml"), []byte(validPhase0ReleaseEvidenceArchive("not-ready")), 0o644); err != nil {
		t.Fatalf("WriteFile(phase0-release-evidence-archive) error = %v", err)
	}
	chdirForTest(t, root)

	report, err := buildGateReport([]byte(validDrillManifest()), validAlertRules(), "unknown")
	if err != nil {
		t.Fatalf("buildGateReport() error = %v", err)
	}
	if report.Phase0ReleaseEvidenceArchive == nil || report.Phase0ReleaseEvidenceArchive.ReleaseEvidence != phase0ReleaseEvidencePath {
		t.Fatalf("Phase0ReleaseEvidenceArchive = %+v, want B9 archive contract", report.Phase0ReleaseEvidenceArchive)
	}
	if evidenceItemStatus(report.EvidencePacket, phase0ReleaseEvidenceArchiveEvidenceName) != "not-ready" {
		t.Fatalf("EvidencePacket.Items = %+v, want B9 archive not-ready evidence", report.EvidencePacket.Items)
	}
	for _, want := range []string{"phase0_release_evidence_archive=not-ready", "| B9 Phase 0 release evidence archive | not-ready | 5 |", phase0ReleaseEvidencePath} {
		if !strings.Contains(report.String()+report.Markdown(), want) {
			t.Fatalf("report output missing %q\nString=%q\nMarkdown=%q", want, report.String(), report.Markdown())
		}
	}
}

func validPhase0ReleaseEvidenceArchive(status string) string {
	return `version: 1
owner: S08
sprint: Phase 0 Beta Sprint B9
status: ` + status + `
local-only: true
release-evidence: artifacts/phase0/phase0-release-evidence.json
release-evidence-index: artifacts/phase0/phase0-release-evidence-index.json
release-summary: artifacts/phase0/phase0-release-summary.json
verify-release-evidence-index: artifacts/phase0/phase0-release-evidence-index.json
verify-evidence-files: true
operator-notes: artifacts/phase0/operator-notes.md
runner-os: artifacts/phase0/runner-os.json
live-host-probes: false
required-flags:
  - release-evidence
  - release-evidence-index
  - release-summary
  - verify-release-evidence-index
  - verify-evidence-files
`
}

func validPhase0ReleaseEvidenceArchiveReadinessOnly() string {
	return `version: 1
owner: S08
sprint: Phase 0 Beta Sprint B9
local-only: true
source-manifest: test/drills/manifest.yaml
archive-root: artifacts/phase0
gate-command:
  name: phase0-release-gate
  mode: local-only
  command-ref: artifacts/phase0/phase0-release-gate-command.txt
  required-flag: --verify-evidence-files
evidence-refs:
  canonical:
    release-evidence: artifacts/phase0/phase0-release-evidence.json
    release-evidence-index: artifacts/phase0/phase0-release-evidence-index.json
    release-summary: artifacts/phase0/phase0-release-summary.json
  operator-metadata:
    operator-notes: artifacts/phase0/operator-notes.md
    runner-os: artifacts/phase0/runner-os.json
readiness:
  rows:
    - id: phase0-release-evidence
      status: pending-local-archive
      required-ref: artifacts/phase0/phase0-release-evidence.json
`
}
