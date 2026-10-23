// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package oauth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPKCERoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })

	verifier, err := NewCodeVerifier()
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	state, err := NewState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	issued, err := store.IssueCode(AuthorizeRequest{
		ClientID:            DefaultClientID,
		CodeChallenge:       ComputeS256Challenge(verifier),
		CodeChallengeMethod: "S256",
		State:               state,
		RedirectURI:         "http://127.0.0.1:54321/callback",
	})
	if err != nil {
		t.Fatalf("issue code: %v", err)
	}
	if issued.State != state {
		t.Fatalf("state round-trip = %q, want %q", issued.State, state)
	}
	if err := store.Redeem(TokenRequest{
		GrantType:    "authorization_code",
		Code:         issued.Code,
		CodeVerifier: verifier,
		RedirectURI:  "http://127.0.0.1:54321/callback",
		ClientID:     DefaultClientID,
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// Single use: a second redeem must fail.
	if err := store.Redeem(TokenRequest{
		GrantType:    "authorization_code",
		Code:         issued.Code,
		CodeVerifier: verifier,
		RedirectURI:  "http://127.0.0.1:54321/callback",
	}); !errors.Is(err, ErrUnknownCode) {
		t.Fatalf("second redeem err = %v, want ErrUnknownCode", err)
	}
}

func TestPKCEVerifierMismatch(t *testing.T) {
	store := NewStore(nil)
	verifier := "the-correct-verifier-string-with-enough-entropy-1234"
	issued, err := store.IssueCode(AuthorizeRequest{
		ClientID:      DefaultClientID,
		CodeChallenge: ComputeS256Challenge(verifier),
		RedirectURI:   "http://127.0.0.1:0/callback",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	err = store.Redeem(TokenRequest{
		GrantType:    "authorization_code",
		Code:         issued.Code,
		CodeVerifier: "the-wrong-verifier-string-with-enough-entropy-9876",
		RedirectURI:  "http://127.0.0.1:0/callback",
	})
	if !errors.Is(err, ErrCodeVerifierMismatch) {
		t.Fatalf("err = %v, want ErrCodeVerifierMismatch", err)
	}
}

func TestPKCERedirectURIMustMatch(t *testing.T) {
	store := NewStore(nil)
	verifier := "verifier-with-enough-entropy-for-rfc7636-redir-test"
	issued, err := store.IssueCode(AuthorizeRequest{
		ClientID:      DefaultClientID,
		CodeChallenge: ComputeS256Challenge(verifier),
		RedirectURI:   "http://127.0.0.1:54321/callback",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	err = store.Redeem(TokenRequest{
		GrantType:    "authorization_code",
		Code:         issued.Code,
		CodeVerifier: verifier,
		RedirectURI:  "http://127.0.0.1:54322/callback",
	})
	if !errors.Is(err, ErrRedirectURIMismatch) {
		t.Fatalf("err = %v, want ErrRedirectURIMismatch", err)
	}
}

func TestPKCEAuthorizeValidatesChallenge(t *testing.T) {
	if err := ValidateAuthorize(AuthorizeRequest{ClientID: DefaultClientID}); !errors.Is(err, ErrCodeChallengeRequired) {
		t.Fatalf("missing challenge err = %v", err)
	}
	if err := ValidateAuthorize(AuthorizeRequest{ClientID: "rogue", CodeChallenge: "abc"}); !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("rogue client err = %v", err)
	}
	if err := ValidateAuthorize(AuthorizeRequest{ClientID: DefaultClientID, CodeChallenge: "abc", CodeChallengeMethod: "plain"}); !errors.Is(err, ErrCodeChallengeMethodUnsupported) {
		t.Fatalf("plain method err = %v", err)
	}
}

func TestPKCECodeExpires(t *testing.T) {
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	clock := now
	store := NewStore(func() time.Time { return clock })
	verifier := "verifier-with-enough-entropy-for-rfc7636-expir-tst"
	issued, err := store.IssueCode(AuthorizeRequest{
		ClientID:      DefaultClientID,
		CodeChallenge: ComputeS256Challenge(verifier),
		RedirectURI:   "http://127.0.0.1:0/callback",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	clock = clock.Add(AuthorizationCodeTTL + time.Second)
	err = store.Redeem(TokenRequest{
		GrantType:    "authorization_code",
		Code:         issued.Code,
		CodeVerifier: verifier,
		RedirectURI:  "http://127.0.0.1:0/callback",
	})
	if !errors.Is(err, ErrUnknownCode) {
		t.Fatalf("err = %v, want ErrUnknownCode after expiry", err)
	}
}

func TestAuthorizeAcceptsTokenOnlyFromHeaderOrFormBody(t *testing.T) {
	handler := AuthorizeHandler(HandlerConfig{
		Store:       NewStore(nil),
		BearerToken: "operator-secret",
	})
	authorizeURL := "/auth/oauth/authorize?client_id=midnight-cli" +
		"&code_challenge=challenge&code_challenge_method=S256" +
		"&state=state-a&redirect_uri=http%3A%2F%2F127.0.0.1%3A54321%2Fcallback"

	queryToken := httptest.NewRecorder()
	handler.ServeHTTP(queryToken, httptest.NewRequest(http.MethodGet, authorizeURL+"&bootstrap_token=operator-secret", nil))
	if queryToken.Code != http.StatusUnauthorized {
		t.Fatalf("query token status = %d, want %d", queryToken.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(queryToken.Body.String(), `type="password"`) {
		t.Fatalf("unauthenticated page does not render a password form: %s", queryToken.Body.String())
	}

	form := url.Values{"bootstrap_token": []string{"operator-secret"}}
	authorized := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, authorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusSeeOther {
		t.Fatalf("form token status = %d body=%s, want %d", authorized.Code, authorized.Body.String(), http.StatusSeeOther)
	}
	location, err := url.Parse(authorized.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Host != "127.0.0.1:54321" || location.Path != "/callback" || location.Query().Get("code") == "" {
		t.Fatalf("authorize redirect = %q", location.String())
	}
}

func TestOAuthShimRejectsScopedMidnightRecoveryClasses(t *testing.T) {
	for _, token := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/auth/oauth/authorize", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			if defaultAuthorizeAuthenticator(token)(request) {
				t.Fatalf("OAuth authorize accepted scoped token %q", token)
			}

			response := httptest.NewRecorder()
			TokenHandler(HandlerConfig{
				Store:       NewStore(nil),
				BearerToken: token,
			}).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodPost, "/auth/oauth/token", nil),
			)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf(
					"OAuth token endpoint status = %d body=%s",
					response.Code,
					response.Body.String(),
				)
			}
		})
	}
}

func TestTokenHandlerRejectsNonStrictOrOversizedJSON(t *testing.T) {
	handler := TokenHandler(HandlerConfig{
		Store:       NewStore(nil),
		BearerToken: "operator-secret",
	})
	tests := map[string]string{
		"unknown field":     `{"grant_type":"authorization_code","extra":true}`,
		"duplicate field":   `{"code":"first","code":"second"}`,
		"trailing document": `{} {}`,
		"oversized":         strings.Repeat(" ", int(oauthRequestBodyLimitBytes)) + `{}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/auth/oauth/token", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_request"`) {
				t.Fatalf("status = %d body=%s, want invalid_request", response.Code, response.Body.String())
			}
		})
	}
}

func TestAuthorizeRejectsNonLoopbackRedirects(t *testing.T) {
	handler := AuthorizeHandler(HandlerConfig{
		Store:       NewStore(nil),
		BearerToken: "operator-secret",
	})
	for _, redirectURI := range []string{
		"https://attacker.example/callback",
		"http://localhost:54321/callback",
		"http://127.0.0.1:54321/not-callback",
		"http://127.0.0.1/callback",
	} {
		t.Run(redirectURI, func(t *testing.T) {
			values := url.Values{
				"client_id":             []string{DefaultClientID},
				"code_challenge":        []string{"challenge"},
				"code_challenge_method": []string{"S256"},
				"redirect_uri":          []string{redirectURI},
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/auth/oauth/authorize?"+values.Encode(), nil)
			req.Header.Set("Authorization", "Bearer operator-secret")
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
			}
		})
	}
}
