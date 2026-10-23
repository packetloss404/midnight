// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package oauth

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/strictjson"
)

const oauthRequestBodyLimitBytes int64 = 64 << 10

// HandlerConfig wires the OAuth shim into the API router.
type HandlerConfig struct {
	// Store is the authorization-code backing store.
	Store *Store

	// BearerToken is the access token the token endpoint returns on success.
	// For the single-tenant v1 shim this is the operator bearer token.
	BearerToken string

	// AuthorizeAuthenticator decides whether the inbound authorize request
	// represents an already-authenticated operator. The default checks the
	// `Authorization: Bearer <token>` header against BearerToken. The hook is
	// exposed so a future multi-user identity layer can plug in here.
	AuthorizeAuthenticator func(*http.Request) bool
}

// AuthorizeHandler renders a consent page for GET /auth/oauth/authorize and
// redirects to the supplied redirect_uri with a fresh authorization code on
// POST /auth/oauth/authorize (consent submission).
func AuthorizeHandler(cfg HandlerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.Store == nil {
			http.Error(w, "oauth store not configured", http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, oauthRequestBodyLimitBytes)
		}
		req, err := parseAuthorizeRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := ValidateAuthorize(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		authn := cfg.AuthorizeAuthenticator
		if authn == nil {
			authn = defaultAuthorizeAuthenticator(cfg.BearerToken)
		}
		if !authn(r) {
			renderUnauthenticatedPage(w, req)
			return
		}
		if r.Method == http.MethodGet {
			renderConsentPage(w, req)
			return
		}
		// Consent POST: mint a code and 302 to the redirect_uri.
		issued, err := cfg.Store.IssueCode(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		callback, err := url.Parse(req.RedirectURI)
		if err != nil {
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return
		}
		q := callback.Query()
		q.Set("code", issued.Code)
		q.Set("state", req.State)
		callback.RawQuery = q.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
	}
}

// TokenHandler implements POST /auth/oauth/token. On success it returns an
// RFC 6749 token response whose access_token is the configured bearer
// token. The shim never mints a new token; it only gates the bearer behind
// the PKCE round-trip so the CLI's browser flow has something to exchange.
func TokenHandler(cfg HandlerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cfg.Store == nil {
			http.Error(w, "oauth store not configured", http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		expected := strings.TrimSpace(cfg.BearerToken)
		if expected == "" || auth.ValidateOperatorRecoveryToken(expected) != nil {
			writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "operator bearer token is not configured")
			return
		}
		var req TokenRequest
		r.Body = http.MaxBytesReader(w, r.Body, oauthRequestBodyLimitBytes)
		if contentType := r.Header.Get("Content-Type"); strings.HasPrefix(contentType, "application/json") {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				err = strictjson.Decode(body, &req)
			}
			if err != nil {
				writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
			req = TokenRequest{
				GrantType:    r.Form.Get("grant_type"),
				Code:         r.Form.Get("code"),
				CodeVerifier: r.Form.Get("code_verifier"),
				RedirectURI:  r.Form.Get("redirect_uri"),
				ClientID:     r.Form.Get("client_id"),
			}
		}
		if err := cfg.Store.Redeem(req); err != nil {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", err.Error())
			return
		}
		response := map[string]any{
			"access_token": cfg.BearerToken,
			"token_type":   "Bearer",
			"expires_in":   0,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func defaultAuthorizeAuthenticator(bearerToken string) func(*http.Request) bool {
	expected := strings.TrimSpace(bearerToken)
	return func(r *http.Request) bool {
		if expected == "" || auth.ValidateOperatorRecoveryToken(expected) != nil {
			return false
		}
		header := r.Header.Get("Authorization")
		parts := strings.Fields(header)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && tokenEqual(parts[1], expected) {
			return true
		}
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err == nil {
				if got := strings.TrimSpace(r.PostForm.Get("bootstrap_token")); got != "" && tokenEqual(got, expected) {
					return true
				}
			}
		}
		return false
	}
}

func tokenEqual(got, expected string) bool {
	got = strings.TrimSpace(got)
	if len(got) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func parseAuthorizeRequest(r *http.Request) (AuthorizeRequest, error) {
	values := r.URL.Query()
	req := AuthorizeRequest{
		ClientID:            values.Get("client_id"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
		State:               values.Get("state"),
		RedirectURI:         values.Get("redirect_uri"),
		Scope:               values.Get("scope"),
	}
	if req.ClientID == "" || req.CodeChallenge == "" || req.RedirectURI == "" {
		return req, fmt.Errorf("oauth: client_id, code_challenge, and redirect_uri are required")
	}
	parsed, err := url.Parse(req.RedirectURI)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return req, fmt.Errorf("oauth: redirect_uri must be an HTTP loopback URL")
	}
	host := net.ParseIP(parsed.Hostname())
	if host == nil || !host.IsLoopback() || parsed.Port() == "" || parsed.EscapedPath() != "/callback" {
		return req, fmt.Errorf("oauth: redirect_uri must use a loopback IP, explicit port, and /callback path")
	}
	return req, nil
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func renderUnauthenticatedPage(w http.ResponseWriter, req AuthorizeRequest) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>Midnight login</title></head>
<body style="font-family:system-ui;max-width:520px;margin:60px auto;color:#101820;">
<h1 style="font-weight:600;font-size:1.4rem;">Operator authentication required</h1>
<p>Enter the operator token to authorize <strong>` + html.EscapeString(req.ClientID) + `</strong>.
The token is submitted in the request body and is never placed in the URL.</p>
<form method="POST">
  <label for="bootstrap_token" style="display:block;margin-bottom:6px;">Operator token</label>
  <input id="bootstrap_token" name="bootstrap_token" type="password" autocomplete="current-password"
    required style="box-sizing:border-box;width:100%;padding:9px;margin-bottom:12px;">
  <button type="submit" style="padding:8px 16px;background:#101820;color:#fff;border:0;border-radius:6px;cursor:pointer;">Authorize CLI</button>
</form>
<p>The proper multi-user OAuth surface lands when accounts land; this is the v1 shim.</p>
</body></html>`))
}

func renderConsentPage(w http.ResponseWriter, req AuthorizeRequest) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	page := `<!doctype html><html><head><title>Authorize midnight CLI</title></head>
<body style="font-family:system-ui;max-width:520px;margin:60px auto;color:#101820;">
<h1 style="font-weight:600;font-size:1.4rem;">Authorize the midnight CLI</h1>
<p>The midnight command-line client wants to obtain an access token to act
on your behalf.</p>
<p><strong>Client:</strong> ` + html.EscapeString(req.ClientID) + `<br>
<strong>Callback:</strong> ` + html.EscapeString(req.RedirectURI) + `</p>
<form method="POST">
  <input type="hidden" name="client_id" value="` + html.EscapeString(req.ClientID) + `">
  <input type="hidden" name="code_challenge" value="` + html.EscapeString(req.CodeChallenge) + `">
  <input type="hidden" name="code_challenge_method" value="S256">
  <input type="hidden" name="state" value="` + html.EscapeString(req.State) + `">
  <input type="hidden" name="redirect_uri" value="` + html.EscapeString(req.RedirectURI) + `">
  <button type="submit" style="padding:8px 16px;background:#101820;color:#fff;border:0;border-radius:6px;cursor:pointer;">Authorize</button>
</form>
</body></html>`
	_, _ = w.Write([]byte(page))
}
