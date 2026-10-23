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
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/cliconfig"
)

const createUserStrongPassword = "correct horse battery staple"

func setCreateUserEndpoint(t *testing.T, apiBase string) {
	t.Helper()
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, apiBase)
	t.Setenv(envBaseURL, "")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(envAPIToken, "operator-recovery-token")
}

func createUserArgs(extra ...string) []string {
	return append([]string{
		"--email", "owner@example.com",
		"--display-name", "Owner",
		"--workspace-slug", "acme-platform",
		"--workspace-name", "Acme Platform",
	}, extra...)
}

func TestAdminCreateUserClaimsWorkspaceAndPrintsEnrollment(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	adminCreateUserClock = func() time.Time { return now }
	t.Cleanup(func() { adminCreateUserClock = func() time.Time { return time.Now().UTC() } })

	var captured struct {
		authorization  string
		idempotencyKey string
		body           adminCreateUserRequest
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != identityClaimAPIPath {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		captured.authorization = r.Header.Get("Authorization")
		captured.idempotencyKey = r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&captured.body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"user":{"id":"usr_abc","email":"owner@example.com","display_name":"Owner","instance_admin":true,"mfa_enabled":true},
			"workspace":{"id":"wrk_default","slug":"acme-platform","display_name":"Acme Platform","role":"admin","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}
		}`))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(createUserArgs("--password", createUserStrongPassword), stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}

	if captured.authorization != "Bearer operator-recovery-token" {
		t.Fatalf("Authorization = %q", captured.authorization)
	}
	if !strings.HasPrefix(captured.idempotencyKey, "admin-create-user-") {
		t.Fatalf("Idempotency-Key = %q, want a generated key", captured.idempotencyKey)
	}
	if captured.body.Email != "owner@example.com" || captured.body.WorkspaceSlug != "acme-platform" {
		t.Fatalf("claim body = %+v", captured.body)
	}
	secret, err := bootstrap.DecodeTOTPSecret(captured.body.TOTPSecret)
	if err != nil {
		t.Fatalf("server received an undecodable TOTP secret: %v", err)
	}
	if err := bootstrap.VerifyTOTP(secret, captured.body.TOTPCode, now); err != nil {
		t.Fatalf("submitted TOTP code did not verify against the submitted secret: %v", err)
	}

	out := stdout.String()
	for _, want := range []string{
		"owner@example.com",
		"otpauth://totp/Midnight:owner%40example.com",
		captured.body.TOTPSecret,
		"Save this now",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, createUserStrongPassword) {
		t.Fatalf("stdout leaked the password: %q", out)
	}
}

func TestAdminCreateUserJSONOutputCarriesEnrollmentSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"user":{"id":"usr_abc","email":"owner@example.com","display_name":"Owner","instance_admin":true,"mfa_enabled":true},
			"workspace":{"id":"wrk_default","slug":"acme-platform","display_name":"Acme Platform","role":"admin","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}
		}`))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(
		createUserArgs("--password", createUserStrongPassword, "--json"),
		stdout,
		stderr,
	)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded adminCreateUserResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if decoded.User.ID != "usr_abc" || decoded.Workspace.Slug != "acme-platform" {
		t.Fatalf("decoded = %+v", decoded)
	}
	if _, err := bootstrap.DecodeTOTPSecret(decoded.TOTPSecret); err != nil {
		t.Fatalf("json totp_secret is not a usable Base32 secret: %v", err)
	}
	if !strings.HasPrefix(decoded.OTPAuthURI, "otpauth://totp/") {
		t.Fatalf("otpauth_uri = %q", decoded.OTPAuthURI)
	}
}

func TestAdminCreateUserReportsAlreadyClaimed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"RESOURCE_CONFLICT","message":"default workspace is already claimed"}}`))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(createUserArgs("--password", createUserStrongPassword), stdout, stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already has an administrator") {
		t.Fatalf("stderr = %q, want the already-claimed explanation", stderr.String())
	}
	if !strings.Contains(stderr.String(), "midnight login") {
		t.Fatalf("stderr = %q, want next-step guidance", stderr.String())
	}
}

func TestAdminCreateUserRejectsWeakPasswordBeforeSending(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("weak password reached the API")
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(createUserArgs("--password", "short"), &bytes.Buffer{}, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "at least 12 characters") {
		t.Fatalf("stderr = %q, want the minimum-length rule", stderr.String())
	}
}

func TestAdminCreateUserSurfacesRejectedTOTPCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"MFA_INVALID_CODE","message":"TOTP enrollment secret and code did not verify"}}`))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(createUserArgs("--password", createUserStrongPassword), &bytes.Buffer{}, stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "MFA_INVALID_CODE") {
		t.Fatalf("stderr = %q, want the typed failure code", stderr.String())
	}
}

func TestAdminCreateUserRequiresIdentityFlags(t *testing.T) {
	setCreateUserEndpoint(t, "http://127.0.0.1:1")

	stderr := &bytes.Buffer{}
	code := runAdminCreateUser([]string{"--email", "owner@example.com"}, &bytes.Buffer{}, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--display-name is required") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAdminCreateUserPromptsForPasswordAndConfirmation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("mismatched confirmation reached the API")
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	answers := []string{createUserStrongPassword, "a different password"}
	original := loginSecretPrompt
	loginSecretPrompt = func(string, io.Writer) (string, error) {
		value := answers[0]
		answers = answers[1:]
		return value, nil
	}
	t.Cleanup(func() { loginSecretPrompt = original })

	stderr := &bytes.Buffer{}
	code := runAdminCreateUser(createUserArgs(), &bytes.Buffer{}, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "passwords did not match") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAdminCreateUserIsRegisteredInTheCommandTree(t *testing.T) {
	var found bool
	for _, spec := range cliCommandSpecs() {
		if spec.Use != "admin" {
			continue
		}
		for _, child := range spec.Children {
			if child.Use == "create-user" {
				found = true
				if child.Flags == nil {
					t.Fatal("admin create-user has no --help flag set")
				}
				flags := child.Flags()
				for _, name := range []string{"email", "display-name", "workspace-slug", "workspace-name", "password"} {
					if flags.Lookup(name) == nil {
						t.Errorf("admin create-user --help omits --%s", name)
					}
				}
			}
			if child.Use == "generate-bootstrap-token" || child.Use == "show-bootstrap" {
				t.Errorf("removed bootstrap command %q is still registered", child.Use)
			}
		}
	}
	if !found {
		t.Fatal("admin create-user is not registered")
	}
}
