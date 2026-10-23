//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestGitHubPreviewLifecycleCreatesIsolatesAttachesAndCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "preview-proof", DisplayName: "Preview proof"})
	if err != nil {
		t.Fatal(err)
	}
	production, err := resources.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "production", Name: "Production", Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web", DisplayName: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_environment_configs (
		project_id,service_id,environment_id,version,builder,target_port,replicas
	) VALUES ($1,$2,$3,3,'railpack',8080,4)`, project.ID, service.ID, production.ID); err != nil {
		t.Fatal(err)
	}

	keyProvider, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := secrets.NewVaultWithStore(keyProvider, secrets.DenyByDefault{}, secrets.NewPostgresEnvelopeStore(pool))
	for _, variable := range []struct {
		name  string
		scope secrets.Scope
	}{
		{"public_flag", secrets.ScopePlain}, {"api_token", secrets.ScopeSecret}, {"root_key", secrets.ScopeSealed},
	} {
		if _, err := vault.Set(ctx, secrets.VariableInput{Name: variable.name, Workspace: DefaultWorkspaceID,
			Project: string(project.ID), Environment: string(production.ID), Service: string(service.ID),
			Scope: variable.scope, Plaintext: []byte("value-" + variable.name), ValueKind: "literal"}); err != nil {
			t.Fatal(err)
		}
	}

	links := githubapp.NewSourceLinkStore(pool)
	links.Now = func() time.Time { return now }
	var installation githubapp.InstallationGrant
	var repository githubapp.RepositoryGrant
	var link githubapp.DurableServiceLink
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var err error
		installation, err = links.PutInstallationGrantInTx(ctx, tx, githubapp.InstallationGrant{
			WorkspaceID: DefaultWorkspaceID, InstallationID: 77, AccountLogin: "acme",
			TargetType: "Organization", GrantedByActor: "recovery:test"})
		if err != nil {
			return err
		}
		repository, err = links.PutRepositoryGrantInTx(ctx, tx, githubapp.RepositoryGrant{
			WorkspaceID: DefaultWorkspaceID, InstallationID: 77, RepositoryID: 7700,
			Repository: "acme/web", CloneURL: "https://github.com/acme/web.git",
			DefaultBranch: "main", VerifiedAt: now, GrantedByActor: "recovery:test"})
		if err != nil {
			return err
		}
		link, err = links.PutServiceLinkInTx(ctx, tx, githubapp.DurableServiceLink{
			ServiceID: string(service.ID), ProjectID: string(project.ID), WorkspaceID: DefaultWorkspaceID,
			Link: githubapp.ServiceLink{InstallationID: 77, RepositoryID: 7700, Repo: "acme/web",
				Branch: "main", CloneURL: "https://github.com/acme/web.git"}, AutoDeploy: true})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	routes := router.NewPostgresDurableRouteStore(pool)
	routes.Now = func() time.Time { return now }
	actions := NewPostgresDeploymentActionStore(pool)
	actions.Now = func() time.Time { return now }
	lifecycle := NewPostgresGitHubPreviewLifecycle(pool, canonicalSourceLinkLookup{store: links}, keyProvider, routes, actions, "apps.example.test")
	lifecycle.Now = func() time.Time { return now }

	intent := previewIntegrationIntent(service, project, installation, repository, link, 42, strings.Repeat("a", 40), false)
	prepared, err := lifecycle.PreparePullRequest(ctx, intent)
	if err != nil {
		t.Fatalf("prepare trusted preview: %v", err)
	}
	assertPreviewCopiedScopes(t, ctx, pool, prepared.EnvironmentID, []string{"plain", "secret"})
	var replicas int
	if err := pool.QueryRow(ctx, `SELECT replicas FROM service_environment_configs WHERE environment_id=$1 AND service_id=$2`, prepared.EnvironmentID, service.ID).Scan(&replicas); err != nil || replicas != 4 {
		t.Fatalf("trusted preview replicas=%d err=%v", replicas, err)
	}
	if !strings.HasPrefix(prepared.URL, "https://") || !strings.HasSuffix(prepared.URL, ".apps.example.test") {
		t.Fatalf("preview URL=%q", prepared.URL)
	}

	outbox := NewPostgresStagedWorkOutbox(pool)
	outbox.Now = func() time.Time { return now }
	op := orailtypes.Operation{ID: defaultOperationID(), Kind: "build", State: orailtypes.OperationQueued}
	deployment := DeploymentRecord{ID: mintID(orailtypes.IDPrefixDeployment), ProjectID: string(project.ID),
		ServiceID: string(service.ID), EnvironmentID: prepared.EnvironmentID, Action: deploy.ActionDeploySource,
		SourceType: "git", Repo: intent.CloneURL, Ref: intent.SHA, SHA: intent.SHA,
		OperationID: string(op.ID), TriggerKey: "preview-proof:42:a"}
	commitRequest := GitHubBuildIntakeCommit{
		Operation: op, Deployment: deployment, PreviewID: prepared.PreviewID, PreviewURL: prepared.URL,
		SourceAuthority: GitHubBuildSourceAuthority{WorkspaceID: DefaultWorkspaceID,
			ProjectID: string(project.ID), ServiceID: string(service.ID), InstallationID: 77,
			InstallationGrantResourceVersion: installation.ResourceVersion, RepositoryID: 7700,
			Repository: "acme/web", CloneURL: "https://github.com/acme/web.git",
			RepositoryGrantResourceVersion: repository.ResourceVersion, Branch: "main",
			LinkResourceVersion: link.ResourceVersion},
		Enqueue: queue.EnqueueRequest{Queue: queue.Build, PartitionKey: string(project.ID),
			ParentOperationID: string(op.ID), AvailableAt: now, Payload: []byte(`{"schema_version":"midnight.dev/deploy-job/v1alpha1"}`)},
		Now: now,
	}
	result, err := outbox.CommitGitHubBuildIntake(ctx, commitRequest)
	if err != nil {
		t.Fatalf("attach preview deployment: %v", err)
	}
	var state, attached string
	if err := pool.QueryRow(ctx, `SELECT state,COALESCE(deployment_id,'') FROM github_pull_request_previews WHERE id=$1`, prepared.PreviewID).Scan(&state, &attached); err != nil || state != "deploying" || attached != result.DeploymentID {
		t.Fatalf("attached preview state=%q deployment=%q err=%v", state, attached, err)
	}
	var routeBindings, comments int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM deployment_route_intents WHERE deployment_id=$1`, result.DeploymentID).Scan(&routeBindings); err != nil || routeBindings != 1 {
		t.Fatalf("route bindings=%d err=%v", routeBindings, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM github_pull_request_preview_comments WHERE preview_id=$1`, prepared.PreviewID).Scan(&comments); err != nil || comments != 1 {
		t.Fatalf("comments=%d err=%v", comments, err)
	}

	for index, next := range []string{"building", "deploying", "active"} {
		active := next == "active"
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,
			completed_at=CASE WHEN $2 THEN $3 ELSE completed_at END,updated_at=$3 WHERE id=$4`,
			next, active, now.Add(time.Duration(index+1)*time.Minute), result.DeploymentID); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM github_pull_request_previews WHERE id=$1`, prepared.PreviewID).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("ready state=%q err=%v", state, err)
	}
	replayedPreparation, err := lifecycle.PreparePullRequest(ctx, intent)
	if err != nil || replayedPreparation != prepared {
		t.Fatalf("replayed preparation=%#v err=%v", replayedPreparation, err)
	}
	replayedCommit, err := outbox.CommitGitHubBuildIntake(ctx, commitRequest)
	if err != nil || replayedCommit.Created || replayedCommit.DeploymentID != result.DeploymentID {
		t.Fatalf("replayed commit=%#v err=%v", replayedCommit, err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM github_pull_request_previews WHERE id=$1`, prepared.PreviewID).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("replayed ready state=%q err=%v", state, err)
	}
	commentStore := &githubapp.PostgresPreviewCommentStore{Pool: pool}
	claimed, err := commentStore.ClaimPreviewComments(ctx, githubapp.PreviewCommentClaimRequest{Limit: 25, Now: now.Add(4 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || len(claimed) != 1 || claimed[0].State != "deploying" {
		t.Fatalf("first serialized preview comment=%#v err=%v", claimed, err)
	}
	if err := commentStore.MarkPreviewCommentPosted(ctx, claimed[0].ID, claimed[0].LeaseToken, 901, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claimed, err = commentStore.ClaimPreviewComments(ctx, githubapp.PreviewCommentClaimRequest{Limit: 25, Now: now.Add(5 * time.Minute), LeaseDuration: time.Minute})
	if err != nil || len(claimed) != 1 || claimed[0].State != "ready" || claimed[0].CommentID != 901 {
		t.Fatalf("second serialized preview comment=%#v err=%v", claimed, err)
	}

	forkIntent := previewIntegrationIntent(service, project, installation, repository, link, 43, strings.Repeat("b", 40), true)
	forkPrepared, err := lifecycle.PreparePullRequest(ctx, forkIntent)
	if err != nil {
		t.Fatalf("prepare fork preview: %v", err)
	}
	assertPreviewCopiedScopes(t, ctx, pool, forkPrepared.EnvironmentID, []string{"plain"})
	if err := pool.QueryRow(ctx, `SELECT replicas FROM service_environment_configs WHERE environment_id=$1 AND service_id=$2`, forkPrepared.EnvironmentID, service.ID).Scan(&replicas); err != nil || replicas != 1 {
		t.Fatalf("fork replicas=%d err=%v", replicas, err)
	}

	closeIntent := intent
	closeIntent.Preview = &githubapp.PullRequestPreviewIntent{Action: previewActionDelete, DeliveryID: "delivery-close",
		Number: 42, ExistingPreviewID: prepared.PreviewID, ExistingEnvironmentID: prepared.EnvironmentID,
		BaseBranch: "main", HeadBranch: "feature", HeadSHA: intent.SHA}
	if _, err := lifecycle.ClosePullRequest(ctx, closeIntent); err != nil {
		t.Fatalf("close preview: %v", err)
	}
	var action string
	if err := pool.QueryRow(ctx, `SELECT action FROM deployment_runtime_actions WHERE deployment_id=$1`, result.DeploymentID).Scan(&action); err != nil || action != "stop" {
		t.Fatalf("cleanup action=%q err=%v", action, err)
	}

	lifecycle.Now = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if _, err := lifecycle.Reconcile(ctx, 25); err != nil {
		t.Fatalf("expire fork preview: %v", err)
	}
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT state FROM github_pull_request_previews WHERE id=$1`, forkPrepared.PreviewID).Scan(&state); err != nil || state != "deleted" {
		t.Fatalf("expired fork state=%q err=%v", state, err)
	}
	if err := pool.QueryRow(ctx, `SELECT deleted_at FROM environments WHERE id=$1`, forkPrepared.EnvironmentID).Scan(&deletedAt); err != nil || deletedAt == nil {
		t.Fatalf("expired environment deleted_at=%v err=%v", deletedAt, err)
	}
}

func previewIntegrationIntent(service Service, project Project, installation githubapp.InstallationGrant, repository githubapp.RepositoryGrant, link githubapp.DurableServiceLink, number int, sha string, fork bool) githubapp.DeployIntent {
	return githubapp.DeployIntent{ServiceID: string(service.ID), ProjectID: string(project.ID), WorkspaceID: project.WorkspaceID,
		Repo: "acme/web", RepositoryID: 7700, CloneURL: "https://github.com/acme/web.git", Ref: "refs/heads/main", SHA: sha,
		InstallationID: 77, InstallationGrantResourceVersion: installation.ResourceVersion,
		InstallationGrantCreatedAt: installation.CreatedAt, InstallationGrantUpdatedAt: installation.UpdatedAt,
		RepositoryGrantResourceVersion: repository.ResourceVersion, RepositoryGrantCreatedAt: repository.CreatedAt,
		RepositoryGrantUpdatedAt: repository.UpdatedAt, RepositoryAccessVerifiedAt: repository.VerifiedAt,
		LinkVersion: link.ResourceVersion, LinkCreatedAt: link.CreatedAt, LinkUpdatedAt: link.UpdatedAt,
		LinkRepositoryAccessVerifiedAt: link.LinkRepositoryAccessVerifiedAt, Untrusted: fork,
		Preview: &githubapp.PullRequestPreviewIntent{Action: previewActionUpsert, DeliveryID: "delivery-preview",
			Number: number, BaseBranch: "main", HeadRepositoryID: 7700, HeadRepository: "acme/web",
			HeadBranch: "feature", HeadSHA: sha, FromFork: fork}}
}

func assertPreviewCopiedScopes(t *testing.T, ctx context.Context, pool *pg.Pool, environmentID string, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT scope FROM service_variables WHERE environment_id=$1 AND deleted_at IS NULL ORDER BY scope`, environmentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("preview scopes=%v want=%v", got, want)
	}
}
