package router

import (
	"errors"
	"testing"
)

func TestValidateStepOrderAcceptsCanonicalOrder(t *testing.T) {
	steps := []Step{
		{RouteName: "a", Step: RouteDiffStepNoop},
		{RouteName: "b", Step: RouteDiffStepUpdate},
		{RouteName: "c", Step: RouteDiffStepSwap},
		{RouteName: "d", Step: RouteDiffStepRemove},
		{RouteName: "e", Step: RouteDiffStepCreate},
	}
	if err := ValidateStepOrder(steps); err != nil {
		t.Fatalf("ValidateStepOrder returned %v for canonical order", err)
	}
}

func TestValidateStepOrderAcceptsEmptyInput(t *testing.T) {
	if err := ValidateStepOrder(nil); err != nil {
		t.Fatalf("expected nil error for nil input, got %v", err)
	}
	if err := ValidateStepOrder([]Step{}); err != nil {
		t.Fatalf("expected nil error for empty slice, got %v", err)
	}
}

func TestValidateStepOrderRejectsRemoveBeforeSwap(t *testing.T) {
	steps := []Step{
		{RouteName: "old", Step: RouteDiffStepRemove},
		{RouteName: "swap", Step: RouteDiffStepSwap},
	}
	err := ValidateStepOrder(steps)
	if err == nil {
		t.Fatal("expected violation when remove precedes swap")
	}
	var v StepOrderViolation
	if !errors.As(err, &v) {
		t.Fatalf("expected StepOrderViolation, got %T", err)
	}
	if v.Step != RouteDiffStepSwap || v.PriorStep != RouteDiffStepRemove {
		t.Fatalf("unexpected violation %#v", v)
	}
}

func TestValidateStepOrderRejectsCreateBeforeRemove(t *testing.T) {
	steps := []Step{
		{RouteName: "new", Step: RouteDiffStepCreate},
		{RouteName: "old", Step: RouteDiffStepRemove},
	}
	err := ValidateStepOrder(steps)
	if err == nil {
		t.Fatal("expected violation when create precedes remove")
	}
	var v StepOrderViolation
	if !errors.As(err, &v) {
		t.Fatalf("expected StepOrderViolation, got %T", err)
	}
	if v.Step != RouteDiffStepRemove || v.PriorStep != RouteDiffStepCreate {
		t.Fatalf("unexpected violation %#v", v)
	}
}

func TestValidateStepOrderRejectsCreateBeforeSwap(t *testing.T) {
	steps := []Step{
		{RouteName: "new", Step: RouteDiffStepCreate},
		{RouteName: "swap", Step: RouteDiffStepSwap},
	}
	if err := ValidateStepOrder(steps); err == nil {
		t.Fatal("expected violation when create precedes swap")
	}
}

func TestValidateStepOrderRejectsUnknownStep(t *testing.T) {
	steps := []Step{{RouteName: "x", Step: RouteDiffStep("unknown")}}
	err := ValidateStepOrder(steps)
	if err == nil {
		t.Fatal("expected error for unknown step")
	}
	var v StepOrderViolation
	if errors.As(err, &v) {
		t.Fatalf("unknown step should not produce StepOrderViolation, got %v", err)
	}
}

func TestSortStepsCanonicalProducesValidOrder(t *testing.T) {
	steps := []Step{
		{RouteName: "create-1", Step: RouteDiffStepCreate},
		{RouteName: "remove-1", Step: RouteDiffStepRemove},
		{RouteName: "swap-1", Step: RouteDiffStepSwap},
		{RouteName: "update-1", Step: RouteDiffStepUpdate},
		{RouteName: "noop-1", Step: RouteDiffStepNoop},
		{RouteName: "create-2", Step: RouteDiffStepCreate},
		{RouteName: "swap-2", Step: RouteDiffStepSwap},
	}
	sorted := SortStepsCanonical(steps)
	if err := ValidateStepOrder(sorted); err != nil {
		t.Fatalf("SortStepsCanonical produced invalid order: %v\n%v", err, sorted)
	}
	// Verify stability within ranks: swap-1 must precede swap-2; create-1 must precede create-2.
	swapIdx := func(name string) int {
		for i, e := range sorted {
			if e.RouteName == name {
				return i
			}
		}
		return -1
	}
	if swapIdx("swap-1") >= swapIdx("swap-2") {
		t.Fatal("stable sort within rank not preserved for swaps")
	}
	if swapIdx("create-1") >= swapIdx("create-2") {
		t.Fatal("stable sort within rank not preserved for creates")
	}
}

func TestStepOrderViolationErrorMessageMentionsSteps(t *testing.T) {
	v := StepOrderViolation{
		Index:      2,
		Step:       RouteDiffStepSwap,
		PriorStep:  RouteDiffStepRemove,
		PriorIndex: 1,
	}
	msg := v.Error()
	if msg == "" {
		t.Fatal("error message must not be empty")
	}
	for _, want := range []string{"swap", "remove", "1", "2"} {
		if !contains(msg, want) {
			t.Fatalf("error message %q missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && stringIndex(s, sub) >= 0
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
