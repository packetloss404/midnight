// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/queue"
)

type recordingBuildHandoff struct {
	commits  []api.BuildDeployHandoffCommit
	recovery *api.CommittedBuildDeployHandoff
}

func (s *recordingBuildHandoff) CommitBuildDeployHandoff(_ context.Context, commit api.BuildDeployHandoffCommit) (api.StagedWorkCommitResult, error) {
	s.commits = append(s.commits, commit)
	return api.StagedWorkCommitResult{
		OperationID:  commit.OperationID,
		DeploymentID: commit.DeploymentID,
		OutboxID:     "wox_build_handoff",
		Created:      len(s.commits) == 1,
	}, nil
}

func (s *recordingBuildHandoff) GetBuildDeployHandoff(_ context.Context, deploymentID string) (api.CommittedBuildDeployHandoff, bool, error) {
	if s.recovery == nil || s.recovery.DeploymentID != deploymentID {
		return api.CommittedBuildDeployHandoff{}, false, nil
	}
	return *s.recovery, true, nil
}

func TestBuildHandoffUsesDurableOutboxWithoutDirectDeployEnqueue(t *testing.T) {
	directQueue := &buildHandoffQueueStore{}
	staged := &recordingBuildHandoff{}
	executor := &buildJobExecutor{
		queueStore:   directQueue,
		handoffStore: staged,
		redactor:     build.NewRedactor(nil),
	}
	payload := buildJobPayload{
		SchemaVersion: "midnight.dev/deploy-job/v1alpha1",
		OperationID:   "op_build_handoff",
		ServiceID:     "svc_web",
		ProjectID:     "prj_acme",
		Request: buildJobRequest{
			EnvironmentID: "env_prod",
			DeploymentID:  "dep_build_handoff",
			InstanceID:    "dep_build_handoff-r0",
		},
		Candidate: buildJobCandidate{DeploymentID: "dep_build_handoff"},
	}
	const digestRef = "registry.test/acme/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result, err := executor.enqueueDeployAfterBuild(context.Background(), payload, digestRef, api.BuildDeployHandoffCommit{
		DeploymentID: "dep_build_handoff",
		SourceSHA:    "deadbeef",
		Builder:      "railpack",
		ImageRef:     "registry.test/acme/web",
		ImageDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Durable || result.OutboxID == "" || len(directQueue.requests) != 0 || len(staged.commits) != 1 {
		t.Fatalf("result=%#v direct=%d commits=%d, want durable-only handoff", result, len(directQueue.requests), len(staged.commits))
	}
	commit := staged.commits[0]
	if commit.OperationID != payload.OperationID ||
		commit.DeploymentID != "dep_build_handoff" ||
		commit.Enqueue.Queue != queue.Deploy ||
		commit.Enqueue.ParentOperationID != payload.OperationID {
		t.Fatalf("commit = %#v, want canonical deploy enqueue identity", commit)
	}
	var decoded buildJobPayload
	if err := json.Unmarshal(commit.Enqueue.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Request.Source != nil || decoded.Candidate.Image != digestRef {
		t.Fatalf("deploy payload = %#v, want source removed and digest image", decoded)
	}
}

func TestRetriedBuildCompletesWithoutRebuildAfterDurableHandoffCommit(t *testing.T) {
	recovery := api.CommittedBuildDeployHandoff{
		OperationID:  "op_build_retry",
		DeploymentID: "dep_build_retry",
		OutboxID:     "wox_committed",
		ImageRef:     "registry.test/acme/web",
		ImageDigest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	staged := &recordingBuildHandoff{recovery: &recovery}
	executor := &buildJobExecutor{
		handoffStore: staged,
		redactor:     build.NewRedactor(nil),
	}
	payload := buildJobPayload{
		SchemaVersion: "midnight.dev/deploy-job/v1alpha1",
		OperationID:   recovery.OperationID,
		ServiceID:     "svc_web",
		ProjectID:     "prj_acme",
		Request: buildJobRequest{
			DeploymentID: recovery.DeploymentID,
			Source: &buildJobSourceIntent{
				Repo: "https://github.com/acme/web.git",
				Ref:  "deadbeef",
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), queue.Job{
		ID:                "job_build_retry",
		Queue:             queue.Build,
		ParentOperationID: recovery.OperationID,
		Payload:           body,
	}); err != nil {
		t.Fatalf("recovered Execute: %v", err)
	}
	if len(staged.commits) != 0 {
		t.Fatalf("recovered build emitted %d new handoffs, want zero", len(staged.commits))
	}
}
