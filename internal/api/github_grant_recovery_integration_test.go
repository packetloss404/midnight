//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

type githubGrantRecoveryIntegrationVerifier struct {
	mu                sync.Mutex
	installation      githubapp.Installation
	repositories      map[int64]githubapp.Repo
	installationCalls int
	repositoryCalls   []int64
}

func (v *githubGrantRecoveryIntegrationVerifier) GetInstallation(
	_ context.Context,
	installationID int64,
) (githubapp.Installation, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.installationCalls++
	if v.installation.ID != installationID {
		return githubapp.Installation{}, githubapp.ErrAppHTTP
	}
	return v.installation, nil
}

func (v *githubGrantRecoveryIntegrationVerifier) GetRepositoryForInstallation(
	_ context.Context,
	installationID int64,
	repositoryID int64,
) (githubapp.Repo, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.repositoryCalls = append(v.repositoryCalls, repositoryID)
	if v.installation.ID != installationID {
		return githubapp.Repo{}, githubapp.ErrAppHTTP
	}
	repository, ok := v.repositories[repositoryID]
	if !ok {
		return githubapp.Repo{}, githubapp.ErrAppHTTP
	}
	return repository, nil
}

func (v *githubGrantRecoveryIntegrationVerifier) counts() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.installationCalls, len(v.repositoryCalls)
}

func TestPostgresGitHubGrantRecoveryCoordinatorReplacementReplayAndRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	fixture := newTenantGitHubIntegrationFixture(t)
	verifier := &githubGrantRecoveryIntegrationVerifier{
		installation: githubapp.Installation{
			ID:         4301,
			Account:    "server-observed-acme",
			TargetType: "Organization",
			HTMLURL:    "https://github.com/organizations/server-observed-acme/settings/installations/4301",
		},
		repositories: map[int64]githubapp.Repo{
			5301: {
				ID:       5301,
				FullName: "server-observed-acme/api",
				Private:  true,
				CloneURL: "https://github.com/server-observed-acme/api.git",
				HTMLURL:  "https://github.com/server-observed-acme/api",
				Default:  "trunk",
			},
			5302: {
				ID:       5302,
				FullName: "server-observed-acme/worker",
				Private:  false,
				CloneURL: "https://github.com/server-observed-acme/worker.git",
				HTMLURL:  "https://github.com/server-observed-acme/worker",
				Default:  "main",
			},
		},
	}
	coordinator := NewPostgresGitHubGrantRecoveryCoordinator(
		fixture.pool,
		fixture.links,
		fixture.coordinator.Idempotency,
		fixture.coordinator.Audit,
		func() time.Time { return fixture.now },
	)
	coordinator.Verifier = verifier

	initial := githubGrantRecoveryIntegrationRequest(
		"wrk_github_a",
		4301,
		[]int64{5301, 5302},
		idempotency.Key("github-grant-initial"),
		http.MethodPut,
		[]byte(`{"workspace_id":"wrk_github_a","repository_ids":[5301,5302]}`),
	)
	first, err := coordinator.Put(fixture.ctx, initial)
	if err != nil {
		t.Fatalf("put initial recovery grant: %v", err)
	}
	if first.Replayed || first.Response.StatusCode != http.StatusOK {
		t.Fatalf("initial result = %#v, want committed 200", first)
	}
	var initialView GitHubGrantRecoveryView
	if err := json.Unmarshal(first.Response.Body, &initialView); err != nil {
		t.Fatalf("decode initial response: %v", err)
	}
	if initialView.WorkspaceID != "wrk_github_a" ||
		initialView.Installation.ID != 4301 ||
		initialView.Installation.Account != "server-observed-acme" ||
		initialView.Installation.TargetType != "Organization" ||
		len(initialView.Repositories) != 2 {
		t.Fatalf("initial server-observed view = %#v", initialView)
	}
	assertGitHubGrantRecoveryRepository(
		t,
		fixture,
		4301,
		5301,
		"server-observed-acme/api",
		"https://github.com/server-observed-acme/api.git",
		"trunk",
		true,
	)
	assertGitHubGrantRecoveryRepository(
		t,
		fixture,
		4301,
		5302,
		"server-observed-acme/worker",
		"https://github.com/server-observed-acme/worker.git",
		"main",
		false,
	)

	beforeInstallationCalls, beforeRepositoryCalls := verifier.counts()
	replay, err := coordinator.Put(fixture.ctx, initial)
	if err != nil {
		t.Fatalf("replay initial recovery grant: %v", err)
	}
	if !replay.Replayed ||
		replay.Response.StatusCode != first.Response.StatusCode ||
		string(replay.Response.Body) != string(first.Response.Body) {
		t.Fatalf("replay = %#v, want verbatim terminal response", replay)
	}
	afterInstallationCalls, afterRepositoryCalls := verifier.counts()
	if afterInstallationCalls != beforeInstallationCalls ||
		afterRepositoryCalls != beforeRepositoryCalls {
		t.Fatalf(
			"replay made GitHub calls: installation %d->%d repositories %d->%d",
			beforeInstallationCalls,
			afterInstallationCalls,
			beforeRepositoryCalls,
			afterRepositoryCalls,
		)
	}

	differentBody := initial
	differentBody.RepositoryIDs = []int64{5302}
	differentBody.Fingerprint.BodySHA256 = idempotency.BodyHashSHA256(
		[]byte(`{"workspace_id":"wrk_github_a","repository_ids":[5302]}`),
	)
	_, err = coordinator.Put(fixture.ctx, differentBody)
	if !errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		t.Fatalf("same key with different body = %v, want idempotency body conflict", err)
	}
	finalInstallationCalls, finalRepositoryCalls := verifier.counts()
	if finalInstallationCalls != afterInstallationCalls ||
		finalRepositoryCalls != afterRepositoryCalls {
		t.Fatalf("idempotency conflict made a GitHub call")
	}

	foreign := githubGrantRecoveryIntegrationRequest(
		"wrk_github_b",
		4301,
		[]int64{5301},
		idempotency.Key("github-grant-foreign"),
		http.MethodPut,
		[]byte(`{"workspace_id":"wrk_github_b","repository_ids":[5301]}`),
	)
	if _, err := coordinator.Put(fixture.ctx, foreign); !errors.Is(err, githubapp.ErrSourceLinkConflict) {
		t.Fatalf("cross-workspace assignment = %v, want source-link conflict", err)
	}
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM github_workspace_installation_grants
		  WHERE workspace_id='wrk_github_b' AND installation_id=4301`,
		0,
	)

	if err := fixture.pool.WithTx(fixture.ctx, func(tx *pg.Tx) error {
		_, putErr := fixture.links.PutServiceLinkInTx(
			fixture.ctx,
			tx,
			githubapp.DurableServiceLink{
				ServiceID:   "svc_github_main",
				ProjectID:   "prj_github_a",
				WorkspaceID: "wrk_github_a",
				Link: githubapp.ServiceLink{
					InstallationID: 4301,
					RepositoryID:   5301,
					Repo:           "server-observed-acme/api",
					Branch:         "trunk",
				},
				AutoDeploy:     true,
				LinkedByUserID: "usr_github_member",
			},
		)
		return putErr
	}); err != nil {
		t.Fatalf("seed dependent source link: %v", err)
	}

	replacement := githubGrantRecoveryIntegrationRequest(
		"wrk_github_a",
		4301,
		[]int64{5302},
		idempotency.Key("github-grant-replacement"),
		http.MethodPut,
		[]byte(`{"workspace_id":"wrk_github_a","repository_ids":[5302]}`),
	)
	replaced, err := coordinator.Put(fixture.ctx, replacement)
	if err != nil {
		t.Fatalf("replace repository grant set: %v", err)
	}
	var replacementView GitHubGrantRecoveryView
	if err := json.Unmarshal(replaced.Response.Body, &replacementView); err != nil {
		t.Fatalf("decode replacement response: %v", err)
	}
	if len(replacementView.Repositories) != 1 ||
		replacementView.Repositories[0].RepositoryID != 5302 ||
		len(replacementView.UnlinkedServices) != 1 ||
		replacementView.UnlinkedServices[0] != "svc_github_main" {
		t.Fatalf("replacement response = %#v", replacementView)
	}
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM github_workspace_repository_grants
		  WHERE workspace_id='wrk_github_a' AND installation_id=4301 AND repository_id=5301`,
		0,
	)
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM service_github_links
		  WHERE service_id='svc_github_main'`,
		0,
	)
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM audit_logs
		  WHERE resource_kind='github_installation_grant'
		    AND resource_id='4301'
		    AND action='github.installation.workspace_grant.replace'`,
		2,
	)

	if err := fixture.pool.WithTx(fixture.ctx, func(tx *pg.Tx) error {
		_, putErr := fixture.links.PutServiceLinkInTx(
			fixture.ctx,
			tx,
			githubapp.DurableServiceLink{
				ServiceID:   "svc_github_main",
				ProjectID:   "prj_github_a",
				WorkspaceID: "wrk_github_a",
				Link: githubapp.ServiceLink{
					InstallationID: 4301,
					RepositoryID:   5302,
					Repo:           "server-observed-acme/worker",
					Branch:         "main",
				},
				AutoDeploy:     true,
				LinkedByUserID: "usr_github_member",
			},
		)
		return putErr
	}); err != nil {
		t.Fatalf("seed revocation source link: %v", err)
	}
	revoke := githubGrantRecoveryIntegrationRequest(
		"wrk_github_a",
		4301,
		nil,
		idempotency.Key("github-grant-revoke"),
		http.MethodDelete,
		[]byte(`{"workspace_id":"wrk_github_a"}`),
	)
	revoked, err := coordinator.Delete(fixture.ctx, revoke)
	if err != nil {
		t.Fatalf("revoke installation grant: %v", err)
	}
	var revokeView GitHubGrantRecoveryView
	if err := json.Unmarshal(revoked.Response.Body, &revokeView); err != nil {
		t.Fatalf("decode revoke response: %v", err)
	}
	if len(revokeView.Repositories) != 0 ||
		len(revokeView.UnlinkedServices) != 1 ||
		revokeView.UnlinkedServices[0] != "svc_github_main" {
		t.Fatalf("revoke response = %#v", revokeView)
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM github_workspace_installation_grants
		  WHERE workspace_id='wrk_github_a' AND installation_id=4301`,
		`SELECT COUNT(*) FROM github_workspace_repository_grants
		  WHERE workspace_id='wrk_github_a' AND installation_id=4301`,
		`SELECT COUNT(*) FROM service_github_links
		  WHERE service_id='svc_github_main'`,
	} {
		assertTenantGitHubCount(t, fixture, query, 0)
	}
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM audit_logs
		  WHERE resource_kind='github_installation_grant'
		    AND resource_id='4301'
		    AND action='github.installation.workspace_grant.revoke'`,
		1,
	)
	deleteReplay, err := coordinator.Delete(fixture.ctx, revoke)
	if err != nil || !deleteReplay.Replayed {
		t.Fatalf("delete replay = %#v, %v", deleteReplay, err)
	}
	assertTenantGitHubCount(
		t,
		fixture,
		`SELECT COUNT(*) FROM audit_logs
		  WHERE resource_kind='github_installation_grant'
		    AND resource_id='4301'
		    AND action='github.installation.workspace_grant.revoke'`,
		1,
	)
}

func githubGrantRecoveryIntegrationRequest(
	workspaceID string,
	installationID int64,
	repositoryIDs []int64,
	key idempotency.Key,
	method string,
	body []byte,
) GitHubGrantRecoveryRequest {
	return GitHubGrantRecoveryRequest{
		WorkspaceID:    workspaceID,
		InstallationID: installationID,
		RepositoryIDs:  append([]int64(nil), repositoryIDs...),
		Key:            key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     method,
			Path:       githubGrantRecoveryIdempotencyPath(installationID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: "req-" + string(key),
		SourceIP:  "192.0.2.90:443",
		UserAgent: "midnight-grant-recovery-integration",
	}
}

func assertGitHubGrantRecoveryRepository(
	t *testing.T,
	fixture *tenantGitHubIntegrationFixture,
	installationID int64,
	repositoryID int64,
	repository string,
	cloneURL string,
	defaultBranch string,
	private bool,
) {
	t.Helper()
	grant, err := fixture.links.GetRepositoryGrant(
		fixture.ctx,
		"wrk_github_a",
		installationID,
		repositoryID,
	)
	if err != nil {
		t.Fatalf("get repository grant %d: %v", repositoryID, err)
	}
	if grant.Repository != repository ||
		grant.CloneURL != cloneURL ||
		grant.DefaultBranch != defaultBranch ||
		grant.Private != private ||
		!grant.VerifiedAt.Equal(fixture.now) {
		t.Fatalf("repository grant %d = %#v", repositoryID, grant)
	}
}
