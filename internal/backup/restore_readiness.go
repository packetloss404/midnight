package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RestoreReadinessStatusReady       = "ready"
	RestoreReadinessStatusNeedsReview = "needs-review"
	RestoreReadinessStatusBlocked     = "blocked"

	RestoreReadinessIssueStepsInvalid     = "restore-steps-invalid"
	RestoreReadinessIssueStepsMissing     = "restore-steps-missing"
	RestoreReadinessIssueStepsFailed      = "restore-steps-failed"
	RestoreReadinessIssueStepsIncomplete  = "restore-steps-incomplete"
	RestoreReadinessIssueSmokeInvalid     = "restore-smoke-invalid"
	RestoreReadinessIssueSmokeMissing     = "restore-smoke-missing"
	RestoreReadinessIssueSmokeFailed      = "restore-smoke-failed"
	RestoreReadinessIssueSmokeIncomplete  = "restore-smoke-incomplete"
	RestoreReadinessIssueChecksumInvalid  = "checksum-verification-invalid"
	RestoreReadinessIssueChecksumMissing  = "checksum-verification-missing"
	RestoreReadinessIssueChecksumFailed   = "checksum-verification-failed"
	RestoreReadinessIssueChecksumPending  = "checksum-verification-pending"
	RestoreReadinessIssueRollbackOwnerTBD = "rollback-owner-tbd"
)

// RestoreReadinessInput is the dry-run material used to decide whether restore can proceed.
type RestoreReadinessInput struct {
	Steps            []RestoreStep               `json:"steps"`
	SmokeTestResults []RestoreSmokeProbeResult   `json:"smokeTestResults"`
	ChecksumSummary  ChecksumVerificationSummary `json:"checksumSummary"`
	RollbackOwner    string                      `json:"rollbackOwner"`
}

// RestoreReadinessIssue records one deterministic restore readiness finding.
type RestoreReadinessIssue struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// RestoreStepSummary is the release-gate aggregate for restore steps.
type RestoreStepSummary struct {
	Total         int              `json:"total"`
	Succeeded     int              `json:"succeeded"`
	Failed        int              `json:"failed"`
	Skipped       int              `json:"skipped"`
	Pending       int              `json:"pending"`
	Running       int              `json:"running"`
	OverallState  RestoreStepState `json:"overallState"`
	FailedReasons map[string]int   `json:"failedReasons"`
}

// ChecksumVerificationSummary records caller-provided checksum verification readiness.
type ChecksumVerificationSummary struct {
	Total         int              `json:"total"`
	Verified      int              `json:"verified"`
	Failed        int              `json:"failed"`
	Pending       int              `json:"pending"`
	Missing       int              `json:"missing"`
	OverallState  RestoreStepState `json:"overallState"`
	FailedReasons map[string]int   `json:"failedReasons"`
}

// RestoreReadinessSummary is a JSON-friendly operator review result for Phase 0 restore.
type RestoreReadinessSummary struct {
	Status           string                      `json:"status"`
	Issues           []RestoreReadinessIssue     `json:"issues"`
	StepSummary      RestoreStepSummary          `json:"stepSummary"`
	SmokeTestSummary VerificationResultSummary   `json:"smokeTestSummary"`
	ChecksumSummary  ChecksumVerificationSummary `json:"checksumSummary"`
	RollbackOwner    string                      `json:"rollbackOwner"`
}

// ReviewRestoreReadiness combines dry-run restore validation into a deterministic readiness status.
func ReviewRestoreReadiness(input RestoreReadinessInput) RestoreReadinessSummary {
	summary := RestoreReadinessSummary{
		Status:          RestoreReadinessStatusReady,
		ChecksumSummary: input.ChecksumSummary,
		RollbackOwner:   strings.TrimSpace(input.RollbackOwner),
	}

	stepSummary, err := SummarizeRestoreSteps(input.Steps)
	if err != nil {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueStepsInvalid, fmt.Sprintf("restore steps are invalid: %v", err)))
	} else {
		summary.StepSummary = stepSummary
		appendRestoreStepIssues(&summary, stepSummary)
	}

	smokeSummary, err := SummarizeVerificationResults(input.SmokeTestResults)
	if err != nil {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueSmokeInvalid, fmt.Sprintf("restore smoke test results are invalid: %v", err)))
	} else {
		summary.SmokeTestSummary = smokeSummary
		appendRestoreSmokeIssues(&summary, smokeSummary)
	}

	if err := input.ChecksumSummary.Validate(); err != nil {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueChecksumInvalid, fmt.Sprintf("checksum verification summary is invalid: %v", err)))
	} else {
		appendChecksumIssues(&summary, input.ChecksumSummary)
	}

	if summary.RollbackOwner == "" || strings.EqualFold(summary.RollbackOwner, "tbd") || strings.EqualFold(summary.RollbackOwner, "placeholder") {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueRollbackOwnerTBD, "rollback owner must be assigned before restore"))
	}

	summary.Status = restoreReadinessStatus(summary.Issues)
	return summary
}

// SummarizeRestoreSteps validates and aggregates restore step outcomes.
func SummarizeRestoreSteps(steps []RestoreStep) (RestoreStepSummary, error) {
	summary := RestoreStepSummary{Total: len(steps), OverallState: RestoreStepSucceeded, FailedReasons: map[string]int{}}
	for i, step := range steps {
		if err := step.Validate(); err != nil {
			return RestoreStepSummary{}, fmt.Errorf("steps[%d]: %w", i, err)
		}
		switch step.State {
		case RestoreStepSucceeded:
			summary.Succeeded++
		case RestoreStepFailed:
			summary.Failed++
			summary.FailedReasons[step.ReasonCode]++
		case RestoreStepSkipped:
			summary.Skipped++
		case RestoreStepPending:
			summary.Pending++
		case RestoreStepRunning:
			summary.Running++
		default:
			return RestoreStepSummary{}, errors.New("restore step state is invalid")
		}
	}
	if summary.Failed > 0 {
		summary.OverallState = RestoreStepFailed
	} else if summary.Running > 0 {
		summary.OverallState = RestoreStepRunning
	} else if summary.Pending > 0 {
		summary.OverallState = RestoreStepPending
	} else if summary.Skipped > 0 && summary.Succeeded == 0 {
		summary.OverallState = RestoreStepSkipped
	}
	return summary, nil
}

// Validate checks that checksum verification counts are internally consistent.
func (s ChecksumVerificationSummary) Validate() error {
	if s.Total < 0 || s.Verified < 0 || s.Failed < 0 || s.Pending < 0 || s.Missing < 0 {
		return errors.New("checksum verification counts must be non-negative")
	}
	if s.Total != s.Verified+s.Failed+s.Pending+s.Missing {
		return errors.New("checksum verification total must equal counted states")
	}
	if s.Failed > 0 && len(s.FailedReasons) == 0 {
		return errors.New("failed checksum verification requires failed reasons")
	}
	switch s.OverallState {
	case RestoreStepSucceeded, RestoreStepFailed, RestoreStepPending:
	case "":
		if s.Total != 0 {
			return errors.New("checksum verification overall state is required")
		}
	default:
		return errors.New("checksum verification overall state is invalid")
	}
	return nil
}

// MarshalRestoreReadinessSummaryJSON renders stable, human-readable restore readiness JSON.
func MarshalRestoreReadinessSummaryJSON(summary RestoreReadinessSummary) ([]byte, error) {
	return json.MarshalIndent(summary, "", "  ")
}

func appendRestoreStepIssues(summary *RestoreReadinessSummary, steps RestoreStepSummary) {
	if steps.Total == 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueStepsMissing, "restore plan has not recorded any steps"))
	}
	if steps.Failed > 0 {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueStepsFailed, "one or more restore steps failed"))
	}
	if steps.Pending > 0 || steps.Running > 0 || steps.Skipped > 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueStepsIncomplete, "one or more restore steps are not complete"))
	}
}

func appendRestoreSmokeIssues(summary *RestoreReadinessSummary, smoke VerificationResultSummary) {
	if smoke.Total == 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueSmokeMissing, "restore smoke test has not recorded any probe results"))
	}
	if smoke.Failed > 0 {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueSmokeFailed, "one or more restore smoke test probes failed"))
	}
	if smoke.Pending > 0 || smoke.Running > 0 || smoke.Skipped > 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueSmokeIncomplete, "one or more restore smoke test probes are not complete"))
	}
}

func appendChecksumIssues(summary *RestoreReadinessSummary, checksum ChecksumVerificationSummary) {
	if checksum.Total == 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueChecksumMissing, "checksum verification has not recorded any artifacts"))
	}
	if checksum.Failed > 0 {
		summary.Issues = append(summary.Issues, restoreBlockedIssue(RestoreReadinessIssueChecksumFailed, "one or more checksum verifications failed"))
	}
	if checksum.Pending > 0 || checksum.Missing > 0 {
		summary.Issues = append(summary.Issues, restoreReviewIssue(RestoreReadinessIssueChecksumPending, "one or more checksum verifications are pending or missing"))
	}
}

func restoreReadinessStatus(issues []RestoreReadinessIssue) string {
	status := RestoreReadinessStatusReady
	for _, issue := range issues {
		if issue.Status == RestoreReadinessStatusBlocked {
			return RestoreReadinessStatusBlocked
		}
		if issue.Status == RestoreReadinessStatusNeedsReview {
			status = RestoreReadinessStatusNeedsReview
		}
	}
	return status
}

func restoreBlockedIssue(code, message string) RestoreReadinessIssue {
	return RestoreReadinessIssue{Code: code, Status: RestoreReadinessStatusBlocked, Message: message}
}

func restoreReviewIssue(code, message string) RestoreReadinessIssue {
	return RestoreReadinessIssue{Code: code, Status: RestoreReadinessStatusNeedsReview, Message: message}
}
