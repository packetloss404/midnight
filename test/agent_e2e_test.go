// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package agent_test walks the AI agent's whole journey — HTTP request
// in, provider call out, tool executed against the real control-plane
// stores, audit row on the chain, assistant turn back out over HTTP.
//
// Every other agent test in this repo hands built-by-hand inputs to one
// package in isolation. Each of those passed while four separate wiring
// gaps sat in production: the chat backend sent a zero tool Env so every
// tool answered "not wired"; no tool call was ever audited; the MCP
// server was never mounted; and the diagnosis worker was constructed
// with a nil Caller so it bailed out of every run it was ever asked to
// do. None of those are bugs inside a package — they are bugs in the
// seams between packages, and only a test that crosses the seams can
// see them.
package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/agent/claude"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const testBearerToken = "test-operator-token-0123456789abcdef"

// ---- fake Anthropic transport ---------------------------------------------

// fakeAnthropicTransport returns canned SSE responses for POST
// /v1/messages, and records what was asked. It is the same shape as the
// fakeTransport in internal/agent/claude, deliberately duplicated here:
// this test must exercise the real claude.NewCaller path, and an
// unexported helper is not reachable from another module path.
type fakeAnthropicTransport struct {
	mu        sync.Mutex
	Requests  []map[string]any
	Responses [][]byte
}

func (t *fakeAnthropicTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	t.Requests = append(t.Requests, parsed)

	if len(t.Responses) == 0 {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"server","message":"no canned response"}}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	}
	resp := t.Responses[0]
	t.Responses = t.Responses[1:]
	contentType := "application/json"
	if streaming, _ := parsed["stream"].(bool); streaming {
		contentType = "text/event-stream"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(resp)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Request:    r,
	}, nil
}

func (t *fakeAnthropicTransport) Do(r *http.Request) (*http.Response, error) { return t.RoundTrip(r) }

func (t *fakeAnthropicTransport) requestCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.Requests)
}

// ---- SSE canned-response builders -----------------------------------------

func sseFrame(event string, payload map[string]any) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return "event: " + event + "\ndata: " + string(encoded) + "\n\n"
}

func messageStart(id string) string {
	return sseFrame("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant",
			"model": "claude-opus-5", "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens": 120, "output_tokens": 1,
				"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
			},
		},
	})
}

func messageStop(stopReason string) string {
	return sseFrame("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": 30},
	}) + sseFrame("message_stop", map[string]any{"type": "message_stop"})
}

// toolUseTurn is a canned assistant turn that calls one tool.
func toolUseTurn(id, toolUseID, toolName string, input map[string]any) []byte {
	encoded, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	var b strings.Builder
	b.WriteString(messageStart(id))
	b.WriteString(sseFrame("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{
			"type": "tool_use", "id": toolUseID, "name": toolName, "input": map[string]any{},
		},
	}))
	b.WriteString(sseFrame("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(encoded)},
	}))
	b.WriteString(sseFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
	b.WriteString(messageStop("tool_use"))
	return []byte(b.String())
}

// textTurn is a canned assistant turn that answers in text.
func textTurn(id string, chunks ...string) []byte {
	var b strings.Builder
	b.WriteString(messageStart(id))
	b.WriteString(sseFrame("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	}))
	for _, chunk := range chunks {
		b.WriteString(sseFrame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": chunk},
		}))
	}
	b.WriteString(sseFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
	b.WriteString(messageStop("end_turn"))
	return []byte(b.String())
}

// ---- stack ----------------------------------------------------------------

type agentStack struct {
	server    *httptest.Server
	transport *fakeAnthropicTransport
	chain     *audit.MemoryChain
	projectID string
	serviceID string
}

// bootAgentStack starts the memory-mode API with the agent surface
// wired exactly as production wires it — a real claude.Caller over a
// fake transport, the real tool registry, the real audit chain — and
// seeds one project with one service.
func bootAgentStack(t *testing.T) *agentStack {
	t.Helper()

	resources := api.NewMemoryResourceStore()
	operations := api.NewMemoryOperationStore()
	chain := audit.NewMemoryChain()
	transport := &fakeAnthropicTransport{}

	registry := tools.NewDefaultRegistry()
	caller, err := claude.NewCaller(claude.Options{
		APIKey:     "sk-test-not-a-real-key",
		HTTPClient: transport,
		Registry:   registry,
	})
	if err != nil {
		t.Fatalf("claude.NewCaller: %v", err)
	}

	stack := api.DataStack{
		ProjectStore:      resources,
		ServiceStore:      resources,
		OperationStore:    operations,
		AuditChain:        chain,
		AgentCaller:       caller,
		AgentChatModel:    "claude-opus-5",
		AgentAuditSink:    agentauth.NewAuditChainSink(chain),
		AgentMutationGate: agentauth.NewEnvMutationGateFromValue("true"),
		MCPRegistry:       registry,
		APIBearerToken:    testBearerToken,
	}
	server := httptest.NewServer(api.NewRouterWithDataStack(stack))
	t.Cleanup(server.Close)

	ctx := context.Background()
	project, err := resources.CreateProject(ctx, api.Project{
		WorkspaceID: "wsp_e2e",
		Slug:        "demo",
		DisplayName: "Demo",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	service, err := resources.CreateService(ctx, api.Service{
		ProjectID:   project.ID,
		Slug:        "api",
		DisplayName: "API",
	})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}

	return &agentStack{
		server:    server,
		transport: transport,
		chain:     chain,
		projectID: string(project.ID),
		serviceID: string(service.ID),
	}
}

func (s *agentStack) do(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, s.server.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testBearerToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp, responseBody
}

// startConversation creates a conversation over HTTP and returns its id.
func (s *agentStack) startConversation(t *testing.T) string {
	t.Helper()
	resp, body := s.do(t, http.MethodPost, "/api/v1alpha/agent/conversations", map[string]any{
		"title":      "why is my service down",
		"project_id": s.projectID,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create conversation status = %d, body = %s", resp.StatusCode, body)
	}
	var conv struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &conv); err != nil {
		t.Fatalf("decode conversation: %v (body %s)", err, body)
	}
	if conv.ID == "" {
		t.Fatalf("conversation id missing from %s", body)
	}
	return conv.ID
}

// agentAuditRows returns the audit rows the agent wrote.
func (s *agentStack) agentAuditRows(t *testing.T) []audit.Row {
	t.Helper()
	rows, err := s.chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("read audit chain: %v", err)
	}
	var out []audit.Row
	for _, row := range rows {
		if row.Attrs[agentauth.AuditAttrActorKind] == agentauth.AgentAuditActorKind {
			out = append(out, row)
		}
	}
	return out
}

// ---- the journey ----------------------------------------------------------

// TestAgentChatExecutesToolAgainstRealStoresAndAudits is the missing
// cross-package journey. It fails if the chat backend stops passing a
// real tool Env (the tool would answer "not wired"), if the tool result
// stops reaching the model, if the audit sink comes unwired, or if the
// assistant turn stops being persisted.
func TestAgentChatExecutesToolAgainstRealStoresAndAudits(t *testing.T) {
	stack := bootAgentStack(t)
	stack.transport.Responses = [][]byte{
		toolUseTurn("msg_1", "toolu_1", "list_services", map[string]any{"project_id": stack.projectID}),
		textTurn("msg_2", "You have one service, ", "API."),
	}

	conversationID := stack.startConversation(t)
	resp, body := stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "what services do I have?"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send message status = %d, body = %s", resp.StatusCode, body)
	}

	var sent struct {
		AssistantMessage struct {
			Content []struct {
				Kind       string          `json:"kind"`
				Text       string          `json:"text"`
				ToolUse    json.RawMessage `json:"tool_use"`
				ToolResult *struct {
					ToolUseID string          `json:"tool_use_id"`
					Output    json.RawMessage `json:"output"`
					IsError   bool            `json:"is_error"`
				} `json:"tool_result"`
			} `json:"content"`
		} `json:"assistant_message"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode send response: %v (body %s)", err, body)
	}

	if stack.transport.requestCount() != 2 {
		t.Fatalf("provider was called %d times, want 2 (tool turn + answer turn)", stack.transport.requestCount())
	}

	// 1. The tool ran against the real store and saw the seeded service.
	var toolOutput json.RawMessage
	for _, block := range sent.AssistantMessage.Content {
		if block.ToolResult != nil && block.ToolResult.ToolUseID == "toolu_1" {
			if block.ToolResult.IsError {
				t.Fatalf("list_services returned an error result: %s", block.ToolResult.Output)
			}
			toolOutput = block.ToolResult.Output
		}
	}
	if len(toolOutput) == 0 {
		t.Fatalf("no tool_result for toolu_1 in the persisted turn: %s", body)
	}
	if !bytes.Contains(toolOutput, []byte(stack.serviceID)) {
		t.Errorf("tool result did not carry the seeded service %q: %s", stack.serviceID, toolOutput)
	}
	if !bytes.Contains(toolOutput, []byte("API")) {
		t.Errorf("tool result did not carry the seeded service name: %s", toolOutput)
	}

	// 2. The tool result was fed back to the model as a tool_result
	//    block on a user turn — not dropped, not stringified into prose.
	secondRequest := stack.transport.Requests[1]
	messages, _ := secondRequest["messages"].([]any)
	if len(messages) < 3 {
		t.Fatalf("second provider request had %d messages, want at least 3", len(messages))
	}
	last, _ := messages[len(messages)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message role = %v, want user", last["role"])
	}
	content, _ := last["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("last user message has %d blocks, want 1 tool_result", len(content))
	}
	resultBlock, _ := content[0].(map[string]any)
	if resultBlock["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", resultBlock["type"])
	}

	// 3. The model's final answer came back to the HTTP caller.
	var answer strings.Builder
	for _, block := range sent.AssistantMessage.Content {
		if block.Kind == string(agent.BlockText) {
			answer.WriteString(block.Text)
		}
	}
	if !strings.Contains(answer.String(), "API") {
		t.Errorf("assistant answer = %q, want the model's text", answer.String())
	}
	if sent.StopReason != "end_turn" {
		t.Errorf("stop reason = %q, want end_turn", sent.StopReason)
	}

	// 4. An audit row landed on the tamper-evident chain, scoped to the
	//    resource the tool touched.
	rows := stack.agentAuditRows(t)
	if len(rows) != 1 {
		t.Fatalf("agent audit rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if got := row.Attrs[agentauth.AuditAttrToolName]; got != "list_services" {
		t.Errorf("audit tool_name = %v, want list_services", got)
	}
	if row.Action != agentauth.AgentAuditActionToolCall {
		t.Errorf("audit action = %q, want %q", row.Action, agentauth.AgentAuditActionToolCall)
	}
	if row.ProjectID != stack.projectID {
		t.Errorf("audit project_id = %q, want %q — an audit chain that cannot be queried by resource is only half an audit chain",
			row.ProjectID, stack.projectID)
	}
	if row.ResourceKind != "project" {
		t.Errorf("audit resource_kind = %q, want project", row.ResourceKind)
	}
	if got, _ := row.Attrs[agentauth.AuditAttrToolError].(string); got != "" {
		t.Errorf("audit recorded a tool error: %v", got)
	}

	// 5. The transcript survived the round trip.
	resp, body = stack.do(t, http.MethodGet,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list messages status = %d, body = %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("what services do I have?")) {
		t.Errorf("transcript is missing the user message: %s", body)
	}
}

// TestAgentMutatingToolIsHiddenWhenMutationsAreOff proves the gate is
// enforced across the seam and not merely configured: with the
// per-message toggle off, the mutating tools must not even appear in the
// tool list the model receives.
func TestAgentMutatingToolIsHiddenWhenMutationsAreOff(t *testing.T) {
	stack := bootAgentStack(t)
	stack.transport.Responses = [][]byte{textTurn("msg_1", "I cannot change that.")}

	conversationID := stack.startConversation(t)
	resp, body := stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "set PORT to 8080", "allow_mutations": false})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send message status = %d, body = %s", resp.StatusCode, body)
	}

	toolNames := providerToolNames(t, stack.transport.Requests[0])
	for _, mutating := range []string{"set_variable", "unset_variable", "redeploy", "rollback", "deploy_from_source"} {
		if toolNames[mutating] {
			t.Errorf("mutating tool %q was offered to the model with mutations disabled", mutating)
		}
	}
	// The read-only tools must still be there — the gate scopes the
	// agent, it does not lobotomise it.
	for _, readOnly := range []string{"list_projects", "list_services", "get_logs"} {
		if !toolNames[readOnly] {
			t.Errorf("read-only tool %q was missing from the tool list", readOnly)
		}
	}
}

// TestAgentMutatingToolRefusesHonestlyInMemoryMode is the honesty test.
// Memory mode installs no durable mutation coordinator, so a mutating
// call must come back as an explicit error the model can relay — never
// as a success for a change that did not happen.
func TestAgentMutatingToolRefusesHonestlyInMemoryMode(t *testing.T) {
	stack := bootAgentStack(t)
	stack.transport.Responses = [][]byte{
		toolUseTurn("msg_1", "toolu_1", "set_variable", map[string]any{
			"service_id": stack.serviceID,
			"name":       "PORT",
			"value":      "8080",
		}),
		textTurn("msg_2", "I could not make that change."),
	}

	conversationID := stack.startConversation(t)
	resp, body := stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "set PORT to 8080", "allow_mutations": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("send message status = %d, body = %s", resp.StatusCode, body)
	}

	// The mutating tool must have been offered (the user opted in)...
	toolNames := providerToolNames(t, stack.transport.Requests[0])
	if !toolNames["set_variable"] {
		t.Fatal("set_variable was not offered to the model even though mutations were allowed")
	}

	// ...and must have failed loudly.
	var sent struct {
		AssistantMessage struct {
			Content []struct {
				ToolResult *struct {
					Output  json.RawMessage `json:"output"`
					IsError bool            `json:"is_error"`
				} `json:"tool_result"`
			} `json:"content"`
		} `json:"assistant_message"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode send response: %v", err)
	}
	var found bool
	for _, block := range sent.AssistantMessage.Content {
		if block.ToolResult == nil {
			continue
		}
		found = true
		if !block.ToolResult.IsError {
			t.Fatalf("set_variable reported success in memory mode: %s", block.ToolResult.Output)
		}
		if !bytes.Contains(block.ToolResult.Output, []byte("not available")) {
			t.Errorf("refusal should say plainly that the change is unavailable, got %s", block.ToolResult.Output)
		}
	}
	if !found {
		t.Fatal("no tool_result was persisted for the mutating call")
	}

	// The refusal is on the audit chain too: an attempted change that
	// did not happen is still something an operator needs to see.
	rows := stack.agentAuditRows(t)
	if len(rows) != 1 {
		t.Fatalf("agent audit rows = %d, want 1", len(rows))
	}
	if got := rows[0].Attrs[agentauth.AuditAttrToolName]; got != "set_variable" {
		t.Errorf("audit tool_name = %v, want set_variable", got)
	}
	if got, _ := rows[0].Attrs[agentauth.AuditAttrToolError].(string); got == "" {
		t.Error("audit row did not record the tool error")
	}
	if rows[0].ResourceID != stack.serviceID {
		t.Errorf("audit resource_id = %q, want the targeted service %q", rows[0].ResourceID, stack.serviceID)
	}

	// The variable's value must not have followed the tool input onto
	// the audit chain. The chain is tamper-evident and long-retention:
	// a secret written there is a secret that cannot be unwritten.
	encodedRow, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal audit row: %v", err)
	}
	if bytes.Contains(encodedRow, []byte("8080")) {
		t.Errorf("the variable value leaked into the audit chain: %s", encodedRow)
	}
	toolInput, _ := rows[0].Attrs[agentauth.AuditAttrToolInput].(map[string]any)
	if toolInput == nil {
		t.Fatalf("audit row carried no tool input: %v", rows[0].Attrs)
	}
	if toolInput["name"] != "PORT" {
		t.Errorf("audit tool input lost the variable name: %v", toolInput)
	}
	redacted, _ := toolInput["value"].(string)
	if !strings.HasPrefix(redacted, "[redacted") {
		t.Errorf("audit tool input value = %q, want a redaction placeholder", redacted)
	}
}

// TestAgentTranscriptNeverEchoesAVariableValue guards the other half of
// the write-only rule: a value that round-trips through the conversation
// transcript is a value sitting in the chat history in the clear.
func TestAgentTranscriptNeverEchoesAVariableValue(t *testing.T) {
	stack := bootAgentStack(t)
	const secret = "super-secret-value-9174"
	stack.transport.Responses = [][]byte{
		toolUseTurn("msg_1", "toolu_1", "set_variable", map[string]any{
			"service_id": stack.serviceID,
			"name":       "API_TOKEN",
			"value":      secret,
		}),
		textTurn("msg_2", "I could not make that change."),
	}

	conversationID := stack.startConversation(t)
	if resp, body := stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "set API_TOKEN", "allow_mutations": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("send message status = %d, body = %s", resp.StatusCode, body)
	}

	resp, body := stack.do(t, http.MethodGet,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list messages status = %d", resp.StatusCode)
	}
	// The model's own tool_use block is echoed back verbatim by design —
	// it is the model's turn, and dropping it would corrupt the replay.
	// What must never appear is the value coming back *out* of the
	// platform: in a tool result, or in an audit row.
	var transcript struct {
		Data []struct {
			Content []struct {
				Kind       string `json:"kind"`
				ToolResult *struct {
					Output json.RawMessage `json:"output"`
				} `json:"tool_result"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &transcript); err != nil {
		t.Fatalf("decode transcript: %v (body %s)", err, body)
	}
	for _, msg := range transcript.Data {
		for _, block := range msg.Content {
			if block.ToolResult != nil && bytes.Contains(block.ToolResult.Output, []byte(secret)) {
				t.Errorf("the variable value came back in a tool result: %s", block.ToolResult.Output)
			}
		}
	}

	rows := stack.agentAuditRows(t)
	if len(rows) != 1 {
		t.Fatalf("agent audit rows = %d, want 1", len(rows))
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("marshal audit row: %v", err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Errorf("the variable value leaked into the audit chain: %s", encoded)
	}
}

// TestMCPServerIsMountedAndListsTools guards the third wiring gap: the
// MCP server existed, was tested in isolation, and was not mounted on
// any route.
func TestMCPServerIsMountedAndListsTools(t *testing.T) {
	stack := bootAgentStack(t)

	resp, body := stack.do(t, http.MethodGet, "/.well-known/mcp", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/.well-known/mcp status = %d, body = %s", resp.StatusCode, body)
	}

	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "midnight-e2e", "version": "0"},
		},
	}
	encoded, err := json.Marshal(initialize)
	if err != nil {
		t.Fatalf("marshal initialize: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, stack.server.URL+"/mcp/v1", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testBearerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	initResp, err := stack.server.Client().Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	initBody, _ := io.ReadAll(initResp.Body)
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK {
		t.Fatalf("mcp initialize status = %d, body = %s", initResp.StatusCode, initBody)
	}
	sessionID := initResp.Header.Get("Mcp-Session-Id")

	listCall := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}
	encoded, err = json.Marshal(listCall)
	if err != nil {
		t.Fatalf("marshal tools/list: %v", err)
	}
	req, err = http.NewRequest(http.MethodPost, stack.server.URL+"/mcp/v1", bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testBearerToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	listResp, err := stack.server.Client().Do(req)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("tools/list status = %d, body = %s", listResp.StatusCode, listBody)
	}
	if !bytes.Contains(listBody, []byte("list_services")) {
		t.Errorf("tools/list did not advertise list_services: %s", listBody)
	}
}

// TestAgentBudgetStopsRunawayConversation walks the budget across the
// HTTP seam: once a conversation has spent its cap, further turns are
// refused with a 429 rather than quietly continuing to spend.
func TestAgentBudgetStopsRunawayConversation(t *testing.T) {
	t.Setenv(agentauth.EnvConversationTokenCap, "10")
	stack := bootAgentStack(t)
	stack.transport.Responses = [][]byte{
		textTurn("msg_1", "first answer"),
		textTurn("msg_2", "second answer"),
	}

	conversationID := stack.startConversation(t)
	resp, body := stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first send status = %d, body = %s", resp.StatusCode, body)
	}

	// The first turn reported 150 tokens against a 10-token cap.
	resp, body = stack.do(t, http.MethodPost,
		"/api/v1alpha/agent/conversations/"+conversationID+"/messages",
		map[string]any{"content": "two"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second send status = %d, want 429; body = %s", resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("budget")) {
		t.Errorf("429 body should explain the budget breach: %s", body)
	}
	if stack.transport.requestCount() != 1 {
		t.Errorf("provider was called %d times, want 1: an exhausted budget must not reach the provider",
			stack.transport.requestCount())
	}
}

// providerToolNames returns the set of tool names offered in a provider
// request body.
func providerToolNames(t *testing.T, request map[string]any) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	toolList, _ := request["tools"].([]any)
	for _, raw := range toolList {
		tool, _ := raw.(map[string]any)
		if name, ok := tool["name"].(string); ok {
			names[name] = true
		}
	}
	return names
}

// unusedGuards keeps imports honest if a test above is trimmed.
var (
	_ = time.Second
	_ = orailtypes.ID("")
	_ = api.OperationEvent{}
)
