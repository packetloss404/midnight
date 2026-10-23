// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsageUsesGeneratedClientAndPrintsCoverage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1alpha/usage/summary" {
			t.Fatalf("request = %s %s, want GET /api/v1alpha/usage/summary", request.Method, request.URL.Path)
		}
		if got := request.URL.Query().Get("workspace_id"); got != "wrk_1" {
			t.Fatalf("workspace_id = %q, want wrk_1", got)
		}
		if got := request.URL.Query().Get("since"); got != "2026-08-01T00:00:00Z" {
			t.Fatalf("since = %q", got)
		}
		if got := request.URL.Query().Get("until"); got != "2026-09-01T00:00:00Z" {
			t.Fatalf("until = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"workspace_id":"wrk_1",
			"window_start":"2026-08-01T00:00:00Z",
			"window_end":"2026-09-01T00:00:00Z",
			"as_of":"2026-08-31T12:00:00Z",
			"policy_profile":"self_hosted",
			"replay_after":0,
			"disclosures":["Build duration uses exact lifecycle markers."],
			"usage":[{"dimension":"volume_count","used":2,"unit":"count","limit":3,"enforceable":true,"coverage":{"source":"resource_gauge","status":"complete","missing_intervals":0}}]
		}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runUsage([]string{
		"--workspace-id", "wrk_1",
		"--since", "2026-08-01T00:00:00Z",
		"--until", "2026-09-01T00:00:00Z",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"Workspace: wrk_1", "volume_count", "2 count", "complete", "Build duration uses exact lifecycle markers."} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestUsageRejectsInvalidWindowBeforeRequest(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runUsage([]string{
		"--workspace-id", "wrk_1",
		"--since", "2026-09-01T00:00:00Z",
		"--until", "2026-08-01T00:00:00Z",
	}, stdout, stderr)
	if code != cliUsageExitCode {
		t.Fatalf("exit = %d, want %d", code, cliUsageExitCode)
	}
	if !strings.Contains(stderr.String(), "--since must be before --until") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
