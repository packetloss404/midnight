package install

import (
	"encoding/json"
	"testing"

	"github.com/midnight/midnight/internal/preflight"
)

func TestNewReadinessSummaryReady(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusPass, Severity: "fatal"},
		{ID: "PRE-DOCKER-API-001", Status: preflight.StatusPass, Severity: "fatal"},
	}, RemediationPlan{})

	if summary.Readiness != ReadinessReady {
		t.Fatalf("Readiness = %q, want %q", summary.Readiness, ReadinessReady)
	}
	if summary.BlockerCount != 0 || summary.WarningCount != 0 || summary.SafeFixCount != 0 || summary.RestartRequiredCount != 0 {
		t.Fatalf("summary counts = %+v, want all zero", summary)
	}
	assertNextActions(t, summary.NextActions, []string{"proceed"})
}

func TestNewReadinessSummaryReadyWithWarnings(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusPass, Severity: "fatal"},
		{ID: "PRE-DOCKER-DAEMON-001", Status: preflight.StatusWarn, Severity: "warn", SafeFixAvailable: true},
	}, RemediationPlan{Steps: []RemediationStep{
		{Title: "Resolve Docker daemon.json baseline", ApplyMode: ApplyModeSafeFix, RequiresRestart: true},
	}})

	if summary.Readiness != ReadinessReadyWithWarnings {
		t.Fatalf("Readiness = %q, want %q", summary.Readiness, ReadinessReadyWithWarnings)
	}
	if summary.BlockerCount != 0 || summary.WarningCount != 1 || summary.SafeFixCount != 1 || summary.RestartRequiredCount != 1 {
		t.Fatalf("summary counts = %+v, want warning with one safe fix and restart", summary)
	}
	assertNextActions(t, summary.NextActions, []string{"review-warnings", "apply-safe-fixes", "plan-restart"})
}

func TestNewReadinessSummaryBlocked(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusFail, Severity: "fatal"},
		{ID: "PRE-DOCKER-API-001", Status: preflight.StatusFail, Severity: "fatal"},
		{ID: "PRE-DOCKER-DAEMON-001", Status: preflight.StatusWarn, Severity: "warn"},
	}, RemediationPlan{Steps: []RemediationStep{
		{Title: "Enable cgroup v2", ApplyMode: ApplyModeManual, RequiresRestart: true},
		{Title: "Upgrade Docker API provider", ApplyMode: ApplyModeManual, RequiresRestart: true},
	}})

	if summary.Readiness != ReadinessBlocked {
		t.Fatalf("Readiness = %q, want %q", summary.Readiness, ReadinessBlocked)
	}
	if summary.BlockerCount != 2 || summary.WarningCount != 1 || summary.SafeFixCount != 0 || summary.RestartRequiredCount != 2 {
		t.Fatalf("summary counts = %+v, want two blockers, one warning, two restarts", summary)
	}
	assertNextActions(t, summary.NextActions, []string{"fix-blockers", "plan-restart"})
}

func TestNewReadinessSummaryConfigErrorBlocks(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{{ID: "PRE-CONFIG-001", Status: preflight.StatusConfigError}}, RemediationPlan{})
	if summary.Readiness != ReadinessBlocked || summary.BlockerCount != 1 {
		t.Fatalf("summary = %+v, want config error blocker", summary)
	}
}

func TestMarshalReadinessSummaryJSON(t *testing.T) {
	t.Parallel()

	summary := ReadinessSummary{
		SchemaVersion:        ReadinessSummarySchemaVersion,
		Readiness:            ReadinessReadyWithWarnings,
		WarningCount:         1,
		SafeFixCount:         1,
		RestartRequiredCount: 1,
		NextActions:          []string{"review-warnings", "apply-safe-fixes", "plan-restart"},
	}
	data, err := MarshalReadinessSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalReadinessSummaryJSON() error = %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("readiness JSON is invalid: %s", data)
	}

	var decoded ReadinessSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}
	if decoded.Readiness != summary.Readiness || decoded.NextActions[1] != "apply-safe-fixes" {
		t.Fatalf("decoded = %+v, want round-trip", decoded)
	}
	if decoded.SchemaVersion != ReadinessSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", decoded.SchemaVersion, ReadinessSummarySchemaVersion)
	}
}

func TestRoundTripReadinessSummaryJSON(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{
		{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"},
	}, RemediationPlan{})
	got, err := RoundTripReadinessSummaryJSON(summary)
	if err != nil {
		t.Fatalf("RoundTripReadinessSummaryJSON() error = %v", err)
	}
	if got.SchemaVersion != ReadinessSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, ReadinessSummarySchemaVersion)
	}
}

func TestUnmarshalReadinessSummaryJSONRejectsBadSchema(t *testing.T) {
	t.Parallel()

	bad := []byte(`{"schema_version":"wrong","readiness":"ready","blocker_count":0,"warning_count":0,"safe_fix_count":0,"restart_required_count":0,"next_actions":["proceed"]}`)
	if _, err := UnmarshalReadinessSummaryJSON(bad); err == nil {
		t.Fatal("UnmarshalReadinessSummaryJSON() error = nil, want schema error")
	}
}

func TestNewReadinessSummaryStampsSchemaVersion(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass}}, RemediationPlan{})
	if summary.SchemaVersion != ReadinessSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", summary.SchemaVersion, ReadinessSummarySchemaVersion)
	}
}

func TestMarshalUnmarshalReadinessSummaryRoundTrip(t *testing.T) {
	t.Parallel()

	summary := NewReadinessSummary([]preflight.Result{
		{ID: "PRE-CGROUP-001", Status: preflight.StatusPass, Severity: "fatal"},
	}, RemediationPlan{})

	data, err := MarshalReadinessSummary(summary)
	if err != nil {
		t.Fatalf("MarshalReadinessSummary() error = %v", err)
	}

	got, err := UnmarshalReadinessSummary(data)
	if err != nil {
		t.Fatalf("UnmarshalReadinessSummary() error = %v", err)
	}
	if got.SchemaVersion != ReadinessSummarySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, ReadinessSummarySchemaVersion)
	}
	if got.Readiness != summary.Readiness {
		t.Fatalf("Readiness = %q, want %q", got.Readiness, summary.Readiness)
	}
}

func TestMarshalReadinessSummaryStampsSchemaVersion(t *testing.T) {
	t.Parallel()

	// The aliased helper should fill in SchemaVersion when omitted.
	summary := ReadinessSummary{Readiness: ReadinessReady, NextActions: []string{"proceed"}}
	data, err := MarshalReadinessSummary(summary)
	if err != nil {
		t.Fatalf("MarshalReadinessSummary() error = %v", err)
	}
	if _, err := UnmarshalReadinessSummary(data); err != nil {
		t.Fatalf("UnmarshalReadinessSummary() error = %v", err)
	}
}

func assertNextActions(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("NextActions = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("NextActions = %v, want %v", got, want)
		}
	}
}
