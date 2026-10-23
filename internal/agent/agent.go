// Package agent defines the public seam through which all Midnight tracks
// (chat backend, build-failure diagnosis, MCP server, future autonomous
// surfaces) consume the in-product AI agent.
//
// The package exposes:
//
//   - [Caller]: the one-way door for invoking a model with Midnight tools.
//     Concurrent tracks should depend on this interface, never on the
//     concrete provider SDK implementation.
//   - [CallRequest] / [CallResult] / [StreamEvent]: the transport-neutral
//     shapes that flow across the seam.
//   - [Env]: the bag of platform capabilities a tool needs to execute.
//   - [MetricsEmitter]: the cost-telemetry hook every call emits to.
//   - Typed error sentinels ([AgentAPIKeyMissing] etc.) so callers can
//     fail closed without string-matching.
//
// Provider wiring lives under internal/agent provider packages such as
// [github.com/midnight/midnight/internal/agent/claude] and
// [github.com/midnight/midnight/internal/agent/openai]. The platform tool
// set lives in [github.com/midnight/midnight/internal/agent/tools]. Both
// packages produce a [Caller] / [Tool] for the rest of the codebase, but
// no track outside `internal/agent/...` is permitted to import the SDK
// directly — that is the boundary this package defends.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// MessageRole enumerates the conversational roles supported on the seam.
// "system" never appears in [CallRequest.Conversation]; system text is
// supplied via [CallRequest.SystemPreamble] so the platform system prompt
// is always prepended and always cacheable.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
)

// ContentBlockKind enumerates the variants of [ContentBlock].
type ContentBlockKind string

const (
	BlockText       ContentBlockKind = "text"
	BlockToolUse    ContentBlockKind = "tool_use"
	BlockToolResult ContentBlockKind = "tool_result"
)

// ContentBlock is the transport-neutral content payload. Exactly one of
// Text / ToolUse / ToolResult is populated, gated by Kind. Callers that
// want a string-only message can use [TextMessage].
type ContentBlock struct {
	Kind       ContentBlockKind `json:"kind"`
	Text       string           `json:"text,omitempty"`
	ToolUse    *ToolCall        `json:"tool_use,omitempty"`
	ToolResult *ToolResult      `json:"tool_result,omitempty"`
}

// Message is one conversational turn in a [CallRequest].
type Message struct {
	Role    MessageRole    `json:"role"`
	Content []ContentBlock `json:"content"`
	// ID is an opaque identifier the caller may set for correlation with
	// its own persistence layer (e.g. conversation row id). The agent
	// core does not interpret it.
	ID string `json:"id,omitempty"`
}

// TextMessage is a convenience constructor for a single-text message.
func TextMessage(role MessageRole, text string) Message {
	return Message{
		Role:    role,
		Content: []ContentBlock{{Kind: BlockText, Text: text}},
	}
}

// ToolCall is the model's request to invoke a named tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is the executed-tool payload returned to the model.
type ToolResult struct {
	ToolUseID string          `json:"tool_use_id"`
	Output    json.RawMessage `json:"output"`
	IsError   bool            `json:"is_error,omitempty"`
}

// CallRequest is the input to [Caller.Send] / [Caller.Stream].
type CallRequest struct {
	// Conversation is the user/assistant turn history, oldest first.
	// The last message MUST be from the user (or be a tool_result-bearing
	// user message); the agent core does not auto-insert turns.
	Conversation []Message
	// SystemPreamble is op-supplied context appended after the platform
	// system prompt (and cached together with it). Keep this stable
	// across many turns to get cache hits — append-only changes are
	// preferred.
	SystemPreamble string
	// Model overrides the default chat model when non-empty.
	Model string
	// MaxTokens caps the generation. 0 == default ([DefaultMaxTokens]).
	MaxTokens int
	// AllowedTools filters the registry by name. Empty/nil means "all
	// registered tools". This is the surface the build-failure diagnosis
	// track uses to scope its read-only loop.
	AllowedTools []string
	// MaxIterations caps the tool loop. 0 == default ([DefaultMaxIterations]).
	MaxIterations int
	// Deadline caps wall-clock time across the whole loop (including
	// tool execution). 0 == default ([DefaultDeadline]). The context
	// passed to Send/Stream always wins if it expires sooner.
	Deadline time.Duration
	// Thinking selects extended-thinking behaviour for this call.
	// [ThinkingDefault] (the zero value) defers to the provider's
	// operator-configured default.
	Thinking ThinkingMode
	// Effort is the model's thinking-depth / token-spend knob. It maps
	// onto the Anthropic `output_config.effort` field.
	// [EffortDefault] (the zero value) defers to the provider's
	// operator-configured default.
	Effort Effort
	// Env carries the platform handles tools need. Required.
	Env Env
}

// ThinkingMode selects extended-thinking behaviour on the seam. The
// provider layer is responsible for translating this into whatever the
// upstream API expects (and for dropping it entirely on models that do
// not accept it).
type ThinkingMode string

const (
	// ThinkingDefault defers to the provider's configured default.
	ThinkingDefault ThinkingMode = ""
	// ThinkingAdaptive lets the model decide when and how deeply to
	// think. This is the recommended mode on current models.
	ThinkingAdaptive ThinkingMode = "adaptive"
	// ThinkingOff disables extended thinking.
	ThinkingOff ThinkingMode = "off"
)

// Effort is the thinking-depth / token-spend knob. Values mirror the
// Anthropic `output_config.effort` enum.
type Effort string

const (
	// EffortDefault defers to the provider's configured default.
	EffortDefault Effort = ""
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

// Valid reports whether e is a recognized effort level (or the
// "defer to the default" zero value).
func (e Effort) Valid() bool {
	switch e {
	case EffortDefault, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	default:
		return false
	}
}

// Valid reports whether m is a recognized thinking mode (or the
// "defer to the default" zero value).
func (m ThinkingMode) Valid() bool {
	switch m {
	case ThinkingDefault, ThinkingAdaptive, ThinkingOff:
		return true
	default:
		return false
	}
}

// Defaults pinned by ADR-0023, revised once real token streaming landed.
//
// DefaultMaxTokens is the non-streaming ceiling: it is deliberately well
// under the SDK's 10-minute HTTP timeout, because a non-streaming call
// that runs long enough to hit that timeout is unrecoverable.
// DefaultStreamMaxTokens is the streaming ceiling — streaming has no
// such timeout pressure, so the model gets room to finish a thought
// rather than being truncated mid-sentence and needing a retry.
const (
	DefaultMaxTokens       = 16_000
	DefaultStreamMaxTokens = 64_000
	// MaxTokensCeiling is the hard cap the provider layer clamps to.
	// Current Claude models top out at 128K output tokens.
	MaxTokensCeiling     = 128_000
	DefaultMaxIterations = 10
	// DefaultDeadline bounds the whole loop. Adaptive thinking plus a
	// multi-step tool loop routinely outruns a one-minute budget, and a
	// deadline that fires mid-answer is indistinguishable to the user
	// from a broken agent.
	DefaultDeadline = 5 * time.Minute
)

// CallResult is the return value of [Caller.Send].
type CallResult struct {
	// Messages contains every turn produced during the loop, in order
	// (assistant tool_use, user tool_result, assistant text, etc.). The
	// caller's pre-existing Conversation is NOT echoed back — only the
	// new turns.
	Messages []Message
	// FinalText is the concatenation of text blocks in the terminal
	// assistant turn, for callers that only want the answer.
	FinalText string
	// StopReason is the reason the loop terminated. One of "end_turn",
	// "max_tokens", "max_iterations", "deadline_exceeded", "tool_error".
	StopReason string
	// Iterations is the number of model calls performed (>= 1).
	Iterations int
	// Usage aggregates token + cache + cost telemetry across every
	// model call in the loop.
	Usage Usage
}

// StreamEventKind enumerates the wire-level events emitted by [Caller.Stream].
type StreamEventKind string

const (
	// EventTextDelta is a text chunk from the assistant.
	EventTextDelta StreamEventKind = "text_delta"
	// EventToolCall fires when the model emits a tool_use block; Delta
	// is empty and ToolCall is populated.
	EventToolCall StreamEventKind = "tool_call"
	// EventToolResult fires immediately after a tool executes; ToolResult
	// is populated.
	EventToolResult StreamEventKind = "tool_result"
	// EventIterationEnd fires when one model turn completes; Usage is
	// populated with that turn's tokens.
	EventIterationEnd StreamEventKind = "iteration_end"
	// EventDone fires once when the whole loop terminates; Usage holds
	// the aggregated counters.
	EventDone StreamEventKind = "done"
	// EventError fires when the loop aborts. Delta carries the message.
	EventError StreamEventKind = "error"
)

// StreamEvent is one frame on the streaming channel. Exactly one of the
// Delta / ToolCall / ToolResult / Usage fields is meaningful, gated by
// Kind.
type StreamEvent struct {
	Kind       StreamEventKind `json:"kind"`
	Delta      string          `json:"delta,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	ToolResult *ToolResult     `json:"tool_result,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
}

// Usage is the cost-telemetry shape emitted on every iteration and once
// per [Caller.Send] via [MetricsEmitter].
type Usage struct {
	Model               string  `json:"model"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CostUSDEstimated    float64 `json:"cost_usd_estimated"`
	IterationCount      int     `json:"iteration_count,omitempty"`
}

// Add accumulates other into u in place. Model is preserved from u if
// already set; otherwise other's value wins.
func (u *Usage) Add(other Usage) {
	if u.Model == "" {
		u.Model = other.Model
	}
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CostUSDEstimated += other.CostUSDEstimated
	u.IterationCount += other.IterationCount
}

// Caller is the seam every Midnight track consumes. Implementations MUST
// be safe for concurrent use by multiple goroutines.
//
// Send is the non-streaming entrypoint; it blocks until the tool loop
// terminates (or the context expires / the deadline fires).
//
// Stream is the streaming entrypoint; it emits frames into out as they
// arrive and closes out before returning. The error mirrors the
// terminating condition (nil on clean completion, the context error on
// cancel, [AgentAPIKeyMissing] on missing key, etc.).
type Caller interface {
	Send(ctx context.Context, req CallRequest) (CallResult, error)
	Stream(ctx context.Context, req CallRequest, out chan<- StreamEvent) error
}

// MetricsEmitter receives one Usage event per [Caller.Send] (covering
// every iteration aggregated together). Implementations MUST NOT block
// and MUST be safe for concurrent use. The default emitter in the
// claude package writes to a stdlib logger.
type MetricsEmitter interface {
	EmitUsage(ctx context.Context, u Usage)
}

// MetricsEmitterFunc is the function-shaped adapter for [MetricsEmitter].
type MetricsEmitterFunc func(ctx context.Context, u Usage)

// EmitUsage implements [MetricsEmitter].
func (f MetricsEmitterFunc) EmitUsage(ctx context.Context, u Usage) { f(ctx, u) }

// AuditSink is the audit-log hook tools call before mutating state.
// Implementations should be transactional with the underlying mutation
// when possible, but the seam intentionally accepts a fire-and-forget
// implementation for read-only tools and for tests.
type AuditSink interface {
	Record(ctx context.Context, entry AuditEntry) error
}

// AuditEntry is one row written by a tool invocation.
type AuditEntry struct {
	Timestamp time.Time       `json:"ts"`
	Actor     string          `json:"actor"` // e.g. "agent:chat" / "agent:diagnosis"
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	ProjectID string          `json:"project_id,omitempty"`
	ServiceID string          `json:"service_id,omitempty"`
	// DeploymentID scopes the row to a deployment when the tool named
	// one. Together with ProjectID / ServiceID this is what makes the
	// audit chain queryable by resource rather than only by actor.
	DeploymentID string `json:"deployment_id,omitempty"`
	// EnvironmentID scopes the row to an environment when the tool
	// named one (the variable tools always do).
	EnvironmentID string `json:"environment_id,omitempty"`
	Outcome       string `json:"outcome"` // "ok" or "error"
	Error         string `json:"error,omitempty"`
}

// AuditSinkFunc is the function-shaped adapter for [AuditSink].
type AuditSinkFunc func(ctx context.Context, entry AuditEntry) error

// Record implements [AuditSink].
func (f AuditSinkFunc) Record(ctx context.Context, entry AuditEntry) error { return f(ctx, entry) }

// Env is the bag of platform capabilities a tool needs to execute. The
// fields are interfaces so tests can supply in-memory fakes. Every field
// is mandatory unless explicitly documented otherwise — tools assert on
// their nil-ness rather than the agent core.
//
// AllowMutations gates write-side tools (set_variable, redeploy, etc.).
// Read-only loops (the build-failure-diagnosis track) leave this false
// and refuse to register the mutating tools.
//
// Actor is the audit-log actor string for this Env, e.g. "agent:chat"
// scoped by user_id. The tools layer never derives this — the caller
// (chat backend, diagnosis worker) is responsible for setting it.
type Env struct {
	Services        ServicesAPI
	Operations      OperationStore
	Builds          BuildIntake
	Metrics         MetricsStore // may be nil; metrics tool returns a stub
	Audit           AuditSink
	ProjectScope    string // optional; when set, list/scope tools auto-filter
	Actor           string
	AllowMutations  bool
	AllowBuiltinVar bool // honors --allow-builtin-override semantics for MIDNIGHT_ prefixed vars
}

// ServicesAPI is the read-side projection of Midnight's projects/services
// state. The control-plane track supplies the concrete implementation;
// the agent core depends only on these methods.
type ServicesAPI interface {
	ListProjects(ctx context.Context) ([]Project, error)
	ListServices(ctx context.Context, projectID string) ([]Service, error)
	GetService(ctx context.Context, serviceID string) (Service, error)
	SetVariable(ctx context.Context, req VariableWrite) error
	UnsetVariable(ctx context.Context, req VariableWrite) error
}

// VariableWrite is the input to the variable mutation tools. Variables
// are scoped to a (service, environment) pair in the control plane, so
// the environment travels with the write; implementations resolve an
// empty EnvironmentID when the service has exactly one environment and
// refuse the write when the choice is ambiguous.
//
// Value is write-only. No implementation may echo it back — not in a
// result, not in an error, not in an audit row.
type VariableWrite struct {
	ServiceID     string
	EnvironmentID string
	Name          string
	// Value is unused by UnsetVariable.
	Value string
}

// OperationStore exposes deployment + log queries.
type OperationStore interface {
	GetDeployment(ctx context.Context, deploymentID string) (Deployment, error)
	GetLogs(ctx context.Context, deploymentID string, lines int, search string) ([]LogLine, error)
	Redeploy(ctx context.Context, serviceID string) (Deployment, error)
	Rollback(ctx context.Context, deploymentID string) (Deployment, error)
}

// BuildIntake is the entrypoint for kicking off a new build from source.
type BuildIntake interface {
	DeployFromSource(ctx context.Context, serviceID, repo, ref string) (Deployment, error)
}

// MetricsStore is the (optional) metrics projection. Implementations
// that don't have metrics wired yet can return [ErrMetricsUnavailable].
type MetricsStore interface {
	GetServiceMetrics(ctx context.Context, serviceID string) (ServiceMetrics, error)
}

// Project is the agent-core projection of a Midnight project.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
}

// Service is the agent-core projection of a Midnight service.
type Service struct {
	ID           string            `json:"id"`
	ProjectID    string            `json:"project_id"`
	Name         string            `json:"name"`
	Variables    map[string]string `json:"variables,omitempty"` // metadata only — values may be redacted by the impl
	Deployments  []Deployment      `json:"deployments,omitempty"`
	GitHubRepo   string            `json:"github_repo,omitempty"`
	GitHubRef    string            `json:"github_ref,omitempty"`
	LatestStatus string            `json:"latest_status,omitempty"`
	CreatedAt    time.Time         `json:"created_at,omitempty"`
}

// Deployment is the agent-core projection of a deployment.
type Deployment struct {
	ID         string    `json:"id"`
	ServiceID  string    `json:"service_id"`
	Status     string    `json:"status"`
	Route      string    `json:"route,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	LogsHead   []LogLine `json:"logs_head,omitempty"`
}

// LogLine is one structured log entry.
type LogLine struct {
	Timestamp time.Time `json:"ts"`
	Stream    string    `json:"stream,omitempty"` // "stdout" | "stderr" | "build"
	Message   string    `json:"message"`
}

// ServiceMetrics is a minimal metrics projection.
type ServiceMetrics struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryBytes   int64   `json:"memory_bytes"`
	RestartCount  int64   `json:"restart_count"`
	WindowSeconds int64   `json:"window_seconds"`
}

// Typed errors. Callers fail closed on these — do NOT string-match.
var (
	// AgentAPIKeyMissing is returned when the selected provider key is unset
	// and no explicit key was supplied to the constructor. The agent NEVER
	// silently degrades.
	AgentAPIKeyMissing = errors.New("agent: ANTHROPIC_API_KEY not set")
	// ErrToolNotFound is returned when the model asks for a tool that
	// isn't registered (or isn't in AllowedTools).
	ErrToolNotFound = errors.New("agent: tool not found")
	// ErrMutationsDisabled is returned by mutating tools when
	// Env.AllowMutations is false.
	ErrMutationsDisabled = errors.New("agent: mutations disabled in this context")
	// ErrMaxIterations is returned when the tool loop hits the iteration
	// cap before the model emits stop_reason=end_turn.
	ErrMaxIterations = errors.New("agent: tool loop exceeded max iterations")
	// ErrMetricsUnavailable is returned by MetricsStore implementations
	// that don't have a backing store wired yet. The get_metrics tool
	// translates this into a friendly string for the model.
	ErrMetricsUnavailable = errors.New("agent: metrics store not wired")
)
