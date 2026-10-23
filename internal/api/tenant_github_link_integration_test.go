//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type tenantGitHubIntegrationHuman struct {
	principal identity.Principal
	session   identity.Session
}

type tenantGitHubVerifierKey struct {
	installationID int64
	repositoryID   int64
}

// tenantGitHubIntegrationVerifier is a deliberately strict fake for the live
// GitHub boundary. Hooks run after the local preflight has committed and before
// the coordinator begins its final SERIALIZABLE transaction, which makes
// grant-version and revocation races deterministic.
type tenantGitHubIntegrationVerifier struct {
	mu           sync.Mutex
	repositories map[tenantGitHubVerifierKey]githubapp.Repo
	branches     map[tenantGitHubVerifierKey][]githubapp.Branch
	verifyCalls  []tenantGitHubVerifierKey
	getCalls     []tenantGitHubVerifierKey
	verifyHook   func(context.Context) error
}

func (v *tenantGitHubIntegrationVerifier) GetRepositoryForInstallation(
	_ context.Context,
	installationID int64,
	repositoryID int64,
) (githubapp.Repo, error) {
	key := tenantGitHubVerifierKey{installationID: installationID, repositoryID: repositoryID}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.getCalls = append(v.getCalls, key)
	repository, ok := v.repositories[key]
	if !ok {
		return githubapp.Repo{}, githubapp.ErrAppHTTP
	}
	return repository, nil
}

func (v *tenantGitHubIntegrationVerifier) ListBranchesForRepository(
	_ context.Context,
	installationID int64,
	repositoryID int64,
) ([]githubapp.Branch, error) {
	key := tenantGitHubVerifierKey{installationID: installationID, repositoryID: repositoryID}
	v.mu.Lock()
	defer v.mu.Unlock()
	branches, ok := v.branches[key]
	if !ok {
		return nil, githubapp.ErrAppHTTP
	}
	return append([]githubapp.Branch(nil), branches...), nil
}

func (v *tenantGitHubIntegrationVerifier) VerifyRepositoryBranch(
	ctx context.Context,
	installationID int64,
	repositoryID int64,
	_ string,
) (githubapp.Repo, error) {
	key := tenantGitHubVerifierKey{installationID: installationID, repositoryID: repositoryID}
	v.mu.Lock()
	v.verifyCalls = append(v.verifyCalls, key)
	hook := v.verifyHook
	v.verifyHook = nil
	repository, ok := v.repositories[key]
	v.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return githubapp.Repo{}, err
		}
	}
	if !ok {
		return githubapp.Repo{}, githubapp.ErrAppHTTP
	}
	return repository, nil
}

func (v *tenantGitHubIntegrationVerifier) setVerifyHook(hook func(context.Context) error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verifyHook = hook
}

func (v *tenantGitHubIntegrationVerifier) verifyCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.verifyCalls)
}

type tenantGitHubIntegrationFixture struct {
	ctx         context.Context
	pool        *pg.Pool
	now         time.Time
	links       *githubapp.SourceLinkStore
	coordinator *PostgresTenantGitHubSourceLinkCoordinator
	verifier    *tenantGitHubIntegrationVerifier
	member      tenantGitHubIntegrationHuman
	viewer      tenantGitHubIntegrationHuman
	outsider    tenantGitHubIntegrationHuman
}

func TestPostgresTenantGitHubSourceLinkCoordinatorAuthorityAndRevocation(
	t *testing.T,
) {
	if testing.Short() {
		t.Skip("integration test")
	}
	fixture := newTenantGitHubIntegrationFixture(t)

	t.Run("tenant discovery is exact and live branch verification is scoped", func(t *testing.T) {
		items, err := fixture.coordinator.ListProjectRepositories(
			fixture.ctx,
			TenantGitHubProjectRequest{
				TenantGitHubIdentityRequest: TenantGitHubIdentityRequest{
					Principal: fixture.member.principal,
					Session:   fixture.member.session,
				},
				ProjectID: "prj_github_a",
			},
		)
		if err != nil {
			t.Fatalf("list repositories: %v", err)
		}
		if len(items) != 5 {
			t.Fatalf("repository grants = %#v, want five workspace-a grants", items)
		}
		for _, item := range items {
			if item.Repository == "foreign/api" || item.InstallationID == 4201 {
				t.Fatalf("foreign workspace grant leaked through discovery: %#v", item)
			}
		}

		branches, err := fixture.coordinator.ListProjectRepositoryBranches(
			fixture.ctx,
			TenantGitHubProjectRepositoryRequest{
				TenantGitHubProjectRequest: TenantGitHubProjectRequest{
					TenantGitHubIdentityRequest: TenantGitHubIdentityRequest{
						Principal: fixture.member.principal,
						Session:   fixture.member.session,
					},
					ProjectID: "prj_github_a",
				},
				RepositoryID: 5101,
			},
		)
		if err != nil {
			t.Fatalf("list branches: %v", err)
		}
		if len(branches) != 2 || branches[0].Name != "main" || branches[1].Name != "release" {
			t.Fatalf("branches = %#v, want live repository-scoped result", branches)
		}
	})

	t.Run("put commits link idempotency and audit atomically", func(t *testing.T) {
		request := fixture.putRequest(
			fixture.member,
			"svc_github_main",
			4101,
			5101,
			"main",
			githubPutKey("main"),
		)
		beforeVerify := fixture.verifier.verifyCount()
		first, err := fixture.coordinator.PutServiceLink(fixture.ctx, request)
		if err != nil {
			t.Fatalf("put service link: %v", err)
		}
		if first.Replayed || first.Response.StatusCode != http.StatusOK {
			t.Fatalf("first put = %#v, want committed 200", first)
		}
		var view TenantGitHubLinkView
		if err := json.Unmarshal(first.Response.Body, &view); err != nil {
			t.Fatalf("decode put response: %v", err)
		}
		if view.InstallationID != 4101 ||
			view.RepositoryID != 5101 ||
			view.Repository != "acme/control-plane" ||
			view.Branch != "main" ||
			view.BindingState != "bound" {
			t.Fatalf("put response = %#v, want exact verified binding", view)
		}

		replay, err := fixture.coordinator.PutServiceLink(fixture.ctx, request)
		if err != nil {
			t.Fatalf("replay service link: %v", err)
		}
		if !replay.Replayed ||
			replay.Response.StatusCode != first.Response.StatusCode ||
			string(replay.Response.Body) != string(first.Response.Body) {
			t.Fatalf("replay = %#v, want verbatim terminal response", replay)
		}
		if got := fixture.verifier.verifyCount(); got != beforeVerify+1 {
			t.Fatalf("live verify calls = %d, want one (replay must stay local)", got-beforeVerify)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM service_github_links WHERE service_id='svc_github_main'`,
			1,
		)
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM audit_logs
			  WHERE resource_id='svc_github_main' AND action='service.github.link'`,
			1,
		)
	})

	t.Run("cross-workspace and role denials happen before GitHub", func(t *testing.T) {
		beforeVerify := fixture.verifier.verifyCount()
		for _, test := range []struct {
			name  string
			human tenantGitHubIntegrationHuman
			want  error
			key   string
		}{
			{
				name:  "foreign workspace",
				human: fixture.outsider,
				want:  identity.ErrAccessNotFound,
				key:   "foreign-workspace",
			},
			{
				name:  "viewer role",
				human: fixture.viewer,
				want:  identity.ErrAuthorizationDenied,
				key:   "viewer-role",
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := fixture.coordinator.PutServiceLink(
					fixture.ctx,
					fixture.putRequest(
						test.human,
						"svc_github_denied",
						4101,
						5101,
						"main",
						githubPutKey("denied-"+test.key),
					),
				)
				if !errors.Is(err, test.want) {
					t.Fatalf("put denial = %v, want %v", err, test.want)
				}
			})
		}
		if got := fixture.verifier.verifyCount(); got != beforeVerify {
			t.Fatalf("denied requests made %d live GitHub calls", got-beforeVerify)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM service_github_links WHERE service_id='svc_github_denied'`,
			0,
		)
	})

	t.Run("grant version change between preflight and commit fails closed", func(t *testing.T) {
		fixture.verifier.setVerifyHook(func(ctx context.Context) error {
			return fixture.pool.WithTx(ctx, func(tx *pg.Tx) error {
				_, err := fixture.links.PutRepositoryGrantInTx(
					ctx,
					tx,
					fixture.repositoryGrant(4102, 5102, "acme/versioned"),
				)
				return err
			})
		})
		_, err := fixture.coordinator.PutServiceLink(
			fixture.ctx,
			fixture.putRequest(
				fixture.member,
				"svc_github_version_race",
				4102,
				5102,
				"main",
				githubPutKey("version-race"),
			),
		)
		if !errors.Is(err, ErrTenantGitHubSourceConflict) {
			t.Fatalf("version race = %v, want source conflict", err)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM service_github_links
			  WHERE service_id='svc_github_version_race'`,
			0,
		)
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM audit_logs
			  WHERE resource_id='svc_github_version_race'`,
			0,
		)
	})

	t.Run("webhook installation fence precedes human service row locks", func(t *testing.T) {
		raceCtx, cancelRace := context.WithTimeout(fixture.ctx, 10*time.Second)
		defer cancelRace()
		setup := fixture.putRequest(
			fixture.member,
			"svc_github_lock_order",
			4105,
			5105,
			"main",
			githubPutKey("lock-order-setup"),
		)
		if _, err := fixture.coordinator.PutServiceLink(fixture.ctx, setup); err != nil {
			t.Fatalf("put lock-order fixture: %v", err)
		}
		canonical, err := fixture.links.GetServiceLink(
			fixture.ctx,
			"wrk_github_a",
			"prj_github_a",
			"svc_github_lock_order",
		)
		if err != nil {
			t.Fatalf("get lock-order fixture: %v", err)
		}
		grant, err := fixture.links.GetRepositoryGrant(
			fixture.ctx,
			"wrk_github_a",
			4105,
			5105,
		)
		if err != nil {
			t.Fatalf("get lock-order grant: %v", err)
		}

		webhookHasInstallation := make(chan struct{})
		allowWebhookServiceLock := make(chan struct{})
		webhookDone := make(chan error, 1)
		go func() {
			webhookDone <- fixture.pool.WithTxOpts(
				raceCtx,
				pgx.TxOptions{IsoLevel: pgx.Serializable},
				func(tx *pg.Tx) error {
					if err := revalidateGitHubBuildSourceAuthorityInTx(
						raceCtx,
						tx,
						GitHubBuildSourceAuthority{
							WorkspaceID:                      canonical.WorkspaceID,
							ProjectID:                        canonical.ProjectID,
							ServiceID:                        canonical.ServiceID,
							InstallationID:                   canonical.Link.InstallationID,
							RepositoryID:                     canonical.Link.RepositoryID,
							Repository:                       canonical.Link.Repo,
							CloneURL:                         canonical.Link.CloneURL,
							Branch:                           canonical.Link.Branch,
							InstallationGrantResourceVersion: canonical.InstallationGrantResourceVersion,
							RepositoryGrantResourceVersion:   canonical.RepositoryGrantResourceVersion,
							LinkResourceVersion:              canonical.ResourceVersion,
						},
					); err != nil {
						return err
					}
					close(webhookHasInstallation)
					select {
					case <-allowWebhookServiceLock:
					case <-raceCtx.Done():
						return raceCtx.Err()
					}
					if _, err := tx.Exec(
						raceCtx,
						`SET LOCAL lock_timeout='1s'`,
					); err != nil {
						return err
					}
					var serviceID string
					return tx.QueryRow(
						raceCtx,
						`SELECT id FROM services WHERE id=$1 FOR KEY SHARE`,
						canonical.ServiceID,
					).Scan(&serviceID)
				},
			)
		}()

		select {
		case <-webhookHasInstallation:
		case err := <-webhookDone:
			t.Fatalf("webhook transaction ended before race: %v", err)
		case <-raceCtx.Done():
			t.Fatal(raceCtx.Err())
		}

		humanPID := make(chan int32, 1)
		humanDone := make(chan error, 1)
		go func() {
			humanDone <- fixture.pool.WithTxOpts(
				raceCtx,
				pgx.TxOptions{IsoLevel: pgx.Serializable},
				func(tx *pg.Tx) error {
					var pid int32
					if err := tx.QueryRow(
						raceCtx,
						`SELECT pg_backend_pid()`,
					).Scan(&pid); err != nil {
						return err
					}
					humanPID <- pid
					_, err := fixture.coordinator.putServiceLinkInTx(
						raceCtx,
						tx,
						fixture.putRequest(
							fixture.member,
							"svc_github_lock_order",
							4105,
							5105,
							"release",
							githubPutKey("lock-order-race"),
						),
						grant,
						integrationGitHubRepo(5105, "acme/lock-order"),
					)
					return err
				},
			)
		}()

		var pid int32
		select {
		case pid = <-humanPID:
		case err := <-humanDone:
			t.Fatalf("human transaction ended before blocking: %v", err)
		case <-raceCtx.Done():
			t.Fatal(raceCtx.Err())
		}
		waitForTenantGitHubAdvisoryWait(t, fixture, pid)
		close(allowWebhookServiceLock)

		select {
		case err := <-webhookDone:
			if err != nil {
				t.Fatalf(
					"webhook could not lock the service while human waited on installation: %v",
					err,
				)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("webhook transaction did not complete; possible lock-order deadlock")
		}
		select {
		case err := <-humanDone:
			if err != nil {
				t.Fatalf("human link mutation after webhook commit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("human transaction did not resume after installation fence released")
		}
	})

	t.Run("delete replay revalidates the exact current grant", func(t *testing.T) {
		putRequest := fixture.putRequest(
			fixture.member,
			"svc_github_delete",
			4103,
			5103,
			"main",
			githubPutKey("delete-setup"),
		)
		if _, err := fixture.coordinator.PutServiceLink(fixture.ctx, putRequest); err != nil {
			t.Fatalf("put delete fixture: %v", err)
		}
		deleteRequest := fixture.deleteRequest(
			fixture.member,
			"svc_github_delete",
			idempotency.Key("github-delete-replay"),
		)
		first, err := fixture.coordinator.DeleteServiceLink(fixture.ctx, deleteRequest)
		if err != nil {
			t.Fatalf("delete service link: %v", err)
		}
		if first.Replayed || first.Response.StatusCode != http.StatusNoContent {
			t.Fatalf("first delete = %#v, want committed 204", first)
		}
		replay, err := fixture.coordinator.DeleteServiceLink(fixture.ctx, deleteRequest)
		if err != nil || !replay.Replayed {
			t.Fatalf("delete replay = %#v, %v", replay, err)
		}
		if err := fixture.pool.WithTx(fixture.ctx, func(tx *pg.Tx) error {
			_, err := fixture.links.RevokeRepositoryGrantInTx(
				fixture.ctx,
				tx,
				"wrk_github_a",
				4103,
				5103,
			)
			return err
		}); err != nil {
			t.Fatalf("revoke replay grant: %v", err)
		}
		if _, err := fixture.coordinator.DeleteServiceLink(
			fixture.ctx,
			deleteRequest,
		); !errors.Is(err, githubapp.ErrRepositoryGrantNotFound) {
			t.Fatalf("delete replay after revocation = %v, want repository grant not found", err)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM audit_logs
			  WHERE resource_id='svc_github_delete' AND action='service.github.unlink'`,
			1,
		)
	})

	t.Run("installation revocation wins link race and blocks build intake", func(t *testing.T) {
		existingRequest := fixture.putRequest(
			fixture.member,
			"svc_github_revoked",
			4104,
			5104,
			"main",
			githubPutKey("revoke-existing"),
		)
		if _, err := fixture.coordinator.PutServiceLink(
			fixture.ctx,
			existingRequest,
		); err != nil {
			t.Fatalf("put revocation fixture: %v", err)
		}
		canonical, err := fixture.links.GetServiceLink(
			fixture.ctx,
			"wrk_github_a",
			"prj_github_a",
			"svc_github_revoked",
		)
		if err != nil {
			t.Fatalf("get revocation fixture: %v", err)
		}

		fixture.verifier.setVerifyHook(func(ctx context.Context) error {
			return fixture.pool.WithTx(ctx, func(tx *pg.Tx) error {
				serviceIDs, err := fixture.links.RevokeInstallationGrantInTx(
					ctx,
					tx,
					"wrk_github_a",
					4104,
				)
				if err != nil {
					return err
				}
				if len(serviceIDs) != 1 || serviceIDs[0] != "svc_github_revoked" {
					t.Fatalf("revoked service ids = %#v, want existing link", serviceIDs)
				}
				return nil
			})
		})
		_, err = fixture.coordinator.PutServiceLink(
			fixture.ctx,
			fixture.putRequest(
				fixture.member,
				"svc_github_revocation_race",
				4104,
				5104,
				"main",
				githubPutKey("revocation-race"),
			),
		)
		if !errors.Is(err, githubapp.ErrInstallationGrantNotFound) &&
			!errors.Is(err, githubapp.ErrRepositoryGrantNotFound) {
			t.Fatalf("link-vs-revocation race = %v, want revoked grant", err)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM service_github_links
			  WHERE service_id IN ('svc_github_revoked','svc_github_revocation_race')`,
			0,
		)

		beforeVerify := fixture.verifier.verifyCount()
		if _, err := fixture.coordinator.PutServiceLink(
			fixture.ctx,
			fixture.putRequest(
				fixture.member,
				"svc_github_revocation_race",
				4104,
				5104,
				"main",
				githubPutKey("revoked-after-race"),
			),
		); !errors.Is(err, githubapp.ErrRepositoryGrantNotFound) &&
			!errors.Is(err, githubapp.ErrInstallationGrantNotFound) {
			t.Fatalf("put after revocation = %v, want revoked grant", err)
		}
		if got := fixture.verifier.verifyCount(); got != beforeVerify {
			t.Fatalf("revoked grant made %d post-revocation live GitHub calls", got-beforeVerify)
		}

		operationID := defaultOperationID()
		deploymentID := mintID(orailtypes.IDPrefixDeployment)
		outbox := NewPostgresStagedWorkOutbox(fixture.pool)
		_, err = outbox.CommitGitHubBuildIntake(
			fixture.ctx,
			GitHubBuildIntakeCommit{
				Operation: orailtypes.Operation{
					ID:    operationID,
					Kind:  "build",
					State: orailtypes.OperationQueued,
				},
				Deployment: DeploymentRecord{
					ID:            deploymentID,
					ProjectID:     "prj_github_a",
					ServiceID:     "svc_github_revoked",
					EnvironmentID: "env_github_a",
					Action:        deploy.ActionDeploySource,
					SourceType:    "git",
					Repo:          canonical.Link.CloneURL,
					Ref:           strings.Repeat("a", 40),
					SHA:           strings.Repeat("a", 40),
					OperationID:   string(operationID),
					TriggerKey:    "github:revoked:deadbeef",
				},
				SourceAuthority: GitHubBuildSourceAuthority{
					WorkspaceID:                      canonical.WorkspaceID,
					ProjectID:                        canonical.ProjectID,
					ServiceID:                        canonical.ServiceID,
					InstallationID:                   canonical.Link.InstallationID,
					RepositoryID:                     canonical.Link.RepositoryID,
					Repository:                       canonical.Link.Repo,
					CloneURL:                         canonical.Link.CloneURL,
					Branch:                           canonical.Link.Branch,
					InstallationGrantResourceVersion: canonical.InstallationGrantResourceVersion,
					RepositoryGrantResourceVersion:   canonical.RepositoryGrantResourceVersion,
					LinkResourceVersion:              canonical.ResourceVersion,
				},
				Enqueue: queue.EnqueueRequest{
					Queue:             queue.Build,
					PartitionKey:      "prj_github_a",
					ParentOperationID: string(operationID),
					AvailableAt:       fixture.now,
					Payload:           []byte(`{"source":"github"}`),
				},
				Now: fixture.now,
			},
		)
		if !errors.Is(err, ErrGitHubBuildSourceAuthorityRevoked) {
			t.Fatalf("build intake after revocation = %v, want revoked source authority", err)
		}
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM operations WHERE id=$1`,
			0,
			string(operationID),
		)
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM deployments WHERE id=$1`,
			0,
			deploymentID,
		)
		assertTenantGitHubCount(
			t,
			fixture,
			`SELECT COUNT(*) FROM staged_work_outbox
			  WHERE dedupe_key='github-build-intake:github:revoked:deadbeef'`,
			0,
		)
	})
}

func newTenantGitHubIntegrationFixture(t *testing.T) *tenantGitHubIntegrationFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	t.Cleanup(pool.Close)
	if _, err := (&pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	password := "github source link correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id,email,display_name,password_hash,session_generation,
			created_at,updated_at
		) VALUES
			('usr_github_member','member-github@example.com','Member',$1,1,$2,$2),
			('usr_github_viewer','viewer-github@example.com','Viewer',$1,1,$2,$2),
			('usr_github_outsider','outsider-github@example.com','Outsider',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (
			id,slug,display_name,created_at,updated_at
		) VALUES
			('wrk_github_a','github-a','GitHub A',$1,$1),
			('wrk_github_b','github-b','GitHub B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id,workspace_id,slug,display_name,created_at,updated_at
		) VALUES
			('prj_github_a','wrk_github_a','app','App',$1,$1),
			('prj_github_b','wrk_github_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id,project_id,slug,display_name,created_at,updated_at
		) VALUES
			('svc_github_main','prj_github_a','main','Main',$1,$1),
			('svc_github_denied','prj_github_a','denied','Denied',$1,$1),
			('svc_github_version_race','prj_github_a','version-race','Version Race',$1,$1),
			('svc_github_lock_order','prj_github_a','lock-order','Lock Order',$1,$1),
			('svc_github_delete','prj_github_a','delete','Delete',$1,$1),
			('svc_github_revoked','prj_github_a','revoked','Revoked',$1,$1),
			('svc_github_revocation_race','prj_github_a','revocation-race','Revocation Race',$1,$1),
			('svc_github_foreign','prj_github_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id,project_id,slug,name,tier,access_restricted,created_at,updated_at
		) VALUES
			('env_github_a','prj_github_a','production','Production','production',FALSE,$1,$1),
			('env_github_b','prj_github_b','production','Production','production',FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		) VALUES
			('wrk_github_a','usr_github_member','member',$1,$1,$1,$1),
			('wrk_github_a','usr_github_viewer','viewer',$1,$1,$1,$1),
			('wrk_github_b','usr_github_outsider','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed tenant graph: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	login := func(email string) tenantGitHubIntegrationHuman {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email:      email,
			Password:   password,
			ClientKind: "api",
			RemoteAddr: "192.0.2.81:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		principal, session, err := stack.IdentitySessions.Authenticate(
			ctx,
			"Bearer "+result.AccessToken,
		)
		if err != nil {
			t.Fatalf("authenticate %s: %v", email, err)
		}
		return tenantGitHubIntegrationHuman{principal: principal, session: session}
	}

	links := githubapp.NewSourceLinkStore(pool)
	links.Now = func() time.Time { return now }
	repositories := map[tenantGitHubVerifierKey]githubapp.Repo{
		{installationID: 4101, repositoryID: 5101}: integrationGitHubRepo(5101, "acme/control-plane"),
		{installationID: 4102, repositoryID: 5102}: integrationGitHubRepo(5102, "acme/versioned"),
		{installationID: 4103, repositoryID: 5103}: integrationGitHubRepo(5103, "acme/delete"),
		{installationID: 4104, repositoryID: 5104}: integrationGitHubRepo(5104, "acme/revoked"),
		{installationID: 4105, repositoryID: 5105}: integrationGitHubRepo(5105, "acme/lock-order"),
		{installationID: 4201, repositoryID: 5201}: integrationGitHubRepo(5201, "foreign/api"),
	}
	verifier := &tenantGitHubIntegrationVerifier{
		repositories: repositories,
		branches: map[tenantGitHubVerifierKey][]githubapp.Branch{
			{installationID: 4101, repositoryID: 5101}: {
				{Name: "main"},
				{Name: "release"},
			},
		},
	}
	fixture := &tenantGitHubIntegrationFixture{
		ctx:      ctx,
		pool:     pool,
		now:      now,
		links:    links,
		verifier: verifier,
		member:   login("member-github@example.com"),
		viewer:   login("viewer-github@example.com"),
		outsider: login("outsider-github@example.com"),
	}
	for _, grant := range []struct {
		workspaceID    string
		installationID int64
		repositoryID   int64
		repository     string
	}{
		{workspaceID: "wrk_github_a", installationID: 4101, repositoryID: 5101, repository: "acme/control-plane"},
		{workspaceID: "wrk_github_a", installationID: 4102, repositoryID: 5102, repository: "acme/versioned"},
		{workspaceID: "wrk_github_a", installationID: 4103, repositoryID: 5103, repository: "acme/delete"},
		{workspaceID: "wrk_github_a", installationID: 4104, repositoryID: 5104, repository: "acme/revoked"},
		{workspaceID: "wrk_github_a", installationID: 4105, repositoryID: 5105, repository: "acme/lock-order"},
		{workspaceID: "wrk_github_b", installationID: 4201, repositoryID: 5201, repository: "foreign/api"},
	} {
		if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
			if _, err := links.PutInstallationGrantInTx(ctx, tx, githubapp.InstallationGrant{
				WorkspaceID:    grant.workspaceID,
				InstallationID: grant.installationID,
				AccountLogin:   "acme",
				TargetType:     "Organization",
				GrantedByActor: "recovery:integration",
			}); err != nil {
				return err
			}
			repository := fixture.repositoryGrant(
				grant.installationID,
				grant.repositoryID,
				grant.repository,
			)
			repository.WorkspaceID = grant.workspaceID
			_, err := links.PutRepositoryGrantInTx(ctx, tx, repository)
			return err
		}); err != nil {
			t.Fatalf("seed source grant %d/%d: %v", grant.installationID, grant.repositoryID, err)
		}
	}

	idempotencyStore := idempotency.NewPostgresStore(pool)
	idempotencyStore.Now = func() time.Time { return now }
	auditChain := audit.NewPostgresChain(pool)
	auditChain.SetClock(func() time.Time { return now })
	fixture.coordinator = NewPostgresTenantGitHubSourceLinkCoordinator(
		pool,
		idempotencyStore,
		auditChain,
		links,
		func() time.Time { return now },
	)
	fixture.coordinator.Verifier = verifier
	return fixture
}

func (f *tenantGitHubIntegrationFixture) repositoryGrant(
	installationID int64,
	repositoryID int64,
	repository string,
) githubapp.RepositoryGrant {
	return githubapp.RepositoryGrant{
		WorkspaceID:    "wrk_github_a",
		InstallationID: installationID,
		RepositoryID:   repositoryID,
		Repository:     repository,
		CloneURL:       "https://github.com/" + repository + ".git",
		DefaultBranch:  "main",
		VerifiedAt:     f.now.Add(-time.Minute),
		GrantedByActor: "recovery:integration",
	}
}

func (f *tenantGitHubIntegrationFixture) putRequest(
	human tenantGitHubIntegrationHuman,
	serviceID string,
	installationID int64,
	repositoryID int64,
	branch string,
	key idempotency.Key,
) TenantGitHubLinkMutationRequest {
	body := []byte(`{"installation_id":4101,"repository_id":5101,"branch":"` + branch + `"}`)
	return TenantGitHubLinkMutationRequest{
		TenantGitHubServiceRequest: TenantGitHubServiceRequest{
			TenantGitHubIdentityRequest: TenantGitHubIdentityRequest{
				Principal: human.principal,
				Session:   human.session,
			},
			ServiceID: orailtypes.ID(serviceID),
		},
		InstallationID: installationID,
		RepositoryID:   repositoryID,
		Branch:         branch,
		Key:            key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPut,
			Path:       tenantGitHubLinkIdempotencyPath(orailtypes.ID(serviceID)),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: "req-" + string(key),
		SourceIP:  "192.0.2.81",
		UserAgent: "midnight-integration",
	}
}

func (f *tenantGitHubIntegrationFixture) deleteRequest(
	human tenantGitHubIntegrationHuman,
	serviceID string,
	key idempotency.Key,
) TenantGitHubLinkDeleteRequest {
	return TenantGitHubLinkDeleteRequest{
		TenantGitHubServiceRequest: TenantGitHubServiceRequest{
			TenantGitHubIdentityRequest: TenantGitHubIdentityRequest{
				Principal: human.principal,
				Session:   human.session,
			},
			ServiceID: orailtypes.ID(serviceID),
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodDelete,
			Path:       tenantGitHubLinkIdempotencyPath(orailtypes.ID(serviceID)),
			BodySHA256: idempotency.BodyHashSHA256(nil),
		},
		RequestID: "req-" + string(key),
		SourceIP:  "192.0.2.81",
		UserAgent: "midnight-integration",
	}
}

func integrationGitHubRepo(id int64, repository string) githubapp.Repo {
	return githubapp.Repo{
		ID:       id,
		FullName: repository,
		CloneURL: "https://github.com/" + repository + ".git",
		HTMLURL:  "https://github.com/" + repository,
		Default:  "main",
	}
}

func githubPutKey(suffix string) idempotency.Key {
	return idempotency.Key("github-put-" + suffix)
}

func assertTenantGitHubCount(
	t *testing.T,
	fixture *tenantGitHubIntegrationFixture,
	query string,
	want int,
	args ...any,
) {
	t.Helper()
	var got int
	if err := fixture.pool.QueryRow(fixture.ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d for %s", got, want, query)
	}
}

func waitForTenantGitHubAdvisoryWait(
	t *testing.T,
	fixture *tenantGitHubIntegrationFixture,
	pid int32,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waitType, waitEvent string
		err := fixture.pool.QueryRow(fixture.ctx, `
			SELECT COALESCE(wait_event_type,''),COALESCE(wait_event,'')
			  FROM pg_stat_activity
			 WHERE pid=$1`,
			pid,
		).Scan(&waitType, &waitEvent)
		if err == nil &&
			waitType == "Lock" &&
			strings.EqualFold(waitEvent, "advisory") {
			return
		}
		select {
		case <-fixture.ctx.Done():
			t.Fatal(fixture.ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("backend %d did not block on the installation advisory lock", pid)
}
