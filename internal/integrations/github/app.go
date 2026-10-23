// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"bytes"
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
	"net/url"
	"strings"
	"time"
)

// AppConfig captures the operator-supplied GitHub App credentials.
//
// Loaded at startup from env vars / config file:
//
//   - MIDNIGHT_GITHUB_APP_ID
//   - MIDNIGHT_GITHUB_PRIVATE_KEY_PATH (path to PEM-encoded RSA private key)
//   - MIDNIGHT_GITHUB_WEBHOOK_SECRET
//   - MIDNIGHT_GITHUB_CLIENT_ID (optional, for OAuth flow)
//   - MIDNIGHT_GITHUB_CLIENT_SECRET (optional, for OAuth flow)
//   - MIDNIGHT_GITHUB_API_BASE_URL (optional override; defaults to https://api.github.com)
//   - MIDNIGHT_GITHUB_WEB_BASE_URL (required for GHES browser setup)
//   - MIDNIGHT_GITHUB_APP_SLUG (required for browser setup)
//   - MIDNIGHT_GITHUB_CALLBACK_URL (required for browser setup)
type AppConfig struct {
	AppID         int64  `json:"app_id"`
	PrivateKeyPEM []byte `json:"-"`
	WebhookSecret string `json:"-"`
	ClientID      string `json:"client_id,omitempty"`
	ClientSecret  string `json:"-"`
	APIBaseURL    string `json:"api_base_url,omitempty"`
	WebBaseURL    string `json:"web_base_url,omitempty"`
	AppSlug       string `json:"app_slug,omitempty"`
	CallbackURL   string `json:"callback_url,omitempty"`
}

// Validate returns ErrAppConfigInvalid when required server-side App fields
// are missing. The webhook secret is required by the inbound receiver, but it
// is deliberately not required by ValidateTokenMinting: build workers only
// need the App id and private key to mint an installation token.
func (c AppConfig) Validate() error {
	if err := c.ValidateTokenMinting(); err != nil {
		return err
	}
	if strings.TrimSpace(c.WebhookSecret) == "" {
		return fmt.Errorf("%w: webhook_secret is required", ErrAppConfigInvalid)
	}
	return nil
}

// ValidateTokenMinting validates the least-privilege credential subset needed
// by a build worker. Keeping this separate prevents operators from having to
// distribute the webhook HMAC secret to the checkout worker.
func (c AppConfig) ValidateTokenMinting() error {
	if c.AppID <= 0 {
		return fmt.Errorf("%w: app_id is required", ErrAppConfigInvalid)
	}
	if len(c.PrivateKeyPEM) == 0 {
		return fmt.Errorf("%w: private_key_pem is required", ErrAppConfigInvalid)
	}
	if _, err := parseRSAPrivateKey(c.PrivateKeyPEM); err != nil {
		return fmt.Errorf("%w: %v", ErrAppConfigInvalid, err)
	}
	return nil
}

// APIBase returns the configured base URL or the public GitHub default.
func (c AppConfig) APIBase() string {
	if v := strings.TrimSpace(c.APIBaseURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

// WebBase returns the GitHub browser origin used for installation and OAuth.
// GHES operators should set MIDNIGHT_GITHUB_WEB_BASE_URL explicitly; the
// public GitHub API maps to github.com by default.
func (c AppConfig) WebBase() string {
	if v := strings.TrimSpace(c.WebBaseURL); v != "" {
		return strings.TrimRight(v, "/")
	}
	if c.APIBase() == "https://api.github.com" {
		return "https://github.com"
	}
	return ""
}

// ValidateSetup validates the additional operator configuration needed for
// the browser installation/OAuth callback. The core App remains usable for
// webhooks and checkout when these optional fields are absent.
func (c AppConfig) ValidateSetup() error {
	if err := c.ValidateTokenMinting(); err != nil {
		return err
	}
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientSecret) == "" {
		return fmt.Errorf("%w: client_id and client_secret are required for setup", ErrAppConfigInvalid)
	}
	if !validAppSlug(c.AppSlug) {
		return fmt.Errorf("%w: app_slug is invalid", ErrAppConfigInvalid)
	}
	callback, err := validatedHTTPSURL(c.CallbackURL)
	if err != nil {
		return fmt.Errorf("%w: callback_url: %v", ErrAppConfigInvalid, err)
	}
	if callback.Path != "/api/v1alpha/github/setup/callback" || callback.RawQuery != "" {
		return fmt.Errorf("%w: callback_url must target /api/v1alpha/github/setup/callback without query", ErrAppConfigInvalid)
	}
	webBase, err := validatedHTTPSURL(c.WebBase())
	if err != nil {
		return fmt.Errorf("%w: web_base_url: %v", ErrAppConfigInvalid, err)
	}
	if (webBase.Path != "" && webBase.Path != "/") || webBase.RawQuery != "" {
		return fmt.Errorf("%w: web_base_url must be an origin without path or query", ErrAppConfigInvalid)
	}
	return nil
}

// InstallationURL returns the exact GitHub App installation URL carrying one
// opaque, signed Midnight state. No workspace or account identity is added as
// a separate query parameter.
func (c AppConfig) InstallationURL(state string) (string, error) {
	if err := c.ValidateSetup(); err != nil {
		return "", err
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return "", fmt.Errorf("%w: setup state is required", ErrAppConfigInvalid)
	}
	base, _ := validatedHTTPSURL(c.WebBase())
	base.Path = "/apps/" + c.AppSlug + "/installations/new"
	base.RawQuery = url.Values{"state": []string{state}}.Encode()
	return base.String(), nil
}

func validAppSlug(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 100 || strings.Trim(value, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		return false
	}
	return value[0] != '-' && value[len(value)-1] != '-'
}

func validatedHTTPSURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("an absolute https URL without credentials or fragment is required")
	}
	return parsed, nil
}

// ErrAppConfigInvalid is returned when the AppConfig fails validation.
var ErrAppConfigInvalid = errors.New("github: app config invalid")

// ErrAppHTTP is returned for non-2xx responses from the GitHub API.
var ErrAppHTTP = errors.New("github: app api error")

// AppHTTPError preserves a GitHub response status for safe transport mapping
// while keeping the response body bounded and token-scrubbed.
type AppHTTPError struct {
	StatusCode int
	Body       string
}

func (e *AppHTTPError) Error() string {
	if e == nil {
		return ErrAppHTTP.Error()
	}
	return fmt.Sprintf("%s: status=%d body=%s", ErrAppHTTP, e.StatusCode, e.Body)
}

func (e *AppHTTPError) Unwrap() error { return ErrAppHTTP }

// HTTPDoer is the minimal http.Client subset AppClient consumes. Tests
// inject a stub.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Clock returns the current time. Defaults to time.Now (UTC).
type Clock func() time.Time

// AppClient mints JWTs and exchanges them for short-lived installation
// access tokens. All HTTP requests honour the AppConfig.APIBase override
// so tests can target an httptest.Server.
type AppClient struct {
	cfg    AppConfig
	doer   HTTPDoer
	clock  Clock
	jwtTTL time.Duration
}

// NewAppClient validates the supplied config and returns an AppClient.
func NewAppClient(cfg AppConfig, opts ...AppClientOption) (*AppClient, error) {
	if err := cfg.ValidateTokenMinting(); err != nil {
		return nil, err
	}
	c := &AppClient{
		cfg:    cfg,
		doer:   &http.Client{Timeout: 30 * time.Second},
		clock:  func() time.Time { return time.Now().UTC() },
		jwtTTL: 9 * time.Minute, // GitHub allows up to 10 minutes
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// AppClientOption customises an AppClient.
type AppClientOption func(*AppClient)

// WithHTTPDoer overrides the default http.Client.
func WithHTTPDoer(d HTTPDoer) AppClientOption {
	return func(c *AppClient) {
		if d != nil {
			c.doer = d
		}
	}
}

// WithClock overrides the default clock (testing).
func WithClock(clk Clock) AppClientOption {
	return func(c *AppClient) {
		if clk != nil {
			c.clock = clk
		}
	}
}

// WithJWTTTL customises the JWT lifetime (max 10 minutes per GitHub).
func WithJWTTTL(d time.Duration) AppClientOption {
	return func(c *AppClient) {
		if d > 0 && d <= 10*time.Minute {
			c.jwtTTL = d
		}
	}
}

// MintJWT returns an RS256-signed bearer token suitable for the
// /app/installations/{id}/access_tokens exchange. The JWT iat is offset
// by -60s per GitHub's tolerance guidance.
func (c *AppClient) MintJWT() (string, error) {
	pk, err := parseRSAPrivateKey(c.cfg.PrivateKeyPEM)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrAppConfigInvalid, err)
	}
	now := c.clock().UTC()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	payload := map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(c.jwtTTL).Unix(),
		"iss": c.cfg.AppID,
	}
	return signRS256JWT(pk, header, payload)
}

// InstallationToken is the short-lived token returned by GitHub.
//
// Token is a redacting Secret and deliberately carries NO `json:"token"` tag:
// the field is populated by an explicit decode step in MintInstallationToken
// (below) so a stray json.Marshal or %v on an InstallationToken can never spill
// the live token. Read the real value via Token.Value().
type InstallationToken struct {
	Token     Secret    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MintInstallationToken exchanges the App JWT for a short-lived
// installation token (panel-locked TTL: <= 1 hour per GitHub).
func (c *AppClient) MintInstallationToken(ctx context.Context, installationID int64) (InstallationToken, error) {
	return c.mintInstallationToken(ctx, installationID, nil, nil)
}

// MintRepositoryToken exchanges the App JWT for an installation token that is
// restricted to one exact repository. Product source verification and checkout
// paths use this instead of an installation-wide token so a compromised request
// cannot widen itself from one workspace grant to every repository visible to
// the GitHub App installation.
func (c *AppClient) MintRepositoryToken(ctx context.Context, installationID, repositoryID int64) (InstallationToken, error) {
	if repositoryID <= 0 {
		return InstallationToken{}, fmt.Errorf("%w: repository_id must be positive", ErrAppConfigInvalid)
	}
	return c.mintInstallationToken(
		ctx,
		installationID,
		[]int64{repositoryID},
		map[string]string{"contents": "read"},
	)
}

func (c *AppClient) mintInstallationToken(
	ctx context.Context,
	installationID int64,
	repositoryIDs []int64,
	permissions map[string]string,
) (InstallationToken, error) {
	if installationID <= 0 {
		return InstallationToken{}, fmt.Errorf("%w: installation_id must be positive", ErrAppConfigInvalid)
	}
	jwt, err := c.MintJWT()
	if err != nil {
		return InstallationToken{}, err
	}
	jwtRedactor := newRedactor(jwt, "Bearer "+jwt)
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.cfg.APIBase(), installationID)
	var requestBody io.Reader
	if len(repositoryIDs) > 0 || len(permissions) > 0 {
		body, marshalErr := json.Marshal(struct {
			RepositoryIDs []int64           `json:"repository_ids,omitempty"`
			Permissions   map[string]string `json:"permissions,omitempty"`
		}{
			RepositoryIDs: repositoryIDs,
			Permissions:   permissions,
		})
		if marshalErr != nil {
			return InstallationToken{}, marshalErr
		}
		requestBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, requestBody)
	if err != nil {
		return InstallationToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return InstallationToken{}, errors.New(jwtRedactor.redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return InstallationToken{}, err
	}
	if resp.StatusCode/100 != 2 {
		safeBody := jwtRedactor.redact(string(body))
		return InstallationToken{}, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, truncate([]byte(safeBody), 256))
	}
	// Decode the raw token into a redacting Secret via a private struct so the
	// exported InstallationToken never carries a `json:"token"` tag.
	var raw struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return InstallationToken{}, fmt.Errorf("%w: decode: %v", ErrAppHTTP, err)
	}
	if strings.TrimSpace(raw.Token) == "" {
		return InstallationToken{}, fmt.Errorf("%w: empty token", ErrAppHTTP)
	}
	return InstallationToken{Token: NewSecret(raw.Token), ExpiresAt: raw.ExpiresAt}, nil
}

// Installation describes one App installation visible to the operator.
type Installation struct {
	ID         int64  `json:"id"`
	Account    string `json:"account"`
	TargetType string `json:"target_type,omitempty"`
	HTMLURL    string `json:"html_url,omitempty"`
}

type rawInstallation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
	TargetType string `json:"target_type"`
	HTMLURL    string `json:"html_url"`
}

// ListInstallations enumerates installations of this App. The caller
// uses this to drive the link-flow UI.
func (c *AppClient) ListInstallations(ctx context.Context) ([]Installation, error) {
	jwt, err := c.MintJWT()
	if err != nil {
		return nil, err
	}
	url := c.cfg.APIBase() + "/app/installations?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, truncate(body, 256))
	}
	var raw []rawInstallation
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrAppHTTP, err)
	}
	out := make([]Installation, 0, len(raw))
	for _, r := range raw {
		out = append(out, Installation{ID: r.ID, Account: r.Account.Login, TargetType: r.TargetType, HTMLURL: r.HTMLURL})
	}
	return out, nil
}

// GetInstallation resolves one exact App installation with the App JWT. Setup
// and recovery flows use this instead of trusting installation account metadata
// supplied by a client.
func (c *AppClient) GetInstallation(ctx context.Context, installationID int64) (Installation, error) {
	if installationID <= 0 {
		return Installation{}, fmt.Errorf("%w: installation_id must be positive", ErrAppConfigInvalid)
	}
	jwt, err := c.MintJWT()
	if err != nil {
		return Installation{}, err
	}
	jwtRedactor := newRedactor(jwt, "Bearer "+jwt)
	endpoint := fmt.Sprintf("%s/app/installations/%d", c.cfg.APIBase(), installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Installation{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return Installation{}, errors.New(jwtRedactor.redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return Installation{}, err
	}
	if resp.StatusCode/100 != 2 {
		return Installation{}, &AppHTTPError{
			StatusCode: resp.StatusCode,
			Body:       jwtRedactor.redact(truncate(body, 256)),
		}
	}
	var raw rawInstallation
	if err := json.Unmarshal(body, &raw); err != nil {
		return Installation{}, fmt.Errorf("%w: decode installation: %v", ErrAppHTTP, err)
	}
	if raw.ID != installationID || strings.TrimSpace(raw.Account.Login) == "" {
		return Installation{}, fmt.Errorf("%w: installation response identity is incomplete", ErrAppHTTP)
	}
	return Installation{
		ID:         raw.ID,
		Account:    raw.Account.Login,
		TargetType: raw.TargetType,
		HTMLURL:    raw.HTMLURL,
	}, nil
}

// Repo is one installation-visible repository.
type Repo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	CloneURL string `json:"clone_url"`
	HTMLURL  string `json:"html_url,omitempty"`
	Default  string `json:"default_branch,omitempty"`
}

type rawInstallationRepos struct {
	TotalCount   int    `json:"total_count"`
	Repositories []Repo `json:"repositories"`
}

// ListReposForInstallation returns the repos the installation grants
// access to. Uses the installation token (not the App JWT).
func (c *AppClient) ListReposForInstallation(ctx context.Context, installationID int64) ([]Repo, error) {
	tok, err := c.MintInstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	const (
		repositoriesPerPage = 100
		maxRepositoryPages  = 100
	)
	repositories := make([]Repo, 0, repositoriesPerPage)
	seen := make(map[int64]struct{}, repositoriesPerPage)
	for page := 1; page <= maxRepositoryPages; page++ {
		endpoint := fmt.Sprintf(
			"%s/installation/repositories?per_page=%d&page=%d",
			c.cfg.APIBase(), repositoriesPerPage, page,
		)
		body, err := c.doInstallationGET(ctx, endpoint, tok)
		if err != nil {
			return nil, err
		}
		var raw rawInstallationRepos
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("%w: decode repositories: %v", ErrAppHTTP, err)
		}
		if raw.TotalCount < 0 {
			return nil, fmt.Errorf("%w: repository response total_count is invalid", ErrAppHTTP)
		}
		for _, repository := range raw.Repositories {
			if repository.ID <= 0 || strings.TrimSpace(repository.FullName) == "" {
				return nil, fmt.Errorf("%w: repository response identity is incomplete", ErrAppHTTP)
			}
			if _, duplicate := seen[repository.ID]; duplicate {
				continue
			}
			seen[repository.ID] = struct{}{}
			repositories = append(repositories, repository)
		}
		if len(raw.Repositories) < repositoriesPerPage {
			if raw.TotalCount > len(repositories) {
				return nil, fmt.Errorf(
					"%w: repository pagination ended at %d of %d entries",
					ErrAppHTTP, len(repositories), raw.TotalCount,
				)
			}
			return repositories, nil
		}
	}
	return nil, fmt.Errorf(
		"%w: repository response exceeds %d entries",
		ErrAppHTTP, repositoriesPerPage*maxRepositoryPages,
	)
}

// Branch is a repository branch visible to a repository-scoped installation
// token. Only its immutable name is returned to product discovery callers.
type Branch struct {
	Name string `json:"name"`
}

// GetRepositoryForInstallation resolves one repository by numeric GitHub id
// with a token restricted to that same repository. The returned name, clone
// URL, privacy bit, and default branch are server-observed metadata; callers
// must never persist client-supplied substitutes for these fields.
func (c *AppClient) GetRepositoryForInstallation(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
) (Repo, error) {
	tok, err := c.MintRepositoryToken(ctx, installationID, repositoryID)
	if err != nil {
		return Repo{}, err
	}
	endpoint := fmt.Sprintf("%s/repositories/%d", c.cfg.APIBase(), repositoryID)
	body, err := c.doInstallationGET(ctx, endpoint, tok)
	if err != nil {
		return Repo{}, err
	}
	var repo Repo
	if err := json.Unmarshal(body, &repo); err != nil {
		return Repo{}, fmt.Errorf("%w: decode repository: %v", ErrAppHTTP, err)
	}
	if repo.ID != repositoryID ||
		strings.TrimSpace(repo.FullName) == "" ||
		strings.TrimSpace(repo.CloneURL) == "" ||
		strings.TrimSpace(repo.Default) == "" {
		return Repo{}, fmt.Errorf("%w: repository response identity is incomplete", ErrAppHTTP)
	}
	return repo, nil
}

// ListBranchesForRepository enumerates branches for one server-resolved
// repository. It intentionally accepts numeric identity only: owner/name comes
// from GitHub, not from the product request. Pagination is exhaustive up to an
// explicit safety bound; it never returns a silently truncated branch list.
func (c *AppClient) ListBranchesForRepository(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
) ([]Branch, error) {
	repo, err := c.GetRepositoryForInstallation(ctx, installationID, repositoryID)
	if err != nil {
		return nil, err
	}
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("%w: invalid repository full_name", ErrAppHTTP)
	}
	tok, err := c.MintRepositoryToken(ctx, installationID, repositoryID)
	if err != nil {
		return nil, err
	}
	const (
		branchesPerPage = 100
		maxBranchPages  = 100
	)
	branches := make([]Branch, 0, branchesPerPage)
	for page := 1; page <= maxBranchPages; page++ {
		endpoint := fmt.Sprintf(
			"%s/repos/%s/%s/branches?per_page=%d&page=%d",
			c.cfg.APIBase(),
			url.PathEscape(owner),
			url.PathEscape(name),
			branchesPerPage,
			page,
		)
		body, err := c.doInstallationGET(ctx, endpoint, tok)
		if err != nil {
			return nil, err
		}
		var batch []Branch
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, fmt.Errorf("%w: decode branches: %v", ErrAppHTTP, err)
		}
		for _, branch := range batch {
			if strings.TrimSpace(branch.Name) == "" {
				return nil, fmt.Errorf("%w: branch response contains an empty name", ErrAppHTTP)
			}
		}
		branches = append(branches, batch...)
		if len(batch) < branchesPerPage {
			return branches, nil
		}
	}
	return nil, fmt.Errorf("%w: branch response exceeds %d entries", ErrAppHTTP, branchesPerPage*maxBranchPages)
}

// VerifyRepositoryBranch proves that the numeric repository is visible to the
// installation and that one exact branch currently exists. The repository
// metadata returned here is suitable for a canonical link transaction.
func (c *AppClient) VerifyRepositoryBranch(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
	branch string,
) (Repo, error) {
	branch = strings.TrimSpace(strings.TrimPrefix(branch, "refs/heads/"))
	if branch == "" {
		return Repo{}, fmt.Errorf("%w: branch is required", ErrLinkInvalid)
	}
	repo, err := c.GetRepositoryForInstallation(ctx, installationID, repositoryID)
	if err != nil {
		return Repo{}, err
	}
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("%w: invalid repository full_name", ErrAppHTTP)
	}
	tok, err := c.MintRepositoryToken(ctx, installationID, repositoryID)
	if err != nil {
		return Repo{}, err
	}
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/branches/%s",
		c.cfg.APIBase(),
		url.PathEscape(owner),
		url.PathEscape(name),
		url.PathEscape(branch),
	)
	if _, err := c.doInstallationGET(ctx, endpoint, tok); err != nil {
		return Repo{}, err
	}
	return repo, nil
}

func (c *AppClient) doInstallationGET(
	ctx context.Context,
	endpoint string,
	tok InstallationToken,
) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+tok.Token.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, errors.New(scrubSecret(err.Error(), tok.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 4<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &AppHTTPError{
			StatusCode: resp.StatusCode,
			Body:       scrubSecret(truncate(body, 256), tok.Token),
		}
	}
	return body, nil
}

func readAppResponseBody(body io.Reader, limit int64) ([]byte, error) {
	if body == nil || limit <= 0 {
		return nil, fmt.Errorf("%w: invalid response body boundary", ErrAppHTTP)
	}
	payload, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: response body read: %v", ErrAppHTTP, err)
	}
	if int64(len(payload)) > limit {
		return nil, fmt.Errorf("%w: response body exceeds %d bytes", ErrAppHTTP, limit)
	}
	return payload, nil
}

// CombinedStatusLive queries the combined-status API for a commit SHA
// using an installation token. Implements StatusChecker. This replaces
// the StubStatusChecker for operators with a wired GitHub App.
func (c *AppClient) CombinedStatusLive(ctx context.Context, installationID int64, repoFullName, sha string) ([]CheckObservation, error) {
	tok, err := c.MintInstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/repos/%s/commits/%s/check-runs?per_page=100", c.cfg.APIBase(), repoFullName, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+tok.Token.Value())
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readAppResponseBody(resp.Body, 1<<20)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrAppHTTP, resp.StatusCode, scrubSecret(truncate(body, 256), tok.Token))
	}
	var raw struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrAppHTTP, err)
	}
	out := make([]CheckObservation, 0, len(raw.CheckRuns))
	for _, cr := range raw.CheckRuns {
		out = append(out, CheckObservation{Name: cr.Name, Status: mapCheckConclusion(cr.Status, cr.Conclusion)})
	}
	return out, nil
}

func mapCheckConclusion(status, conclusion string) CheckRunStatus {
	if status != "completed" {
		return CheckPending
	}
	switch conclusion {
	case "success", "neutral", "skipped":
		return CheckSuccess
	case "failure", "cancelled", "timed_out", "action_required", "stale":
		return CheckFailure
	default:
		return CheckMissing
	}
}

// parseRSAPrivateKey decodes a PEM-encoded RSA private key. Both PKCS#1
// ("RSA PRIVATE KEY") and PKCS#8 ("PRIVATE KEY") blocks are accepted.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("private key: no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pkcs1: %w", err)
		}
		return key, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("pkcs8: %w", err)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not RSA")
		}
		return rsaKey, nil
	default:
		return nil, fmt.Errorf("private key: unsupported PEM block type %q", block.Type)
	}
}

func signRS256JWT(pk *rsa.PrivateKey, header map[string]string, payload map[string]any) (string, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, pk, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// LoadAppConfigFromEnv reads the panel-locked env vars; private key is
// loaded from the path in MIDNIGHT_GITHUB_PRIVATE_KEY_PATH. The reader
// is injected so tests stay hermetic.
type FileReader func(path string) ([]byte, error)

// LoadAppConfig builds an AppConfig from a getter (typically os.Getenv)
// and a file reader (typically os.ReadFile). Returns ErrAppConfigInvalid
// when required env vars are missing.
func LoadAppConfig(get func(string) string, read FileReader) (AppConfig, error) {
	cfg, err := LoadTokenMintingAppConfig(get, read)
	if err != nil {
		return AppConfig{}, err
	}
	secret := strings.TrimSpace(get("MIDNIGHT_GITHUB_WEBHOOK_SECRET"))
	if secret == "" {
		return AppConfig{}, fmt.Errorf("%w: MIDNIGHT_GITHUB_WEBHOOK_SECRET unset", ErrAppConfigInvalid)
	}
	cfg.WebhookSecret = secret
	cfg.ClientID = strings.TrimSpace(get("MIDNIGHT_GITHUB_CLIENT_ID"))
	cfg.ClientSecret = strings.TrimSpace(get("MIDNIGHT_GITHUB_CLIENT_SECRET"))
	cfg.WebBaseURL = strings.TrimSpace(get("MIDNIGHT_GITHUB_WEB_BASE_URL"))
	cfg.AppSlug = strings.TrimSpace(get("MIDNIGHT_GITHUB_APP_SLUG"))
	cfg.CallbackURL = strings.TrimSpace(get("MIDNIGHT_GITHUB_CALLBACK_URL"))
	if err := cfg.Validate(); err != nil {
		return AppConfig{}, err
	}
	return cfg, nil
}

// LoadTokenMintingAppConfig loads only the credentials needed to mint an App
// installation token. Outbound-only workers must use this loader so the
// inbound webhook secret is not distributed to another process.
func LoadTokenMintingAppConfig(get func(string) string, read FileReader) (AppConfig, error) {
	if get == nil {
		return AppConfig{}, fmt.Errorf("%w: getter required", ErrAppConfigInvalid)
	}
	idStr := strings.TrimSpace(get("MIDNIGHT_GITHUB_APP_ID"))
	if idStr == "" {
		return AppConfig{}, fmt.Errorf("%w: MIDNIGHT_GITHUB_APP_ID unset", ErrAppConfigInvalid)
	}
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id <= 0 {
		return AppConfig{}, fmt.Errorf("%w: MIDNIGHT_GITHUB_APP_ID invalid", ErrAppConfigInvalid)
	}
	keyPath := strings.TrimSpace(get("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH"))
	if keyPath == "" {
		return AppConfig{}, fmt.Errorf("%w: MIDNIGHT_GITHUB_PRIVATE_KEY_PATH unset", ErrAppConfigInvalid)
	}
	if read == nil {
		return AppConfig{}, fmt.Errorf("%w: file reader required", ErrAppConfigInvalid)
	}
	pem, err := read(keyPath)
	if err != nil {
		return AppConfig{}, fmt.Errorf("%w: read private key: %v", ErrAppConfigInvalid, err)
	}
	cfg := AppConfig{
		AppID:         id,
		PrivateKeyPEM: pem,
		APIBaseURL:    strings.TrimSpace(get("MIDNIGHT_GITHUB_API_BASE_URL")),
	}
	if err := cfg.ValidateTokenMinting(); err != nil {
		return AppConfig{}, err
	}
	return cfg, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
