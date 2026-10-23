// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth

import (
	"context"
	"os"
	"strings"
)

// EnvAllowMutations is the operator-level kill switch for every
// mutating tool the agent can call. Default true (mutations allowed
// at the operator level so the per-message flag still controls).
const EnvAllowMutations = "MIDNIGHT_AGENT_ALLOW_MUTATIONS"

// SessionContext is the per-turn context passed to MutationGate and
// SessionBudget. The chat-backend and MCP packages own the
// concrete `SessionID` / `ConversationID` strings; this struct is
// the redaction-safe view.
type SessionContext struct {
	// ConversationID is the chat conversation id (the in-product
	// chat surface). Empty for MCP-only sessions.
	ConversationID string
	// MCPSessionID is the MCP session id for external clients.
	// Empty for in-product chat.
	MCPSessionID string
	// Principal is the resolved calling identity.
	Principal Principal
	// AllowMutations is the per-message flag the user toggled in
	// the chat UI (or the MCP client supplied). false for a
	// read-only turn.
	AllowMutations bool
	// HostID is the host the conversation is running on. Used by
	// SessionBudget to enforce the per-host daily cap.
	HostID string
	// ToolName is set when the gate is being checked at tool-
	// dispatch time (the gate is also evaluated at turn-start
	// time, when ToolName is empty).
	ToolName string
}

// SessionKey returns a stable identifier suitable for budget /
// revoker tables. It prefers the conversation id and falls back to
// the MCP session id.
func (s SessionContext) SessionKey() string {
	if id := strings.TrimSpace(s.ConversationID); id != "" {
		return id
	}
	return strings.TrimSpace(s.MCPSessionID)
}

// MutationGate is the operator+session double-brake on mutating
// tool calls. The chat-backend service consumes it before
// forwarding allow_mutations to the agent core; the MCP server
// consumes it before exposing mutating tools.
type MutationGate interface {
	// AllowMutations reports whether the supplied session may
	// execute a mutating tool right now. The check is
	// instantaneous — no I/O — so the caller can re-evaluate on
	// every tool dispatch.
	AllowMutations(ctx context.Context, session SessionContext) bool
}

// EnvMutationGate is the operator-level brake. It reads
// MIDNIGHT_AGENT_ALLOW_MUTATIONS once at construction time;
// callers that need hot-reload swap the gate in place.
type EnvMutationGate struct {
	allowed bool
}

// NewEnvMutationGate reads the env var. Default (unset / empty) is
// true. Recognized "off" values: "0", "false", "no", "off".
func NewEnvMutationGate() *EnvMutationGate {
	return &EnvMutationGate{allowed: parseEnvBool(os.Getenv(EnvAllowMutations), true)}
}

// NewEnvMutationGateFromValue constructs a gate from an explicit
// value (used in tests + the bootstrap path so the env read is at
// a single seam).
func NewEnvMutationGateFromValue(v string) *EnvMutationGate {
	return &EnvMutationGate{allowed: parseEnvBool(v, true)}
}

// AllowMutations returns the operator-level decision.
func (g *EnvMutationGate) AllowMutations(ctx context.Context, session SessionContext) bool {
	if g == nil {
		return false
	}
	return g.allowed
}

// SessionMutationGate is the per-message brake. It accepts the
// session if and only if the SessionContext.AllowMutations flag is
// set. The chat-backend forwards the user's per-message toggle
// here.
type SessionMutationGate struct{}

// NewSessionMutationGate constructs the per-message brake.
func NewSessionMutationGate() *SessionMutationGate { return &SessionMutationGate{} }

// AllowMutations consults SessionContext.AllowMutations.
func (SessionMutationGate) AllowMutations(ctx context.Context, session SessionContext) bool {
	return session.AllowMutations
}

// CompositeGate composes an Operator gate with a Session gate.
// Both must say yes for a mutation to proceed. The naming follows
// the track contract: CompositeGate{Operator, Session}.
type CompositeGate struct {
	Operator MutationGate
	Session  MutationGate
}

// AllowMutations returns true when both inner gates accept the
// session.
func (c CompositeGate) AllowMutations(ctx context.Context, session SessionContext) bool {
	if c.Operator != nil && !c.Operator.AllowMutations(ctx, session) {
		return false
	}
	if c.Session != nil && !c.Session.AllowMutations(ctx, session) {
		return false
	}
	return true
}

// DefaultMutationGate returns the production composite gate.
func DefaultMutationGate() MutationGate {
	return CompositeGate{
		Operator: NewEnvMutationGate(),
		Session:  NewSessionMutationGate(),
	}
}

func parseEnvBool(v string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return fallback
	case "0", "false", "no", "off", "disable", "disabled":
		return false
	case "1", "true", "yes", "on", "enable", "enabled":
		return true
	default:
		return fallback
	}
}
