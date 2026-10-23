// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
)

const resetPasswordStrongPassword = "correct horse battery staple"

func resetPasswordSuccessBody(reenrolled bool, revoked int) string {
	body, _ := json.Marshal(map[string]any{
		"user": map[string]any{
			"id": "usr_admin", "email": "ops@example.com", "display_name": "Ops",
			"instance_admin": true, "mfa_enabled": true,
		},
		"totp_reenrolled":  reenrolled,
		"sessions_revoked": revoked,
	})
	return string(body)
}

func TestAdminResetPasswordEnrollsAReplacementFactorAndPrintsItOnce(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	adminCreateUserClock = func() time.Time { return now }
	t.Cleanup(func() { adminCreateUserClock = func() time.Time { return time.Now().UTC() } })

	var captured struct {
		authorization  string
		idempotencyKey string
		body           adminResetPasswordRequest
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != identityPasswordResetAPIPath {
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
		_, _ = w.Write([]byte(resetPasswordSuccessBody(true, 2)))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminResetPassword([]string{
		"--email", "ops@example.com", "--password", resetPasswordStrongPassword,
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if captured.authorization != "Bearer operator-recovery-token" {
		t.Fatalf("Authorization = %q", captured.authorization)
	}
	if !strings.HasPrefix(captured.idempotencyKey, "admin-reset-password-") {
		t.Fatalf("Idempotency-Key = %q, want a generated key", captured.idempotencyKey)
	}
	if captured.body.Email != "ops@example.com" || captured.body.PreserveTOTP {
		t.Fatalf("reset body = %+v", captured.body)
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
		"ops@example.com",
		"sessions revoked:  2",
		"otpauth://totp/Midnight:ops%40example.com",
		captured.body.TOTPSecret,
		"Save this now",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, resetPasswordStrongPassword) {
		t.Fatalf("stdout leaked the password: %q", out)
	}
}

func TestAdminResetPasswordKeepTOTPSendsNoEnrollmentMaterial(t *testing.T) {
	var captured adminResetPasswordRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resetPasswordSuccessBody(false, 0)))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminResetPassword([]string{
		"--email", "ops@example.com", "--password", resetPasswordStrongPassword, "--keep-totp",
	}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !captured.PreserveTOTP || captured.TOTPSecret != "" || captured.TOTPCode != "" {
		t.Fatalf("keep-totp body = %+v", captured)
	}
	out := stdout.String()
	if !strings.Contains(out, "unchanged (--keep-totp)") {
		t.Fatalf("stdout = %q, want the preserved-authenticator summary", out)
	}
	if strings.Contains(out, "otpauth://") {
		t.Fatalf("stdout advertised an enrollment URL for a preserved factor: %q", out)
	}
}

func TestAdminResetPasswordJSONOutputCarriesReplacementEnrollment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resetPasswordSuccessBody(true, 1)))
	}))
	defer server.Close()
	setCreateUserEndpoint(t, server.URL)

	stdout := &bytes.Buffer{}
	code := runAdminResetPassword([]string{
		"--email", "ops@example.com", "--password", resetPasswordStrongPassword, "--json",
	}, stdout, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var decoded adminResetPasswordResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout.String())
	}
	if decoded.User.ID != "usr_admin" || !decoded.TOTPReenrolled || decoded.SessionsRevoked != 1 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if _, err := bootstrap.DecodeTOTPSecret(decoded.TOTPSecret); err != nil {
		t.Fatalf("json totp_secret is not a usable Base32 secret: %v", err)
	}
	if !strings.HasPrefix(decoded.OTPAuthURI, "otpauth://totp/") {
		t.Fatalf("otpauth_uri = %q", decoded.OTPAuthURI)
	}
}

func TestAdminResetPasswordExplainsTypedRefusals(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
		want   []string
	}{
		"human session": {
			status: http.StatusForbidden,
			body:   `{"error":{"code":"AUTH_OPERATOR_RECOVERY_REQUIRED","message":"operator recovery credential required"}}`,
			want:   []string{"operator recovery credential", "MIDNIGHT_API_BEARER_TOKEN"},
		},
		"no such administrator": {
			status: http.StatusNotFound,
			body:   `{"error":{"code":"RESOURCE_NOT_FOUND","message":"no enabled administrator account matches that address"}}`,
			want:   []string{"no enabled administrator account", "disabled or deleted accounts"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			setCreateUserEndpoint(t, server.URL)

			stderr := &bytes.Buffer{}
			code := runAdminResetPassword([]string{
				"--email", "ops@example.com", "--password", resetPasswordStrongPassword,
			}, &bytes.Buffer{}, stderr)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr=%q", code, stderr.String())
			}
			for _, want := range test.want {
				if !strings.Contains(stderr.String(), want) {
					t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}

func TestAdminResetPasswordRejectsBadInputBeforeSending(t *testing.T) {
	tests := map[string][]string{
		"missing email":  {"--password", resetPasswordStrongPassword},
		"short password": {"--email", "ops@example.com", "--password", "short"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("invalid input reached the API")
			}))
			defer server.Close()
			setCreateUserEndpoint(t, server.URL)

			stderr := &bytes.Buffer{}
			if code := runAdminResetPassword(args, &bytes.Buffer{}, stderr); code != 64 {
				t.Fatalf("exit = %d, want 64; stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestAdminDispatchesResetPassword(t *testing.T) {
	stderr := &bytes.Buffer{}
	if code := runAdmin(nil, &bytes.Buffer{}, stderr); code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "midnight admin reset-password") {
		t.Fatalf("usage = %q, want the recovery subcommand listed", stderr.String())
	}
}

func TestAdminResetPasswordIsRegisteredInTheCommandTree(t *testing.T) {
	var found bool
	for _, spec := range cliCommandSpecs() {
		if spec.Use != "admin" {
			continue
		}
		for _, child := range spec.Children {
			if child.Use != "reset-password" {
				continue
			}
			found = true
			if child.Flags == nil {
				t.Fatal("admin reset-password has no --help flag set")
			}
			flags := child.Flags()
			for _, name := range []string{"email", "password", "keep-totp", "issuer"} {
				if flags.Lookup(name) == nil {
					t.Errorf("admin reset-password --help omits --%s", name)
				}
			}
		}
	}
	if !found {
		t.Fatal("admin reset-password is not registered")
	}
}
