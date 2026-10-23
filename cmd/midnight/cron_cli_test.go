package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCronListCLIUsesScopedOccurrenceEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/services/svc_1/environments/env_1/cron-occurrences" || r.URL.Query().Get("state") != "failed" {
			t.Fatalf("request=%s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`[{"id":"cron_1","due_at":"2026-07-27T12:00:00Z","state":"failed","attempt":2,"max_attempts":2,"failure_code":"PREDEPLOY_FAILED","failure_message":"exit 3"}]`))
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	if code := runCron([]string{"list", "--api-base", server.URL, "--token", "token", "--service-id", "svc_1", "--environment-id", "env_1", "--state", "failed"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "cron_1\tfailed") || !strings.Contains(out.String(), "PREDEPLOY_FAILED: exit 3") {
		t.Fatalf("output=%q", out.String())
	}
}
