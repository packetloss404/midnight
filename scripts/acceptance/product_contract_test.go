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
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
)

// This deliberately unprotected handler fixture proves only the DTO/service
// shape, not canonical router authentication. The test below covers admission.
func TestToolProofConsumesConversationHandlerResponseShape(t *testing.T) {
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

type productCaller struct{ requireAdmin bool }

func (productCaller) Send(context.Context, agent.CallRequest) (agent.CallResult, error) {
	return agent.CallResult{}, errors.New("unused")
}
func (c productCaller) Stream(ctx context.Context, request agent.CallRequest, out chan<- agent.StreamEvent) error {
	defer close(out)
	if c.requireAdmin && !api.IsInstanceAdminRequest(ctx) {
		return errors.New("canonical administrator identity context missing")
	}
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

func TestCanonicalConversationRouterRequiresAdministratorHuman(t *testing.T) {
	ctx := context.Background()
	hash, err := bootstrap.HashPassword("fixture-correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	store := identity.NewMemoryStore(
		identity.UserRecord{ID: "usr_admin", Email: "admin@example.invalid", InstanceAdmin: true, PasswordHash: hash, SessionGeneration: 1},
		identity.UserRecord{ID: "usr_tenant", Email: "tenant@example.invalid", PasswordHash: hash, SessionGeneration: 1},
	)
	sessions, err := identity.NewManager(identity.ManagerConfig{Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sessions.Login(ctx, identity.LoginRequest{Email: "admin@example.invalid", Password: "fixture-correct-horse-battery", ClientKind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := sessions.Login(ctx, identity.LoginRequest{Email: "tenant@example.invalid", Password: "fixture-correct-horse-battery", ClientKind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	keyProvider, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewRouterWithDataStack(api.DataStack{APIBearerToken: "fixture-operator-credential", ProductionAuthRequired: true, IdentitySessions: sessions, VariableStore: secrets.NewVault(keyProvider, secrets.DenyByDefault{}), ConversationStore: conversation.NewMemoryStore(nil), AgentCaller: productCaller{requireAdmin: true}, AgentChatModel: "fixture-model"})
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, tc := range []struct {
		name, token string
		want        int
	}{{"anonymous", "", 401}, {"ordinary-valid-human", tenant.AccessToken, 403}, {"administrator-valid-human", admin.AccessToken, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", server.URL+api.AgentConversationsPrefix+"?limit=1", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("canonical conversation admission=%d want%d", resp.StatusCode, tc.want)
			}
		})
	}
	r := newRunner(config{values: map[string]string{"QA_API_BASE": server.URL, "QA_PROJECT_ID": "prj_qa", "QA_SERVICE_ID": "svc_qa", "MODEL": "fixture-model"}}, &receipt{})
	r.token = admin.AccessToken
	attempt := conversationAttempt{key: "qa-canonical-contract", body: map[string]any{"project_id": "prj_qa", "service_id": "svc_qa", "model": "fixture-model"}}
	if err := r.createConversation(ctx, &attempt); err != nil {
		t.Fatal(err)
	}
	turn, err := r.send(ctx, attempt.id, "Read exact QA service", []string{"get_service"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTool(turn, "get_service", func(input map[string]any) bool { return input["service_id"] == "svc_qa" }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token string
		want  int
	}{{tenant.AccessToken, 403}, {admin.AccessToken, 200}} {
		req, _ := http.NewRequest("GET", server.URL+api.AgentConversationsPrefix+"/"+attempt.id, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("canonical stored conversation read=%d want%d", response.StatusCode, tc.want)
		}
	}
}
