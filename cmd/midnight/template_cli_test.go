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
)

func TestTemplateDeployAndStatusExposePendingProofHonestly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1alpha/templates/postgres/instances":
			if r.Header.Get("Idempotency-Key") != "template-cli-proof" {
				t.Fatalf("idempotency key=%q", r.Header.Get("Idempotency-Key"))
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["project_id"] != "prj_1" || body["name"] != "Orders" {
				t.Fatalf("body=%v", body)
			}
			_, _ = w.Write([]byte(`{"operation_id":"op_1","provision_run_id":"tpl_1","template_slug":"postgres","state":"queued","environment_id":"env_1","deployment_id":"dep_1","status_url":"/api/v1alpha/templates/postgres/instances/tpl_1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1alpha/templates/postgres/instances/tpl_1":
			_, _ = w.Write([]byte(`{"id":"tpl_1","template_slug":"postgres","operation_id":"op_1","deployment_id":"dep_1","state":"succeeded","connectivity_verified":true,"persistence_verified":true,"cleanup_completed":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)
	var stdout, stderr bytes.Buffer
	if code := runTemplate([]string{"deploy", "--project-id", "prj_1", "--name", "Orders", "--idempotency-key", "template-cli-proof", "postgres"}, &stdout, &stderr); code != 0 {
		t.Fatalf("deploy code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Success remains pending") || !strings.Contains(stdout.String(), "tpl_1") {
		t.Fatalf("deploy output=%s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTemplate([]string{"status", "tpl_1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Persistence verified: true") ||
		!strings.Contains(stdout.String(), "Probe cleanup completed: true") {
		t.Fatalf("status output=%s", stdout.String())
	}
}

func TestTemplateDeployPassesOfficialDatabaseSlugThrough(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/templates/mysql/instances" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"operation_id":"op_mysql","provision_run_id":"tpl_mysql","template_slug":"mysql","state":"queued","environment_id":"env_1","deployment_id":"dep_mysql","status_url":"/api/v1alpha/templates/mysql/instances/tpl_mysql"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	var stdout, stderr bytes.Buffer
	if code := runTemplate([]string{"deploy", "--project-id", "prj_1", "mysql"}, &stdout, &stderr); code != 0 {
		t.Fatalf("deploy code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "midnight template status --slug mysql tpl_mysql") {
		t.Fatalf("deploy output=%s", stdout.String())
	}
}

func TestTemplateDeployReusesPendingAttemptKeyAfterDroppedResponse(t *testing.T) {
	t.Setenv(mutationJournalDirEnv, t.TempDir())
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
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"operation_id":"op_1","provision_run_id":"tpl_1","template_slug":"postgres","state":"queued","environment_id":"env_1","deployment_id":"dep_1","status_url":"/api/v1alpha/templates/postgres/instances/tpl_1"}`))
	}))
	defer server.Close()
	setCLIEndpoint(t, server.URL)

	args := []string{"deploy", "--project-id", "prj_1", "--name", "Orders", "postgres"}
	var stdout, stderr bytes.Buffer
	if code := runTemplate(args, &stdout, &stderr); code != 1 {
		t.Fatalf("dropped response exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "outcome is unknown") {
		t.Fatalf("dropped response stderr = %q, want safe retry guidance", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTemplate(args, &stdout, &stderr); code != 0 {
		t.Fatalf("retry exit = %d stderr=%q", code, stderr.String())
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("idempotency keys = %#v, want the same pending-attempt key", keys)
	}
}
