package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/supportbundle"
)

func TestBuildReleaseEvidencePacketReady(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())

	if packet.Status != ReleaseEvidenceStatusReady {
		t.Fatalf("BuildReleaseEvidencePacket() status = %q, want ready", packet.Status)
	}
	if packet.OperatorReviewRequired {
		t.Fatal("BuildReleaseEvidencePacket() OperatorReviewRequired = true, want false")
	}
	if packet.OperatorReviewDecision.Decision != OperatorReviewDecisionPending {
		t.Fatalf("BuildReleaseEvidencePacket() operator review decision = %+v, want pending", packet.OperatorReviewDecision)
	}
	if len(packet.Issues) != 0 {
		t.Fatalf("BuildReleaseEvidencePacket() issues = %+v, want none", packet.Issues)
	}
}

func TestBuildReleaseEvidencePacketNeedsReviewRollupIsDeterministic(t *testing.T) {
	input := readyReleaseEvidenceInput()
	input.SupportBundleArchiveReview.Decision = supportbundle.ReviewDecisionNeedsReview
	input.SupportBundleArchiveReview.ExcludedCount = 1
	input.SupportBundleArchiveReview.RedactionSummary = supportbundle.RedactionSummary{
		Findings: []supportbundle.RedactionFinding{{Path: "logs/api.log", Rule: "token", Count: 1}},
		Total:    1,
	}
	input.BackupReadinessReview = BackupReadinessSummary{
		Status: BackupReadinessStatusNeedsReview,
		Issues: []BackupReadinessIssue{{
			Code:    BackupReadinessIssueVerificationSkipped,
			Status:  BackupReadinessStatusNeedsReview,
			Message: "one or more backup verification probes were skipped",
		}},
	}

	packet := BuildReleaseEvidencePacket(input)

	if packet.Status != ReleaseEvidenceStatusNeedsReview {
		t.Fatalf("BuildReleaseEvidencePacket() status = %q, want needs-review", packet.Status)
	}
	if !packet.OperatorReviewRequired {
		t.Fatal("BuildReleaseEvidencePacket() OperatorReviewRequired = false, want true")
	}
	wantCodes := []string{
		ReleaseEvidenceIssueSupportBundleArchiveNeedsReview,
		ReleaseEvidenceIssueSupportBundleArchiveRedactions,
		ReleaseEvidenceIssueSupportBundleArchiveExclusions,
		BackupReadinessIssueVerificationSkipped,
	}
	if got := issueCodes(packet.Issues); !reflect.DeepEqual(got, wantCodes) {
		t.Fatalf("BuildReleaseEvidencePacket() issue codes = %+v, want %+v", got, wantCodes)
	}
}

func TestBuildReleaseEvidencePacketBlockedRollup(t *testing.T) {
	input := readyReleaseEvidenceInput()
	input.BackupFailureReview = BackupFailureReviewSummary{
		Status: ReviewStatusFailed,
		Issues: []ReviewIssue{{
			Code:    BackupFailureIssueAttemptsFailed,
			Status:  ReviewStatusFailed,
			Message: "one or more backup attempt steps failed",
		}},
	}
	input.RestoreChecksumReview = RestoreChecksumReviewSummary{Status: "unexpected"}

	packet := BuildReleaseEvidencePacket(input)

	if packet.Status != ReleaseEvidenceStatusBlocked {
		t.Fatalf("BuildReleaseEvidencePacket() status = %q, want blocked", packet.Status)
	}
	if !packet.OperatorReviewRequired {
		t.Fatal("BuildReleaseEvidencePacket() OperatorReviewRequired = false, want true")
	}
	want := []ReleaseEvidenceIssue{
		{Component: ReleaseEvidenceComponentBackupFailure, Code: BackupFailureIssueAttemptsFailed, Status: ReleaseEvidenceStatusBlocked, Message: "one or more backup attempt steps failed"},
		{Component: ReleaseEvidenceComponentRestoreChecksum, Code: ReleaseEvidenceIssueRestoreChecksumInvalidStatus, Status: ReleaseEvidenceStatusBlocked, Message: "restore checksum review status \"unexpected\" is invalid"},
	}
	if !reflect.DeepEqual(packet.Issues, want) {
		t.Fatalf("BuildReleaseEvidencePacket() issues = %+v, want %+v", packet.Issues, want)
	}
}

func TestMarshalReleaseEvidencePacketJSON(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	data, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}

	var got ReleaseEvidencePacket
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() returned invalid JSON: %v", err)
	}
	if got.Status != ReleaseEvidenceStatusReady || got.OperatorReviewRequired {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() packet = %+v, want ready packet", got)
	}
	if got.OperatorReviewDecision.Decision != OperatorReviewDecisionPending {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() decision = %+v, want pending", got.OperatorReviewDecision)
	}
	want, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() second error = %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() = %s, want deterministic %s", string(data), string(want))
	}
}

func TestReleaseEvidencePacketValidateAcceptsApprovedDecisionOnlyWhenReady(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	packet.OperatorReviewDecision = OperatorReviewDecision{Decision: OperatorReviewDecisionApproved, Reviewer: "ops@example.com", Timestamp: "2026-04-25T12:30:00Z", Reason: "release evidence is complete"}
	if err := packet.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want approved ready packet", err)
	}

	input := readyReleaseEvidenceInput()
	input.BackupReadinessReview = BackupReadinessSummary{
		Status: BackupReadinessStatusNeedsReview,
		Issues: []BackupReadinessIssue{{
			Code:    BackupReadinessIssueVerificationSkipped,
			Status:  BackupReadinessStatusNeedsReview,
			Message: "one or more backup verification probes were skipped",
		}},
	}
	packet = BuildReleaseEvidencePacket(input)
	packet.OperatorReviewDecision = OperatorReviewDecision{Decision: OperatorReviewDecisionApproved, Reviewer: "ops@example.com", Timestamp: "2026-04-25T12:30:00Z", Reason: "approving with outstanding review"}
	if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "approval requires ready") {
		t.Fatalf("Validate() error = %v, want approval readiness validation error", err)
	}
}

func TestReleaseEvidencePacketValidateRejectsIncompleteRejectedDecision(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	packet.OperatorReviewDecision = OperatorReviewDecision{Decision: OperatorReviewDecisionRejected, Reviewer: "ops@example.com", Timestamp: "2026-04-25T12:30:00Z"}

	if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("Validate() error = %v, want reason validation error", err)
	}
}

func TestReleaseEvidencePacketValidateRejectsOperatorReviewMismatch(t *testing.T) {
	input := readyReleaseEvidenceInput()
	input.BackupReadinessReview = BackupReadinessSummary{
		Status: BackupReadinessStatusNeedsReview,
		Issues: []BackupReadinessIssue{{
			Code:    BackupReadinessIssueVerificationSkipped,
			Status:  BackupReadinessStatusNeedsReview,
			Message: "one or more backup verification probes were skipped",
		}},
	}
	packet := BuildReleaseEvidencePacket(input)
	packet.OperatorReviewRequired = false

	if err := packet.Validate(); err == nil || !strings.Contains(err.Error(), "operatorReviewRequired") {
		t.Fatalf("Validate() error = %v, want operatorReviewRequired validation error", err)
	}
}

func TestWriteReadReleaseEvidencePacketJSONRoundTripStable(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	path := filepath.Join(t.TempDir(), "release-evidence.json")

	if err := WriteReleaseEvidencePacketJSON(path, packet); err != nil {
		t.Fatalf("WriteReleaseEvidencePacketJSON() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("WriteReleaseEvidencePacketJSON() wrote %q, want stable JSON %q", string(written), string(want))
	}

	got, err := ReadReleaseEvidencePacketJSON(path)
	if err != nil {
		t.Fatalf("ReadReleaseEvidencePacketJSON() error = %v", err)
	}
	if !reflect.DeepEqual(got, packet) {
		t.Fatalf("ReadReleaseEvidencePacketJSON() = %+v, want %+v", got, packet)
	}
}

func TestReadReleaseEvidencePacketJSONValidatesPacket(t *testing.T) {
	input := readyReleaseEvidenceInput()
	input.BackupReadinessReview = BackupReadinessSummary{
		Status: BackupReadinessStatusNeedsReview,
		Issues: []BackupReadinessIssue{{
			Code:    BackupReadinessIssueVerificationSkipped,
			Status:  BackupReadinessStatusNeedsReview,
			Message: "one or more backup verification probes were skipped",
		}},
	}
	packet := BuildReleaseEvidencePacket(input)
	packet.OperatorReviewRequired = false
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "release-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadReleaseEvidencePacketJSON(path); err == nil || !strings.Contains(err.Error(), "operatorReviewRequired") {
		t.Fatalf("ReadReleaseEvidencePacketJSON() error = %v, want operatorReviewRequired validation error", err)
	}
}

func TestReadReleaseEvidencePacketJSONRejectsUnknownFields(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	data, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidencePacketJSON() error = %v", err)
	}
	data = []byte(strings.Replace(string(data), "\n}", ",\n  \"unexpected\": true\n}", 1))
	path := filepath.Join(t.TempDir(), "release-evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadReleaseEvidencePacketJSON(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ReadReleaseEvidencePacketJSON() error = %v, want unknown field validation error", err)
	}
}

func TestReleaseEvidenceJSONPathValidationRejectsUnsafePaths(t *testing.T) {
	packet := BuildReleaseEvidencePacket(readyReleaseEvidenceInput())
	dir := t.TempDir()
	for _, path := range []string{"", " release-evidence.json", dir + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "release-evidence.json", filepath.Join(dir, "release-evidence.txt"), filepath.Join(dir, "*.json")} {
		if err := WriteReleaseEvidencePacketJSON(path, packet); err == nil {
			t.Fatalf("WriteReleaseEvidencePacketJSON(%q) error = nil, want path validation error", path)
		}
	}
}

func readyReleaseEvidenceInput() ReleaseEvidenceInput {
	return ReleaseEvidenceInput{
		SupportBundleArchiveReview: supportbundle.ArchiveReviewSummary{
			ManifestID:      "bundle-001",
			EntryCount:      1,
			BytesByCategory: map[string]int64{"log": 42},
			Decision:        supportbundle.ReviewDecisionApproved,
		},
		BackupReadinessReview:  BackupReadinessSummary{Status: BackupReadinessStatusReady},
		BackupFailureReview:    BackupFailureReviewSummary{Status: ReviewStatusOK},
		RestoreReadinessReview: RestoreReadinessSummary{Status: RestoreReadinessStatusReady},
		RestoreChecksumReview:  RestoreChecksumReviewSummary{Status: ReviewStatusOK},
	}
}

func issueCodes(issues []ReleaseEvidenceIssue) []string {
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}
