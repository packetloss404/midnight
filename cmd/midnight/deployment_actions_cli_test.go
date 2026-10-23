// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

func TestPostDeploymentActionUsesDedicatedImmutableActionEndpoint(t *testing.T) {
	var method, path, key, contentType, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.EscapedPath(), r.Header.Get("Idempotency-Key")
		contentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
		_, _ = io.WriteString(w, `{"created":true}`)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := postDeploymentAction(server.URL, "token", "svc one", "dpl/one", "rollback", "safe-key", &stdout)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %q", method)
	}
	if path != "/api/v1alpha/services/svc%20one/deployments/dpl%2Fone/actions/rollback" {
		t.Fatalf("path = %q", path)
	}
	if key != "safe-key" {
		t.Fatalf("Idempotency-Key = %q", key)
	}
	if contentType != "" {
		t.Fatalf("Content-Type = %q, want empty", contentType)
	}
	if body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
}

func TestPostOperationCancelUsesExactEmptyBodyRoute(t *testing.T) {
	var method, path, key, contentType, body string
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		method = r.Method
		path = r.URL.EscapedPath()
		key = r.Header.Get("Idempotency-Key")
		contentType = r.Header.Get("Content-Type")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(raw)
		_, _ = io.WriteString(
			w,
			`{"id":"op /one","kind":"deploy","state":"CANCELED"}`,
		)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := postOperationCancel(
		server.URL,
		"token",
		"op /one",
		"safe-cancel-key",
		&stdout,
	)
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost ||
		path != "/api/v1alpha/operations/op%20%2Fone/cancel" ||
		key != "safe-cancel-key" ||
		contentType != "" ||
		body != "" {
		t.Fatalf(
			"request method=%q path=%q key=%q content-type=%q body=%q",
			method,
			path,
			key,
			contentType,
			body,
		)
	}
	// The default output is the one-line human summary; the raw document is
	// what --output json asks for.
	if !strings.Contains(stdout.String(), "operation op /one: CANCELED") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	var jsonOut bytes.Buffer
	execution := NewCLIExecutionContext(nil, &jsonOut, io.Discard)
	execution.Options.Output = "json"
	if err := postOperationCancel(
		server.URL,
		"token",
		"op /one",
		"safe-cancel-key",
		&cliOutputWriter{Writer: &jsonOut, execution: execution},
	); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut.String(), `"state": "CANCELED"`) {
		t.Fatalf("json stdout = %q", jsonOut.String())
	}
}

func TestDeploymentActionTokenAcceptsHumanSession(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "https://midnight.example.test")
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_human-session")

	token, err := deploymentActionToken("https://midnight.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if token != "midn_user_human-session" {
		t.Fatalf("token = %q", token)
	}
}

func TestDeploymentSelectorsStayInLinkedEnvironment(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/api/v1alpha/services/svc_1/deployments" {
			t.Fatalf("path = %q, want service deployment collection", r.URL.Path)
		}
		if got := r.URL.Query().Get("environment_id"); got != "env_prod" {
			t.Fatalf("environment_id = %q, want env_prod", got)
		}
		if got := r.URL.Query().Get("limit"); got != "100" {
			t.Fatalf("limit = %q, want 100", got)
		}
		_, _ = io.WriteString(w, `[
			{"id":"dpl_staging","deployment_id":"dpl_staging","service_id":"svc_1","environment_id":"env_staging","operation_id":"op_staging","operation_state":"SUCCEEDED","status":"ACTIVE","active":true},
			{"id":"dpl_building","deployment_id":"dpl_building","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_building","operation_state":"RUNNING","status":"BUILDING"},
			{"id":"dpl_current","deployment_id":"dpl_current","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_current","operation_state":"SUCCEEDED","status":"ACTIVE","active":true},
			{"id":"dpl_failed","deployment_id":"dpl_failed","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_failed","operation_state":"FAILED","status":"FAILED"},
			{"id":"op_legacy","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_legacy","operation_state":"SUCCEEDED","status":"succeeded"},
			{"id":"dpl_previous","deployment_id":"dpl_previous","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_previous","operation_state":"SUCCEEDED","status":"SUPERSEDED"}
		]`)
	}))
	t.Cleanup(server.Close)

	operationID, err := latestDeployOperationID(server.URL, "token", "svc_1", "env_prod")
	if err != nil {
		t.Fatal(err)
	}
	if operationID != "op_building" {
		t.Fatalf("latest operation = %q, want op_building", operationID)
	}

	deploymentID, err := deploymentIDForOperation(server.URL, "token", "svc_1", "env_prod", "op_current")
	if err != nil {
		t.Fatal(err)
	}
	if deploymentID != "dpl_current" {
		t.Fatalf("deployment for operation = %q, want dpl_current", deploymentID)
	}

	activeID, err := latestActiveDeployment(server.URL, "token", "svc_1", "env_prod")
	if err != nil {
		t.Fatal(err)
	}
	if activeID != "dpl_current" {
		t.Fatalf("latest active = %q, want dpl_current", activeID)
	}

	previousID, err := previousDeployment(server.URL, "token", "svc_1", "env_prod")
	if err != nil {
		t.Fatal(err)
	}
	if previousID != "dpl_previous" {
		t.Fatalf("previous deployment = %q, want dpl_previous", previousID)
	}
	if requestCount != 4 {
		t.Fatalf("deployment requests = %d, want 4", requestCount)
	}
}

func TestDeploymentListBuildsScopedPaginationQueries(t *testing.T) {
	var requests []*http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Clone(r.Context()))
		switch r.URL.Query().Get("cursor") {
		case "":
			_, _ = io.WriteString(w, `{"items":[
				{"id":"dpl_wrong","deployment_id":"dpl_wrong","service_id":"svc/one","environment_id":"env_other","operation_id":"op_wrong"}
			],"next_cursor":"next/+ cursor=="}`)
		case "next/+ cursor==":
			_, _ = io.WriteString(w, `{"items":[
				{"id":"dpl_right","deployment_id":"dpl_right","service_id":"svc/one","environment_id":"env/prod","operation_id":"op_right"}
			]}`)
		default:
			t.Fatalf("unexpected cursor %q", r.URL.Query().Get("cursor"))
		}
	}))
	t.Cleanup(server.Close)

	operationID, err := latestDeployOperationID(server.URL, "token", "svc/one", "env/prod")
	if err != nil {
		t.Fatal(err)
	}
	if operationID != "op_right" {
		t.Fatalf("latest operation = %q, want op_right", operationID)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for index, request := range requests {
		if got := request.URL.EscapedPath(); got != "/api/v1alpha/services/svc%2Fone/deployments" {
			t.Fatalf("request %d escaped path = %q", index, got)
		}
		if got := request.URL.Query().Get("environment_id"); got != "env/prod" {
			t.Fatalf("request %d environment_id = %q", index, got)
		}
		if got := request.URL.Query().Get("limit"); got != "100" {
			t.Fatalf("request %d limit = %q", index, got)
		}
	}
	if got := requests[0].URL.Query().Get("cursor"); got != "" {
		t.Fatalf("first cursor = %q, want empty", got)
	}
	if got := requests[1].URL.Query().Get("cursor"); got != "next/+ cursor==" {
		t.Fatalf("second cursor = %q", got)
	}
}

func TestDeploymentSelectorsRequireEnvironmentBeforeRequest(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	if _, err := latestActiveDeployment(server.URL, "token", "svc_1", ""); err == nil {
		t.Fatal("latestActiveDeployment succeeded without an environment")
	}
	if requested {
		t.Fatal("deployment selector made a request without an environment")
	}
}

func TestRunRollbackPreviousSelectsSuccessfulCanonicalTargetInLinkedEnvironment(t *testing.T) {
	var actionPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if got := r.URL.Query().Get("environment_id"); got != "env_prod" {
				t.Fatalf("environment_id = %q, want env_prod", got)
			}
			_, _ = io.WriteString(w, `[
				{"id":"dpl_other","deployment_id":"dpl_other","service_id":"svc_1","environment_id":"env_other","operation_id":"op_other","operation_state":"SUCCEEDED","status":"ACTIVE","active":true},
				{"id":"dpl_current","deployment_id":"dpl_current","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_current","operation_state":"SUCCEEDED","status":"ACTIVE","active":true},
				{"id":"dpl_failed","deployment_id":"dpl_failed","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_failed","operation_state":"FAILED","status":"FAILED"},
				{"id":"op_noncanonical","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_noncanonical","operation_state":"SUCCEEDED","status":"succeeded"},
				{"id":"dpl_previous","deployment_id":"dpl_previous","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_previous","operation_state":"SUCCEEDED","status":"SUPERSEDED"}
			]`)
		case http.MethodPost:
			actionPath = r.URL.EscapedPath()
			_, _ = io.WriteString(w, `{"operation_id":"op_rollback"}`)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := os.WriteFile(filepath.Join(workdir, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "test-token")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runRollback([]string{"--previous"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.HasSuffix(actionPath, "/deployments/dpl_previous/actions/rollback") {
		t.Fatalf("action path = %q, want dpl_previous rollback target", actionPath)
	}
}

func TestRunCancelSelectsLatestInFlightOperationInLinkedEnvironment(
	t *testing.T,
) {
	var cancelPath, cancelKey, cancelBody, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[
				{"id":"dpl_done","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_done","operation_state":"SUCCEEDED"},
				{"id":"dpl_running","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_running","operation_state":"RUNNING"}
			]`)
		case http.MethodPost:
			cancelPath = r.URL.EscapedPath()
			cancelKey = r.Header.Get("Idempotency-Key")
			authorization = r.Header.Get("Authorization")
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			cancelBody = string(raw)
			_, _ = io.WriteString(
				w,
				`{"id":"op_running","kind":"deploy","state":"CANCELED"}`,
			)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_human-session")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runCancel(nil, &stdout, &stderr); code != 0 {
		t.Fatalf(
			"exit=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
	if cancelPath != "/api/v1alpha/operations/op_running/cancel" ||
		!strings.HasPrefix(cancelKey, "midnight-cli-cancel-") ||
		cancelBody != "" ||
		authorization != "Bearer midn_user_human-session" {
		t.Fatalf(
			"cancel path=%q key=%q body=%q authorization=%q",
			cancelPath,
			cancelKey,
			cancelBody,
			authorization,
		)
	}
}

func TestRedeployPendingAttemptReusesUnknownOutcomeAndRotatesAfterSuccess(t *testing.T) {
	var actionKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `[{"id":"dpl_1","deployment_id":"dpl_1","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_1","operation_state":"SUCCEEDED","status":"ACTIVE","active":true}]`)
		case http.MethodPost:
			actionKeys = append(actionKeys, r.Header.Get("Idempotency-Key"))
			if len(actionKeys) == 1 {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("test server does not support connection hijacking")
				}
				connection, _, err := hijacker.Hijack()
				if err != nil {
					t.Fatal(err)
				}
				_ = connection.Close()
				return
			}
			_, _ = io.WriteString(w, `{"operation_id":"op_redeploy"}`)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase: server.URL, ProjectID: "prj_1", EnvironmentID: "env_prod", ServiceID: "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "test-token")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(mutationJournalDirEnv, t.TempDir())

	var firstOut, firstErr bytes.Buffer
	if code := runRedeploy([]string{"--deployment", "dpl_1"}, &firstOut, &firstErr); code == 0 {
		t.Fatalf("first redeploy succeeded after dropped response: stdout=%q", firstOut.String())
	}
	if !strings.Contains(firstErr.String(), "outcome is unknown") ||
		!strings.Contains(firstErr.String(), "--idempotency-key") {
		t.Fatalf("first stderr=%q, want stable retry guidance", firstErr.String())
	}

	var retryOut, retryErr bytes.Buffer
	if code := runRedeploy([]string{"--deployment", "dpl_1"}, &retryOut, &retryErr); code != 0 {
		t.Fatalf("retry exit=%d stdout=%q stderr=%q", code, retryOut.String(), retryErr.String())
	}
	if len(actionKeys) != 2 || actionKeys[0] == "" || actionKeys[0] != actionKeys[1] {
		t.Fatalf("action keys=%#v, want identical non-empty pending-attempt keys", actionKeys)
	}

	var nextOut, nextErr bytes.Buffer
	if code := runRedeploy([]string{"--deployment", "dpl_1"}, &nextOut, &nextErr); code != 0 {
		t.Fatalf("later redeploy exit=%d stdout=%q stderr=%q", code, nextOut.String(), nextErr.String())
	}
	if len(actionKeys) != 3 || actionKeys[2] == "" || actionKeys[2] == actionKeys[1] {
		t.Fatalf("action keys=%#v, want a new key after the prior attempt completed", actionKeys)
	}
}

func TestExplicitDeploymentActionsRejectTargetsOutsideLinkedEnvironment(t *testing.T) {
	var postCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if got := r.URL.Query().Get("environment_id"); got != "env_prod" {
				t.Fatalf("environment_id = %q, want env_prod", got)
			}
			_, _ = io.WriteString(w, `[
				{"id":"dpl_good","deployment_id":"dpl_good","service_id":"svc_1","environment_id":"env_prod","operation_id":"op_good","operation_state":"SUCCEEDED","status":"ACTIVE","active":true},
				{"id":"dpl_wrong","deployment_id":"dpl_wrong","service_id":"svc_1","environment_id":"env_other","operation_id":"op_wrong","operation_state":"SUCCEEDED","status":"SUPERSEDED"}
			]`)
		case http.MethodPost:
			postCount++
			_, _ = io.WriteString(w, `{"operation_id":"op_action"}`)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "test-token")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	for _, test := range []struct {
		name string
		run  func(io.Writer, io.Writer) int
	}{
		{
			name: "redeploy",
			run: func(stdout, stderr io.Writer) int {
				return runRedeploy([]string{"--deployment", "dpl_wrong"}, stdout, stderr)
			},
		},
		{
			name: "rollback",
			run: func(stdout, stderr io.Writer) int {
				return runRollback([]string{"--to", "dpl_wrong"}, stdout, stderr)
			},
		},
		{
			name: "remove",
			run: func(stdout, stderr io.Writer) int {
				return runRuntimeAction("remove", []string{"--deployment", "dpl_wrong"}, stdout, stderr)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := test.run(&stdout, &stderr); code == 0 {
				t.Fatalf("exit=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "was not found in linked environment env_prod") {
				t.Fatalf("stderr=%q, want linked-environment rejection", stderr.String())
			}
		})
	}
	if postCount != 0 {
		t.Fatalf("action posts=%d, want zero", postCount)
	}
}

func TestDeploymentActionsRejectProjectAutomationBeforeRequest(t *testing.T) {
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		http.Error(w, "operator-only endpoint must not be requested", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	actions := []struct {
		name string
		run  func(io.Writer, io.Writer) int
	}{
		{
			name: "redeploy",
			run: func(stdout, stderr io.Writer) int {
				return runRedeploy([]string{"--deployment", "dpl_1"}, stdout, stderr)
			},
		},
		{
			name: "rollback",
			run: func(stdout, stderr io.Writer) int {
				return runRollback([]string{"--to", "dpl_1"}, stdout, stderr)
			},
		},
		{
			name: "restart",
			run: func(stdout, stderr io.Writer) int {
				return runRuntimeAction("restart", []string{"--deployment", "dpl_1"}, stdout, stderr)
			},
		},
		{
			name: "stop",
			run: func(stdout, stderr io.Writer) int {
				return runRuntimeAction("stop", []string{"--deployment", "dpl_1"}, stdout, stderr)
			},
		},
		{
			name: "remove",
			run: func(stdout, stderr io.Writer) int {
				return runRuntimeAction("remove", []string{"--deployment", "dpl_1"}, stdout, stderr)
			},
		},
		{
			name: "cancel",
			run: func(stdout, stderr io.Writer) int {
				return runCancel(
					[]string{"--operation", "op_1"},
					stdout,
					stderr,
				)
			},
		},
	}
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv(cliconfig.EnvBearerToken, "midn_sa_project-automation")
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			before := requestCount.Load()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := action.run(&stdout, &stderr); code == 0 {
				t.Fatalf(
					"exit=0 stdout=%q stderr=%q",
					stdout.String(),
					stderr.String(),
				)
			}
			if got := requestCount.Load(); got != before {
				t.Fatalf("HTTP requests=%d, want zero", got-before)
			}
			want := "project-automation credentials cannot use human/operator deployment actions"
			if !strings.Contains(stderr.String(), want) {
				t.Fatalf("stderr=%q, want %q", stderr.String(), want)
			}
		})
	}
}

func TestRunLogsFollowBuildUsesTenantSafeOperationEvents(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.URL.Path != "/api/v1alpha/operations/op_build/events" ||
			r.URL.Query().Get("stream") != "sse" {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(
			w,
			"event: operation\ndata: {\"state\":\"SUCCEEDED\",\"message\":\"build complete\"}\n\n",
		)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "test-token")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runLogs(
		[]string{"-f", "--build", "--operation-id", "op_build"},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(requestedPath, "/observability/") {
		t.Fatalf("requested operator-only observability path %q", requestedPath)
	}
	if !strings.Contains(stdout.String(), "build complete") ||
		!strings.Contains(stdout.String(), "midnight logs: operation succeeded") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunLogsDoesNotSendTenantCredentialToOperatorRuntimeStream(t *testing.T) {
	requested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:       server.URL,
		ProjectID:     "prj_1",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_human-session")
	t.Setenv(cliconfig.EnvAPIBase, server.URL)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runLogs(
		[]string{"-f", "--operation-id", "op_runtime"},
		&stdout,
		&stderr,
	); code == 0 {
		t.Fatalf("exit=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if requested {
		t.Fatal("human credential was sent to an operator-only runtime stream")
	}
	if !strings.Contains(stderr.String(), "use --build") {
		t.Fatalf("stderr=%q, want tenant-safe recovery guidance", stderr.String())
	}
}
