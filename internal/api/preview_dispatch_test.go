// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/webhook"
)

type previewLifecycleStub struct {
	intents   []githubapp.DeployIntent
	event     githubapp.PullRequestEvent
	planCalls int
}

func (s *previewLifecycleStub) PlanPullRequest(_ context.Context, event githubapp.PullRequestEvent, _ time.Time, _ string) ([]githubapp.DeployIntent, error) {
	s.planCalls++
	s.event = event
	return s.intents, nil
}
func (*previewLifecycleStub) PreparePullRequest(context.Context, githubapp.DeployIntent) (PreviewPreparation, error) {
	return PreviewPreparation{}, nil
}
func (*previewLifecycleStub) ClosePullRequest(context.Context, githubapp.DeployIntent) (PreviewEnvironmentView, error) {
	return PreviewEnvironmentView{}, nil
}
func (*previewLifecycleStub) ListServicePreviews(context.Context, string) ([]PreviewEnvironmentView, error) {
	return nil, nil
}
func (*previewLifecycleStub) Reconcile(context.Context, int) (int, error) { return 0, nil }

func TestGitHubWebhookPlannerRoutesLifecyclePullRequestToImmutableServiceTarget(t *testing.T) {
	sha := strings.Repeat("a", 40)
	intent := githubapp.DeployIntent{ServiceID: "svc_web", ProjectID: "prj_acme", WorkspaceID: "wrk_acme",
		Repo: "acme/web", RepositoryID: 7700, CloneURL: "https://github.com/acme/web.git",
		Ref: "refs/heads/main", SHA: sha, InstallationID: 77,
		InstallationGrantResourceVersion: 1, RepositoryGrantResourceVersion: 2, LinkVersion: 3,
		IdempotencyKey: "github-pr-preview-proof", Preview: &githubapp.PullRequestPreviewIntent{
			Action: previewActionUpsert, DeliveryID: "delivery-pr", Number: 42, BaseBranch: "main",
			HeadRepositoryID: 8800, HeadRepository: "contributor/web", HeadBranch: "feature",
			HeadSHA: sha, FromFork: true}, Untrusted: true}
	lifecycle := &previewLifecycleStub{intents: []githubapp.DeployIntent{intent}}
	dispatcher := NewGitHubWebhookDispatcher(NewMemoryResourceStore(), NewMemoryOperationStore(), nil, nil, time.Now, nil).
		WithPreviewLifecycle(lifecycle)
	receivedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	plan, err := dispatcher.Plan(context.Background(), webhook.ReceivedDelivery{
		Provider: webhook.ProviderGitHub, EventType: string(githubapp.EventPullRequest),
		DeliveryID: "delivery-pr", ReceivedAt: receivedAt,
		Body: previewPullRequestPayload("opened", sha),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.planCalls != 1 || lifecycle.event.Number != 42 || !lifecycle.event.FromFork {
		t.Fatalf("planned event calls=%d event=%#v", lifecycle.planCalls, lifecycle.event)
	}
	if len(plan.Targets) != 1 {
		t.Fatalf("targets=%#v", plan.Targets)
	}
	target := plan.Targets[0]
	if target.ServiceID != "svc_web" || target.RepositoryID != 7700 || target.Branch != "main" || target.CommitSHA != sha || len(target.Payload) == 0 {
		t.Fatalf("immutable target=%#v", target)
	}
}

func TestGitHubWebhookPlannerAcknowledgesNonLifecyclePullRequestAction(t *testing.T) {
	lifecycle := &previewLifecycleStub{}
	dispatcher := NewGitHubWebhookDispatcher(NewMemoryResourceStore(), NewMemoryOperationStore(), nil, nil, time.Now, nil).
		WithPreviewLifecycle(lifecycle)
	plan, err := dispatcher.Plan(context.Background(), webhook.ReceivedDelivery{
		Provider: webhook.ProviderGitHub, EventType: string(githubapp.EventPullRequest),
		DeliveryID: "delivery-label", ReceivedAt: time.Now().UTC(),
		Body: []byte(`{"action":"labeled"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.planCalls != 0 || plan.IgnoredReason != "event_not_routed:pull_request:labeled" {
		t.Fatalf("plan=%#v calls=%d", plan, lifecycle.planCalls)
	}
}

func TestGitHubPreviewDeployFetchesPullRefButPinsExactHeadSHA(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(t.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "preview-ref"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(t.Context(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	staged := &recordingGitHubBuildIntake{}
	dispatcher := NewGitHubWebhookDispatcher(
		resources, NewMemoryOperationStore(), &recordingQueueStore{}, nil, time.Now, nil,
	).WithDeploymentStore(NewMemoryDeploymentStore(nil)).WithStagedWorkOutbox(staged)
	sha := strings.Repeat("a", 40)
	intent := githubapp.DeployIntent{
		ServiceID: string(service.ID), ProjectID: string(project.ID), WorkspaceID: DefaultWorkspaceID,
		Repo: "acme/web", CloneURL: "https://github.com/acme/web.git", Ref: "refs/heads/main", SHA: sha,
		InstallationID: 77, RepositoryID: 7700, LinkVersion: 3, IdempotencyKey: "preview-ref-proof",
		Preview: &githubapp.PullRequestPreviewIntent{Action: previewActionUpsert, Number: 42,
			BaseBranch: "main", HeadSHA: sha, FromFork: true}, Untrusted: true,
	}
	_, err = dispatcher.enqueueDeploy(t.Context(), intent, &PreviewPreparation{
		PreviewID: "prv_preview", EnvironmentID: "env_preview", URL: "https://preview.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.commits) != 1 {
		t.Fatalf("staged commits = %d, want 1", len(staged.commits))
	}
	var payload deployJobPayload
	if err := json.Unmarshal(staged.commits[0].Enqueue.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Request.Source == nil || payload.Request.Source.Ref != sha ||
		payload.Request.Source.FetchRef != "refs/pull/42/head" ||
		payload.Request.Source.TrustProfile != DeployTrustProfileGitHubForkPreview ||
		staged.commits[0].Deployment.SHA != sha ||
		staged.commits[0].Deployment.EnvironmentID != "env_preview" {
		t.Fatalf("preview source=%#v deployment=%#v", payload.Request.Source, staged.commits[0].Deployment)
	}
}

func previewPullRequestPayload(action, sha string) []byte {
	return []byte(`{
		"action":"` + action + `","number":42,"installation":{"id":77},
		"repository":{"id":7700,"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"},
		"pull_request":{"number":42,
			"base":{"ref":"main","repo":{"id":7700,"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"}},
			"head":{"ref":"feature","sha":"` + sha + `","repo":{"id":8800,"full_name":"contributor/web","clone_url":"https://github.com/contributor/web.git"}}
		},"sender":{"login":"contributor"}
	}`)
}
