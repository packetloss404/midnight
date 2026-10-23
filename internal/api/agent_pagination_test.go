package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/failurecodes"
)

func TestAgentConversationHTTPKeysetCursorBinding(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := conversation.NewMemoryStore(func() time.Time { return now })
	for _, item := range []struct {
		id      string
		project string
	}{
		{id: "conv_a", project: "prj_a"},
		{id: "conv_b", project: "prj_a"},
		{id: "conv_c", project: "prj_a"},
		{id: "conv_other", project: "prj_b"},
	} {
		if _, err := store.CreateConversation(ctx, conversation.Conversation{
			ID: item.id, ProjectID: item.project, ActorID: "usr_owner",
		}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	handler := newAgentPaginationTestHandler(t, store, func() time.Time { return now })

	status, body := serveAgentPagination(t, handler, AgentConversationsPrefix+"?limit=1&project_id=prj_a", "usr_reader")
	var first conversationsPage
	decodeAgentPaginationResponse(t, status, body, http.StatusOK, &first)
	if len(first.Data) != 1 || first.Data[0].ID != "conv_c" || first.Page.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}

	// updated_at is mutable; touching a not-yet-returned row must not move it
	// across the immutable created_at cursor boundary.
	now = now.Add(time.Minute)
	if _, err := store.UpdateConversation(ctx, conversation.Conversation{ID: "conv_b", Title: "touched"}); err != nil {
		t.Fatal(err)
	}
	page2URL := AgentConversationsPrefix + "?limit=1&project_id=prj_a&cursor=" + url.QueryEscape(first.Page.NextCursor)
	status, body = serveAgentPagination(t, handler, page2URL, "usr_reader")
	var second conversationsPage
	decodeAgentPaginationResponse(t, status, body, http.StatusOK, &second)
	if len(second.Data) != 1 || second.Data[0].ID != "conv_b" {
		t.Fatalf("second page = %#v", second)
	}

	for name, request := range map[string]struct {
		path  string
		actor string
	}{
		"raw offset":     {AgentConversationsPrefix + "?limit=1&project_id=prj_a&cursor=1", "usr_reader"},
		"changed limit":  {AgentConversationsPrefix + "?limit=2&project_id=prj_a&cursor=" + url.QueryEscape(first.Page.NextCursor), "usr_reader"},
		"changed filter": {AgentConversationsPrefix + "?limit=1&project_id=prj_b&cursor=" + url.QueryEscape(first.Page.NextCursor), "usr_reader"},
		"changed actor":  {page2URL, "usr_other"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := serveAgentPagination(t, handler, request.path, request.actor)
			assertAgentPaginationError(t, status, body, failurecodes.CursorInvalid)
		})
	}

	for _, path := range []string{
		AgentConversationsPrefix + "?limit=1&limit=2",
		AgentConversationsPrefix + "?project_id=",
		AgentConversationsPrefix + "?unexpected=x",
	} {
		status, body := serveAgentPagination(t, handler, path, "usr_reader")
		assertAgentPaginationError(t, status, body, failurecodes.CursorInvalid)
	}
	status, body = serveAgentPagination(t, handler, AgentConversationsPrefix+"?limit=201", "usr_reader")
	assertAgentPaginationError(t, status, body, failurecodes.PaginationLimitExceeded)
}

func TestAgentMessageHTTPKeysetCursorBindingAndOrder(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := conversation.NewMemoryStore(func() time.Time { return now })
	for _, id := range []string{"conv_messages", "conv_other"} {
		if _, err := store.CreateConversation(ctx, conversation.Conversation{ID: id, ActorID: "usr_owner"}); err != nil {
			t.Fatal(err)
		}
	}
	for seq := int64(1); seq <= 5; seq++ {
		if _, err := store.AppendMessage(ctx, conversation.Message{
			ConversationID: "conv_messages", Seq: seq, Role: agent.RoleUser,
		}); err != nil {
			t.Fatal(err)
		}
	}
	handler := newAgentPaginationTestHandler(t, store, func() time.Time { return now })
	base := AgentConversationsPrefix + "/conv_messages/messages"

	status, body := serveAgentPagination(t, handler, base+"?limit=2", "usr_reader")
	var first messagesPage
	decodeAgentPaginationResponse(t, status, body, http.StatusOK, &first)
	if len(first.Data) != 2 || first.Data[0].Seq != 1 || first.Data[1].Seq != 2 || first.Page.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	// Appends after page one remain reachable without offset drift.
	if _, err := store.AppendMessage(ctx, conversation.Message{ConversationID: "conv_messages", Seq: 6, Role: agent.RoleUser}); err != nil {
		t.Fatal(err)
	}
	nextURL := base + "?limit=2&cursor=" + url.QueryEscape(first.Page.NextCursor)
	status, body = serveAgentPagination(t, handler, nextURL, "usr_reader")
	var second messagesPage
	decodeAgentPaginationResponse(t, status, body, http.StatusOK, &second)
	if len(second.Data) != 2 || second.Data[0].Seq != 3 || second.Data[1].Seq != 4 {
		t.Fatalf("second page = %#v", second)
	}

	status, body = serveAgentPagination(t, handler, base+"?limit=2&order=desc", "usr_reader")
	var descending messagesPage
	decodeAgentPaginationResponse(t, status, body, http.StatusOK, &descending)
	if len(descending.Data) != 2 || descending.Data[0].Seq != 6 || descending.Data[1].Seq != 5 {
		t.Fatalf("descending page = %#v", descending)
	}

	for name, request := range map[string]struct {
		path  string
		actor string
	}{
		"changed order": {base + "?limit=2&order=desc&cursor=" + url.QueryEscape(first.Page.NextCursor), "usr_reader"},
		"changed limit": {base + "?limit=3&cursor=" + url.QueryEscape(first.Page.NextCursor), "usr_reader"},
		"conversation":  {AgentConversationsPrefix + "/conv_other/messages?limit=2&cursor=" + url.QueryEscape(first.Page.NextCursor), "usr_reader"},
		"actor":         {nextURL, "usr_other"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := serveAgentPagination(t, handler, request.path, request.actor)
			assertAgentPaginationError(t, status, body, failurecodes.CursorInvalid)
		})
	}
	status, body = serveAgentPagination(t, handler, base+"?order=newest", "usr_reader")
	assertAgentPaginationError(t, status, body, failurecodes.CursorInvalid)
}

func newAgentPaginationTestHandler(t *testing.T, store conversation.Store, now func() time.Time) http.Handler {
	t.Helper()
	service, err := conversation.NewService(conversation.Config{Store: store, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return agentConversationsHandler(AgentConversationHandlerDeps{
		Service: service, Now: now, CursorCodec: NewCursorCodec([]byte("agent-pagination-test-secret")),
		PrincipalFromRequest: func(r *http.Request) string {
			return r.Header.Get("X-Midnight-Actor-Id")
		},
	})
}

func serveAgentPagination(t *testing.T, handler http.Handler, path, actor string) (int, []byte) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Midnight-Actor-Id", actor)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func decodeAgentPaginationResponse(t *testing.T, status int, body []byte, wantStatus int, target any) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", status, wantStatus, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, body)
	}
}

func assertAgentPaginationError(t *testing.T, status int, body []byte, want failurecodes.Code) {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
	var response ErrorResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, body)
	}
	if response.Code != want {
		t.Fatalf("code = %s, want %s; body=%s", response.Code, want, body)
	}
}
