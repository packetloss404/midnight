package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/install"
	"github.com/midnight/midnight/internal/preflight"
)

func TestNewReleaseEvidencePacketReady(t *testing.T) {
	t.Parallel()

	preflightResults := []preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	installSummary := install.NewReadinessSummary(preflightResults, install.RemediationPlan{})
	doctorSummary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-OK", Status: StatusPass, Severity: "fatal"}})
	pressure, err := NewDiskPressureReviewSummary(0.5)
	if err != nil {
		t.Fatalf("NewDiskPressureReviewSummary() error = %v", err)
	}

	packet := NewReleaseEvidencePacket([]preflight.CommandEvidence{fakeCommandEvidence(0, false, "")}, preflightSummary, install.RemediationPlan{}, installSummary, doctorSummary, []ResourcePressureReviewSummary{pressure})

	if packet.Status != ReleaseEvidenceReady {
		t.Fatalf("Status = %q, want %q", packet.Status, ReleaseEvidenceReady)
	}
	if packet.IssueRollup.TotalIssues != 0 {
		t.Fatalf("IssueRollup = %+v, want no issues", packet.IssueRollup)
	}
	if err := ValidateReleaseEvidencePacket(packet); err != nil {
		t.Fatalf("ValidateReleaseEvidencePacket() error = %v", err)
	}
}

func TestNewReleaseEvidencePacketNeedsReview(t *testing.T) {
	t.Parallel()

	preflightResults := []preflight.Result{{ID: "PRE-WARN", Status: preflight.StatusWarn, Severity: "warn", SafeFixAvailable: true}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	plan := install.RemediationPlan{Steps: []install.RemediationStep{{Title: "Safe fix", ApplyMode: install.ApplyModeSafeFix, RequiresRestart: true}}}
	installSummary := install.NewReadinessSummary(preflightResults, plan)
	doctorSummary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-WARN", Status: StatusWarn, Severity: "warn"}})
	pressure, err := NewConntrackPressureReviewSummary(0.81)
	if err != nil {
		t.Fatalf("NewConntrackPressureReviewSummary() error = %v", err)
	}

	packet := NewReleaseEvidencePacket([]preflight.CommandEvidence{fakeCommandEvidence(2, false, "")}, preflightSummary, plan, installSummary, doctorSummary, []ResourcePressureReviewSummary{pressure})

	if packet.Status != ReleaseEvidenceNeedsReview {
		t.Fatalf("Status = %q, want %q", packet.Status, ReleaseEvidenceNeedsReview)
	}
	if packet.IssueRollup.BlockedCount != 0 || packet.IssueRollup.NeedsReviewCount != 6 {
		t.Fatalf("IssueRollup = %+v, want six review issues and no blockers", packet.IssueRollup)
	}
	if packet.IssueRollup.CountsBySource["command_evidence"] != 1 || packet.IssueRollup.CountsBySource["resource_pressure"] != 1 {
		t.Fatalf("CountsBySource = %+v, want command and resource issues", packet.IssueRollup.CountsBySource)
	}
}

func TestNewReleaseEvidencePacketBlocked(t *testing.T) {
	t.Parallel()

	preflightResults := []preflight.Result{{ID: "PRE-FAIL", Status: preflight.StatusFail, Severity: "fatal"}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	plan := install.RemediationPlan{Steps: []install.RemediationStep{{Title: "Manual fix", ApplyMode: install.ApplyModeManual, RequiresRestart: true}}}
	installSummary := install.NewReadinessSummary(preflightResults, plan)
	doctorSummary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-FAIL", Status: StatusFail, Severity: "fatal"}})
	pressure, err := NewDiskPressureReviewSummary(0.9)
	if err != nil {
		t.Fatalf("NewDiskPressureReviewSummary() error = %v", err)
	}

	packet := NewReleaseEvidencePacket([]preflight.CommandEvidence{fakeCommandEvidence(-1, true, "deadline exceeded")}, preflightSummary, plan, installSummary, doctorSummary, []ResourcePressureReviewSummary{pressure})

	if packet.Status != ReleaseEvidenceBlocked {
		t.Fatalf("Status = %q, want %q", packet.Status, ReleaseEvidenceBlocked)
	}
	if packet.IssueRollup.BlockedCount != 5 {
		t.Fatalf("IssueRollup = %+v, want five blockers", packet.IssueRollup)
	}
	if packet.IssueRollup.Issues[0].Source != "command_evidence" || packet.IssueRollup.Issues[0].Status != "blocked" {
		t.Fatalf("first issue = %+v, want blocked command evidence", packet.IssueRollup.Issues[0])
	}
}

func TestMarshalReleaseEvidencePacketJSON(t *testing.T) {
	t.Parallel()

	packet := readyReleaseEvidencePacket()

	data, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("packet JSON is invalid: %s", data)
	}

	var decoded ReleaseEvidencePacket
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(packet) error = %v", err)
	}
	if decoded.Schema != ReleaseEvidencePacketSchemaV1 || decoded.Status != ReleaseEvidenceReady || len(decoded.CommandEvidence) != 1 {
		t.Fatalf("decoded = %+v, want ready packet round-trip", decoded)
	}
}

func TestWriteReadReleaseEvidencePacketJSONFile(t *testing.T) {
	t.Parallel()

	packet := readyReleaseEvidencePacket()
	path := filepath.Join(t.TempDir(), "release-evidence.json")

	if err := WriteReleaseEvidencePacketJSONFile(path, packet, 0o600); err != nil {
		t.Fatalf("WriteReleaseEvidencePacketJSONFile() error = %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(first) error = %v", err)
	}
	if !json.Valid(first) {
		t.Fatalf("written JSON is invalid: %s", first)
	}
	if len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatalf("written JSON must end with newline: %q", first)
	}

	if err := WriteReleaseEvidencePacketJSONFile(path, packet, 0o600); err != nil {
		t.Fatalf("WriteReleaseEvidencePacketJSONFile(second) error = %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(second) error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("written JSON changed between identical writes\nfirst: %s\nsecond: %s", first, second)
	}

	decoded, err := ReadReleaseEvidencePacketJSONFile(path)
	if err != nil {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile() error = %v", err)
	}
	if decoded.Schema != ReleaseEvidencePacketSchemaV1 || decoded.Status != ReleaseEvidenceReady || len(decoded.CommandEvidence) != 1 {
		t.Fatalf("decoded = %+v, want ready packet round-trip", decoded)
	}
}

func TestReleaseEvidencePacketJSONFilePathValidation(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	packet := readyReleaseEvidencePacket()

	if err := WriteReleaseEvidencePacketJSONFile(filepath.Join(tempDir, "release-evidence.txt"), packet, 0o600); err == nil || !strings.Contains(err.Error(), "must end with .json") {
		t.Fatalf("WriteReleaseEvidencePacketJSONFile(non-json) error = %v, want .json validation", err)
	}
	if _, err := ReadReleaseEvidencePacketJSONFile(filepath.Join(tempDir, "release-evidence.txt")); err == nil || !strings.Contains(err.Error(), "must end with .json") {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile(non-json) error = %v, want .json validation", err)
	}

	missingParentPath := filepath.Join(tempDir, "missing", "release-evidence.json")
	if err := WriteReleaseEvidencePacketJSONFile(missingParentPath, packet, 0o600); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("WriteReleaseEvidencePacketJSONFile(missing parent) error = %v, want parent error", err)
	}
	if _, err := ReadReleaseEvidencePacketJSONFile(missingParentPath); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile(missing parent) error = %v, want parent error", err)
	}
}

func TestReadReleaseEvidencePacketJSONFileStrictDecodeAndValidation(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	packet := readyReleaseEvidencePacket()
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("json.Marshal(packet) error = %v", err)
	}

	unknownFieldPath := filepath.Join(tempDir, "unknown-field.json")
	unknownFieldJSON := strings.Replace(string(data), "{", `{"unexpected":true,`, 1)
	if err := os.WriteFile(unknownFieldPath, []byte(unknownFieldJSON), 0o600); err != nil {
		t.Fatalf("os.WriteFile(unknown field) error = %v", err)
	}
	if _, err := ReadReleaseEvidencePacketJSONFile(unknownFieldPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile(unknown field) error = %v, want strict decode error", err)
	}

	trailingPath := filepath.Join(tempDir, "trailing.json")
	if err := os.WriteFile(trailingPath, append(data, []byte(` {}`)...), 0o600); err != nil {
		t.Fatalf("os.WriteFile(trailing) error = %v", err)
	}
	if _, err := ReadReleaseEvidencePacketJSONFile(trailingPath); err == nil {
		t.Fatal("ReadReleaseEvidencePacketJSONFile(trailing) error = nil, want trailing JSON error")
	}

	invalidPath := filepath.Join(tempDir, "invalid-packet.json")
	invalidPacket := packet
	invalidPacket.Status = "unknown"
	invalidData, err := json.Marshal(invalidPacket)
	if err != nil {
		t.Fatalf("json.Marshal(invalidPacket) error = %v", err)
	}
	if err := os.WriteFile(invalidPath, invalidData, 0o600); err != nil {
		t.Fatalf("os.WriteFile(invalid packet) error = %v", err)
	}
	if _, err := ReadReleaseEvidencePacketJSONFile(invalidPath); err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("ReadReleaseEvidencePacketJSONFile(invalid packet) error = %v, want validation error", err)
	}
}

func TestValidateReleaseEvidencePacketRejectsInvalidPacket(t *testing.T) {
	t.Parallel()

	packet := ReleaseEvidencePacket{Schema: ReleaseEvidencePacketSchemaV1, Status: ReleaseEvidenceReady, IssueRollup: ReleaseEvidenceIssueRollup{TotalIssues: 1}}
	if err := ValidateReleaseEvidencePacket(packet); err == nil {
		t.Fatal("ValidateReleaseEvidencePacket() error = nil, want issue count error")
	}

	packet = ReleaseEvidencePacket{Schema: ReleaseEvidencePacketSchemaV1, Status: "unknown"}
	if err := ValidateReleaseEvidencePacket(packet); err == nil {
		t.Fatal("ValidateReleaseEvidencePacket() error = nil, want invalid status error")
	}

	packet = ReleaseEvidencePacket{Schema: ReleaseEvidencePacketSchemaV1, Status: ReleaseEvidenceReady, CommandEvidence: []preflight.CommandEvidence{{Schema: "bad"}}}
	if err := ValidateReleaseEvidencePacket(packet); err == nil {
		t.Fatal("ValidateReleaseEvidencePacket() error = nil, want command evidence schema error")
	}
}

func TestValidateReleaseEvidencePacketRejectsInconsistentIssueRollupCounts(t *testing.T) {
	t.Parallel()

	packet := readyReleaseEvidencePacket()
	packet.Status = ReleaseEvidenceNeedsReview
	packet.IssueRollup = ReleaseEvidenceIssueRollup{
		TotalIssues:      1,
		BlockedCount:     1,
		NeedsReviewCount: 0,
		CountsBySource:   map[string]int{"command_evidence": 1},
		Issues: []ReleaseEvidenceIssue{{
			Source:  "command_evidence",
			ID:      preflight.CommandSpecDockerAPI,
			Status:  string(ReleaseEvidenceNeedsReview),
			Message: "command evidence exited non-zero",
		}},
	}

	if err := ValidateReleaseEvidencePacket(packet); err == nil || !strings.Contains(err.Error(), "blocked count") {
		t.Fatalf("ValidateReleaseEvidencePacket() error = %v, want blocked count validation", err)
	}

	packet.IssueRollup.BlockedCount = 0
	packet.IssueRollup.NeedsReviewCount = 1
	packet.IssueRollup.CountsBySource["command_evidence"] = 2
	if err := ValidateReleaseEvidencePacket(packet); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("ValidateReleaseEvidencePacket() error = %v, want source count validation", err)
	}
}

func readyReleaseEvidencePacket() ReleaseEvidencePacket {
	preflightResults := []preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	installSummary := install.NewReadinessSummary(preflightResults, install.RemediationPlan{})
	doctorSummary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-OK", Status: StatusPass, Severity: "fatal"}})
	return NewReleaseEvidencePacket([]preflight.CommandEvidence{fakeCommandEvidence(0, false, "")}, preflightSummary, install.RemediationPlan{}, installSummary, doctorSummary, nil)
}

func fakeCommandEvidence(exitCode int, timedOut bool, runError string) preflight.CommandEvidence {
	return preflight.CommandEvidence{
		Schema:     preflight.CommandEvidenceSchemaV1,
		SpecID:     preflight.CommandSpecDockerAPI,
		CheckID:    "PRE-DOCKER-API-001",
		Executable: "docker",
		Args:       []string{"version", "--format", "{{.Server.APIVersion}}"},
		Timeout:    "3s",
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		Error:      runError,
	}
}
