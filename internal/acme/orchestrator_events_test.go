package acme

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestOrchestratorEmitsEventsForHappyPath(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)

	if _, err := orch.Register("order-happy", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	steps := []OrchestratorEventType{
		OrchestratorEventDNSPresented,
		OrchestratorEventValidationStarted,
		OrchestratorEventValidationSucceeded,
	}
	for _, step := range steps {
		if _, err := orch.Apply("order-happy", step, ""); err != nil {
			t.Fatalf("Apply(%s): %v", step, err)
		}
	}

	events := sink.Events()
	if len(events) != 4 {
		t.Fatalf("captured %d events, want 4 (register + 3 transitions)", len(events))
	}
	wantTransitions := []struct {
		from OrchestratorState
		to   OrchestratorState
	}{
		{"", OrchestratorStateOrderPending},
		{OrchestratorStateOrderPending, OrchestratorStateDNSChallengePresented},
		{OrchestratorStateDNSChallengePresented, OrchestratorStateValidationWaiting},
		{OrchestratorStateValidationWaiting, OrchestratorStateIssued},
	}
	for i, want := range wantTransitions {
		got := events[i]
		if got.From != want.from || got.To != want.to {
			t.Fatalf("event %d: from=%q to=%q, want from=%q to=%q", i, got.From, got.To, want.from, want.to)
		}
		if got.At != now {
			t.Fatalf("event %d At = %v, want %v", i, got.At, now)
		}
		if got.OrderID != "order-happy" {
			t.Fatalf("event %d OrderID = %q", i, got.OrderID)
		}
	}
}

func TestOrchestratorEmitsRetryableFailureEvent(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)

	if _, err := orch.Register("order-retryable", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := orch.Apply("order-retryable", OrchestratorEventDNSPresented, ""); err != nil {
		t.Fatalf("dns-presented: %v", err)
	}
	if _, err := orch.Apply("order-retryable", OrchestratorEventValidationStarted, ""); err != nil {
		t.Fatalf("validation-started: %v", err)
	}
	if _, err := orch.Apply("order-retryable", OrchestratorEventValidationRetryable, "transient resolver error"); err != nil {
		t.Fatalf("retryable: %v", err)
	}

	events := sink.Events()
	if len(events) != 4 {
		t.Fatalf("captured %d events, want 4", len(events))
	}
	last := events[3]
	if last.To != OrchestratorStateFailedRetryable {
		t.Fatalf("last To = %q, want %q", last.To, OrchestratorStateFailedRetryable)
	}
	if last.Reason != failurecodes.ACMEOrderFailedRetryable {
		t.Fatalf("last Reason = %q, want %q", last.Reason, failurecodes.ACMEOrderFailedRetryable)
	}
	if last.RetryAfter <= 0 {
		t.Fatalf("RetryAfter = %s, want positive", last.RetryAfter)
	}
	if last.Detail != "transient resolver error" {
		t.Fatalf("Detail = %q", last.Detail)
	}
}

func TestOrchestratorEmitsPermanentFailureEvent(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)

	if _, err := orch.Register("order-perm", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := orch.Apply("order-perm", OrchestratorEventValidationPermanent, "CAA refusal"); err != nil {
		t.Fatalf("permanent: %v", err)
	}

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("captured %d events, want 2 (register + permanent)", len(events))
	}
	last := events[1]
	if last.To != OrchestratorStateFailedPermanent {
		t.Fatalf("last To = %q, want %q", last.To, OrchestratorStateFailedPermanent)
	}
	if last.Reason != failurecodes.ACMEOrderFailedPermanent {
		t.Fatalf("last Reason = %q, want %q", last.Reason, failurecodes.ACMEOrderFailedPermanent)
	}
	if last.From != OrchestratorStateOrderPending {
		t.Fatalf("From = %q, want %q", last.From, OrchestratorStateOrderPending)
	}
}

func TestOrchestratorEmitsResetForRetryEvent(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)

	if _, err := orch.Register("order-reset", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, step := range []OrchestratorEventType{
		OrchestratorEventDNSPresented,
		OrchestratorEventValidationStarted,
		OrchestratorEventValidationRetryable,
		OrchestratorEventResetForRetry,
	} {
		if _, err := orch.Apply("order-reset", step, ""); err != nil {
			t.Fatalf("Apply(%s): %v", step, err)
		}
	}

	events := sink.Events()
	if len(events) != 5 {
		t.Fatalf("captured %d events, want 5", len(events))
	}
	last := events[4]
	if last.To != OrchestratorStateOrderPending || last.From != OrchestratorStateFailedRetryable {
		t.Fatalf("reset-for-retry transition wrong: from=%q to=%q", last.From, last.To)
	}
}

func TestOrchestratorDoesNotEmitForInvalidTransition(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)

	if _, err := orch.Register("order-invalid", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Apply ValidationStarted before DNS-Presented: not a legal transition.
	if _, err := orch.Apply("order-invalid", OrchestratorEventValidationStarted, ""); err == nil {
		t.Fatal("expected invalid transition error")
	}

	events := sink.Events()
	if len(events) != 1 {
		t.Fatalf("captured %d events, want 1 (register only)", len(events))
	}
	if events[0].To != OrchestratorStateOrderPending {
		t.Fatalf("captured event To = %q, want %q", events[0].To, OrchestratorStateOrderPending)
	}
}

func TestOrchestratorEventSinkFuncAdapter(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	orch := NewOrchestrator(func() time.Time { return now })

	captured := make([]OrchestratorEvent, 0, 2)
	orch.SetEventSink(EventSinkFunc(func(event OrchestratorEvent) {
		captured = append(captured, event)
	}))

	if _, err := orch.Register("order-func", "app.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := orch.Apply("order-func", OrchestratorEventDNSPresented, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("captured %d events, want 2", len(captured))
	}
}

func TestOrchestratorSetEventSinkNilSilencesEmission(t *testing.T) {
	orch, _ := newTestOrchestrator(t)
	sink := NewCapturedEventSink()
	orch.SetEventSink(sink)
	if _, err := orch.Register("order-x", "x.example.test"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("after first register, captured %d events", got)
	}
	orch.SetEventSink(nil)
	if _, err := orch.Apply("order-x", OrchestratorEventDNSPresented, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("expected nil sink to silence emission, got %d events", got)
	}
}

func TestCapturedEventSinkResetClearsHistory(t *testing.T) {
	sink := NewCapturedEventSink()
	sink.Receive(OrchestratorEvent{OrderID: "a"})
	sink.Receive(OrchestratorEvent{OrderID: "b"})
	if got := len(sink.Events()); got != 2 {
		t.Fatalf("captured %d events, want 2", got)
	}
	sink.Reset()
	if got := len(sink.Events()); got != 0 {
		t.Fatalf("after Reset, captured %d events, want 0", got)
	}
}
