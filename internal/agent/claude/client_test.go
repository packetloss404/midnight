package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

// ---- fake transport -------------------------------------------------------

// fakeTransport is a minimal http.RoundTripper that returns canned
// responses for v1/messages POSTs. Test bodies push response payloads
// into Responses; each request consumes the next entry.
type fakeTransport struct {
	mu          sync.Mutex
	Requests    []map[string]any
	RawRequests []string
	Responses   [][]byte
	Calls       int32
}

func (t *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	atomic.AddInt32(&t.Calls, 1)
	t.mu.Lock()
	defer t.mu.Unlock()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(bodyBytes, &parsed)
	t.Requests = append(t.Requests, parsed)
	t.RawRequests = append(t.RawRequests, string(bodyBytes))

	if len(t.Responses) == 0 {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"server","message":"no canned response"}}`)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	}
	resp := t.Responses[0]
	t.Responses = t.Responses[1:]
	contentType := "application/json"
	if streaming, _ := parsed["stream"].(bool); streaming {
		// Streaming requests get the SSE content type; test bodies are
		// responsible for pushing an SSE-framed payload for those.
		contentType = "text/event-stream"
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(resp)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Request:    r,
	}, nil
}

func (t *fakeTransport) Do(r *http.Request) (*http.Response, error) {
	return t.RoundTrip(r)
}

// ---- canned message factories --------------------------------------------

func textResponse(id, text string) []byte {
	return mustJSONBytes(map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         "claude-sonnet-4-6",
		"content":       []any{map[string]any{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"container":     nil,
		"stop_details":  map[string]any{},
		"usage": map[string]any{
			"input_tokens":                100,
			"output_tokens":               20,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     50,
			"server_tool_use":             map[string]any{},
			"service_tier":                "standard",
			"inference_geo":               "us",
			"cache_creation":              map[string]any{},
		},
	})
}

func toolUseResponse(id, toolID, toolName string, input map[string]any) []byte {
	return mustJSONBytes(map[string]any{
		"id":    id,
		"type":  "message",
		"role":  "assistant",
		"model": "claude-sonnet-4-6",
		"content": []any{
			map[string]any{
				"type":  "tool_use",
				"id":    toolID,
				"name":  toolName,
				"input": input,
				"caller": map[string]any{
					"type": "direct",
				},
			},
		},
		"stop_reason":   "tool_use",
		"stop_sequence": nil,
		"container":     nil,
		"stop_details":  map[string]any{},
		"usage": map[string]any{
			"input_tokens":                500,
			"output_tokens":               40,
			"cache_creation_input_tokens": 200,
			"cache_read_input_tokens":     0,
			"server_tool_use":             map[string]any{},
			"service_tier":                "standard",
			"inference_geo":               "us",
			"cache_creation":              map[string]any{},
		},
	})
}

// ---- canned SSE stream factories ------------------------------------------

// sseFrame renders one Anthropic SSE frame.
func sseFrame(event string, payload map[string]any) string {
	return "event: " + event + "\ndata: " + string(mustJSONBytes(payload)) + "\n\n"
}

// streamStartFrames renders message_start for a turn. Input + cache
// counters ride on message_start; output tokens arrive on message_delta,
// which is exactly how the real wire behaves.
func streamStartFrames(id string) string {
	return sseFrame("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         "claude-sonnet-4-6",
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":                100,
				"output_tokens":               1,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     50,
			},
		},
	})
}

func streamStopFrames(stopReason string, outputTokens int) string {
	return sseFrame("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	}) + sseFrame("message_stop", map[string]any{"type": "message_stop"})
}

// streamTextResponse renders a complete SSE turn whose single text block
// is delivered as several deltas, so tests can assert that the caller
// forwards chunks rather than one block-sized lump.
func streamTextResponse(id string, chunks ...string) []byte {
	var b strings.Builder
	b.WriteString(streamStartFrames(id))
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
	b.WriteString(streamStopFrames("end_turn", 20))
	return []byte(b.String())
}

// streamToolUseResponse renders a complete SSE turn containing one
// tool_use block whose arguments arrive as input_json_delta fragments.
func streamToolUseResponse(id, toolID, toolName string, inputJSON string) []byte {
	var b strings.Builder
	b.WriteString(streamStartFrames(id))
	b.WriteString(sseFrame("content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{
			"type": "tool_use", "id": toolID, "name": toolName, "input": map[string]any{},
		},
	}))
	b.WriteString(sseFrame("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": inputJSON},
	}))
	b.WriteString(sseFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
	b.WriteString(streamStopFrames("tool_use", 40))
	return []byte(b.String())
}

// streamToolUseBlocks renders an SSE turn carrying several tool_use
// blocks — the parallel-tool-call shape.
func streamToolUseBlocks(id string, calls ...[2]string) []byte {
	var b strings.Builder
	b.WriteString(streamStartFrames(id))
	for i, call := range calls {
		b.WriteString(sseFrame("content_block_start", map[string]any{
			"type": "content_block_start", "index": i,
			"content_block": map[string]any{
				"type": "tool_use", "id": call[0], "name": call[1], "input": map[string]any{},
			},
		}))
		b.WriteString(sseFrame("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": i,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"},
		}))
		b.WriteString(sseFrame("content_block_stop", map[string]any{"type": "content_block_stop", "index": i}))
	}
	b.WriteString(streamStopFrames("tool_use", 40))
	return []byte(b.String())
}

func mustJSONBytes(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ---- env helpers ----------------------------------------------------------

type stubServices struct{}

func (stubServices) ListProjects(context.Context) ([]agent.Project, error) {
	return []agent.Project{{ID: "p1", Name: "demo"}}, nil
}
func (stubServices) ListServices(context.Context, string) ([]agent.Service, error) {
	return []agent.Service{{ID: "s1", Name: "api", ProjectID: "p1"}}, nil
}
func (stubServices) GetService(context.Context, string) (agent.Service, error) {
	return agent.Service{ID: "s1", Name: "api", ProjectID: "p1"}, nil
}
func (stubServices) SetVariable(context.Context, agent.VariableWrite) error   { return nil }
func (stubServices) UnsetVariable(context.Context, agent.VariableWrite) error { return nil }

type stubOps struct{}

func (stubOps) GetDeployment(context.Context, string) (agent.Deployment, error) {
	return agent.Deployment{ID: "d1", Status: "running"}, nil
}
func (stubOps) GetLogs(context.Context, string, int, string) ([]agent.LogLine, error) {
	return []agent.LogLine{{Message: "hello"}}, nil
}
func (stubOps) Redeploy(context.Context, string) (agent.Deployment, error) {
	return agent.Deployment{}, nil
}
func (stubOps) Rollback(context.Context, string) (agent.Deployment, error) {
	return agent.Deployment{}, nil
}

type stubBuilds struct{}

func (stubBuilds) DeployFromSource(context.Context, string, string, string) (agent.Deployment, error) {
	return agent.Deployment{}, nil
}

type captureAudit struct {
	mu      sync.Mutex
	entries []agent.AuditEntry
}

func (c *captureAudit) Record(_ context.Context, e agent.AuditEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	return nil
}

func defaultEnv() agent.Env {
	return agent.Env{
		Services:       stubServices{},
		Operations:     stubOps{},
		Builds:         stubBuilds{},
		Audit:          &captureAudit{},
		Actor:          "agent:test",
		AllowMutations: false,
	}
}

func newTestCaller(t *testing.T, transport *fakeTransport) agent.Caller {
	t.Helper()
	c, err := NewCaller(Options{
		APIKey:     "sk-test",
		HTTPClient: transport,
		Registry:   tools.NewDefaultRegistry(),
	})
	if err != nil {
		t.Fatalf("NewCaller: %v", err)
	}
	return c
}

// ---- tests ----------------------------------------------------------------

func TestNewCallerMissingKeyFailsClosed(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, err := NewCaller(Options{})
	if !errors.Is(err, agent.AgentAPIKeyMissing) {
		t.Fatalf("expected AgentAPIKeyMissing, got %v", err)
	}
}

func TestSendSingleTurn(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "hello world")}}
	c := newTestCaller(t, tp)

	res, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.FinalText != "hello world" {
		t.Errorf("expected hello world, got %q", res.FinalText)
	}
	if res.Iterations != 1 {
		t.Errorf("expected 1 iter, got %d", res.Iterations)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %s", res.StopReason)
	}
}

func TestSendCacheControlOnSystemAndTools(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "list projects")},
		Env:          defaultEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.RawRequests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(tp.RawRequests))
	}
	body := tp.RawRequests[0]
	// system prompt should carry at least one cache_control breakpoint.
	systemPart := extractField(t, body, "system")
	if !strings.Contains(systemPart, `"cache_control"`) {
		t.Errorf("expected cache_control on system prompt, got: %s", systemPart)
	}
	// last tool should carry cache_control.
	toolsPart := extractField(t, body, "tools")
	if !strings.Contains(toolsPart, `"cache_control"`) {
		t.Errorf("expected cache_control on tools list, got: %s", toolsPart)
	}
}

func TestSendToolLoopClosesCleanly(t *testing.T) {
	tp := &fakeTransport{
		Responses: [][]byte{
			toolUseResponse("m1", "tu_1", "list_projects", map[string]any{}),
			textResponse("m2", "found 1 project: demo"),
		},
	}
	c := newTestCaller(t, tp)
	env := defaultEnv()
	res, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "list my projects")},
		Env:          env,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Iterations != 2 {
		t.Errorf("expected 2 iterations, got %d", res.Iterations)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("expected end_turn, got %s", res.StopReason)
	}
	// Second request must contain the tool_result.
	if !strings.Contains(tp.RawRequests[1], `"tool_use_id":"tu_1"`) {
		t.Errorf("expected tool_use_id echoed in second request, got: %s", tp.RawRequests[1])
	}
}

func TestSendHaltsAtMaxIterations(t *testing.T) {
	// Always return tool_use — the loop should bail with ErrMaxIterations.
	tp := &fakeTransport{}
	for i := 0; i < 5; i++ {
		tp.Responses = append(tp.Responses, toolUseResponse("m", "tu", "list_projects", map[string]any{}))
	}
	c := newTestCaller(t, tp)
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation:  []agent.Message{agent.TextMessage(agent.RoleUser, "loop")},
		Env:           defaultEnv(),
		MaxIterations: 3,
	})
	if !errors.Is(err, agent.ErrMaxIterations) {
		t.Fatalf("expected ErrMaxIterations, got %v", err)
	}
	if atomic.LoadInt32(&tp.Calls) != 3 {
		t.Errorf("expected 3 calls, got %d", tp.Calls)
	}
}

func TestSendMutatingToolsHiddenWhenAllowMutationsFalse(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	env := defaultEnv()
	env.AllowMutations = false
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          env,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := tp.RawRequests[0]
	for _, mut := range []string{"set_variable", "unset_variable", "redeploy", "rollback", "deploy_from_source"} {
		if strings.Contains(body, `"name":"`+mut+`"`) {
			t.Errorf("mutating tool %q must not appear when AllowMutations=false", mut)
		}
	}
}

func TestSendMutatingToolsExposedWhenAllowed(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	env := defaultEnv()
	env.AllowMutations = true
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          env,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tp.RawRequests[0], `"name":"set_variable"`) {
		t.Error("expected set_variable in tool list when mutations enabled")
	}
}

func TestStreamEmitsTextAndDoneEvents(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "hi ", "there")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "ping")},
		Env:          defaultEnv(),
	}, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var deltas []string
	var sawDone bool
	for ev := range out {
		switch ev.Kind {
		case agent.EventTextDelta:
			deltas = append(deltas, ev.Delta)
		case agent.EventDone:
			sawDone = true
			if ev.StopReason != "end_turn" {
				t.Errorf("unexpected stop reason %q", ev.StopReason)
			}
		}
	}
	if strings.Join(deltas, "") != "hi there" {
		t.Errorf("reassembled text = %q, want %q", strings.Join(deltas, ""), "hi there")
	}
	if !sawDone {
		t.Error("expected EventDone")
	}
}

// TestStreamEmitsTokenLevelDeltas is the regression guard for the switch
// off the non-streaming endpoint: the old implementation buffered the
// whole turn and emitted a single block-sized delta, which looked
// identical to a working stream from the console's side.
func TestStreamEmitsTokenLevelDeltas(t *testing.T) {
	chunks := []string{"The ", "deploy ", "failed ", "because..."}
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", chunks...)}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 32)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "why?")},
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got []string
	for ev := range out {
		if ev.Kind == agent.EventTextDelta {
			got = append(got, ev.Delta)
		}
	}
	if len(got) != len(chunks) {
		t.Fatalf("got %d text deltas (%q), want %d — one per upstream chunk", len(got), got, len(chunks))
	}
	for i, want := range chunks {
		if got[i] != want {
			t.Errorf("delta %d = %q, want %q", i, got[i], want)
		}
	}

	// The request must actually have asked for a stream.
	if len(tp.Requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(tp.Requests))
	}
	if streaming, _ := tp.Requests[0]["stream"].(bool); !streaming {
		t.Error("Stream did not set stream=true on the upstream request")
	}
}

func TestStreamEmitsToolEvents(t *testing.T) {
	tp := &fakeTransport{
		Responses: [][]byte{
			streamToolUseResponse("m1", "tu_99", "list_projects", "{}"),
			streamTextResponse("m2", "done"),
		},
	}
	c := newTestCaller(t, tp)
	out := make(chan agent.StreamEvent, 16)
	err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "list")},
		Env:          defaultEnv(),
	}, out)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawCall, sawResult, sawDone bool
	for ev := range out {
		switch ev.Kind {
		case agent.EventToolCall:
			sawCall = true
			if ev.ToolCall == nil || ev.ToolCall.Name != "list_projects" {
				t.Errorf("unexpected tool call %#v", ev.ToolCall)
			}
		case agent.EventToolResult:
			sawResult = true
			if ev.ToolResult == nil || ev.ToolResult.ToolUseID != "tu_99" {
				t.Errorf("unexpected tool result %#v", ev.ToolResult)
			}
		case agent.EventDone:
			sawDone = true
		}
	}
	if !sawCall || !sawResult || !sawDone {
		t.Errorf("missing events: call=%v result=%v done=%v", sawCall, sawResult, sawDone)
	}
}

func TestSendEmitsUsageThroughMetricsEmitter(t *testing.T) {
	tp := &fakeTransport{
		Responses: [][]byte{
			toolUseResponse("m1", "tu_1", "list_projects", map[string]any{}),
			textResponse("m2", "ok"),
		},
	}
	var captured []agent.Usage
	c, err := NewCaller(Options{
		APIKey:     "sk-test",
		HTTPClient: tp,
		Registry:   tools.NewDefaultRegistry(),
		Metrics: agent.MetricsEmitterFunc(func(_ context.Context, u agent.Usage) {
			captured = append(captured, u)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "list")},
		Env:          defaultEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 usage emission, got %d", len(captured))
	}
	u := captured[0]
	if u.InputTokens != 600 { // 500 + 100
		t.Errorf("expected 600 input tokens, got %d", u.InputTokens)
	}
	if u.OutputTokens != 60 {
		t.Errorf("expected 60 output, got %d", u.OutputTokens)
	}
	if u.CacheCreationTokens != 200 {
		t.Errorf("expected 200 cache creation, got %d", u.CacheCreationTokens)
	}
	if u.CacheReadTokens != 50 {
		t.Errorf("expected 50 cache read, got %d", u.CacheReadTokens)
	}
	if u.CostUSDEstimated == 0 {
		t.Error("expected non-zero cost estimate")
	}
	if u.IterationCount != 2 {
		t.Errorf("expected 2 iters, got %d", u.IterationCount)
	}
}

func TestSendRespectsContextCancellation(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := c.Send(ctx, agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
	})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestSendRejectsEmptyConversation(t *testing.T) {
	tp := &fakeTransport{}
	c := newTestCaller(t, tp)
	_, err := c.Send(context.Background(), agent.CallRequest{Env: defaultEnv()})
	if err == nil {
		t.Fatal("expected empty conversation error")
	}
}

func TestUsageAddAccumulates(t *testing.T) {
	a := agent.Usage{Model: "m", InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, CostUSDEstimated: 0.5, IterationCount: 1}
	a.Add(agent.Usage{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 40, CostUSDEstimated: 1.5, IterationCount: 2})
	if a.InputTokens != 11 || a.OutputTokens != 22 || a.CacheCreationTokens != 33 || a.CacheReadTokens != 44 {
		t.Errorf("Add did not accumulate: %#v", a)
	}
	if a.CostUSDEstimated != 2.0 || a.IterationCount != 3 {
		t.Errorf("Add cost/iter wrong: %#v", a)
	}
}

func TestDeadlineEnforced(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	// Very short deadline; should still complete because fake transport
	// is synchronous and fast. The point is to exercise the timeout
	// wiring path.
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
		Deadline:     5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModelOverrideHonored(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	_, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
		Model:        "claude-opus-4-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := tp.RawRequests[0]
	if !strings.Contains(body, `"model":"claude-opus-4-7"`) {
		t.Errorf("expected opus override in request body: %s", body)
	}
}

func TestDefaultModelFromEnvVar(t *testing.T) {
	t.Setenv(EnvChatModel, "claude-opus-4-6")
	tp := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	c, err := NewCaller(Options{APIKey: "sk-test", HTTPClient: tp, Registry: tools.NewDefaultRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tp.RawRequests[0], `"model":"claude-opus-4-6"`) {
		t.Errorf("expected env-override model, got: %s", tp.RawRequests[0])
	}
}

// extractField marshal-roundtrips out the value for a top-level key. We
// don't want to parse the full body — just confirm the substring of the
// serialized field appears, since cache_control is buried in nested
// content.
func extractField(t *testing.T, raw, key string) string {
	t.Helper()
	var generic map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	v, ok := generic[key]
	if !ok {
		t.Fatalf("missing field %q in body: %s", key, raw)
	}
	return string(v)
}

// ---- model controls -------------------------------------------------------

func TestStreamAppliesAdaptiveThinkingAndEffort(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-opus-5",
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	req := tp.Requests[0]
	thinking, ok := req["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no thinking config: %v", req["thinking"])
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v, want adaptive", thinking["type"])
	}
	if _, hasBudget := thinking["budget_tokens"]; hasBudget {
		t.Error("budget_tokens must not be sent: it is rejected on current models")
	}
	outputConfig, ok := req["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no output_config: %v", req["output_config"])
	}
	if outputConfig["effort"] != "high" {
		t.Errorf("effort = %v, want high", outputConfig["effort"])
	}
}

func TestCallRequestOverridesThinkingAndEffort(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-opus-5",
		Thinking:     agent.ThinkingOff,
		Effort:       agent.EffortLow,
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	req := tp.Requests[0]
	thinking, _ := req["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want type=disabled", req["thinking"])
	}
	outputConfig, _ := req["output_config"].(map[string]any)
	if outputConfig == nil || outputConfig["effort"] != "low" {
		t.Errorf("output_config = %v, want effort=low", req["output_config"])
	}
}

// TestThinkingOffClampsEffortOnOpus5 covers the documented 400: Opus 5
// accepts an explicit thinking=disabled only at effort high or below.
func TestThinkingOffClampsEffortOnOpus5(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-opus-5",
		Thinking:     agent.ThinkingOff,
		Effort:       agent.EffortMax,
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	outputConfig, _ := tp.Requests[0]["output_config"].(map[string]any)
	if outputConfig == nil || outputConfig["effort"] != "high" {
		t.Errorf("output_config = %v, want effort clamped to high", tp.Requests[0]["output_config"])
	}
}

// TestModelControlsOmittedOnLegacyModel guards the operator who points
// MIDNIGHT_AGENT_CHAT_MODEL at a model that predates adaptive thinking:
// sending the modern knobs there is a 400, so they must be dropped.
func TestModelControlsOmittedOnLegacyModel(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-haiku-4-5-20251001",
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	req := tp.Requests[0]
	if _, ok := req["thinking"]; ok {
		t.Errorf("thinking must be omitted on a pre-adaptive model, got %v", req["thinking"])
	}
	if _, ok := req["output_config"]; ok {
		t.Errorf("output_config must be omitted on a pre-effort model, got %v", req["output_config"])
	}
}

func TestXHighEffortClampedOnModelsWithoutIt(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-sonnet-4-6",
		Effort:       agent.EffortXHigh,
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	outputConfig, _ := tp.Requests[0]["output_config"].(map[string]any)
	if outputConfig == nil || outputConfig["effort"] != "high" {
		t.Errorf("output_config = %v, want effort clamped to high", tp.Requests[0]["output_config"])
	}
}

func TestEffortAndThinkingFromEnv(t *testing.T) {
	t.Setenv(EnvEffort, "max")
	t.Setenv(EnvThinking, "off")
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c, err := NewCaller(Options{APIKey: "sk-test", HTTPClient: tp, Registry: tools.NewDefaultRegistry()})
	if err != nil {
		t.Fatalf("NewCaller: %v", err)
	}

	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-sonnet-5",
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	req := tp.Requests[0]
	thinking, _ := req["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "disabled" {
		t.Errorf("thinking = %v, want disabled from env", req["thinking"])
	}
	outputConfig, _ := req["output_config"].(map[string]any)
	if outputConfig == nil || outputConfig["effort"] != "max" {
		t.Errorf("output_config = %v, want effort=max from env", req["output_config"])
	}
}

func TestStreamMaxTokensDefaultsHigherThanSend(t *testing.T) {
	streamTP := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	sc := newTestCaller(t, streamTP)
	out := make(chan agent.StreamEvent, 16)
	if err := sc.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	sendTP := &fakeTransport{Responses: [][]byte{textResponse("m1", "ok")}}
	nc := newTestCaller(t, sendTP)
	if _, err := nc.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Env:          defaultEnv(),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	streamMax, _ := streamTP.Requests[0]["max_tokens"].(float64)
	sendMax, _ := sendTP.Requests[0]["max_tokens"].(float64)
	if int(streamMax) != agent.DefaultStreamMaxTokens {
		t.Errorf("stream max_tokens = %v, want %d", streamMax, agent.DefaultStreamMaxTokens)
	}
	if int(sendMax) != agent.DefaultMaxTokens {
		t.Errorf("send max_tokens = %v, want %d", sendMax, agent.DefaultMaxTokens)
	}
	if streamMax <= sendMax {
		t.Error("streaming should get a larger default budget than non-streaming")
	}
}

func TestMaxTokensClampedToModelCeiling(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		Model:        "claude-haiku-4-5",
		MaxTokens:    500_000,
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	maxTokens, _ := tp.Requests[0]["max_tokens"].(float64)
	if int(maxTokens) != 64_000 {
		t.Errorf("max_tokens = %v, want clamp to the model ceiling 64000", maxTokens)
	}
}

// ---- streaming preserves the pre-existing loop invariants -----------------

// TestStreamParallelToolResultsRideOneUserMessage guards the invariant
// that splitting parallel tool_results across messages silently teaches
// the model to stop making parallel calls.
func TestStreamParallelToolResultsRideOneUserMessage(t *testing.T) {
	tp := &fakeTransport{
		Responses: [][]byte{
			streamToolUseBlocks("m1",
				[2]string{"tu_a", "list_projects"},
				[2]string{"tu_b", "list_services"},
			),
			streamTextResponse("m2", "done"),
		},
	}
	c := newTestCaller(t, tp)
	out := make(chan agent.StreamEvent, 32)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "look")},
		Env:          defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var toolCalls, toolResults int
	for ev := range out {
		switch ev.Kind {
		case agent.EventToolCall:
			toolCalls++
		case agent.EventToolResult:
			toolResults++
		}
	}
	if toolCalls != 2 || toolResults != 2 {
		t.Errorf("tool calls=%d results=%d, want 2 and 2", toolCalls, toolResults)
	}

	if len(tp.Requests) != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(tp.Requests))
	}
	messages, _ := tp.Requests[1]["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("second request had %d messages, want 3 (user, assistant, tool results)", len(messages))
	}
	last, _ := messages[2].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message role = %v, want user", last["role"])
	}
	content, _ := last["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("tool results were split across messages: last user message has %d blocks, want 2", len(content))
	}
	for i, raw := range content {
		block, _ := raw.(map[string]any)
		if block["type"] != "tool_result" {
			t.Errorf("block %d type = %v, want tool_result", i, block["type"])
		}
	}
}

// TestStreamKeepsCacheControlBreakpoints guards the two ephemeral
// breakpoints (system prompt + last tool definition) through the
// streaming path.
func TestStreamKeepsCacheControlBreakpoints(t *testing.T) {
	tp := &fakeTransport{Responses: [][]byte{streamTextResponse("m1", "ok")}}
	c := newTestCaller(t, tp)
	out := make(chan agent.StreamEvent, 16)
	if err := c.Stream(context.Background(), agent.CallRequest{
		Conversation:   []agent.Message{agent.TextMessage(agent.RoleUser, "hi")},
		SystemPreamble: "operator context",
		Env:            defaultEnv(),
	}, out); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	drain(out)

	raw := tp.RawRequests[0]
	if strings.Count(raw, `"cache_control"`) < 2 {
		t.Errorf("expected at least 2 cache_control breakpoints in streaming request, got %d", strings.Count(raw, `"cache_control"`))
	}
	system, _ := tp.Requests[0]["system"].([]any)
	if len(system) == 0 {
		t.Fatal("streaming request carried no system blocks")
	}
	first, _ := system[0].(map[string]any)
	if _, ok := first["cache_control"]; !ok {
		t.Error("platform system prompt lost its cache_control breakpoint under streaming")
	}
}

func drain(out <-chan agent.StreamEvent) {
	for range out {
	}
}
