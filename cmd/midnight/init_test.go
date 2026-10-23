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

// initTestServer serves the smallest project/environment/service surface
// `midnight init` needs, and records every mutation it receives.
type initTestServer struct {
	projects     []cliProject
	environments []cliEnvironment
	services     []cliService
	created      []string
}

func (s *initTestServer) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1alpha/workspaces":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "wrk_1", "slug": "default"}})
		case r.URL.Path == "/api/v1alpha/projects" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.projects)
		case r.URL.Path == "/api/v1alpha/projects" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			created := cliProject{ID: "prj_new", Slug: body["slug"], DisplayName: body["display_name"], WorkspaceID: body["workspace_id"]}
			s.projects = append(s.projects, created)
			s.created = append(s.created, "project:"+created.Slug)
			_ = json.NewEncoder(w).Encode(created)
		case r.URL.Path == "/api/v1alpha/environments" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.environments)
		case r.URL.Path == "/api/v1alpha/environments" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			created := cliEnvironment{ID: "env_new", Slug: body["slug"].(string), ResourceVersion: 1}
			s.environments = append(s.environments, created)
			s.created = append(s.created, "environment:"+created.Slug)
			_ = json.NewEncoder(w).Encode(created)
		case r.URL.Path == "/api/v1alpha/services" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.services)
		case r.URL.Path == "/api/v1alpha/services" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			created := cliService{ID: "svc_new", Slug: body["slug"], ProjectID: body["project_id"]}
			s.services = append(s.services, created)
			s.created = append(s.created, "service:"+created.Slug)
			_ = json.NewEncoder(w).Encode(created)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func runInitTest(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	execution := NewCLIExecutionContext(strings.NewReader(stdin), &stdout, &stderr)
	for _, arg := range args {
		if arg == "--no-input" {
			execution.Options.NoInput = true
		}
	}
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "--no-input" {
			filtered = append(filtered, arg)
		}
	}
	code := runInit(filtered, &cliOutputWriter{Writer: &stdout, execution: execution}, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInitCreatesProjectEnvironmentServiceAndLinks(t *testing.T) {
	backend := &initTestServer{}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	code, stdout, stderr := runInitTest(t, "", "--no-input", "--name", "checkout", "--service-name", "web")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	want := []string{"project:checkout", "environment:production", "service:web"}
	if len(backend.created) != 3 {
		t.Fatalf("created = %v, want %v", backend.created, want)
	}
	for index, expected := range want {
		if backend.created[index] != expected {
			t.Fatalf("created = %v, want %v", backend.created, want)
		}
	}
	if !strings.Contains(stdout, "Next: midnight up") {
		t.Errorf("init did not print the next command:\n%s", stdout)
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		t.Fatalf("LoadProjectLink() error = %v", err)
	}
	if link.ProjectSlug != "checkout" || link.ServiceSlug != "web" || link.EnvironmentName != "production" {
		t.Fatalf("link = %+v", link)
	}
}

func TestInitIsIdempotent(t *testing.T) {
	backend := &initTestServer{
		projects:     []cliProject{{ID: "prj_1", Slug: "checkout"}},
		environments: []cliEnvironment{{ID: "env_1", Slug: "production"}},
		services:     []cliService{{ID: "svc_1", Slug: "web", ProjectID: "prj_1"}},
	}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	for attempt := 0; attempt < 2; attempt++ {
		code, _, stderr := runInitTest(t, "", "--no-input", "--name", "checkout", "--service-name", "web")
		if code != 0 {
			t.Fatalf("attempt %d exit code = %d, stderr=%q", attempt, code, stderr)
		}
	}
	if len(backend.created) != 0 {
		t.Fatalf("re-running init created %v, want nothing", backend.created)
	}
	link, err := cliconfig.LoadProjectLink("")
	if err != nil {
		t.Fatalf("LoadProjectLink() error = %v", err)
	}
	if link.ServiceID != "svc_1" || link.EnvironmentID != "env_1" {
		t.Fatalf("link = %+v", link)
	}
}

func TestInitUnderNoInputRefusesToGuess(t *testing.T) {
	backend := &initTestServer{
		projects: []cliProject{{ID: "prj_1", Slug: "one"}, {ID: "prj_2", Slug: "two"}},
	}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	code, _, stderr := runInitTest(t, "", "--no-input")
	if code != cliUsageExitCode {
		t.Fatalf("exit code = %d, want %d; stderr=%q", code, cliUsageExitCode, stderr)
	}
	if !strings.Contains(stderr, "--project-id or --name is required") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestInitPromptsForTheServiceSlug(t *testing.T) {
	backend := &initTestServer{
		projects:     []cliProject{{ID: "prj_1", Slug: "checkout"}},
		environments: []cliEnvironment{{ID: "env_1", Slug: "production"}},
	}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	code, stdout, stderr := runInitTest(t, "api\n")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "Service slug") {
		t.Errorf("init did not prompt for the service slug:\n%s", stdout)
	}
	if len(backend.created) != 1 || backend.created[0] != "service:api" {
		t.Fatalf("created = %v, want [service:api]", backend.created)
	}
}

func TestSanitizeInitSlug(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"My App":         "my-app",
		"my_app.v2":      "my-app-v2",
		"---":            "app",
		"9lives":         "app-9lives",
		"already-fine":   "already-fine",
		"Trailing dash-": "trailing-dash",
	}
	for input, want := range tests {
		if got := sanitizeInitSlug(input); got != want {
			t.Errorf("sanitizeInitSlug(%q) = %q, want %q", input, got, want)
		}
		if err := validateInitSlug("service", sanitizeInitSlug(input)); err != nil {
			t.Errorf("sanitizeInitSlug(%q) produced an invalid slug: %v", input, err)
		}
	}
}

func TestCLIPrompterHonoursDisabledInput(t *testing.T) {
	t.Parallel()

	prompt := newCLIPrompter(io.Discard, strings.NewReader(""), true)
	if prompt.interactive() {
		t.Fatal("prompter reports interactive while input is disabled")
	}
	if value, err := prompt.text("Service slug", "fallback"); err != nil || value != "fallback" {
		t.Fatalf("text() = %q, %v; want the fallback", value, err)
	}
	if _, err := prompt.text("Service slug", ""); err == nil {
		t.Fatal("text() with no fallback should fail when input is disabled")
	}
	if _, err := prompt.choice(3); err == nil {
		t.Fatal("choice() should fail when input is disabled")
	}
}
