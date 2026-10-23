package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

// IdempotencyKeyHeader is the canonical header the executor sets so the
// control plane can de-duplicate retries of the same logical request.
const IdempotencyKeyHeader = "Idempotency-Key"

// RetryClass classifies a control-plane error for the retry policy. The
// class drives whether the executor sleeps and retries or breaks out and
// surfaces the error immediately.
type RetryClass string

const (
	// RetryClassTransient covers errors the policy expects to recover from
	// without operator intervention. Includes 5xx, 408, 429, generic
	// transport faults.
	RetryClassTransient RetryClass = "transient"
	// RetryClassPermanent covers 4xx-shaped errors that mean "do not retry
	// this request". Includes 4xx (other than 408/429), validation errors,
	// auth/idempotency rejections.
	RetryClassPermanent RetryClass = "permanent"
	// RetryClassContext covers context cancellation or deadline exceeded.
	// The executor stops immediately; the transcript records the attempt.
	RetryClassContext RetryClass = "context"
	// RetryClassNone is the placeholder for successful attempts so
	// transcript readers can filter on a single field.
	RetryClassNone RetryClass = ""
)

// RESTLoopActionTerminalResult enumerates the deterministic outcomes of a
// retry loop. It is the single field reviewers consult when deciding
// whether the action succeeded, exhausted retries, or hit a permanent
// error.
type RESTLoopActionTerminalResult string

const (
	// RESTLoopActionTerminalOK indicates the action returned success
	// before exhausting retries.
	RESTLoopActionTerminalOK RESTLoopActionTerminalResult = "ok"
	// RESTLoopActionTerminalExhausted indicates retries were exhausted on
	// transient errors.
	RESTLoopActionTerminalExhausted RESTLoopActionTerminalResult = "exhausted"
	// RESTLoopActionTerminalPermanent indicates the action ended on a
	// permanent error and was not retried further.
	RESTLoopActionTerminalPermanent RESTLoopActionTerminalResult = "permanent"
	// RESTLoopActionTerminalCanceled indicates the context was canceled
	// during execution.
	RESTLoopActionTerminalCanceled RESTLoopActionTerminalResult = "canceled"
	// RESTLoopActionTerminalDryRun indicates a dry-run pass; no real
	// request was sent.
	RESTLoopActionTerminalDryRun RESTLoopActionTerminalResult = "dry_run"
)

// RESTLoopActionIntent records the HTTP request a dry-run loop action would send.
// It intentionally stores request metadata only, not bodies or credentials.
type RESTLoopActionIntent struct {
	Action         string `json:"action"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	AgentID        string `json:"agent_id,omitempty"`
	ETag           string `json:"etag,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	Attempt        int    `json:"attempt,omitempty"`
}

// RESTLoopActionTranscriptEntry records one outcome from the retry loop.
// Transcripts feed into evidence packets so reviewers can replay decisions.
type RESTLoopActionTranscriptEntry struct {
	Action         string        `json:"action"`
	Attempt        int           `json:"attempt"`
	Status         string        `json:"status"`
	IssueCode      string        `json:"issue_code,omitempty"`
	StatusCode     int           `json:"status_code,omitempty"`
	Wait           time.Duration `json:"wait,omitempty"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	RetryClass     RetryClass    `json:"retry_class,omitempty"`
}

// RESTLoopActionTerminal records the deterministic terminal outcome of one
// logical loop action invocation, including total attempts, total wait,
// the issue code (if any), and the idempotency key actually used.
type RESTLoopActionTerminal struct {
	Action         string                       `json:"action"`
	Result         RESTLoopActionTerminalResult `json:"result"`
	Attempts       int                          `json:"attempts"`
	TotalWait      time.Duration                `json:"total_wait,omitempty"`
	StatusCode     int                          `json:"status_code,omitempty"`
	IssueCode      string                       `json:"issue_code,omitempty"`
	RetryClass     RetryClass                   `json:"retry_class,omitempty"`
	IdempotencyKey string                       `json:"idempotency_key,omitempty"`
}

// RetryClassPolicy carries the per-class backoff parameters the executor
// uses. A permanent-class action ignores all timings and breaks out
// immediately on the first attempt. A non-positive class entry collapses
// to safe defaults.
type RetryClassPolicy struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
}

// RetryPolicy describes the structured retry/backoff used by the executor
// when control-plane calls fail. The policy is pure data; callers may
// construct their own to bound retries per loop pass. Missing classes
// inherit defaults from the top-level fields.
type RetryPolicy struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration

	Transient RetryClassPolicy
	Permanent RetryClassPolicy
}

// RESTLoopActionExecutor adapts supervisor loop actions to the REST protocol client.
// When DryRun is true it uses the request builder and records intents without
// invoking the protocol executor, so no network I/O occurs.
type RESTLoopActionExecutor struct {
	Client protocol.AgentClient

	Registration     protocol.RegistrationRequest
	HeartbeatRequest protocol.HeartbeatRequest
	DesiredAgentID   string
	DesiredETag      string

	// IdempotencyKeyFn returns the idempotency key the executor MUST reuse
	// across retries of the same logical action. Implementations should
	// return a stable key that uniquely identifies the request payload.
	IdempotencyKeyFn func(action string, attempt int) string

	// Retry controls structured retry/backoff per logical action.
	Retry RetryPolicy

	// Sleep is injected so tests do not block on real time.
	Sleep func(time.Duration)

	DryRun     bool
	Intents    []RESTLoopActionIntent
	Transcript []RESTLoopActionTranscriptEntry
	Terminals  []RESTLoopActionTerminal
}

// Register sends or records the agent registration request.
func (e *RESTLoopActionExecutor) Register(ctx context.Context) (LoopInjectedOutcome, error) {
	if err := ctx.Err(); err != nil {
		return LoopInjectedOutcome{}, err
	}
	if e == nil {
		return LoopInjectedOutcome{}, fmt.Errorf("rest loop action executor is required")
	}
	if e.DryRun {
		return e.runDryRun(LoopActionRegister, func(attempt int) (*http.Request, error) {
			req, err := e.Client.Builder.NewRegistrationRequest(e.Registration)
			return e.attachIdempotency(LoopActionRegister, attempt, req, err)
		})
	}
	return e.runWithRetry(ctx, LoopActionRegister, func(int) (LoopInjectedOutcome, error, int) {
		resp, meta, err := e.Client.Register(e.Registration)
		if err != nil {
			return failedRESTOutcome(err), err, meta.StatusCode
		}
		return LoopInjectedOutcome{Message: fmt.Sprintf("registered agent %s with status %d", strings.TrimSpace(resp.AgentID), meta.StatusCode)}, nil, meta.StatusCode
	})
}

// Heartbeat sends or records the agent heartbeat request.
func (e *RESTLoopActionExecutor) Heartbeat(ctx context.Context) (LoopInjectedOutcome, error) {
	if err := ctx.Err(); err != nil {
		return LoopInjectedOutcome{}, err
	}
	if e == nil {
		return LoopInjectedOutcome{}, fmt.Errorf("rest loop action executor is required")
	}
	if e.DryRun {
		return e.runDryRun(LoopActionHeartbeat, func(attempt int) (*http.Request, error) {
			req, err := e.Client.Builder.NewHeartbeatRequest(e.HeartbeatRequest)
			return e.attachIdempotency(LoopActionHeartbeat, attempt, req, err)
		})
	}
	return e.runWithRetry(ctx, LoopActionHeartbeat, func(int) (LoopInjectedOutcome, error, int) {
		resp, meta, err := e.Client.Heartbeat(e.HeartbeatRequest)
		if err != nil {
			return failedRESTOutcome(err), err, meta.StatusCode
		}
		return LoopInjectedOutcome{Message: fmt.Sprintf("heartbeat accepted sequence %d with status %d", resp.AcceptedSequence, meta.StatusCode)}, nil, meta.StatusCode
	})
}

// FetchDesired sends or records the desired-state polling request.
func (e *RESTLoopActionExecutor) FetchDesired(ctx context.Context) (LoopInjectedOutcome, error) {
	if err := ctx.Err(); err != nil {
		return LoopInjectedOutcome{}, err
	}
	if e == nil {
		return LoopInjectedOutcome{}, fmt.Errorf("rest loop action executor is required")
	}
	if e.DryRun {
		return e.runDryRun(LoopActionFetchDesired, func(attempt int) (*http.Request, error) {
			req, err := e.Client.Builder.NewDesiredStateRequest(e.DesiredAgentID, e.DesiredETag)
			return e.attachIdempotency(LoopActionFetchDesired, attempt, req, err)
		})
	}
	return e.runWithRetry(ctx, LoopActionFetchDesired, func(int) (LoopInjectedOutcome, error, int) {
		resp, err := e.Client.FetchDesired(e.DesiredAgentID, e.DesiredETag)
		if err != nil {
			return failedRESTOutcome(err), err, 0
		}
		if resp.NotModified {
			return LoopInjectedOutcome{Message: fmt.Sprintf("desired state not modified with etag %s", strings.TrimSpace(resp.ETag))}, nil, http.StatusNotModified
		}
		return LoopInjectedOutcome{Message: fmt.Sprintf("fetched desired state %d bytes with etag %s", len(resp.Raw), strings.TrimSpace(resp.ETag))}, nil, http.StatusOK
	})
}

func (e *RESTLoopActionExecutor) runDryRun(action string, build func(int) (*http.Request, error)) (LoopInjectedOutcome, error) {
	req, err := build(1)
	if err != nil {
		e.appendTranscript(action, 1, "build_error", "", 0, 0, RetryClassPermanent)
		e.recordTerminal(action, RESTLoopActionTerminalPermanent, 1, 0, 0, "", RetryClassPermanent, "")
		return LoopInjectedOutcome{}, err
	}
	intent := RESTLoopActionIntent{
		Action:         action,
		Method:         req.Method,
		Path:           req.URL.EscapedPath(),
		AgentID:        strings.TrimSpace(req.Header.Get(protocol.HeaderAgentID)),
		ETag:           strings.TrimSpace(req.Header.Get(protocol.HeaderETag)),
		IdempotencyKey: strings.TrimSpace(req.Header.Get(IdempotencyKeyHeader)),
		Attempt:        1,
	}
	e.Intents = append(e.Intents, intent)
	e.appendTranscript(action, 1, "dry_run", "", 0, 0, RetryClassNone)
	e.recordTerminal(action, RESTLoopActionTerminalDryRun, 1, 0, 0, "", RetryClassNone, intent.IdempotencyKey)
	return LoopInjectedOutcome{Message: fmt.Sprintf("dry-run recorded %s %s", req.Method, req.URL.EscapedPath())}, nil
}

func (e *RESTLoopActionExecutor) runWithRetry(ctx context.Context, action string, attempt func(int) (LoopInjectedOutcome, error, int)) (LoopInjectedOutcome, error) {
	transient := normalizeRetryClassPolicy(e.Retry.Transient, e.Retry, transientDefaults())
	permanent := normalizeRetryClassPolicy(e.Retry.Permanent, e.Retry, permanentDefaults())
	maxAttempts := transient.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastOutcome LoopInjectedOutcome
	var lastErr error
	totalWait := time.Duration(0)
	for i := 1; i <= maxAttempts; i++ {
		key := e.idempotencyKey(action, i)
		if err := ctx.Err(); err != nil {
			e.appendTranscript(action, i, "context_canceled", "", 0, 0, RetryClassContext)
			e.recordTerminal(action, RESTLoopActionTerminalCanceled, i, totalWait, 0, "", RetryClassContext, key)
			return LoopInjectedOutcome{}, err
		}
		outcome, err, status := attempt(i)
		if err == nil {
			outcome.Completed = true
			e.appendTranscript(action, i, "ok", "", status, 0, RetryClassNone)
			outcome.Message = annotateMessage(outcome.Message, key)
			e.recordTerminal(action, RESTLoopActionTerminalOK, i, totalWait, status, "", RetryClassNone, key)
			return outcome, nil
		}
		class := classifyRetryError(err, status)
		if class == RetryClassPermanent {
			e.appendTranscript(action, i, "error", outcome.IssueCode, status, 0, class)
			e.recordTerminal(action, RESTLoopActionTerminalPermanent, i, totalWait, status, outcome.IssueCode, class, key)
			return outcome, err
		}
		wait := time.Duration(0)
		if i < maxAttempts {
			switch class {
			case RetryClassTransient:
				wait = retryBackoff(i, transient)
			default:
				wait = retryBackoff(i, permanent)
			}
		}
		e.appendTranscript(action, i, "error", outcome.IssueCode, status, wait, class)
		lastOutcome = outcome
		lastErr = err
		if wait <= 0 {
			break
		}
		totalWait += wait
		if e.Sleep != nil {
			e.Sleep(wait)
		}
	}
	terminalKey := e.idempotencyKey(action, maxAttempts)
	terminalStatus := terminalStatusCode(lastErr)
	e.recordTerminal(action, RESTLoopActionTerminalExhausted, maxAttempts, totalWait, terminalStatus, lastOutcome.IssueCode, RetryClassTransient, terminalKey)
	return lastOutcome, lastErr
}

func (e *RESTLoopActionExecutor) attachIdempotency(action string, attempt int, req *http.Request, err error) (*http.Request, error) {
	if err != nil {
		return nil, err
	}
	key := e.idempotencyKey(action, attempt)
	if key != "" && req != nil {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	return req, nil
}

func (e *RESTLoopActionExecutor) idempotencyKey(action string, attempt int) string {
	if e.IdempotencyKeyFn == nil {
		return ""
	}
	_ = attempt
	return e.IdempotencyKeyFn(action, attempt)
}

func (e *RESTLoopActionExecutor) appendTranscript(action string, attempt int, status, issueCode string, statusCode int, wait time.Duration, class RetryClass) {
	e.Transcript = append(e.Transcript, RESTLoopActionTranscriptEntry{
		Action:         action,
		Attempt:        attempt,
		Status:         status,
		IssueCode:      issueCode,
		StatusCode:     statusCode,
		Wait:           wait,
		IdempotencyKey: e.idempotencyKey(action, attempt),
		RetryClass:     class,
	})
}

func (e *RESTLoopActionExecutor) recordTerminal(action string, result RESTLoopActionTerminalResult, attempts int, totalWait time.Duration, statusCode int, issueCode string, class RetryClass, key string) {
	e.Terminals = append(e.Terminals, RESTLoopActionTerminal{
		Action:         action,
		Result:         result,
		Attempts:       attempts,
		TotalWait:      totalWait,
		StatusCode:     statusCode,
		IssueCode:      issueCode,
		RetryClass:     class,
		IdempotencyKey: key,
	})
}

func annotateMessage(message, key string) string {
	if key == "" {
		return message
	}
	return strings.TrimSpace(message)
}

func transientDefaults() RetryClassPolicy {
	return RetryClassPolicy{MaxAttempts: 3, Base: 100 * time.Millisecond, Max: 5 * time.Second}
}

func permanentDefaults() RetryClassPolicy {
	return RetryClassPolicy{MaxAttempts: 1, Base: 0, Max: 0}
}

func normalizeRetryClassPolicy(class RetryClassPolicy, top RetryPolicy, fallback RetryClassPolicy) RetryClassPolicy {
	policy := class
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = top.MaxAttempts
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = fallback.MaxAttempts
	}
	if policy.Base <= 0 {
		policy.Base = top.Base
	}
	if policy.Base <= 0 {
		policy.Base = fallback.Base
	}
	if policy.Max <= 0 {
		policy.Max = top.Max
	}
	if policy.Max <= 0 {
		policy.Max = fallback.Max
	}
	if policy.Max > 0 && policy.Max < policy.Base {
		policy.Max = policy.Base
	}
	return policy
}

func retryBackoff(attempt int, policy RetryClassPolicy) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if policy.Base <= 0 {
		return 0
	}
	delay := policy.Base
	for i := 1; i < attempt; i++ {
		if policy.Max > 0 && delay >= policy.Max/2 {
			return policy.Max
		}
		delay *= 2
	}
	if policy.Max > 0 && delay > policy.Max {
		return policy.Max
	}
	return delay
}

// classifyRetryError maps an error and HTTP status code into a RetryClass.
// 5xx, 408, 429, and any non-HTTPError fault are transient. 4xx (other than
// 408/429) and explicit context cancellation are permanent / context.
func classifyRetryError(err error, status int) RetryClass {
	if err == nil {
		return RetryClassNone
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RetryClassContext
	}
	var httpErr protocol.HTTPError
	if errors.As(err, &httpErr) {
		status = httpErr.StatusCode
	}
	switch {
	case status >= 500:
		return RetryClassTransient
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return RetryClassTransient
	case status >= 400 && status < 500:
		return RetryClassPermanent
	default:
		return RetryClassTransient
	}
}

func terminalStatusCode(err error) int {
	var httpErr protocol.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}

func failedRESTOutcome(err error) LoopInjectedOutcome {
	outcome := LoopInjectedOutcome{}
	var httpErr protocol.HTTPError
	if errors.As(err, &httpErr) && strings.TrimSpace(httpErr.Code) != "" {
		outcome.IssueCode = strings.TrimSpace(httpErr.Code)
	}
	return outcome
}
