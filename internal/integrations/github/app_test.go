// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustGenTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(pk)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	return pk, pemBytes
}

func TestAppConfigValidate(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	cases := map[string]struct {
		cfg AppConfig
		ok  bool
	}{
		"valid":   {AppConfig{AppID: 1, PrivateKeyPEM: pemBytes, WebhookSecret: "shh"}, true},
		"no id":   {AppConfig{PrivateKeyPEM: pemBytes, WebhookSecret: "shh"}, false},
		"no key":  {AppConfig{AppID: 1, WebhookSecret: "shh"}, false},
		"no sec":  {AppConfig{AppID: 1, PrivateKeyPEM: pemBytes}, false},
		"bad key": {AppConfig{AppID: 1, PrivateKeyPEM: []byte("nope"), WebhookSecret: "shh"}, false},
	}
	for name, tc := range cases {
		err := tc.cfg.Validate()
		if tc.ok && err != nil {
			t.Errorf("%s: want valid, got %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestAppConfigTokenMintingDoesNotRequireWebhookSecret(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	cfg := AppConfig{AppID: 7, PrivateKeyPEM: pemBytes}
	if err := cfg.ValidateTokenMinting(); err != nil {
		t.Fatalf("ValidateTokenMinting() error = %v", err)
	}
	if _, err := NewAppClient(cfg); err != nil {
		t.Fatalf("NewAppClient() error = %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want webhook receiver config to remain fail-closed")
	}
}

func TestMintJWTSignsRS256(t *testing.T) {
	pk, pemBytes := mustGenTestKey(t)
	fixed := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh"}, WithClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	jwt, err := c.MintJWT()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt parts = %d", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("header = %+v", header)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	// JSON numbers come back as float64
	if int64(payload["iss"].(float64)) != 7 {
		t.Errorf("iss = %v", payload["iss"])
	}
	iat := int64(payload["iat"].(float64))
	exp := int64(payload["exp"].(float64))
	if iat != fixed.Add(-60*time.Second).Unix() {
		t.Errorf("iat = %d", iat)
	}
	if exp <= iat {
		t.Errorf("exp(%d) <= iat(%d)", exp, iat)
	}
	// Verify signature.
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&pk.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestMintInstallationTokenExchange(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"token":"ghs_test","expires_at":"2026-05-01T13:00:00Z"}`))
	}))
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	tok, err := c.MintInstallationToken(context.Background(), 42)
	if err != nil {
		t.Fatalf("mint installation: %v", err)
	}
	if tok.Token.Value() != "ghs_test" {
		t.Errorf("token = %s", tok.Token.Value())
	}
	if !strings.HasPrefix(seenAuth, "Bearer ") {
		t.Errorf("auth = %s", seenAuth)
	}
}

func TestMintRepositoryTokenScopesExchangeToExactRepository(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	var request struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode token scope: %v", err)
		}
		_, _ = w.Write([]byte(`{"token":"ghs_repo","expires_at":"2026-05-01T13:00:00Z"}`))
	}))
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := c.MintRepositoryToken(context.Background(), 42, 11)
	if err != nil {
		t.Fatalf("MintRepositoryToken() error = %v", err)
	}
	if tok.Token.Value() != "ghs_repo" {
		t.Errorf("token = %q", tok.Token.Value())
	}
	if len(request.RepositoryIDs) != 1 || request.RepositoryIDs[0] != 11 {
		t.Fatalf("repository_ids = %#v, want [11]", request.RepositoryIDs)
	}
	if len(request.Permissions) != 1 || request.Permissions["contents"] != "read" {
		t.Fatalf("permissions = %#v, want contents:read only", request.Permissions)
	}
}

func TestMintInstallationTokenRejectsOversizeResponse(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
	}))
	defer srv.Close()
	client, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.MintInstallationToken(context.Background(), 42); !errors.Is(err, ErrAppHTTP) {
		t.Fatalf("MintInstallationToken() error = %v, want ErrAppHTTP", err)
	}
}

func TestMintInstallationTokenSurfacesAPIErr(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = c.MintInstallationToken(context.Background(), 42)
	if !errors.Is(err, ErrAppHTTP) {
		t.Errorf("want ErrAppHTTP, got %v", err)
	}
}

func TestMintInstallationTokenRedactsEchoedAppJWTFromAPIError(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	var echoedAuthorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		echoedAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"` + echoedAuthorization + `"}`))
	}))
	defer srv.Close()
	client, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.MintInstallationToken(context.Background(), 42)
	if !errors.Is(err, ErrAppHTTP) {
		t.Fatalf("MintInstallationToken() error = %v", err)
	}
	if echoedAuthorization == "" || strings.Contains(err.Error(), echoedAuthorization) {
		t.Fatalf("API error leaked App JWT: %v", err)
	}
	jwtPrefix := strings.SplitN(echoedAuthorization, " ", 2)[1][:32]
	if strings.Contains(err.Error(), jwtPrefix) {
		t.Fatalf("API error leaked a truncated App JWT prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("API error did not mark redaction: %v", err)
	}
}

func TestListInstallations(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":1,"account":{"login":"acme"},"target_type":"Organization"}]`))
	}))
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	insts, err := c.ListInstallations(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(insts) != 1 || insts[0].ID != 1 || insts[0].Account != "acme" {
		t.Errorf("insts = %+v", insts)
	}
}

func TestGetInstallationResolvesExactServerIdentity(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/42" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":42,"account":{"login":"acme"},"target_type":"Organization","html_url":"https://github.com/settings/installations/42"}`))
	}))
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	installation, err := c.GetInstallation(context.Background(), 42)
	if err != nil {
		t.Fatalf("GetInstallation() error = %v", err)
	}
	if installation.ID != 42 || installation.Account != "acme" || installation.TargetType != "Organization" {
		t.Fatalf("installation = %#v", installation)
	}
}

func TestListReposForInstallation(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"ghs_x","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "token ") {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"total_count":1,"repositories":[{"id":11,"full_name":"acme/web","private":true,"clone_url":"https://github.com/acme/web.git","default_branch":"main"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	repos, err := c.ListReposForInstallation(context.Background(), 42)
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	if len(repos) != 1 || repos[0].FullName != "acme/web" {
		t.Errorf("repos = %+v", repos)
	}
}

func TestListReposForInstallationExhaustsPagination(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	tokenRequests := 0
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests++
		_, _ = w.Write([]byte(`{"token":"ghs_x","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	pages := make([]string, 0, 2)
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		page := r.URL.Query().Get("page")
		response := rawInstallationRepos{TotalCount: 101}
		if page == "1" {
			response.Repositories = make([]Repo, 100)
			for i := range response.Repositories {
				response.Repositories[i] = Repo{ID: int64(i + 1), FullName: fmt.Sprintf("acme/repo-%d", i+1)}
			}
		} else {
			response.Repositories = []Repo{{ID: 101, FullName: "acme/repo-101"}}
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, WebhookSecret: "shh", APIBaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	repos, err := c.ListReposForInstallation(context.Background(), 42)
	if err != nil {
		t.Fatalf("list repos: %v", err)
	}
	if len(repos) != 101 || repos[100].FullName != "acme/repo-101" {
		t.Fatalf("repos = %d, last = %+v", len(repos), repos[len(repos)-1])
	}
	if tokenRequests != 1 || strings.Join(pages, ",") != "1,2" {
		t.Fatalf("token requests = %d, pages = %v", tokenRequests, pages)
	}
}

func TestVerifyRepositoryBranchUsesServerObservedRepositoryIdentity(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	tokenRequests := 0
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		tokenRequests++
		var request struct {
			RepositoryIDs []int64 `json:"repository_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode token request: %v", err)
		}
		if len(request.RepositoryIDs) != 1 || request.RepositoryIDs[0] != 11 {
			t.Errorf("repository_ids = %#v", request.RepositoryIDs)
		}
		_, _ = w.Write([]byte(`{"token":"ghs_exact","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	mux.HandleFunc("/repositories/11", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token ghs_exact" {
			t.Errorf("repository auth = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":11,"full_name":"acme/web","private":true,"clone_url":"https://github.com/acme/web.git","default_branch":"main"}`))
	})
	mux.HandleFunc("/repos/acme/web/branches/release", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token ghs_exact" {
			t.Errorf("branch auth = %q", got)
		}
		_, _ = w.Write([]byte(`{"name":"release"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := c.VerifyRepositoryBranch(context.Background(), 42, 11, "release")
	if err != nil {
		t.Fatalf("VerifyRepositoryBranch() error = %v", err)
	}
	if repo.ID != 11 || repo.FullName != "acme/web" || repo.Default != "main" {
		t.Fatalf("repo = %#v", repo)
	}
	if tokenRequests != 2 {
		t.Fatalf("token requests = %d, want 2 exact-scope exchanges", tokenRequests)
	}
}

func TestListBranchesForRepositoryReturnsOnlyBranchNames(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"ghs_exact","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	mux.HandleFunc("/repositories/11", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":11,"full_name":"acme/web","private":false,"clone_url":"https://github.com/acme/web.git","default_branch":"main"}`))
	})
	mux.HandleFunc("/repos/acme/web/branches", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"main"},{"name":"release"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	branches, err := c.ListBranchesForRepository(context.Background(), 42, 11)
	if err != nil {
		t.Fatalf("ListBranchesForRepository() error = %v", err)
	}
	if len(branches) != 2 || branches[0].Name != "main" || branches[1].Name != "release" {
		t.Fatalf("branches = %#v", branches)
	}
}

func TestListBranchesForRepositoryPaginatesWithoutSilentTruncation(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/42/access_tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"ghs_exact","expires_at":"2026-05-01T13:00:00Z"}`))
	})
	mux.HandleFunc("/repositories/11", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":11,"full_name":"acme/web","private":false,"clone_url":"https://github.com/acme/web.git","default_branch":"main"}`))
	})
	mux.HandleFunc("/repos/acme/web/branches", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		var branches []Branch
		switch page {
		case "1":
			branches = make([]Branch, 100)
			for i := range branches {
				branches[i] = Branch{Name: fmt.Sprintf("branch-%03d", i)}
			}
		case "2":
			branches = []Branch{{Name: "branch-100"}}
		default:
			t.Fatalf("unexpected branch page %q", page)
		}
		if err := json.NewEncoder(w).Encode(branches); err != nil {
			t.Fatal(err)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := NewAppClient(AppConfig{AppID: 7, PrivateKeyPEM: pemBytes, APIBaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	branches, err := c.ListBranchesForRepository(context.Background(), 42, 11)
	if err != nil {
		t.Fatalf("ListBranchesForRepository() error = %v", err)
	}
	if len(branches) != 101 || branches[0].Name != "branch-000" || branches[100].Name != "branch-100" {
		t.Fatalf("branches = %#v", branches)
	}
}

func TestLoadAppConfigFromEnv(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	env := map[string]string{
		"MIDNIGHT_GITHUB_APP_ID":           "12",
		"MIDNIGHT_GITHUB_PRIVATE_KEY_PATH": "/dev/keys/midnight.pem",
		"MIDNIGHT_GITHUB_WEBHOOK_SECRET":   "shh",
		"MIDNIGHT_GITHUB_CLIENT_ID":        "Iv1.abc",
	}
	get := func(k string) string { return env[k] }
	read := func(p string) ([]byte, error) {
		if p != "/dev/keys/midnight.pem" {
			t.Errorf("path = %s", p)
		}
		return pemBytes, nil
	}
	cfg, err := LoadAppConfig(get, read)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AppID != 12 || cfg.WebhookSecret != "shh" || cfg.ClientID != "Iv1.abc" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadAppConfigEnvMissing(t *testing.T) {
	get := func(string) string { return "" }
	_, err := LoadAppConfig(get, func(string) ([]byte, error) { return nil, nil })
	if !errors.Is(err, ErrAppConfigInvalid) {
		t.Errorf("want invalid, got %v", err)
	}
}

func TestLoadTokenMintingAppConfigDoesNotRequireWebhookSecret(t *testing.T) {
	_, pemBytes := mustGenTestKey(t)
	env := map[string]string{
		"MIDNIGHT_GITHUB_APP_ID":           "12",
		"MIDNIGHT_GITHUB_PRIVATE_KEY_PATH": "/dev/keys/midnight.pem",
		"MIDNIGHT_GITHUB_API_BASE_URL":     "https://github.example/api/v3",
	}
	cfg, err := LoadTokenMintingAppConfig(
		func(key string) string { return env[key] },
		func(string) ([]byte, error) { return pemBytes, nil },
	)
	if err != nil {
		t.Fatalf("load token config: %v", err)
	}
	if cfg.AppID != 12 || cfg.APIBaseURL != env["MIDNIGHT_GITHUB_API_BASE_URL"] || cfg.WebhookSecret != "" {
		t.Fatalf("token config = %+v", cfg)
	}
}

func TestMapCheckConclusion(t *testing.T) {
	if mapCheckConclusion("queued", "") != CheckPending {
		t.Errorf("queued")
	}
	if mapCheckConclusion("completed", "success") != CheckSuccess {
		t.Errorf("success")
	}
	if mapCheckConclusion("completed", "failure") != CheckFailure {
		t.Errorf("failure")
	}
	if mapCheckConclusion("completed", "unknown") != CheckMissing {
		t.Errorf("unknown")
	}
}
