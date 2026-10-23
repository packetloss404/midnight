package supervisor

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

func TestRESTLoopActionExecutorRetriesTransientFailures(t *testing.T) {
	attempts := 0
	fake := &fakeRESTExecutor{execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
		attempts++
		if attempts < 3 {
			return protocol.ResponseMeta{StatusCode: http.StatusServiceUnavailable}, protocol.HTTPError{StatusCode: http.StatusServiceUnavailable, Code: "transient"}
		}
		return protocol.ResponseMeta{StatusCode: http.StatusCreated}, nil
	}}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.example"},
			Executor: fake,
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-r"},
		Retry:        RetryPolicy{MaxAttempts: 4, Base: time.Millisecond, Max: time.Millisecond},
		Sleep:        func(time.Duration) {},
		IdempotencyKeyFn: func(action string, attempt int) string {
			return "key-" + action
		},
	}
	if _, err := executor.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(executor.Transcript) != 3 {
		t.Fatalf("expected 3 transcript entries, got %d", len(executor.Transcript))
	}
	first := executor.Transcript[0]
	if first.IdempotencyKey != "key-register" {
		t.Fatalf("expected stable idempotency key, got %s", first.IdempotencyKey)
	}
	if first.RetryClass != RetryClassTransient {
		t.Fatalf("expected transient retry class on first attempt, got %q", first.RetryClass)
	}
	last := executor.Transcript[2]
	if last.Status != "ok" || last.IdempotencyKey != "key-register" {
		t.Fatalf("final transcript entry mismatch: %+v", last)
	}
	if last.RetryClass != RetryClassNone {
		t.Fatalf("expected none retry class on success, got %q", last.RetryClass)
	}
	if len(executor.Terminals) != 1 {
		t.Fatalf("expected 1 terminal record, got %d", len(executor.Terminals))
	}
	terminal := executor.Terminals[0]
	if terminal.Result != RESTLoopActionTerminalOK || terminal.Attempts != 3 {
		t.Fatalf("terminal mismatch: %+v", terminal)
	}
	if terminal.IdempotencyKey != "key-register" {
		t.Fatalf("terminal idempotency key = %q", terminal.IdempotencyKey)
	}
}

func TestRESTLoopActionExecutorRetryStopsAtMaxAttempts(t *testing.T) {
	fake := &fakeRESTExecutor{execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
		return protocol.ResponseMeta{StatusCode: http.StatusBadGateway}, errors.New("upstream")
	}}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.example"},
			Executor: fake,
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-r"},
		Retry:        RetryPolicy{MaxAttempts: 2, Base: time.Millisecond, Max: time.Millisecond},
		Sleep:        func(time.Duration) {},
	}
	if _, err := executor.Register(context.Background()); err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if len(executor.Transcript) != 2 {
		t.Fatalf("expected 2 transcript entries, got %d", len(executor.Transcript))
	}
	if len(executor.Terminals) != 1 {
		t.Fatalf("expected 1 terminal record, got %d", len(executor.Terminals))
	}
	if executor.Terminals[0].Result != RESTLoopActionTerminalExhausted {
		t.Fatalf("terminal result = %q, want exhausted", executor.Terminals[0].Result)
	}
}

func TestRESTLoopActionExecutorRetryStopsImmediatelyOnPermanentError(t *testing.T) {
	calls := 0
	fake := &fakeRESTExecutor{execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
		calls++
		return protocol.ResponseMeta{StatusCode: http.StatusBadRequest}, protocol.HTTPError{StatusCode: http.StatusBadRequest, Code: "validation_failed"}
	}}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.example"},
			Executor: fake,
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-r"},
		Retry:        RetryPolicy{MaxAttempts: 4, Base: time.Millisecond, Max: time.Millisecond},
		Sleep:        func(time.Duration) { t.Fatal("permanent errors must not sleep") },
		IdempotencyKeyFn: func(action string, attempt int) string {
			return "perm-" + action
		},
	}
	outcome, err := executor.Register(context.Background())
	if err == nil {
		t.Fatal("expected permanent error")
	}
	if calls != 1 {
		t.Fatalf("expected single attempt for permanent error, got %d", calls)
	}
	if outcome.IssueCode != "validation_failed" {
		t.Fatalf("expected issue code preserved, got %q", outcome.IssueCode)
	}
	if len(executor.Transcript) != 1 {
		t.Fatalf("expected 1 transcript entry, got %d", len(executor.Transcript))
	}
	if executor.Transcript[0].RetryClass != RetryClassPermanent {
		t.Fatalf("transcript class = %q", executor.Transcript[0].RetryClass)
	}
	if len(executor.Terminals) != 1 || executor.Terminals[0].Result != RESTLoopActionTerminalPermanent {
		t.Fatalf("terminal record = %+v", executor.Terminals)
	}
	if executor.Terminals[0].IdempotencyKey != "perm-register" {
		t.Fatalf("terminal idempotency key = %q", executor.Terminals[0].IdempotencyKey)
	}
}

func TestRESTLoopActionExecutorRetryClassifiesRetryAfterStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   RetryClass
	}{
		{http.StatusOK, RetryClassNone},
		{http.StatusBadRequest, RetryClassPermanent},
		{http.StatusUnauthorized, RetryClassPermanent},
		{http.StatusRequestTimeout, RetryClassTransient},
		{http.StatusTooManyRequests, RetryClassTransient},
		{http.StatusInternalServerError, RetryClassTransient},
		{http.StatusBadGateway, RetryClassTransient},
		{http.StatusServiceUnavailable, RetryClassTransient},
	}
	for _, tc := range cases {
		var err error
		if tc.status == http.StatusOK {
			err = nil
		} else {
			err = protocol.HTTPError{StatusCode: tc.status}
		}
		got := classifyRetryError(err, tc.status)
		if got != tc.want {
			t.Fatalf("classify(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestRESTLoopActionExecutorRetryClassifiesContextErrors(t *testing.T) {
	if got := classifyRetryError(context.Canceled, 0); got != RetryClassContext {
		t.Fatalf("classify(context.Canceled) = %q", got)
	}
	if got := classifyRetryError(context.DeadlineExceeded, 0); got != RetryClassContext {
		t.Fatalf("classify(context.DeadlineExceeded) = %q", got)
	}
}

func TestRESTLoopActionExecutorTerminalRecordsTotalWait(t *testing.T) {
	attempts := 0
	fake := &fakeRESTExecutor{execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
		attempts++
		return protocol.ResponseMeta{StatusCode: http.StatusInternalServerError}, protocol.HTTPError{StatusCode: http.StatusInternalServerError}
	}}
	totalSlept := time.Duration(0)
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.example"},
			Executor: fake,
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-r"},
		Retry:        RetryPolicy{MaxAttempts: 3, Base: time.Millisecond, Max: 4 * time.Millisecond},
		Sleep:        func(d time.Duration) { totalSlept += d },
	}
	_, _ = executor.Register(context.Background())
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(executor.Terminals) != 1 {
		t.Fatalf("expected 1 terminal record, got %d", len(executor.Terminals))
	}
	if executor.Terminals[0].TotalWait != totalSlept {
		t.Fatalf("terminal total wait = %v, sleeps = %v", executor.Terminals[0].TotalWait, totalSlept)
	}
}

func TestRESTLoopActionExecutorDryRunSetsIdempotencyHeader(t *testing.T) {
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder: protocol.RequestBuilder{BaseURL: "https://control.example"},
		},
		Registration:     protocol.RegistrationRequest{AgentID: "agent-r"},
		DryRun:           true,
		IdempotencyKeyFn: func(action string, attempt int) string { return "idem-" + action },
	}
	if _, err := executor.Register(context.Background()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(executor.Intents) != 1 {
		t.Fatalf("expected one intent, got %d", len(executor.Intents))
	}
	if executor.Intents[0].IdempotencyKey != "idem-register" {
		t.Fatalf("intent idempotency key = %q", executor.Intents[0].IdempotencyKey)
	}
	if len(executor.Terminals) != 1 || executor.Terminals[0].Result != RESTLoopActionTerminalDryRun {
		t.Fatalf("terminal = %+v", executor.Terminals)
	}
}
