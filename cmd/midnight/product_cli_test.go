package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/cliconfig"
)

func setCLIEndpoint(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "test-token")
}

func setServiceConfigHumanSession(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetToken(apiBase, "midn_admin_service-config-recovery"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(apiBase, cliconfig.HumanSessionCredential{
		Token: "midn_user_service-config",
	}); err != nil {
		t.Fatal(err)
	}
}

func setEnvironmentUpdateHumanSession(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetToken(apiBase, "midn_admin_environment-recovery"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(apiBase, cliconfig.HumanSessionCredential{
		Token: "midn_user_environment-update",
	}); err != nil {
		t.Fatal(err)
	}
}

func setVariableHumanSession(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetToken(apiBase, "midn_admin_preserved-variable-recovery"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(apiBase, cliconfig.HumanSessionCredential{
		Token: "midn_user_variables",
	}); err != nil {
		t.Fatal(err)
	}
}

func setVariableOperatorRecovery(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetToken(apiBase, "variable-token"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectListUsesEnvBaseAndPrintsTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/projects" {
			t.Fatalf("request = %s %s, want GET /api/v1alpha/projects", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"prj_1","slug":"alpha","display_name":"Alpha"}]`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runProjectList(nil, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "prj_1") || !strings.Contains(out, "alpha") || !strings.Contains(out, "Alpha") {
		t.Fatalf("stdout = %q, want project table", out)
	}
}

func TestProductExplicitCredentialsOverrideEnvironmentAndStoredCredential(t *testing.T) {
	var environmentRequests int
	environmentServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		environmentRequests++
	}))
	defer environmentServer.Close()

	explicitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer explicit-token" {
			t.Fatalf("Authorization = %q, want explicit token", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer explicitServer.Close()

	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	if err := cliconfig.SetToken(explicitServer.URL, "stored-token"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envAPIBase, environmentServer.URL)
	t.Setenv(cliconfig.EnvBearerToken, "project-token")
	t.Setenv(envAPIToken, "legacy-token")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runProjectList([]string{
		"--api-base", explicitServer.URL,
		"--token", "explicit-token",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if environmentRequests != 0 {
		t.Fatalf("environment endpoint requests = %d, want explicit endpoint only", environmentRequests)
	}
}

func TestProductUsesProjectEndpointAndStoredServerCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer stored-token" {
			t.Fatalf("Authorization = %q, want stored token", got)
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	workdir := t.TempDir()
	t.Chdir(workdir)
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, "")
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase:   server.URL,
		ProjectID: "prj_1",
		ServiceID: "svc_1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetToken(server.URL, "stored-token"); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runProjectList(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
}

func TestProductFailsBeforeRequestWhenTokenIsMissing(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runProjectList(
		[]string{"--api-base", "https://api.midnight.invalid"},
		&stdout,
		&stderr,
	)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no token") {
		t.Fatalf("stderr = %q, want typed missing-token message", stderr.String())
	}
}

func TestProjectCreateSendsAuthAndIdempotency(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/projects" {
			t.Fatalf("request = %s %s, want POST /api/v1alpha/projects", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "create-alpha" {
			t.Fatalf("Idempotency-Key = %q, want create-alpha", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prj_1","slug":"alpha","display_name":"Alpha"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)
	t.Setenv(envAPIToken, "test-token")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runProjectCreate([]string{"--workspace-id", "wrk_alpha", "--name", "Alpha", "--idempotency-key", "create-alpha", "alpha"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotBody["workspace_id"] != "wrk_alpha" || gotBody["slug"] != "alpha" || gotBody["display_name"] != "Alpha" {
		t.Fatalf("body = %#v, want project create body", gotBody)
	}
	if !strings.Contains(stdout.String(), "project created: prj_1 (alpha)") {
		t.Fatalf("stdout = %q, want created summary", stdout.String())
	}
}

func TestProjectCreateReusesExactIntentKeyAfterDroppedResponse(t *testing.T) {
	var keys []string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, _ = io.Copy(io.Discard, r.Body)
		if requests == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack dropped response: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"prj_1","slug":"alpha","display_name":"Alpha"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	args := []string{"--workspace-id", "wrk_alpha", "--name", "Alpha", "alpha"}
	var stdout, stderr bytes.Buffer
	if code := runProjectCreate(args, &stdout, &stderr); code != 1 {
		t.Fatalf("dropped response exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outcome is unknown") {
		t.Fatalf("dropped response stderr = %q, want safe retry guidance", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runProjectCreate(args, &stdout, &stderr); code != 0 {
		t.Fatalf("retry exit = %d stderr=%q", code, stderr.String())
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys = %#v, want same non-empty exact-intent key", keys)
	}
}

func TestServiceCreateRequiresProjectID(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runServiceCreate([]string{"web"}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--project-id") {
		t.Fatalf("stderr = %q, want project id usage", stderr.String())
	}
}

func TestServiceConfigGetUsesEnvironmentScopedREST(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_web/environments/env_prod/config"
		if r.Method != http.MethodGet || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want GET %s", r.Method, r.URL.Path, wantPath)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_service-config" {
			t.Fatalf("Authorization headers = %#v, want active human session only", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"project_id":"prj_1","service_id":"svc_web","environment_id":"env_prod",
			"version":3,"explicit":true,"builder":"railpack","dockerfile_path":"Dockerfile",
			"target_port":8080,"health_kind":"tcp","health_path":"/",
			"health_timeout_seconds":120,"health_interval_seconds":2,
				"restart_policy":"unless-stopped","replicas":1,
				"overlap_enabled":true,"drain_grace_seconds":20,
				"runtime_kind":"service","cron_schedule":"",
				"cron_timeout_seconds":900,"cron_max_attempts":1,
				"cron_retry_delay_seconds":30
			}`))
	}))
	defer server.Close()
	setServiceConfigHumanSession(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runService([]string{"config", "get", "--environment-id", "env_prod", "svc_web"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"svc_web", "env_prod", "3 (explicit)", "railpack", "RUNTIME", "service"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestServiceConfigPutSendsCASBodyAndIdempotency(t *testing.T) {
	var got cliPutServiceConfigRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_web/environments/env_prod/config"
		if r.Method != http.MethodPut || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want PUT %s", r.Method, r.URL.Path, wantPath)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_service-config" {
			t.Fatalf("Authorization headers = %#v, want active human session only", got)
		}
		if values := r.Header.Values("Idempotency-Key"); len(values) != 1 || values[0] != "config-v4" {
			t.Fatalf("Idempotency-Key headers = %#v, want singleton config-v4", values)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"project_id":"prj_1","service_id":"svc_web","environment_id":"env_prod",
			"version":5,"explicit":true,"builder":"dockerfile","dockerfile_path":"deploy/Dockerfile",
			"target_port":3000,"health_kind":"http","health_path":"/health",
			"health_timeout_seconds":120,"health_interval_seconds":2,
				"restart_policy":"on-failure","restart_retries":3,"replicas":1,
				"overlap_enabled":true,"drain_grace_seconds":20,
				"runtime_kind":"cron","cron_schedule":"*/5 * * * *",
				"cron_timeout_seconds":600,"cron_max_attempts":2,
				"cron_retry_delay_seconds":45
			}`))
	}))
	defer server.Close()
	setServiceConfigHumanSession(t, server.URL)

	configPath := filepath.Join(t.TempDir(), "service-config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"expected_version":4,
		"root_directory":"apps/web",
		"builder":"dockerfile",
		"dockerfile_path":"deploy/Dockerfile",
		"build_command":"npm run build",
		"start_command":"node server.js",
		"predeploy_command":"",
		"target_port":3000,
		"health_kind":"http",
		"health_path":"/health",
		"health_port":3000,
		"health_timeout_seconds":120,
		"health_interval_seconds":2,
		"restart_policy":"on-failure",
		"restart_retries":3,
			"replicas":1,
			"overlap_enabled":true,
			"drain_grace_seconds":20,
			"runtime_kind":"cron",
			"cron_schedule":"*/5 * * * *",
			"cron_timeout_seconds":600,
			"cron_max_attempts":2,
			"cron_retry_delay_seconds":45
		}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runService([]string{
		"config", "put",
		"--environment-id", "env_prod",
		"--file", configPath,
		"--idempotency-key", "config-v4",
		"svc_web",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if got.ExpectedVersion != 4 || got.RootDirectory != "apps/web" ||
		got.HealthPort != 3000 || got.RestartRetries != 3 ||
		got.RuntimeKind != "cron" || got.CronSchedule != "*/5 * * * *" ||
		got.CronTimeoutSeconds != 600 || got.CronMaxAttempts != 2 ||
		got.CronRetryDelaySeconds != 45 {
		t.Fatalf("request body = %#v", got)
	}
	if !strings.Contains(stdout.String(), "version 5") {
		t.Fatalf("stdout = %q, want saved version", stdout.String())
	}
}

func TestServiceConfigPutGeneratesStableExactIntentKey(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_web/environments/env_prod/config"
		if r.Method != http.MethodPut || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want PUT %s", r.Method, r.URL.Path, wantPath)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_service-config" {
			t.Fatalf("Authorization headers = %#v, want active human session only", got)
		}
		values := r.Header.Values("Idempotency-Key")
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			t.Fatalf("Idempotency-Key headers = %#v, want one generated value", values)
		}
		keys = append(keys, values[0])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"project_id":"prj_1","service_id":"svc_web","environment_id":"env_prod",
			"version":1,"explicit":true,"builder":"auto","dockerfile_path":"Dockerfile",
			"target_port":8080,"health_kind":"tcp","health_path":"/",
			"health_timeout_seconds":300,"health_interval_seconds":2,
			"restart_policy":"unless-stopped","replicas":1,
			"overlap_enabled":true,"drain_grace_seconds":20
		}`))
	}))
	defer server.Close()
	setServiceConfigHumanSession(t, server.URL)

	configPath := filepath.Join(t.TempDir(), "service-config.json")
	if err := os.WriteFile(configPath, []byte(`{"expected_version":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := runServiceConfigPut([]string{
			"--environment-id", "env_prod",
			"--file", configPath,
			"svc_web",
		}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d stderr=%q", code, stderr.String())
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("generated keys = %#v, want one stable exact-intent key", keys)
	}
	if !strings.HasPrefix(keys[0], "midnight-service-config-put-v1-") {
		t.Fatalf("generated key = %q, want versioned service-config prefix", keys[0])
	}
	baseRequest := cliPutServiceConfigRequest{ExpectedVersion: 0}
	changedRequest := baseRequest
	changedRequest.ExpectedVersion = 1
	for name, changed := range map[string]string{
		"service": deterministicServiceConfigPutIdempotencyKey("svc_api", "env_prod", baseRequest),
		"environment": deterministicServiceConfigPutIdempotencyKey(
			"svc_web",
			"env_staging",
			baseRequest,
		),
		"body": deterministicServiceConfigPutIdempotencyKey("svc_web", "env_prod", changedRequest),
	} {
		if changed == keys[0] {
			t.Fatalf("%s change reused exact-intent key %q", name, changed)
		}
	}
}

func TestServiceConfigPutRejectsStrictBodyViolationsBeforeHTTP(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStderr string
	}{
		{name: "null object", body: "null", wantStderr: "expected one non-null JSON object"},
		{name: "missing expected version", body: `{}`, wantStderr: "expected_version is required"},
		{name: "null expected version", body: `{"expected_version":null}`, wantStderr: "expected_version is required"},
		{name: "unknown property", body: `{"expected_version":0,"mystery":true}`, wantStderr: "unknown field"},
		{name: "trailing json", body: `{"expected_version":0} {}`, wantStderr: "expected one JSON object"},
		{
			name:       "canonical body too large",
			body:       `{"expected_version":0,"build_command":"` + strings.Repeat("x", 32768) + `"}`,
			wantStderr: "request body exceeds 32768 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "service-config.json")
			if err := os.WriteFile(configPath, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runServiceConfigPut([]string{
				"--environment-id", "env_prod",
				"--file", configPath,
				"svc_web",
			}, &stdout, &stderr)
			if code != 64 {
				t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestEnvironmentCreateSendsExplicitIdempotencyKey(t *testing.T) {
	var gotBody struct {
		ProjectID   string `json:"project_id"`
		Slug        string `json:"slug"`
		Name        string `json:"name"`
		Tier        string `json:"tier"`
		IsProtected bool   `json:"is_protected"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/environments" {
			t.Fatalf("request = %s %s, want POST /api/v1alpha/environments", r.Method, r.URL.Path)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_environment-create" {
			t.Fatalf("Authorization headers = %#v", got)
		}
		if got := r.Header.Values("Idempotency-Key"); len(got) != 1 || got[0] != "environment-create-retry-1" {
			t.Fatalf("Idempotency-Key headers = %#v", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"env_staging","project_id":"prj_alpha","slug":"staging",
			"name":"Staging","tier":"staging","is_protected":false
		}`))
	}))
	defer server.Close()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, server.URL)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_environment-create")
	t.Setenv(envAPIToken, "")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runEnvironmentCreate([]string{
		"--project-id", "prj_alpha",
		"--name", "Staging",
		"--tier", "staging",
		"--idempotency-key", "environment-create-retry-1",
		"staging",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotBody.ProjectID != "prj_alpha" ||
		gotBody.Slug != "staging" ||
		gotBody.Name != "Staging" ||
		gotBody.Tier != "staging" ||
		gotBody.IsProtected {
		t.Fatalf("body = %#v, want ordinary environment create", gotBody)
	}
	if !strings.Contains(stdout.String(), "environment created: env_staging (staging)") {
		t.Fatalf("stdout = %q, want created summary", stdout.String())
	}
}

func TestEnvironmentCreateGeneratesIdempotencyKeyWhenOmitted(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		values := r.Header.Values("Idempotency-Key")
		if len(values) != 1 ||
			!strings.HasPrefix(values[0], "midnight-cli-environment-create-") {
			t.Fatalf("Idempotency-Key headers = %#v", values)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"env_dev","project_id":"prj_alpha","slug":"dev",
			"name":"dev","tier":"development","is_protected":false
		}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runEnvironmentCreate(
		[]string{"--project-id", "prj_alpha", "dev"},
		stdout,
		stderr,
	); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestEnvironmentUpdateUsesHumanSessionAndStrictPartialBody(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1alpha/environments/env_staging" {
			t.Fatalf("request = %s %s, want PATCH /api/v1alpha/environments/env_staging", r.Method, r.URL.Path)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_environment-update" {
			t.Fatalf("Authorization headers = %#v, want active human session only", got)
		}
		if got := r.Header.Values("Idempotency-Key"); len(got) != 1 || got[0] != "environment-update-retry-1" {
			t.Fatalf("Idempotency-Key headers = %#v, want singleton explicit key", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"env_staging","project_id":"prj_alpha","slug":"staging",
			"name":"Staging Next","tier":"staging","is_protected":false,
			"resource_version":8
		}`))
	}))
	defer server.Close()
	setEnvironmentUpdateHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runEnvironmentUpdate([]string{
		"--expected-version", "7",
		"--name", "  Staging Next  ",
		"--idempotency-key", "environment-update-retry-1",
		"env_staging",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if len(body) != 2 {
		t.Fatalf("body fields = %v, want expected_version and name only", body)
	}
	var expectedVersion int64
	if err := json.Unmarshal(body["expected_version"], &expectedVersion); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := json.Unmarshal(body["name"], &name); err != nil {
		t.Fatal(err)
	}
	if expectedVersion != 7 || name != "Staging Next" {
		t.Fatalf("body = %v, want trimmed exact partial update", body)
	}
	for _, forbidden := range []string{
		"project_id", "is_protected", "source_id", "copy_config",
		"copy_domains", "copy_volumes", "copy_deployments",
	} {
		if _, present := body[forbidden]; present {
			t.Fatalf("body unexpectedly contains recovery-only field %q: %v", forbidden, body)
		}
	}
	if !strings.Contains(stdout.String(), "environment updated: env_staging (staging), version 8") {
		t.Fatalf("stdout = %q, want update summary with new resource version", stdout.String())
	}
}

func TestEnvironmentUpdateDerivesStableExactIntentKey(t *testing.T) {
	keys := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Idempotency-Key")
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			t.Fatalf("Idempotency-Key headers = %#v, want one generated value", values)
		}
		keys = append(keys, values[0])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"env_staging","project_id":"prj_alpha","slug":"qa",
			"name":"Staging","tier":"staging","is_protected":false,
			"resource_version":8
		}`))
	}))
	defer server.Close()
	setEnvironmentUpdateHumanSession(t, server.URL)

	for range 2 {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := runEnvironmentUpdate([]string{
			"--expected-version", "7",
			"--slug", "qa",
			"env_staging",
		}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d stderr=%q", code, stderr.String())
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("generated keys = %#v, want stable exact-intent key", keys)
	}
	if !strings.HasPrefix(keys[0], "midnight-environment-update-v1-") {
		t.Fatalf("generated key = %q, want versioned environment-update prefix", keys[0])
	}
	slug := "qa"
	base := cliEnvironmentUpdateRequest{ExpectedVersion: 7, Slug: &slug}
	changedSlug := "qa-next"
	changedBody := cliEnvironmentUpdateRequest{ExpectedVersion: 7, Slug: &changedSlug}
	for change, generated := range map[string]string{
		"target": deterministicEnvironmentUpdateIdempotencyKey("env_qa", base),
		"body":   deterministicEnvironmentUpdateIdempotencyKey("env_staging", changedBody),
	} {
		if generated == keys[0] {
			t.Fatalf("%s change reused exact-intent key %q", change, generated)
		}
	}
}

func TestEnvironmentUpdateRejectsRecoveryAndExternalCredentialsBeforeHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	t.Run("stored recovery", func(t *testing.T) {
		t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
		t.Setenv(envAPIBase, server.URL)
		t.Setenv(envBaseURL, "")
		t.Setenv(cliconfig.EnvBearerToken, "")
		t.Setenv(envAPIToken, "")
		if err := cliconfig.SetToken(server.URL, "midn_admin_environment-recovery"); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runEnvironmentUpdate([]string{
			"--expected-version", "7",
			"--name", "Staging Next",
			"env_staging",
		}, &stdout, &stderr)
		if code != 64 || !strings.Contains(stderr.String(), "requires a human session") {
			t.Fatalf("exit = %d stderr=%q, want human-session preflight failure", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("explicit external", func(t *testing.T) {
		t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
		t.Setenv(envAPIBase, "")
		t.Setenv(envBaseURL, "")
		t.Setenv(cliconfig.EnvBearerToken, "")
		t.Setenv(envAPIToken, "")
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runEnvironmentUpdate([]string{
			"--api-base", server.URL,
			"--token", "external-token",
			"--expected-version", "7",
			"--name", "Staging Next",
			"env_staging",
		}, &stdout, &stderr)
		if code != 64 || !strings.Contains(stderr.String(), "requires a human session") {
			t.Fatalf("exit = %d stderr=%q, want human-session preflight failure", code, stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	})

	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want credential rejection before HTTP", requests)
	}
}

func TestEnvironmentUpdateRejectsInvalidIntentBeforeHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	setEnvironmentUpdateHumanSession(t, server.URL)

	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "expected version omitted",
			args:       []string{"--name", "QA", "env_staging"},
			wantStderr: "--expected-version must be positive",
		},
		{
			name:       "expected version zero",
			args:       []string{"--expected-version", "0", "--name", "QA", "env_staging"},
			wantStderr: "--expected-version must be positive",
		},
		{
			name:       "expected version negative",
			args:       []string{"--expected-version=-1", "--name", "QA", "env_staging"},
			wantStderr: "--expected-version must be positive",
		},
		{
			name:       "no metadata",
			args:       []string{"--expected-version", "1", "env_staging"},
			wantStderr: "at least one of --slug, --name, or --tier is required",
		},
		{
			name:       "blank metadata",
			args:       []string{"--expected-version", "1", "--name", "  ", "env_staging"},
			wantStderr: "--name cannot be blank",
		},
		{
			name:       "invalid tier",
			args:       []string{"--expected-version", "1", "--tier", "live", "env_staging"},
			wantStderr: "--tier must be production, staging, preview, or development",
		},
		{
			name: "invalid slug",
			args: []string{
				"--expected-version", "1",
				"--slug", "QA_bad",
				"env_staging",
			},
			wantStderr: "--slug must start with a lowercase letter",
		},
		{
			name: "name too long",
			args: []string{
				"--expected-version", "1",
				"--name", strings.Repeat("x", 201),
				"env_staging",
			},
			wantStderr: "--name must be at most 200 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := runEnvironmentUpdate(tt.args, &stdout, &stderr); code != 64 {
				t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want local validation before HTTP", requests)
	}
}

func TestEnvironmentUpdateRejectsMismatchedSuccessResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"env_wrong","project_id":"prj_alpha","slug":"qa",
			"name":"Staging","tier":"staging","is_protected":false,
			"resource_version":8
		}`))
	}))
	defer server.Close()
	setEnvironmentUpdateHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runEnvironmentUpdate([]string{
		"--expected-version", "7",
		"--slug", "qa",
		"env_staging",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `environment id "env_wrong" does not match requested id "env_staging"`) {
		t.Fatalf("stderr = %q, want wrong-target response rejection", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no success claim", stdout.String())
	}
}

func TestValidateEnvironmentUpdateResponseRejectsVersionAndMetadataDrift(t *testing.T) {
	slug, name, tier := "qa", "QA", "staging"
	request := cliEnvironmentUpdateRequest{
		ExpectedVersion: 7,
		Slug:            &slug,
		Name:            &name,
		Tier:            &tier,
	}
	base := cliEnvironment{
		ID:              "env_staging",
		Slug:            slug,
		Name:            name,
		Tier:            tier,
		ResourceVersion: 8,
	}
	if err := validateEnvironmentUpdateResponse("env_staging", request, base); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*cliEnvironment)
		want   string
	}{
		{name: "version", mutate: func(value *cliEnvironment) { value.ResourceVersion = 7 }, want: "resource_version"},
		{name: "slug", mutate: func(value *cliEnvironment) { value.Slug = "other" }, want: "slug"},
		{name: "name", mutate: func(value *cliEnvironment) { value.Name = "Other" }, want: "name"},
		{name: "tier", mutate: func(value *cliEnvironment) { value.Tier = "preview" }, want: "tier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := base
			tt.mutate(&value)
			err := validateEnvironmentUpdateResponse("env_staging", request, value)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q mismatch", err, tt.want)
			}
		})
	}
}

func TestEnvironmentDuplicateCopyConfigSendsReplayKeyAndPrintsOmissions(t *testing.T) {
	var gotBody struct {
		Slug       string `json:"slug"`
		Name       string `json:"name"`
		CopyConfig bool   `json:"copy_config"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/environments/env_staging/duplicate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "duplicate-staging-qa" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"env_qa","project_id":"prj_one","slug":"qa","name":"QA","tier":"staging",
			"copy_config":true,"copied_service_configs":2,"copied_variables":3,
			"copied_variable_versions":5,
			"omitted_resources":[
				{"resource":"domains","count":1,"reason":"create explicitly"},
				{"resource":"volumes","count":0,"reason":"restore explicitly"},
				{"resource":"deployments","count":4,"reason":"history remains in source"}
			]
		}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runEnvironmentDuplicate([]string{
		"--slug", "qa",
		"--name", "QA",
		"--copy-config",
		"--idempotency-key", "duplicate-staging-qa",
		"env_staging",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if gotBody.Slug != "qa" || gotBody.Name != "QA" || !gotBody.CopyConfig {
		t.Fatalf("body=%#v", gotBody)
	}
	output := stdout.String()
	for _, want := range []string{
		"environment duplicated: env_qa (qa)",
		"2 service config(s), 3 variable(s), 5 variable version(s)",
		"not copied: domains (1)",
		"not copied: deployments (4)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout=%q, missing %q", output, want)
		}
	}
	if strings.Contains(output, "not copied: volumes") {
		t.Fatalf("stdout=%q, zero-count omission should stay quiet", output)
	}
}

func TestEnvironmentDuplicateCopyConfigRequiresReplayKeyBeforeRequest(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runEnvironmentDuplicate([]string{"--slug", "qa", "--copy-config", "env_staging"}, stdout, stderr)
	if code != 64 || !strings.Contains(stderr.String(), "--idempotency-key is required") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestVariableSetPostsMetadataOnlyRequest(t *testing.T) {
	var gotBody struct {
		Name            string `json:"name"`
		WorkspaceID     string `json:"workspace_id"`
		ProjectID       string `json:"project_id"`
		EnvironmentID   string `json:"environment_id"`
		ServiceID       string `json:"service_id"`
		Scope           string `json:"scope"`
		Value           string `json:"value"`
		ExpectedVersion *int   `json:"expected_version"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/variables" {
			t.Fatalf("request = %s %s, want POST /api/v1alpha/variables", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer variable-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "set-db-url" {
			t.Fatalf("Idempotency-Key = %q, want set-db-url", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sealed_database_url_abc","name":"DATABASE_URL","scope":"sealed","project_id":"prj_1","environment_id":"env_prod","service_id":"svc_api","version":1,"length_bucket":"17-64"}`))
	}))
	defer server.Close()
	setVariableOperatorRecovery(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runVariableSet([]string{
		"--project-id", "prj_1",
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--scope", "sealed",
		"--expected-version", "7",
		"--value", "postgres://user:super-secret@host/db",
		"--idempotency-key", "set-db-url",
		"DATABASE_URL",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotBody.Name != "DATABASE_URL" ||
		gotBody.ProjectID != "prj_1" ||
		gotBody.EnvironmentID != "env_prod" ||
		gotBody.ServiceID != "svc_api" ||
		gotBody.Scope != "sealed" ||
		gotBody.ExpectedVersion == nil ||
		*gotBody.ExpectedVersion != 7 ||
		gotBody.Value != "postgres://user:super-secret@host/db" {
		t.Fatalf("body = %#v, want variable set request", gotBody)
	}
	if strings.Contains(stdout.String(), "super-secret") || strings.Contains(stdout.String(), "postgres://") {
		t.Fatalf("stdout leaked variable value: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "variable set: sealed_database_url_abc (DATABASE_URL, sealed)") {
		t.Fatalf("stdout = %q, want set summary", stdout.String())
	}
}

func TestVariableDeleteUsesVersionAndNeverPrintsValue(t *testing.T) {
	var gotBody variableDeleteRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1alpha/variables/sealed_token_abc" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "delete-token" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setVariableOperatorRecovery(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableDelete([]string{
		"--project-id", "prj_1",
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--expected-version", "3",
		"--idempotency-key", "delete-token",
		"sealed_token_abc",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotBody.ProjectID != "prj_1" || gotBody.EnvironmentID != "env_prod" ||
		gotBody.ServiceID != "svc_api" || gotBody.ExpectedVersion != 3 {
		t.Fatalf("body = %#v", gotBody)
	}
	if stdout.String() != "variable deleted: sealed_token_abc\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestVariableListPrintsMetadataWithoutValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/variables" {
			t.Fatalf("request = %s %s, want GET /api/v1alpha/variables", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"var_public_url_abc","name":"PUBLIC_URL","scope":"plain","project_id":"prj_1","environment_id":"env_prod","service_id":"svc_web","length_bucket":"17-64"}]`))
	}))
	defer server.Close()
	setVariableOperatorRecovery(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runVariableList(nil, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "var_public_url_abc") || !strings.Contains(out, "PUBLIC_URL") || !strings.Contains(out, "17-64") { // not-a-failure-code
		t.Fatalf("stdout = %q, want variable metadata table", out)
	}
	if strings.Contains(out, "https://") {
		t.Fatalf("stdout leaked a variable value: %q", out)
	}
}

func TestVariableSetRequiresValue(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runVariableSet([]string{"DATABASE_URL"}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "exactly one of --value or --stdin is required") {
		t.Fatalf("stderr = %q, want value usage", stderr.String())
	}
}

func TestVariableHumanListUsesExactTenantRouteAndShowsVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_web/environments/env_prod/variables"
		if r.Method != http.MethodGet || r.URL.Path != wantPath || r.URL.RawQuery != "" {
			t.Fatalf("request = %s %s?%s, want GET %s", r.Method, r.URL.Path, r.URL.RawQuery, wantPath)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_variables" {
			t.Fatalf("Authorization headers = %#v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"id":"var_public_url","name":"PUBLIC_URL","scope":"plain",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_web",
			"version":7,"length_bucket":"17-64","value_kind":"literal"
		}]`))
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableList([]string{
		"--environment-id", "env_prod",
		"--service-id", "svc_web",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "VERSION") || !strings.Contains(out, "7") {
		t.Fatalf("stdout = %q, want version column", out)
	}
}

func TestVariableHumanSetUsesCreateOnlyBodyStdinAndStableHMACKey(t *testing.T) {
	const secret = "postgres://user:private-password@db/app"
	var keys []string
	var bodies []map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_api/environments/env_prod/variables"
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
		}
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_variables" {
			t.Fatalf("Authorization headers = %#v", got)
		}
		if got := r.Header.Values("Idempotency-Key"); len(got) != 1 || strings.TrimSpace(got[0]) == "" {
			t.Fatalf("Idempotency-Key headers = %#v", got)
		} else {
			keys = append(keys, got[0])
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"sealed_database_url","name":"DATABASE_URL","scope":"sealed",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_api",
			"version":1,"length_bucket":"17-64","value_kind":"literal"
		}`))
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	for range 2 {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runVariableSetWithInput([]string{
			"--environment-id", "env_prod",
			"--service-id", "svc_api",
			"--scope", "sealed",
			"--stdin",
			"DATABASE_URL",
		}, strings.NewReader(secret), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit = %d stderr=%q", code, stderr.String())
		}
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("output leaked secret: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}
	if len(keys) != 2 || keys[0] != keys[1] {
		t.Fatalf("keys = %#v, want stable exact-intent HMAC", keys)
	}
	if !strings.HasPrefix(keys[0], "midnight-variable-create-v1-") || strings.Contains(keys[0], secret) {
		t.Fatalf("generated key = %q, want opaque versioned key", keys[0])
	}
	for _, body := range bodies {
		if len(body) != 3 || body["name"] == nil || body["scope"] == nil || body["value"] == nil {
			t.Fatalf("human create body = %s, want exact name/scope/value", mustMarshalJSON(t, body))
		}
		var value string
		if err := json.Unmarshal(body["value"], &value); err != nil || value != secret {
			t.Fatalf("value = %q err=%v, want exact stdin bytes", value, err)
		}
		for _, forbidden := range []string{"workspace_id", "project_id", "environment_id", "service_id", "expected_version"} {
			if _, ok := body[forbidden]; ok {
				t.Fatalf("human create body contains forbidden %q: %s", forbidden, mustMarshalJSON(t, body))
			}
		}
	}
}

func TestVariableHumanUpdateUsesExactIDCASAndPreservesStdinBytes(t *testing.T) {
	const secret = "line one\nline two\n"
	var gotBody map[string]json.RawMessage
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_api/environments/env_prod/variables/sealed_database_url"
		if r.Method != http.MethodPatch || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want PATCH %s", r.Method, r.URL.Path, wantPath)
		}
		gotKey = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"sealed_database_url","name":"DATABASE_URL","scope":"sealed",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_api",
			"version":4,"length_bucket":"17-64","value_kind":"literal"
		}`))
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableUpdateWithInput([]string{
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--expected-version", "3",
		"--stdin",
		"sealed_database_url",
	}, strings.NewReader(secret), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if len(gotBody) != 2 || gotBody["expected_version"] == nil || gotBody["value"] == nil {
		t.Fatalf("human update body = %s, want exact expected_version/value", mustMarshalJSON(t, gotBody))
	}
	if _, ok := gotBody["scope"]; ok {
		t.Fatalf("human update body = %s, omitted scope must retain current scope", mustMarshalJSON(t, gotBody))
	}
	var value string
	if err := json.Unmarshal(gotBody["value"], &value); err != nil || value != secret {
		t.Fatalf("value = %q err=%v, want exact stdin bytes", value, err)
	}
	if !strings.HasPrefix(gotKey, "midnight-variable-update-v1-") || strings.Contains(gotKey, secret) {
		t.Fatalf("generated key = %q, want opaque versioned key", gotKey)
	}
	if strings.Contains(stdout.String(), secret) || !strings.Contains(stdout.String(), "version 4") {
		t.Fatalf("stdout = %q, want redacted versioned summary", stdout.String())
	}

	path := humanVariableItemAPIPath("svc_api", "env_prod", "sealed_database_url")
	base := humanVariableUpdateRequest{ExpectedVersion: 3, Value: secret}
	baseKey := deterministicVariableIdempotencyKey("midn_user_variables", "update", http.MethodPatch, path, base)
	if changed := deterministicVariableIdempotencyKey("midn_user_variables", "update", http.MethodPatch, path, humanVariableUpdateRequest{
		ExpectedVersion: 3,
		Value:           secret + "changed",
	}); changed == baseKey {
		t.Fatalf("changed secret reused exact-intent key %q", changed)
	}
	if changedToken := deterministicVariableIdempotencyKey(
		"midn_user_other-session",
		"update",
		http.MethodPatch,
		path,
		base,
	); changedToken == baseKey {
		t.Fatalf("different human session reused keyed intent %q", changedToken)
	}
}

func TestVariableHumanDeleteUsesExactItemAndCASOnly(t *testing.T) {
	var gotBody map[string]json.RawMessage
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v1alpha/services/svc_api/environments/env_prod/variables/var_token"
		if r.Method != http.MethodDelete || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want DELETE %s", r.Method, r.URL.Path, wantPath)
		}
		gotKey = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableDelete([]string{
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--expected-version", "9",
		"var_token",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if len(gotBody) != 1 || string(gotBody["expected_version"]) != "9" {
		t.Fatalf("human delete body = %s, want exact CAS body", mustMarshalJSON(t, gotBody))
	}
	if !strings.HasPrefix(gotKey, "midnight-variable-delete-v1-") {
		t.Fatalf("generated key = %q, want versioned key", gotKey)
	}
}

func TestVariableHumanCreateTreatsExplicitEmptyValueAndEmptyStdinAsSameIntent(t *testing.T) {
	var keys []string
	var values []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.Path != "/api/v1alpha/services/svc_api/environments/env_prod/variables" {
			t.Fatalf("request = %s %s, want nested human create", r.Method, r.URL.Path)
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		var body humanVariableCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		values = append(values, body.Value)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"var_empty","name":"EMPTY","scope":"secret",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_api",
			"version":1,"length_bucket":"empty","value_kind":"literal"
		}`))
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	invocations := []struct {
		args  []string
		stdin string
	}{
		{
			args: []string{
				"--environment-id", "env_prod",
				"--service-id", "svc_api",
				"--value", "",
				"EMPTY",
			},
		},
		{
			args: []string{
				"--environment-id", "env_prod",
				"--service-id", "svc_api",
				"--stdin",
				"EMPTY",
			},
		},
	}
	for _, invocation := range invocations {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := runVariableSetWithInput(
			invocation.args,
			strings.NewReader(invocation.stdin),
			&stdout,
			&stderr,
		)
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
	if len(values) != 2 || values[0] != "" || values[1] != "" {
		t.Fatalf("submitted values = %#v, want two explicit empty values", values)
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys = %#v, want one stable empty-value intent", keys)
	}
}

func TestVariableHumanSetRejectsNonPortableNamesBeforeHTTP(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	tests := []struct {
		name         string
		variableName string
	}{
		{name: "hyphen", variableName: "LEGACY-NAME"},
		{name: "leading digit", variableName: "1TOKEN"},
		{name: "whitespace", variableName: "TOKEN NAME"},
		{name: "leading whitespace", variableName: " TOKEN"},
		{name: "over 255 bytes", variableName: strings.Repeat("A", humanVariableNameLimitBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runVariableSetWithInput([]string{
				"--environment-id", "env_prod",
				"--service-id", "svc_api",
				"--value", "secret",
				tt.variableName,
			}, strings.NewReader(""), &stdout, &stderr)
			if code != 64 || !strings.Contains(stderr.String(), "[A-Za-z_][A-Za-z0-9_]*") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want portable-name rejection before HTTP", requests)
	}
}

func TestVariableOperatorRecoveryPreservesLegacyHyphenatedName(t *testing.T) {
	var got variableSetRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/variables" {
			t.Fatalf("request = %s %s, want legacy global POST", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id":"var_legacy","name":"LEGACY-NAME","scope":"secret",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_api",
			"version":1,"length_bucket":"1-16"
		}`))
	}))
	defer server.Close()
	setVariableOperatorRecovery(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableSetWithInput([]string{
		"--workspace-id", "wrk_1",
		"--project-id", "prj_1",
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--value", "legacy",
		"LEGACY-NAME",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got.Name != "LEGACY-NAME" {
		t.Fatalf("legacy name = %q, want exact hyphenated input", got.Name)
	}
}

func TestVariableOperatorRecoveryUpdateKeepsLegacyGlobalRouteAndOwnershipBody(t *testing.T) {
	var got struct {
		WorkspaceID     string `json:"workspace_id"`
		ProjectID       string `json:"project_id"`
		EnvironmentID   string `json:"environment_id"`
		ServiceID       string `json:"service_id"`
		Scope           string `json:"scope"`
		Value           string `json:"value"`
		ExpectedVersion int    `json:"expected_version"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1alpha/variables/var_token" {
			t.Fatalf("request = %s %s, want legacy global PATCH", r.Method, r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer variable-token" {
			t.Fatalf("Authorization = %q, want stored recovery credential", authorization)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"var_token","name":"TOKEN","scope":"secret",
			"workspace_id":"wrk_1","project_id":"prj_1",
			"environment_id":"env_prod","service_id":"svc_api",
			"version":3,"length_bucket":"1-16"
		}`))
	}))
	defer server.Close()
	setVariableOperatorRecovery(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableUpdateWithInput([]string{
		"--workspace-id", "wrk_1",
		"--project-id", "prj_1",
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--scope", "secret",
		"--expected-version", "2",
		"--value", "rotated",
		"var_token",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got.WorkspaceID != "wrk_1" || got.ProjectID != "prj_1" ||
		got.EnvironmentID != "env_prod" || got.ServiceID != "svc_api" ||
		got.Scope != "secret" || got.Value != "rotated" || got.ExpectedVersion != 2 {
		t.Fatalf("legacy update body = %#v", got)
	}
}

func TestVariableHumanSetRejectsUpdateModeAndValueInputViolationsBeforeHTTP(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	tests := []struct {
		name       string
		args       []string
		stdin      string
		wantStderr string
	}{
		{
			name: "explicit version zero",
			args: []string{
				"--environment-id", "env_prod", "--service-id", "svc_api",
				"--expected-version", "0", "--value", "secret", "TOKEN",
			},
			wantStderr: "--expected-version must be at least 1",
		},
		{
			name: "set remains create only",
			args: []string{
				"--environment-id", "env_prod", "--service-id", "svc_api",
				"--expected-version", "1", "--value", "secret", "TOKEN",
			},
			wantStderr: "use `midnight variable update`",
		},
		{
			name: "value and stdin are exclusive",
			args: []string{
				"--environment-id", "env_prod", "--service-id", "svc_api",
				"--value", "secret", "--stdin", "TOKEN",
			},
			stdin:      "other",
			wantStderr: "exactly one of --value or --stdin",
		},
		{
			name: "stdin is bounded",
			args: []string{
				"--environment-id", "env_prod", "--service-id", "svc_api",
				"--stdin", "TOKEN",
			},
			stdin:      strings.Repeat("x", variableValueLimitBytes+1),
			wantStderr: "variable value exceeds 65536 bytes",
		},
		{
			name: "encoded request body is bounded",
			args: []string{
				"--environment-id", "env_prod", "--service-id", "svc_api",
				"--stdin", "TOKEN",
			},
			stdin:      strings.Repeat("x", variableValueLimitBytes),
			wantStderr: "variable request body exceeds 65536 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runVariableSetWithInput(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr)
			if code != 64 || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want local rejection", requests)
	}
}

func TestVariableCommandsRejectExternalCredentialBeforeHTTP(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{
			name: "list",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runVariableList([]string{"--environment-id", "env_prod", "--service-id", "svc_api"}, stdout, stderr)
			},
		},
		{
			name: "set",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runVariableSetWithInput([]string{"--environment-id", "env_prod", "--service-id", "svc_api", "--value", "secret", "TOKEN"}, strings.NewReader(""), stdout, stderr)
			},
		},
		{
			name: "update",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runVariableUpdateWithInput([]string{"--environment-id", "env_prod", "--service-id", "svc_api", "--expected-version", "1", "--value", "secret", "var_token"}, strings.NewReader(""), stdout, stderr)
			},
		},
		{
			name: "delete",
			run: func(stdout, stderr *bytes.Buffer) int {
				return runVariableDelete([]string{"--environment-id", "env_prod", "--service-id", "svc_api", "--expected-version", "1", "var_token"}, stdout, stderr)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := tt.run(&stdout, &stderr); code != 64 || !strings.Contains(stderr.String(), "require a human session or a stored operator recovery credential") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want credential rejection before HTTP", requests)
	}
}

func TestVariableHumanRejectsSecretBearingOrMismatchedMetadataResponse(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantStderr string
	}{
		{
			name: "secret field",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1,
				"value":"must-not-print"
			}]`,
			wantStderr: `forbidden secret field "value"`,
		},
		{
			name: "wrong service",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_other","version":1
			}]`,
			wantStderr: "does not match the requested service",
		},
		{
			name: "missing required value kind",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1
			}]`,
			wantStderr: "variable value kind is invalid",
		},
		{
			name: "unexpected nested reference field",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1,
				"value_kind":"reference",
				"references":[{"kind":"shared","name":"TOKEN","value":"must-not-print"}]
			}]`,
			wantStderr: "contains an unexpected field",
		},
		{
			name: "duplicate nested reference field",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1,
				"value_kind":"reference",
				"references":[{"kind":"shared","kind":"builtin","name":"TOKEN"}]
			}]`,
			wantStderr: "contains a duplicate field",
		},
		{
			name: "decode failure hides response contents",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1,
				"last_rotated_at":"must-not-print"
			}]`,
			wantStderr: "server returned an invalid metadata response",
		},
		{
			name: "known metadata field has wrong type",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"workspace_id":null,
				"environment_id":"env_prod","service_id":"svc_api","version":1
			}]`,
			wantStderr: "metadata field workspace_id is not a string",
		},
		{
			name: "unexpected field name stays redacted",
			response: `[{
				"id":"var_token","name":"TOKEN","scope":"secret",
				"environment_id":"env_prod","service_id":"svc_api","version":1,
				"must-not-print":"anything"
			}]`,
			wantStderr: "unexpected non-metadata field",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			setVariableHumanSession(t, server.URL)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := runVariableList([]string{
				"--environment-id", "env_prod",
				"--service-id", "svc_api",
				"--json",
			}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || strings.Contains(stderr.String(), "must-not-print") {
				t.Fatalf("secret escaped response validation: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestVariableMutationHTTPFailureNeverEchoesSubmittedValue(t *testing.T) {
	const secret = "submitted-value-must-not-print"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected value: "+secret, http.StatusBadRequest)
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableSetWithInput([]string{
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--value", secret,
		"TOKEN",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("submitted value leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "400 Bad Request") {
		t.Fatalf("stderr=%q, want safe HTTP status", stderr.String())
	}
}

func TestVariableMutationErrorEnvelopeKeepsVettedCodeButRedactsMessage(t *testing.T) {
	const secret = "submitted-value-must-not-print"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"RESOURCE_VALIDATION_FAILED","message":%q}}`, "rejected "+secret)
	}))
	defer server.Close()
	setVariableHumanSession(t, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runVariableSetWithInput([]string{
		"--environment-id", "env_prod",
		"--service-id", "svc_api",
		"--value", secret,
		"TOKEN",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("submitted value leaked: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "RESOURCE_VALIDATION_FAILED") {
		t.Fatalf("stderr=%q, want vetted machine code", stderr.String())
	}
}

func mustMarshalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestConfigApplyUsesBearerToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "midnight.json")
	if err := os.WriteFile(configPath, []byte(`{"api_version":"midnight/v1alpha","projects":[{"slug":"alpha","display_name":"Alpha"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/config/apply" {
			t.Fatalf("request = %s %s, want config apply", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer config-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "cfg-1" {
			t.Fatalf("Idempotency-Key = %q, want cfg-1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"applied":true}`))
	}))
	defer server.Close()
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "config-token")

	stdout := &bytes.Buffer{}
	if err := runConfig([]string{"apply", "--file", configPath, "--endpoint", server.URL, "--idempotency-key", "cfg-1"}, stdout); err != nil {
		t.Fatalf("runConfig() error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"applied":true`) {
		t.Fatalf("stdout = %q, want config response", stdout.String())
	}
}

func TestChangesetApplyUsesRevisionAndReplayFences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1alpha/environments/env%2Fprod/changeset/apply" {
			t.Fatalf("request = %s %s, want changeset apply", r.Method, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer changeset-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "apply-revision-7" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		if got := r.Header.Get("If-Match"); got != `"7"` {
			t.Fatalf("If-Match = %q", got)
		}
		var body map[string]int64
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["expected_revision"] != 7 {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"changeset":{"state":"applied","revision":8}}`))
	}))
	defer server.Close()

	stdout := &bytes.Buffer{}
	err := runChangeset([]string{
		"apply",
		"--environment-id", "env/prod",
		"--revision", "7",
		"--endpoint", server.URL,
		"--token", "changeset-token",
		"--idempotency-key", "apply-revision-7",
	}, stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"state":"applied"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDeployImagePostsDeployIntakePayload(t *testing.T) {
	var gotBody struct {
		EnvironmentID string `json:"environment_id"`
		Image         string `json:"image"`
		DeploymentID  string `json:"deployment_id"`
		InstanceID    string `json:"instance_id"`
		Route         struct {
			Name   string `json:"name"`
			Group  string `json:"group"`
			Host   string `json:"host"`
			Path   string `json:"path"`
			Public bool   `json:"public"`
			TLS    bool   `json:"tls"`
		} `json:"route"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/services/svc_1/deployments" {
			t.Fatalf("request = %s %s, want deploy intake path", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "deploy-alpha" {
			t.Fatalf("Idempotency-Key = %q, want deploy-alpha", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"QUEUED"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployImage([]string{
		"--service-id", "svc_1",
		"--image", "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--environment-id", "env_prod",
		"--deployment-id", "dep_1",
		"--instance-id", "inst_1",
		"--route-name", "web",
		"--route-group", "prod",
		"--route-host", "web.example.test",
		"--route-path", "/",
		"--route-tls",
		"--idempotency-key", "deploy-alpha",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotBody.EnvironmentID != "env_prod" ||
		gotBody.Image != "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		gotBody.DeploymentID != "dep_1" ||
		gotBody.InstanceID != "inst_1" ||
		gotBody.Route.Name != "web" ||
		gotBody.Route.Group != "prod" ||
		gotBody.Route.Host != "web.example.test" ||
		gotBody.Route.Path != "/" ||
		!gotBody.Route.Public ||
		!gotBody.Route.TLS {
		t.Fatalf("body = %#v, want deploy image request with route intent", gotBody)
	}
	if !strings.Contains(stdout.String(), "deploy queued: op_1 (state=QUEUED)") {
		t.Fatalf("stdout = %q, want queued operation", stdout.String())
	}
}

func TestDeployImageHumanSessionUsesNarrowAdmissionContractAndReusesExplicitKey(t *testing.T) {
	const image = "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var bodies []string
	var authorizationHeaders [][]string
	var idempotencyHeaders [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/api/v1alpha/services/svc%2Fhuman/deployments" {
			t.Fatalf("request = %s %s, want exact tenant deploy intake path", r.Method, r.URL.EscapedPath())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		authorizationHeaders = append(authorizationHeaders, r.Header.Values("Authorization"))
		idempotencyHeaders = append(idempotencyHeaders, r.Header.Values("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"op_human","kind":"deploy","state":"QUEUED"}`))
	}))
	defer server.Close()

	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, "")
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "")
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_stored-session",
	}); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--service-id", "svc/human",
		"--environment-id", "env_prod",
		"--image", image,
		"--idempotency-key", "human-retry-key",
	}
	for attempt := 0; attempt < 2; attempt++ {
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		if code := runDeployImage(args, stdout, stderr); code != 0 {
			t.Fatalf("attempt %d exit = %d stderr=%q", attempt+1, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "deploy queued: op_human (state=QUEUED)") {
			t.Fatalf("attempt %d stdout = %q", attempt+1, stdout.String())
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want two explicit-key retry attempts", len(bodies))
	}
	const wantBody = `{"environment_id":"env_prod","image":"` + image + `"}`
	for i := range bodies {
		if bodies[i] != wantBody {
			t.Fatalf("request %d body = %q, want exact two-field body %q", i+1, bodies[i], wantBody)
		}
		if got := authorizationHeaders[i]; len(got) != 1 || got[0] != "Bearer midn_user_stored-session" {
			t.Fatalf("request %d Authorization headers = %#v", i+1, got)
		}
		if got := idempotencyHeaders[i]; len(got) != 1 || got[0] != "human-retry-key" {
			t.Fatalf("request %d Idempotency-Key headers = %#v", i+1, got)
		}
	}
}

func TestDeployImageHumanEnvironmentCredentialUsesNarrowAdmissionContract(t *testing.T) {
	const image = "registry.example/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_environment-session" {
			t.Fatalf("Authorization headers = %#v", got)
		}
		if got := r.Header.Values("Idempotency-Key"); len(got) != 1 || strings.TrimSpace(got[0]) == "" {
			t.Fatalf("Idempotency-Key headers = %#v", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 2 || body["environment_id"] != "env_stage" || body["image"] != image {
			t.Fatalf("body = %#v, want exact human admission fields", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"op_env","kind":"deploy","state":"QUEUED"}`))
	}))
	defer server.Close()

	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, server.URL)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_environment-session")
	t.Setenv(envAPIToken, "")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runDeployImage([]string{
		"--service-id", "svc_env",
		"--environment-id", "env_stage",
		"--image", image,
	}, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestDeployImageHumanSessionRequestsOnlyGeneratedPublicRoute(t *testing.T) {
	const image = "registry.example/web@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer midn_user_public-route-session" {
			t.Fatalf("Authorization headers = %#v", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		const wantBody = `{"environment_id":"env_prod","image":"` + image + `","route":{"public":true}}`
		if string(body) != wantBody {
			t.Fatalf("body = %q, want exact generated-route body %q", body, wantBody)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"op_public","kind":"deploy","state":"QUEUED"}`))
	}))
	defer server.Close()

	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, server.URL)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_public-route-session")
	t.Setenv(envAPIToken, "")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runDeployImage([]string{
		"--service-id", "svc_public",
		"--environment-id", "env_prod",
		"--image", image,
		"--route-public",
		"--idempotency-key", "human-public-route",
	}, stdout, stderr); code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestDeployImageHumanSessionRejectsUnsupportedInputsBeforeHTTP(t *testing.T) {
	const image = "registry.example/web@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "human validation reached HTTP", http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, server.URL)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "midn_user_validation-session")
	t.Setenv(envAPIToken, "")

	base := []string{"--service-id", "svc_human", "--environment-id", "env_prod", "--image", image}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing environment", args: []string{"--service-id", "svc_human", "--image", image}, want: "--environment-id is required"},
		{name: "mutable image", args: []string{"--service-id", "svc_human", "--environment-id", "env_prod", "--image", "registry.example/web:latest"}, want: "immutable reference"},
		{name: "uppercase digest", args: []string{"--service-id", "svc_human", "--environment-id", "env_prod", "--image", "registry.example/web@sha256:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"}, want: "lowercase hex"},
		{name: "deployment id", args: append(append([]string{}, base...), "--deployment-id", "dep_client"), want: "--deployment-id is not available"},
		{name: "instance id", args: append(append([]string{}, base...), "--instance-id", "inst_client"), want: "--instance-id is not available"},
		{name: "source ref", args: append(append([]string{}, base...), "--source-ref", "refs/heads/main"), want: "--source-ref is reserved"},
		{name: "route name", args: append(append([]string{}, base...), "--route-name", "web"), want: "--route-name is not available"},
		{name: "route group", args: append(append([]string{}, base...), "--route-group", "prod"), want: "--route-group is not available"},
		{name: "route host", args: append(append([]string{}, base...), "--route-host", "web.example.test"), want: "--route-host is not available"},
		{name: "route public false", args: append(append([]string{}, base...), "--route-public=false"), want: "--route-public=false is ambiguous"},
		{name: "route path default", args: append(append([]string{}, base...), "--route-path", "/"), want: "--route-path is not available"},
		{name: "route tls", args: append(append([]string{}, base...), "--route-tls=false"), want: "--route-tls is not available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if code := runDeployImage(tc.args, stdout, stderr); code != 64 {
				t.Fatalf("exit = %d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.want)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("HTTP requests = %d, want zero", requests)
	}
}

func TestDeployImageRejectsSourceRefUntilBuildIntegration(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployImage([]string{
		"--service-id", "svc_1",
		"--image", "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--source-ref", "refs/heads/main",
	}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--source-ref is reserved") ||
		!strings.Contains(stderr.String(), "immutable sha256 candidate image") ||
		!strings.Contains(stderr.String(), "without --source-ref") {
		t.Fatalf("stderr = %q, want source-ref reservation message", stderr.String())
	}
}

func TestDeployImageRejectsSourceRefWithoutImageBeforeUsage(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployImage([]string{
		"--service-id", "svc_1",
		"--source-ref", "refs/heads/main",
	}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--source-ref is reserved") {
		t.Fatalf("stderr = %q, want source-ref reservation message", stderr.String())
	}
}

func TestDeployStatusFetchesOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/operations/op_1" {
			t.Fatalf("request = %s %s, want operation status path", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"RUNNING"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployStatus([]string{"--operation-id", "op_1"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "op_1\tdeploy\tRUNNING") {
		t.Fatalf("stdout = %q, want operation status row", stdout.String())
	}
}

func TestDeployLogsPrintsOperationEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/operations/op_1/events" || r.URL.Query().Get("after") != "2" {
			t.Fatalf("request = %s %s?%s, want operation events path after cursor", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: operation\n"))
		_, _ = w.Write([]byte(`data: {"id":"op_1-3","operation_id":"op_1","seq":3,"state":"RUNNING","message":"deploying"}` + "\n\n"))
		_, _ = w.Write([]byte("event: cursor\n"))
		_, _ = w.Write([]byte(`data: {"next_sequence":3}` + "\n\n"))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployEvents([]string{"--operation-id", "op_1", "--after", "2"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "event: operation") || !strings.Contains(stdout.String(), `"message":"deploying"`) {
		t.Fatalf("stdout = %q, want raw event stream", stdout.String())
	}
}

func TestDeployLogsJSONExtractsOperationEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: operation\n"))
		_, _ = w.Write([]byte(`data: {"id":"op_1-1","operation_id":"op_1","seq":1,"state":"QUEUED","message":"queued"}` + "\n\n"))
		_, _ = w.Write([]byte("event: cursor\n"))
		_, _ = w.Write([]byte(`data: {"next_sequence":1}` + "\n\n"))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployEvents([]string{"--operation-id", "op_1", "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	var events []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &events); err != nil {
		t.Fatalf("stdout = %q, want JSON operation events: %v", stdout.String(), err)
	}
	if len(events) != 1 || events[0]["message"] != "queued" {
		t.Fatalf("events = %#v, want queued operation event only", events)
	}
}

func TestDeployLogsShowsTruthfulAvailability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/operations/op_1/logs" {
			t.Fatalf("request = %s %s, want operation logs path", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"operation_id":"op_1",
			"operation_events":{"available":true,"kind":"operation_events","url":"/api/v1alpha/operations/op_1/events","count":2},
			"deploy_logs":{"available":true,"kind":"operation_events","url":"/api/v1alpha/operations/op_1/events","count":2},
			"app_logs":[
				{"surface":"build","available":false,"reason":"unavailable: no backed app log store is wired for this surface"},
				{"surface":"runtime","available":false,"reason":"unavailable: no backed app log store is wired for this surface"},
				{"surface":"http","available":false,"reason":"unavailable: no backed app log store is wired for this surface"}
			],
			"message":"Operation events are available. Build, runtime, and HTTP app logs are not wired to a backing log store in this API surface yet."
		}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployLogs([]string{"--operation-id", "op_1"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"deploy logs: available (2 events)",
		"build logs: unavailable",
		"runtime logs: unavailable",
		"http logs: unavailable",
		"not wired to a backing log store",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestDeployLogsShowsBackedEntriesAndQueryFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/operations/op_1/logs" {
			t.Fatalf("request = %s %s, want operation logs path", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("limit") != "2" || query.Get("q") != "ready" || query.Get("surface") != "runtime" || query.Get("cursor") != "lc1.opaque-token" {
			t.Fatalf("query = %s, want backed log filters", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"operation_id":"op_1",
			"operation_events":{"available":true,"kind":"operation_events","url":"/api/v1alpha/operations/op_1/events","count":4},
			"deploy_logs":{"available":true,"kind":"operation_events","url":"/api/v1alpha/operations/op_1/events","count":4},
			"app_logs":[
				{"surface":"build","available":true,"count":0,"entries":[]},
				{"surface":"runtime","available":true,"count":1,"entries":[{"at":"2026-05-01T12:00:00Z","level":"info","component":"app","message":"runtime ready","fields":{"deployment_id":"dep_1"}}],"truncated":true,"next_cursor":{"token":"lc1.next-opaque-token"}},
				{"surface":"http","available":true,"count":0,"entries":[]}
			],
			"platform_logs":[
				{"surface":"platform","available":true,"count":1,"entries":[{"at":"2026-05-01T12:00:01Z","level":"warn","component":"midnight-worker","message":"deploy retry scheduled","fields":{"operation_id":"op_1"}}]}
			],
			"message":"Operation events and configured app/platform log surfaces are returned from backed stores."
		}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployLogs([]string{"--operation-id", "op_1", "--limit", "2", "--q", "ready", "--surface", "runtime", "--cursor", "lc1.opaque-token"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"deploy logs: available (4 events)",
		"runtime logs: available (1 entries)",
		"2026-05-01T12:00:00Z info app  runtime ready",
		"runtime logs next cursor: --surface runtime --cursor lc1.next-opaque-token",
		"platform logs: available (1 entries)",
		"deploy retry scheduled",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestDeployLogsJSONPrintsAvailabilityResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1alpha/operations/op_1/logs" {
			t.Fatalf("request = %s %s, want operation logs path", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"operation_id":"op_1","operation_events":{"available":true,"kind":"operation_events","url":"/api/v1alpha/operations/op_1/events","count":0},"app_logs":[{"surface":"build","available":false,"reason":"unavailable"}],"message":"operation events only"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployLogs([]string{"--operation-id", "op_1", "--json"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout = %q, want JSON availability response: %v", stdout.String(), err)
	}
	if got["operation_id"] != "op_1" {
		t.Fatalf("response = %#v, want operation logs JSON", got)
	}
}

func TestDeployLogsRequiresSurfaceWithCursor(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployLogs([]string{"--operation-id", "op_1", "--cursor", "lc1.opaque-token"}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--surface is required") {
		t.Fatalf("stderr = %q, want surface validation", stderr.String())
	}
}

func TestDeployEventsRejectsNegativeAfter(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployEvents([]string{"--operation-id", "op_1", "--after", "-1"}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--after must be >= 0") {
		t.Fatalf("stderr = %q, want cursor validation", stderr.String())
	}
}

func TestAPIErrorEnvelopeIsClear(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"RESOURCE_VALIDATION_FAILED","message":"project slug is required","request_id":"req_1"}}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runProjectCreate([]string{"--workspace-id", "wrk_default", "--idempotency-key", "bad", ""}, stdout, stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "400 Bad Request") ||
		!strings.Contains(errOut, "RESOURCE_VALIDATION_FAILED") ||
		!strings.Contains(errOut, "project slug is required") {
		t.Fatalf("stderr = %q, want clear API error", errOut)
	}
}

func TestMutationCommandsHonorGlobalOutputJSON(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		args []string
		id   string
	}{
		{
			name: "project create",
			path: "/api/v1alpha/projects",
			body: `{"id":"prj_json","slug":"alpha","display_name":"Alpha"}`,
			args: []string{"project", "create", "--workspace-id", "wrk_1", "--name", "Alpha", "--idempotency-key", "k1", "alpha", "--output", "json"},
			id:   "prj_json",
		},
		{
			name: "service create",
			path: "/api/v1alpha/services",
			body: `{"id":"svc_json","project_id":"prj_1","slug":"web","display_name":"Web"}`,
			args: []string{"service", "create", "--project-id", "prj_1", "--name", "Web", "--idempotency-key", "k2", "web", "--output=json"},
			id:   "svc_json",
		},
		{
			name: "environment create",
			path: "/api/v1alpha/environments",
			body: `{"id":"env_json","project_id":"prj_1","slug":"staging","name":"Staging","tier":"staging"}`,
			args: []string{"environment", "create", "--output", "json", "--project-id", "prj_1", "--idempotency-key", "k3", "staging"},
			id:   "env_json",
		},
		{
			name: "deploy image",
			path: "/api/v1alpha/services/svc_1/deployments",
			body: `{"id":"op_json","kind":"deploy","state":"QUEUED"}`,
			args: []string{"deploy", "image", "--service-id", "svc_1", "--environment-id", "env_1",
				"--image", "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"--idempotency-key", "k4", "--output", "json"},
			id: "op_json",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != test.path {
					t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, test.path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			setCLIEndpoint(t, server.URL)

			var stdout, stderr bytes.Buffer
			code := ExecuteCLI(test.args, strings.NewReader(""), &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit = %d stderr=%q", code, stderr.String())
			}
			var decoded map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
				t.Fatalf("stdout = %q, want JSON object: %v", stdout.String(), err)
			}
			if decoded["id"] != test.id {
				t.Fatalf("stdout = %q, want the created resource with id %s", stdout.String(), test.id)
			}
			if strings.Contains(stdout.String(), "created:") || strings.Contains(stdout.String(), "queued:") {
				t.Fatalf("stdout = %q, want no human summary in JSON mode", stdout.String())
			}

			stdout.Reset()
			stderr.Reset()
			quietArgs := append([]string(nil), test.args...)
			quietArgs = append(quietArgs, "--quiet")
			if code := ExecuteCLI(quietArgs, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("quiet exit = %d stderr=%q", code, stderr.String())
			}
			if got := strings.TrimSpace(stdout.String()); got != test.id {
				t.Fatalf("quiet stdout = %q, want only %s", got, test.id)
			}
		})
	}
}

func TestDeployImageSavesPortAndReadinessIntentBeforeDeploying(t *testing.T) {
	const image = "registry.example/redis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var order []string
	var savedConfig map[string]any
	var ifMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1alpha/services/svc_1/environments/env_prod/config":
			order = append(order, "get-config")
			_, _ = w.Write([]byte(`{"project_id":"prj_1","service_id":"svc_1","environment_id":"env_prod","version":4,"explicit":true,"target_port":8080,"health_kind":"tcp","health_path":"/","builder":"auto","dockerfile_path":"Dockerfile","restart_policy":"unless-stopped","replicas":1,"runtime_kind":"service"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1alpha/services/svc_1/environments/env_prod/config":
			order = append(order, "put-config")
			ifMatch = r.Header.Get("If-Match")
			if err := json.NewDecoder(r.Body).Decode(&savedConfig); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"project_id":"prj_1","service_id":"svc_1","environment_id":"env_prod","version":5,"explicit":true,"target_port":6379,"health_kind":"tcp","health_path":"/"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha/services/svc_1/deployments":
			order = append(order, "deploy")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"QUEUED"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runDeployImage([]string{
		"--service-id", "svc_1",
		"--environment-id", "env_prod",
		"--image", image,
		"--port", "6379",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	// The configuration must be durable before the rollout that reads it.
	if len(order) != 3 || order[0] != "get-config" || order[1] != "put-config" || order[2] != "deploy" {
		t.Fatalf("request order = %v, want the config saved before the deploy", order)
	}
	if ifMatch != `"4"` {
		t.Fatalf("If-Match = %q, want the version the CLI read", ifMatch)
	}
	if savedConfig["target_port"] != float64(6379) {
		t.Fatalf("saved target_port = %v, want 6379", savedConfig["target_port"])
	}
	if savedConfig["expected_version"] != float64(4) {
		t.Fatalf("saved expected_version = %v, want 4", savedConfig["expected_version"])
	}
	// Unrelated stored fields survive the replace.
	if savedConfig["restart_policy"] != "unless-stopped" || savedConfig["replicas"] != float64(1) {
		t.Fatalf("saved config dropped unrelated fields: %#v", savedConfig)
	}
	if !strings.Contains(stdout.String(), "service config saved: port 6379") {
		t.Fatalf("stdout = %q, want the saved port reported", stdout.String())
	}
}

func TestDeployImageSkipsConfigWriteWhenNothingChanges(t *testing.T) {
	const image = "registry.example/redis@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var order []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet:
			order = append(order, "get-config")
			_, _ = w.Write([]byte(`{"service_id":"svc_1","environment_id":"env_prod","version":4,"explicit":true,"target_port":6379,"health_kind":"tcp","health_path":"/"}`))
		case r.Method == http.MethodPost:
			order = append(order, "deploy")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"op_1","kind":"deploy","state":"QUEUED"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	code := runDeployImage([]string{
		"--service-id", "svc_1", "--environment-id", "env_prod",
		"--image", image, "--port", "6379",
	}, &bytes.Buffer{}, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(order) != 2 || order[1] != "deploy" {
		t.Fatalf("request order = %v, want no write when the stored config already matches", order)
	}
}

func TestDeployImageRejectsInvalidRuntimeIntentFlags(t *testing.T) {
	const image = "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	base := []string{"--service-id", "svc_1", "--environment-id", "env_prod", "--image", image}
	for name, extra := range map[string][]string{
		"port out of range":        {"--port", "70000"},
		"unknown health kind":      {"--health-kind", "ping"},
		"relative health path":     {"--health-kind", "http", "--health-path", "healthz"},
		"health path without http": {"--health-kind", "tcp", "--health-path", "/healthz"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("invalid flags must be rejected before any request")
			}))
			defer server.Close()
			setCLIEndpoint(t, server.URL)
			stderr := &bytes.Buffer{}
			if code := runDeployImage(append(append([]string{}, base...), extra...), &bytes.Buffer{}, stderr); code != 64 {
				t.Fatalf("exit = %d stderr=%q, want a usage failure", code, stderr.String())
			}
		})
	}
}

func TestServiceConfigPutSendsIfMatchValidator(t *testing.T) {
	var ifMatch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ifMatch = r.Header.Get("If-Match")
		if ifMatch == "" {
			w.WriteHeader(http.StatusPreconditionRequired)
			_, _ = w.Write([]byte(`{"code":"PRECONDITION_REQUIRED","message":"If-Match is required"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service_id":"svc_1","environment_id":"env_1","version":4}`))
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"expected_version":3,"target_port":8000}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runServiceConfigPut([]string{
		"--api-base", server.URL, "--token", "t",
		"--environment-id", "env_1", "--file", path, "svc_1",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if ifMatch != `"3"` {
		t.Fatalf("If-Match = %q, want the expected_version from the request body", ifMatch)
	}
}
