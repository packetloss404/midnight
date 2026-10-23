// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/audit"
)

// AgentAuditActorKind is the canonical audit_logs.actor_type value
// for agent-originated rows. NOTE: the audit_logs check constraint
// added in migration 0001 enumerates ('user','service_account',
// 'system','mcp_client') only. Until a follow-up migration relaxes
// the constraint, the AuditChainSink writes "system" for the
// actor_type column and stores the canonical agent actor kind in
// the row's Attrs map under "actor_kind" + actor_id under
// "agent_actor_id". This preserves filterability without violating
// the constraint. The track contract calls this out as part of the
// coordination note ("actor_kind = \"agent\"") — see ADR-0027.
const (
	AgentAuditActorKind   = "agent"
	AuditAttrActorKind    = "actor_kind"
	AuditAttrAgentActor   = "agent_actor_id"
	AuditAttrToolName     = "tool_name"
	AuditAttrToolInput    = "tool_input_redacted"
	AuditAttrToolResult   = "tool_result_summary"
	AuditAttrToolError    = "tool_error"
	AuditAttrModel        = "model"
	AuditAttrUsage        = "usage"
	AuditAttrConversation = "conversation_id"
	AuditAttrMCPSession   = "mcp_session_id"
	AuditAttrPrincipal    = "principal"
	AuditAttrSourceKind   = "source_kind"
)

// AgentAuditRow is the agent-specific audit payload the chat-
// backend and the MCP server emit on every tool call (or every
// fatal failure). The Sink wraps it into an audit.Row so the
// existing tamper-evident chain stays the single source of truth.
type AgentAuditRow struct {
	// OccurredAt is the wall-clock time the tool call ran.
	OccurredAt time.Time

	// Source identifies the wire surface: "chat" (in-product) or
	// "mcp" (external MCP client). Used to filter audit queries
	// without inspecting attrs.
	Source string

	// ConversationID is the chat conversation id (in-product
	// chat). Empty for MCP-only rows.
	ConversationID string
	// MCPSessionID is the MCP session id. Empty for in-product
	// chat.
	MCPSessionID string

	// Principal is the resolved calling identity.
	Principal Principal

	// ToolName is the canonical name of the tool the agent
	// invoked (e.g. "list_services", "redeploy_service").
	ToolName string
	// ToolInput is the redacted input payload the agent passed
	// to the tool. Plaintext secret values MUST be removed via
	// the Redactor before this is populated.
	ToolInput map[string]any
	// ToolResultSummary is a redaction-safe summary of the tool's
	// result. Free-form; bounded to ~512 bytes by convention.
	ToolResultSummary string
	// ToolError, when non-empty, replaces ToolResultSummary in
	// the surfaced row.
	ToolError string

	// Action is the canonical action string. When empty, the sink
	// defaults to "agent_tool_call".
	Action string

	// Resource describes the target of the tool call.
	Resource AgentAuditResource

	// Model is the canonical Claude model id used for the turn.
	Model string
	// Usage is the per-turn token + cost roll-up.
	Usage Usage

	// RequestID propagates the request scope through the audit
	// chain so per-request joins still work.
	RequestID string

	// PolicyDecision carries any short tag the authorizer
	// emitted ("allow", "deny:budget_exhausted", etc).
	PolicyDecision string
}

// AgentAuditResource is the target of a tool call.
type AgentAuditResource struct {
	Kind          string
	ID            string
	WorkspaceID   string
	ProjectID     string
	EnvironmentID string
}

// AgentAuditSink is the audit emit seam every tool call writes
// through. Implementations MUST NOT mutate the supplied row.
type AgentAuditSink interface {
	Record(ctx context.Context, row AgentAuditRow) error
}

// AgentAuditSourceChat is the canonical Source value for in-
// product chat-originated rows.
const (
	AgentAuditSourceChat = "chat"
	AgentAuditSourceMCP  = "mcp"
)

// AgentAuditActionToolCall is the default Action value.
const AgentAuditActionToolCall = "agent_tool_call"

// AuditChainSink writes AgentAuditRow into an existing
// audit.AuditChain. Plaintext secret redaction MUST happen before
// the row reaches the sink (see Redactor); the sink itself does
// not look at the tool input beyond marshaling.
type AuditChainSink struct {
	Chain    audit.AuditChain
	Now      func() time.Time
	Redactor *SecretRedactor // optional final-pass redactor over string fields
}

// NewAuditChainSink wires the sink with a wall clock.
func NewAuditChainSink(chain audit.AuditChain) *AuditChainSink {
	return &AuditChainSink{
		Chain: chain,
		Now:   func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the wall clock used to stamp recorded_at.
func (s *AuditChainSink) SetClock(fn func() time.Time) {
	if s == nil || fn == nil {
		return
	}
	s.Now = fn
}

// SetRedactor optionally installs a final-pass redactor that
// rewrites string fields (ToolInput values, ToolResultSummary,
// ToolError) before they enter the chain. The chat-backend and
// MCP server should still redact before calling the sink — this
// is belt-and-braces.
func (s *AuditChainSink) SetRedactor(r *SecretRedactor) {
	if s == nil {
		return
	}
	s.Redactor = r
}

// Record implements AgentAuditSink.
func (s *AuditChainSink) Record(ctx context.Context, row AgentAuditRow) error {
	if s == nil || s.Chain == nil {
		return errors.New("agent audit sink: chain not initialized")
	}
	now := s.Now()
	occurred := row.OccurredAt
	if occurred.IsZero() {
		occurred = now
	}
	action := strings.TrimSpace(row.Action)
	if action == "" {
		action = AgentAuditActionToolCall
	}

	attrs := map[string]any{
		AuditAttrActorKind:    AgentAuditActorKind,
		AuditAttrAgentActor:   actorIDForRow(row),
		AuditAttrToolName:     row.ToolName,
		AuditAttrSourceKind:   row.Source,
		AuditAttrConversation: row.ConversationID,
		AuditAttrMCPSession:   row.MCPSessionID,
		AuditAttrModel:        row.Model,
	}
	if row.ToolError != "" {
		attrs[AuditAttrToolError] = redactString(s.Redactor, row.ToolError)
	} else if row.ToolResultSummary != "" {
		attrs[AuditAttrToolResult] = redactString(s.Redactor, row.ToolResultSummary)
	}
	if len(row.ToolInput) > 0 {
		attrs[AuditAttrToolInput] = redactMap(s.Redactor, row.ToolInput)
	}
	if row.Principal.Subject != "" || row.Principal.ID != "" {
		attrs[AuditAttrPrincipal] = map[string]any{
			"kind":    string(row.Principal.Kind),
			"id":      row.Principal.ID,
			"subject": row.Principal.Subject,
		}
	}
	if row.Usage.TotalTokens() > 0 || row.Usage.CostCents > 0 {
		attrs[AuditAttrUsage] = map[string]any{
			"input_tokens":          row.Usage.InputTokens,
			"output_tokens":         row.Usage.OutputTokens,
			"cache_creation_tokens": row.Usage.CacheCreationTokens,
			"cache_read_tokens":     row.Usage.CacheReadTokens,
			"cost_cents":            row.Usage.CostCents,
		}
	}

	// strip empty fields so the JSONB column stays clean
	for k, v := range attrs {
		if s, ok := v.(string); ok && s == "" {
			delete(attrs, k)
		}
	}

	resourceKind := row.Resource.Kind
	if resourceKind == "" {
		resourceKind = "agent_tool"
	}

	auditRow := audit.Row{
		RequestID:      row.RequestID,
		OccurredAt:     occurred,
		RecordedAt:     now,
		ActorID:        actorIDForRow(row),
		ActorType:      "system", // see AgentAuditActorKind doc on constraint
		AuthMethod:     "agent",
		WorkspaceID:    row.Resource.WorkspaceID,
		ProjectID:      row.Resource.ProjectID,
		EnvironmentID:  row.Resource.EnvironmentID,
		ResourceKind:   resourceKind,
		ResourceID:     row.Resource.ID,
		Action:         action,
		PolicyDecision: row.PolicyDecision,
		Attrs:          attrs,
	}
	if _, err := s.Chain.Append(ctx, auditRow); err != nil {
		return fmt.Errorf("agent audit sink: append: %w", err)
	}
	return nil
}

// actorIDForRow chooses the canonical actor id: the conversation
// id for in-product chat, the mcp session id for MCP rows.
func actorIDForRow(row AgentAuditRow) string {
	if id := strings.TrimSpace(row.ConversationID); id != "" {
		return id
	}
	if id := strings.TrimSpace(row.MCPSessionID); id != "" {
		return id
	}
	return strings.TrimSpace(row.Principal.ID)
}

// MultiAuditSink fans out a single Record to multiple sinks. Used
// by tests that want an in-memory observer + the real chain.
type MultiAuditSink struct {
	Sinks []AgentAuditSink
}

// Record fan-outs to every sink. The first error wins; remaining
// sinks still receive the row.
func (m MultiAuditSink) Record(ctx context.Context, row AgentAuditRow) error {
	var firstErr error
	for _, s := range m.Sinks {
		if s == nil {
			continue
		}
		if err := s.Record(ctx, row); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// NoopAuditSink discards every row. Used by Phase 0 tests that
// don't care about audit.
type NoopAuditSink struct{}

// Record returns nil.
func (NoopAuditSink) Record(ctx context.Context, _ AgentAuditRow) error { return nil }

// MemoryAuditSink retains rows in memory. Useful for assertions.
type MemoryAuditSink struct {
	rows []AgentAuditRow
}

// NewMemoryAuditSink constructs an empty in-memory sink.
func NewMemoryAuditSink() *MemoryAuditSink { return &MemoryAuditSink{} }

// Record appends the row.
func (m *MemoryAuditSink) Record(ctx context.Context, row AgentAuditRow) error {
	if m == nil {
		return errors.New("memory audit sink: nil")
	}
	m.rows = append(m.rows, row)
	return nil
}

// Rows returns a copy of every recorded row.
func (m *MemoryAuditSink) Rows() []AgentAuditRow {
	if m == nil {
		return nil
	}
	out := make([]AgentAuditRow, len(m.rows))
	copy(out, m.rows)
	return out
}
