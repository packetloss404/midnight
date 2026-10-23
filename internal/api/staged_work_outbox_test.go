// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/webhook"
)

type recordingGitHubBuildIntake struct {
	mu      sync.Mutex
	commits []GitHubBuildIntakeCommit
	trigger map[string]StagedWorkCommitResult
}

func (s *recordingGitHubBuildIntake) CommitGitHubBuildIntake(_ context.Context, commit GitHubBuildIntakeCommit) (StagedWorkCommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.trigger == nil {
		s.trigger = make(map[string]StagedWorkCommitResult)
	}
	if existing, ok := s.trigger[commit.Deployment.TriggerKey]; ok {
		return existing, nil
	}
	s.commits = append(s.commits, commit)
	result := StagedWorkCommitResult{
		OperationID:  string(commit.Operation.ID),
		DeploymentID: commit.Deployment.ID,
		OutboxID:     "wox_github_test",
		Created:      true,
	}
	s.trigger[commit.Deployment.TriggerKey] = StagedWorkCommitResult{
		OperationID:  result.OperationID,
		DeploymentID: result.DeploymentID,
		OutboxID:     result.OutboxID,
		Created:      false,
	}
	return result, nil
}

func TestGitHubDispatcherUsesDurableStagedIntakeWithoutDirectQueueWrite(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "github-outbox"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.UpdateServiceSpec(context.Background(), service.ID, func(spec map[string]any) (map[string]any, error) {
		spec["github"] = map[string]any{
			"installation_id": float64(99),
			"repository_id":   float64(4242),
			"repo":            "acme/web",
			"branch":          "main",
		}
		return spec, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(time.Now)
	production, err := createDefaultProductionEnvironment(context.Background(), environments, project)
	if err != nil {
		t.Fatal(err)
	}
	directQueue := &recordingQueueStore{jobID: "must-not-enqueue-directly"}
	staged := &recordingGitHubBuildIntake{}
	dispatcher := NewGitHubWebhookDispatcher(
		resources,
		NewMemoryOperationStore(),
		directQueue,
		nil,
		func() time.Time { return time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC) },
		nil,
		environments,
	).WithDeploymentStore(NewMemoryDeploymentStore(nil)).WithStagedWorkOutbox(staged)

	body := []byte(`{
		"ref":"refs/heads/main",
		"after":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"repository":{"id":4242,"full_name":"acme/web","clone_url":"https://github.com/acme/web.git"},
		"sender":{"type":"User"},
		"commits":[{"id":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef","modified":["main.go"]}],
		"installation":{"id":99}
	}`)
	delivery := webhook.ReceivedDelivery{
		Provider:   webhook.ProviderGitHub,
		EventType:  string(github.EventPush),
		DeliveryID: "github-staged-1",
		Body:       body,
	}
	if err := dispatcher.Dispatch(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if len(directQueue.requests) != 0 || len(staged.commits) != 1 {
		t.Fatalf("direct jobs=%d staged commits=%d, want 0/1", len(directQueue.requests), len(staged.commits))
	}
	commit := staged.commits[0]
	if commit.Operation.ID == "" ||
		commit.Deployment.ID == "" ||
		commit.Deployment.OperationID != string(commit.Operation.ID) ||
		commit.Enqueue.Queue != queue.Build ||
		commit.Enqueue.ParentOperationID != string(commit.Operation.ID) ||
		commit.Deployment.EnvironmentID != string(production.ID) ||
		commit.Deployment.SHA != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" ||
		commit.SourceAuthority.CloneURL != "https://github.com/acme/web.git" {
		t.Fatalf("staged commit = %#v, want one canonical build identity", commit)
	}
	if err := dispatcher.Dispatch(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if len(staged.commits) != 1 {
		t.Fatalf("redelivery staged commits = %d, want trigger-idempotent one", len(staged.commits))
	}
}
