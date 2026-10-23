package backup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReviewRestoreReadinessReady(t *testing.T) {
	checkedAt := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)

	summary := ReviewRestoreReadiness(RestoreReadinessInput{
		Steps: []RestoreStep{
			{Name: "load-database", State: RestoreStepSucceeded},
			{Name: "load-config", State: RestoreStepSucceeded},
		},
		SmokeTestResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: checkedAt, Object: Object{Key: "database/postgres.dump", Bytes: 100, SHA256: "sha256:abc"}},
		},
		ChecksumSummary: ChecksumVerificationSummary{Total: 2, Verified: 2, OverallState: RestoreStepSucceeded},
		RollbackOwner:   "platform-oncall",
	})

	if summary.Status != RestoreReadinessStatusReady {
		t.Fatalf("ReviewRestoreReadiness() status = %q, want %q; issues=%+v", summary.Status, RestoreReadinessStatusReady, summary.Issues)
	}
	if summary.StepSummary.Total != 2 || summary.SmokeTestSummary.Total != 1 || summary.ChecksumSummary.Verified != 2 {
		t.Fatalf("ReviewRestoreReadiness() = %+v, want combined step, smoke, and checksum summaries", summary)
	}
}

func TestReviewRestoreReadinessBlockedIssuesAreDeterministic(t *testing.T) {
	checkedAt := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)

	summary := ReviewRestoreReadiness(RestoreReadinessInput{
		Steps: []RestoreStep{
			{Name: "load-database", State: RestoreStepFailed, ReasonCode: "postgres-load-failed"},
		},
		SmokeTestResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepFailed, CheckedAt: checkedAt, ReasonCode: "read-failed"},
		},
		ChecksumSummary: ChecksumVerificationSummary{Total: 2, Verified: 1, Failed: 1, OverallState: RestoreStepFailed, FailedReasons: map[string]int{"checksum-mismatch": 1}},
		RollbackOwner:   "TBD",
	})

	assertRestoreReadinessCodes(t, summary, []string{
		RestoreReadinessIssueStepsFailed,
		RestoreReadinessIssueSmokeFailed,
		RestoreReadinessIssueChecksumFailed,
		RestoreReadinessIssueRollbackOwnerTBD,
	})
	if summary.Status != RestoreReadinessStatusBlocked {
		t.Fatalf("ReviewRestoreReadiness() status = %q, want %q", summary.Status, RestoreReadinessStatusBlocked)
	}
}

func TestReviewRestoreReadinessNeedsReviewForIncompleteDryRun(t *testing.T) {
	checkedAt := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)

	summary := ReviewRestoreReadiness(RestoreReadinessInput{
		Steps: []RestoreStep{
			{Name: "load-database", State: RestoreStepRunning},
		},
		SmokeTestResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSkipped, CheckedAt: checkedAt},
		},
		ChecksumSummary: ChecksumVerificationSummary{Total: 2, Verified: 1, Pending: 1, OverallState: RestoreStepPending},
		RollbackOwner:   "platform-oncall",
	})

	assertRestoreReadinessCodes(t, summary, []string{
		RestoreReadinessIssueStepsIncomplete,
		RestoreReadinessIssueSmokeIncomplete,
		RestoreReadinessIssueChecksumPending,
	})
	if summary.Status != RestoreReadinessStatusNeedsReview {
		t.Fatalf("ReviewRestoreReadiness() status = %q, want %q", summary.Status, RestoreReadinessStatusNeedsReview)
	}
}

func TestReviewRestoreReadinessReportsMissingInputs(t *testing.T) {
	summary := ReviewRestoreReadiness(RestoreReadinessInput{})

	assertRestoreReadinessCodes(t, summary, []string{
		RestoreReadinessIssueStepsMissing,
		RestoreReadinessIssueSmokeMissing,
		RestoreReadinessIssueChecksumMissing,
		RestoreReadinessIssueRollbackOwnerTBD,
	})
	if summary.Status != RestoreReadinessStatusNeedsReview {
		t.Fatalf("ReviewRestoreReadiness() status = %q, want %q", summary.Status, RestoreReadinessStatusNeedsReview)
	}
}

func TestReviewRestoreReadinessReportsInvalidInputs(t *testing.T) {
	checkedAt := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)

	summary := ReviewRestoreReadiness(RestoreReadinessInput{
		Steps:            []RestoreStep{{Name: "load-database", State: RestoreStepState("done")}},
		SmokeTestResults: []RestoreSmokeProbeResult{{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: checkedAt}},
		ChecksumSummary:  ChecksumVerificationSummary{Total: 2, Verified: 1, OverallState: RestoreStepSucceeded},
		RollbackOwner:    "platform-oncall",
	})

	assertRestoreReadinessCodes(t, summary, []string{
		RestoreReadinessIssueStepsInvalid,
		RestoreReadinessIssueSmokeInvalid,
		RestoreReadinessIssueChecksumInvalid,
	})
	if summary.Status != RestoreReadinessStatusBlocked {
		t.Fatalf("ReviewRestoreReadiness() status = %q, want %q", summary.Status, RestoreReadinessStatusBlocked)
	}
}

func TestMarshalRestoreReadinessSummaryJSON(t *testing.T) {
	summary := RestoreReadinessSummary{Status: RestoreReadinessStatusNeedsReview, Issues: []RestoreReadinessIssue{{Code: RestoreReadinessIssueRollbackOwnerTBD, Status: RestoreReadinessStatusNeedsReview}}}

	data, err := MarshalRestoreReadinessSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalRestoreReadinessSummaryJSON() error = %v", err)
	}

	var got RestoreReadinessSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalRestoreReadinessSummaryJSON() returned invalid JSON: %v", err)
	}
	if got.Status != RestoreReadinessStatusNeedsReview || len(got.Issues) != 1 || got.Issues[0].Code != RestoreReadinessIssueRollbackOwnerTBD {
		t.Fatalf("MarshalRestoreReadinessSummaryJSON() = %+v, want restore readiness summary", got)
	}
}

func assertRestoreReadinessCodes(t *testing.T, summary RestoreReadinessSummary, want []string) {
	t.Helper()
	if len(summary.Issues) != len(want) {
		t.Fatalf("ReviewRestoreReadiness() issues = %+v, want codes %v", summary.Issues, want)
	}
	for i, code := range want {
		if summary.Issues[i].Code != code {
			t.Fatalf("ReviewRestoreReadiness() issues[%d].Code = %q, want %q; issues=%+v", i, summary.Issues[i].Code, code, summary.Issues)
		}
	}
}
