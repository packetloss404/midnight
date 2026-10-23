// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type faultRetryDeployIntake struct {
	mu          sync.Mutex
	commits     []DeployIntakeCommit
	dispatches  int
	failFirst   bool
	deliveries  []queue.EnqueueRequest
	outboxID    string
	committedID string
}

func (s *faultRetryDeployIntake) CommitDeployIntake(_ context.Context, request DeployIntakeCommit) (DeployIntakeCommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits = append(s.commits, request)
	s.committedID = string(request.Operation.ID)
	return DeployIntakeCommitResult{
		Operation:  request.Operation,
		Deployment: request.Deployment,
		OutboxID:   firstNonEmpty(s.outboxID, "dox_test"),
	}, nil
}

func (s *faultRetryDeployIntake) DispatchDeployIntake(_ context.Context, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatches++
	if s.failFirst && s.dispatches == 1 {
		return 0, errors.New("injected crash before queue delivery")
	}
	if limit <= 0 || len(s.commits) == 0 || len(s.deliveries) > 0 {
		return 0, nil
	}
	s.deliveries = append(s.deliveries, s.commits[0].Enqueue)
	return 1, nil
}

func TestDeployIntakeDurableCommitSurvivesDeliveryFaultAndRetries(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "durable"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	durable := &faultRetryDeployIntake{failFirst: true}
	directQueue := &recordingQueueStore{jobID: "job_must_not_be_written_directly"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:     NewMemoryOperationStore(),
		DeploymentStore:    NewMemoryDeploymentStore(nil),
		IdempotencyStore:   idempotency.NewMemoryStore(),
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         directQueue,
		DeployIntakeOutbox: durable,
		Clock: func() time.Time {
			return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		},
	})

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/services/"+string(service.ID)+"/deployments",
		strings.NewReader(`{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`),
	)
	req.Header.Set("Idempotency-Key", "durable-deploy-1")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Midnight-Delivery-State"); got != "pending" {
		t.Fatalf("delivery state = %q, want pending after injected fault", got)
	}
	if len(directQueue.requests) != 0 {
		t.Fatalf("direct queue writes = %d, want 0 on durable path", len(directQueue.requests))
	}
	if len(durable.commits) != 1 {
		t.Fatalf("durable commits = %d, want 1", len(durable.commits))
	}
	commit := durable.commits[0]
	if commit.Operation.ID == "" ||
		commit.Deployment.ID == "" ||
		commit.Deployment.OperationID != string(commit.Operation.ID) ||
		commit.Enqueue.ParentOperationID != string(commit.Operation.ID) ||
		commit.Deployment.TriggerKey == "" {
		t.Fatalf("commit = %#v, want one canonical identity across operation/deployment/outbox", commit)
	}
	if commit.Operation.CreatedAt.IsZero() ||
		!commit.Operation.CreatedAt.Equal(commit.Operation.UpdatedAt) ||
		commit.Response.StatusCode != rr.Code ||
		commit.Response.Header.Get("Content-Type") != rr.Header().Get("Content-Type") ||
		commit.Response.Header.Get("Location") != rr.Header().Get("Location") ||
		commit.Response.Header.Get("X-Midnight-Delivery-State") != rr.Header().Get("X-Midnight-Delivery-State") ||
		string(commit.Response.Body) != rr.Body.String() {
		t.Fatalf("committed response = %#v, want exact timestamped HTTP response %#v", commit.Response, rr.Result())
	}
	var payload deployJobPayload
	if err := json.Unmarshal(commit.Enqueue.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OperationID != string(commit.Operation.ID) ||
		payload.Request.DeploymentID != commit.Deployment.ID ||
		payload.Candidate.DeploymentID != commit.Deployment.ID {
		t.Fatalf("payload = %#v, want canonical committed identity", payload)
	}

	// Simulate a fresh dispatcher pass after the API process died between
	// commit and delivery. The committed work is still available and is
	// delivered exactly once.
	if delivered, err := durable.DispatchDeployIntake(context.Background(), 1); err == nil || delivered != 0 {
		t.Fatalf("injected delivery fault = %d, %v; want visible failure", delivered, err)
	}
	delivered, err := durable.DispatchDeployIntake(context.Background(), 1)
	if err != nil {
		t.Fatalf("second retry dispatch: %v", err)
	}
	if delivered != 1 || len(durable.deliveries) != 1 {
		t.Fatalf("retry delivered=%d rows=%d, want one", delivered, len(durable.deliveries))
	}
	if deliveredAgain, err := durable.DispatchDeployIntake(context.Background(), 1); err != nil || deliveredAgain != 0 {
		t.Fatalf("duplicate dispatch = %d, %v; want idempotent no-op", deliveredAgain, err)
	}
}

func TestValidateDeployIntakeCommitRejectsMismatchedIdentity(t *testing.T) {
	request := DeployIntakeCommit{
		Operation: orailtypes.Operation{
			ID:    "op_test",
			Kind:  "deploy",
			State: orailtypes.OperationPending,
		},
		Deployment: DeploymentRecord{
			ID:            "dep_test",
			ProjectID:     "prj_test",
			ServiceID:     "svc_test",
			EnvironmentID: "env_test",
			Action:        "deploy_image",
			OperationID:   "op_other",
			TriggerKey:    "trigger:test",
		},
		IdempotencyKey: "idem-test",
		Enqueue: queue.EnqueueRequest{
			Queue:             queue.Deploy,
			ParentOperationID: "op_test",
			Payload:           []byte(`{}`),
		},
	}
	if err := validateDeployIntakeCommit(request); err == nil || !strings.Contains(err.Error(), "deployment operation") {
		t.Fatalf("validate error = %v, want deployment operation mismatch", err)
	}
}

func TestServiceUpDurableCommitOwnsCheckoutBeforeDelivery(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "durable-up"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	durable := &faultRetryDeployIntake{failFirst: true}
	directQueue := &recordingQueueStore{jobID: "job_must_not_be_written_directly"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:     NewMemoryOperationStore(),
		DeploymentStore:    NewMemoryDeploymentStore(nil),
		IdempotencyStore:   idempotency.NewMemoryStore(),
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         directQueue,
		DeployIntakeOutbox: durable,
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/services/"+string(service.ID)+"/up",
		bytes.NewReader(buildSampleTarball(t)),
	)
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Idempotency-Key", "durable-up-1")
	req.Header.Set("X-Midnight-Environment", "env_prod")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted || rr.Header().Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("status=%d delivery=%q body=%s, want accepted pending", rr.Code, rr.Header().Get("X-Midnight-Delivery-State"), rr.Body.String())
	}
	if len(directQueue.requests) != 0 || len(durable.commits) != 1 {
		t.Fatalf("direct jobs=%d commits=%d, want 0/1", len(directQueue.requests), len(durable.commits))
	}
	commit := durable.commits[0]
	if commit.Operation.CreatedAt.IsZero() ||
		!commit.Operation.CreatedAt.Equal(commit.Operation.UpdatedAt) ||
		commit.Response.StatusCode != rr.Code ||
		commit.Response.Header.Get("Content-Type") != rr.Header().Get("Content-Type") ||
		commit.Response.Header.Get("Location") != rr.Header().Get("Location") ||
		commit.Response.Header.Get("X-Midnight-Delivery-State") != rr.Header().Get("X-Midnight-Delivery-State") ||
		string(commit.Response.Body) != rr.Body.String() {
		t.Fatalf("committed response = %#v, want exact timestamped HTTP response %#v", commit.Response, rr.Result())
	}
	source := durable.commits[0].Deployment.Repo
	if !strings.HasPrefix(source, "local://") {
		t.Fatalf("source = %q, want local checkout", source)
	}
	checkout := strings.TrimPrefix(source, "local://")
	if _, err := os.Stat(checkout); err != nil {
		t.Fatalf("durable checkout was removed before delivery retry: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(checkout) })
	if delivered, err := durable.DispatchDeployIntake(context.Background(), 1); err == nil || delivered != 0 {
		t.Fatalf("injected retry fault = %d, %v; want visible failure", delivered, err)
	}
	if delivered, err := durable.DispatchDeployIntake(context.Background(), 1); err != nil || delivered != 1 {
		t.Fatalf("second retry dispatch = %d, %v; want one", delivered, err)
	}
}

func TestDeployOutboxRetryDelayIsBounded(t *testing.T) {
	if got := outboxRetryDelay(1); got != time.Second {
		t.Fatalf("attempt 1 delay = %s, want 1s", got)
	}
	if got := outboxRetryDelay(100); got != 256*time.Second {
		t.Fatalf("attempt 100 delay = %s, want capped exponential 256s", got)
	}
	long := errors.New(strings.Repeat("x", 600))
	if got := boundedOutboxError(long); len(got) != 512 {
		t.Fatalf("bounded error length = %d, want 512", len(got))
	}
}
