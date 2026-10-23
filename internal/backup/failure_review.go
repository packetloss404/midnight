package backup

import (
	"encoding/json"
	"fmt"
)

const (
	ReviewStatusOK          = "ok"
	ReviewStatusFailed      = "failed"
	ReviewStatusNeedsReview = "needs-review"

	BackupFailureIssueAttemptsInvalid     = "backup-attempts-invalid"
	BackupFailureIssueAttemptsMissing     = "backup-attempts-missing"
	BackupFailureIssueAttemptsFailed      = "backup-attempts-failed"
	BackupFailureIssueAttemptsIncomplete  = "backup-attempts-incomplete"
	BackupFailureIssueVerificationInvalid = "backup-verification-invalid"
	BackupFailureIssueVerificationMissing = "backup-verification-missing"
	BackupFailureIssueVerificationFailed  = "backup-verification-failed"
	BackupFailureIssueVerificationPending = "backup-verification-pending"
	BackupFailureIssueVerificationSkipped = "backup-verification-skipped"
	RestoreChecksumIssueInvalid           = "restore-checksum-invalid"
	RestoreChecksumIssueMissing           = "restore-checksum-missing"
	RestoreChecksumIssueFailed            = "restore-checksum-failed"
	RestoreChecksumIssuePending           = "restore-checksum-pending"
)

// BackupFailureReviewInput is the post-attempt material used to classify backup health.
type BackupFailureReviewInput struct {
	Attempts            []RestoreStep             `json:"attempts"`
	VerificationSummary VerificationResultSummary `json:"verificationSummary"`
}

// ReviewIssue records one deterministic finding for JSON review output.
type ReviewIssue struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// BackupFailureReviewSummary is a JSON-friendly backup attempt classification.
type BackupFailureReviewSummary struct {
	Status              string                    `json:"status"`
	Issues              []ReviewIssue             `json:"issues"`
	AttemptSummary      RestoreStepSummary        `json:"attemptSummary"`
	VerificationSummary VerificationResultSummary `json:"verificationSummary"`
}

// RestoreChecksumReviewSummary is a JSON-friendly checksum-only restore review.
type RestoreChecksumReviewSummary struct {
	Status          string                      `json:"status"`
	Issues          []ReviewIssue               `json:"issues"`
	ChecksumSummary ChecksumVerificationSummary `json:"checksumSummary"`
}

// ReviewBackupFailure classifies backup attempt and verification summaries without I/O.
func ReviewBackupFailure(input BackupFailureReviewInput) BackupFailureReviewSummary {
	summary := BackupFailureReviewSummary{
		Status:              ReviewStatusOK,
		VerificationSummary: input.VerificationSummary,
	}

	attemptSummary, err := SummarizeRestoreSteps(input.Attempts)
	if err != nil {
		summary.Issues = append(summary.Issues, failedReviewIssue(BackupFailureIssueAttemptsInvalid, fmt.Sprintf("backup attempts are invalid: %v", err)))
	} else {
		summary.AttemptSummary = attemptSummary
		appendBackupAttemptIssues(&summary, attemptSummary)
	}

	if err := input.VerificationSummary.Validate(); err != nil {
		summary.Issues = append(summary.Issues, failedReviewIssue(BackupFailureIssueVerificationInvalid, fmt.Sprintf("backup verification summary is invalid: %v", err)))
	} else {
		appendBackupVerificationSummaryIssues(&summary, input.VerificationSummary)
	}

	summary.Status = reviewStatus(summary.Issues)
	return summary
}

// ReviewRestoreChecksum classifies checksum-only restore validation for D-15 review gates.
func ReviewRestoreChecksum(checksum ChecksumVerificationSummary) RestoreChecksumReviewSummary {
	summary := RestoreChecksumReviewSummary{Status: ReviewStatusOK, ChecksumSummary: checksum}
	if err := checksum.Validate(); err != nil {
		summary.Issues = append(summary.Issues, failedReviewIssue(RestoreChecksumIssueInvalid, fmt.Sprintf("restore checksum summary is invalid: %v", err)))
	} else {
		appendRestoreChecksumReviewIssues(&summary, checksum)
	}
	summary.Status = reviewStatus(summary.Issues)
	return summary
}

// MarshalBackupFailureReviewSummaryJSON renders stable, human-readable backup review JSON.
func MarshalBackupFailureReviewSummaryJSON(summary BackupFailureReviewSummary) ([]byte, error) {
	return json.MarshalIndent(summary, "", "  ")
}

// MarshalRestoreChecksumReviewSummaryJSON renders stable, human-readable checksum review JSON.
func MarshalRestoreChecksumReviewSummaryJSON(summary RestoreChecksumReviewSummary) ([]byte, error) {
	return json.MarshalIndent(summary, "", "  ")
}

// Validate checks that verification summary counts are internally consistent.
func (s VerificationResultSummary) Validate() error {
	if s.Total < 0 || s.Succeeded < 0 || s.Failed < 0 || s.Skipped < 0 || s.Pending < 0 || s.Running < 0 {
		return fmt.Errorf("verification summary counts must be non-negative")
	}
	if s.Total != s.Succeeded+s.Failed+s.Skipped+s.Pending+s.Running {
		return fmt.Errorf("verification summary total must equal counted states")
	}
	if s.Failed > 0 && len(s.FailedReasons) == 0 {
		return fmt.Errorf("failed verification summary requires failed reasons")
	}
	switch s.OverallState {
	case RestoreStepSucceeded, RestoreStepFailed, RestoreStepPending, RestoreStepRunning, RestoreStepSkipped:
	case "":
		if s.Total != 0 {
			return fmt.Errorf("verification summary overall state is required")
		}
	default:
		return fmt.Errorf("verification summary overall state is invalid")
	}
	return nil
}

func appendBackupAttemptIssues(summary *BackupFailureReviewSummary, attempts RestoreStepSummary) {
	if attempts.Total == 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(BackupFailureIssueAttemptsMissing, "backup attempt has not recorded any steps"))
	}
	if attempts.Failed > 0 {
		summary.Issues = append(summary.Issues, failedReviewIssue(BackupFailureIssueAttemptsFailed, "one or more backup attempt steps failed"))
	}
	if attempts.Pending > 0 || attempts.Running > 0 || attempts.Skipped > 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(BackupFailureIssueAttemptsIncomplete, "one or more backup attempt steps are not complete"))
	}
}

func appendBackupVerificationSummaryIssues(summary *BackupFailureReviewSummary, verification VerificationResultSummary) {
	if verification.Total == 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(BackupFailureIssueVerificationMissing, "backup verification summary has not recorded any probes"))
	}
	if verification.Failed > 0 {
		summary.Issues = append(summary.Issues, failedReviewIssue(BackupFailureIssueVerificationFailed, "one or more backup verification probes failed"))
	}
	if verification.Pending > 0 || verification.Running > 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(BackupFailureIssueVerificationPending, "one or more backup verification probes are not complete"))
	}
	if verification.Skipped > 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(BackupFailureIssueVerificationSkipped, "one or more backup verification probes were skipped"))
	}
}

func appendRestoreChecksumReviewIssues(summary *RestoreChecksumReviewSummary, checksum ChecksumVerificationSummary) {
	if checksum.Total == 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(RestoreChecksumIssueMissing, "restore checksum summary has not recorded any artifacts"))
	}
	if checksum.Failed > 0 {
		summary.Issues = append(summary.Issues, failedReviewIssue(RestoreChecksumIssueFailed, "one or more restore checksum verifications failed"))
	}
	if checksum.Pending > 0 || checksum.Missing > 0 {
		summary.Issues = append(summary.Issues, needsReviewIssue(RestoreChecksumIssuePending, "one or more restore checksum verifications are pending or missing"))
	}
}

func reviewStatus(issues []ReviewIssue) string {
	status := ReviewStatusOK
	for _, issue := range issues {
		if issue.Status == ReviewStatusFailed {
			return ReviewStatusFailed
		}
		if issue.Status == ReviewStatusNeedsReview {
			status = ReviewStatusNeedsReview
		}
	}
	return status
}

func failedReviewIssue(code, message string) ReviewIssue {
	return ReviewIssue{Code: code, Status: ReviewStatusFailed, Message: message}
}

func needsReviewIssue(code, message string) ReviewIssue {
	return ReviewIssue{Code: code, Status: ReviewStatusNeedsReview, Message: message}
}
