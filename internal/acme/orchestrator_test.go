package acme

import (
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func newTestOrchestrator(t *testing.T) (*Orchestrator, *time.Time) {
	t.Helper()
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	return NewOrchestrator(clock), &now
}

func TestOrchestratorRegistersAndIssuesHappyPath(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	register, err := orch.Register("order-1", "App.example.test")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if register.Domain != "app.example.test" || register.To != OrchestratorStateOrderPending {
		t.Fatalf("unexpected register event: %#v", register)
	}

	steps := []struct {
		eventType OrchestratorEventType
		want      OrchestratorState
	}{
		{OrchestratorEventDNSPresented, OrchestratorStateDNSChallengePresented},
		{OrchestratorEventValidationStarted, OrchestratorStateValidationWaiting},
		{OrchestratorEventValidationSucceeded, OrchestratorStateIssued},
	}
	for _, step := range steps {
		event, err := orch.Apply("order-1", step.eventType, "")
		if err != nil {
			t.Fatalf("Apply(%s): %v", step.eventType, err)
		}
		if event.To != step.want {
			t.Fatalf("Apply(%s) -> %s, want %s", step.eventType, event.To, step.want)
		}
	}
	state, ok := orch.State("order-1")
	if !ok || state != OrchestratorStateIssued {
		t.Fatalf("State() = (%s, %v), want issued/true", state, ok)
	}
	events := orch.Events("order-1")
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4", len(events))
	}
}

func TestOrchestratorRetryableLoopsBackToPending(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	if _, err := orch.Register("order-1", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := orch.Apply("order-1", OrchestratorEventDNSPresented, ""); err != nil {
		t.Fatalf("dns-presented: %v", err)
	}
	if _, err := orch.Apply("order-1", OrchestratorEventValidationStarted, ""); err != nil {
		t.Fatalf("validation-started: %v", err)
	}
	event, err := orch.Apply("order-1", OrchestratorEventValidationRetryable, "transient resolver error")
	if err != nil {
		t.Fatalf("retryable: %v", err)
	}
	if event.Reason != failurecodes.ACMEOrderFailedRetryable {
		t.Fatalf("reason = %q, want %q", event.Reason, failurecodes.ACMEOrderFailedRetryable)
	}
	if event.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %s, want positive", event.RetryAfter)
	}
	if _, err := orch.Apply("order-1", OrchestratorEventResetForRetry, ""); err != nil {
		t.Fatalf("reset-for-retry: %v", err)
	}
	state, _ := orch.State("order-1")
	if state != OrchestratorStateOrderPending {
		t.Fatalf("state after reset = %s, want order-pending", state)
	}
}

func TestOrchestratorPermanentFailureIsTerminal(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	if _, err := orch.Register("order-1", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	event, err := orch.Apply("order-1", OrchestratorEventValidationPermanent, "CAA refusal")
	if err != nil {
		t.Fatalf("permanent: %v", err)
	}
	if event.To != OrchestratorStateFailedPermanent || event.Reason != failurecodes.ACMEOrderFailedPermanent {
		t.Fatalf("unexpected permanent event: %#v", event)
	}
	if _, err := orch.Apply("order-1", OrchestratorEventResetForRetry, ""); err == nil {
		t.Fatal("expected reset to be rejected from terminal state")
	}
}

func TestOrchestratorRejectsInvalidTransitionWithRegisteredReason(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	if _, err := orch.Register("order-1", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := orch.Apply("order-1", OrchestratorEventValidationStarted, "")
	if err == nil {
		t.Fatal("expected invalid transition error")
	}
	code, hint, ok := failurecodes.ReasonOf(err)
	if !ok {
		t.Fatalf("expected registered reason, got %v", err)
	}
	if code != failurecodes.OperationInvalidTransition || hint == "" {
		t.Fatalf("invalid transition reason = (%q, %q)", code, hint)
	}
	var failure *failurecodes.FailureError
	if !errors.As(err, &failure) {
		t.Fatal("expected *FailureError unwrap")
	}
}

func TestOrchestratorRejectsDuplicateRegister(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	if _, err := orch.Register("order-1", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := orch.Register("order-1", "app.example.test"); err == nil {
		t.Fatal("expected duplicate register error")
	}
}

func TestOrchestratorAllOrderIDsIsSorted(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	for _, id := range []string{"c", "a", "b"} {
		if _, err := orch.Register(id, "domain.example.test"); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	got := orch.AllOrderIDs()
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("AllOrderIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllOrderIDs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
