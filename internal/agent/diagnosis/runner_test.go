// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package diagnosis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// failedOp returns a deterministic failed deploy operation used across
// every test in this file. Building it inline keeps each test focused
// on the behaviour under test rather than fixture plumbing.
func failedOp(id string) orailtypes.Operation {
	return orailtypes.Operation{
		ID:     orailtypes.ID(id),
		Kind:   "deploy",
		State:  orailtypes.OperationFailed,
		Code:   "RECONCILE_STEP_FAILED",
		Reason: "promote failed: traefik dynamic publication missing",
	}
}

// fakeCaller is a single-shot Caller stub. The runner only uses Send,
// so Stream is implemented as an error to make any accidental usage
// loud.
type fakeCaller struct {
	mu       sync.Mutex
	requests []CallRequest
	result   CallResult
	err      error
}

func (c *fakeCaller) Send(_ context.Context, req CallRequest) (CallResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	if c.err != nil {
		return CallResult{}, c.err
	}
	return c.result, nil
}

func (c *fakeCaller) Stream(_ context.Context, _ CallRequest, _ chan<- StreamEvent) error {
	return errors.New("fakeCaller.Stream not used by the diagnosis runner")
}

func (c *fakeCaller) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *fakeCaller) lastRequest() (CallRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return CallRequest{}, false
	}
	return c.requests[len(c.requests)-1], true
}

type fakeConversation struct {
	mu       sync.Mutex
	started  []string
	tags     []map[string]string
	messages []string
	conv     Conversation
	startErr error
}

func (f *fakeConversation) StartConversation(_ context.Context, title string, tags map[string]string) (Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return Conversation{}, f.startErr
	}
	f.started = append(f.started, title)
	copied := make(map[string]string, len(tags))
	for k, v := range tags {
		copied[k] = v
	}
	f.tags = append(f.tags, copied)
	if f.conv.ID == "" {
		f.conv.ID = "conv-fake-1"
	}
	return f.conv, nil
}

func (f *fakeConversation) AppendMessage(_ context.Context, _ string, _ string, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, content)
	return nil
}

func (f *fakeConversation) startedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func (f *fakeConversation) lastMessage() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		return "", false
	}
	return f.messages[len(f.messages)-1], true
}

type fakeMetadataWriter struct {
	mu      sync.Mutex
	written map[orailtypes.ID]string
}

func (f *fakeMetadataWriter) SetDiagnosisID(_ context.Context, id orailtypes.ID, conversationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.written == nil {
		f.written = make(map[orailtypes.ID]string)
	}
	f.written[id] = conversationID
	return nil
}

func (f *fakeMetadataWriter) lookup(id orailtypes.ID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	conv, ok := f.written[id]
	return conv, ok
}

type fakeEventAppender struct {
	mu     sync.Mutex
	events []api.OperationEvent
}

func (f *fakeEventAppender) AppendEvent(_ context.Context, event api.OperationEvent) (api.OperationEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return event, nil
}

func (f *fakeEventAppender) snapshot() []api.OperationEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.OperationEvent(nil), f.events...)
}

// syncAsync runs the supplied function in the calling goroutine. The
// runner depends on its Options.AsyncRun seam to schedule work; the
// tests use this so OnFailed's effects are observable synchronously
// without sleeps or polling.
func syncAsync(fn func()) { fn() }

// newTestRunner wires a Runner with every dependency replaced by a
// deterministic stub. Returning the stubs lets each test assert on the
// surface it cares about.
func newTestRunner(t *testing.T, caller *fakeCaller, conv *fakeConversation) (*Runner, *fakeMetadataWriter, *fakeEventAppender) {
	t.Helper()
	meta := &fakeMetadataWriter{}
	events := &fakeEventAppender{}
	runner := NewRunner(Options{
		Enabled:  true,
		AsyncRun: syncAsync,
	})
	runner.Caller = caller
	runner.Conversations = conv
	runner.Metadata = meta
	runner.Events = events
	return runner, meta, events
}

func TestRunnerHappyPathAttachesDiagnosis(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "- set_variable(PORT=8080)\n"}}
	conv := &fakeConversation{conv: Conversation{ID: "conv-happy-1"}}
	runner, meta, events := newTestRunner(t, caller, conv)

	op := failedOp("op_happy_abc12345")
	runner.OnFailed(context.Background(), op, []api.OperationEvent{
		{OperationID: op.ID, State: orailtypes.OperationRunning, Message: "pull-verify started"},
		{OperationID: op.ID, State: orailtypes.OperationFailed, Message: "promote failed: traefik dynamic publication missing"},
	})

	if caller.calls() != 1 {
		t.Fatalf("Caller.Send calls = %d, want 1", caller.calls())
	}
	req, ok := caller.lastRequest()
	if !ok {
		t.Fatalf("Caller.Send was never invoked")
	}
	if req.Model != DefaultModel {
		t.Fatalf("CallRequest.Model = %q, want %q", req.Model, DefaultModel)
	}
	if req.System != SystemPreamble {
		t.Fatalf("CallRequest.System = %q, want preamble", req.System)
	}
	if req.AllowMutations {
		t.Fatalf("CallRequest.AllowMutations = true, want false")
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("CallRequest.Messages = %#v, want one user message", req.Messages)
	}
	if !strings.Contains(req.Messages[0].Content, string(op.ID)) {
		t.Fatalf("user message missing operation id: %q", req.Messages[0].Content)
	}
	if conv.startedCount() != 1 {
		t.Fatalf("Conversations.Start calls = %d, want 1", conv.startedCount())
	}
	if title := conv.started[0]; !strings.HasPrefix(title, "Diagnosis:") {
		t.Fatalf("conversation title = %q, want \"Diagnosis:\" prefix", title)
	}
	got, ok := conv.lastMessage()
	if !ok || !strings.Contains(got, "set_variable") {
		t.Fatalf("appended message = %q, want diagnosis body", got)
	}
	if got, ok := meta.lookup(op.ID); !ok || got != "conv-happy-1" {
		t.Fatalf("metadata diagnosis_id = %q, want conv-happy-1 (ok=%v)", got, ok)
	}
	appended := events.snapshot()
	if len(appended) != 1 || !strings.Contains(appended[0].Message, EventNameDiagnosisAttached) {
		t.Fatalf("appended events = %#v, want one %s event", appended, EventNameDiagnosisAttached)
	}
}

func TestRunnerIgnoresNonDiagnosableKinds(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "ignored"}}
	conv := &fakeConversation{}
	runner, _, _ := newTestRunner(t, caller, conv)

	op := failedOp("op_backup_1")
	op.Kind = "backup"
	runner.OnFailed(context.Background(), op, nil)
	if caller.calls() != 0 {
		t.Fatalf("Caller.Send calls = %d, want 0 for non-diagnosable kind", caller.calls())
	}
}

func TestRunnerDedupePerOperation(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "diagnosis"}}
	conv := &fakeConversation{}
	runner, _, _ := newTestRunner(t, caller, conv)

	op := failedOp("op_dedupe_1")
	for i := 0; i < 5; i++ {
		runner.OnFailed(context.Background(), op, nil)
	}
	if caller.calls() != 1 {
		t.Fatalf("Caller.Send calls = %d, want 1 (dedupe enforced)", caller.calls())
	}
}

func TestRunnerRespectsRateLimit(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "diagnosis"}}
	conv := &fakeConversation{}
	runner := NewRunner(Options{
		Enabled:    true,
		AsyncRun:   syncAsync,
		RateLimit:  2,
		RateWindow: time.Minute,
	})
	runner.Caller = caller
	runner.Conversations = conv

	for i := 0; i < 5; i++ {
		runner.OnFailed(context.Background(), failedOp("op_rate_"+itoa(i)), nil)
	}
	if caller.calls() != 2 {
		t.Fatalf("Caller.Send calls = %d, want 2 (token bucket cap)", caller.calls())
	}
}

func TestRunnerCircuitBreakerOpensAfterConsecutiveErrors(t *testing.T) {
	caller := &fakeCaller{err: errors.New("boom")}
	conv := &fakeConversation{}
	// Use a high rate limit so the breaker — not the bucket — is the
	// gate under test. Threshold of 3 mirrors the production default
	// and matches the ADR.
	runner := NewRunner(Options{
		Enabled:          true,
		AsyncRun:         syncAsync,
		RateLimit:        100,
		RateWindow:       time.Minute,
		CircuitThreshold: 3,
		CircuitCooldown:  time.Hour,
	})
	runner.Caller = caller
	runner.Conversations = conv

	for i := 0; i < 3; i++ {
		runner.OnFailed(context.Background(), failedOp("op_breaker_"+itoa(i)), nil)
	}
	failures, openedAt := runner.breaker.State()
	if failures != 3 || openedAt.IsZero() {
		t.Fatalf("breaker state failures=%d openedAt=%v, want failures=3 openedAt set", failures, openedAt)
	}
	// Next OnFailed should be skipped before reaching the caller.
	runner.OnFailed(context.Background(), failedOp("op_breaker_after_open"), nil)
	if caller.calls() != 3 {
		t.Fatalf("Caller.Send calls = %d, want 3 (breaker prevented 4th call)", caller.calls())
	}
}

func TestRunnerSwallowsCallerErrorsWithoutRefailingOperation(t *testing.T) {
	// The contract documented in ADR-0025 is: a Caller failure MUST
	// never re-enter the operation FSM. The test asserts that no
	// metadata write, no event append, and no conversation start
	// happen when the agent call errors. Combined with the runner's
	// detached context, this is the safety net.
	caller := &fakeCaller{err: errors.New("caller exploded")}
	conv := &fakeConversation{}
	runner, meta, events := newTestRunner(t, caller, conv)

	op := failedOp("op_swallow_1")
	runner.OnFailed(context.Background(), op, []api.OperationEvent{
		{OperationID: op.ID, State: orailtypes.OperationFailed, Message: "boom"},
	})

	if conv.startedCount() != 0 {
		t.Fatalf("Conversations.Start calls = %d, want 0 on caller error", conv.startedCount())
	}
	if _, ok := meta.lookup(op.ID); ok {
		t.Fatalf("metadata diagnosis_id was set despite caller error")
	}
	if got := events.snapshot(); len(got) != 0 {
		t.Fatalf("AppendEvent calls = %d, want 0 on caller error", len(got))
	}
}

func TestRunnerDisabledWhenEnvOff(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "diagnosis"}}
	conv := &fakeConversation{}
	runner := NewRunner(Options{
		// Enabled left false; env not set in test process.
		AsyncRun: syncAsync,
	})
	runner.Caller = caller
	runner.Conversations = conv

	runner.OnFailed(context.Background(), failedOp("op_off_1"), nil)
	if caller.calls() != 0 {
		t.Fatalf("Caller.Send calls = %d, want 0 when disabled", caller.calls())
	}
}

func TestRunnerSkipsWhenCallerOrConversationMissing(t *testing.T) {
	// Production wiring may stage the runner with only Conversations
	// or only Caller in place; OnFailed should bow out cleanly without
	// consuming dedupe + rate budget.
	caller := &fakeCaller{result: CallResult{Text: "diagnosis"}}
	runner := NewRunner(Options{Enabled: true, AsyncRun: syncAsync})
	// Conversations intentionally left nil.
	runner.Caller = caller

	op := failedOp("op_partial_1")
	runner.OnFailed(context.Background(), op, nil)
	if caller.calls() != 0 {
		t.Fatalf("Caller.Send calls = %d, want 0 without ConversationStore", caller.calls())
	}
	// Verify the runner did not claim the dedupe slot — a later wiring
	// with Conversations should still get its one chance.
	runner.Conversations = &fakeConversation{}
	runner.OnFailed(context.Background(), op, nil)
	if caller.calls() != 1 {
		t.Fatalf("Caller.Send calls = %d after wiring, want 1", caller.calls())
	}
}

func TestRunnerIgnoresNonFailedTransitions(t *testing.T) {
	caller := &fakeCaller{result: CallResult{Text: "diagnosis"}}
	conv := &fakeConversation{}
	runner, _, _ := newTestRunner(t, caller, conv)

	op := failedOp("op_nonfailed")
	op.State = orailtypes.OperationSucceeded
	runner.OnFailed(context.Background(), op, nil)
	if caller.calls() != 0 {
		t.Fatalf("Caller.Send calls = %d, want 0 when op.State != FAILED", caller.calls())
	}
}

func TestRunnerPanicInCallerIsRecovered(t *testing.T) {
	// A panicking Caller would otherwise crash the runner goroutine.
	// The runner's deferred recovery converts it to a logged failure
	// and bumps the breaker.
	caller := panickingCaller{}
	conv := &fakeConversation{}
	runner := NewRunner(Options{
		Enabled:          true,
		AsyncRun:         syncAsync,
		CircuitThreshold: 5,
		CircuitCooldown:  time.Minute,
	})
	runner.Caller = caller
	runner.Conversations = conv

	op := failedOp("op_panic_1")
	runner.OnFailed(context.Background(), op, nil)
	failures, _ := runner.breaker.State()
	if failures != 1 {
		t.Fatalf("breaker failures after panic = %d, want 1", failures)
	}
}

func TestObservedStoreEmitsOnFailAndForwardsErrors(t *testing.T) {
	// Sanity test the wrapper this package depends on. Using the
	// in-memory store keeps the test boundary inside internal/api.
	base := api.NewMemoryOperationStore()
	ctx := context.Background()
	op, err := base.Create(ctx, orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	var fired atomic.Int32
	store := api.NewObservedOperationStore(base)
	store.Register(api.OperationLifecycleObserverFunc(func(_ context.Context, _ orailtypes.Operation, _ []api.OperationEvent) {
		fired.Add(1)
	}))
	if _, err := store.Fail(ctx, op.ID, "WORKER_ERROR", "kaboom"); err != nil {
		t.Fatal(err)
	}
	if fired.Load() != 1 {
		t.Fatalf("observer fires = %d, want 1", fired.Load())
	}
	if _, err := store.Fail(ctx, op.ID, "AGAIN", "again"); !errors.Is(err, api.ErrOperationInvalidTransition) {
		t.Fatalf("re-fail error = %v, want ErrOperationInvalidTransition", err)
	}
	if fired.Load() != 1 {
		t.Fatalf("observer fires after invalid transition = %d, want 1", fired.Load())
	}
}

// panickingCaller exists to assert the runner's panic-recovery path.
type panickingCaller struct{}

func (panickingCaller) Send(_ context.Context, _ CallRequest) (CallResult, error) {
	panic("caller panic")
}

func (panickingCaller) Stream(_ context.Context, _ CallRequest, _ chan<- StreamEvent) error {
	return errors.New("unused")
}

// itoa avoids pulling strconv into the test surface for one digit.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := "0123456789"
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	return out
}
