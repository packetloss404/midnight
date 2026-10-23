package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestAgentConversationCursorIsSignedBoundAndExpires(t *testing.T) {
	codec := NewCursorCodec([]byte("agent-conversation-cursor-secret"))
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	want := conversation.ConversationListPosition{CreatedAt: now.Add(-time.Minute), ID: "conv_a"}
	raw, err := codec.encodeAgentConversationCursor(want, "binding-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "ac1.") {
		t.Fatalf("cursor = %q, want signed ac1 prefix", raw)
	}
	got, err := codec.decodeAgentConversationCursor(raw, "binding-a", now.Add(time.Minute))
	if err != nil || got != want {
		t.Fatalf("decode = %#v, %v, want %#v", got, err, want)
	}
	for name, test := range map[string]struct {
		candidate string
		binding   string
		at        time.Time
	}{
		"tampered":    {raw[:len(raw)-1] + "x", "binding-a", now},
		"cross-query": {raw, "binding-b", now},
		"expired":     {raw, "binding-a", now.Add(agentConversationCursorTTL)},
	} {
		if _, err := codec.decodeAgentConversationCursor(test.candidate, test.binding, test.at); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
			t.Fatalf("%s decode error = %v, want invalid cursor", name, err)
		}
	}
}

func TestAgentCursorBindingIncludesResolvedTenantAndActor(t *testing.T) {
	request := httptest.NewRequest("GET", AgentConversationsPrefix+"?limit=50", nil)
	filter := conversation.ListFilter{ProjectID: "prj_a", Limit: 50}
	binding := func(actor, tenant string) string {
		ctx := idempotency.WithActor(context.Background(), idempotency.Actor(actor))
		ctx = idempotency.WithTenant(ctx, idempotency.Tenant(tenant))
		return agentConversationListBinding(request.WithContext(ctx), actor, filter, "created_at.desc,id.desc", 50)
	}
	base := binding("usr_a", "workspace:a")
	if base == binding("usr_a", "workspace:b") {
		t.Fatal("cursor binding does not distinguish tenants")
	}
	if base == binding("usr_b", "workspace:a") {
		t.Fatal("cursor binding does not distinguish authenticated actors")
	}
}

func TestAgentMessageCursorIsSignedBoundAndKeylessCompatibilityIsExplicit(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	codec := NewCursorCodec([]byte("agent-message-cursor-secret"))
	raw, err := codec.encodeAgentMessageCursor(42, "binding-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "am1.") {
		t.Fatalf("cursor = %q, want signed am1 prefix", raw)
	}
	if got, err := codec.decodeAgentMessageCursor(raw, "binding-a", now); err != nil || got != 42 {
		t.Fatalf("decode = %d, %v, want 42", got, err)
	}

	keyless := NewCursorCodec(nil)
	unsigned, err := keyless.encodeAgentMessageCursor(7, "dev-binding", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(unsigned, "amu1.") {
		t.Fatalf("unsigned cursor = %q", unsigned)
	}
	if _, err := codec.decodeAgentMessageCursor(unsigned, "dev-binding", now); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("signed codec accepted unsigned cursor: %v", err)
	}
}
