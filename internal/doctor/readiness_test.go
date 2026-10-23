package doctor

import (
	"encoding/json"
	"testing"

	"github.com/midnight/midnight/internal/install"
	"github.com/midnight/midnight/internal/preflight"
)

func TestNewReadinessSummaryHealthy(t *testing.T) {
	t.Parallel()

	preflightSummary := preflight.NewAssessmentSummary([]preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}})
	installSummary := install.NewReadinessSummary([]preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}}, install.RemediationPlan{})
	summary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-OK", Status: StatusPass, Severity: "fatal"}})

	if summary.Status != ReadinessHealthy {
		t.Fatalf("Status = %q, want %q", summary.Status, ReadinessHealthy)
	}
	if summary.RestartRequired || summary.SafeFixCount != 0 {
		t.Fatalf("summary = %+v, want no restart and no safe fixes", summary)
	}
	if summary.CountsBySource["preflight"].StatusCounts["pass"] != 1 || summary.CountsBySource["install"].StatusCounts["ready"] != 1 || summary.CountsBySource["doctor"].StatusCounts["pass"] != 1 {
		t.Fatalf("CountsBySource = %+v, want pass/ready/pass", summary.CountsBySource)
	}
	assertDoctorNextActions(t, summary.NextActions, []string{"proceed"})
}

func TestNewReadinessSummaryDegraded(t *testing.T) {
	t.Parallel()

	preflightResults := []preflight.Result{{ID: "PRE-WARN", Status: preflight.StatusWarn, Severity: "warn", SafeFixAvailable: true}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	installSummary := install.NewReadinessSummary(preflightResults, install.RemediationPlan{Steps: []install.RemediationStep{{ApplyMode: install.ApplyModeSafeFix, RequiresRestart: true}}})
	summary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-SKIP", Status: StatusSkip, Severity: "warn"}})

	if summary.Status != ReadinessDegraded {
		t.Fatalf("Status = %q, want %q", summary.Status, ReadinessDegraded)
	}
	if !summary.RestartRequired || summary.SafeFixCount != 2 {
		t.Fatalf("summary = %+v, want restart and two safe-fix signals", summary)
	}
	if summary.CountsBySource["doctor"].StatusCounts["skip"] != 1 {
		t.Fatalf("doctor counts = %+v, want one skip", summary.CountsBySource["doctor"])
	}
	assertDoctorNextActions(t, summary.NextActions, []string{"review-warnings", "inspect-doctor-results", "apply-safe-fixes", "plan-restart"})
}

func TestNewReadinessSummaryBlocked(t *testing.T) {
	t.Parallel()

	preflightResults := []preflight.Result{{ID: "PRE-FAIL", Status: preflight.StatusFail, Severity: "fatal"}}
	preflightSummary := preflight.NewAssessmentSummary(preflightResults)
	installSummary := install.NewReadinessSummary(preflightResults, install.RemediationPlan{Steps: []install.RemediationStep{{ApplyMode: install.ApplyModeManual, RequiresRestart: true}}})
	summary := NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-FAIL", Status: StatusFail, Severity: "fatal"}})

	if summary.Status != ReadinessBlocked {
		t.Fatalf("Status = %q, want %q", summary.Status, ReadinessBlocked)
	}
	if summary.CountsBySource["preflight"].StatusCounts["fail"] != 1 || summary.CountsBySource["install"].StatusCounts["blocked"] != 1 || summary.CountsBySource["doctor"].StatusCounts["fail"] != 1 {
		t.Fatalf("CountsBySource = %+v, want fail/blocked/fail", summary.CountsBySource)
	}
	assertDoctorNextActions(t, summary.NextActions, []string{"fix-blockers", "inspect-doctor-results", "plan-restart"})
}

func TestMarshalReadinessSummaryJSON(t *testing.T) {
	t.Parallel()

	summary := ReadinessSummary{
		Status: ReadinessDegraded,
		CountsBySource: map[string]SourceCounts{
			"doctor": {Total: 1, StatusCounts: map[string]int{"warn": 1}},
		},
		SafeFixCount: 1,
		NextActions:  []string{"inspect-doctor-results", "apply-safe-fixes"},
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
	if decoded.Status != ReadinessDegraded || decoded.CountsBySource["doctor"].StatusCounts["warn"] != 1 {
		t.Fatalf("decoded = %+v, want degraded doctor warning", decoded)
	}
}

func TestNewOperatorReadinessRequiresPhaseZeroThroughThreeCoverage(t *testing.T) {
	t.Parallel()

	summary := healthyReadinessSummary()
	readiness := NewOperatorReadiness(summary, nil)

	if readiness.SchemaVersion != OperatorReadinessSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", readiness.SchemaVersion, OperatorReadinessSchemaVersion)
	}
	if readiness.Status != LocalReadinessNeedsReview {
		t.Fatalf("Status = %q, want %q", readiness.Status, LocalReadinessNeedsReview)
	}
	if len(readiness.MissingDomains) != 6 {
		t.Fatalf("MissingDomains = %+v, want six non-install/doctor domains", readiness.MissingDomains)
	}
	assertOperatorPhaseStatus(t, readiness, 0, LocalReadinessReady)
	assertOperatorPhaseStatus(t, readiness, 1, LocalReadinessReady)
	assertOperatorPhaseStatus(t, readiness, 2, LocalReadinessNeedsReview)
	assertOperatorPhaseStatus(t, readiness, 3, LocalReadinessNeedsReview)
}

func TestNewOperatorReadinessReadyWhenAllLocalPlannerGatesPass(t *testing.T) {
	t.Parallel()

	readiness := NewOperatorReadiness(healthyReadinessSummary(), []OperatorReadinessGate{
		{Phase: 2, Domain: OperatorDomainNotifications, Source: "notifications.BuildDeliveryPreview", Status: LocalReadinessReady, References: []string{"docs/control-plane/notifications.md"}},
		{Phase: 2, Domain: OperatorDomainMarketplace, Source: "templates.SearchMarketplace", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainNetworking, Source: "networking.CheckInternalDNSResolverReadiness", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainRuntimeAdapter, Source: "runtime.conformance.RunAcceptanceSubset", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainRegistryCache, Source: "registry.PlanCache", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainVolumeAttach, Source: "backup.PlanVolumeAttachments", Status: LocalReadinessReady},
	})

	if readiness.Status != LocalReadinessReady {
		t.Fatalf("Status = %q, want %q: %+v", readiness.Status, LocalReadinessReady, readiness)
	}
	if len(readiness.MissingDomains) != 0 {
		t.Fatalf("MissingDomains = %+v, want none", readiness.MissingDomains)
	}
	assertOperatorPhaseStatus(t, readiness, 3, LocalReadinessReady)
}

func TestNewOperatorReadinessBlocksOnLocalPlannerGate(t *testing.T) {
	t.Parallel()

	readiness := NewOperatorReadiness(healthyReadinessSummary(), []OperatorReadinessGate{
		{Phase: 2, Domain: OperatorDomainNotifications, Source: "notifications.BuildDeliveryPreview", Status: LocalReadinessReady},
		{Phase: 2, Domain: OperatorDomainMarketplace, Source: "templates.SearchMarketplace", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainNetworking, Source: "networking.CheckInternalDNSResolverReadiness", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainRuntimeAdapter, Source: "runtime.conformance.RunAcceptanceSubset", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainRegistryCache, Source: "registry.PlanCache", Status: LocalReadinessReady},
		{Phase: 3, Domain: OperatorDomainVolumeAttach, Source: "backup.PlanVolumeAttachments", Status: LocalReadinessBlocked, Reason: "backend does not allow volume attach/detach"},
	})

	if readiness.Status != LocalReadinessBlocked {
		t.Fatalf("Status = %q, want %q", readiness.Status, LocalReadinessBlocked)
	}
	assertOperatorDomainStatus(t, readiness, 3, OperatorDomainVolumeAttach, LocalReadinessBlocked)
	if len(readiness.MissingDomains) != 0 {
		t.Fatalf("MissingDomains = %+v, want none", readiness.MissingDomains)
	}
	data, err := MarshalOperatorReadinessJSON(readiness)
	if err != nil {
		t.Fatalf("MarshalOperatorReadinessJSON() error = %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("operator readiness JSON is invalid: %s", data)
	}
}

func assertDoctorNextActions(t *testing.T, got, want []string) {
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

func healthyReadinessSummary() ReadinessSummary {
	preflightSummary := preflight.NewAssessmentSummary([]preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}})
	installSummary := install.NewReadinessSummary([]preflight.Result{{ID: "PRE-OK", Status: preflight.StatusPass, Severity: "fatal"}}, install.RemediationPlan{})
	return NewReadinessSummary(preflightSummary, installSummary, []Result{{ID: "DOC-OK", Status: StatusPass, Severity: "fatal"}})
}

func assertOperatorPhaseStatus(t *testing.T, readiness OperatorReadiness, phase int, want LocalReadinessStatus) {
	t.Helper()
	for _, got := range readiness.Phases {
		if got.Phase == phase {
			if got.Status != want {
				t.Fatalf("phase %d status = %q, want %q", phase, got.Status, want)
			}
			return
		}
	}
	t.Fatalf("phase %d missing from %+v", phase, readiness.Phases)
}

func assertOperatorDomainStatus(t *testing.T, readiness OperatorReadiness, phase int, domain string, want LocalReadinessStatus) {
	t.Helper()
	for _, gotPhase := range readiness.Phases {
		if gotPhase.Phase != phase {
			continue
		}
		for _, gotDomain := range gotPhase.Domains {
			if gotDomain.Domain == domain {
				if gotDomain.Status != want {
					t.Fatalf("phase %d domain %s status = %q, want %q", phase, domain, gotDomain.Status, want)
				}
				return
			}
		}
	}
	t.Fatalf("phase %d domain %s missing from %+v", phase, domain, readiness.Phases)
}
