//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

// The upstream Caller alone is a local double. Authentication, memberships,
// private conversation pages, messages, tool adapters/coordinators and the
// canonical HTTP router all use real PostgreSQL production implementations.
func TestTenantConversationOwnershipRolesToolsAndRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	if os.Getenv("MIDNIGHT_TESTCONTAINERS_DIRECT_IP") == "1" { // not-a-failure-code
		ip, err := container.ContainerIP(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Host = net.JoinHostPort(ip, "5432")
		dsn = parsed.String()
	}
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	password := "tenant chat test correct horse battery staple"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"owner", "editor", "viewer", "peer", "outsider", "admin"} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,is_admin,session_generation) VALUES($1,$2,$3,$4,$5,1)`, "usr_chat_"+id, id+"-chat@example.test", id, hash, id == "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO workspaces(id,slug,display_name) VALUES('wrk_chat_a','chat-a','Chat A'),('wrk_chat_b','chat-b','Chat B');
INSERT INTO projects(id,workspace_id,slug,display_name) VALUES('prj_chat_a','wrk_chat_a','a','A'),('prj_chat_b','wrk_chat_b','b','B');
INSERT INTO environments(id,project_id,slug,name,access_restricted) VALUES('env_chat_a','prj_chat_a','main','Main',FALSE),('env_chat_alt','prj_chat_a','alt','Alt',FALSE),('env_chat_private','prj_chat_a','private','Private',TRUE),('env_chat_b','prj_chat_b','main','Main',FALSE);
INSERT INTO services(id,project_id,slug,display_name) VALUES('svc_chat_a','prj_chat_a','web','Web'),('svc_chat_peer','prj_chat_a','other','Other'),('svc_chat_b','prj_chat_b','web','Web');
INSERT INTO project_members(project_id,user_id,role,accepted_at) VALUES('prj_chat_a','usr_chat_owner','owner',CURRENT_TIMESTAMP),('prj_chat_a','usr_chat_editor','editor',CURRENT_TIMESTAMP),('prj_chat_a','usr_chat_viewer','viewer',CURRENT_TIMESTAMP),('prj_chat_a','usr_chat_peer','editor',CURRENT_TIMESTAMP),('prj_chat_b','usr_chat_outsider','owner',CURRENT_TIMESTAMP);
INSERT INTO operations(id,kind,state,code,expires_at) VALUES('op_chat_a','deploy','FAILED','RECONCILE_STEP_FAILED',CURRENT_TIMESTAMP+INTERVAL '1 day'),('op_chat_alt','deploy','FAILED','RECONCILE_STEP_FAILED',CURRENT_TIMESTAMP+INTERVAL '1 day'),('op_chat_b','deploy','FAILED','RECONCILE_STEP_FAILED',CURRENT_TIMESTAMP+INTERVAL '1 day');
INSERT INTO deployments(id,project_id,service_id,environment_id,operation_id,action,state) VALUES('dep_chat_a','prj_chat_a','svc_chat_a','env_chat_a','op_chat_a','deploy_image','queued'),('dep_chat_alt','prj_chat_a','svc_chat_a','env_chat_alt','op_chat_alt','deploy_image','queued'),('dep_chat_b','prj_chat_b','svc_chat_b','env_chat_b','op_chat_b','deploy_image','queued');`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='failed',failure_code='RECONCILE_STEP_FAILED' WHERE id IN ('dep_chat_a','dep_chat_alt'); UPDATE operations SET state='RUNNING',code='' WHERE id='op_chat_alt'`); err != nil {
		t.Fatal(err)
	}
	stack := postgresIdentityIntegrationStack(t, pool, now)
	stack.APIBearerToken = "qa-tenant-chat-operator-recovery"
	captured := make(chan agent.Env, 32)
	started := make(chan struct{}, 1)
	stack.AgentCaller = tenantConversationCaller{captured: captured, started: started}
	stack.AgentMutationGate = tenantChatOpenGate{}
	stack.ConversationStore = conversation.NewPostgresStore(pool)
	stack.ConversationScopeAuthorizer = NewTenantConversationPolicy(stack)
	handler := NewRouterWithDataStack(stack)
	tokens := map[string]string{}
	for _, id := range []string{"owner", "editor", "viewer", "peer", "outsider", "admin"} {
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: id + "-chat@example.test", Password: password, ClientKind: "api", RemoteAddr: "192.0.2.9:443"})
		if err != nil {
			t.Fatal(err)
		}
		tokens[id] = result.AccessToken
	}
	request := func(actor, method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token := tokens[actor]; token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Midnight-Actor-Id", "user:usr_chat_owner") // never an authority source
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s as %s: status=%d want=%d body=%s", method, path, actor, response.Code, want, response.Body.String())
		}
		return response.Body.Bytes()
	}
	create := func(actor string) conversation.Conversation {
		t.Helper()
		var conv conversation.Conversation
		body := request(actor, "POST", AgentConversationsPrefix, `{"project_id":"prj_chat_a","service_id":"svc_chat_a","environment_id":"env_chat_a","metadata":{"midnight.kind":"diagnosis","midnight.workspace_id":"wrk_chat_b"}}`, http.StatusCreated)
		if err := json.Unmarshal(body, &conv); err != nil {
			t.Fatal(err)
		}
		if conv.ActorID != "user:usr_chat_"+actor || conv.Metadata["midnight.kind"] != "chat" || conv.Metadata["midnight.workspace_id"] != "wrk_chat_a" {
			t.Fatalf("caller forged authoritative metadata: %#v", conv)
		}
		return conv
	}
	owner, editor, viewer, peer := create("owner"), create("editor"), create("viewer"), create("peer")
	request("viewer", "POST", AgentConversationsPrefix, `{}`, http.StatusForbidden)
	request("viewer", "POST", AgentConversationsPrefix, `{"project_id":"prj_chat_a","service_id":"svc_chat_b"}`, http.StatusForbidden)
	request("viewer", "POST", AgentConversationsPrefix, `{"project_id":"prj_chat_a","environment_id":"env_chat_private"}`, http.StatusForbidden)
	request("peer", "GET", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusNotFound)
	request("outsider", "GET", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusNotFound)
	request("peer", "GET", AgentConversationsPrefix+"/"+owner.ID+"/messages", "", http.StatusNotFound)
	request("peer", "POST", AgentConversationsPrefix+"/"+owner.ID+"/messages", `{"content":"steal"}`, http.StatusNotFound)
	request("peer", "DELETE", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusNotFound)
	request("admin", "GET", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusOK)
	request("", "GET", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusUnauthorized)
	// Save the same immutable system report the worker produces, including
	// its durable operation attachment, then test deliberate shared reads.
	diagnosis, err := stack.ConversationStore.CreateConversation(ctx, conversation.Conversation{ProjectID: "prj_chat_a", ServiceID: "svc_chat_a", ActorID: "agent:diagnosis", Metadata: map[string]any{"midnight.kind": "diagnosis", "midnight.workspace_id": "wrk_chat_a", "midnight.project_id": "prj_chat_a", "midnight.service_id": "svc_chat_a", "midnight.environment_id": "env_chat_a", "midnight.deployment_id": "dep_chat_a", "midnight.operation_id": "op_chat_a"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.ConversationStore.AppendMessage(ctx, conversation.Message{ConversationID: diagnosis.ID, Role: agent.RoleAssistant, Content: []agent.ContentBlock{{Kind: agent.BlockText, Text: "diagnosis report"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stack.OperationStore.AppendEvent(ctx, OperationEvent{OperationID: "op_chat_a", State: "FAILED", Message: "deploy:diagnosis_attached diagnosis_id=" + diagnosis.ID}); err != nil {
		t.Fatal(err)
	}
	request("viewer", "GET", AgentConversationsPrefix+"/"+diagnosis.ID+"/messages", "", http.StatusOK)
	request("viewer", "POST", AgentConversationsPrefix+"/"+diagnosis.ID+"/messages", `{"content":"edit report"}`, http.StatusForbidden)
	request("owner", "DELETE", AgentConversationsPrefix+"/"+diagnosis.ID, "", http.StatusForbidden)
	request("outsider", "GET", AgentConversationsPrefix+"/"+diagnosis.ID, "", http.StatusForbidden)
	forged, err := stack.ConversationStore.CreateConversation(ctx, conversation.Conversation{ProjectID: "prj_chat_a", ServiceID: "svc_chat_a", ActorID: "agent:diagnosis", Metadata: map[string]any{"midnight.kind": "diagnosis", "midnight.workspace_id": "wrk_chat_a", "midnight.project_id": "prj_chat_a", "midnight.service_id": "svc_chat_a", "midnight.environment_id": "env_chat_alt", "midnight.deployment_id": "dep_chat_alt", "midnight.operation_id": "op_chat_alt"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stack.OperationStore.AppendEvent(ctx, OperationEvent{OperationID: "op_chat_alt", State: "FAILED", Message: "deploy:diagnosis_attached diagnosis_id=" + forged.ID}); err != nil {
		t.Fatal(err)
	}
	request("viewer", "GET", AgentConversationsPrefix+"/"+forged.ID, "", http.StatusForbidden)
	// Every owner-filtered SQL page contains only this caller and approved
	// shared reports, with an actor-bound cursor that cannot cross users.
	path := AgentConversationsPrefix + "?project_id=prj_chat_a&limit=1"
	seen := map[string]bool{}
	var firstCursor string
	for path != "" {
		var page conversationsPage
		if err := json.Unmarshal(request("viewer", "GET", path, "", http.StatusOK), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Data {
			if item.ID != viewer.ID && item.ID != diagnosis.ID {
				t.Fatalf("private chat leaked into page: %s", item.ID)
			}
			seen[item.ID] = true
		}
		path = ""
		if page.Page.NextCursor != "" {
			if firstCursor == "" {
				firstCursor = page.Page.NextCursor
			}
			path = AgentConversationsPrefix + "?project_id=prj_chat_a&limit=1&cursor=" + url.QueryEscape(page.Page.NextCursor)
		}
	}
	if !seen[viewer.ID] || !seen[diagnosis.ID] || len(seen) != 2 {
		t.Fatalf("visible pages=%v", seen)
	}
	request("peer", "GET", AgentConversationsPrefix+"?project_id=prj_chat_a&limit=1&cursor="+url.QueryEscape(firstCursor), "", http.StatusBadRequest)
	request("viewer", "GET", AgentConversationsPrefix+"?project_id=prj_chat_a&actor_id=user:usr_chat_owner", "", http.StatusForbidden)
	send := func(actor string, conv conversation.Conversation) agent.Env {
		t.Helper()
		request(actor, "POST", AgentConversationsPrefix+"/"+conv.ID+"/messages", `{"content":"check tools","allow_mutations":true}`, http.StatusOK)
		return <-captured
	}
	viewerEnv, editorEnv := send("viewer", viewer), send("editor", editor)
	if viewerEnv.AllowMutations || !editorEnv.AllowMutations {
		t.Fatal("viewer/editor model mutation capability is wrong")
	}
	registry := tools.NewDefaultRegistry()
	for _, attack := range []struct{ name, input string }{
		{"list_services", `{"project_id":"prj_chat_b"}`},
		{"get_service", `{"service_id":"svc_chat_peer"}`},
		{"get_service", `{"service_id":"svc_chat_b"}`},
		{"get_deployment", `{"deployment_id":"dep_chat_alt"}`},
		{"get_deployment", `{"deployment_id":"dep_chat_b"}`},
		{"get_logs", `{"deployment_id":"op_chat_b"}`},
		{"set_variable", `{"service_id":"svc_chat_a","environment_id":"env_chat_alt","name":"QA_VALUE","value":"denied"}`},
	} {
		if _, err := registry.Execute(context.Background(), attack.name, json.RawMessage(attack.input), editorEnv); err == nil {
			t.Fatalf("tool escaped bound scope: %s", attack.name)
		}
	}
	if _, err := registry.Execute(ctx, "get_service", json.RawMessage(`{"service_id":"svc_chat_a"}`), viewerEnv); err != nil {
		t.Fatalf("viewer read tool refused: %v", err)
	}
	if _, err := registry.Execute(ctx, "set_variable", json.RawMessage(`{"service_id":"svc_chat_a","environment_id":"env_chat_a","name":"QA_VALUE","value":"denied"}`), viewerEnv); !errors.Is(err, agent.ErrMutationsDisabled) {
		t.Fatalf("viewer mutation=%v", err)
	}
	if _, err := registry.Execute(ctx, "set_variable", json.RawMessage(`{"service_id":"svc_chat_a","environment_id":"env_chat_a","name":"QA_VALUE","value":"accepted-fixture"}`), editorEnv); err != nil {
		t.Fatalf("editor tenant mutation refused: %v", err)
	}
	var variables int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE service_id='svc_chat_a' AND environment_id='env_chat_a' AND name='QA_VALUE' AND deleted_at IS NULL`).Scan(&variables); err != nil || variables != 1 {
		t.Fatalf("durable tool mutation count=%d err=%v", variables, err)
	}
	// Removing a membership invalidates old captured tool handles as well as
	// new resource/page reads; erasing context never restores operator rights.
	if _, err := pool.Exec(ctx, `DELETE FROM project_members WHERE user_id='usr_chat_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := viewerEnv.Services.GetService(context.Background(), "svc_chat_a"); err == nil {
		t.Fatal("captured handle survived membership revocation")
	}
	request("viewer", "GET", AgentConversationsPrefix+"/"+viewer.ID, "", http.StatusForbidden)
	request("viewer", "GET", AgentConversationsPrefix+"?project_id=prj_chat_a", "", http.StatusForbidden)
	// A long-running provider turn is canceled after live membership loss.
	streamServer := httptest.NewServer(handler)
	defer streamServer.Close()
	streamRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, streamServer.URL+AgentConversationsPrefix+"/"+peer.ID+"/stream", nil)
	streamRequest.Header.Set("Authorization", "Bearer "+tokens["peer"])
	streamResponse, err := streamServer.Client().Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		t.Fatalf("stream handshake status=%d", streamResponse.StatusCode)
	}
	streamClosed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, streamResponse.Body); close(streamClosed) }()
	result := make(chan int, 1)
	go func() {
		req := httptest.NewRequest("POST", AgentConversationsPrefix+"/"+peer.ID+"/messages", strings.NewReader(`{"content":"block-until-revoked"}`))
		req.Header.Set("Authorization", "Bearer "+tokens["peer"])
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		result <- recorder.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider turn did not start")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_members WHERE user_id='usr_chat_peer'`); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != http.StatusForbidden {
			t.Fatalf("revoked send status=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revoked in-flight turn did not stop")
	}
	select {
	case <-streamClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked SSE reader retained access")
	}
	request("owner", "DELETE", AgentConversationsPrefix+"/"+owner.ID, "", http.StatusNoContent)
	if _, err := pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE user_id='usr_chat_editor'`); err != nil {
		t.Fatal(err)
	}
	if _, err := editorEnv.Services.GetService(context.Background(), "svc_chat_a"); err == nil {
		t.Fatal("captured tool handle survived session revocation")
	}
	request("editor", "GET", AgentConversationsPrefix+"/"+editor.ID, "", http.StatusUnauthorized)
}

type tenantConversationCaller struct {
	captured chan agent.Env
	started  chan struct{}
}

func (c tenantConversationCaller) Send(context.Context, agent.CallRequest) (agent.CallResult, error) {
	return agent.CallResult{}, fmt.Errorf("unexpected nonstream call")
}
func (c tenantConversationCaller) Stream(ctx context.Context, request agent.CallRequest, out chan<- agent.StreamEvent) error {
	defer close(out)
	last := request.Conversation[len(request.Conversation)-1]
	if strings.Contains(last.Content[0].Text, "block-until-revoked") {
		c.started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	c.captured <- request.Env
	out <- agent.StreamEvent{Kind: agent.EventTextDelta, Delta: "local caller double"}
	out <- agent.StreamEvent{Kind: agent.EventDone, StopReason: "end_turn"}
	return nil
}

type tenantChatOpenGate struct{}

func (tenantChatOpenGate) AllowMutations(context.Context, agentauth.SessionContext) bool { return true }
