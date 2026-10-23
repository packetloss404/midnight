// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

func writeTestProjectLink(t *testing.T, apiBase string) {
	t.Helper()
	link := cliconfig.ProjectLink{
		APIBase:         apiBase,
		ProjectID:       "prj_1",
		ProjectSlug:     "checkout",
		EnvironmentID:   "env_1",
		EnvironmentName: "production",
		ServiceID:       "svc_1",
		ServiceSlug:     "web",
	}
	if err := cliconfig.SaveProjectLink("", link); err != nil {
		t.Fatalf("SaveProjectLink() error = %v", err)
	}
}

// runCLICommandTest drives a legacy handler through the framework's output
// writer so the handler sees the same --output selection the real CLI gives it.
func runCLICommandTest(
	t *testing.T,
	handler legacyCLIHandler,
	stdin string,
	options CLIOptions,
	args ...string,
) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	execution := NewCLIExecutionContext(strings.NewReader(stdin), &stdout, &stderr)
	if options.Output == "" {
		options.Output = "table"
	}
	execution.Options = options
	code := handler(args, &cliOutputWriter{Writer: &stdout, execution: execution}, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestStatusReportsServingInFlightAndLastFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1alpha/services/svc_1/deployments"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []cliDeployment{
				{DeploymentID: "dep_running", EnvironmentID: "env_1", OperationID: "op_running", OperationState: "RUNNING"},
				{DeploymentID: "dep_active", EnvironmentID: "env_1", OperationID: "op_active", OperationState: "SUCCEEDED", Active: true},
				{DeploymentID: "dep_failed", EnvironmentID: "env_1", OperationID: "op_failed", OperationState: "FAILED"},
			}})
		case r.URL.Path == "/api/v1alpha/operations/op_failed":
			_ = json.NewEncoder(w).Encode(cliOperationDetail{
				ID:     "op_failed",
				State:  "FAILED",
				Code:   "RECONCILE_STEP_FAILED",
				Reason: "candidate never became ready",
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())
	writeTestProjectLink(t, server.URL)

	code, stdout, stderr := runCLICommandTest(t, runStatus, "", CLIOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	for _, want := range []string{
		"project      : checkout (prj_1)",
		"serving      : dep_active",
		"in flight    : dep_running",
		"last failure : dep_failed",
		"RECONCILE_STEP_FAILED: candidate never became ready",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output missing %q:\n%s", want, stdout)
		}
	}
}

func TestStatusHonoursOutputJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []cliDeployment{
			{DeploymentID: "dep_active", EnvironmentID: "env_1", OperationID: "op_active", OperationState: "SUCCEEDED", Active: true},
		}})
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())
	writeTestProjectLink(t, server.URL)

	code, stdout, stderr := runCLICommandTest(t, runStatus, "", CLIOptions{Output: "json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	var report cliStatusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("status --output json is not a JSON document: %v\n%s", err, stdout)
	}
	if report.Serving == nil || report.Serving.DeploymentID != "dep_active" {
		t.Fatalf("report = %+v", report)
	}
	if report.InFlight != nil || report.LastFailure != nil {
		t.Fatalf("report reported rollouts that do not exist: %+v", report)
	}
}

func TestServiceDeleteRequiresConfirmation(t *testing.T) {
	var deletes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	// --no-input without --yes is a usage error, never a silent delete.
	code, _, stderr := runCLICommandTest(t, runServiceDelete, "", CLIOptions{NoInput: true}, "svc_1")
	if code != cliUsageExitCode || !strings.Contains(stderr, "--yes is required with --no-input") {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if deletes != 0 {
		t.Fatal("service delete issued a request without confirmation")
	}

	// A mismatched confirmation aborts.
	code, _, stderr = runCLICommandTest(t, runServiceDelete, "wrong\n", CLIOptions{}, "svc_1")
	if code == 0 || !strings.Contains(stderr, "confirmation did not match") {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if deletes != 0 {
		t.Fatal("service delete issued a request after a mismatched confirmation")
	}

	// Retyping the id proceeds.
	code, stdout, stderr := runCLICommandTest(t, runServiceDelete, "svc_1\n", CLIOptions{}, "svc_1")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if deletes != 1 || !strings.Contains(stdout, "service deleted: svc_1") {
		t.Fatalf("deletes = %d, stdout = %q", deletes, stdout)
	}

	// --yes skips the prompt entirely.
	code, _, stderr = runCLICommandTest(t, runServiceDelete, "", CLIOptions{Yes: true, NoInput: true}, "svc_1")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if deletes != 2 {
		t.Fatalf("deletes = %d, want 2", deletes)
	}
}

func TestProjectDeleteReportsAMissingServerRouteClearly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":{"code":"method_not_allowed","message":"method not allowed"}}`))
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())

	code, _, stderr := runCLICommandTest(t, runProjectDelete, "", CLIOptions{Yes: true}, "prj_1")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "does not implement project deletion yet") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestLogsRendersASummaryByDefaultAndRawJSONOnRequest(t *testing.T) {
	body := `{"operation_id":"op_1","deploy_logs":{"available":true,"kind":"operation_events","count":3,"url":"/events"},` +
		`"app_logs":[{"surface":"runtime","available":true,"count":1,"entries":[{"at":"2026-09-04T08:18:12Z","level":"info","component":"app.runtime","message":"ready"}],` +
		`"next_cursor":{"token":"` + strings.Repeat("c", 200) + `"},"truncated":true}],"platform_logs":[],"message":""}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1alpha/services/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []cliDeployment{
				{DeploymentID: "dep_1", EnvironmentID: "env_1", OperationID: "op_1", OperationState: "SUCCEEDED", Active: true},
			}})
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "dev-secret")
	t.Chdir(t.TempDir())
	writeTestProjectLink(t, server.URL)

	code, stdout, stderr := runCLICommandTest(t, runLogs, "", CLIOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, `"app_logs"`) || strings.Contains(stdout, `"next_cursor"`) {
		t.Fatalf("logs still dumps the raw document by default:\n%s", stdout)
	}
	if !strings.Contains(stdout, "operation logs: op_1") || !strings.Contains(stdout, "ready") {
		t.Fatalf("logs summary is missing content:\n%s", stdout)
	}
	// The resume cursor stays copy-pasteable, but as one labelled line rather
	// than a field buried in a pretty-printed document.
	if !strings.Contains(stdout, "runtime logs next cursor: --surface runtime --cursor ") {
		t.Fatalf("logs summary dropped the resume hint:\n%s", stdout)
	}
	if lines := strings.Count(strings.TrimSpace(stdout), "\n") + 1; lines > 12 {
		t.Fatalf("logs summary is %d lines, want a short human summary:\n%s", lines, stdout)
	}

	code, stdout, stderr = runCLICommandTest(t, runLogs, "", CLIOptions{Output: "json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("logs --output json is not a JSON document: %v", err)
	}
	if decoded["operation_id"] != "op_1" {
		t.Fatalf("logs --output json lost the document: %v", decoded)
	}
}

func TestConfigAndChangesetParseCanonicalAndAliasFlags(t *testing.T) {
	t.Parallel()

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := runChangeset([]string{
		"get", "--environment-id", "env_1", "--endpoint", server.URL, "--token", "t",
	}, &stdout); err != nil {
		t.Fatalf("runChangeset with --endpoint alias: %v", err)
	}
	stdout.Reset()
	if err := runChangeset([]string{
		"get", "--env", "env_1", "--api-base", server.URL, "--token", "t",
	}, &stdout); err != nil {
		t.Fatalf("runChangeset with --env alias: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("requests = %v, want two changeset reads", seen)
	}
	for _, path := range seen {
		if path != "/api/v1alpha/environments/env_1/changeset" {
			t.Fatalf("request path = %q", path)
		}
	}

	// An unknown flag is now a parse error rather than a hand-rolled string
	// comparison, and a missing revision is still refused.
	if err := runChangeset([]string{"get", "--nope", "x"}, &stdout); err == nil {
		t.Fatal("runChangeset accepted an unknown flag")
	}
	if err := runChangeset([]string{"apply", "--environment-id", "env_1"}, &stdout); err == nil ||
		!strings.Contains(err.Error(), "--revision is required") {
		t.Fatalf("runChangeset apply error = %v", err)
	}
	if err := runConfig([]string{"validate"}, &stdout); err == nil ||
		!strings.Contains(err.Error(), "--file is required") {
		t.Fatalf("runConfig validate error = %v", err)
	}
}
