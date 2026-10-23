package backup

import (
	"encoding/json"
	"testing"
)

func TestReviewBackupFailureOK(t *testing.T) {
	summary := ReviewBackupFailure(BackupFailureReviewInput{
		Attempts: []RestoreStep{
			{Name: "export-manifest", State: RestoreStepSucceeded},
			{Name: "write-artifacts", State: RestoreStepSucceeded},
		},
		VerificationSummary: VerificationResultSummary{Total: 2, Succeeded: 2, OverallState: RestoreStepSucceeded},
	})

	if summary.Status != ReviewStatusOK {
		t.Fatalf("ReviewBackupFailure() status = %q, want %q; issues=%+v", summary.Status, ReviewStatusOK, summary.Issues)
	}
	if summary.AttemptSummary.Total != 2 || summary.VerificationSummary.Succeeded != 2 {
		t.Fatalf("ReviewBackupFailure() = %+v, want attempt and verification summaries", summary)
	}
}

func TestReviewBackupFailureFailedIssuesAreDeterministic(t *testing.T) {
	summary := ReviewBackupFailure(BackupFailureReviewInput{
		Attempts: []RestoreStep{
			{Name: "write-artifacts", State: RestoreStepFailed, ReasonCode: "destination-write-failed"},
		},
		VerificationSummary: VerificationResultSummary{Total: 1, Failed: 1, OverallState: RestoreStepFailed, FailedReasons: map[string]int{"checksum-mismatch": 1}},
	})

	assertReviewCodes(t, summary.Issues, []string{
		BackupFailureIssueAttemptsFailed,
		BackupFailureIssueVerificationFailed,
	})
	if summary.Status != ReviewStatusFailed {
		t.Fatalf("ReviewBackupFailure() status = %q, want %q", summary.Status, ReviewStatusFailed)
	}
}

func TestReviewBackupFailureNeedsReviewForIncompleteInputs(t *testing.T) {
	summary := ReviewBackupFailure(BackupFailureReviewInput{
		Attempts: []RestoreStep{
			{Name: "write-artifacts", State: RestoreStepRunning},
		},
		VerificationSummary: VerificationResultSummary{Total: 2, Pending: 1, Skipped: 1, OverallState: RestoreStepPending},
	})

	assertReviewCodes(t, summary.Issues, []string{
		BackupFailureIssueAttemptsIncomplete,
		BackupFailureIssueVerificationPending,
		BackupFailureIssueVerificationSkipped,
	})
	if summary.Status != ReviewStatusNeedsReview {
		t.Fatalf("ReviewBackupFailure() status = %q, want %q", summary.Status, ReviewStatusNeedsReview)
	}
}

func TestReviewBackupFailureReportsMissingInputs(t *testing.T) {
	summary := ReviewBackupFailure(BackupFailureReviewInput{})

	assertReviewCodes(t, summary.Issues, []string{
		BackupFailureIssueAttemptsMissing,
		BackupFailureIssueVerificationMissing,
	})
	if summary.Status != ReviewStatusNeedsReview {
		t.Fatalf("ReviewBackupFailure() status = %q, want %q", summary.Status, ReviewStatusNeedsReview)
	}
}

func TestReviewBackupFailureReportsInvalidInputs(t *testing.T) {
	summary := ReviewBackupFailure(BackupFailureReviewInput{
		Attempts:            []RestoreStep{{Name: "write-artifacts", State: RestoreStepState("done")}},
		VerificationSummary: VerificationResultSummary{Total: 1, Succeeded: 1, OverallState: RestoreStepState("done")},
	})

	assertReviewCodes(t, summary.Issues, []string{
		BackupFailureIssueAttemptsInvalid,
		BackupFailureIssueVerificationInvalid,
	})
	if summary.Status != ReviewStatusFailed {
		t.Fatalf("ReviewBackupFailure() status = %q, want %q", summary.Status, ReviewStatusFailed)
	}
}

func TestReviewRestoreChecksumShortcut(t *testing.T) {
	summary := ReviewRestoreChecksum(ChecksumVerificationSummary{Total: 3, Verified: 3, OverallState: RestoreStepSucceeded})

	if summary.Status != ReviewStatusOK {
		t.Fatalf("ReviewRestoreChecksum() status = %q, want %q; issues=%+v", summary.Status, ReviewStatusOK, summary.Issues)
	}
}

func TestReviewRestoreChecksumShortcutIssuesAreDeterministic(t *testing.T) {
	summary := ReviewRestoreChecksum(ChecksumVerificationSummary{Total: 2, Verified: 1, Failed: 1, OverallState: RestoreStepFailed, FailedReasons: map[string]int{"checksum-mismatch": 1}})

	assertReviewCodes(t, summary.Issues, []string{RestoreChecksumIssueFailed})
	if summary.Status != ReviewStatusFailed {
		t.Fatalf("ReviewRestoreChecksum() status = %q, want %q", summary.Status, ReviewStatusFailed)
	}
}

func TestReviewRestoreChecksumShortcutNeedsReview(t *testing.T) {
	summary := ReviewRestoreChecksum(ChecksumVerificationSummary{Total: 2, Verified: 1, Missing: 1, OverallState: RestoreStepPending})

	assertReviewCodes(t, summary.Issues, []string{RestoreChecksumIssuePending})
	if summary.Status != ReviewStatusNeedsReview {
		t.Fatalf("ReviewRestoreChecksum() status = %q, want %q", summary.Status, ReviewStatusNeedsReview)
	}
}

func TestReviewRestoreChecksumShortcutInvalid(t *testing.T) {
	summary := ReviewRestoreChecksum(ChecksumVerificationSummary{Total: 2, Verified: 1, OverallState: RestoreStepSucceeded})

	assertReviewCodes(t, summary.Issues, []string{RestoreChecksumIssueInvalid})
	if summary.Status != ReviewStatusFailed {
		t.Fatalf("ReviewRestoreChecksum() status = %q, want %q", summary.Status, ReviewStatusFailed)
	}
}

func TestMarshalBackupFailureReviewSummaryJSON(t *testing.T) {
	summary := BackupFailureReviewSummary{Status: ReviewStatusNeedsReview, Issues: []ReviewIssue{{Code: BackupFailureIssueAttemptsMissing, Status: ReviewStatusNeedsReview}}}

	data, err := MarshalBackupFailureReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalBackupFailureReviewSummaryJSON() error = %v", err)
	}

	var got BackupFailureReviewSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalBackupFailureReviewSummaryJSON() returned invalid JSON: %v", err)
	}
	if got.Status != ReviewStatusNeedsReview || len(got.Issues) != 1 || got.Issues[0].Code != BackupFailureIssueAttemptsMissing {
		t.Fatalf("MarshalBackupFailureReviewSummaryJSON() = %+v, want backup failure review summary", got)
	}
}

func TestMarshalRestoreChecksumReviewSummaryJSON(t *testing.T) {
	summary := RestoreChecksumReviewSummary{Status: ReviewStatusNeedsReview, Issues: []ReviewIssue{{Code: RestoreChecksumIssueMissing, Status: ReviewStatusNeedsReview}}}

	data, err := MarshalRestoreChecksumReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalRestoreChecksumReviewSummaryJSON() error = %v", err)
	}

	var got RestoreChecksumReviewSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalRestoreChecksumReviewSummaryJSON() returned invalid JSON: %v", err)
	}
	if got.Status != ReviewStatusNeedsReview || len(got.Issues) != 1 || got.Issues[0].Code != RestoreChecksumIssueMissing {
		t.Fatalf("MarshalRestoreChecksumReviewSummaryJSON() = %+v, want restore checksum review summary", got)
	}
}

func assertReviewCodes(t *testing.T, issues []ReviewIssue, want []string) {
	t.Helper()
	if len(issues) != len(want) {
		t.Fatalf("review issues = %+v, want codes %v", issues, want)
	}
	for i, code := range want {
		if issues[i].Code != code {
			t.Fatalf("review issues[%d].Code = %q, want %q; issues=%+v", i, issues[i].Code, code, issues)
		}
	}
}
