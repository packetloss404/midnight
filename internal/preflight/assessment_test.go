package preflight

import (
	"encoding/json"
	"testing"
)

func TestNewAssessmentSummaryCountsFakeResults(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{
		{ID: "PRE-PASS", Status: StatusPass, Severity: "fatal"},
		{ID: "PRE-WARN", Status: StatusWarn, Severity: "warn", SafeFixAvailable: true},
		{ID: "PRE-SKIP", Status: StatusSkip, Severity: "warn"},
	})

	if summary.StatusCounts[StatusPass] != 1 || summary.StatusCounts[StatusWarn] != 1 || summary.StatusCounts[StatusSkip] != 1 {
		t.Fatalf("StatusCounts = %+v, want one pass, warn, and skip", summary.StatusCounts)
	}
	if summary.SeverityCounts["fatal"] != 1 || summary.SeverityCounts["warn"] != 2 {
		t.Fatalf("SeverityCounts = %+v, want fatal=1 warn=2", summary.SeverityCounts)
	}
	if summary.HasBlockers {
		t.Fatal("HasBlockers = true, want false")
	}
	if summary.SafeFixAvailableCount != 1 {
		t.Fatalf("SafeFixAvailableCount = %d, want 1", summary.SafeFixAvailableCount)
	}
	if summary.RecommendedNextAction != RecommendedReviewWarnings {
		t.Fatalf("RecommendedNextAction = %q, want %q", summary.RecommendedNextAction, RecommendedReviewWarnings)
	}
}

func TestNewAssessmentSummaryRecommendsFixBlockers(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{
		{ID: "PRE-WARN", Status: StatusWarn, Severity: "warn"},
		{ID: "PRE-FAIL", Status: StatusFail, Severity: "fatal", SafeFixAvailable: true},
	})

	if !summary.HasBlockers {
		t.Fatal("HasBlockers = false, want true")
	}
	if summary.SafeFixAvailableCount != 1 {
		t.Fatalf("SafeFixAvailableCount = %d, want 1", summary.SafeFixAvailableCount)
	}
	if summary.RecommendedNextAction != RecommendedFixBlockers {
		t.Fatalf("RecommendedNextAction = %q, want %q", summary.RecommendedNextAction, RecommendedFixBlockers)
	}
}

func TestNewAssessmentSummaryRecommendsProceed(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{
		{ID: "PRE-PASS", Status: StatusPass, Severity: "fatal"},
		{ID: "PRE-SKIP", Status: StatusSkip, Severity: "warn"},
	})

	if summary.HasBlockers {
		t.Fatal("HasBlockers = true, want false")
	}
	if summary.RecommendedNextAction != RecommendedProceed {
		t.Fatalf("RecommendedNextAction = %q, want %q", summary.RecommendedNextAction, RecommendedProceed)
	}
}

func TestNewAssessmentSummaryConfigErrorBlocks(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{{ID: "PRE-CONFIG", Status: StatusConfigError}})
	if !summary.HasBlockers {
		t.Fatal("HasBlockers = false, want true")
	}
	if summary.RecommendedNextAction != RecommendedFixBlockers {
		t.Fatalf("RecommendedNextAction = %q, want %q", summary.RecommendedNextAction, RecommendedFixBlockers)
	}
}

func TestMarshalAssessmentSummaryJSON(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{{ID: "PRE-WARN", Status: StatusWarn, Severity: "warn", SafeFixAvailable: true}})
	data, err := MarshalAssessmentSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalAssessmentSummaryJSON() error = %v", err)
	}

	var decoded AssessmentSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}
	if decoded.StatusCounts[StatusWarn] != 1 || decoded.RecommendedNextAction != RecommendedReviewWarnings {
		t.Fatalf("decoded summary = %+v, want warning summary", decoded)
	}
}

func TestNewAssessmentSummaryStampsSchemaVersion(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary(nil)
	if summary.SchemaVersion != AssessmentSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", summary.SchemaVersion, AssessmentSummarySchemaVersion)
	}
}

func TestRoundTripAssessmentSummaryJSON(t *testing.T) {
	t.Parallel()

	summary := NewAssessmentSummary([]Result{
		{ID: "PRE-WARN", Status: StatusWarn, Severity: "warn"},
		{ID: "PRE-PASS", Status: StatusPass, Severity: "fatal"},
	})
	got, err := RoundTripAssessmentSummaryJSON(summary)
	if err != nil {
		t.Fatalf("RoundTripAssessmentSummaryJSON() error = %v", err)
	}
	if got.SchemaVersion != AssessmentSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, AssessmentSummarySchemaVersion)
	}
	if got.RecommendedNextAction != summary.RecommendedNextAction {
		t.Fatalf("RecommendedNextAction = %q, want %q", got.RecommendedNextAction, summary.RecommendedNextAction)
	}
}

func TestUnmarshalAssessmentSummaryRejectsBadSchema(t *testing.T) {
	t.Parallel()

	bad := []byte(`{"schema_version":"wrong","status_counts":{},"severity_counts":{},"has_blockers":false,"safe_fix_available_count":0,"recommended_next_action":"proceed"}`)
	if _, err := UnmarshalAssessmentSummary(bad); err == nil {
		t.Fatal("UnmarshalAssessmentSummary(bad schema) error = nil, want schema error")
	}
}

func TestUnmarshalAssessmentSummaryRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	bad := []byte(`{"schema_version":"midnight.dev/preflight-assessment-summary/v1alpha1","status_counts":{},"severity_counts":{},"has_blockers":false,"safe_fix_available_count":0,"recommended_next_action":"proceed","oops":"new"}`)
	if _, err := UnmarshalAssessmentSummary(bad); err == nil {
		t.Fatal("UnmarshalAssessmentSummary(unknown field) error = nil, want strict-decode error")
	}
}
