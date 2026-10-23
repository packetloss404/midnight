package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/supportbundle"
)

const (
	ReleaseEvidenceStatusReady       = "ready"
	ReleaseEvidenceStatusNeedsReview = "needs-review"
	ReleaseEvidenceStatusBlocked     = "blocked"

	OperatorReviewDecisionPending  = "pending"
	OperatorReviewDecisionApproved = "approved"
	OperatorReviewDecisionRejected = "rejected"

	ReleaseEvidenceComponentSupportBundleArchive = "support-bundle-archive"
	ReleaseEvidenceComponentBackupReadiness      = "backup-readiness"
	ReleaseEvidenceComponentBackupFailure        = "backup-failure"
	ReleaseEvidenceComponentRestoreReadiness     = "restore-readiness"
	ReleaseEvidenceComponentRestoreChecksum      = "restore-checksum"

	ReleaseEvidenceIssueSupportBundleArchiveInvalid     = "support-bundle-archive-invalid"
	ReleaseEvidenceIssueSupportBundleArchiveNeedsReview = "support-bundle-archive-needs-review"
	ReleaseEvidenceIssueSupportBundleArchiveRejected    = "support-bundle-archive-rejected"
	ReleaseEvidenceIssueSupportBundleArchiveOverBudget  = "support-bundle-archive-over-budget"
	ReleaseEvidenceIssueSupportBundleArchiveRedactions  = "support-bundle-archive-redactions"
	ReleaseEvidenceIssueSupportBundleArchiveExclusions  = "support-bundle-archive-exclusions"
	ReleaseEvidenceIssueBackupReadinessInvalidStatus    = "backup-readiness-invalid-status"
	ReleaseEvidenceIssueBackupFailureInvalidStatus      = "backup-failure-invalid-status"
	ReleaseEvidenceIssueRestoreReadinessInvalidStatus   = "restore-readiness-invalid-status"
	ReleaseEvidenceIssueRestoreChecksumInvalidStatus    = "restore-checksum-invalid-status"
)

// ReleaseEvidenceInput combines already-built, pure review summaries for a release gate.
type ReleaseEvidenceInput struct {
	SupportBundleArchiveReview supportbundle.ArchiveReviewSummary `json:"supportBundleArchiveReview"`
	BackupReadinessReview      BackupReadinessSummary             `json:"backupReadinessReview"`
	BackupFailureReview        BackupFailureReviewSummary         `json:"backupFailureReview"`
	RestoreReadinessReview     RestoreReadinessSummary            `json:"restoreReadinessReview"`
	RestoreChecksumReview      RestoreChecksumReviewSummary       `json:"restoreChecksumReview"`
}

// ReleaseEvidenceIssue records one normalized release evidence finding.
type ReleaseEvidenceIssue struct {
	Component string `json:"component"`
	Code      string `json:"code"`
	Status    string `json:"status"`
	Message   string `json:"message"`
}

// OperatorReviewDecision records an auditable manual decision for release evidence.
type OperatorReviewDecision struct {
	Decision  string `json:"decision"`
	Reviewer  string `json:"reviewer,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ReleaseEvidencePacket is the deterministic JSON-friendly Phase 0 release gate packet.
type ReleaseEvidencePacket struct {
	Status                     string                             `json:"status"`
	OperatorReviewRequired     bool                               `json:"operatorReviewRequired"`
	OperatorReviewDecision     OperatorReviewDecision             `json:"operatorReviewDecision"`
	Issues                     []ReleaseEvidenceIssue             `json:"issues"`
	SupportBundleArchiveReview supportbundle.ArchiveReviewSummary `json:"supportBundleArchiveReview"`
	BackupReadinessReview      BackupReadinessSummary             `json:"backupReadinessReview"`
	BackupFailureReview        BackupFailureReviewSummary         `json:"backupFailureReview"`
	RestoreReadinessReview     RestoreReadinessSummary            `json:"restoreReadinessReview"`
	RestoreChecksumReview      RestoreChecksumReviewSummary       `json:"restoreChecksumReview"`
}

// BuildReleaseEvidencePacket rolls up support, backup, and restore reviews without doing I/O.
func BuildReleaseEvidencePacket(input ReleaseEvidenceInput) ReleaseEvidencePacket {
	packet := ReleaseEvidencePacket{
		Status:                     ReleaseEvidenceStatusReady,
		OperatorReviewDecision:     OperatorReviewDecision{Decision: OperatorReviewDecisionPending},
		SupportBundleArchiveReview: input.SupportBundleArchiveReview,
		BackupReadinessReview:      input.BackupReadinessReview,
		BackupFailureReview:        input.BackupFailureReview,
		RestoreReadinessReview:     input.RestoreReadinessReview,
		RestoreChecksumReview:      input.RestoreChecksumReview,
	}

	appendSupportBundleArchiveIssues(&packet, input.SupportBundleArchiveReview)
	appendBackupReadinessIssues(&packet, input.BackupReadinessReview)
	appendBackupFailureIssues(&packet, input.BackupFailureReview)
	appendRestoreReadinessIssues(&packet, input.RestoreReadinessReview)
	appendRestoreChecksumIssues(&packet, input.RestoreChecksumReview)

	packet.Status = releaseEvidenceStatus(packet.Issues)
	packet.OperatorReviewRequired = packet.Status != ReleaseEvidenceStatusReady
	return packet
}

// MarshalReleaseEvidencePacketJSON renders stable, human-readable release evidence JSON.
func MarshalReleaseEvidencePacketJSON(packet ReleaseEvidencePacket) ([]byte, error) {
	if err := packet.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(packet, "", "  ")
}

// WriteReleaseEvidencePacketJSON validates and writes a stable release evidence JSON file.
func WriteReleaseEvidencePacketJSON(path string, packet ReleaseEvidencePacket) error {
	if err := validateReleaseEvidenceJSONPath(path); err != nil {
		return err
	}
	data, err := MarshalReleaseEvidencePacketJSON(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ReadReleaseEvidencePacketJSON reads and validates a release evidence JSON file.
func ReadReleaseEvidencePacketJSON(path string) (ReleaseEvidencePacket, error) {
	if err := validateReleaseEvidenceJSONPath(path); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var packet ReleaseEvidencePacket
	if err := decoder.Decode(&packet); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	var trailing struct{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ReleaseEvidencePacket{}, errors.New("release evidence JSON must contain one document")
	}
	if err := packet.Validate(); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

// Validate checks that a release evidence packet is canonical for its embedded reviews.
func (p ReleaseEvidencePacket) Validate() error {
	if !validReleaseStatus(p.Status) {
		return fmt.Errorf("release evidence status %q is invalid", p.Status)
	}
	for i, issue := range p.Issues {
		if err := issue.Validate(); err != nil {
			return fmt.Errorf("issues[%d]: %w", i, err)
		}
	}
	want := BuildReleaseEvidencePacket(ReleaseEvidenceInput{
		SupportBundleArchiveReview: p.SupportBundleArchiveReview,
		BackupReadinessReview:      p.BackupReadinessReview,
		BackupFailureReview:        p.BackupFailureReview,
		RestoreReadinessReview:     p.RestoreReadinessReview,
		RestoreChecksumReview:      p.RestoreChecksumReview,
	})
	if p.Status != want.Status {
		return fmt.Errorf("release evidence status %q does not match issues status %q", p.Status, want.Status)
	}
	if p.OperatorReviewRequired != (p.Status != ReleaseEvidenceStatusReady) {
		return errors.New("release evidence operatorReviewRequired must match non-ready status")
	}
	if err := p.OperatorReviewDecision.ValidateForStatus(p.Status); err != nil {
		return err
	}
	if !reflect.DeepEqual(p.Issues, want.Issues) {
		return errors.New("release evidence issues do not match embedded reviews")
	}
	return nil
}

// ValidateForStatus checks that an operator decision is complete and matches release readiness.
func (d OperatorReviewDecision) ValidateForStatus(status string) error {
	switch d.Decision {
	case OperatorReviewDecisionPending:
		if strings.TrimSpace(d.Reviewer) != "" || strings.TrimSpace(d.Timestamp) != "" || strings.TrimSpace(d.Reason) != "" {
			return errors.New("pending operator review decision must not include reviewer, timestamp, or reason")
		}
		return nil
	case OperatorReviewDecisionApproved:
		if status != ReleaseEvidenceStatusReady && status != EvidenceIndexStatusReady {
			return errors.New("operator review approval requires ready status")
		}
	case OperatorReviewDecisionRejected:
	default:
		return fmt.Errorf("operator review decision %q is invalid", d.Decision)
	}
	if strings.TrimSpace(d.Reviewer) == "" || strings.TrimSpace(d.Reviewer) != d.Reviewer {
		return errors.New("operator review reviewer is required and must be trimmed")
	}
	if strings.TrimSpace(d.Reason) == "" || strings.TrimSpace(d.Reason) != d.Reason {
		return errors.New("operator review reason is required and must be trimmed")
	}
	if strings.TrimSpace(d.Timestamp) == "" || strings.TrimSpace(d.Timestamp) != d.Timestamp {
		return errors.New("operator review timestamp is required and must be trimmed")
	}
	if _, err := time.Parse(time.RFC3339, d.Timestamp); err != nil {
		return fmt.Errorf("operator review timestamp must be RFC3339: %w", err)
	}
	return nil
}

// Validate checks that a release evidence issue is complete and uses a release status.
func (i ReleaseEvidenceIssue) Validate() error {
	if strings.TrimSpace(i.Component) == "" {
		return errors.New("component is required")
	}
	if strings.TrimSpace(i.Code) == "" {
		return errors.New("code is required")
	}
	if !validReleaseStatus(i.Status) {
		return fmt.Errorf("status %q is invalid", i.Status)
	}
	if strings.TrimSpace(i.Message) == "" {
		return errors.New("message is required")
	}
	return nil
}

func appendSupportBundleArchiveIssues(packet *ReleaseEvidencePacket, review supportbundle.ArchiveReviewSummary) {
	if err := review.Validate(); err != nil {
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveInvalid, fmt.Sprintf("support bundle archive review is invalid: %v", err)))
		return
	}
	switch review.Decision {
	case supportbundle.ReviewDecisionApproved:
	case supportbundle.ReviewDecisionNeedsReview:
		packet.Issues = append(packet.Issues, releaseNeedsReviewIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveNeedsReview, "support bundle archive is waiting for operator approval"))
	case supportbundle.ReviewDecisionRejected:
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveRejected, "support bundle archive was rejected by operator review"))
	}
	if len(review.OverBudgetCategories) > 0 {
		packet.Issues = append(packet.Issues, releaseNeedsReviewIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveOverBudget, "support bundle archive has one or more over-budget categories"))
	}
	if review.RedactionSummary.Total > 0 {
		packet.Issues = append(packet.Issues, releaseNeedsReviewIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveRedactions, "support bundle archive has redaction findings requiring operator review"))
	}
	if review.ExcludedCount > 0 {
		packet.Issues = append(packet.Issues, releaseNeedsReviewIssue(ReleaseEvidenceComponentSupportBundleArchive, ReleaseEvidenceIssueSupportBundleArchiveExclusions, "support bundle archive excluded one or more planned entries"))
	}
}

func appendBackupReadinessIssues(packet *ReleaseEvidencePacket, review BackupReadinessSummary) {
	for _, issue := range review.Issues {
		packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Component: ReleaseEvidenceComponentBackupReadiness, Code: issue.Code, Status: issue.Status, Message: issue.Message})
	}
	if !validReleaseStatus(review.Status) {
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentBackupReadiness, ReleaseEvidenceIssueBackupReadinessInvalidStatus, fmt.Sprintf("backup readiness status %q is invalid", review.Status)))
	}
}

func appendBackupFailureIssues(packet *ReleaseEvidencePacket, review BackupFailureReviewSummary) {
	for _, issue := range review.Issues {
		packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Component: ReleaseEvidenceComponentBackupFailure, Code: issue.Code, Status: failureStatusToReleaseStatus(issue.Status), Message: issue.Message})
	}
	if !validFailureStatus(review.Status) {
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentBackupFailure, ReleaseEvidenceIssueBackupFailureInvalidStatus, fmt.Sprintf("backup failure review status %q is invalid", review.Status)))
	}
}

func appendRestoreReadinessIssues(packet *ReleaseEvidencePacket, review RestoreReadinessSummary) {
	for _, issue := range review.Issues {
		packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Component: ReleaseEvidenceComponentRestoreReadiness, Code: issue.Code, Status: issue.Status, Message: issue.Message})
	}
	if !validReleaseStatus(review.Status) {
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentRestoreReadiness, ReleaseEvidenceIssueRestoreReadinessInvalidStatus, fmt.Sprintf("restore readiness status %q is invalid", review.Status)))
	}
}

func appendRestoreChecksumIssues(packet *ReleaseEvidencePacket, review RestoreChecksumReviewSummary) {
	for _, issue := range review.Issues {
		packet.Issues = append(packet.Issues, ReleaseEvidenceIssue{Component: ReleaseEvidenceComponentRestoreChecksum, Code: issue.Code, Status: failureStatusToReleaseStatus(issue.Status), Message: issue.Message})
	}
	if !validFailureStatus(review.Status) {
		packet.Issues = append(packet.Issues, releaseBlockedIssue(ReleaseEvidenceComponentRestoreChecksum, ReleaseEvidenceIssueRestoreChecksumInvalidStatus, fmt.Sprintf("restore checksum review status %q is invalid", review.Status)))
	}
}

func releaseEvidenceStatus(issues []ReleaseEvidenceIssue) string {
	status := ReleaseEvidenceStatusReady
	for _, issue := range issues {
		if issue.Status == ReleaseEvidenceStatusBlocked {
			return ReleaseEvidenceStatusBlocked
		}
		if issue.Status == ReleaseEvidenceStatusNeedsReview {
			status = ReleaseEvidenceStatusNeedsReview
		}
	}
	return status
}

func failureStatusToReleaseStatus(status string) string {
	if status == ReviewStatusFailed {
		return ReleaseEvidenceStatusBlocked
	}
	return status
}

func validReleaseStatus(status string) bool {
	switch status {
	case ReleaseEvidenceStatusReady, ReleaseEvidenceStatusNeedsReview, ReleaseEvidenceStatusBlocked:
		return true
	default:
		return false
	}
}

func validFailureStatus(status string) bool {
	switch status {
	case ReviewStatusOK, ReviewStatusNeedsReview, ReviewStatusFailed:
		return true
	default:
		return false
	}
}

func releaseBlockedIssue(component, code, message string) ReleaseEvidenceIssue {
	return ReleaseEvidenceIssue{Component: component, Code: code, Status: ReleaseEvidenceStatusBlocked, Message: message}
}

func releaseNeedsReviewIssue(component, code, message string) ReleaseEvidenceIssue {
	return ReleaseEvidenceIssue{Component: component, Code: code, Status: ReleaseEvidenceStatusNeedsReview, Message: message}
}

func validateReleaseEvidenceJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("release evidence JSON path is required")
	}
	if strings.ContainsAny(path, "*?") {
		return errors.New("release evidence JSON path must name one file")
	}
	clean := filepath.Clean(path)
	if clean == "." || strings.TrimSpace(path) != path {
		return errors.New("release evidence JSON path must be explicit and clean")
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		switch part {
		case ".", "..":
			return errors.New("release evidence JSON path must not contain relative traversal")
		}
	}
	if filepath.Ext(clean) != ".json" {
		return errors.New("release evidence JSON path must end with .json")
	}
	return nil
}
