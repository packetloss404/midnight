// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMonitorCreateCLIUsesDurableRESTSurface(t *testing.T) {
	var body map[string]any
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1alpha/monitors" || request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		idempotencyKey = request.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeJSONForCLITest(w, http.StatusCreated, map[string]any{
			"id": "mon_1", "name": "CPU high", "signal": "cpu_percent",
			"operator": "gt", "threshold": 80, "state": "unknown",
		})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := runMonitor([]string{
		"create", "--api-base", server.URL, "--token", "test-token",
		"--project-id", "prj_1", "--environment-id", "env_1",
		"--service-id", "svc_1", "--name", "CPU high",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if idempotencyKey == "" {
		t.Fatal("Idempotency-Key was not set")
	}
	if body["service_id"] != "svc_1" || body["window_seconds"] != float64(300) {
		t.Fatalf("body = %#v", body)
	}
}

func TestMonitorDeliveryListCLIUsesScopedMetadataSurface(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1alpha/monitor-deliveries" || request.Method != http.MethodGet {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.URL.Query().Get("project_id") != "prj_1" ||
			request.URL.Query().Get("service_id") != "svc_1" ||
			request.URL.Query().Get("state") != "dead" {
			t.Errorf("query = %s", request.URL.RawQuery)
		}
		writeJSONForCLITest(w, http.StatusOK, []map[string]any{{
			"id": "mdl_1", "incident_id": "minc_1", "monitor_id": "mon_1",
			"monitor_name": "CPU high", "channel_id": "mch_1", "channel_name": "on-call",
			"event_type": "monitor.firing", "state": "dead", "attempts": 8,
			"manual_retries": 1, "response_status": 503,
			"error_summary": "remote endpoint returned a non-success status",
			"available_at":  "2026-07-28T03:00:00Z",
		}})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := runMonitor([]string{
		"delivery", "list", "--api-base", server.URL, "--token", "test-token",
		"--project-id", "prj_1", "--service-id", "svc_1", "--state", "dead",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"mdl_1", "CPU high", "on-call", "dead", "attempts=8", "retries=1"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestMonitorDeliveryRetryCLIIsScopedAndIdempotent(t *testing.T) {
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1alpha/monitor-deliveries/mdl_1/retry" ||
			request.Method != http.MethodPost {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") != "retry-mdl-1" {
			t.Errorf("idempotency = %q", request.Header.Get("Idempotency-Key"))
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeJSONForCLITest(w, http.StatusAccepted, map[string]any{
			"id": "mdl_1", "state": "pending", "manual_retries": 2,
			"available_at": "2026-07-28T03:00:00Z",
		})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := runMonitor([]string{
		"delivery", "retry", "--api-base", server.URL, "--token", "test-token",
		"--project-id", "prj_1", "--idempotency-key", "retry-mdl-1", "mdl_1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if body["project_id"] != "prj_1" || !bytes.Contains(stdout.Bytes(), []byte("manual retry 2")) {
		t.Fatalf("body = %#v stdout = %q", body, stdout.String())
	}
}

func writeJSONForCLITest(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
