// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

func testGitHubAppPEM(t *testing.T) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
}

func TestGitHubAppTokenMinterScopesInstallationTokenToRepository(t *testing.T) {
	const installationToken = "ghs_1234567890123456789012345678901234567890"
	var authorization string
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/42/access_tokens" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		authorization = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = string(body)
		_, _ = w.Write([]byte(`{"token":"` + installationToken + `","expires_at":"2030-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	minter := &githubAppTokenMinter{
		appID:      "7",
		keyPath:    "/run/secrets/github.pem",
		apiBaseURL: server.URL,
		readFile: func(path string) ([]byte, error) {
			if path != "/run/secrets/github.pem" {
				t.Fatalf("key path = %q", path)
			}
			return testGitHubAppPEM(t), nil
		},
		httpDoer: server.Client(),
	}
	token, err := minter.MintRepositoryToken(context.Background(), 42, 4242)
	if err != nil {
		t.Fatalf("MintRepositoryToken() error = %v", err)
	}
	if token.Token.Value() != installationToken {
		t.Fatal("minted token did not reach the final in-memory boundary")
	}
	if !strings.HasPrefix(authorization, "Bearer ") || strings.Contains(authorization, installationToken) {
		t.Fatalf("App JWT authorization = %q", authorization)
	}
	if requestBody != `{"repository_ids":[4242],"permissions":{"contents":"read"}}` {
		t.Fatalf("repository token scope = %s", requestBody)
	}
}

func TestPrivateGitHubCheckoutMintsImmediatelyAndRedactsGitTransportFailure(t *testing.T) {
	const installationToken = "ghs_1234567890123456789012345678901234567890"
	script := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'args=%s auth=%s\\n' \"$*\" \"$GIT_CONFIG_VALUE_0\" >&2\nexit 128\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	minter := &recordingInstallationTokenMinter{
		token: githubapp.InstallationToken{
			Token:     githubapp.NewSecret(installationToken),
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	executor := &buildJobExecutor{
		cfg: workerConfig{
			CheckoutRoot:    t.TempDir(),
			GitPath:         script,
			GitHubCloneHost: "github.com",
		},
		githubTokenMinter: minter,
		githubSourceLinks: &recordingGitHubSourceAuthority{link: testGitHubSourceAuthority()},
		redactor:          build.NewRedactor(nil),
	}
	descriptor := &buildJobSourceIntent{
		Provider:            "github",
		Repository:          "acme/private",
		WorkspaceID:         "wrk_private",
		InstallationID:      42,
		RepositoryID:        4242,
		LinkResourceVersion: 7,
		Repo:                "https://github.com/acme/private.git",
		Ref:                 "main",
	}
	_, err := executor.checkoutSource(context.Background(), "svc-private", descriptor, build.SourceRef{
		Repo: descriptor.Repo,
		Ref:  descriptor.Ref,
	}, &sourceArtifactCheckoutIdentity{ProjectID: "prj_private"})
	if err == nil {
		t.Fatal("checkoutSource() error = nil")
	}
	if minter.calls != 1 || minter.installationID != 42 || minter.repositoryID != 4242 {
		t.Fatalf("minter calls=%d installation=%d repository=%d", minter.calls, minter.installationID, minter.repositoryID)
	}
	if strings.Contains(err.Error(), installationToken) {
		t.Fatalf("checkout error leaked installation token: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("checkout error did not prove transport redaction: %v", err)
	}
	if strings.Contains(err.Error(), "x-access-token") {
		t.Fatalf("checkout command embedded a token-bearing URL: %v", err)
	}
}

func TestPrivateGitHubCheckoutFailsHonestlyWhenCredentialsAbsent(t *testing.T) {
	executor := &buildJobExecutor{
		cfg: workerConfig{
			CheckoutRoot:    t.TempDir(),
			GitPath:         "git",
			GitHubCloneHost: "github.com",
		},
		githubTokenMinter: &githubAppTokenMinter{},
		githubSourceLinks: &recordingGitHubSourceAuthority{link: testGitHubSourceAuthority()},
	}
	descriptor := &buildJobSourceIntent{
		Provider:            "github",
		Repository:          "acme/private",
		WorkspaceID:         "wrk_private",
		InstallationID:      42,
		RepositoryID:        4242,
		LinkResourceVersion: 7,
		Repo:                "https://github.com/acme/private.git",
		Ref:                 "main",
	}
	_, err := executor.checkoutSource(
		context.Background(),
		"svc-private",
		descriptor,
		build.SourceRef{Repo: descriptor.Repo, Ref: descriptor.Ref},
		&sourceArtifactCheckoutIdentity{ProjectID: "prj_private"},
	)
	if !errors.Is(err, errGitHubAppCredentialsUnavailable) {
		t.Fatalf("checkoutSource() error = %v, want credentials unavailable", err)
	}
	if !strings.Contains(err.Error(), "MIDNIGHT_GITHUB_APP_ID") || !strings.Contains(err.Error(), "MIDNIGHT_GITHUB_PRIVATE_KEY_PATH") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestPrivateGitHubCheckoutRefusesCredentialExfiltrationTargets(t *testing.T) {
	for name, cloneURL := range map[string]string{
		"wrong host":       "https://example.com/acme/private.git",
		"wrong repository": "https://github.com/acme/other.git",
		"inline user":      "https://user@github.com/acme/private.git",
	} {
		t.Run(name, func(t *testing.T) {
			minter := &recordingInstallationTokenMinter{}
			executor := &buildJobExecutor{
				cfg:               workerConfig{CheckoutRoot: t.TempDir(), GitHubCloneHost: "github.com"},
				githubTokenMinter: minter,
				githubSourceLinks: &recordingGitHubSourceAuthority{link: testGitHubSourceAuthority()},
			}
			descriptor := &buildJobSourceIntent{
				Provider:            "github",
				Repository:          "acme/private",
				WorkspaceID:         "wrk_private",
				InstallationID:      42,
				RepositoryID:        4242,
				LinkResourceVersion: 7,
				Repo:                cloneURL,
				Ref:                 "main",
			}
			if _, err := executor.checkoutSource(
				context.Background(),
				"svc-private",
				descriptor,
				build.SourceRef{Repo: cloneURL, Ref: "main"},
				&sourceArtifactCheckoutIdentity{ProjectID: "prj_private"},
			); err == nil {
				t.Fatalf("checkoutSource(%q) error = nil", cloneURL)
			}
			if minter.calls != 0 {
				t.Fatalf("credential minted before target validation")
			}
		})
	}
}

func TestPrivateGitHubCheckoutRevalidatesCurrentSourceAuthorityBeforeTokenMint(t *testing.T) {
	minter := &recordingInstallationTokenMinter{}
	authority := &recordingGitHubSourceAuthority{err: githubapp.ErrSourceLinkNotFound}
	executor := &buildJobExecutor{
		cfg: workerConfig{
			CheckoutRoot:    t.TempDir(),
			GitHubCloneHost: "github.com",
		},
		githubTokenMinter: minter,
		githubSourceLinks: authority,
	}
	descriptor := &buildJobSourceIntent{
		Provider:            "github",
		Repository:          "acme/private",
		WorkspaceID:         "wrk_private",
		InstallationID:      42,
		RepositoryID:        4242,
		LinkResourceVersion: 7,
		Repo:                "https://github.com/acme/private.git",
		Ref:                 "main",
	}
	_, err := executor.checkoutSource(
		context.Background(),
		"svc-private",
		descriptor,
		build.SourceRef{Repo: descriptor.Repo, Ref: descriptor.Ref},
		&sourceArtifactCheckoutIdentity{ProjectID: "prj_private"},
	)
	if !errors.Is(err, errGitHubSourceAuthorityRevoked) {
		t.Fatalf("checkoutSource() error = %v, want revoked authority", err)
	}
	if minter.calls != 0 {
		t.Fatalf("token minter calls = %d, want zero after revocation", minter.calls)
	}
}

func TestGitHubSourceAuthorityUsesLinkedBranchWhenCheckoutRefIsSHA(t *testing.T) {
	authority := &recordingGitHubSourceAuthority{link: testGitHubSourceAuthority()}
	executor := &buildJobExecutor{githubSourceLinks: authority}
	descriptor := &buildJobSourceIntent{
		Provider:            "github",
		Repository:          "acme/private",
		WorkspaceID:         "wrk_private",
		InstallationID:      42,
		RepositoryID:        4242,
		LinkResourceVersion: 7,
		AuthorityBranch:     "main",
		TrustProfile:        api.DeployTrustProfileGitHubTrusted,
		Repo:                "https://github.com/acme/private.git",
		Ref:                 strings.Repeat("a", 40),
	}
	if err := executor.verifyGitHubSourceAuthority(
		context.Background(),
		"prj_private",
		"svc-private",
		descriptor,
	); err != nil {
		t.Fatalf("verifyGitHubSourceAuthority() = %v", err)
	}
}

func TestManualGitHubSourceAuthorityDoesNotRequireAutoDeploy(t *testing.T) {
	link := testGitHubSourceAuthority()
	link.AutoDeploy = false
	executor := &buildJobExecutor{githubSourceLinks: &recordingGitHubSourceAuthority{link: link}}
	descriptor := &buildJobSourceIntent{
		Provider: "github", Repository: "acme/private", WorkspaceID: "wrk_private",
		InstallationID: 42, RepositoryID: 4242, LinkResourceVersion: 7,
		AuthorityBranch: "main", TrustProfile: api.DeployTrustProfileGitHubManual,
		Repo: "https://github.com/acme/private.git", Ref: strings.Repeat("a", 40),
	}
	if err := validateBuildSourceTrustProfile(descriptor); err != nil {
		t.Fatalf("validateBuildSourceTrustProfile() = %v", err)
	}
	if err := executor.verifyGitHubSourceAuthority(context.Background(), "prj_private", "svc-private", descriptor); err != nil {
		t.Fatalf("manual verifyGitHubSourceAuthority() = %v", err)
	}
	descriptor.TrustProfile = api.DeployTrustProfileGitHubTrusted
	if err := executor.verifyGitHubSourceAuthority(context.Background(), "prj_private", "svc-private", descriptor); !errors.Is(err, errGitHubSourceAuthorityRevoked) {
		t.Fatalf("webhook authority with auto-deploy disabled error=%v, want revoked", err)
	}
}

func TestGitHubForkPreviewTrustProfileUsesQuarantineNamespace(t *testing.T) {
	source := &buildJobSourceIntent{
		Provider:     "github",
		TrustProfile: api.DeployTrustProfileGitHubForkPreview,
		Ref:          strings.Repeat("a", 40),
		FetchRef:     "refs/pull/42/head",
	}
	if err := validateBuildSourceTrustProfile(source); err != nil {
		t.Fatalf("validateBuildSourceTrustProfile() = %v", err)
	}
	policy := sourceBuildExecutionPolicy(source, "/var/lib/midnight/buildkit/cache")
	if policy.AllowBuildSecrets || policy.CacheRoot != "" || !policy.QuarantineImage {
		t.Fatalf("fork preview build policy = %#v", policy)
	}
	if got, want := buildImageRepository("svc_web", "prj_acme", true),
		"midnight/quarantine/prj_acme/svc_web"; got != want {
		t.Fatalf("buildImageRepository() = %q, want %q", got, want)
	}
	if got, want := buildImageRepository("svc_web", "prj_acme", false),
		"midnight/prj_acme/svc_web"; got != want {
		t.Fatalf("trusted buildImageRepository() = %q, want %q", got, want)
	}
}

func TestBuildSourceTrustProfileRejectsCallerControlledOrUnknownProfiles(t *testing.T) {
	for name, source := range map[string]*buildJobSourceIntent{
		"generic source":              {Provider: "git", TrustProfile: api.DeployTrustProfileGitHubForkPreview},
		"unknown github":              {Provider: "github", TrustProfile: "skip-security"},
		"generic fetch ref":           {Provider: "git", Ref: strings.Repeat("a", 40), FetchRef: "refs/pull/42/head"},
		"non pull ref":                {Provider: "github", Ref: strings.Repeat("a", 40), FetchRef: "refs/heads/main"},
		"moving pull ref without pin": {Provider: "github", Ref: "main", FetchRef: "refs/pull/42/head"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateBuildSourceTrustProfile(source); err == nil {
				t.Fatal("validateBuildSourceTrustProfile() error = nil")
			}
		})
	}
}

func testGitHubSourceAuthority() githubapp.DurableServiceLink {
	return githubapp.DurableServiceLink{
		ServiceID:       "svc-private",
		ProjectID:       "prj_private",
		WorkspaceID:     "wrk_private",
		AutoDeploy:      true,
		ResourceVersion: 7,
		Link: githubapp.ServiceLink{
			InstallationID: 42,
			RepositoryID:   4242,
			Repo:           "acme/private",
			Branch:         "main",
			CloneURL:       "https://github.com/acme/private.git",
		},
	}
}

type recordingGitHubSourceAuthority struct {
	link  githubapp.DurableServiceLink
	err   error
	calls int
}

func (s *recordingGitHubSourceAuthority) GetServiceLink(
	_ context.Context,
	workspaceID,
	projectID,
	serviceID string,
) (githubapp.DurableServiceLink, error) {
	s.calls++
	if s.err != nil {
		return githubapp.DurableServiceLink{}, s.err
	}
	if workspaceID != s.link.WorkspaceID ||
		projectID != s.link.ProjectID ||
		serviceID != s.link.ServiceID {
		return githubapp.DurableServiceLink{}, githubapp.ErrSourceLinkNotFound
	}
	return s.link, nil
}

type recordingInstallationTokenMinter struct {
	token          githubapp.InstallationToken
	err            error
	calls          int
	installationID int64
	repositoryID   int64
}

func (m *recordingInstallationTokenMinter) MintRepositoryToken(
	_ context.Context,
	installationID,
	repositoryID int64,
) (githubapp.InstallationToken, error) {
	m.calls++
	m.installationID = installationID
	m.repositoryID = repositoryID
	return m.token, m.err
}
