// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/cliconfig"
)

func TestHumanDomainCommandsUseStrictNestedGeneratedLifecycle(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	view := cliGeneratedDomain{
		ID: "dom_test", WorkspaceID: "wrk_test", ProjectID: "prj_test",
		ServiceID: "svc_test", EnvironmentID: "env_test", Kind: "generated",
		Hostname: "svc-test.apps.midnight.test", Path: "/", TargetPort: 8080,
		Public: true, TLS: false, Version: 1, RouteApplyState: "blocked_backend",
		RouteReachable: false, CreatedAt: now, UpdatedAt: now,
	}
	collection := "/api/v1alpha/services/svc_test/environments/env_test/domains"
	var methods []string
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer midn_user_domain_test" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1alpha/domains") {
			t.Fatalf("human command used flat recovery path %q", r.URL.Path)
		}
		methods = append(methods, r.Method+" "+r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == collection:
			_ = json.NewEncoder(w).Encode([]cliGeneratedDomain{view})
		case r.Method == http.MethodGet && r.URL.Path == collection+"/dom_test":
			_ = json.NewEncoder(w).Encode(view)
		case r.Method == http.MethodPost && r.URL.Path == collection:
			body, _ := io.ReadAll(r.Body)
			if string(body) != "{}" {
				t.Fatalf("create body=%q", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(view)
		case r.Method == http.MethodDelete && r.URL.Path == collection+"/dom_test":
			var request cliGeneratedDomainDeleteRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.ExpectedVersion != 1 {
				t.Fatalf("delete request=%#v", request)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase: server.URL, ProjectID: "prj_test", EnvironmentID: "env_test",
		ServiceID: "svc_test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_domain_test",
	}); err != nil {
		t.Fatal(err)
	}

	for _, command := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{"list", func(stdout, stderr *bytes.Buffer) int {
			return runDomainList(nil, stdout, stderr)
		}},
		{"add", func(stdout, stderr *bytes.Buffer) int {
			return runDomainAdd(nil, stdout, stderr)
		}},
		{"get", func(stdout, stderr *bytes.Buffer) int {
			return runDomainGet([]string{"dom_test"}, stdout, stderr)
		}},
		{"delete", func(stdout, stderr *bytes.Buffer) int {
			return runDomainDelete([]string{"--expected-version", "1", "dom_test"}, stdout, stderr)
		}},
		{"add after delete", func(stdout, stderr *bytes.Buffer) int {
			return runDomainAdd(nil, stdout, stderr)
		}},
		{"delete recreated domain", func(stdout, stderr *bytes.Buffer) int {
			return runDomainDelete([]string{"--expected-version", "1", "dom_test"}, stdout, stderr)
		}},
	} {
		t.Run(command.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := command.run(&stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
	if len(methods) != 6 {
		t.Fatalf("methods=%#v", methods)
	}
	if keys[0] != "" || keys[2] != "" {
		t.Fatalf("read keys=%#v", keys)
	}
	if keys[1] == "" || keys[4] == "" || keys[1] == keys[4] {
		t.Fatalf("independent create keys must be fresh: %#v", keys)
	}
	if keys[3] == "" || keys[5] == "" || keys[3] == keys[5] {
		t.Fatalf("independent delete keys must be fresh: %#v", keys)
	}
}

func TestHumanDomainAddPreservesExplicitIdempotencyKey(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	const explicitKey = "domain-create-intent-retry-1"
	var observedKey string
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedKey = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(cliGeneratedDomain{
			ID: "dom_test", WorkspaceID: "wrk_test", ProjectID: "prj_test",
			ServiceID: "svc_test", EnvironmentID: "env_test", Kind: "generated",
			Hostname: "svc-test.apps.midnight.test", Path: "/", TargetPort: 8080,
			Public: true, Version: 1, RouteApplyState: "blocked_backend",
			CreatedAt: now, UpdatedAt: now,
		})
	}))
	t.Cleanup(server.Close)
	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase: server.URL, ProjectID: "prj_test", EnvironmentID: "env_test",
		ServiceID: "svc_test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_domain_test",
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runDomainAdd(
		[]string{"--idempotency-key", explicitKey},
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if observedKey != explicitKey {
		t.Fatalf("idempotency key=%q, want %q", observedKey, explicitKey)
	}
}

func TestNewDomainIdempotencyKeyFailsClosedWithoutSecureRandomness(t *testing.T) {
	_, err := newDomainIdempotencyKey("create", failingReader{})
	if err == nil || !strings.Contains(err.Error(), "secure randomness") {
		t.Fatalf("error=%v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestHumanDomainAddRejectsRecoveryControlsBeforeRequest(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	t.Cleanup(server.Close)
	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase: server.URL, ProjectID: "prj_test", EnvironmentID: "env_test",
		ServiceID: "svc_test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_domain_test",
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runDomainAdd([]string{"--tls", "--port", "3000", "app.example.test"}, &stdout, &stderr)
	if code == 0 || requests != 0 ||
		!strings.Contains(stderr.String(), "server generates one HTTP domain") {
		t.Fatalf("code=%d requests=%d stdout=%q stderr=%q", code, requests, stdout.String(), stderr.String())
	}
}

func TestHumanDomainListRejectsUnknownResponseFields(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":"dom_test","unexpected":"unsafe"}]`)
	}))
	t.Cleanup(server.Close)
	workdir := t.TempDir()
	t.Chdir(workdir)
	if err := cliconfig.SaveProjectLink(workdir, cliconfig.ProjectLink{
		APIBase: server.URL, ProjectID: "prj_test", EnvironmentID: "env_test",
		ServiceID: "svc_test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_domain_test",
	}); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runDomainList(nil, &stdout, &stderr); code == 0 ||
		!strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
