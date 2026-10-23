package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTCPProxyCreateCLISupportsOptionalObservedPeerRestrictionAndKeepsClaimsHonest(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1alpha/tcp-proxies" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Idempotency-Key") == "" {
			t.Error("missing idempotency key")
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeJSONForCLITest(w, http.StatusCreated, map[string]any{
			"id": "tcp_1", "public_address": "db--prod--prj.tcp.example.test:20000",
			"apply_state": "pending", "desired_enabled": true, "listener_bound": false,
			"backend_reachable": false, "dns_provisioned": false,
			"public_reachability_verified": false,
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runTCPProxy([]string{
		"create", "--api-base", server.URL, "--token", "token",
		"--project-id", "prj", "--environment-id", "env", "--service-id", "svc",
		"--target-port", "5432",
		"--allow-cidr", "192.0.2.0/24", "--allow-cidr", "198.51.100.4/32",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	allowed, ok := body["allowed_source_cidrs"].([]any)
	if !ok || len(allowed) != 2 {
		t.Fatalf("body=%#v", body)
	}
	if _, ok := body["backend_host"]; ok {
		t.Fatalf("CLI accepted caller-controlled backend host: %#v", body)
	}
	for _, want := range []string{"state=pending", "listener=false", "operator-managed", "unverified"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("stdout=%q want=%q", stdout.String(), want)
		}
	}
}

func TestTCPProxyCreateCLIDefaultsToPublicListener(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeJSONForCLITest(w, http.StatusCreated, map[string]any{
			"id": "tcp_public", "public_address": "db.tcp.example.test:20000",
			"apply_state": "pending", "desired_enabled": true,
			"allowed_source_cidrs": []string{},
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runTCPProxy([]string{
		"create", "--api-base", server.URL, "--token", "token",
		"--project-id", "prj", "--environment-id", "env", "--service-id", "svc",
		"--target-port", "5432",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	allowed, ok := body["allowed_source_cidrs"].([]any)
	if !ok || len(allowed) != 0 {
		t.Fatalf("body=%#v", body)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("accepts every peer address it observes")) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestTCPProxyStatusCLIPrintsLocalAndExternalEvidenceSeparately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		writeJSONForCLITest(w, http.StatusOK, map[string]any{
			"id": "tcp_1", "public_address": "db.tcp.example.test:20000",
			"apply_state": "active", "desired_enabled": true, "listener_bound": true,
			"backend_reachable": true, "dns_provisioned": false,
			"public_reachability_verified": false,
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := runTCPProxy([]string{"status", "--api-base", server.URL, "--token", "token", "tcp_1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"state=active", "listener=true", "backend=true", "dns_provisioned=false", "public_reachability_verified=false"} {
		if !bytes.Contains(stdout.Bytes(), []byte(want)) {
			t.Fatalf("stdout=%q want=%q", stdout.String(), want)
		}
	}
}
