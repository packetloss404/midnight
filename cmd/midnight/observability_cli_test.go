// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestObservabilityLogsCLIUsesAuthenticatedScopedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1alpha/observability/logs" ||
			r.URL.Query().Get("project_id") != "prj_1" ||
			r.URL.Query().Get("service_id") != "svc_1" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		fmt.Fprint(w, `{
			"records":[{
				"observed_at":"2026-07-27T20:00:00Z",
				"scope":{"project_id":"prj_1","service_id":"svc_1"},
				"surface":"runtime","stream":"stdout","level":"info",
				"component":"app.runtime","message":"ready"
			}],
			"truncated":true,
			"next_cursor":{"token":"opaque-token"},
			"dropped_at_ingest":2,
			"collector_health":[{"kind":"docker","status":"stale","heartbeat_gap":true,"reason":"collector heartbeat is stale"}]
		}`)
	}))
	defer server.Close()
	var stdout, stderr strings.Builder
	code := runObservability([]string{
		"logs", "--api-base", server.URL, "--token", "token-1",
		"--project-id", "prj_1", "--service-id", "svc_1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	for _, value := range []string{"app.runtime", "ready", "ingestion drops", "opaque-token", "collector heartbeat gap", "docker", "stale"} {
		if !strings.Contains(stdout.String(), value) {
			t.Fatalf("output %q missing %q", stdout.String(), value)
		}
	}
}

func TestObservabilityMetricsCLIRequiresProjectAndService(t *testing.T) {
	var stdout, stderr strings.Builder
	code := runObservability([]string{"metrics", "--project-id", "prj_1"}, &stdout, &stderr)
	if code != 64 || !strings.Contains(stderr.String(), "--service-id") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestObservabilityLogStreamReconnectsWithOpaqueLastEventID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer token-1" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if call == 2 && r.Header.Get("Last-Event-ID") != "opaque-one" {
			t.Errorf("Last-Event-ID = %q, want opaque-one", r.Header.Get("Last-Event-ID"))
		}
		token, message := "opaque-one", "first"
		if call == 2 {
			token, message = "opaque-two", "second"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: collector-health\ndata: [{\"kind\":\"traefik\",\"status\":\"stale\",\"heartbeat_gap\":true,\"reason\":\"collector heartbeat is stale\"}]\n\n")
		fmt.Fprintf(w, "id: %s\nevent: log\ndata: {\"observed_at\":\"2026-07-27T20:00:00Z\",\"surface\":\"runtime\",\"level\":\"info\",\"component\":\"app.runtime\",\"message\":%q}\n\n", token, message)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		if call == 2 {
			time.AfterFunc(25*time.Millisecond, cancel)
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	var stdout, stderr strings.Builder
	code := streamObservabilityLogs(ctx, &cliAPIClient{
		baseURL: server.URL, token: "token-1", client: server.Client(),
	}, "/api/v1alpha/observability/logs/stream?project_id=prj_1", false, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if calls.Load() != 2 || !strings.Contains(stdout.String(), "first") ||
		!strings.Contains(stdout.String(), "second") ||
		!strings.Contains(stdout.String(), "collector heartbeat gap: traefik") {
		t.Fatalf("calls=%d stdout=%q stderr=%q", calls.Load(), stdout.String(), stderr.String())
	}
}
