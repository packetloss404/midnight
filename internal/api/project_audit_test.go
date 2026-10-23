// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestProjectAuditEventsAreTenantScopedPaginatedAndViewerReadable(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	chain := audit.NewMemoryChain()
	stack.AuditChain = chain
	stack.CursorCodec = NewCursorCodec([]byte("project-audit-pagination-secret"))
	now := time.Date(2026, 8, 30, 18, 0, 0, 0, time.UTC)
	for _, row := range []audit.Row{
		{ProjectID: "prj_a", ActorID: "usr_alice", ActorType: "user", ResourceKind: "service", ResourceID: "svc_a", Action: "service.created", RequestID: "req_a1", OccurredAt: now},
		{ProjectID: "prj_b", ActorID: "usr_carol", ActorType: "user", ResourceKind: "service", ResourceID: "svc_b", Action: "secret.other-project", RequestID: "req_b", OccurredAt: now.Add(time.Minute)},
		{ProjectID: "prj_a", ActorID: "usr_bob", ActorType: "user", ResourceKind: "deployment", ResourceID: "dep_a", Action: "deployment.created", RequestID: "req_a2", OccurredAt: now.Add(2 * time.Minute), PolicyDecision: "allowed", SourceIP: "192.0.2.44", After: map[string]any{"credential": "must-not-render"}},
	} {
		if _, err := chain.Append(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, manager, "alice@example.com")
	bob := loginIdentityForTest(t, manager, "bob@example.com")
	carol := loginIdentityForTest(t, manager, "carol@example.com")

	first := authenticatedIdentityRequest(router, http.MethodGet, v1alphaPrefix+"/projects/prj_a/audit/events?limit=1", alice)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", first.Code, first.Body.String())
	}
	var firstPage projectAuditEventPage
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if firstPage.ProjectID != "prj_a" || len(firstPage.Events) != 1 ||
		firstPage.Events[0].Action != "deployment.created" || firstPage.NextCursor == nil ||
		!firstPage.Truncated || strings.Contains(first.Body.String(), "secret.other-project") ||
		strings.Contains(first.Body.String(), "must-not-render") || strings.Contains(first.Body.String(), "192.0.2.44") {
		t.Fatalf("first page=%#v body=%s", firstPage, first.Body.String())
	}

	second := authenticatedIdentityRequest(
		router, http.MethodGet,
		v1alphaPrefix+"/projects/prj_a/audit/events?limit=1&cursor="+*firstPage.NextCursor,
		bob,
	)
	if second.Code != http.StatusOK {
		t.Fatalf("viewer second page status=%d body=%s", second.Code, second.Body.String())
	}
	var secondPage projectAuditEventPage
	if err := json.Unmarshal(second.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Events) != 1 || secondPage.Events[0].Action != "service.created" ||
		secondPage.NextCursor != nil || secondPage.Truncated {
		t.Fatalf("second page=%#v", secondPage)
	}

	for _, denied := range []struct {
		path  string
		token string
	}{
		{path: v1alphaPrefix + "/projects/prj_b/audit/events", token: alice},
		{path: v1alphaPrefix + "/projects/prj_a/audit/events", token: carol},
	} {
		response := authenticatedIdentityRequest(router, http.MethodGet, denied.path, denied.token)
		if response.Code != http.StatusNotFound ||
			!strings.Contains(response.Body.String(), `"code":"`+string(failurecodes.ResourceNotFound)+`"`) ||
			strings.Contains(response.Body.String(), "secret.other-project") {
			t.Fatalf("denied response status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestProjectAuditCursorIsSignedAndProjectBound(t *testing.T) {
	codec := NewCursorCodec([]byte("project-audit-cursor-secret"))
	token, err := codec.encodeProjectAuditCursor("prj_a", 42)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := codec.decodeProjectAuditCursor(token, "prj_a"); err != nil || got != 42 {
		t.Fatalf("round trip got=%d err=%v", got, err)
	}
	if _, err := codec.decodeProjectAuditCursor(token, "prj_b"); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("cross-project decode err=%v", err)
	}
	tampered := token[:len(token)-1] + "x"
	if _, err := codec.decodeProjectAuditCursor(tampered, "prj_a"); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("tampered decode err=%v", err)
	}
	if _, err := codec.decodeProjectAuditCursor(strings.Repeat("x", projectAuditCursorMaxSize+1), "prj_a"); !errors.Is(err, orailtypes.ErrInvalidCursorPage) {
		t.Fatalf("oversized decode err=%v", err)
	}
}
