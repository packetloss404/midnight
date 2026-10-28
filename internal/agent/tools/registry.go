// Package tools defines the platform tool set the Midnight agent uses to
// drive the control plane. Each tool is a small, side-effect-honest unit
// the model can call by name; mutating tools refuse to run unless
// [agent.Env].AllowMutations is set, and every invocation lands an audit
// row through [agent.Env].Audit.
//
// The package exports a single mutable [Registry] type plus one file per
// tool. The Anthropic SDK ToolParam slice is built by
// [Registry.BuildAnthropicTools] so the claude package never has to
// reach across the boundary.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"github.com/midnight/midnight/internal/agent"
)

// Tool is one named operation the agent may invoke. Implementations live
// in this package; the registry is what other tracks consume.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON Schema (Draft 2020-12) object describing the
	// Execute argument. Use json.RawMessage rather than a typed schema so
	// each tool can hand-craft the precise shape Claude sees — automatic
	// jsonschema generation hides too much.
	InputSchema json.RawMessage
	// Mutates flags tools that change state. The registry refuses to
	// execute these when [agent.Env].AllowMutations is false. The
	// claude package strips them from the model-visible tool list in
	// the same case so the model never even sees the option.
	Mutates bool
	// Execute is the handler. It MUST return a JSON-encodable value
	// (typically a struct marshalled with json.Marshal). On error the
	// registry converts the error into an is_error=true tool_result
	// containing the error message — Claude will see it.
	Execute func(ctx context.Context, input json.RawMessage, env agent.Env) (any, error)
}

// Registry holds the tools available to the agent. The zero value is
// usable; prefer [NewDefaultRegistry] to get the full Midnight tool set.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	// Now is injectable for tests that need deterministic audit
	// timestamps. Defaults to [time.Now].
	Now func() time.Time
}

// New returns an empty Registry. Most callers want [NewDefaultRegistry].
func New() *Registry {
	return &Registry{tools: map[string]Tool{}, Now: func() time.Time { return time.Now().UTC() }}
}

// NewDefaultRegistry returns a Registry pre-populated with the Midnight
// platform tool set:
//
//   - list_projects
//   - list_services
//   - get_service
//   - get_deployment
//   - get_logs
//   - set_variable      (mutating)
//   - unset_variable    (mutating)
//   - redeploy          (mutating)
//   - rollback          (mutating)
//   - deploy_from_source(mutating)
//   - get_metrics
//
// Concurrent tracks that want a different scope (e.g. the
// build-failure-diagnosis worker, which is read-only) should call
// [Registry.Filter] or pass AllowedTools through [agent.CallRequest].
func NewDefaultRegistry() *Registry {
	r := New()
	r.MustRegister(listProjectsTool())
	r.MustRegister(listServicesTool())
	r.MustRegister(getServiceTool())
	r.MustRegister(getSourceDeploymentContextTool())
	r.MustRegister(getDeploymentTool())
	r.MustRegister(getLogsTool())
	r.MustRegister(setVariableTool())
	r.MustRegister(unsetVariableTool())
	r.MustRegister(redeployTool())
	r.MustRegister(rollbackTool())
	r.MustRegister(deployFromSourceTool())
	r.MustRegister(getMetricsTool())
	return r
}

// Register adds t to the registry. Re-registering the same Name is an
// error — callers that want to override should Unregister first.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return errors.New("tools: tool name is required")
	}
	if t.Execute == nil {
		return fmt.Errorf("tools: tool %q has nil Execute", t.Name)
	}
	if len(t.InputSchema) == 0 {
		return fmt.Errorf("tools: tool %q has empty InputSchema", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = map[string]Tool{}
	}
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("tools: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister panics on error. Used by the default builders.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		// lint:allow-panic Static default-tool construction is a programmer invariant.
		panic(err)
	}
}

// Unregister removes name from the registry. Returns false if absent.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	return true
}

// Names returns the registered tool names in sorted order. Useful for
// snapshot tests and for logging the loop's tool surface.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Get returns the tool by name. The second return is false when absent.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Visible returns the tool definitions a model provider may see for this
// request. When allowed is empty, all registered tools are eligible; otherwise
// only the named tools are returned. When includeMutations is false, mutating
// tools are hidden from the model-visible list.
func (r *Registry) Visible(allowed []string, includeMutations bool) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	allow := func(name string) bool {
		if len(allowed) == 0 {
			return true
		}
		for _, a := range allowed {
			if a == name {
				return true
			}
		}
		return false
	}

	names := make([]string, 0, len(r.tools))
	for n, t := range r.tools {
		if !allow(n) {
			continue
		}
		if t.Mutates && !includeMutations {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// BuildAnthropicTools serialises the registry into the SDK's ToolUnionParam
// shape. When allowed is empty, all tools are included; otherwise only
// the named tools (preserving the caller's order). When includeMutations
// is false, mutating tools are dropped — the model never sees them.
//
// The LAST tool in the returned slice carries cache_control=ephemeral so
// the tool definitions become a cacheable prompt prefix. Combined with
// the cache_control on the system prompt, this is the cost-control
// strategy mandated by ADR-0023.
func (r *Registry) BuildAnthropicTools(allowed []string, includeMutations bool) []anthropic.ToolUnionParam {
	visible := r.Visible(allowed, includeMutations)
	out := make([]anthropic.ToolUnionParam, 0, len(visible))
	for i, t := range visible {
		var schema anthropic.ToolInputSchemaParam
		// Decode into the SDK's schema param via the ExtraFields path.
		// The SDK marshals Properties + Required + Type — we feed those
		// from our raw JSON.
		var probe struct {
			Properties map[string]any `json:"properties,omitempty"`
			Required   []string       `json:"required,omitempty"`
		}
		_ = json.Unmarshal(t.InputSchema, &probe)
		schema.Properties = probe.Properties
		schema.Required = probe.Required

		tp := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}
		// Mark the LAST tool with ephemeral cache_control so the tool
		// list becomes a cacheable prefix. Setting TTL explicitly forces
		// the param-marshaller to emit the block (a zero-valued
		// CacheControlEphemeralParam is omitted by `json:",omitzero"`).
		if i == len(visible)-1 {
			tp.CacheControl = anthropic.CacheControlEphemeralParam{
				TTL: anthropic.CacheControlEphemeralTTLTTL5m,
			}
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tp})
	}
	return out
}

// Execute runs the named tool. The registry enforces the mutation gate
// and writes an audit row via env.Audit. Tool implementations focus on
// the operation itself.
//
// The returned interface{} is whatever the tool's Execute produced (a
// struct typically); the caller is responsible for marshalling it into
// the tool_result content block.
func (r *Registry) Execute(ctx context.Context, name string, input json.RawMessage, env agent.Env) (any, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", agent.ErrToolNotFound, name)
	}
	if t.Mutates && !env.AllowMutations {
		// Audit the refusal so the operator can see what the model
		// tried to do.
		_ = r.audit(ctx, env, t.Name, input, scopeFromInput(input), "refused", agent.ErrMutationsDisabled)
		return nil, agent.ErrMutationsDisabled
	}

	result, runErr := t.Execute(ctx, input, env)

	// Pull resource hints out of the input for the audit row so
	// downstream review queries can scope by resource, not just actor.
	scope := scopeFromInput(input)

	outcome := "ok"
	if runErr != nil {
		outcome = "error"
	}
	if auditErr := r.audit(ctx, env, t.Name, input, scope, outcome, runErr); auditErr != nil {
		// Audit failure should not mask the tool's outcome — log via
		// returning a wrapped error only when the tool itself succeeded.
		// If the tool already failed, callers care about the runErr.
		if runErr == nil {
			return result, fmt.Errorf("tools: audit emit failed: %w", auditErr)
		}
	}
	return result, runErr
}

func (r *Registry) audit(ctx context.Context, env agent.Env, tool string, input json.RawMessage, scope auditScope, outcome string, runErr error) error {
	if env.Audit == nil {
		return nil
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now()
	}
	entry := agent.AuditEntry{
		Timestamp:     now,
		Actor:         env.Actor,
		Tool:          tool,
		Arguments:     input,
		ProjectID:     scope.ProjectID,
		ServiceID:     scope.ServiceID,
		DeploymentID:  scope.DeploymentID,
		EnvironmentID: scope.EnvironmentID,
		Outcome:       outcome,
	}
	if runErr != nil {
		entry.Error = runErr.Error()
	}
	return env.Audit.Record(ctx, entry)
}

// auditScope is the set of resource ids a tool call touched, as sniffed
// out of its input envelope.
type auditScope struct {
	ProjectID     string
	ServiceID     string
	DeploymentID  string
	EnvironmentID string
}

// scopeFromInput sniffs resource ids out of a tool input envelope. It
// tolerates missing fields — best-effort for audit scope.
func scopeFromInput(input json.RawMessage) auditScope {
	if len(input) == 0 {
		return auditScope{}
	}
	var probe struct {
		ProjectID     string `json:"project_id"`
		ServiceID     string `json:"service_id"`
		DeploymentID  string `json:"deployment_id"`
		EnvironmentID string `json:"environment_id"`
	}
	_ = json.Unmarshal(input, &probe)
	return auditScope{
		ProjectID:     probe.ProjectID,
		ServiceID:     probe.ServiceID,
		DeploymentID:  probe.DeploymentID,
		EnvironmentID: probe.EnvironmentID,
	}
}

// MarshalToolResult turns a tool's Execute return value into the JSON
// payload the loop will pass back to Claude. Errors collapse into a
// minimal string-typed result so the model sees something it can read.
func MarshalToolResult(value any, runErr error) (json.RawMessage, bool) {
	if runErr != nil {
		// is_error=true; serialize the message as a JSON string so
		// Claude's parser receives a clean payload.
		b, _ := json.Marshal(map[string]string{"error": runErr.Error()})
		return b, true
	}
	if value == nil {
		return json.RawMessage(`{"ok":true}`), false
	}
	b, err := json.Marshal(value)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"error": "tool result not JSON-encodable: " + err.Error()})
		return b, true
	}
	return b, false
}
