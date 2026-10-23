package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestBuildCanonicalProofPacketContractRecordsB4Fields(t *testing.T) {
	flags := cliModeFlags{
		Format:                     "json",
		LastGreen:                  "abc123",
		DrillAdapter:               "local",
		ExecuteDrills:              true,
		RequireExecutedDrills:      true,
		ReleaseEvidence:            "artifacts/phase0/release-evidence.json",
		ReleaseEvidenceIndex:       "artifacts/phase0/release-evidence-index.json",
		VerifyReleaseEvidenceIndex: "artifacts/phase0/release-evidence-index.json",
		VerifyEvidenceFiles:        true,
		ReleaseSummary:             "artifacts/phase0/release-summary.json",
		EvidenceRefs: []evidenceRef{
			{Name: "z-extra", Path: "artifacts/phase0/z.json"},
			{Name: "operator-notes", Path: "artifacts/phase0/operator-notes.md"},
		},
	}

	contract, err := buildCanonicalProofPacketContract(flags, &releaseEvidenceIndexVerification{Status: "pass"})
	if err != nil {
		t.Fatalf("buildCanonicalProofPacketContract() error = %v", err)
	}

	if contract.ID != canonicalProofPacketID || contract.Status != "pass" || contract.Mode != "local-contract" || !contract.OfflineByDefault {
		t.Fatalf("contract header = %+v, want passing local B4 contract", contract)
	}
	if contract.RunnerOS != runtime.GOOS || contract.CommitSHA != "abc123" || contract.OperatorNotesRef != "artifacts/phase0/operator-notes.md" {
		t.Fatalf("contract identity fields = %+v, want runner OS, commit SHA, and operator notes ref", contract)
	}
	if contract.ReleaseEvidenceRef != "artifacts/phase0/release-evidence.json" || contract.ReleaseEvidenceIndexRef != "artifacts/phase0/release-evidence-index.json" || contract.ReleaseSummaryRef != "artifacts/phase0/release-summary.json" || contract.VerificationStatus != "pass" {
		t.Fatalf("contract release refs = %+v, want release evidence/index/summary refs and verification pass", contract)
	}
	for _, field := range []string{"command", "runnerOS", "commitSHA", "operatorNotesRef", "releaseEvidenceRef", "releaseEvidenceIndexRef", "releaseSummaryRef", "verificationStatus"} {
		if !stringSliceContains(contract.RequiredFields, field) {
			t.Fatalf("RequiredFields missing %q: %+v", field, contract.RequiredFields)
		}
	}
	operatorRefPos := strings.Index(contract.Command, "--evidence-ref=operator-notes=artifacts/phase0/operator-notes.md")
	zRefPos := strings.Index(contract.Command, "--evidence-ref=z-extra=artifacts/phase0/z.json")
	if operatorRefPos == -1 || zRefPos == -1 || operatorRefPos > zRefPos {
		t.Fatalf("Command = %q, want sorted evidence refs", contract.Command)
	}
}

func TestValidateCanonicalProofPacketRejectsUnsafeRefs(t *testing.T) {
	contract, err := buildCanonicalProofPacketContract(cliModeFlags{
		Format:               "json",
		LastGreen:            "abc123",
		ReleaseEvidence:      "artifacts/phase0/release-evidence.json",
		ReleaseEvidenceIndex: "artifacts/phase0/release-evidence-index.json",
		ReleaseSummary:       "artifacts/phase0/release-summary.json",
	}, nil)
	if err != nil {
		t.Fatalf("buildCanonicalProofPacketContract() error = %v", err)
	}

	contract.ReleaseSummaryRef = "../summary.json"
	if err := validateCanonicalProofPacketContract(contract); err == nil || !strings.Contains(err.Error(), "must not escape workspace") {
		t.Fatalf("validateCanonicalProofPacketContract() error = %v, want unsafe ref failure", err)
	}
}

func TestCanonicalProofPacketUsesPlaceholdersForAbsentOptionalRefs(t *testing.T) {
	contract, err := buildCanonicalProofPacketContract(cliModeFlags{Format: "text"}, nil)
	if err != nil {
		t.Fatalf("buildCanonicalProofPacketContract() error = %v", err)
	}

	if contract.CommitSHA != "unknown" || contract.OperatorNotesRef != canonicalNotProvided || contract.ReleaseEvidenceRef != canonicalNotProvided || contract.VerificationStatus != "not-run" {
		t.Fatalf("contract = %+v, want deterministic placeholders for missing values", contract)
	}
}
