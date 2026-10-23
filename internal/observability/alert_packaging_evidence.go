package observability

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	AlertPackagingEvidenceStatusReady   = "ready"
	AlertPackagingEvidenceStatusBlocked = "blocked"

	AlertPackagingEvidenceIssueLockedAlertsInvalid = "locked-alerts-invalid"
)

// AlertPackagingEvidence records the pure validation result for packaged alerts.
type AlertPackagingEvidence struct {
	Path             string                        `json:"path"`
	Status           string                        `json:"status"`
	AlertCount       int                           `json:"alertCount"`
	LockedAlertCount int                           `json:"lockedAlertCount"`
	Issues           []AlertPackagingEvidenceIssue `json:"issues"`
}

// AlertPackagingEvidenceIssue records one packaged alert validation finding.
type AlertPackagingEvidenceIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BuildAlertPackagingEvidence validates packaged alert bytes without filesystem I/O.
func BuildAlertPackagingEvidence(path string, data []byte) AlertPackagingEvidence {
	alerts, err := ParsePrometheusAlerts(data)
	evidence := AlertPackagingEvidence{
		Path:             path,
		Status:           AlertPackagingEvidenceStatusReady,
		AlertCount:       len(alerts),
		LockedAlertCount: len(LockedAlerts),
	}
	if err != nil {
		evidence.Status = AlertPackagingEvidenceStatusBlocked
		evidence.Issues = append(evidence.Issues, AlertPackagingEvidenceIssue{Code: AlertPackagingEvidenceIssueLockedAlertsInvalid, Message: err.Error()})
		return evidence
	}
	if err := ValidatePrometheusAlerts(data); err != nil {
		evidence.Status = AlertPackagingEvidenceStatusBlocked
		evidence.Issues = append(evidence.Issues, AlertPackagingEvidenceIssue{Code: AlertPackagingEvidenceIssueLockedAlertsInvalid, Message: err.Error()})
	}
	return evidence
}

// MarshalAlertPackagingEvidenceJSON validates and renders stable alert packaging JSON.
func MarshalAlertPackagingEvidenceJSON(evidence AlertPackagingEvidence) ([]byte, error) {
	if err := evidence.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(evidence, "", "  ")
}

// Validate checks that alert packaging evidence is internally consistent.
func (e AlertPackagingEvidence) Validate() error {
	if err := validateEvidencePath(e.Path); err != nil {
		return err
	}
	switch e.Status {
	case AlertPackagingEvidenceStatusReady:
		if len(e.Issues) != 0 {
			return errors.New("ready alert packaging evidence must not include issues")
		}
	case AlertPackagingEvidenceStatusBlocked:
		if len(e.Issues) == 0 {
			return errors.New("blocked alert packaging evidence must include issues")
		}
	default:
		return fmt.Errorf("alert packaging evidence status %q is invalid", e.Status)
	}
	if e.AlertCount < 0 || e.LockedAlertCount < 0 {
		return errors.New("alert packaging evidence counts must be non-negative")
	}
	if e.LockedAlertCount != len(LockedAlerts) {
		return errors.New("alert packaging evidence locked alert count must match locked alerts")
	}
	for i, issue := range e.Issues {
		if strings.TrimSpace(issue.Code) == "" {
			return fmt.Errorf("issues[%d]: code is required", i)
		}
		if strings.TrimSpace(issue.Message) == "" {
			return fmt.Errorf("issues[%d]: message is required", i)
		}
	}
	return nil
}

func validateEvidencePath(path string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) != path {
		return errors.New("alert packaging evidence path is required and must be trimmed")
	}
	if filepath.IsAbs(path) || strings.Contains(path, "\\") || strings.Contains(path, "*") {
		return errors.New("alert packaging evidence path must be a safe relative slash path")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return errors.New("alert packaging evidence path must be a safe relative slash path")
	}
	return nil
}
