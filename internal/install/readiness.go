package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/midnight/midnight/internal/preflight"
)

// ReadinessSummarySchemaVersion is the stable schema for the install readiness summary.
const ReadinessSummarySchemaVersion = "midnight.dev/install-readiness-summary/v1alpha1"

// Readiness is the installer-facing decision derived from preflight and remediation data.
type Readiness string

const (
	ReadinessReady             Readiness = "ready"
	ReadinessReadyWithWarnings Readiness = "ready-with-warnings"
	ReadinessBlocked           Readiness = "blocked"
)

const (
	nextActionProceed        = "proceed"
	nextActionReviewWarnings = "review-warnings"
	nextActionApplySafeFixes = "apply-safe-fixes"
	nextActionPlanRestart    = "plan-restart"
	nextActionFixBlockers    = "fix-blockers"
)

// ReadinessSummary is a JSON-friendly, pure install readiness aggregate.
type ReadinessSummary struct {
	SchemaVersion        string    `json:"schema_version"`
	Readiness            Readiness `json:"readiness"`
	BlockerCount         int       `json:"blocker_count"`
	WarningCount         int       `json:"warning_count"`
	SafeFixCount         int       `json:"safe_fix_count"`
	RestartRequiredCount int       `json:"restart_required_count"`
	NextActions          []string  `json:"next_actions"`
}

// NewReadinessSummary combines already-collected preflight results with dry-run remediation steps.
func NewReadinessSummary(results []preflight.Result, plan RemediationPlan) ReadinessSummary {
	assessment := preflight.NewAssessmentSummary(results)
	summary := ReadinessSummary{
		SchemaVersion: ReadinessSummarySchemaVersion,
		Readiness:     ReadinessReady,
		BlockerCount:  countBlockers(results),
		WarningCount:  assessment.StatusCounts[preflight.StatusWarn],
	}

	for _, step := range plan.Steps {
		if step.ApplyMode == ApplyModeSafeFix {
			summary.SafeFixCount++
		}
		if step.RequiresRestart {
			summary.RestartRequiredCount++
		}
	}

	if summary.BlockerCount > 0 {
		summary.Readiness = ReadinessBlocked
	} else if summary.WarningCount > 0 {
		summary.Readiness = ReadinessReadyWithWarnings
	}
	summary.NextActions = nextActions(summary)

	return summary
}

// MarshalReadinessSummaryJSON emits the stable readiness summary JSON representation.
func MarshalReadinessSummaryJSON(summary ReadinessSummary) ([]byte, error) {
	return json.Marshal(summary)
}

// UnmarshalReadinessSummaryJSON strictly decodes a readiness summary and
// rejects unknown fields and trailing JSON. It also enforces SchemaVersion.
func UnmarshalReadinessSummaryJSON(data []byte) (ReadinessSummary, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var summary ReadinessSummary
	if err := decoder.Decode(&summary); err != nil {
		return ReadinessSummary{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("readiness summary JSON must contain one document")
		}
		return ReadinessSummary{}, err
	}
	if summary.SchemaVersion != ReadinessSummarySchemaVersion {
		return ReadinessSummary{}, fmt.Errorf("readiness summary schema_version %q is invalid", summary.SchemaVersion)
	}
	return summary, nil
}

// RoundTripReadinessSummaryJSON marshals and strictly decodes a summary.
func RoundTripReadinessSummaryJSON(summary ReadinessSummary) (ReadinessSummary, error) {
	if summary.SchemaVersion == "" {
		summary.SchemaVersion = ReadinessSummarySchemaVersion
	}
	data, err := MarshalReadinessSummaryJSON(summary)
	if err != nil {
		return ReadinessSummary{}, err
	}
	return UnmarshalReadinessSummaryJSON(data)
}

// MarshalReadinessSummary is the canonical Phase 1.5 serializer for the install
// readiness summary. It stamps the schema version when missing and emits stable
// key ordering. Callers preferring the explicit JSON-suffixed name should use
// MarshalReadinessSummaryJSON; the two return identical bytes.
func MarshalReadinessSummary(summary ReadinessSummary) ([]byte, error) {
	if summary.SchemaVersion == "" {
		summary.SchemaVersion = ReadinessSummarySchemaVersion
	}
	return MarshalReadinessSummaryJSON(summary)
}

// UnmarshalReadinessSummary is the canonical Phase 1.5 deserializer for the
// install readiness summary. It rejects unknown fields, trailing JSON, and
// any schema version other than ReadinessSummarySchemaVersion.
func UnmarshalReadinessSummary(data []byte) (ReadinessSummary, error) {
	return UnmarshalReadinessSummaryJSON(data)
}

func countBlockers(results []preflight.Result) int {
	var count int
	for _, result := range results {
		if result.Status == preflight.StatusConfigError || result.Status == preflight.StatusFail && result.Severity == "fatal" {
			count++
		}
	}
	return count
}

func nextActions(summary ReadinessSummary) []string {
	if summary.BlockerCount > 0 {
		return appendRemediationActions([]string{nextActionFixBlockers}, summary)
	}
	if summary.WarningCount > 0 {
		return appendRemediationActions([]string{nextActionReviewWarnings}, summary)
	}
	return []string{nextActionProceed}
}

func appendRemediationActions(actions []string, summary ReadinessSummary) []string {
	if summary.SafeFixCount > 0 {
		actions = append(actions, nextActionApplySafeFixes)
	}
	if summary.RestartRequiredCount > 0 {
		actions = append(actions, nextActionPlanRestart)
	}
	return actions
}
