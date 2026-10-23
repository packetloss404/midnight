package harness

import (
	"strings"
	"testing"
)

func TestAssertionResultPassed(t *testing.T) {
	if !((AssertionResult{Status: AssertionPassed}).Passed()) {
		t.Fatal("Passed() = false, want true for passed assertion")
	}
	if (AssertionResult{Status: AssertionFailed}).Passed() {
		t.Fatal("Passed() = true, want false for failed assertion")
	}
}

func TestEmitJUnitXML(t *testing.T) {
	xml, err := EmitJUnitXML([]AssertionResult{
		{ID: "D-01", Status: AssertionPassed},
		{ID: "D-02", Status: AssertionFailed, ReasonCode: "phase0.failed", Diagnostic: "expected alert"},
		{ID: "D-03", Status: AssertionSkipped, ReasonCode: "phase0.skipped"},
	})
	if err != nil {
		t.Fatalf("EmitJUnitXML() error = %v", err)
	}
	got := string(xml)
	for _, want := range []string{`tests="3"`, `failures="1"`, `skipped="1"`, `name="D-02"`, `message="phase0.failed"`, `expected alert`} {
		if !strings.Contains(got, want) {
			t.Fatalf("EmitJUnitXML() = %s, want %q", got, want)
		}
	}
}

func TestEmitJUnitXMLRejectsUnknownStatus(t *testing.T) {
	_, err := EmitJUnitXML([]AssertionResult{{ID: "D-01", Status: AssertionStatus("unknown")}})
	if err == nil {
		t.Fatal("EmitJUnitXML() error = nil, want unknown status error")
	}
}

func TestEvaluateDrillsComputesStatuses(t *testing.T) {
	evaluations, err := EvaluateDrills([]DrillAssertionResults{
		{DrillID: "D-01", Assertions: []AssertionResult{{ID: "a", Status: AssertionPassed}}},
		{DrillID: "D-02", Assertions: []AssertionResult{{ID: "a", Status: AssertionFailed}}},
		{DrillID: "D-03", Assertions: []AssertionResult{{ID: "a", Status: AssertionSkipped}}},
		{DrillID: "D-04"},
	})
	if err != nil {
		t.Fatalf("EvaluateDrills() error = %v", err)
	}

	want := []DrillStatus{DrillPassed, DrillFailed, DrillBlocked, DrillPlaceholder}
	for i, status := range want {
		if evaluations[i].Status != status {
			t.Fatalf("EvaluateDrills()[%d].Status = %q, want %q", i, evaluations[i].Status, status)
		}
	}
	if evaluations[3].Placeholders != 1 {
		t.Fatalf("EvaluateDrills() placeholder count = %d, want 1", evaluations[3].Placeholders)
	}
}

func TestEvaluateDrillsFailedOverridesBlocked(t *testing.T) {
	evaluations, err := EvaluateDrills([]DrillAssertionResults{{DrillID: "D-01", Assertions: []AssertionResult{
		{ID: "a", Status: AssertionSkipped},
		{ID: "b", Status: AssertionFailed},
	}}})
	if err != nil {
		t.Fatalf("EvaluateDrills() error = %v", err)
	}
	if evaluations[0].Status != DrillFailed || evaluations[0].Failed != 1 || evaluations[0].Blocked != 1 {
		t.Fatalf("EvaluateDrills() = %+v, want failed with blocked count", evaluations[0])
	}
}

func TestEvaluateDrillsRejectsUnknownAssertionStatus(t *testing.T) {
	_, err := EvaluateDrills([]DrillAssertionResults{{DrillID: "D-01", Assertions: []AssertionResult{{ID: "a", Status: AssertionStatus("unknown")}}}})
	if err == nil {
		t.Fatal("EvaluateDrills() error = nil, want unknown status error")
	}
}
