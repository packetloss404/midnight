// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
)

func TestProjectTokenCreateUsesHumanSessionAndSensitiveReplayKey(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/projects/prj_test/automation-tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer midn_user_session" {
			t.Fatalf("Authorization = %q", got)
		}
		gotKey = r.Header.Get("Idempotency-Key")
		if !strings.HasPrefix(gotKey, "cli-token-") {
			t.Fatalf("Idempotency-Key = %q", gotKey)
		}
		var body cliAutomationTokenCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Name != "release" || time.Until(body.ExpiresAt) < 23*time.Hour {
			t.Fatalf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"midn_sa_secret","token":{"id":"tok_1","name":"release","token_prefix":"midn_sa_sec","profile":"ci-deploy-v1","expires_at":"2030-01-01T00:00:00Z"}}`))
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{Token: "midn_user_session"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if code := runProjectToken([]string{"create", "--api-base", server.URL, "--project-id", "prj_test", "--name", "release", "--expires-in", "24h"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "midn_sa_secret") || gotKey == "" {
		t.Fatalf("stdout=%q key=%q", stdout.String(), gotKey)
	}
}

func TestProjectTokenManagementRefusesRecoveryCredential(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configDir)
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	if err := cliconfig.SetOperatorRecoveryToken("https://midnight.example", "midn_admin_recovery"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runProjectToken([]string{"list", "--api-base", "https://midnight.example", "--project-id", "prj_test"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "requires a human session") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestProjectTokenCreateRequiresFixedExpiryWithCallerRetryKey(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached server with a non-replayable relative expiry")
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{Token: "midn_user_session"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runProjectToken([]string{
		"create",
		"--api-base", server.URL,
		"--project-id", "prj_test",
		"--name", "release",
		"--idempotency-key", "release-token-2026",
	}, &stdout, &stderr)
	if code != 64 || !strings.Contains(stderr.String(), "--expires-at is required") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestProjectTokenCreateUsesFixedExpiryForExactReplay(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	const fixedExpiry = "2027-07-28T12:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body cliAutomationTokenCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ExpiresAt.Format(time.RFC3339) != fixedExpiry ||
			r.Header.Get("Idempotency-Key") != "release-token-2026" {
			t.Fatalf("expiry=%s key=%q", body.ExpiresAt, r.Header.Get("Idempotency-Key"))
		}
		_, _ = w.Write([]byte(`{"access_token":"midn_sa_secret","token":{"id":"tok_1","name":"release","token_prefix":"midn_sa_sec","profile":"ci-deploy-v1","expires_at":"2027-07-28T12:00:00Z"}}`))
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{Token: "midn_user_session"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runProjectToken([]string{
		"create",
		"--api-base", server.URL,
		"--project-id", "prj_test",
		"--name", "release",
		"--idempotency-key", "release-token-2026",
		"--expires-at", fixedExpiry,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestProjectTokenManagementRefusesCredentialRedirect(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	received := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if err := cliconfig.SetHumanSession(source.URL, cliconfig.HumanSessionCredential{Token: "midn_user_redirect"}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	code := runProjectToken([]string{"list", "--api-base", source.URL, "--project-id", "prj_test"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), errAuthRedirect.Error()) {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	select {
	case authorization := <-received:
		t.Fatalf("redirect target received Authorization %q", authorization)
	default:
	}
}

func TestProjectTokenListUsesLinkedProject(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("MIDNIGHT_CONFIG_HOME", configDir)
	t.Setenv(cliconfig.EnvAPIBase, "")
	t.Setenv(cliconfig.EnvBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1alpha/projects/prj_linked/automation-tokens" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{Token: "midn_user_linked"}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCWD) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(cwd, cliconfig.ProjectLinkDirName)
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := cliconfig.ProjectLink{
		Version:   cliconfig.ProjectLinkCurrentVersion,
		APIBase:   server.URL,
		ProjectID: "prj_linked",
	}
	data, err := json.Marshal(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkDir, cliconfig.ProjectLinkFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	if code := runProjectToken([]string{"list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ID") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
