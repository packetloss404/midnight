package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/observability"
	"github.com/midnight/midnight/internal/supportbundle"
)

func TestBuildEvidenceIndexReady(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}

	if index.Status != EvidenceIndexStatusReady || index.OperatorReviewRequired {
		t.Fatalf("BuildEvidenceIndex() = %+v, want ready without operator review", index)
	}
	wantNames := []string{
		EvidenceReferenceSupportBundleManifest,
		EvidenceReferenceBackupExportManifest,
		EvidenceReferenceBackupReleaseEvidence,
		EvidenceReferenceAlertPackagingEvidence,
	}
	if got := evidenceReferenceNames(index.References); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("BuildEvidenceIndex() references = %+v, want %+v", got, wantNames)
	}
}

func TestBuildEvidenceIndexNeedsReviewForExportPassphraseAndReleaseEvidence(t *testing.T) {
	input := readyEvidenceIndexInput(t)
	input.BackupExportManifest.PassphraseStatus = PassphraseStatusMissing
	releaseInput := readyReleaseEvidenceInput()
	releaseInput.SupportBundleArchiveReview.Decision = supportbundle.ReviewDecisionNeedsReview
	input.BackupReleaseEvidence = BuildReleaseEvidencePacket(releaseInput)

	index, err := BuildEvidenceIndex(input)
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}

	if index.Status != EvidenceIndexStatusNeedsReview || !index.OperatorReviewRequired {
		t.Fatalf("BuildEvidenceIndex() = %+v, want needs-review with operator review", index)
	}
	if !index.References[1].OperatorReviewRequired || !index.References[2].OperatorReviewRequired {
		t.Fatalf("BuildEvidenceIndex() references = %+v, want export and release review required", index.References)
	}
}

func TestBuildEvidenceIndexBlockedForAlertPackagingEvidence(t *testing.T) {
	input := readyEvidenceIndexInput(t)
	input.AlertPackagingEvidence = observability.AlertPackagingEvidence{
		Path:             "deploy/prometheus/alerts.yaml",
		Status:           observability.AlertPackagingEvidenceStatusBlocked,
		AlertCount:       len(observability.LockedAlerts),
		LockedAlertCount: len(observability.LockedAlerts),
		Issues:           []observability.AlertPackagingEvidenceIssue{{Code: observability.AlertPackagingEvidenceIssueLockedAlertsInvalid, Message: "drift"}},
	}

	index, err := BuildEvidenceIndex(input)
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}

	if index.Status != EvidenceIndexStatusBlocked || !index.OperatorReviewRequired || !index.References[3].OperatorReviewRequired {
		t.Fatalf("BuildEvidenceIndex() = %+v, want blocked alert packaging reference", index)
	}
}

func TestEvidenceIndexValidateRejectsUnsafePath(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.References[0].Path = "../support/manifest.json"

	if err := index.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want unsafe path error")
	}
}

func TestEvidenceIndexValidateRejectsStatusRollupMismatch(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.Status = EvidenceIndexStatusNeedsReview
	index.OperatorReviewRequired = true

	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "does not match references") {
		t.Fatalf("Validate() error = %v, want status rollup validation error", err)
	}
}

func TestEvidenceIndexValidateRejectsOperatorReviewMismatch(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.References[1].Status = EvidenceIndexStatusNeedsReview
	index.Status = EvidenceIndexStatusNeedsReview

	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "operatorReviewRequired") {
		t.Fatalf("Validate() error = %v, want operator review validation error", err)
	}
}

func TestEvidenceIndexReferenceValidateChecksSHA256WhenPresent(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "bundle-manifest.json")
	data := []byte(`{"id":"bundle-001"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	index.References[0].SHA256 = sha256ForExplicitEvidenceFile(t, path)
	if err := index.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	index.References[0].SHA256 = "sha256:" + index.References[0].SHA256
	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("Validate() error = %v, want sha256 format error", err)
	}
}

func TestMarshalEvidenceIndexJSONIsDeterministic(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	data, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	want := `{
  "status": "ready",
  "operatorReviewRequired": false,
  "operatorReviewDecision": {
    "decision": "pending"
  },
  "references": [
    {
      "name": "support-bundle-manifest",
      "path": "support/bundle-manifest.json",
      "id": "bundle-001",
      "status": "ready",
      "operatorReviewRequired": false
    },
    {
      "name": "backup-export-manifest",
      "path": "backup/export-manifest.json",
      "id": "backup-001",
      "status": "ready",
      "operatorReviewRequired": false
    },
    {
      "name": "backup-release-evidence",
      "path": "backup/release-evidence.json",
      "status": "ready",
      "operatorReviewRequired": false
    },
    {
      "name": "alert-packaging-evidence",
      "path": "observability/alert-packaging-evidence.json",
      "status": "ready",
      "operatorReviewRequired": false
    }
  ]
}`
	if string(data) != want {
		t.Fatalf("MarshalEvidenceIndexJSON() = %s, want %s", string(data), want)
	}

	var got EvidenceIndex
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() returned invalid JSON: %v", err)
	}
}

func TestEvidenceIndexValidateAcceptsApprovedDecisionOnlyWhenReady(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.OperatorReviewDecision = OperatorReviewDecision{Decision: OperatorReviewDecisionApproved, Reviewer: "ops@example.com", Timestamp: "2026-04-25T12:30:00Z", Reason: "release evidence is complete"}
	if err := index.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want approved ready index", err)
	}

	index.References[1].Status = EvidenceIndexStatusNeedsReview
	index.References[1].OperatorReviewRequired = true
	index.Status = EvidenceIndexStatusNeedsReview
	index.OperatorReviewRequired = true
	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "approval requires ready") {
		t.Fatalf("Validate() error = %v, want approval readiness validation error", err)
	}
}

func TestEvidenceIndexValidateRejectsPendingDecisionMetadata(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.OperatorReviewDecision = OperatorReviewDecision{Decision: OperatorReviewDecisionPending, Reviewer: "ops@example.com"}

	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "pending operator review decision") {
		t.Fatalf("Validate() error = %v, want pending metadata validation error", err)
	}
}

func TestWriteReadEvidenceIndexJSONRoundTrip(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence-index.json")

	if err := WriteEvidenceIndexJSON(path, index); err != nil {
		t.Fatalf("WriteEvidenceIndexJSON() error = %v", err)
	}
	got, err := ReadEvidenceIndexJSON(path)
	if err != nil {
		t.Fatalf("ReadEvidenceIndexJSON() error = %v", err)
	}
	if !reflect.DeepEqual(got, index) {
		t.Fatalf("ReadEvidenceIndexJSON() = %+v, want %+v", got, index)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("WriteEvidenceIndexJSON() wrote %s, want %s", string(data), string(want))
	}
}

func TestWriteEvidenceIndexJSONRejectsNonJSONPath(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence-index.txt")

	if err := WriteEvidenceIndexJSON(path, index); err == nil || !strings.Contains(err.Error(), ".json") {
		t.Fatalf("WriteEvidenceIndexJSON() error = %v, want .json path error", err)
	}
}

func TestWriteEvidenceIndexJSONRejectsMissingParent(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "missing", "evidence-index.json")

	if err := WriteEvidenceIndexJSON(path, index); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("WriteEvidenceIndexJSON() error = %v, want parent directory error", err)
	}
}

func TestReadEvidenceIndexJSONRejectsInvalidPath(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"non-json":       filepath.Join(dir, "evidence-index.txt"),
		"missing-parent": filepath.Join(dir, "missing", "evidence-index.json"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadEvidenceIndexJSON(path); err == nil {
				t.Fatal("ReadEvidenceIndexJSON() error = nil, want path validation error")
			}
		})
	}
}

func TestReadEvidenceIndexJSONRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence-index.json")
	if err := os.WriteFile(path, []byte(`{"status":"ready","operatorReviewRequired":false,"references":[],"unexpected":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadEvidenceIndexJSON(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ReadEvidenceIndexJSON() error = %v, want unknown field error", err)
	}
}

func TestReadEvidenceIndexJSONRejectsMultipleDocuments(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	data, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence-index.json")
	if err := os.WriteFile(path, append(data, []byte(` {}`)...), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadEvidenceIndexJSON(path); err == nil || !strings.Contains(err.Error(), "one document") {
		t.Fatalf("ReadEvidenceIndexJSON() error = %v, want one document error", err)
	}
}

func TestReadEvidenceIndexJSONValidatesIndex(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.References[0].Path = "../support/bundle-manifest.json"
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence-index.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := ReadEvidenceIndexJSON(path); err == nil || !strings.Contains(err.Error(), "safe relative slash path") {
		t.Fatalf("ReadEvidenceIndexJSON() error = %v, want validation error", err)
	}
}

func TestEvidenceIndexValidateRejectsAlertPackagingNeedsReview(t *testing.T) {
	index, err := BuildEvidenceIndex(readyEvidenceIndexInput(t))
	if err != nil {
		t.Fatalf("BuildEvidenceIndex() error = %v", err)
	}
	index.References[3].Status = EvidenceIndexStatusNeedsReview
	index.References[3].OperatorReviewRequired = true
	index.Status = EvidenceIndexStatusNeedsReview
	index.OperatorReviewRequired = true

	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "alert packaging evidence status") {
		t.Fatalf("Validate() error = %v, want alert packaging evidence status error", err)
	}
}

func readyEvidenceIndexInput(t *testing.T) EvidenceIndexInput {
	t.Helper()
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	supportManifest, err := supportbundle.BuildPreviewManifest("bundle-001", createdAt, supportbundle.Selector{Logs: true}, []supportbundle.PreviewFile{{Path: "logs/api.log", Kind: "log", Bytes: 42}})
	if err != nil {
		t.Fatalf("BuildPreviewManifest() error = %v", err)
	}
	exportManifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	return EvidenceIndexInput{
		SupportBundleManifestPath:  "support/bundle-manifest.json",
		SupportBundleManifest:      supportManifest,
		BackupExportManifestPath:   "backup/export-manifest.json",
		BackupExportManifest:       exportManifest,
		BackupReleaseEvidencePath:  "backup/release-evidence.json",
		BackupReleaseEvidence:      BuildReleaseEvidencePacket(readyReleaseEvidenceInput()),
		AlertPackagingEvidencePath: "observability/alert-packaging-evidence.json",
		AlertPackagingEvidence: observability.AlertPackagingEvidence{
			Path:             "deploy/prometheus/alerts.yaml",
			Status:           observability.AlertPackagingEvidenceStatusReady,
			AlertCount:       len(observability.LockedAlerts),
			LockedAlertCount: len(observability.LockedAlerts),
		},
	}
}

func evidenceReferenceNames(references []EvidenceIndexReference) []string {
	names := make([]string, 0, len(references))
	for _, reference := range references {
		names = append(names, reference.Name)
	}
	return names
}

func sha256ForExplicitEvidenceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
