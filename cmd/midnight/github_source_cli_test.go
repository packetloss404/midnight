// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

func setGitHubSourceHumanSession(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetHumanSession(apiBase, cliconfig.HumanSessionCredential{
		Token: "midn_user_github-source",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceGitHubRepositoriesUsesOnlyProjectScopedRoute(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/projects/prj_alpha/github/repositories" {
			t.Fatalf("request = %s %s, want project-scoped repository route", r.Method, r.URL.Path)
		}
		if strings.Contains(r.URL.Path, "/github/installations") {
			t.Fatalf("request unexpectedly used global installation route: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer midn_user_github-source" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"installation_id":41,"repository_id":88,"repository_full_name":"acme/web","private":true,"default_branch":"main"}]}`))
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService([]string{"github", "repositories", "--project-id", "prj_alpha"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	for _, want := range []string{"41", "88", "acme/web", "main", "private"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestServiceGitHubRepositoriesJSONPreservesCollectionEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService(
		[]string{"github", "repositories", "--project-id", "prj_alpha", "--json"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	var got cliGitHubRepositoryList
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode output: %v; stdout=%q", err, stdout.String())
	}
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("items = %#v, want present empty collection", got.Items)
	}
}

func TestServiceGitHubBranchesUsesProjectAndNumericRepositoryRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/api/v1alpha/projects/prj_alpha/github/repositories/88/branches"
		if r.Method != http.MethodGet || r.URL.Path != want {
			t.Fatalf("request = %s %s, want GET %s", r.Method, r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"name":"main"},{"name":"release/v2"}]}`))
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService(
		[]string{"github", "branches", "--project-id", "prj_alpha", "--repository-id", "88"},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "main") || !strings.Contains(stdout.String(), "release/v2") {
		t.Fatalf("stdout = %q, want branch table", stdout.String())
	}
}

func TestServiceGitHubGetDefaultsToLinkedService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/services/svc_web/github" {
			t.Fatalf("request = %s %s, want linked service source route", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"installation_id":41,
			"repository_id":88,
			"repository_full_name":"acme/web",
			"branch":"main",
			"binding_state":"bound",
			"repository_access_verified_at":"2026-07-29T12:00:00Z",
			"updated_at":"2026-07-29T12:01:00Z"
		}`))
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)
	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:   server.URL,
		ProjectID: "prj_alpha",
		ServiceID: "svc_web",
	}); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService([]string{"github", "get"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"svc_web", "41", "88 (acme/web)", "main", "bound", "2026-07-29T12:00:00Z"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestServiceGitHubLinkSendsStrictBodyAndStableRetryKey(t *testing.T) {
	const wantBody = `{"installation_id":41,"repository_id":88,"branch":"main"}`
	var bodies []string
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1alpha/services/svc_web/github" {
			t.Fatalf("request = %s %s, want strict service source route", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if values := r.Header.Values("Idempotency-Key"); len(values) != 1 {
			t.Fatalf("Idempotency-Key headers = %#v, want exactly one", values)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer midn_user_github-source" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"installation_id":41,"repository_id":88,"repository_full_name":"acme/web","branch":"main","binding_state":"bound"}`))
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)

	args := []string{
		"github", "link",
		"--installation-id", "41",
		"--repository-id", "88",
		"--branch", "main",
		"--idempotency-key", "source-link-retry-1",
		"svc_web",
	}
	for attempt := 0; attempt < 2; attempt++ {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := runService(args, &stdout, &stderr); code != 0 {
			t.Fatalf("attempt %d exit = %d stderr=%q", attempt+1, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "svc_web -> acme/web@main") {
			t.Fatalf("attempt %d stdout = %q", attempt+1, stdout.String())
		}
	}
	if len(bodies) != 2 || len(keys) != 2 {
		t.Fatalf("requests = %d bodies, %d keys; want 2", len(bodies), len(keys))
	}
	for i := range bodies {
		if bodies[i] != wantBody {
			t.Fatalf("request %d body = %q, want %q", i+1, bodies[i], wantBody)
		}
		if keys[i] != "source-link-retry-1" {
			t.Fatalf("request %d key = %q, want stable retry key", i+1, keys[i])
		}
	}
}

func TestServiceGitHubLinkRejectsInvalidBranchAndKeyBeforeRequest(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "full ref",
			args: []string{"--branch", "refs/heads/main"},
		},
		{
			name: "branch whitespace",
			args: []string{"--branch", " main"},
		},
		{
			name: "comma in idempotency key",
			args: []string{"--branch", "main", "--idempotency-key", "retry,other"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				requests++
			}))
			defer server.Close()
			setGitHubSourceHumanSession(t, server.URL)

			args := []string{
				"github", "link",
				"--installation-id", "41",
				"--repository-id", "88",
			}
			args = append(args, tt.args...)
			args = append(args, "svc_web")
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runService(args, &stdout, &stderr)
			if code != 64 {
				t.Fatalf("exit = %d, want usage error; stderr=%q", code, stderr.String())
			}
			if requests != 0 {
				t.Fatalf("requests = %d, want validation before request", requests)
			}
		})
	}
}

func TestServiceGitHubUnlinkSendsEmptyBodyAndStableRetryKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1alpha/services/svc_web/github" {
			t.Fatalf("request = %s %s, want DELETE service source route", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != 0 {
			t.Fatalf("body = %q, want empty", body)
		}
		if got := r.Header.Get("Content-Type"); got != "" {
			t.Fatalf("Content-Type = %q, want absent for empty delete", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "source-unlink-retry-1" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setGitHubSourceHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService([]string{
		"github", "unlink",
		"--idempotency-key", "source-unlink-retry-1",
		"--json",
		"svc_web",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	// --json is the alias for --output json, so the document is rendered by
	// the shared renderer: indented, like every other JSON response.
	if stdout.String() != "{\n  \"deleted\": true,\n  \"service_id\": \"svc_web\"\n}\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestServiceGitHubCommandsRejectNonHumanCredentialsBeforeRequest(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, server.URL)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "midn_sa_project-token")
	t.Setenv(envAPIToken, "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runService(
		[]string{"github", "repositories", "--project-id", "prj_alpha"},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires a human session") {
		t.Fatalf("stderr = %q, want human-session guidance", stderr.String())
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want local rejection", requests)
	}
}
