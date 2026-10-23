// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package diagnosis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
)

// recordingAgentCaller captures the agent.CallRequest the adapter built.
type recordingAgentCaller struct {
	last   agent.CallRequest
	result agent.CallResult
	err    error
}

func (c *recordingAgentCaller) Send(_ context.Context, req agent.CallRequest) (agent.CallResult, error) {
	c.last = req
	return c.result, c.err
}

func (c *recordingAgentCaller) Stream(_ context.Context, req agent.CallRequest, out chan<- agent.StreamEvent) error {
	c.last = req
	close(out)
	return c.err
}

// TestAgentCallerTranslatesEveryMismatchedField is the regression guard
// for the drift this adapter exists to fix. The diagnosis package's
// local CallRequest was documented as "field-compatible" with
// agent.CallRequest and was not: System vs SystemPreamble, flat string
// content vs content blocks, Text vs FinalText. Each mismatch fails
// silently — a dropped system prompt makes the answer worse, not
// broken — so each one is asserted here by name.
func TestAgentCallerTranslatesEveryMismatchedField(t *testing.T) {
	inner := &recordingAgentCaller{result: agent.CallResult{FinalText: "  the port is wrong  ", StopReason: "end_turn"}}
	caller, err := NewAgentCaller(inner, AgentCallerOptions{})
	if err != nil {
		t.Fatalf("NewAgentCaller: %v", err)
	}

	got, err := caller.Send(context.Background(), CallRequest{
		Model:    "claude-opus-5",
		System:   SystemPreamble,
		Messages: []Message{{Role: "user", Content: "build failed"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// System -> SystemPreamble.
	if inner.last.SystemPreamble != SystemPreamble {
		t.Errorf("SystemPreamble = %q, want the diagnosis preamble", inner.last.SystemPreamble)
	}
	// Messages[]{Role,Content string} -> Conversation[]{Role,[]ContentBlock}.
	if len(inner.last.Conversation) != 1 {
		t.Fatalf("Conversation has %d messages, want 1", len(inner.last.Conversation))
	}
	turn := inner.last.Conversation[0]
	if turn.Role != agent.RoleUser {
		t.Errorf("role = %q, want user", turn.Role)
	}
	if len(turn.Content) != 1 || turn.Content[0].Kind != agent.BlockText || turn.Content[0].Text != "build failed" {
		t.Errorf("content blocks = %+v, want one text block carrying the message", turn.Content)
	}
	if inner.last.Model != "claude-opus-5" {
		t.Errorf("model = %q", inner.last.Model)
	}
	// FinalText -> Text, trimmed.
	if got.Text != "the port is wrong" {
		t.Errorf("Text = %q, want the model's final text", got.Text)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q", got.StopReason)
	}
}

// TestAgentCallerPinsMutationsOff proves the read-only guarantee is
// structural rather than a convention the caller has to remember.
func TestAgentCallerPinsMutationsOff(t *testing.T) {
	inner := &recordingAgentCaller{result: agent.CallResult{FinalText: "ok"}}
	caller, err := NewAgentCaller(inner, AgentCallerOptions{
		// Deliberately hostile input: mutations requested, and a
		// mutating tool named in the allow-list.
		Env:          agent.Env{AllowMutations: true, Actor: ""},
		AllowedTools: append(append([]string{}, ReadOnlyDiagnosisTools...), "set_variable"),
	})
	if err != nil {
		t.Fatalf("NewAgentCaller: %v", err)
	}
	if _, err := caller.Send(context.Background(), CallRequest{
		System:   SystemPreamble,
		Messages: []Message{{Role: "user", Content: "why"}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if inner.last.Env.AllowMutations {
		t.Error("diagnosis must never run with mutations enabled")
	}
	if inner.last.Env.Actor != "agent:diagnosis" {
		t.Errorf("actor = %q, want agent:diagnosis so audit rows are attributable", inner.last.Env.Actor)
	}
}

func TestAgentCallerRequiresInner(t *testing.T) {
	if _, err := NewAgentCaller(nil, AgentCallerOptions{}); !errors.Is(err, ErrCallerRequired) {
		t.Fatalf("NewAgentCaller(nil) = %v, want ErrCallerRequired", err)
	}
}

func TestAgentCallerRejectsEmptyConversation(t *testing.T) {
	caller, err := NewAgentCaller(&recordingAgentCaller{}, AgentCallerOptions{})
	if err != nil {
		t.Fatalf("NewAgentCaller: %v", err)
	}
	if _, err := caller.Send(context.Background(), CallRequest{System: SystemPreamble}); err == nil {
		t.Fatal("expected an error for a request with no messages")
	}
}

func TestAgentCallerPropagatesError(t *testing.T) {
	sentinel := errors.New("upstream is down")
	caller, err := NewAgentCaller(&recordingAgentCaller{err: sentinel}, AgentCallerOptions{})
	if err != nil {
		t.Fatalf("NewAgentCaller: %v", err)
	}
	if _, err := caller.Send(context.Background(), CallRequest{
		Messages: []Message{{Role: "user", Content: "why"}},
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Send error = %v, want the upstream error", err)
	}
}

// TestConversationStoreAdapterPersistsDiagnosis walks the second half of
// the wiring: the runner's store seam onto the real chat backend, so a
// diagnosis lands where the console can open it.
func TestConversationStoreAdapterPersistsDiagnosis(t *testing.T) {
	service, err := conversation.NewService(conversation.Config{Store: conversation.NewMemoryStore(nil)})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	store := NewConversationStore(service, "agent:diagnosis", "claude-opus-5")
	if store == nil {
		t.Fatal("NewConversationStore returned nil")
	}

	conv, err := store.StartConversation(context.Background(), "deploy failure", map[string]string{
		"midnight.kind":         "diagnosis",
		"midnight.operation_id": "op_1",
		"midnight.service_id":   "svc_1",
		"midnight.project_id":   "prj_1",
	})
	if err != nil {
		t.Fatalf("StartConversation: %v", err)
	}
	if conv.ID == "" {
		t.Fatal("conversation id is empty")
	}
	if err := store.AppendMessage(context.Background(), conv.ID, "assistant", "the PORT variable is unset"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	stored, err := service.Get(context.Background(), conv.ID, "agent:diagnosis")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Model != "claude-opus-5" {
		t.Errorf("model = %q, want the diagnosis model recorded on the conversation", stored.Model)
	}
	if stored.ServiceID != "svc_1" || stored.ProjectID != "prj_1" {
		t.Errorf("scope = %q/%q, want the failed service's scope", stored.ProjectID, stored.ServiceID)
	}
	if stored.Metadata["midnight.operation_id"] != "op_1" {
		t.Errorf("metadata lost the operation id: %+v", stored.Metadata)
	}

	page, err := service.ListMessages(context.Background(), conv.ID, "agent:diagnosis", conversation.MessageListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("transcript has %d messages, want 1", len(page.Items))
	}
	msg := page.Items[0]
	if msg.Role != agent.RoleAssistant {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	if len(msg.Content) != 1 || !strings.Contains(msg.Content[0].Text, "PORT") {
		t.Errorf("stored content = %+v, want the diagnosis body", msg.Content)
	}
}

// TestSystemPreambleDoesNotPromiseToolsItLacks guards the mismatch
// between the prompt and the runner's capabilities: the diagnosis loop
// is read-only and cannot call a mutating tool, so instructing the model
// to call one produced advice no one could follow.
func TestSystemPreambleDoesNotPromiseToolsItLacks(t *testing.T) {
	for _, forbidden := range []string{"set_variable", "redeploy(", "rollback("} {
		if strings.Contains(SystemPreamble, forbidden) {
			t.Errorf("system preamble names %q, but the diagnosis loop cannot call mutating tools", forbidden)
		}
	}
	rendered := RenderUserMessage(FailureContext{})
	if strings.Contains(rendered, "set_variable") {
		t.Error("rendered user message names set_variable, which the diagnosis loop cannot call")
	}
}
