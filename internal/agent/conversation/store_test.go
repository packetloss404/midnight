// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package conversation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/db/pg"
	migrationsfs "github.com/midnight/midnight/migrations"
)

func TestMemoryStoreCRUD(t *testing.T) {
	t.Parallel()

	clock := newDeterministicClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	store := NewMemoryStore(clock.now)
	ctx := context.Background()

	conv, err := store.CreateConversation(ctx, Conversation{
		ProjectID: "prj_demo",
		ServiceID: "svc_demo",
		ActorID:   "usr_alice",
		Title:     "first turn",
		Model:     "claude-3-5-sonnet-latest",
		Metadata:  map[string]any{"source": "test"},
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if !strings.HasPrefix(conv.ID, ConversationIDPrefix+"_") {
		t.Fatalf("conv id %q must use %s_ prefix", conv.ID, ConversationIDPrefix)
	}
	if conv.CreatedAt.IsZero() || conv.UpdatedAt.IsZero() {
		t.Fatalf("CreateConversation must set timestamps")
	}

	got, err := store.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Title != "first turn" {
		t.Fatalf("GetConversation title = %q, want %q", got.Title, "first turn")
	}

	clock.advance(time.Second)
	user, err := store.AppendMessage(ctx, Message{
		ConversationID: conv.ID,
		Role:           agent.RoleUser,
		Content:        []agent.ContentBlock{{Kind: agent.BlockText, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("AppendMessage user: %v", err)
	}
	if user.Seq != 1 {
		t.Fatalf("user seq = %d, want 1", user.Seq)
	}
	if !strings.HasPrefix(user.ID, MessageIDPrefix+"_") {
		t.Fatalf("user id %q must use %s_ prefix", user.ID, MessageIDPrefix)
	}

	clock.advance(time.Second)
	assistant, err := store.AppendMessage(ctx, Message{
		ConversationID: conv.ID,
		Role:           agent.RoleAssistant,
		Content: []agent.ContentBlock{
			{Kind: agent.BlockText, Text: "hello"},
			{Kind: agent.BlockToolUse, ToolUse: &agent.ToolCall{ID: "tu_1", Name: "echo", Input: []byte(`{"x":1}`)}},
		},
		Usage: &agent.Usage{InputTokens: 12, OutputTokens: 8},
	})
	if err != nil {
		t.Fatalf("AppendMessage assistant: %v", err)
	}
	if assistant.Seq != 2 {
		t.Fatalf("assistant seq = %d, want 2", assistant.Seq)
	}
	if assistant.Usage == nil || assistant.Usage.InputTokens != 12 {
		t.Fatalf("assistant usage round-trip failed: %+v", assistant.Usage)
	}

	page, err := store.ListMessages(ctx, conv.ID, MessageListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListMessages len = %d, want 2", len(page.Items))
	}
	if page.Items[0].Seq != 1 || page.Items[1].Seq != 2 {
		t.Fatalf("ListMessages order broken: %+v", page.Items)
	}

	listed, err := store.ListConversations(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != conv.ID {
		t.Fatalf("ListConversations = %+v", listed)
	}

	conv.Title = "renamed"
	if _, err := store.UpdateConversation(ctx, conv); err != nil {
		t.Fatalf("UpdateConversation: %v", err)
	}
	got, _ = store.GetConversation(ctx, conv.ID)
	if got.Title != "renamed" {
		t.Fatalf("Update did not stick: %q", got.Title)
	}

	if err := store.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := store.GetConversation(ctx, conv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}

	if err := store.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatalf("Delete is not idempotent: %v", err)
	}
}

func TestMemoryStoreListFiltering(t *testing.T) {
	t.Parallel()
	clock := newDeterministicClock(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	store := NewMemoryStore(clock.now)
	ctx := context.Background()

	mustCreate := func(t *testing.T, project, service string) Conversation {
		t.Helper()
		c, err := store.CreateConversation(ctx, Conversation{ProjectID: project, ServiceID: service, ActorID: "usr_a"})
		if err != nil {
			t.Fatalf("CreateConversation: %v", err)
		}
		clock.advance(time.Millisecond)
		return c
	}

	a := mustCreate(t, "prj_1", "")
	b := mustCreate(t, "prj_2", "svc_1")
	c := mustCreate(t, "prj_2", "")

	filtered, err := store.ListConversations(ctx, ListFilter{ProjectID: "prj_2"})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(filtered.Items) != 2 {
		t.Fatalf("len = %d, want 2 (b,c); items=%+v", len(filtered.Items), filtered.Items)
	}
	if filtered.Items[0].ID != c.ID || filtered.Items[1].ID != b.ID {
		t.Fatalf("expected newest-first ordering c,b; got %s,%s", filtered.Items[0].ID, filtered.Items[1].ID)
	}

	filtered, _ = store.ListConversations(ctx, ListFilter{ServiceID: "svc_1"})
	if len(filtered.Items) != 1 || filtered.Items[0].ID != b.ID {
		t.Fatalf("ServiceID filter expected b; got %+v", filtered.Items)
	}

	_ = a // referenced for ordering invariants
}

func TestMemoryStoreConversationKeysetIsStableAcrossUpdates(t *testing.T) {
	t.Parallel()
	clock := newDeterministicClock(time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	store := NewMemoryStore(clock.now)
	ctx := context.Background()

	var created []Conversation
	for _, id := range []string{"conv_a", "conv_b", "conv_c"} {
		item, err := store.CreateConversation(ctx, Conversation{ID: id, ActorID: "usr_a"})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, item)
		clock.advance(time.Second)
	}
	first, err := store.ListConversations(ctx, ListFilter{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "conv_c" || first.Next == nil {
		t.Fatalf("first page = %#v, error = %v", first, err)
	}
	// Touching a not-yet-returned conversation must not move it across the
	// immutable created_at cursor boundary.
	if _, err := store.UpdateConversation(ctx, Conversation{ID: "conv_b", Title: "touched"}); err != nil {
		t.Fatal(err)
	}
	second, err := store.ListConversations(ctx, ListFilter{Limit: 1, After: first.Next})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "conv_b" || second.Next == nil {
		t.Fatalf("second page = %#v, error = %v", second, err)
	}
	third, err := store.ListConversations(ctx, ListFilter{Limit: 1, After: second.Next})
	if err != nil || len(third.Items) != 1 || third.Items[0].ID != "conv_a" || third.Next != nil {
		t.Fatalf("third page = %#v, error = %v", third, err)
	}
	_ = created
}

func TestMemoryStoreMessageKeysetParityAndBounds(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	ctx := context.Background()
	conv, err := store.CreateConversation(ctx, Conversation{ID: "conv_page"})
	if err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 5; seq++ {
		if _, err := store.AppendMessage(ctx, Message{ConversationID: conv.ID, Seq: seq, Role: agent.RoleUser}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ListMessages(ctx, conv.ID, MessageListOptions{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.Items[0].Seq != 1 || first.Items[1].Seq != 2 || first.NextSeq == nil {
		t.Fatalf("ascending first = %#v, error = %v", first, err)
	}
	second, err := store.ListMessages(ctx, conv.ID, MessageListOptions{Limit: 2, AfterSeq: first.NextSeq})
	if err != nil || len(second.Items) != 2 || second.Items[0].Seq != 3 || second.Items[1].Seq != 4 {
		t.Fatalf("ascending second = %#v, error = %v", second, err)
	}
	reverse, err := store.ListMessages(ctx, conv.ID, MessageListOptions{Limit: 2, Reverse: true})
	if err != nil || len(reverse.Items) != 2 || reverse.Items[0].Seq != 5 || reverse.Items[1].Seq != 4 || reverse.NextSeq == nil {
		t.Fatalf("descending first = %#v, error = %v", reverse, err)
	}
	if _, err := store.ListMessages(ctx, conv.ID, MessageListOptions{Limit: MaxPageLimit + 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized limit error = %v, want ErrInvalid", err)
	}
}

func TestConversationListQueryUsesSargableFilterShapes(t *testing.T) {
	position := &ConversationListPosition{
		CreatedAt: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC),
		ID:        "conv_cursor",
	}
	query, args := conversationListQuery(ListFilter{
		ProjectID: "prj_a",
		ServiceID: "svc_a",
		ActorID:   "usr_a",
		After:     position,
	}, 51)
	for _, required := range []string{
		"project_id = $1",
		"service_id = $2",
		"actor_id = $3",
		"(created_at, id) < ($4, $5)",
		"ORDER BY created_at DESC, id DESC",
		"LIMIT $6",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	if strings.Contains(query, " OR ") {
		t.Fatalf("query uses optional-parameter OR predicates that inhibit index plans:\n%s", query)
	}
	if len(args) != 6 || args[0] != "prj_a" || args[1] != "svc_a" || args[2] != "usr_a" || args[4] != "conv_cursor" || args[5] != 51 {
		t.Fatalf("args = %#v", args)
	}

	query, args = conversationListQuery(ListFilter{}, 201)
	if strings.Contains(query, "WHERE") || !strings.Contains(query, "LIMIT $1") || len(args) != 1 || args[0] != 201 {
		t.Fatalf("unfiltered query = %q, args = %#v", query, args)
	}
}

func TestMessageListQueryUsesSargableKeysetShape(t *testing.T) {
	after := int64(42)
	query, args := messageListQuery("conv_a", &after, ">", "ASC", 51)
	for _, required := range []string{"conversation_id = $1", "seq > $2", "ORDER BY seq ASC", "LIMIT $3"} {
		if !strings.Contains(query, required) {
			t.Fatalf("query missing %q:\n%s", required, query)
		}
	}
	if strings.Contains(query, " OR ") || len(args) != 3 || args[0] != "conv_a" || args[1] != int64(42) || args[2] != 51 {
		t.Fatalf("query = %q, args = %#v", query, args)
	}

	query, args = messageListQuery("conv_a", nil, "<", "DESC", 201)
	if strings.Contains(query, "seq <") || !strings.Contains(query, "ORDER BY seq DESC") || !strings.Contains(query, "LIMIT $2") || len(args) != 2 {
		t.Fatalf("initial descending query = %q, args = %#v", query, args)
	}
}

func TestMemoryStoreAppendUnknownConversation(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore(nil)
	_, err := store.AppendMessage(context.Background(), Message{
		ConversationID: "conv_missing",
		Role:           agent.RoleUser,
		Content:        []agent.ContentBlock{{Kind: agent.BlockText, Text: "x"}},
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendMessage err = %v, want ErrNotFound", err)
	}
}

func TestPostgresStoreHappyPath(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MIDNIGHT_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("MIDNIGHT_TEST_PG_DSN not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	defer pool.Close()

	runner := &pg.MigrationRunner{Pool: pool, FS: migrationsfs.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrations up: %v", err)
	}

	store := NewPostgresStore(pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_conversations WHERE actor_id = 'pg_test'`)
	})

	conv, err := store.CreateConversation(ctx, Conversation{
		ActorID: "pg_test",
		Title:   "pg roundtrip",
		Model:   "claude-3-5-sonnet-latest",
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	user, err := store.AppendMessage(ctx, Message{
		ConversationID: conv.ID,
		Role:           agent.RoleUser,
		Content:        []agent.ContentBlock{{Kind: agent.BlockText, Text: "ping"}},
	})
	if err != nil {
		t.Fatalf("AppendMessage user: %v", err)
	}
	if user.Seq != 1 {
		t.Fatalf("user seq = %d, want 1", user.Seq)
	}
	asst, err := store.AppendMessage(ctx, Message{
		ConversationID: conv.ID,
		Role:           agent.RoleAssistant,
		Content:        []agent.ContentBlock{{Kind: agent.BlockText, Text: "pong"}},
		Usage:          &agent.Usage{InputTokens: 1, OutputTokens: 1},
	})
	if err != nil {
		t.Fatalf("AppendMessage asst: %v", err)
	}
	if asst.Seq != 2 {
		t.Fatalf("asst seq = %d, want 2", asst.Seq)
	}

	page, err := store.ListMessages(ctx, conv.ID, MessageListOptions{})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("ListMessages len = %d, want 2", len(page.Items))
	}
	if page.Items[1].Usage == nil || page.Items[1].Usage.OutputTokens != 1 {
		t.Fatalf("usage did not survive round trip: %+v", page.Items[1].Usage)
	}

	got, err := store.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Title != "pg roundtrip" {
		t.Fatalf("GetConversation title = %q", got.Title)
	}

	if err := store.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := store.GetConversation(ctx, conv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v, want ErrNotFound", err)
	}
}

// --- helpers ---------------------------------------------------------------

type deterministicClock struct {
	t time.Time
}

func newDeterministicClock(t time.Time) *deterministicClock { return &deterministicClock{t: t.UTC()} }

func (c *deterministicClock) now() time.Time {
	c.t = c.t.Add(time.Nanosecond)
	return c.t
}

func (c *deterministicClock) advance(d time.Duration) { c.t = c.t.Add(d) }
