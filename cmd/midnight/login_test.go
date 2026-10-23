// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/auth/oauth"
	"github.com/midnight/midnight/internal/cliconfig"
)

// fakeServer is a minimal OAuth-on-API server good enough for the login
// test. It mints codes and exchanges them for a fixed bearer token.
type fakeServer struct {
	store       *oauth.Store
	bearerToken string
	mu          struct {
		lastAuthorizeQuery url.Values
	}
}

func newFakeServer(t *testing.T) (*fakeServer, *httptest.Server) {
	t.Helper()
	fs := &fakeServer{store: oauth.NewStore(nil), bearerToken: "operator-recovery-token"}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.lastAuthorizeQuery = r.URL.Query()
		issued, err := fs.store.IssueCode(oauth.AuthorizeRequest{
			ClientID:      r.URL.Query().Get("client_id"),
			CodeChallenge: r.URL.Query().Get("code_challenge"),
			State:         r.URL.Query().Get("state"),
			RedirectURI:   r.URL.Query().Get("redirect_uri"),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		callback, err := url.Parse(r.URL.Query().Get("redirect_uri"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		q := callback.Query()
		q.Set("code", issued.Code)
		q.Set("state", issued.State)
		callback.RawQuery = q.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
	})
	mux.HandleFunc("/auth/oauth/token", oauth.TokenHandler(oauth.HandlerConfig{Store: fs.store, BearerToken: fs.bearerToken}))
	return fs, httptest.NewServer(mux)
}

func TestAdminAuthorizeCLIStoresOperatorRecoveryCredential(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, "http://127.0.0.1:1")
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	fs, server := newFakeServer(t)
	defer server.Close()
	_ = fs

	originalOpener := browserOpener
	defer func() { browserOpener = originalOpener }()
	browserOpener = func(target string) error {
		// Simulate the browser by following the authorize URL with a real
		// HTTP client. The fake server immediately redirects to the local
		// callback listener.
		go func() {
			client := &http.Client{
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					// Follow the redirect so the local listener receives it.
					return nil
				},
				Timeout: 30 * time.Second,
			}
			resp, err := client.Get(target)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runAdminAuthorizeCLI([]string{"--api-base", server.URL, "--timeout", "10s"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s, stdout=%s", code, stderr.String(), stdout.String())
	}
	got, err := cliconfig.OperatorRecoveryToken(server.URL)
	if err != nil {
		t.Fatalf("OperatorRecoveryToken after authorization: %v", err)
	}
	if got != "operator-recovery-token" {
		t.Fatalf("token = %q, want operator-recovery-token", got)
	}
	if !strings.Contains(stdout.String(), "not a human login") {
		t.Fatalf("stdout = %q, want explicit operator classification", stdout.String())
	}
}

func TestHumanLoginStoresSessionWithoutOverwritingOperatorRecovery(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	const rawEmail = "alice@example.com\x1b]52;c;hidden-email\a"
	const rawDisplayName = "Ali\x1b[31mce\x1b[0m"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1alpha/auth/sessions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["email"] != "alice@example.com" ||
			request["password"] != "correct horse battery staple" ||
			request["client_kind"] != "cli" {
			t.Fatalf("login request = %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		if request["totp_code"] == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "MFA_REQUIRED", "message": "multi-factor authentication is required"},
			})
			return
		}
		if request["totp_code"] != "123456" {
			t.Fatalf("totp_code = %q", request["totp_code"])
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "midn_user_alpha",
			"token_type":   "Bearer",
			"user": map[string]any{
				"id": "usr_alice", "email": rawEmail, "display_name": rawDisplayName,
			},
			"session": map[string]any{"id": "ses_cli", "expires_at": expiresAt},
		})
	}))
	defer server.Close()
	if err := cliconfig.SetOperatorRecoveryToken(server.URL, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	stubLoginSecrets(t, "correct horse battery staple", "123456")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runLogin([]string{"--api-base", server.URL, "--email", "alice@example.com"}, stdout, stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	session, err := cliconfig.HumanSession(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if session.Token != "midn_user_alpha" || session.SessionID != "ses_cli" ||
		session.Email != rawEmail || session.DisplayName != rawDisplayName ||
		!session.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("stored human session = %#v", session)
	}
	recovery, err := cliconfig.OperatorRecoveryToken(server.URL)
	if err != nil || recovery != "operator-recovery-token" {
		t.Fatalf("preserved recovery=%q err=%v", recovery, err)
	}
	if !strings.Contains(stdout.String(), "human session") ||
		strings.Contains(stdout.String(), "operator-recovery-token") ||
		strings.Contains(stdout.String(), "hidden-email") ||
		strings.ContainsRune(stdout.String(), '\x1b') ||
		strings.Contains(stderr.String(), "correct horse") {
		t.Fatalf("unsafe or misleading output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHumanLoginRejectsAuthErrorsAndNonHumanTokenClass(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		response   map[string]any
		wantOutput string
	}{
		{
			name: "mfa required", status: http.StatusUnauthorized,
			response:   map[string]any{"error": map[string]any{"code": "MFA_REQUIRED", "message": "multi-factor authentication is required"}},
			wantOutput: "MFA_REQUIRED",
		},
		{
			name: "operator token from human endpoint", status: http.StatusCreated,
			response: map[string]any{
				"access_token": "operator-recovery-token",
				"token_type":   "Bearer",
				"user":         map[string]any{"id": "usr_alice", "email": "alice@example.com"},
				"session":      map[string]any{"id": "ses_bad", "expires_at": time.Now().UTC().Add(time.Hour)},
			},
			wantOutput: "non-human credential",
		},
		{
			name: "invalid token type", status: http.StatusCreated,
			response: map[string]any{
				"access_token": "midn_user_alpha",
				"token_type":   "MAC",
				"user":         map[string]any{"id": "usr_alice", "email": "alice@example.com"},
				"session":      map[string]any{"id": "ses_bad", "expires_at": time.Now().UTC().Add(time.Hour)},
			},
			wantOutput: "invalid token_type",
		},
		{
			name: "expired session", status: http.StatusCreated,
			response: map[string]any{
				"access_token": "midn_user_alpha",
				"token_type":   "Bearer",
				"user":         map[string]any{"id": "usr_alice", "email": "alice@example.com"},
				"session":      map[string]any{"id": "ses_bad", "expires_at": time.Now().UTC().Add(-time.Minute)},
			},
			wantOutput: "already-expired",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
			t.Setenv(cliconfig.EnvBearerToken, "")
			t.Setenv(cliconfig.EnvAPIToken, "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_ = json.NewEncoder(w).Encode(test.response)
			}))
			defer server.Close()
			stubLoginSecrets(t, "password", "123456")
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			if code := runLogin([]string{"--api-base", server.URL, "--email", "alice@example.com"}, stdout, stderr); code == 0 {
				t.Fatalf("login succeeded stdout=%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.wantOutput) {
				t.Fatalf("stderr=%q want %q", stderr.String(), test.wantOutput)
			}
			if _, err := cliconfig.HumanSession(server.URL); !errors.Is(err, cliconfig.ErrNoHumanSession) {
				t.Fatalf("failed login stored session: %v", err)
			}
		})
	}
}

func TestHumanAuthErrorsBoundUntrustedResponseText(t *testing.T) {
	err := decodeHumanAuthError(http.StatusBadGateway, []byte(strings.Repeat("upstream-secret-like-text", 1000)))
	if len(err.Error()) > (4<<10)+100 || !strings.HasSuffix(err.Error(), "…") {
		t.Fatalf("unbounded auth error length=%d suffix=%q", len(err.Error()), err.Error()[len(err.Error())-3:])
	}
}

func TestHumanAuthErrorsStripTerminalControlSequences(t *testing.T) {
	jsonBody, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "INVALID\x1b[31m",
			"message": "start\x1b]52;c;clipboard-secret\aend\u009b31mred\u009b0m",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	jsonError := decodeHumanAuthError(http.StatusUnauthorized, jsonBody).Error()
	if jsonError != "INVALID: startendred" {
		t.Fatalf("JSON auth error = %q", jsonError)
	}

	plainError := decodeHumanAuthError(
		http.StatusBadGateway,
		[]byte("\x1b[31mred\x1b[0m \x1b]8;;https://evil.test\aunsafe-link\x1b]8;;\a"),
	).Error()
	if plainError != "red unsafe-link" {
		t.Fatalf("non-JSON auth error = %q", plainError)
	}
}

func TestTerminalSafeTextRemovesC0C1ANSIAndOSC(t *testing.T) {
	input := "A\x00B\nC\x1b[31mD\x1b[0mE\x1b]52;c;hidden\aF\u009b32mG\u009b0mH"
	if got := terminalSafeText(input); got != "A B CDEFGH" {
		t.Fatalf("terminalSafeText() = %q", got)
	}
}

func TestAuthHTTPClientRejectsRedirectsBeforeReplayingSecrets(t *testing.T) {
	t.Run("password body", func(t *testing.T) {
		targetRequests := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			targetRequests++
		}))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
		}))
		defer source.Close()

		_, err := createHumanCLISession(
			source.URL,
			"alice@example.com",
			"correct horse battery staple",
			"",
		)
		if !errors.Is(err, errAuthRedirect) {
			t.Fatalf("createHumanCLISession err=%v, want errAuthRedirect", err)
		}
		if targetRequests != 0 {
			t.Fatalf("redirect target received %d password request(s)", targetRequests)
		}
	})

	t.Run("bearer header", func(t *testing.T) {
		targetRequests := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			targetRequests++
		}))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
		}))
		defer source.Close()

		_, _, err := fetchHumanIdentity(source.URL, "midn_user_alpha")
		if !errors.Is(err, errAuthRedirect) {
			t.Fatalf("fetchHumanIdentity err=%v, want errAuthRedirect", err)
		}
		if targetRequests != 0 {
			t.Fatalf("redirect target received %d bearer request(s)", targetRequests)
		}
	})

	t.Run("authorization code body", func(t *testing.T) {
		targetRequests := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			targetRequests++
		}))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/capture", http.StatusPermanentRedirect)
		}))
		defer source.Close()

		_, err := exchangeCodeForToken(
			context.Background(),
			source.URL,
			"single-use-code",
			"pkce-verifier",
			"http://127.0.0.1:12345/callback",
		)
		if !errors.Is(err, errAuthRedirect) {
			t.Fatalf("exchangeCodeForToken err=%v, want errAuthRedirect", err)
		}
		if targetRequests != 0 {
			t.Fatalf("redirect target received %d authorization-code request(s)", targetRequests)
		}
	})
}

func TestHumanLoginRejectsNonLoopbackPlainHTTPBeforePrompt(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	prompted := false
	original := loginSecretPrompt
	t.Cleanup(func() { loginSecretPrompt = original })
	loginSecretPrompt = func(string, io.Writer) (string, error) {
		prompted = true
		return "secret", nil
	}
	stderr := &bytes.Buffer{}
	code := runLogin([]string{"--api-base", "http://api.midnight.test", "--email", "alice@example.com"}, io.Discard, stderr)
	if code != 64 || prompted || !strings.Contains(stderr.String(), "requires HTTPS") {
		t.Fatalf("code=%d prompted=%v stderr=%q", code, prompted, stderr.String())
	}
}

func TestLoginRequiresAPIBase(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(envAPIBase, "")
	t.Setenv(envBaseURL, "")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code := runLogin([]string{}, stdout, stderr)
	if code != 64 {
		t.Fatalf("exit = %d, want 64", code)
	}
	if !strings.Contains(stderr.String(), "--api-base") {
		t.Fatalf("stderr = %q, want --api-base hint", stderr.String())
	}
}

func stubLoginSecrets(t *testing.T, values ...string) {
	t.Helper()
	original := loginSecretPrompt
	t.Cleanup(func() { loginSecretPrompt = original })
	index := 0
	loginSecretPrompt = func(string, io.Writer) (string, error) {
		if index >= len(values) {
			return "", errors.New("unexpected secret prompt")
		}
		value := values[index]
		index++
		return value, nil
	}
}

func TestExchangeCodeForTokenSurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("\x1b[31minvalid_grant\x1b[0m\x1b]52;c;hidden\a"))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := exchangeCodeForToken(ctx, server.URL, "code", "verifier", "http://127.0.0.1:0/callback")
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v, want 400 error", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") ||
		strings.Contains(err.Error(), "hidden") ||
		strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("unsafe token-exchange error = %q", err)
	}
}

func TestWhoamiUsesHumanIdentityEndpointAndDoesNotSendRecoveryCredential(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	requests := 0
	projectRequests := 0
	expiresAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1alpha/projects" {
			// Non-human credentials are verified against a tenant read
			// endpoint instead of the human-only identity endpoint.
			projectRequests++
			if r.Header.Get("Authorization") != "Bearer operator-recovery-token" {
				t.Fatalf("projects auth=%q, want the operator recovery credential", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
			return
		}
		requests++
		if r.URL.Path != "/api/v1alpha/auth/me" || r.Header.Get("Authorization") != "Bearer midn_user_alpha" {
			t.Fatalf("request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"principal": map[string]any{
				"user_id":      "usr_\x1b[31malice\x1b[0m",
				"email":        "alice@example.com\x1b]52;c;hidden\a",
				"display_name": "Ali\x1b[31mce\x1b[0m",
			},
			"session": map[string]any{
				"id": "ses_\u009b31mcli\u009b0m", "expires_at": expiresAt,
			},
		})
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: cliconfig.HumanSessionMetadata{
			SessionID: "ses_cli", UserID: "usr_alice", Email: "alice@example.com", ExpiresAt: expiresAt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runWhoami([]string{"--api-base", server.URL}, stdout, stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if requests != 1 || !strings.Contains(stdout.String(), "Alice <alice@example.com>") ||
		!strings.Contains(stdout.String(), "session_id: ses_cli") {
		t.Fatalf("requests=%d stdout=%q", requests, stdout.String())
	}
	if strings.Contains(stdout.String(), "hidden") ||
		strings.ContainsRune(stdout.String(), '\x1b') ||
		strings.ContainsRune(stdout.String(), '\u009b') {
		t.Fatalf("whoami rendered terminal controls: %q", stdout.String())
	}

	if err := cliconfig.DeleteToken(server.URL); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetOperatorRecoveryToken(server.URL, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runWhoami([]string{"--api-base", server.URL}, stdout, stderr); code != 0 {
		t.Fatalf("operator classification code=%d stderr=%s", code, stderr.String())
	}
	if requests != 1 || projectRequests != 1 || !strings.Contains(stdout.String(), "operator recovery") ||
		!strings.Contains(stdout.String(), "no human identity") || !strings.Contains(stdout.String(), "accepted by the server") {
		t.Fatalf("operator credential was sent to /auth/me or mislabeled requests=%d projectRequests=%d stdout=%q", requests, projectRequests, stdout.String())
	}
}

func TestWhoamiReportsRejectedBearerCredential(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "bogus-token")
	t.Setenv(cliconfig.EnvAPIToken, "")
	authMeRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1alpha/auth/me" {
			authMeRequests++
		}
		if r.URL.Path != "/api/v1alpha/projects" {
			t.Fatalf("request path=%s, want the projects verification read", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"AUTH_SESSION_INVALID","message":"credential rejected"}}`))
	}))
	defer server.Close()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runWhoami([]string{"--api-base", server.URL}, stdout, stderr); code != 1 {
		t.Fatalf("code=%d, want 1 for a rejected credential; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if authMeRequests != 0 {
		t.Fatalf("non-human credential was sent to /auth/me %d times", authMeRequests)
	}
	if !strings.Contains(stderr.String(), "was rejected") || !strings.Contains(stderr.String(), "AUTH_SESSION_INVALID") {
		t.Fatalf("stderr = %q, want rejection with the server's error code", stderr.String())
	}
	if strings.Contains(stdout.String(), "configured for") {
		t.Fatalf("stdout = %q, want no acceptance claim", stdout.String())
	}
}

func TestLogoutRevokesHumanSessionBeforeLocalDeletionAndPreservesRecovery(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	revoked := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer midn_user_alpha" {
			t.Fatalf("request=%s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1alpha/auth/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"principal": map[string]any{"user_id": "usr_alice", "email": "alice@example.com"},
				"session":   map[string]any{"id": "ses_cli", "expires_at": time.Now().UTC().Add(time.Hour)},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1alpha/auth/sessions/ses_cli":
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request=%s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	if err := cliconfig.SetOperatorRecoveryToken(server.URL, "operator-recovery-token"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: cliconfig.HumanSessionMetadata{
			SessionID: "ses_stale", UserID: "usr_alice", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if code := runLogout([]string{"--api-base", server.URL}, stdout, stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !revoked {
		t.Fatal("remote session was not revoked")
	}
	if _, err := cliconfig.HumanSession(server.URL); !errors.Is(err, cliconfig.ErrNoHumanSession) {
		t.Fatalf("human session remains: %v", err)
	}
	if recovery, err := cliconfig.OperatorRecoveryToken(server.URL); err != nil || recovery != "operator-recovery-token" {
		t.Fatalf("recovery=%q err=%v", recovery, err)
	}
	if _, err := cliconfig.Token(server.URL); !errors.Is(err, cliconfig.ErrNoToken) {
		t.Fatalf("recovery silently promoted into default token: %v", err)
	}
}

func TestLogoutPreservesLocalSessionWhenRemoteRevocationFails(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: cliconfig.HumanSessionMetadata{
			SessionID: "ses_cli", UserID: "usr_alice", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	if code := runLogout([]string{"--api-base", server.URL}, io.Discard, stderr); code == 0 {
		t.Fatalf("logout succeeded stderr=%s", stderr.String())
	}
	if _, err := cliconfig.HumanSession(server.URL); err != nil {
		t.Fatalf("local session was deleted after remote failure: %v", err)
	}
	if !strings.Contains(stderr.String(), "local credential preserved") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestLogoutLocalOnlyNeverContactsServer(t *testing.T) {
	t.Setenv("MIDNIGHT_CONFIG_HOME", t.TempDir())
	t.Setenv(cliconfig.EnvBearerToken, "")
	t.Setenv(cliconfig.EnvAPIToken, "")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	if err := cliconfig.SetHumanSession(server.URL, cliconfig.HumanSessionCredential{
		Token: "midn_user_alpha",
		HumanSessionMetadata: cliconfig.HumanSessionMetadata{
			SessionID: "ses_cli", UserID: "usr_alice", Email: "alice@example.com",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if code := runLogout([]string{"--api-base", server.URL, "--local-only"}, io.Discard, io.Discard); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if requests != 0 {
		t.Fatalf("local-only logout made %d requests", requests)
	}
}

func TestLoginLocalListenerBinds(t *testing.T) {
	// Smoke-test that the loopback listener bind never panics on Windows
	// or unix. We don't go through the full flow here.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	if !strings.HasPrefix(lis.Addr().String(), "127.0.0.1:") {
		t.Fatalf("addr = %s, want loopback", lis.Addr().String())
	}
}
