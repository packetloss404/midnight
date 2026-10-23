//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/url"
	"testing"
	"time"

	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

type githubSetupIntegrationVerifier struct {
	exchangeCalls int
	proofCalls    int
	code          string
	installation  githubapp.Installation
	repositories  []githubapp.Repo
}

func (v *githubSetupIntegrationVerifier) ExchangeUserCode(_ context.Context, code string) (githubapp.Secret, error) {
	v.exchangeCalls++
	if code != v.code {
		return githubapp.Secret{}, githubapp.ErrAppHTTP
	}
	return githubapp.NewSecret("ghu_integration_user_proof"), nil
}

func (v *githubSetupIntegrationVerifier) UserCanAccessInstallation(_ context.Context, token githubapp.Secret, installationID int64) error {
	v.proofCalls++
	if token.Value() != "ghu_integration_user_proof" || installationID != v.installation.ID {
		return githubapp.ErrAppHTTP
	}
	return nil
}

func (v *githubSetupIntegrationVerifier) GetInstallation(_ context.Context, installationID int64) (githubapp.Installation, error) {
	if installationID != v.installation.ID {
		return githubapp.Installation{}, githubapp.ErrAppHTTP
	}
	return v.installation, nil
}

func (v *githubSetupIntegrationVerifier) ListReposForInstallation(_ context.Context, installationID int64) ([]githubapp.Repo, error) {
	if installationID != v.installation.ID {
		return nil, githubapp.ErrAppHTTP
	}
	return append([]githubapp.Repo(nil), v.repositories...), nil
}

func TestPostgresGitHubSetupCoordinatorBindsServerResolvedGrantsOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	fixture := newTenantGitHubIntegrationFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE workspace_members
		   SET role='admin',updated_at=$1,resource_version=resource_version+1
		 WHERE workspace_id='wrk_github_a' AND user_id='usr_github_member'`,
		fixture.now,
	); err != nil {
		t.Fatal(err)
	}
	codec, err := githubapp.NewSetupStateCodec(
		make([]byte, 32),
		githubapp.WithSetupClock(func() time.Time { return fixture.now }),
		githubapp.WithSetupRandom(func(dst []byte) error {
			for index := range dst {
				dst[index] = byte(index + 11)
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &githubSetupIntegrationVerifier{
		code: "oauth-code",
		installation: githubapp.Installation{
			ID: 4401, Account: "server-observed-owner", TargetType: "Organization",
			HTMLURL: "https://github.com/organizations/server-observed-owner/settings/installations/4401",
		},
		repositories: []githubapp.Repo{{
			ID: 5401, FullName: "server-observed-owner/control-plane", Private: true,
			CloneURL: "https://github.com/server-observed-owner/control-plane.git",
			Default:  "main",
		}},
	}
	coordinator := NewPostgresGitHubSetupCoordinator(
		fixture.pool, fixture.links, fixture.coordinator.Audit, codec,
		func() time.Time { return fixture.now },
	)
	coordinator.Verifier = verifier
	coordinator.Config = githubSetupIntegrationConfig(t)

	issued, err := coordinator.Initiate(fixture.ctx, GitHubSetupInitiateRequest{
		WorkspaceID: "wrk_github_a", Principal: fixture.member.principal, Session: fixture.member.session,
		RequestID: "req-setup-issued", SourceIP: "192.0.2.100:443", UserAgent: "midnight-setup-integration",
	})
	if err != nil {
		t.Fatalf("initiate: %v", err)
	}
	installURL, err := url.Parse(issued.InstallURL)
	if err != nil {
		t.Fatal(err)
	}
	signedState := installURL.Query().Get("state")
	if signedState == "" || !issued.ExpiresAt.Equal(fixture.now.Add(githubapp.DefaultSetupTTL)) {
		t.Fatalf("issued result = %#v", issued)
	}

	completed, err := coordinator.Complete(fixture.ctx, GitHubSetupCallbackRequest{
		State: signedState, OAuthCode: "oauth-code", InstallationID: 4401,
		RequestID: "req-setup-complete", SourceIP: "192.0.2.101:443", UserAgent: "github-callback",
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.WorkspaceID != "wrk_github_a" || completed.Installation.Account != "server-observed-owner" || completed.RepositoryCount != 1 {
		t.Fatalf("completed result = %#v", completed)
	}
	grant, err := fixture.links.GetRepositoryGrant(fixture.ctx, "wrk_github_a", 4401, 5401)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Repository != "server-observed-owner/control-plane" || grant.CloneURL != "https://github.com/server-observed-owner/control-plane.git" {
		t.Fatalf("server-resolved repository grant = %#v", grant)
	}
	assertTenantGitHubCount(t, fixture, `
		SELECT COUNT(*) FROM github_app_setup_states
		 WHERE consumed_at IS NOT NULL AND installation_id=4401`, 1)
	assertTenantGitHubCount(t, fixture, `
		SELECT COUNT(*) FROM audit_logs
		 WHERE resource_kind='github_app_setup'
		   AND action IN ('github_app_setup_issued','github_app_installation_bound')`, 2)

	exchangeCalls, proofCalls := verifier.exchangeCalls, verifier.proofCalls
	_, err = coordinator.Complete(fixture.ctx, GitHubSetupCallbackRequest{
		State: signedState, OAuthCode: "oauth-code", InstallationID: 4401,
	})
	if !errors.Is(err, ErrGitHubSetupConsumed) {
		t.Fatalf("replayed callback error = %v", err)
	}
	if verifier.exchangeCalls != exchangeCalls || verifier.proofCalls != proofCalls {
		t.Fatal("replayed callback reached GitHub before the durable one-time fence")
	}
}

func githubSetupIntegrationConfig(t *testing.T) githubapp.AppConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return githubapp.AppConfig{
		AppID: 7,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{
			Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
		}),
		ClientID: "Iv1.integration", ClientSecret: "integration-secret",
		AppSlug:     "midnight-integration",
		CallbackURL: "https://midnight.example/api/v1alpha/github/setup/callback",
	}
}
