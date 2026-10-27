package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/api"
)

// The real product HTTP handler and conversation service are deliberately
// included here. A hand-written JSON double alone could drift into expecting
// only the final model turn or using the wrong persisted tool block shape.
func TestToolProofConsumesActualProductHTTPResponse(t *testing.T) {
	service, err := conversation.NewService(conversation.Config{Store: conversation.NewMemoryStore(nil), Caller: productCaller{}, Env: agent.Env{AllowMutations: true}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api.RegisterAgentConversationHandlers(mux, api.AgentConversationHandlerDeps{Service: service, PrincipalFromRequest: func(*http.Request) string { return "usr_qa" }})
	server := httptest.NewServer(mux)
	defer server.Close()
	r := newRunner(config{values: map[string]string{"QA_API_BASE": server.URL, "QA_PROJECT_ID": "prj_qa", "QA_SERVICE_ID": "svc_qa", "MODEL": "fixture-model"}}, &receipt{})
	attempt := conversationAttempt{key: "qa-contract", body: map[string]any{"project_id": "prj_qa", "service_id": "svc_qa", "model": "fixture-model"}}
	if err := r.createConversation(context.Background(), &attempt); err != nil {
		t.Fatal(err)
	}
	turn, err := r.send(context.Background(), attempt.id, "Read exact service", []string{"get_service"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTool(turn, "get_service", func(input map[string]any) bool { return input["service_id"] == "svc_qa" }); err != nil {
		t.Fatal(err)
	}
	if len(turn.Assistant.Content) != 3 {
		t.Fatal("product must return tool call, result, and final text in persisted assistant_message")
	}
}

type productCaller struct{}

func (productCaller) Send(context.Context, agent.CallRequest) (agent.CallResult, error) {
	return agent.CallResult{}, errors.New("unused")
}
func (productCaller) Stream(ctx context.Context, request agent.CallRequest, out chan<- agent.StreamEvent) error {
	defer close(out)
	if request.Env.AllowMutations || len(request.AllowedTools) != 1 || request.AllowedTools[0] != "get_service" {
		return errors.New("unexpected actual product authority")
	}
	turn := fixtureTurn("get_service", map[string]any{"service_id": "svc_qa"})
	for _, event := range []agent.StreamEvent{{Kind: agent.EventToolCall, ToolCall: turn.Assistant.Content[0].ToolUse}, {Kind: agent.EventToolResult, ToolResult: turn.Assistant.Content[1].ToolResult}, {Kind: agent.EventTextDelta, Delta: "Observed service."}, {Kind: agent.EventDone, Usage: turn.Assistant.Usage, StopReason: "end_turn"}} {
		select {
		case out <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
