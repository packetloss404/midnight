// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// capturingOperationStore records the messages that would be persisted so a
// test can assert redaction happens before they reach the store.
type capturingOperationStore struct {
	events      []string
	failReasons []string
}

func (s *capturingOperationStore) Create(context.Context, orailtypes.Operation) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}

func (s *capturingOperationStore) Get(context.Context, orailtypes.ID) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}

func (s *capturingOperationStore) List(context.Context, api.OperationListFilter) ([]orailtypes.Operation, error) {
	return nil, nil
}

func (s *capturingOperationStore) Transition(context.Context, orailtypes.ID, orailtypes.OperationState) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}

func (s *capturingOperationStore) Fail(_ context.Context, _ orailtypes.ID, _ orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	s.failReasons = append(s.failReasons, reason)
	return orailtypes.Operation{}, nil
}

func (s *capturingOperationStore) AppendEvent(_ context.Context, event api.OperationEvent) (api.OperationEvent, error) {
	s.events = append(s.events, event.Message)
	return event, nil
}

func (s *capturingOperationStore) ListEvents(context.Context, orailtypes.ID) ([]api.OperationEvent, error) {
	return nil, nil
}

func TestBuildJobExecutorRedactsProgressBeforePersist(t *testing.T) {
	store := &capturingOperationStore{}
	e := &buildJobExecutor{operationStore: store, redactor: build.NewRedactor(nil)}

	e.appendEvent(context.Background(), "op-1", orailtypes.OperationRunning, "build progress: SECRET=hunter2topsecret exported")
	e.appendEvent(context.Background(), "op-1", orailtypes.OperationRunning, "build progress: buildctl --build-arg TOKEN=ghp_leakyvalue123 build")

	if len(store.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(store.events))
	}
	for _, msg := range store.events {
		if strings.Contains(msg, "hunter2topsecret") || strings.Contains(msg, "ghp_leakyvalue123") {
			t.Fatalf("persisted event leaked a secret: %q", msg)
		}
		if !strings.Contains(msg, "<redacted>") {
			t.Fatalf("persisted event was not redacted: %q", msg)
		}
	}
}

func TestBuildJobExecutorRedactsFailureReason(t *testing.T) {
	store := &capturingOperationStore{}
	e := &buildJobExecutor{operationStore: store, redactor: build.NewRedactor(nil)}

	e.failOperation(context.Background(), "op-2", errors.New("step printed API_KEY=sk-live-abcdef leaked"), "build solve")

	if len(store.failReasons) != 1 {
		t.Fatalf("expected 1 fail reason, got %d", len(store.failReasons))
	}
	reason := store.failReasons[0]
	if strings.Contains(reason, "sk-live-abcdef") {
		t.Fatalf("fail reason leaked a secret: %q", reason)
	}
	if !strings.Contains(reason, "<redacted>") {
		t.Fatalf("fail reason was not redacted: %q", reason)
	}
	// The appended failure event must also be redacted.
	for _, msg := range store.events {
		if strings.Contains(msg, "sk-live-abcdef") {
			t.Fatalf("failure event leaked a secret: %q", msg)
		}
	}
}

func TestBuildJobExecutorRedactsVariableInfrastructureCause(t *testing.T) {
	store := &capturingOperationStore{}
	e := &buildJobExecutor{operationStore: store, redactor: build.NewRedactor(nil)}
	cause := errors.New("open /etc/midnight/kek: permission denied")

	e.failOperation(
		context.Background(),
		"op-variable-infrastructure",
		&secrets.VariableInfrastructureError{Err: cause},
		"build variable resolution",
	)

	if len(store.failReasons) != 1 {
		t.Fatalf("expected 1 fail reason, got %d", len(store.failReasons))
	}
	reason := store.failReasons[0]
	if strings.Contains(reason, "/etc/midnight/kek") ||
		reason != "build variable resolution: variable resolution infrastructure is unavailable" {
		t.Fatalf("unsafe variable infrastructure reason: %q", reason)
	}
}

type buildHandoffQueueStore struct {
	requests []queue.EnqueueRequest
}

func (s *buildHandoffQueueStore) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	s.requests = append(s.requests, request)
	return queue.Job{
		ID:                "job-deploy-1",
		Queue:             request.Queue,
		State:             queue.JobQueued,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}, nil
}

func (s *buildHandoffQueueStore) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	return queue.Job{}, queue.ErrNoWork
}

func (s *buildHandoffQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *buildHandoffQueueStore) Complete(context.Context, string, string) error {
	return nil
}

func (s *buildHandoffQueueStore) Fail(context.Context, string, string, string) error {
	return nil
}

func TestBuildHandoffLeavesSharedOperationRunnableByDeployPhase(t *testing.T) {
	ctx := context.Background()
	operations := api.NewMemoryOperationStore()
	operation, err := api.CreateQueuedOperation(ctx, operations, "build")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Transition(ctx, operation.ID, orailtypes.OperationRunning); err != nil {
		t.Fatal(err)
	}
	jobs := &buildHandoffQueueStore{}
	executor := &buildJobExecutor{
		operationStore: operations,
		queueStore:     jobs,
		redactor:       build.NewRedactor(nil),
	}
	payload := buildJobPayload{
		SchemaVersion: "midnight.dev/deploy-job/v1alpha1",
		OperationID:   string(operation.ID),
		ServiceID:     "svc-web",
		ProjectID:     "prj-acme",
		Request: buildJobRequest{
			Source: &buildJobSourceIntent{
				Repo: "https://github.com/acme/web.git",
				Ref:  "0123456789abcdef",
			},
		},
	}
	const digestRef = "registry.test/acme/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := executor.enqueueDeployAfterBuild(ctx, payload, digestRef); err != nil {
		t.Fatalf("enqueueDeployAfterBuild() error = %v", err)
	}

	current, err := operations.Get(ctx, operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != orailtypes.OperationRunning {
		t.Fatalf("operation state after build handoff = %q, want RUNNING", current.State)
	}
	if len(jobs.requests) != 1 || jobs.requests[0].Queue != queue.Deploy ||
		jobs.requests[0].ParentOperationID != string(operation.ID) {
		t.Fatalf("deploy handoff = %#v, want one deploy job bound to shared operation", jobs.requests)
	}

	// This is the transition performed at the start of deploy.Executor. It
	// must remain valid after the build phase hands off the shared operation.
	if _, err := operations.Transition(ctx, operation.ID, orailtypes.OperationRunning); err != nil {
		t.Fatalf("deploy phase transition to RUNNING failed: %v", err)
	}
	if _, err := operations.Transition(ctx, operation.ID, orailtypes.OperationSucceeded); err != nil {
		t.Fatalf("deploy phase terminal transition failed: %v", err)
	}
}
