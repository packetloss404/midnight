package acme

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

// OrchestratorState is the offline ACME order phase. Live network IO is never
// performed by Phase 1 code; transitions are driven by deterministic events
// emitted by callers (e.g., reconciler workers, restore planner).
type OrchestratorState string

const (
	// OrchestratorStateOrderPending marks an order created but not yet
	// associated with a DNS-01 challenge.
	OrchestratorStateOrderPending OrchestratorState = "order-pending"
	// OrchestratorStateDNSChallengePresented marks an order whose TXT record
	// the caller has confirmed is published.
	OrchestratorStateDNSChallengePresented OrchestratorState = "dns-challenge-presented"
	// OrchestratorStateValidationWaiting marks an order awaiting upstream CA validation.
	OrchestratorStateValidationWaiting OrchestratorState = "validation-waiting"
	// OrchestratorStateIssued marks a successfully issued certificate.
	OrchestratorStateIssued OrchestratorState = "issued"
	// OrchestratorStateFailedRetryable marks a transient failure safe to retry.
	OrchestratorStateFailedRetryable OrchestratorState = "failed-retryable"
	// OrchestratorStateFailedPermanent marks a terminal failure.
	OrchestratorStateFailedPermanent OrchestratorState = "failed-permanent"
)

// OrchestratorEventType enumerates allowed transition triggers.
type OrchestratorEventType string

const (
	// OrchestratorEventDNSPresented is the trigger for order-pending -> dns-challenge-presented.
	OrchestratorEventDNSPresented OrchestratorEventType = "dns-presented"
	// OrchestratorEventValidationStarted is the trigger for dns-challenge-presented -> validation-waiting.
	OrchestratorEventValidationStarted OrchestratorEventType = "validation-started"
	// OrchestratorEventValidationSucceeded is the trigger for validation-waiting -> issued.
	OrchestratorEventValidationSucceeded OrchestratorEventType = "validation-succeeded"
	// OrchestratorEventValidationRetryable is the trigger for validation-waiting -> failed-retryable.
	OrchestratorEventValidationRetryable OrchestratorEventType = "validation-failed-retryable"
	// OrchestratorEventValidationPermanent is the trigger for any state -> failed-permanent.
	OrchestratorEventValidationPermanent OrchestratorEventType = "validation-failed-permanent"
	// OrchestratorEventResetForRetry is the trigger for failed-retryable -> order-pending.
	OrchestratorEventResetForRetry OrchestratorEventType = "reset-for-retry"
)

// OrchestratorEvent is one structured transition record. Events are emitted
// for every transition (including invalid transition attempts) so callers can
// build an immutable audit log.
type OrchestratorEvent struct {
	OrderID    string                `json:"orderId"`
	Domain     string                `json:"domain"`
	From       OrchestratorState     `json:"from"`
	To         OrchestratorState     `json:"to"`
	Type       OrchestratorEventType `json:"type"`
	At         time.Time             `json:"at"`
	Reason     failurecodes.Code     `json:"reason,omitempty"`
	Detail     string                `json:"detail,omitempty"`
	RetryAfter time.Duration         `json:"retryAfterSeconds,omitempty"`
}

// EventSink is the receive end of the orchestrator's structured event
// stream. Every successful transition (including Register, which is a
// transition from the empty state to OrderPending) is forwarded to the sink
// in chronological order. Implementations must be safe for sequential calls
// from a single orchestrator instance; the orchestrator does not invoke the
// sink concurrently for the same order ID.
type EventSink interface {
	// Receive is called once per emitted OrchestratorEvent. Returning an error
	// is logged by future live integrations but does not roll back the state
	// transition; the in-memory order history remains the source of truth.
	Receive(event OrchestratorEvent)
}

// EventSinkFunc adapts a plain function to the EventSink interface.
type EventSinkFunc func(event OrchestratorEvent)

// Receive implements EventSink for EventSinkFunc.
func (f EventSinkFunc) Receive(event OrchestratorEvent) {
	if f == nil {
		return
	}
	f(event)
}

// CapturedEventSink records every event in memory for assertions in tests
// and audit replay. It is safe for concurrent use.
type CapturedEventSink struct {
	mu     sync.Mutex
	events []OrchestratorEvent
}

// NewCapturedEventSink returns an empty CapturedEventSink.
func NewCapturedEventSink() *CapturedEventSink {
	return &CapturedEventSink{}
}

// Receive implements EventSink.
func (c *CapturedEventSink) Receive(event OrchestratorEvent) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// Events returns a defensive copy of every recorded event in receive order.
func (c *CapturedEventSink) Events() []OrchestratorEvent {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]OrchestratorEvent, len(c.events))
	copy(out, c.events)
	return out
}

// Reset clears the captured events. Useful for reusing a sink across
// independent test cases.
func (c *CapturedEventSink) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}

// Orchestrator is an in-memory, offline-only ACME state machine. It records
// transitions, refuses invalid transitions, and tracks per-order events so
// callers can replay history. No network IO is ever performed.
type Orchestrator struct {
	mu     sync.Mutex
	now    func() time.Time
	orders map[string]*orchestratorOrder
	sink   EventSink
}

type orchestratorOrder struct {
	orderID string
	domain  string
	state   OrchestratorState
	events  []OrchestratorEvent
}

// NewOrchestrator constructs an empty orchestrator. When clock is nil it
// defaults to time.Now in UTC. The orchestrator emits no events to a sink
// until SetEventSink is called.
func NewOrchestrator(clock func() time.Time) *Orchestrator {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Orchestrator{
		now:    clock,
		orders: make(map[string]*orchestratorOrder),
	}
}

// SetEventSink installs sink as the receiver for every successful transition
// going forward. It is safe to call at any point in the orchestrator's
// lifetime. Passing a nil sink disables event emission.
func (o *Orchestrator) SetEventSink(sink EventSink) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sink = sink
}

// emitLocked forwards event to the configured sink while o.mu is held. The
// helper must only be called from goroutines that already hold o.mu. The
// sink is invoked synchronously so test assertions can rely on receive order
// matching transition order.
func (o *Orchestrator) emitLocked(event OrchestratorEvent) {
	if o == nil || o.sink == nil {
		return
	}
	o.sink.Receive(event)
}

// Register creates a new order record in the OrderPending state. It returns
// an error when the same order ID is registered twice.
func (o *Orchestrator) Register(orderID, domain string) (OrchestratorEvent, error) {
	if o == nil {
		return OrchestratorEvent{}, errors.New("orchestrator is nil")
	}
	if orderID == "" {
		return OrchestratorEvent{}, errors.New("order ID is required")
	}
	if domain == "" {
		return OrchestratorEvent{}, errors.New("domain is required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.orders[orderID]; ok {
		return OrchestratorEvent{}, fmt.Errorf("order %q is already registered", orderID)
	}
	now := o.now().UTC()
	event := OrchestratorEvent{
		OrderID: orderID,
		Domain:  normalizeDomain(domain),
		From:    "",
		To:      OrchestratorStateOrderPending,
		Type:    "register",
		At:      now,
	}
	o.orders[orderID] = &orchestratorOrder{
		orderID: orderID,
		domain:  event.Domain,
		state:   OrchestratorStateOrderPending,
		events:  []OrchestratorEvent{event},
	}
	o.emitLocked(event)
	return event, nil
}

// State returns the current state for the given order.
func (o *Orchestrator) State(orderID string) (OrchestratorState, bool) {
	if o == nil {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.orders[orderID]
	if !ok {
		return "", false
	}
	return order.state, true
}

// Events returns the immutable per-order event history in chronological order.
func (o *Orchestrator) Events(orderID string) []OrchestratorEvent {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.orders[orderID]
	if !ok {
		return nil
	}
	out := make([]OrchestratorEvent, len(order.events))
	copy(out, order.events)
	return out
}

// AllOrderIDs returns the list of registered order IDs sorted ascending.
func (o *Orchestrator) AllOrderIDs() []string {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.orders))
	for id := range o.orders {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Apply attempts a transition for orderID and returns the emitted event. It
// returns a *failurecodes.FailureError when the transition is invalid so
// callers can surface a registered reason and recovery hint.
func (o *Orchestrator) Apply(orderID string, eventType OrchestratorEventType, detail string) (OrchestratorEvent, error) {
	if o == nil {
		return OrchestratorEvent{}, errors.New("orchestrator is nil")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.orders[orderID]
	if !ok {
		return OrchestratorEvent{}, fmt.Errorf("order %q is not registered", orderID)
	}

	from := order.state
	to, transitionErr := nextState(from, eventType)
	now := o.now().UTC()
	if transitionErr != nil {
		event := OrchestratorEvent{
			OrderID: orderID,
			Domain:  order.domain,
			From:    from,
			To:      from,
			Type:    eventType,
			At:      now,
			Detail:  detail,
		}
		// Do not append invalid-transition events to the order history; they
		// are not part of the order's state machine. Return a failure error so
		// callers learn the registered reason without losing the proposed event.
		return event, failurecodes.Wrap(failurecodes.OperationInvalidTransition, fmt.Sprintf("invalid transition %s for state %s", eventType, from), transitionErr)
	}

	event := OrchestratorEvent{
		OrderID: orderID,
		Domain:  order.domain,
		From:    from,
		To:      to,
		Type:    eventType,
		At:      now,
		Detail:  detail,
	}
	switch eventType {
	case OrchestratorEventValidationRetryable:
		event.Reason = failurecodes.ACMEOrderFailedRetryable
		event.RetryAfter = 60 * time.Second
	case OrchestratorEventValidationPermanent:
		event.Reason = failurecodes.ACMEOrderFailedPermanent
	}
	order.state = to
	order.events = append(order.events, event)
	o.emitLocked(event)
	return event, nil
}

// nextState encodes the allowed transitions table.
func nextState(from OrchestratorState, eventType OrchestratorEventType) (OrchestratorState, error) {
	switch from {
	case OrchestratorStateOrderPending:
		switch eventType {
		case OrchestratorEventDNSPresented:
			return OrchestratorStateDNSChallengePresented, nil
		case OrchestratorEventValidationPermanent:
			return OrchestratorStateFailedPermanent, nil
		}
	case OrchestratorStateDNSChallengePresented:
		switch eventType {
		case OrchestratorEventValidationStarted:
			return OrchestratorStateValidationWaiting, nil
		case OrchestratorEventValidationPermanent:
			return OrchestratorStateFailedPermanent, nil
		}
	case OrchestratorStateValidationWaiting:
		switch eventType {
		case OrchestratorEventValidationSucceeded:
			return OrchestratorStateIssued, nil
		case OrchestratorEventValidationRetryable:
			return OrchestratorStateFailedRetryable, nil
		case OrchestratorEventValidationPermanent:
			return OrchestratorStateFailedPermanent, nil
		}
	case OrchestratorStateFailedRetryable:
		switch eventType {
		case OrchestratorEventResetForRetry:
			return OrchestratorStateOrderPending, nil
		case OrchestratorEventValidationPermanent:
			return OrchestratorStateFailedPermanent, nil
		}
	case OrchestratorStateIssued, OrchestratorStateFailedPermanent:
		// Terminal states; no further transitions are allowed.
	}
	return from, fmt.Errorf("event %q is not allowed from state %q", eventType, from)
}
