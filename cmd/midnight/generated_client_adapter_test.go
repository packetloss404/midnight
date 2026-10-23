package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeneratedReadSlicePreservesRoutesAuthAndOutput(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want test-token", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		if got := request.Header.Get("User-Agent"); !strings.HasPrefix(got, "midnight-cli/") {
			t.Errorf("User-Agent = %q, want midnight-cli version", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1alpha/projects":
			_, _ = writer.Write([]byte(`[{"id":"prj_1","workspace_id":"wrk_1","slug":"alpha","display_name":"Alpha"}]`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1alpha/services":
			if got := request.URL.Query().Get("project_id"); got != "prj_1" {
				t.Errorf("project_id = %q, want prj_1", got)
			}
			_, _ = writer.Write([]byte(`[{"id":"svc_1","project_id":"prj_1","slug":"web","display_name":"Web"}]`))
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1alpha/operations/op_1":
			_, _ = writer.Write([]byte(`{"id":"op_1","kind":"deploy","state":"FAILED","code":"BUILD_FAILED","reason":"compile failed"}`))
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	setCLIEndpoint(t, server.URL)

	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
		want []string
	}{
		{
			name: "project list",
			run:  func(stdout, stderr *bytes.Buffer) int { return runProjectList(nil, stdout, stderr) },
			want: []string{"ID", "SLUG", "NAME", "prj_1", "alpha", "Alpha"},
		},
		{
			name: "service list",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runServiceList([]string{"--project-id", "prj_1"}, stdout, stderr)
			},
			want: []string{"ID", "PROJECT", "SLUG", "NAME", "svc_1", "prj_1", "web", "Web"},
		},
		{
			name: "deploy status",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runDeployStatus([]string{"--operation-id", "op_1"}, stdout, stderr)
			},
			want: []string{"op_1\tdeploy\tFAILED", "reason\tBUILD_FAILED\tcompile failed"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := test.run(&stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d stderr=%q", code, stderr.String())
			}
			for _, want := range test.want {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout = %q, want %q", stdout.String(), want)
				}
			}
		})
	}
	if requests != len(tests) {
		t.Fatalf("requests = %d, want %d", requests, len(tests))
	}
}

func TestGeneratedReadSlicePreservesRawJSONMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1alpha/services" {
			t.Errorf("path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`[{"id":"svc_1","project_id":"prj_1","slug":"web","display_name":"Web"}]`))
	}))
	t.Cleanup(server.Close)
	setCLIEndpoint(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runServiceList([]string{"--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"project_id": "prj_1"`) || !strings.HasSuffix(stdout.String(), "\n") {
		t.Fatalf("stdout = %q, want pretty raw response", stdout.String())
	}
}

func TestGeneratedReadSliceRefusesRedirectWithoutForwardingCredential(t *testing.T) {
	var targetRequests int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests++
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/captured", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	setCLIEndpoint(t, source.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runProjectList(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target requests = %d, want zero", targetRequests)
	}
	if !strings.Contains(stderr.String(), errCLIRedirect.Error()) {
		t.Fatalf("stderr = %q, want redirect refusal", stderr.String())
	}
}

func TestGeneratedReadSlicePreservesResponseLimitAndStatusErrors(t *testing.T) {
	t.Run("bounded", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), maxCLIResponseBytes+1))
		}))
		t.Cleanup(server.Close)
		setCLIEndpoint(t, server.URL)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := runProjectList(nil, &stdout, &stderr); code != 1 {
			t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), fmt.Sprintf("response exceeds %d bytes", maxCLIResponseBytes)) {
			t.Fatalf("stderr = %q, want response cap", stderr.String())
		}
	})

	t.Run("status envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":{"code":"RESOURCE_NOT_FOUND","message":"project not found"}}`))
		}))
		t.Cleanup(server.Close)
		setCLIEndpoint(t, server.URL)

		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := runProjectList(nil, &stdout, &stderr); code != 1 {
			t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
		}
		for _, want := range []string{"GET /api/v1alpha/projects failed", "404 Not Found", "RESOURCE_NOT_FOUND", "project not found"} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want %q", stderr.String(), want)
			}
		}
	})
}
