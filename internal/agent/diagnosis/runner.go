// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package diagnosis

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/redaction"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// EnvEnabled gates whether the runner does any work. Off by default so
// the test suite and developer hosts never trip an outbound agent call;
// production callers set this to "1" / "true" once the upstream
// Anthropic credentials are in place.
const EnvEnabled = "MIDNIGHT_AGENT_DIAGNOSIS_ENABLED"

// EnvModel overrides the model name. Defaults to DefaultModel.
const EnvModel = "MIDNIGHT_AGENT_DIAGNOSIS_MODEL"

// EnvRate overrides the per-host token-bucket rate as "<count>/<duration>"
// (e.g., "10/5m"). Malformed values fall back to the defaults.
const EnvRate = "MIDNIGHT_AGENT_DIAGNOSIS_RATE"

// DefaultModel is the model used for diagnosis when no override is set.
const DefaultModel = "claude-opus-5"

// DefaultTimeout caps every diagnosis run end-to-end (Caller + Conversation
// persistence + metadata write). The runner spawns a goroutine with this
// deadline so the underlying FSM thread is never blocked.
const DefaultTimeout = 2 * time.Minute

// MetadataFieldDiagnosisID names the optional embedding metadata pointer.
// Installed workers use the durable EventNameDiagnosisAttached event instead;
// the canonical operations table has no metadata column.
const MetadataFieldDiagnosisID = "diagnosis_id"

// EventNameDiagnosisAttached is the operation-event message tag the
// runner appends once it has persisted a diagnosis. The dashboard's
// timeline maps this string into the "💡 Diagnosis available" pill.
const EventNameDiagnosisAttached = "deploy:diagnosis_attached"

// kinds the runner reacts to. Other kinds (backup, restore, etc.) are
// ignored.
var diagnosableKinds = map[string]struct{}{
	"deploy": {},
	"build":  {},
}

// CallRequest mirrors the shape the core agent track exposes. The
// runner only sets a tiny subset of fields, and we duplicate the type
// here so this package builds standalone if the core agent package is
// not yet present in the worktree. When agent.CallRequest lands the two
// types are field-compatible.
type CallRequest struct {
	Model          string
	System         string
	Messages       []Message
	AllowMutations bool
	Metadata       map[string]string
}

// Message is one entry in CallRequest.Messages.
type Message struct {
	Role    string
	Content string
}

// CallResult mirrors agent.CallResult.
type CallResult struct {
	Text       string
	StopReason string
}

// StreamEvent mirrors agent.StreamEvent. The diagnosis runner only uses
// Send (non-streaming) so this type is here for interface compatibility.
type StreamEvent struct {
	Type string
	Text string
}

// Caller is the local mirror of agent.Caller. Declared here so the
// diagnosis package builds standalone; when internal/agent/agent.go
// lands, the runner can be switched to agent.Caller via an alias or
// directly without changing any call sites.
type Caller interface {
	Send(ctx context.Context, req CallRequest) (CallResult, error)
	Stream(ctx context.Context, req CallRequest, out chan<- StreamEvent) error
}

// Conversation is the local mirror of conversation.Conversation, the
// minimal shape needed to thread a diagnosis through the chat-backend's
// store. Only ID is read by the runner.
type Conversation struct {
	ID string
}

// ConversationStore is the local mirror of the chat-backend track's
// store seam. Defined here so the diagnosis runner does not couple to
// a package that may not exist yet in this worktree; production wiring
// adapts the chat-backend's store to this interface.
type ConversationStore interface {
	StartConversation(ctx context.Context, title string, tags map[string]string) (Conversation, error)
	AppendMessage(ctx context.Context, conversationID, role, content string) error
}

// LogTailer fetches a tail of log lines for the failed deployment. The
// implementation is owned by the API track (api.LogQueryStore); the
// runner depends on this narrow seam so the package's test surface
// stays small.
type LogTailer interface {
	BuildLogTail(ctx context.Context, deploymentID string, maxLines int) ([]string, error)
	RuntimeLogTail(ctx context.Context, deploymentID string, maxLines int) ([]string, error)
}

// ServiceLookup resolves a deployment to its service + slug + previous
// succeeded deployment for the diagnosis user message. Implementations
// MUST be cheap (these are metadata reads); the runner never blocks the
// FSM on them.
type ServiceLookup interface {
	Resolve(ctx context.Context, op orailtypes.Operation) (ServiceContext, error)
}

// ServiceContext is the lookup result fed into the diagnosis user
// message. All fields are optional — empty strings just suppress the
// corresponding section in the prompt.
type ServiceContext struct {
	WorkspaceID    string
	ProjectID      string
	EnvironmentID  string
	ServiceID      string
	ServiceSlug    string
	DeploymentID   string
	PreviousDeploy string
	Variables      []string
	SpecDigest     string
}

// MetadataWriter is an optional attachment hook for embedders. Installed
// workers use the durable operation event instead of a metadata column.
type MetadataWriter interface {
	SetDiagnosisID(ctx context.Context, operationID orailtypes.ID, conversationID string) error
}

// EventAppender appends an operation event recording that the diagnosis
// has been attached. The runner uses this rather than calling the store
// directly so production callers can wrap it with the same
// best-effort/log-and-continue policy used elsewhere.
type EventAppender interface {
	AppendEvent(ctx context.Context, event api.OperationEvent) (api.OperationEvent, error)
}

// Runner is the package's only public side-effecting type. It
// implements api.OperationLifecycleObserver and is the single hook
// point for build-failure diagnosis.
//
// Construction is via NewRunner; consumers register the runner against
// an api.ObservedOperationStore. The runner is safe for concurrent use.
type Runner struct {
	Caller        Caller
	Conversations ConversationStore
	Logs          LogTailer
	Services      ServiceLookup
	Metadata      MetadataWriter
	Events        EventAppender
	Logger        *slog.Logger

	enabled  bool
	model    string
	timeout  time.Duration
	bucket   *TokenBucket
	breaker  *CircuitBreaker
	clock    func() time.Time
	asyncRun func(func())

	dedupeMu sync.Mutex
	dedupe   map[orailtypes.ID]struct{}
}

// Options configures a Runner. All fields are optional; zero values
// fall back to env-driven defaults so production wiring can write a
// single line.
type Options struct {
	// Caller is the model seam. Without it OnFailed is inert — the
	// runner has nothing to ask. Production wiring builds this with
	// NewAgentCaller.
	Caller Caller
	// Conversations is where the finished diagnosis is persisted.
	// Without it OnFailed is inert: producing a diagnosis nobody can
	// read is worse than not producing one, because it spends money and
	// leaves no trace.
	Conversations ConversationStore
	// Logs and Services enrich the prompt. A configured service lookup must
	// succeed before the model is called, so production never saves an
	// unscoped diagnosis when canonical tenant lineage cannot be resolved.
	// Metadata is an optional embedding hook; installed workers attach the
	// completed report through Events.
	Logs     LogTailer
	Services ServiceLookup
	Metadata MetadataWriter
	Events   EventAppender
	// Enabled forces the runner on even when the env toggle is unset.
	// Used by the test suite to drive happy-path coverage without
	// touching process state.
	Enabled bool
	// Model overrides the model name returned by EffectiveModel.
	Model string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
	// RateLimit / RateWindow override the token-bucket defaults; both
	// must be non-zero for the override to take effect.
	RateLimit  int
	RateWindow time.Duration
	// CircuitThreshold / CircuitCooldown override the breaker defaults.
	CircuitThreshold int
	CircuitCooldown  time.Duration
	// Clock is the runner's clock; defaults to time.Now.
	Clock func() time.Time
	// AsyncRun is the goroutine launcher; defaults to `go fn()`. Tests
	// pass a synchronous runner to make assertions deterministic.
	AsyncRun func(func())
	// Logger is the structured logger; defaults to slog.Default.
	Logger *slog.Logger
}

// NewRunner constructs a runner. Caller and Conversations are required
// for the runner to do any work — if either is nil, OnFailed becomes a
// no-op (logged at debug level) so production hosts can stage the
// wiring incrementally without breaking the observer fan-out.
func NewRunner(opts Options) *Runner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	async := opts.AsyncRun
	if async == nil {
		async = func(fn func()) { go fn() }
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = strings.TrimSpace(os.Getenv(EnvModel))
	}
	if model == "" {
		model = DefaultModel
	}
	enabled := opts.Enabled || envBool(EnvEnabled)
	limit, window := resolveRate(opts.RateLimit, opts.RateWindow)
	bucket := NewTokenBucket(limit, window)
	bucket.SetClock(clock)
	breaker := NewCircuitBreaker(opts.CircuitThreshold, opts.CircuitCooldown)
	breaker.SetClock(clock)
	return &Runner{
		Caller:        opts.Caller,
		Conversations: opts.Conversations,
		Logs:          opts.Logs,
		Services:      opts.Services,
		Metadata:      opts.Metadata,
		Events:        opts.Events,
		enabled:       enabled,
		model:         model,
		timeout:       timeout,
		bucket:        bucket,
		breaker:       breaker,
		clock:         clock,
		asyncRun:      async,
		Logger:        logger,
		dedupe:        make(map[orailtypes.ID]struct{}),
	}
}

// EffectiveDiagnosisModel resolves the diagnosis model the same way
// NewRunner does, for callers that need the value before they have a
// Runner (notably the worker, which stamps it on the conversation).
func EffectiveDiagnosisModel() string {
	if model := strings.TrimSpace(os.Getenv(EnvModel)); model != "" {
		return model
	}
	return DefaultModel
}

// Enabled reports whether the runner will attempt diagnosis. Tests use
// this to assert the env-toggle semantics without invoking OnFailed.
func (r *Runner) Enabled() bool {
	if r == nil {
		return false
	}
	return r.enabled
}

// EffectiveModel returns the model the runner will send in CallRequest.
// Surfaced for the dashboard "what model produced this" affordance and
// for the runner's structured log entries.
func (r *Runner) EffectiveModel() string {
	if r == nil {
		return ""
	}
	return r.model
}

// OnFailed implements api.OperationLifecycleObserver. The method returns
// promptly and dispatches the real diagnosis work to a goroutine; the
// goroutine owns its own timeout context and never re-enters the FSM.
func (r *Runner) OnFailed(ctx context.Context, op orailtypes.Operation, events []api.OperationEvent) {
	if r == nil || !r.enabled {
		return
	}
	if op.State != orailtypes.OperationFailed {
		return
	}
	if _, ok := diagnosableKinds[strings.TrimSpace(op.Kind)]; !ok {
		return
	}
	for _, event := range events {
		if strings.HasPrefix(event.Message, EventNameDiagnosisAttached+" diagnosis_id=") &&
			strings.TrimSpace(strings.TrimPrefix(event.Message, EventNameDiagnosisAttached+" diagnosis_id=")) != "" {
			return // A completed, durably attached report survives worker restart.
		}
	}
	if !r.claim(op.ID) {
		r.Logger.Debug("diagnosis dedupe skip", "operation_id", op.ID)
		return
	}
	if !r.bucket.Allow() {
		r.release(op.ID)
		r.Logger.Warn("diagnosis rate limit exhausted", "operation_id", op.ID)
		return
	}
	if !r.breaker.Allow() {
		r.release(op.ID)
		r.Logger.Warn("diagnosis circuit breaker open", "operation_id", op.ID)
		return
	}
	if r.Caller == nil || r.Conversations == nil {
		r.release(op.ID)
		r.Logger.Debug("diagnosis runner not fully wired; skipping",
			"operation_id", op.ID,
			"have_caller", r.Caller != nil,
			"have_conversations", r.Conversations != nil,
		)
		return
	}
	// Snapshot events so the async run does not race the caller's
	// slice. Cap is a defensive bound on the structured-log size; the
	// prompt-render layer applies its own tail cap.
	snapshot := append([]api.OperationEvent(nil), events...)
	r.asyncRun(func() {
		runCtx, cancel := context.WithTimeout(detachContext(ctx), r.timeout)
		defer cancel()
		r.run(runCtx, op, snapshot)
	})
}

// run executes one diagnosis end-to-end. All errors are logged and
// swallowed so the FSM is never observable to the failure of an
// outbound agent call.
func (r *Runner) run(ctx context.Context, op orailtypes.Operation, events []api.OperationEvent) {
	defer func() {
		if rec := recover(); rec != nil {
			r.Logger.Error("diagnosis panic recovered",
				"operation_id", op.ID,
				"panic", rec,
			)
			r.breaker.Failure()
		}
	}()
	failureCtx := FromOperation(op, events)
	if r.Services != nil {
		if resolved, err := r.Services.Resolve(ctx, op); err != nil {
			r.Logger.Debug("diagnosis service resolve failed",
				"operation_id", op.ID,
				"err", err,
			)
			return
		} else {
			failureCtx.WorkspaceID = resolved.WorkspaceID
			failureCtx.ProjectID = resolved.ProjectID
			failureCtx.EnvironmentID = resolved.EnvironmentID
			failureCtx.ServiceID = resolved.ServiceID
			failureCtx.ServiceSlug = resolved.ServiceSlug
			failureCtx.DeploymentID = resolved.DeploymentID
			failureCtx.PreviousDeploy = resolved.PreviousDeploy
			failureCtx.Variables = resolved.Variables
			failureCtx.SpecDigest = resolved.SpecDigest
		}
	}
	if r.Logs != nil {
		deploymentID := failureCtx.DeploymentID
		if buildLog, err := r.Logs.BuildLogTail(ctx, deploymentID, MaxBuildLogLines); err == nil {
			failureCtx.BuildLogTail = TailLines(buildLog, MaxBuildLogLines)
		} else {
			r.Logger.Debug("diagnosis build log tail failed",
				"operation_id", op.ID,
				"err", err,
			)
		}
		if runtimeLog, err := r.Logs.RuntimeLogTail(ctx, deploymentID, MaxRuntimeLogLines); err == nil {
			failureCtx.RuntimeLogTail = TailLines(runtimeLog, MaxRuntimeLogLines)
		} else {
			r.Logger.Debug("diagnosis runtime log tail failed",
				"operation_id", op.ID,
				"err", err,
			)
		}
	}
	req := CallRequest{
		Model:          r.model,
		System:         SystemPreamble,
		AllowMutations: false,
		Messages: []Message{
			{Role: "user", Content: redaction.DefaultRedactor().RedactString((build.Redactor{}).RedactText(RenderUserMessage(failureCtx)))},
		},
		Metadata: map[string]string{
			"midnight.kind":         "diagnosis",
			"midnight.operation":    string(op.ID),
			"midnight.failure_code": string(op.Code),
		},
	}
	result, err := r.Caller.Send(ctx, req)
	if err != nil {
		r.breaker.Failure()
		failures, openedAt := r.breaker.State()
		r.Logger.Warn("diagnosis caller error",
			"operation_id", op.ID,
			"err", err,
			"breaker_failures", failures,
			"breaker_opened_at", openedAt,
		)
		return
	}
	if strings.TrimSpace(result.Text) == "" {
		r.breaker.Failure()
		r.Logger.Warn("diagnosis caller returned empty body", "operation_id", op.ID)
		return
	}
	r.breaker.Success()
	title := ConversationTitle(failureCtx.ServiceSlug, string(op.ID))
	tags := map[string]string{
		"midnight.kind":         "diagnosis",
		"midnight.operation_id": string(op.ID),
	}
	for key, value := range map[string]string{
		"midnight.workspace_id":   failureCtx.WorkspaceID,
		"midnight.project_id":     failureCtx.ProjectID,
		"midnight.environment_id": failureCtx.EnvironmentID,
	} {
		if value != "" {
			tags[key] = value
		}
	}
	if failureCtx.DeploymentID != "" {
		tags["midnight.deployment_id"] = failureCtx.DeploymentID
	}
	if failureCtx.ServiceID != "" {
		tags["midnight.service_id"] = failureCtx.ServiceID
	}
	conv, err := r.Conversations.StartConversation(ctx, title, tags)
	if err != nil {
		r.Logger.Warn("diagnosis conversation start failed",
			"operation_id", op.ID,
			"err", err,
		)
		return
	}
	if err := r.Conversations.AppendMessage(ctx, conv.ID, "assistant", result.Text); err != nil {
		r.Logger.Warn("diagnosis append message failed",
			"operation_id", op.ID,
			"conversation_id", conv.ID,
			"err", err,
		)
		return
	}
	if r.Metadata != nil {
		if err := r.Metadata.SetDiagnosisID(ctx, op.ID, conv.ID); err != nil {
			r.Logger.Warn("diagnosis metadata write failed",
				"operation_id", op.ID,
				"conversation_id", conv.ID,
				"err", err,
			)
		}
	}
	if r.Events != nil {
		_, err := r.Events.AppendEvent(ctx, api.OperationEvent{
			OperationID: op.ID,
			State:       orailtypes.OperationFailed,
			Message:     fmt.Sprintf("%s diagnosis_id=%s", EventNameDiagnosisAttached, conv.ID),
		})
		if err != nil {
			r.Logger.Debug("diagnosis event append failed",
				"operation_id", op.ID,
				"err", err,
			)
			return
		}
	}
	r.Logger.Info("diagnosis attached",
		"operation_id", op.ID,
		"conversation_id", conv.ID,
		"model", r.model,
	)
}

// claim returns false when the operation has already been diagnosed by
// this runner. The map covers in-flight work in this process. OnFailed also
// checks the persisted attachment event supplied by the observed store on
// replay. Simultaneous workers before the first attachment are not globally
// serialized by this guard.
func (r *Runner) claim(id orailtypes.ID) bool {
	r.dedupeMu.Lock()
	defer r.dedupeMu.Unlock()
	if _, ok := r.dedupe[id]; ok {
		return false
	}
	r.dedupe[id] = struct{}{}
	return true
}

// release is the inverse of claim. Used when the runner decides not to
// proceed (rate limit, circuit open, missing wiring) so a later retry
// — after the rate window resets — still gets one chance.
func (r *Runner) release(id orailtypes.ID) {
	r.dedupeMu.Lock()
	defer r.dedupeMu.Unlock()
	delete(r.dedupe, id)
}

// envBool returns true when the named env var is set to a truthy
// value (1, true, yes, on). Empty / missing returns false.
func envBool(name string) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// resolveRate consolidates the (Options.RateLimit/RateWindow) override
// path with the EnvRate string ("<count>/<duration>"). Malformed env
// values fall back silently to the defaults so a bad config never
// breaks production startup.
func resolveRate(optsLimit int, optsWindow time.Duration) (int, time.Duration) {
	if optsLimit > 0 && optsWindow > 0 {
		return optsLimit, optsWindow
	}
	raw := strings.TrimSpace(os.Getenv(EnvRate))
	if raw == "" {
		return DefaultRateLimit, DefaultRateWindow
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 {
		return DefaultRateLimit, DefaultRateWindow
	}
	count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || count <= 0 {
		return DefaultRateLimit, DefaultRateWindow
	}
	window, err := time.ParseDuration(strings.TrimSpace(parts[1]))
	if err != nil || window <= 0 {
		return DefaultRateLimit, DefaultRateWindow
	}
	return count, window
}

// detachContext returns a context that inherits values from parent but
// drops its cancellation. The runner uses this so a cancelled FSM
// request context does not abort an in-flight diagnosis run.
func detachContext(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return detachedContext{parent: parent}
}

type detachedContext struct {
	parent context.Context
}

func (c detachedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c detachedContext) Done() <-chan struct{}       { return nil }
func (c detachedContext) Err() error                  { return nil }
func (c detachedContext) Value(key any) any {
	if c.parent == nil {
		return nil
	}
	return c.parent.Value(key)
}

// Ensure compile-time conformance with the observer contract from the
// api package.
var _ api.OperationLifecycleObserver = (*Runner)(nil)
