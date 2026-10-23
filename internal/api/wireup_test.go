// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/version"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/pkg/agentapi"
)

// TestRouterWiresAgentRegisterEndpoint asserts Lane 5 plumbed
// RegisterAgentHandlers into the v1alpha mux. The probe hits the
// register endpoint with a minimal payload and asserts a 2xx + the
// panel-locked response shape (agent_id + heartbeat_interval).
func TestRouterWiresAgentRegisterEndpoint(t *testing.T) {
	router := NewRouter()
	body, err := json.Marshal(agentapi.RegistrationRequest{
		AgentID:   "agent-wireup",
		Version:   version.Version,
		Hostname:  "wireup-host",
		OS:        "linux",
		Arch:      "amd64",
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, AgentRegisterPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+string(defaultAgentHMACSecret))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code < 200 || rec.Code >= 300 {
		t.Fatalf("register status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp agentapi.RegistrationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	if resp.AgentID != "agent-wireup" {
		t.Fatalf("agent id = %q, want agent-wireup", resp.AgentID)
	}
	if resp.HeartbeatInterval == 0 {
		t.Fatal("HeartbeatInterval is zero; default registry not seeded")
	}
}

// TestRouterWiresWebhookInboundEndpoint asserts Lane 5 plumbed
// RegisterWebhookHandlers into the v1alpha mux. The probe:
//
//  1. Sends a valid signed Midnight-incoming delivery and asserts a
//     202 Accepted response.
//  2. Resends the SAME delivery and asserts the panel-locked replayed
//     path (200 + Idempotent-Replayed: true).
//  3. Confirms the audit chain attached to the DataStack received both
//     events (one accepted + one replayed) so commit B's audit-emit
//     wiring works end-to-end against the live router.
func TestRouterWiresWebhookInboundEndpoint(t *testing.T) {
	endpoints := webhook.NewMemoryEndpointResolver()
	endpoints.Set("ep-test", webhook.EndpointConfig{
		Provider: webhook.ProviderMidnightIncoming,
		Keys:     []webhook.Key{{KID: "kid_test", Material: []byte("phase1-wireup-secret")}},
	})
	chain := audit.NewMemoryChain()
	now := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	router := NewRouterWithDataStack(DataStack{
		AuditChain:         chain,
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
		Clock:              clock,
	})

	body := []byte(`{"hello":"world"}`)
	signer := webhook.Signer{}
	sig, err := signer.SignBody([]webhook.Key{{KID: "kid_test", Material: []byte("phase1-wireup-secret")}}, body, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, WebhookRoutePrefix+"midnight-incoming/ep-test", bytes.NewReader(body))
		req.Header.Set("Midnight-Signature", sig)
		req.Header.Set("Midnight-Delivery-Id", "whd_wireup_001")
		req.Header.Set("Midnight-Event-Id", "evt_wireup_001")
		req.Header.Set("Midnight-Event-Type", "dev.midnight.deployment.queued")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	first := doPost()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d body=%s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"replayed":false`) {
		t.Fatalf("first delivery body = %s, want replayed:false", first.Body.String())
	}

	second := doPost()
	if second.Code != http.StatusOK {
		t.Fatalf("replayed delivery status = %d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replayed Idempotent-Replayed header = %q, want true", second.Header().Get("Idempotent-Replayed"))
	}

	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("audit chain rows = %d, want >= 2; rows=%#v", len(rows), rows)
	}
	var sawAccepted, sawReplayed bool
	for _, r := range rows {
		if r.ResourceKind != "webhook_delivery" {
			t.Fatalf("audit row resource_kind = %q, want webhook_delivery", r.ResourceKind)
		}
		if strings.HasSuffix(r.Action, ".accepted") {
			sawAccepted = true
		}
		if strings.HasSuffix(r.Action, ".replayed") {
			sawReplayed = true
		}
	}
	if !sawAccepted || !sawReplayed {
		t.Fatalf("audit chain missing outcomes accepted=%t replayed=%t rows=%#v", sawAccepted, sawReplayed, rows)
	}
}

// TestMutatingHandlersEmitAuditRows asserts Lane 5's audit-emit
// hooks fire on the success path of every mutating handler the
// router currently exposes (project create, service create,
// operation cancel). Each handler should produce exactly one
// audit row with the panel-locked Action token and resource ids.
func TestMutatingHandlersEmitAuditRows(t *testing.T) {
	chain := audit.NewMemoryChain()
	router := NewRouterWithDataStack(DataStack{AuditChain: chain})

	// 1. project create -> action "project.create".
	createProj := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"audited"}`))
	createProj.Header.Set("Idempotency-Key", "audit-create-prj")
	prjRR := httptest.NewRecorder()
	router.ServeHTTP(prjRR, createProj)
	if prjRR.Code != http.StatusCreated {
		t.Fatalf("project create status = %d body=%s", prjRR.Code, prjRR.Body.String())
	}
	var project Project
	if err := json.Unmarshal(prjRR.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v", err)
	}

	// 2. service create -> action "service.create".
	createSvc := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services", strings.NewReader(`{"project_id":"`+string(project.ID)+`","slug":"web"}`))
	createSvc.Header.Set("Idempotency-Key", "audit-create-svc")
	svcRR := httptest.NewRecorder()
	router.ServeHTTP(svcRR, createSvc)
	if svcRR.Code != http.StatusCreated {
		t.Fatalf("service create status = %d body=%s", svcRR.Code, svcRR.Body.String())
	}
	var service Service
	if err := json.Unmarshal(svcRR.Body.Bytes(), &service); err != nil {
		t.Fatalf("decode service: %v", err)
	}

	// 3. seed an operation, then cancel it -> action "operation.cancel".
	router2 := router // retain router for cancel
	// We need the operation store to have a cancellable operation. Use
	// the demo long handler which queues one for us.
	demoReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/_demo/long", strings.NewReader(`{}`))
	demoReq.Header.Set("Idempotency-Key", "audit-create-op")
	demoRR := httptest.NewRecorder()
	router2.ServeHTTP(demoRR, demoReq)
	if demoRR.Code != http.StatusAccepted {
		t.Fatalf("demo create status = %d body=%s", demoRR.Code, demoRR.Body.String())
	}
	var op map[string]any
	if err := json.Unmarshal(demoRR.Body.Bytes(), &op); err != nil {
		t.Fatalf("decode op: %v", err)
	}
	opID, _ := op["id"].(string)
	if opID == "" {
		t.Fatalf("demo response missing id; body=%s", demoRR.Body.String())
	}
	cancelReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/operations/"+opID+"/cancel", nil)
	cancelRR := httptest.NewRecorder()
	router2.ServeHTTP(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d body=%s", cancelRR.Code, cancelRR.Body.String())
	}

	// Inspect the audit chain for the three expected rows.
	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("chain read: %v", err)
	}
	want := map[string]string{
		"project.create":   string(project.ID),
		"service.create":   string(service.ID),
		"operation.cancel": opID,
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Action] = r.ResourceID
	}
	for action, resourceID := range want {
		if got[action] != resourceID {
			t.Fatalf("audit chain missing action %q for resource %q; got=%#v rows=%#v", action, resourceID, got, rows)
		}
	}
}

// TestRouterRejectsReplayedWebhookSignatureWindow asserts that a
// signed webhook delivery whose timestamp falls outside the panel-
// locked 300s window is rejected with WEBHOOK_REPLAY_REJECTED. The
// receiver's clock is held fixed so we can sign with a stale t and
// assert the deterministic error path.
func TestRouterRejectsReplayedWebhookSignatureWindow(t *testing.T) {
	endpoints := webhook.NewMemoryEndpointResolver()
	endpoints.Set("ep-test", webhook.EndpointConfig{
		Provider: webhook.ProviderMidnightIncoming,
		Keys:     []webhook.Key{{KID: "kid_test", Material: []byte("phase1-wireup-secret")}},
	})
	now := time.Date(2026, 4, 27, 14, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	router := NewRouterWithDataStack(DataStack{
		WebhookEndpoints:   endpoints,
		WebhookDedupeStore: webhook.NewMemoryDedupeStore(),
		Clock:              clock,
	})

	body := []byte(`{"stale":true}`)
	stale := now.Add(-10 * time.Minute)
	sig, err := webhook.Signer{}.SignBody([]webhook.Key{{KID: "kid_test", Material: []byte("phase1-wireup-secret")}}, body, stale)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, WebhookRoutePrefix+"midnight-incoming/ep-test", bytes.NewReader(body))
	req.Header.Set("Midnight-Signature", sig)
	req.Header.Set("Midnight-Delivery-Id", "whd_stale_001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400 WEBHOOK_REPLAY_REJECTED", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WEBHOOK_REPLAY_REJECTED") {
		t.Fatalf("body = %s, want WEBHOOK_REPLAY_REJECTED", rec.Body.String())
	}
}
