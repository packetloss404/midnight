package backup

import (
	"encoding/json"
	"fmt"
)

const (
	BackupReadinessStatusReady       = "ready"
	BackupReadinessStatusNeedsReview = "needs-review"
	BackupReadinessStatusBlocked     = "blocked"

	BackupReadinessIssueManifestInvalid       = "manifest-invalid"
	BackupReadinessIssueDestinationURIInvalid = "destination-uri-invalid"
	BackupReadinessIssuePassphraseRequired    = "passphrase-required"
	BackupReadinessIssueVerificationInvalid   = "verification-invalid"
	BackupReadinessIssueVerificationFailed    = "verification-failed"
	BackupReadinessIssueVerificationPending   = "verification-pending"
	BackupReadinessIssueVerificationSkipped   = "verification-skipped"
	BackupReadinessIssueVerificationMissing   = "verification-missing"
)

// BackupReadinessInput is the dry-run material used to decide whether a backup can proceed.
type BackupReadinessInput struct {
	Manifest            ExportManifest            `json:"manifest"`
	DestinationURI      string                    `json:"destinationUri"`
	VerificationResults []RestoreSmokeProbeResult `json:"verificationResults"`
}

// BackupReadinessIssue records one deterministic release-gate finding.
type BackupReadinessIssue struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// BackupReadinessSummary is a JSON-friendly operator review result for Phase 0 backups.
type BackupReadinessSummary struct {
	Status              string                    `json:"status"`
	Issues              []BackupReadinessIssue    `json:"issues"`
	ManifestValid       bool                      `json:"manifestValid"`
	DestinationURI      string                    `json:"destinationUri"`
	DestinationPath     string                    `json:"destinationPath,omitempty"`
	DestinationValid    bool                      `json:"destinationValid"`
	PassphraseRequired  bool                      `json:"passphraseRequired"`
	PassphraseStatus    string                    `json:"passphraseStatus"`
	VerificationSummary VerificationResultSummary `json:"verificationSummary"`
}

// ReviewBackupReadiness combines dry-run validation into a deterministic readiness status.
func ReviewBackupReadiness(input BackupReadinessInput) BackupReadinessSummary {
	summary := BackupReadinessSummary{
		Status:             BackupReadinessStatusReady,
		DestinationURI:     input.DestinationURI,
		PassphraseRequired: input.Manifest.PassphraseRequired,
		PassphraseStatus:   input.Manifest.PassphraseStatus,
	}

	if err := input.Manifest.Validate(); err != nil {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueManifestInvalid, fmt.Sprintf("export manifest is invalid: %v", err)))
	} else {
		summary.ManifestValid = true
	}

	if path, err := ParseLocalURI(input.DestinationURI); err != nil {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueDestinationURIInvalid, fmt.Sprintf("destination URI is invalid: %v", err)))
	} else {
		summary.DestinationValid = true
		summary.DestinationPath = path
	}

	if input.Manifest.PassphraseRequired && input.Manifest.PassphraseStatus != PassphraseStatusProvided {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssuePassphraseRequired, "operator passphrase must be provided before backup export"))
	}

	verificationSummary, err := SummarizeVerificationResults(input.VerificationResults)
	if err != nil {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueVerificationInvalid, fmt.Sprintf("verification results are invalid: %v", err)))
	} else {
		summary.VerificationSummary = verificationSummary
		appendVerificationIssues(&summary, verificationSummary)
	}

	summary.Status = readinessStatus(summary.Issues)
	return summary
}

// MarshalBackupReadinessSummaryJSON renders stable, human-readable readiness JSON.
func MarshalBackupReadinessSummaryJSON(summary BackupReadinessSummary) ([]byte, error) {
	return json.MarshalIndent(summary, "", "  ")
}

func appendVerificationIssues(summary *BackupReadinessSummary, verification VerificationResultSummary) {
	if verification.Total == 0 {
		summary.Issues = append(summary.Issues, reviewIssue(BackupReadinessIssueVerificationMissing, "backup verification has not recorded any probe results"))
	}
	if verification.Failed > 0 {
		summary.Issues = append(summary.Issues, blockedIssue(BackupReadinessIssueVerificationFailed, "one or more backup verification probes failed"))
	}
	if verification.Pending > 0 || verification.Running > 0 {
		summary.Issues = append(summary.Issues, reviewIssue(BackupReadinessIssueVerificationPending, "one or more backup verification probes are not complete"))
	}
	if verification.Skipped > 0 {
		summary.Issues = append(summary.Issues, reviewIssue(BackupReadinessIssueVerificationSkipped, "one or more backup verification probes were skipped"))
	}
}

func readinessStatus(issues []BackupReadinessIssue) string {
	status := BackupReadinessStatusReady
	for _, issue := range issues {
		if issue.Status == BackupReadinessStatusBlocked {
			return BackupReadinessStatusBlocked
		}
		if issue.Status == BackupReadinessStatusNeedsReview {
			status = BackupReadinessStatusNeedsReview
		}
	}
	return status
}

func blockedIssue(code, message string) BackupReadinessIssue {
	return BackupReadinessIssue{Code: code, Status: BackupReadinessStatusBlocked, Message: message}
}

func reviewIssue(code, message string) BackupReadinessIssue {
	return BackupReadinessIssue{Code: code, Status: BackupReadinessStatusNeedsReview, Message: message}
}
