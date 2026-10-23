package doctor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/midnight/midnight/internal/install"
	"github.com/midnight/midnight/internal/preflight"
)

const ReleaseEvidencePacketSchemaV1 = "midnight.release_evidence_packet.v1"

// ReleaseEvidenceStatus is the operator-facing release/install decision.
type ReleaseEvidenceStatus string

const (
	ReleaseEvidenceReady       ReleaseEvidenceStatus = "ready"
	ReleaseEvidenceNeedsReview ReleaseEvidenceStatus = "needs-review"
	ReleaseEvidenceBlocked     ReleaseEvidenceStatus = "blocked"
)

// ReleaseEvidenceIssue is a deterministic, JSON-friendly issue rollup entry.
type ReleaseEvidenceIssue struct {
	Source   string `json:"source"`
	ID       string `json:"id,omitempty"`
	Status   string `json:"status"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
}

// ReleaseEvidenceIssueRollup summarizes issue counts and details without host inspection.
type ReleaseEvidenceIssueRollup struct {
	TotalIssues      int                    `json:"total_issues"`
	BlockedCount     int                    `json:"blocked_count"`
	NeedsReviewCount int                    `json:"needs_review_count"`
	CountsBySource   map[string]int         `json:"counts_by_source"`
	Issues           []ReleaseEvidenceIssue `json:"issues"`
}

// ReleaseEvidencePacket combines already-collected release/install evidence.
type ReleaseEvidencePacket struct {
	Schema                  string                          `json:"schema"`
	Status                  ReleaseEvidenceStatus           `json:"status"`
	CommandEvidence         []preflight.CommandEvidence     `json:"command_evidence"`
	PreflightAssessment     preflight.AssessmentSummary     `json:"preflight_assessment"`
	RemediationPlan         install.RemediationPlan         `json:"remediation_plan"`
	InstallReadiness        install.ReadinessSummary        `json:"install_readiness"`
	DoctorReadiness         ReadinessSummary                `json:"doctor_readiness"`
	ResourcePressureReviews []ResourcePressureReviewSummary `json:"resource_pressure_reviews"`
	IssueRollup             ReleaseEvidenceIssueRollup      `json:"issue_rollup"`
}

// NewReleaseEvidencePacket builds a deterministic packet from fake or already-collected inputs.
func NewReleaseEvidencePacket(commandEvidence []preflight.CommandEvidence, preflightAssessment preflight.AssessmentSummary, remediationPlan install.RemediationPlan, installReadiness install.ReadinessSummary, doctorReadiness ReadinessSummary, resourcePressureReviews []ResourcePressureReviewSummary) ReleaseEvidencePacket {
	packet := ReleaseEvidencePacket{
		Schema:                  ReleaseEvidencePacketSchemaV1,
		Status:                  ReleaseEvidenceReady,
		CommandEvidence:         append([]preflight.CommandEvidence(nil), commandEvidence...),
		PreflightAssessment:     preflightAssessment,
		RemediationPlan:         remediationPlan,
		InstallReadiness:        installReadiness,
		DoctorReadiness:         doctorReadiness,
		ResourcePressureReviews: append([]ResourcePressureReviewSummary(nil), resourcePressureReviews...),
	}
	packet.IssueRollup = releaseEvidenceIssueRollup(packet)
	packet.Status = releaseEvidenceStatus(packet.IssueRollup)
	return packet
}

// MarshalReleaseEvidencePacketJSON emits stable JSON for release evidence packets.
func MarshalReleaseEvidencePacketJSON(packet ReleaseEvidencePacket) ([]byte, error) {
	if err := ValidateReleaseEvidencePacket(packet); err != nil {
		return nil, err
	}
	return json.Marshal(packet)
}

// WriteReleaseEvidencePacketJSONFile validates and writes a deterministic packet JSON file.
func WriteReleaseEvidencePacketJSONFile(path string, packet ReleaseEvidencePacket, mode os.FileMode) error {
	if err := validateReleaseEvidencePacketJSONPath(path); err != nil {
		return err
	}
	if err := ValidateReleaseEvidencePacket(packet); err != nil {
		return err
	}
	data, err := json.MarshalIndent(packet, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, mode)
}

// ReadReleaseEvidencePacketJSONFile strictly decodes and validates a packet JSON file.
func ReadReleaseEvidencePacketJSONFile(path string) (ReleaseEvidencePacket, error) {
	if err := validateReleaseEvidencePacketJSONPath(path); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	packet, err := decodeReleaseEvidencePacketJSON(data)
	if err != nil {
		return ReleaseEvidencePacket{}, err
	}
	if err := ValidateReleaseEvidencePacket(packet); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

func decodeReleaseEvidencePacketJSON(data []byte) (ReleaseEvidencePacket, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	var packet ReleaseEvidencePacket
	if err := decoder.Decode(&packet); err != nil {
		return ReleaseEvidencePacket{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return ReleaseEvidencePacket{}, err
	}
	return packet, nil
}

func validateReleaseEvidencePacketJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("release evidence packet path is required")
	}
	if filepath.Ext(path) != ".json" {
		return fmt.Errorf("release evidence packet path %q must end with .json", path)
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("release evidence packet parent directory %q: %w", parent, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("release evidence packet parent path %q is not a directory", parent)
	}
	return nil
}

// ValidateReleaseEvidencePacket rejects incomplete or internally inconsistent packets.
func ValidateReleaseEvidencePacket(packet ReleaseEvidencePacket) error {
	if packet.Schema != ReleaseEvidencePacketSchemaV1 {
		return fmt.Errorf("release evidence packet schema %q is invalid", packet.Schema)
	}
	if !validReleaseEvidenceStatus(packet.Status) {
		return fmt.Errorf("release evidence packet status %q is invalid", packet.Status)
	}
	for index, evidence := range packet.CommandEvidence {
		if evidence.Schema != preflight.CommandEvidenceSchemaV1 {
			return fmt.Errorf("command evidence at index %d has invalid schema %q", index, evidence.Schema)
		}
		if evidence.SpecID == "" || evidence.CheckID == "" || evidence.Executable == "" {
			return fmt.Errorf("command evidence at index %d is missing required identity fields", index)
		}
	}
	if packet.IssueRollup.TotalIssues != len(packet.IssueRollup.Issues) {
		return fmt.Errorf("issue rollup total %d does not match issue count %d", packet.IssueRollup.TotalIssues, len(packet.IssueRollup.Issues))
	}
	if err := validateReleaseEvidenceIssueRollupCounts(packet.IssueRollup); err != nil {
		return err
	}
	if packet.Status != releaseEvidenceStatus(packet.IssueRollup) {
		return fmt.Errorf("release evidence packet status %q does not match issue rollup", packet.Status)
	}
	return nil
}

func validateReleaseEvidenceIssueRollupCounts(rollup ReleaseEvidenceIssueRollup) error {
	countsBySource := map[string]int{}
	blockedCount := 0
	needsReviewCount := 0
	for _, issue := range rollup.Issues {
		countsBySource[issue.Source]++
		switch issue.Status {
		case string(ReleaseEvidenceBlocked):
			blockedCount++
		case string(ReleaseEvidenceNeedsReview):
			needsReviewCount++
		}
	}
	if rollup.BlockedCount != blockedCount {
		return fmt.Errorf("issue rollup blocked count %d does not match issue count %d", rollup.BlockedCount, blockedCount)
	}
	if rollup.NeedsReviewCount != needsReviewCount {
		return fmt.Errorf("issue rollup needs-review count %d does not match issue count %d", rollup.NeedsReviewCount, needsReviewCount)
	}
	if len(rollup.CountsBySource) != len(countsBySource) {
		return fmt.Errorf("issue rollup source count entries %d does not match issue sources %d", len(rollup.CountsBySource), len(countsBySource))
	}
	for source, count := range countsBySource {
		if rollup.CountsBySource[source] != count {
			return fmt.Errorf("issue rollup source %q count %d does not match issue count %d", source, rollup.CountsBySource[source], count)
		}
	}
	return nil
}

func releaseEvidenceIssueRollup(packet ReleaseEvidencePacket) ReleaseEvidenceIssueRollup {
	rollup := ReleaseEvidenceIssueRollup{CountsBySource: map[string]int{}}
	for _, evidence := range packet.CommandEvidence {
		if evidence.ExitCode != 0 || evidence.TimedOut || evidence.Error != "" {
			status := string(ReleaseEvidenceNeedsReview)
			if evidence.TimedOut {
				status = string(ReleaseEvidenceBlocked)
			}
			addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{
				Source:  "command_evidence",
				ID:      evidence.SpecID,
				Status:  status,
				Message: commandEvidenceIssueMessage(evidence),
			})
		}
	}
	if packet.PreflightAssessment.HasBlockers {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "preflight_assessment", Status: string(ReleaseEvidenceBlocked), Severity: "fatal", Message: "preflight assessment has blockers"})
	} else if packet.PreflightAssessment.StatusCounts[preflight.StatusWarn] > 0 {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "preflight_assessment", Status: string(ReleaseEvidenceNeedsReview), Severity: "warn", Message: "preflight assessment has warnings"})
	}
	if len(packet.RemediationPlan.Steps) > 0 {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "remediation_plan", Status: string(ReleaseEvidenceNeedsReview), Message: "remediation plan has operator steps"})
	}
	if packet.InstallReadiness.Readiness == install.ReadinessBlocked {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "install_readiness", Status: string(ReleaseEvidenceBlocked), Severity: "fatal", Message: "install readiness is blocked"})
	} else if packet.InstallReadiness.Readiness == install.ReadinessReadyWithWarnings {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "install_readiness", Status: string(ReleaseEvidenceNeedsReview), Severity: "warn", Message: "install readiness has warnings"})
	}
	if packet.DoctorReadiness.Status == ReadinessBlocked {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "doctor_readiness", Status: string(ReleaseEvidenceBlocked), Severity: "fatal", Message: "doctor readiness is blocked"})
	} else if packet.DoctorReadiness.Status == ReadinessDegraded {
		addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "doctor_readiness", Status: string(ReleaseEvidenceNeedsReview), Severity: "warn", Message: "doctor readiness is degraded"})
	}
	for _, review := range packet.ResourcePressureReviews {
		switch review.Status {
		case ResourcePressureCritical:
			addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "resource_pressure", ID: review.Resource, Status: string(ReleaseEvidenceBlocked), Severity: "fatal", Message: review.Reason})
		case ResourcePressureWarning:
			addReleaseEvidenceIssue(&rollup, ReleaseEvidenceIssue{Source: "resource_pressure", ID: review.Resource, Status: string(ReleaseEvidenceNeedsReview), Severity: "warn", Message: review.Reason})
		}
	}
	return rollup
}

func addReleaseEvidenceIssue(rollup *ReleaseEvidenceIssueRollup, issue ReleaseEvidenceIssue) {
	rollup.Issues = append(rollup.Issues, issue)
	rollup.TotalIssues++
	rollup.CountsBySource[issue.Source]++
	if issue.Status == string(ReleaseEvidenceBlocked) {
		rollup.BlockedCount++
		return
	}
	rollup.NeedsReviewCount++
}

func releaseEvidenceStatus(rollup ReleaseEvidenceIssueRollup) ReleaseEvidenceStatus {
	if rollup.BlockedCount > 0 {
		return ReleaseEvidenceBlocked
	}
	if rollup.NeedsReviewCount > 0 {
		return ReleaseEvidenceNeedsReview
	}
	return ReleaseEvidenceReady
}

func validReleaseEvidenceStatus(status ReleaseEvidenceStatus) bool {
	switch status {
	case ReleaseEvidenceReady, ReleaseEvidenceNeedsReview, ReleaseEvidenceBlocked:
		return true
	default:
		return false
	}
}

func commandEvidenceIssueMessage(evidence preflight.CommandEvidence) string {
	if evidence.TimedOut {
		return "command evidence timed out"
	}
	if evidence.Error != "" {
		return evidence.Error
	}
	return "command evidence exited non-zero"
}
