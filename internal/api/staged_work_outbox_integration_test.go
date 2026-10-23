//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/webhook"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresStagedWorkOutboxRecoversGitHubAndBuildHandoffs(t *testing.T) {
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

	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "staged-work"})
	if err != nil {
		t.Fatal(err)
	}
	environment, err := resources.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "production", Tier: "production"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	sourceLinks := githubapp.NewSourceLinkStore(pool)
	sourceLinks.Now = func() time.Time { return now }
	var canonicalLink githubapp.DurableServiceLink
	var installationGrant githubapp.InstallationGrant
	var repositoryGrant githubapp.RepositoryGrant
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var err error
		installationGrant, err = sourceLinks.PutInstallationGrantInTx(ctx, tx, githubapp.InstallationGrant{
			WorkspaceID:    DefaultWorkspaceID,
			InstallationID: 99,
			AccountLogin:   "acme",
			TargetType:     "Organization",
			GrantedByActor: "recovery:test",
		})
		if err != nil {
			return err
		}
		repositoryGrant, err = sourceLinks.PutRepositoryGrantInTx(ctx, tx, githubapp.RepositoryGrant{
			WorkspaceID:    DefaultWorkspaceID,
			InstallationID: 99,
			RepositoryID:   4242,
			Repository:     "acme/web",
			CloneURL:       "https://github.com/acme/web.git",
			DefaultBranch:  "main",
			VerifiedAt:     now,
			GrantedByActor: "recovery:test",
		})
		if err != nil {
			return err
		}
		canonicalLink, err = sourceLinks.PutServiceLinkInTx(ctx, tx, githubapp.DurableServiceLink{
			ServiceID:   string(service.ID),
			ProjectID:   string(project.ID),
			WorkspaceID: DefaultWorkspaceID,
			Link: githubapp.ServiceLink{
				InstallationID: 99,
				RepositoryID:   4242,
				Repo:           "acme/web",
				Branch:         "main",
				CloneURL:       "https://github.com/acme/web.git",
			},
			AutoDeploy: true,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	outbox := NewPostgresStagedWorkOutbox(pool)
	outbox.Now = func() time.Time { return now }
	operation := orailtypes.Operation{ID: defaultOperationID(), Kind: "build", State: orailtypes.OperationQueued}
	deployment := DeploymentRecord{
		ID:            mintID(orailtypes.IDPrefixDeployment),
		ProjectID:     string(project.ID),
		ServiceID:     string(service.ID),
		EnvironmentID: string(environment.ID),
		Action:        deploy.ActionDeploySource,
		SourceType:    "git",
		Repo:          "https://github.com/acme/web.git",
		Ref:           strings.Repeat("a", 40),
		SHA:           strings.Repeat("a", 40),
		OperationID:   string(operation.ID),
		TriggerKey:    "trigger:github-staged-integration",
	}
	githubPayload := []byte(`{"schema_version":"midnight.dev/deploy-job/v1alpha1","operation_id":"` + string(operation.ID) + `","service_id":"` + string(service.ID) + `"}`)
	githubCommit := GitHubBuildIntakeCommit{
		Operation:  operation,
		Deployment: deployment,
		SourceAuthority: GitHubBuildSourceAuthority{
			WorkspaceID:                      DefaultWorkspaceID,
			ProjectID:                        string(project.ID),
			ServiceID:                        string(service.ID),
			InstallationID:                   99,
			RepositoryID:                     4242,
			Repository:                       "acme/web",
			CloneURL:                         "https://github.com/acme/web.git",
			Branch:                           "main",
			InstallationGrantResourceVersion: installationGrant.ResourceVersion,
			RepositoryGrantResourceVersion:   repositoryGrant.ResourceVersion,
			LinkResourceVersion:              canonicalLink.ResourceVersion,
		},
		Enqueue: queue.EnqueueRequest{
			Queue:             queue.Build,
			PartitionKey:      string(project.ID),
			ParentOperationID: string(operation.ID),
			AvailableAt:       now,
			Payload:           githubPayload,
		},
		Now: now,
	}
	for name, mutate := range map[string]func(*GitHubBuildSourceAuthority){
		"installation grant": func(authority *GitHubBuildSourceAuthority) {
			authority.InstallationGrantResourceVersion++
		},
		"repository grant": func(authority *GitHubBuildSourceAuthority) {
			authority.RepositoryGrantResourceVersion++
		},
		"service link": func(authority *GitHubBuildSourceAuthority) {
			authority.LinkResourceVersion++
		},
	} {
		t.Run("rejects stale "+name+" authority", func(t *testing.T) {
			stale := githubCommit
			mutate(&stale.SourceAuthority)
			if _, err := outbox.CommitGitHubBuildIntake(ctx, stale); !errors.Is(err, ErrGitHubBuildSourceAuthorityRevoked) {
				t.Fatalf("stale %s authority error = %v", name, err)
			}
		})
	}
	assertTableCount(t, ctx, pool, "operations", 0)
	assertTableCount(t, ctx, pool, "deployments", 0)
	assertTableCount(t, ctx, pool, "staged_work_outbox", 0)

	accepted, err := outbox.CommitGitHubBuildIntake(ctx, githubCommit)
	if err != nil || !accepted.Created || accepted.OutboxID == "" {
		t.Fatalf("GitHub staged commit = %#v, %v", accepted, err)
	}
	assertTableCount(t, ctx, pool, "operations", 1)
	assertTableCount(t, ctx, pool, "deployments", 1)
	assertTableCount(t, ctx, pool, "staged_work_outbox", 1)
	assertTableCount(t, ctx, pool, "github_commit_statuses", 1)
	assertTableCount(t, ctx, pool, "jobs", 0)
	statusStore := githubapp.NewPostgresStatusStore(pool)
	firstClaims, err := statusStore.ClaimCommitStatuses(ctx, githubapp.StatusClaimRequest{
		Limit: 1, Now: now, LeaseDuration: time.Minute,
	})
	if err != nil || len(firstClaims) != 1 || firstClaims[0].Attempt != 1 {
		t.Fatalf("first status claim = %#v, %v", firstClaims, err)
	}
	if concurrent, err := statusStore.ClaimCommitStatuses(ctx, githubapp.StatusClaimRequest{
		Limit: 1, Now: now, LeaseDuration: time.Minute,
	}); err != nil || len(concurrent) != 0 {
		t.Fatalf("concurrent status claim = %#v, %v", concurrent, err)
	}
	dead, err := statusStore.MarkCommitStatusFailed(ctx, githubapp.StatusFailureRequest{
		ID: firstClaims[0].ID, LeaseToken: firstClaims[0].LeaseToken,
		FailedAt: now, AvailableAt: now.Add(time.Second), LastError: "temporary",
	})
	if err != nil || dead {
		t.Fatalf("reschedule status = dead:%t err:%v", dead, err)
	}
	retryClaims, err := statusStore.ClaimCommitStatuses(ctx, githubapp.StatusClaimRequest{
		Limit: 1, Now: now.Add(time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || len(retryClaims) != 1 || retryClaims[0].Attempt != 2 {
		t.Fatalf("status retry claim = %#v, %v", retryClaims, err)
	}
	if err := statusStore.MarkCommitStatusPosted(ctx, firstClaims[0].ID, firstClaims[0].LeaseToken, now.Add(2*time.Second)); !errors.Is(err, githubapp.ErrCommitStatusLeaseLost) {
		t.Fatalf("stale status completion = %v, want lease lost", err)
	}
	if err := statusStore.MarkCommitStatusPosted(ctx, retryClaims[0].ID, retryClaims[0].LeaseToken, now.Add(2*time.Second)); err != nil {
		t.Fatalf("complete status retry: %v", err)
	}

	// A concurrent/redelivered source intent carries newly proposed IDs, but
	// the durable trigger lock resolves it to the existing canonical rows.
	duplicate := githubCommit
	duplicate.Operation.ID = defaultOperationID()
	duplicate.Deployment.ID = mintID(orailtypes.IDPrefixDeployment)
	duplicate.Deployment.OperationID = string(duplicate.Operation.ID)
	duplicate.Enqueue.ParentOperationID = string(duplicate.Operation.ID)
	replayed, err := outbox.CommitGitHubBuildIntake(ctx, duplicate)
	if err != nil || replayed.Created ||
		replayed.OperationID != string(operation.ID) ||
		replayed.DeploymentID != deployment.ID {
		t.Fatalf("GitHub staged replay = %#v, %v", replayed, err)
	}
	assertTableCount(t, ctx, pool, "operations", 1)
	assertTableCount(t, ctx, pool, "deployments", 1)

	// Simulate API process loss after commit. A newly constructed dispatcher
	// delivers the build job exactly once from durable state.
	restarted := NewPostgresStagedWorkOutbox(pool)
	restarted.Now = func() time.Time { return now.Add(time.Second) }
	restarted.WorkerID = "github-restart"
	if delivered, err := restarted.DispatchStagedWork(ctx, 8); err != nil || delivered != 1 {
		t.Fatalf("GitHub dispatch after restart = %d, %v", delivered, err)
	}
	assertTableCount(t, ctx, pool, "jobs", 1)

	operations := NewPostgresOperationStore(pool)
	deployments := NewPostgresDeploymentStore(pool)
	deployments.Now = func() time.Time { return now.Add(time.Second) }
	if _, err := operations.Transition(ctx, operation.ID, orailtypes.OperationRunning); err != nil {
		t.Fatal(err)
	}
	if err := deployments.MarkDeploymentBuilding(ctx, deployment.ID); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	handoffPayload := []byte(`{"schema_version":"midnight.dev/deploy-job/v1alpha1","operation_id":"` + string(operation.ID) + `","candidate":{"deployment_id":"` + deployment.ID + `","image":"registry.test/acme/web@` + digest + `"}}`)
	handoff := BuildDeployHandoffCommit{
		OperationID:  string(operation.ID),
		DeploymentID: deployment.ID,
		SourceSHA:    strings.Repeat("a", 40),
		Builder:      "railpack",
		ImageRef:     "registry.test/acme/web",
		ImageDigest:  digest,
		Enqueue: queue.EnqueueRequest{
			Queue:             queue.Deploy,
			PartitionKey:      string(project.ID),
			ParentOperationID: string(operation.ID),
			AvailableAt:       now.Add(2 * time.Second),
			Payload:           handoffPayload,
		},
		Now: now.Add(2 * time.Second),
	}
	handoffAccepted, err := restarted.CommitBuildDeployHandoff(ctx, handoff)
	if err != nil || !handoffAccepted.Created || handoffAccepted.OutboxID == "" {
		t.Fatalf("build handoff commit = %#v, %v", handoffAccepted, err)
	}
	var storedSHA, storedBuilder, storedRef, storedDigest string
	var buildStartedAt, buildCompletedAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT sha,builder,image_ref,image_digest,build_started_at,build_completed_at FROM deployments WHERE id=$1`,
		deployment.ID,
	).Scan(&storedSHA, &storedBuilder, &storedRef, &storedDigest, &buildStartedAt, &buildCompletedAt); err != nil {
		t.Fatal(err)
	}
	if storedSHA != handoff.SourceSHA || storedBuilder != handoff.Builder ||
		storedRef != handoff.ImageRef || storedDigest != handoff.ImageDigest {
		t.Fatalf("stored build result = %q %q %q %q", storedSHA, storedBuilder, storedRef, storedDigest)
	}
	if !buildStartedAt.Equal(now.Add(time.Second)) || !buildCompletedAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("build lifecycle = %s..%s", buildStartedAt, buildCompletedAt)
	}
	if replay, err := restarted.CommitBuildDeployHandoff(ctx, handoff); err != nil || replay.Created || replay.OutboxID != handoffAccepted.OutboxID {
		t.Fatalf("build handoff replay = %#v, %v", replay, err)
	}
	assertTableCount(t, ctx, pool, "staged_work_outbox", 2)
	assertTableCount(t, ctx, pool, "jobs", 1)

	// Simulate build-worker loss after its atomic result+handoff commit.
	workerRestart := NewPostgresStagedWorkOutbox(pool)
	workerRestart.Now = func() time.Time { return now.Add(3 * time.Second) }
	workerRestart.WorkerID = "build-restart"
	if delivered, err := workerRestart.DispatchStagedWork(ctx, 8); err != nil || delivered != 1 {
		t.Fatalf("deploy handoff after restart = %d, %v", delivered, err)
	}
	assertTableCount(t, ctx, pool, "jobs", 2)
	if delivered, err := workerRestart.DispatchStagedWork(ctx, 8); err != nil || delivered != 0 {
		t.Fatalf("duplicate staged delivery = %d, %v", delivered, err)
	}
	var buildJobs, deployJobs int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE queue='build'), count(*) FILTER (WHERE queue='deploy') FROM jobs`,
	).Scan(&buildJobs, &deployJobs); err != nil {
		t.Fatal(err)
	}
	if buildJobs != 1 || deployJobs != 1 {
		t.Fatalf("jobs build=%d deploy=%d, want 1/1", buildJobs, deployJobs)
	}
	deployments.Now = func() time.Time { return now.Add(4 * time.Second) }
	if err := deployments.FailDeployment(ctx, deployment.ID, deploy.DeploymentBuilding, "BUILD_FAILED", "integration terminal status"); err != nil { // not-a-failure-code
		t.Fatalf("fail deployment: %v", err)
	}
	assertTableCount(t, ctx, pool, "github_commit_statuses", 2)
	if _, err := pool.Exec(ctx,
		`UPDATE github_commit_statuses SET max_attempts=1 WHERE deployment_id=$1 AND state='failure'`,
		deployment.ID,
	); err != nil {
		t.Fatal(err)
	}
	terminalClaims, err := statusStore.ClaimCommitStatuses(ctx, githubapp.StatusClaimRequest{
		Limit: 1, Now: now.Add(4 * time.Second), LeaseDuration: time.Minute,
	})
	if err != nil || len(terminalClaims) != 1 || terminalClaims[0].State != "failure" {
		t.Fatalf("terminal status claim = %#v, %v", terminalClaims, err)
	}
	dead, err = statusStore.MarkCommitStatusFailed(ctx, githubapp.StatusFailureRequest{
		ID: terminalClaims[0].ID, LeaseToken: terminalClaims[0].LeaseToken,
		FailedAt: now.Add(4 * time.Second), AvailableAt: now.Add(5 * time.Second), LastError: "permanent provider rejection",
	})
	if err != nil || !dead {
		t.Fatalf("terminal status dead letter = dead:%t err:%v", dead, err)
	}
}

func TestWebhookWorkerRecoversAmbiguousGitHubIntakeCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatal(err)
	}

	resources := NewPostgresResourceStore(pool)
	project, err := resources.CreateProject(ctx, Project{
		WorkspaceID: DefaultWorkspaceID,
		Slug:        "webhook-crash-window",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{
		ProjectID: project.ID,
		Slug:      "web",
	})
	if err != nil {
		t.Fatal(err)
	}

	authorityAt := time.Now().UTC().Add(-time.Minute)
	sourceLinks := githubapp.NewSourceLinkStore(pool)
	sourceLinks.Now = func() time.Time { return authorityAt }
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, err := sourceLinks.PutInstallationGrantInTx(
			ctx,
			tx,
			githubapp.InstallationGrant{
				WorkspaceID:    DefaultWorkspaceID,
				InstallationID: 99,
				AccountLogin:   "acme",
				TargetType:     "Organization",
				GrantedByActor: "recovery:test",
			},
		); err != nil {
			return err
		}
		if _, err := sourceLinks.PutRepositoryGrantInTx(
			ctx,
			tx,
			githubapp.RepositoryGrant{
				WorkspaceID:    DefaultWorkspaceID,
				InstallationID: 99,
				RepositoryID:   4242,
				Repository:     "acme/web",
				CloneURL:       "https://github.com/acme/web.git",
				DefaultBranch:  "main",
				VerifiedAt:     authorityAt,
				GrantedByActor: "recovery:test",
			},
		); err != nil {
			return err
		}
		_, err := sourceLinks.PutServiceLinkInTx(
			ctx,
			tx,
			githubapp.DurableServiceLink{
				ServiceID:   string(service.ID),
				ProjectID:   string(project.ID),
				WorkspaceID: DefaultWorkspaceID,
				Link: githubapp.ServiceLink{
					InstallationID: 99,
					RepositoryID:   4242,
					Repo:           "acme/web",
					Branch:         "main",
					CloneURL:       "https://github.com/acme/web.git",
				},
				AutoDeploy: true,
			},
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	inbox := webhook.NewPostgresDeliveryStore(pool)
	outbox := NewPostgresStagedWorkOutbox(pool)
	operations := NewPostgresOperationStore(pool)
	deployments := NewPostgresDeploymentStore(pool)
	clock := func() time.Time { return time.Now().UTC() }
	dispatcher := NewGitHubWebhookDispatcher(
		resources,
		operations,
		queue.NewPostgresStore(pool),
		nil,
		clock,
		nil,
		resources,
	).WithDeploymentStore(deployments).
		WithStagedWorkOutbox(outbox).
		WithServiceLookup(canonicalSourceLinkLookup{store: sourceLinks})

	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"repository":{
			"id":4242,
			"full_name":"acme/web",
			"clone_url":"https://github.com/acme/web.git"
		},
		"sender":{"type":"User"},
		"pusher":{"name":"alice"},
		"head_commit":{
			"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"message":"feat: crash-safe webhook intake"
		},
		"commits":[{
			"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
			"modified":["main.go"]
		}],
		"installation":{"id":99}
	}`)
	payloadHash := webhook.PayloadHashSHA256(body)
	accepted, err := inbox.Accept(ctx, webhook.InboxAcceptance{
		Provider:           webhook.ProviderGitHub,
		EndpointID:         "github-app",
		ProviderDeliveryID: "delivery-ambiguous-downstream-commit",
		EventType:          string(githubapp.EventPush),
		EventHash:          payloadHash,
		PayloadSHA256:      payloadHash,
		Payload:            body,
		SignatureKeyID:     "github-app-current",
	})
	if err != nil {
		t.Fatalf("accept webhook: %v", err)
	}

	crashingStore := &failFirstWebhookTargetCompletion{
		DurableInbox: inbox,
	}
	firstWorker := webhook.Worker{
		Store:      crashingStore,
		Planner:    dispatcher,
		Dispatcher: dispatcher,
	}
	if err := firstWorker.ProcessOne(ctx); !errors.Is(err, errSyntheticWebhookTargetCompletionCrash) {
		t.Fatalf("first worker error = %v, want synthetic completion crash", err)
	}
	firstResult := crashingStore.result
	if firstResult.OperationID == "" ||
		firstResult.DeploymentID == "" ||
		firstResult.StagedOutboxID == "" {
		t.Fatalf("downstream commit result = %#v, want complete canonical identity", firstResult)
	}
	detail, err := inbox.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != webhook.InboxProcessing ||
		len(detail.Targets) != 1 ||
		detail.Targets[0].State != webhook.InboxProcessing ||
		detail.Targets[0].OperationID != "" ||
		detail.Targets[0].DeploymentID != "" ||
		detail.Targets[0].StagedOutboxID != "" {
		t.Fatalf("ambiguous webhook state = %#v", detail)
	}
	assertTableCount(t, ctx, pool, "operations", 1)
	assertTableCount(t, ctx, pool, "deployments", 1)
	assertTableCount(t, ctx, pool, "staged_work_outbox", 1)
	assertTableCount(t, ctx, pool, "jobs", 0)

	// Simulate process loss by expiring the abandoned delivery and target
	// leases. The replacement worker must redispatch the immutable target.
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_delivery_targets
		   SET updated_at=created_at,
		       lease_expires_at=created_at+INTERVAL '1 microsecond'
		 WHERE delivery_id=$1 AND state='processing'`,
		accepted.ID,
	); err != nil {
		t.Fatalf("expire abandoned webhook target lease: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE webhook_deliveries
		   SET updated_at=received_at,
		       lease_expires_at=received_at+INTERVAL '1 microsecond'
		 WHERE id=$1 AND state='processing'`,
		accepted.ID,
	); err != nil {
		t.Fatalf("expire abandoned webhook delivery lease: %v", err)
	}
	replacementWorker := webhook.Worker{
		Store:      inbox,
		Planner:    dispatcher,
		Dispatcher: dispatcher,
	}
	if err := replacementWorker.ProcessOne(ctx); err != nil {
		t.Fatalf("replacement worker: %v", err)
	}

	detail, err = inbox.GetDelivery(ctx, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State != webhook.InboxSucceeded ||
		len(detail.Targets) != 1 ||
		detail.Targets[0].State != webhook.InboxSucceeded {
		t.Fatalf("recovered webhook state = %#v", detail)
	}
	recovered := detail.Targets[0]
	if recovered.OperationID != firstResult.OperationID ||
		recovered.DeploymentID != firstResult.DeploymentID ||
		recovered.StagedOutboxID != firstResult.StagedOutboxID {
		t.Fatalf(
			"recovered target identity = (%q,%q,%q), want first commit (%q,%q,%q)",
			recovered.OperationID,
			recovered.DeploymentID,
			recovered.StagedOutboxID,
			firstResult.OperationID,
			firstResult.DeploymentID,
			firstResult.StagedOutboxID,
		)
	}
	assertTableCount(t, ctx, pool, "operations", 1)
	assertTableCount(t, ctx, pool, "deployments", 1)
	assertTableCount(t, ctx, pool, "staged_work_outbox", 1)
	assertTableCount(t, ctx, pool, "jobs", 0)
}

var errSyntheticWebhookTargetCompletionCrash = errors.New(
	"synthetic crash before webhook target completion",
)

type failFirstWebhookTargetCompletion struct {
	webhook.DurableInbox
	result webhook.TargetResult
	failed bool
}

func (s *failFirstWebhookTargetCompletion) CompleteTarget(
	ctx context.Context,
	delivery webhook.ClaimedDelivery,
	target webhook.ClaimedTarget,
	result webhook.TargetResult,
) error {
	s.result = result
	if !s.failed {
		s.failed = true
		return errSyntheticWebhookTargetCompletionCrash
	}
	return s.DurableInbox.CompleteTarget(ctx, delivery, target, result)
}
