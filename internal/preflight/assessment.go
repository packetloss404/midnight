package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// AssessmentSummarySchemaVersion is the stable schema version for the
// preflight assessment summary.
const AssessmentSummarySchemaVersion = "midnight.dev/preflight-assessment-summary/v1alpha1"

// RecommendedNextAction is the operator-facing next step derived from results.
type RecommendedNextAction string

const (
	RecommendedProceed        RecommendedNextAction = "proceed"
	RecommendedReviewWarnings RecommendedNextAction = "review-warnings"
	RecommendedFixBlockers    RecommendedNextAction = "fix-blockers"
)

// AssessmentSummary is a pure aggregate of preflight results for installer UX.
//
// SchemaVersion is set by NewAssessmentSummary and the marshal helpers. The
// remaining fields use stable, deterministic key ordering: schema_version,
// status_counts, severity_counts, has_blockers, safe_fix_available_count,
// recommended_next_action.
type AssessmentSummary struct {
	SchemaVersion         string                `json:"schema_version"`
	StatusCounts          map[Status]int        `json:"status_counts"`
	SeverityCounts        map[string]int        `json:"severity_counts"`
	HasBlockers           bool                  `json:"has_blockers"`
	SafeFixAvailableCount int                   `json:"safe_fix_available_count"`
	RecommendedNextAction RecommendedNextAction `json:"recommended_next_action"`
}

// NewAssessmentSummary converts already-collected results into a JSON-ready summary.
func NewAssessmentSummary(results []Result) AssessmentSummary {
	summary := AssessmentSummary{
		SchemaVersion:         AssessmentSummarySchemaVersion,
		StatusCounts:          make(map[Status]int),
		SeverityCounts:        make(map[string]int),
		RecommendedNextAction: RecommendedProceed,
	}

	for _, result := range results {
		summary.StatusCounts[result.Status]++
		if result.Severity != "" {
			summary.SeverityCounts[result.Severity]++
		}
		if result.SafeFixAvailable {
			summary.SafeFixAvailableCount++
		}
		if isBlockingResult(result) {
			summary.HasBlockers = true
		}
	}

	if summary.HasBlockers {
		summary.RecommendedNextAction = RecommendedFixBlockers
	} else if summary.StatusCounts[StatusWarn] > 0 {
		summary.RecommendedNextAction = RecommendedReviewWarnings
	}

	return summary
}

// MarshalAssessmentSummaryJSON emits the stable JSON representation for summary-only callers.
// The schema version is stamped automatically when missing.
func MarshalAssessmentSummaryJSON(summary AssessmentSummary) ([]byte, error) {
	if summary.SchemaVersion == "" {
		summary.SchemaVersion = AssessmentSummarySchemaVersion
	}
	return json.Marshal(summary)
}

// UnmarshalAssessmentSummaryJSON strictly decodes one preflight assessment
// summary document. Unknown fields, trailing JSON, and a missing/incorrect
// schema version are rejected.
func UnmarshalAssessmentSummaryJSON(data []byte) (AssessmentSummary, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var summary AssessmentSummary
	if err := decoder.Decode(&summary); err != nil {
		return AssessmentSummary{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("assessment summary JSON must contain one document")
		}
		return AssessmentSummary{}, err
	}
	if summary.SchemaVersion != AssessmentSummarySchemaVersion {
		return AssessmentSummary{}, fmt.Errorf("assessment summary schema_version %q is invalid", summary.SchemaVersion)
	}
	return summary, nil
}

// MarshalAssessmentSummary is the canonical Phase 1.5 serializer alias.
func MarshalAssessmentSummary(summary AssessmentSummary) ([]byte, error) {
	return MarshalAssessmentSummaryJSON(summary)
}

// UnmarshalAssessmentSummary is the canonical Phase 1.5 deserializer alias.
func UnmarshalAssessmentSummary(data []byte) (AssessmentSummary, error) {
	return UnmarshalAssessmentSummaryJSON(data)
}

// RoundTripAssessmentSummaryJSON marshals and strictly decodes a summary so
// callers can verify a snapshot survives the JSON shape.
func RoundTripAssessmentSummaryJSON(summary AssessmentSummary) (AssessmentSummary, error) {
	if summary.SchemaVersion == "" {
		summary.SchemaVersion = AssessmentSummarySchemaVersion
	}
	data, err := MarshalAssessmentSummaryJSON(summary)
	if err != nil {
		return AssessmentSummary{}, err
	}
	return UnmarshalAssessmentSummaryJSON(data)
}

func isBlockingResult(result Result) bool {
	if result.Status == StatusConfigError {
		return true
	}
	return result.Status == StatusFail && result.Severity == "fatal"
}
