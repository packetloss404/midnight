// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	auth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/audit"
)

func TestAuditChainSink_WritesAgentRow(t *testing.T) {
	t.Parallel()
	chain := audit.NewMemoryChain()
	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	chain.SetClock(func() time.Time { return fixed })
	sink := auth.NewAuditChainSink(chain)
	sink.SetClock(func() time.Time { return fixed })

	row := auth.AgentAuditRow{
		Source:            auth.AgentAuditSourceChat,
		ConversationID:    "conv_42",
		Principal:         auth.Principal{Kind: auth.PrincipalKindOperator, ID: "op_1"},
		ToolName:          "list_services",
		ToolInput:         map[string]any{"project_id": "proj_a"},
		ToolResultSummary: "3 services",
		Resource: auth.AgentAuditResource{
			Kind:      auth.ResourceKindService,
			ProjectID: "proj_a",
		},
		Model:          "claude-opus-4-7",
		Usage:          auth.Usage{InputTokens: 100, OutputTokens: 50, CostCents: 12},
		RequestID:      "req_1",
		PolicyDecision: "allow",
	}
	if err := sink.Record(context.Background(), row); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]
	if got.Action != auth.AgentAuditActionToolCall {
		t.Fatalf("action = %q, want %q", got.Action, auth.AgentAuditActionToolCall)
	}
	if got.ActorID != "conv_42" {
		t.Fatalf("actor id = %q, want conv_42", got.ActorID)
	}
	if got.ProjectID != "proj_a" {
		t.Fatalf("project id = %q, want proj_a", got.ProjectID)
	}
	if got.PolicyDecision != "allow" {
		t.Fatalf("policy decision = %q", got.PolicyDecision)
	}
	if kind, _ := got.Attrs[auth.AuditAttrActorKind].(string); kind != auth.AgentAuditActorKind {
		t.Fatalf("actor_kind attr = %q, want %q", kind, auth.AgentAuditActorKind)
	}
	if tool, _ := got.Attrs[auth.AuditAttrToolName].(string); tool != "list_services" {
		t.Fatalf("tool_name attr = %q", tool)
	}
	if usage, ok := got.Attrs[auth.AuditAttrUsage].(map[string]any); !ok {
		t.Fatalf("usage attr missing or wrong type: %T", got.Attrs[auth.AuditAttrUsage])
	} else if usage["input_tokens"].(int64) != 100 {
		t.Fatalf("usage input_tokens = %v, want 100", usage["input_tokens"])
	}
}

func TestAuditChainSink_RedactsToolInput(t *testing.T) {
	t.Parallel()
	chain := audit.NewMemoryChain()
	sink := auth.NewAuditChainSink(chain)
	red := auth.NewSecretRedactor()
	red.AddPlaintexts("DB_PASS_SUPER")
	sink.SetRedactor(red)

	row := auth.AgentAuditRow{
		Source:         auth.AgentAuditSourceChat,
		ConversationID: "conv_red",
		ToolName:       "set_variable",
		ToolInput: map[string]any{
			"name":  "DATABASE_URL",
			"value": "postgres://u:DB_PASS_SUPER@host/db",
		},
		ToolResultSummary: "ok — applied DB_PASS_SUPER",
	}
	if err := sink.Record(context.Background(), row); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, _ := chain.Read(context.Background(), audit.Query{})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	input, _ := rows[0].Attrs[auth.AuditAttrToolInput].(map[string]any)
	if input == nil {
		t.Fatalf("tool_input attr missing")
	}
	value, _ := input["value"].(string)
	if strings.Contains(value, "DB_PASS_SUPER") {
		t.Fatalf("plaintext leaked into audit row: %q", value)
	}
	summary, _ := rows[0].Attrs[auth.AuditAttrToolResult].(string)
	if strings.Contains(summary, "DB_PASS_SUPER") {
		t.Fatalf("plaintext leaked into result summary: %q", summary)
	}
}

func TestAuditChainSink_MCPRowUsesSessionID(t *testing.T) {
	t.Parallel()
	chain := audit.NewMemoryChain()
	sink := auth.NewAuditChainSink(chain)
	row := auth.AgentAuditRow{
		Source:       auth.AgentAuditSourceMCP,
		MCPSessionID: "mcp_xy",
		ToolName:     "fetch_logs",
	}
	if err := sink.Record(context.Background(), row); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows, _ := chain.Read(context.Background(), audit.Query{})
	if rows[0].ActorID != "mcp_xy" {
		t.Fatalf("actor id = %q, want mcp_xy", rows[0].ActorID)
	}
}

func TestMemoryAuditSink_CapturesRows(t *testing.T) {
	t.Parallel()
	m := auth.NewMemoryAuditSink()
	row := auth.AgentAuditRow{ConversationID: "c", ToolName: "t"}
	if err := m.Record(context.Background(), row); err != nil {
		t.Fatalf("record: %v", err)
	}
	rows := m.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0].ToolName != "t" {
		t.Fatalf("captured wrong row: %+v", rows[0])
	}
}

func TestMultiAuditSink_FanOut(t *testing.T) {
	t.Parallel()
	a := auth.NewMemoryAuditSink()
	b := auth.NewMemoryAuditSink()
	multi := auth.MultiAuditSink{Sinks: []auth.AgentAuditSink{a, b}}
	if err := multi.Record(context.Background(), auth.AgentAuditRow{ConversationID: "c", ToolName: "t"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if len(a.Rows()) != 1 || len(b.Rows()) != 1 {
		t.Fatalf("fan-out failed")
	}
}

func TestNoopAuditSink_NoError(t *testing.T) {
	t.Parallel()
	if err := (auth.NoopAuditSink{}).Record(context.Background(), auth.AgentAuditRow{}); err != nil {
		t.Fatalf("noop returned %v", err)
	}
}
