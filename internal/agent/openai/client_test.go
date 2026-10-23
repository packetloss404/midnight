package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/tools"
)

func TestNewCallerMissingAPIKey(t *testing.T) {
	t.Setenv(EnvAPIKey, "")

	_, err := NewCaller(Options{})
	if !errors.Is(err, agent.AgentAPIKeyMissing) {
		t.Fatalf("NewCaller() error = %v, want AgentAPIKeyMissing", err)
	}
}

func TestSendTextResponse(t *testing.T) {
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q, want /v1/responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "Bearer test-key" {
			sawAuth = true
		}
		var req responseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-test" {
			t.Fatalf("model = %q", req.Model)
		}
		if !strings.Contains(req.Instructions, "extra context") {
			t.Fatalf("instructions did not include system preamble: %q", req.Instructions)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"resp_1",
			"status":"completed",
			"model":"gpt-test",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello from openai"}]}],
			"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}
		}`))
	}))
	defer srv.Close()

	c, err := NewCaller(Options{APIKey: "test-key", BaseURL: srv.URL + "/v1", DefaultModel: "gpt-test", Registry: tools.New()})
	if err != nil {
		t.Fatalf("NewCaller() error = %v", err)
	}
	res, err := c.Send(context.Background(), agent.CallRequest{
		Conversation:   []agent.Message{agent.TextMessage(agent.RoleUser, "hello")},
		SystemPreamble: "extra context",
		Env:            agent.Env{},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if !sawAuth {
		t.Fatal("Authorization header was not sent")
	}
	if res.FinalText != "hello from openai" {
		t.Fatalf("FinalText = %q", res.FinalText)
	}
	if res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %#v", res.Usage)
	}
}

func TestSendToolLoop(t *testing.T) {
	reg := tools.New()
	reg.MustRegister(tools.Tool{
		Name:        "echo",
		Description: "echo a message",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
		Execute: func(_ context.Context, input json.RawMessage, _ agent.Env) (any, error) {
			var req struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(input, &req)
			return map[string]string{"echoed": req.Message}, nil
		},
	})

	var calls atomic.Int32
	var secondRequest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if n == 2 {
			secondRequest = string(raw)
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{
				"id":"resp_1",
				"status":"completed",
				"model":"gpt-test",
				"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":"{\"message\":\"hi\"}"}],
				"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"resp_2",
			"status":"completed",
			"model":"gpt-test",
			"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"echoed hi"}]}],
			"usage":{"input_tokens":25,"output_tokens":4,"total_tokens":29}
		}`))
	}))
	defer srv.Close()

	c, err := NewCaller(Options{APIKey: "test-key", BaseURL: srv.URL + "/v1", DefaultModel: "gpt-test", Registry: reg})
	if err != nil {
		t.Fatalf("NewCaller() error = %v", err)
	}
	res, err := c.Send(context.Background(), agent.CallRequest{
		Conversation: []agent.Message{agent.TextMessage(agent.RoleUser, "use the tool")},
		Env:          agent.Env{AllowMutations: true},
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls = %d, want 2", calls.Load())
	}
	if !strings.Contains(secondRequest, `"type":"function_call_output"`) || !strings.Contains(secondRequest, `"call_id":"call_1"`) {
		t.Fatalf("second request did not include tool output: %s", secondRequest)
	}
	if res.FinalText != "echoed hi" {
		t.Fatalf("FinalText = %q", res.FinalText)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("Messages len = %d, want assistant tool_use, user tool_result, assistant text", len(res.Messages))
	}
}
