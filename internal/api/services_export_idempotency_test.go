// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestEnqueueDeployServiceReplaysExactSuccessWithoutDuplicateJob(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "graphql-deploy-replay"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "job_graphql_replay"}
	api := &ServicesAPI{
		ServiceStore:     resources,
		OperationStore:   operations,
		DeploymentStore:  NewMemoryDeploymentStore(nil),
		QueueStore:       jobs,
		IdempotencyStore: idempotency.NewMemoryStore(),
		Clock: func() time.Time {
			return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
		},
	}
	request := DeployRequest{
		ServiceID:     string(service.ID),
		EnvironmentID: "env_prod",
		Image:         "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	first, serviceErr := api.EnqueueDeployService(ctx, request, "graphql-deploy-exact-replay")
	if serviceErr != nil {
		t.Fatalf("first enqueue: %v", serviceErr)
	}
	if _, err := operations.Transition(ctx, first.ID, orailtypes.OperationRunning); err != nil {
		t.Fatal(err)
	}
	replay, serviceErr := api.EnqueueDeployService(ctx, request, "graphql-deploy-exact-replay")
	if serviceErr != nil {
		t.Fatalf("replay enqueue: %v", serviceErr)
	}
	if replay != first {
		t.Fatalf("replay = %#v, want exact stored %#v", replay, first)
	}
	if len(jobs.requests) != 1 {
		t.Fatalf("queue attempts = %d, want one", len(jobs.requests))
	}
}

func TestEnqueueDeployServiceReplaysTerminalFailureWithoutRetry(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "graphql-deploy-failure"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := &recordingQueueStore{err: errors.New("queue unavailable")}
	api := &ServicesAPI{
		ServiceStore:     resources,
		OperationStore:   NewMemoryOperationStore(),
		DeploymentStore:  NewMemoryDeploymentStore(nil),
		QueueStore:       jobs,
		IdempotencyStore: idempotency.NewMemoryStore(),
		Clock:            func() time.Time { return time.Now().UTC() },
	}
	request := DeployRequest{
		ServiceID:     string(service.ID),
		EnvironmentID: "env_prod",
		Image:         "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}

	_, first := api.EnqueueDeployService(ctx, request, "graphql-deploy-failure-replay")
	if first == nil || first.Status != http.StatusInternalServerError || first.Code != "DEPLOY_JOB_ENQUEUE_FAILED" {
		t.Fatalf("first error = %#v, want terminal enqueue failure", first)
	}
	jobs.err = nil
	_, replay := api.EnqueueDeployService(ctx, request, "graphql-deploy-failure-replay")
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay error = %#v, want exact %#v", replay, first)
	}
	if len(jobs.requests) != 1 {
		t.Fatalf("queue attempts = %d, want one", len(jobs.requests))
	}
}

func TestMaterializeTemplateServiceReplaysFullSuccessWithoutDuplicateResources(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "graphql-template-replay"})
	if err != nil {
		t.Fatal(err)
	}
	keyProvider, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	api := &ServicesAPI{
		ServiceStore:     resources,
		OperationStore:   NewMemoryOperationStore(),
		VariableStore:    secrets.NewVault(keyProvider, secrets.AllowAll{}),
		IdempotencyStore: idempotency.NewMemoryStore(),
	}
	request := TemplateDeployServiceRequest{Slug: "redis", ProjectID: string(project.ID), EnvironmentID: "env_prod"}

	first, serviceErr := api.MaterializeTemplateService(ctx, request, "graphql-template-exact-replay")
	if serviceErr != nil {
		t.Fatalf("first materialize: %v", serviceErr)
	}
	if len(first.CreatedServices) != 1 || first.OperationID == "" {
		t.Fatalf("first result = %#v, want full materialization", first)
	}
	replay, serviceErr := api.MaterializeTemplateService(ctx, request, "graphql-template-exact-replay")
	if serviceErr != nil {
		t.Fatalf("replay materialize: %v", serviceErr)
	}
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay = %#v, want exact full result %#v", replay, first)
	}
	services, err := resources.ListServices(ctx, ResourceListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("created services = %d, want one", len(services))
	}
}

type failingServiceStore struct {
	ServiceStore
	err         error
	createCalls int
}

func (s *failingServiceStore) CreateService(context.Context, Service) (Service, error) {
	s.createCalls++
	return Service{}, s.err
}

func TestMaterializeTemplateServiceReplaysTerminalFailureWithoutRetry(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "graphql-template-failure"})
	if err != nil {
		t.Fatal(err)
	}
	store := &failingServiceStore{ServiceStore: resources, err: errors.New("service store unavailable")}
	api := &ServicesAPI{
		ServiceStore:     store,
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
	}
	request := TemplateDeployServiceRequest{Slug: "redis", ProjectID: string(project.ID), EnvironmentID: "env_prod"}

	_, first := api.MaterializeTemplateService(ctx, request, "graphql-template-failure-replay")
	if first == nil || first.Status != http.StatusBadRequest || first.Code != "RESOURCE_VALIDATION_FAILED" {
		t.Fatalf("first error = %#v, want terminal materialization failure", first)
	}
	store.err = nil
	_, replay := api.MaterializeTemplateService(ctx, request, "graphql-template-failure-replay")
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay error = %#v, want exact %#v", replay, first)
	}
	if store.createCalls != 1 {
		t.Fatalf("service create attempts = %d, want one", store.createCalls)
	}
}
