// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildEnvironmentResourceSummaryUsesOneCanonicalBatch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme", DisplayName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	apiService, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatal(err)
	}
	webService, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web", DisplayName: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	production, err := environments.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID,
		Slug:      "production",
		Name:      "Production",
		Tier:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}

	batch := &environmentBatchDeploymentStore{records: []DeploymentRecord{
		{
			ID:                   "dep-api-active",
			ProjectID:            string(project.ID),
			ServiceID:            string(apiService.ID),
			EnvironmentID:        string(production.ID),
			State:                deploy.DeploymentActive,
			Active:               true,
			OperationID:          "op-api-active",
			ImageDigest:          "sha256:active",
			SourceType:           "github",
			Repo:                 "acme/api",
			Ref:                  "main",
			SHA:                  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			RuntimeDesiredState:  "running",
			RuntimeObservedState: "running",
			RuntimeGeneration:    3,
			CreatedAt:            now.Add(-2 * time.Hour),
			UpdatedAt:            now.Add(-time.Hour),
		},
		{
			ID:                   "dep-api-latest",
			ProjectID:            string(project.ID),
			ServiceID:            string(apiService.ID),
			EnvironmentID:        string(production.ID),
			State:                deploy.DeploymentDeploying,
			OperationID:          "op-api-latest",
			ImageDigest:          "sha256:latest",
			SourceType:           "github",
			Repo:                 "acme/api",
			Ref:                  "feature/canvas",
			SHA:                  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			DockerfilePath:       "deploy/api.Dockerfile",
			RuntimeDesiredState:  "running",
			RuntimeObservedState: "running",
			RuntimeGeneration:    4,
			CreatedAt:            now.Add(-30 * time.Minute),
			UpdatedAt:            now.Add(-20 * time.Minute),
		},
		{
			ID:            "dep-web-active",
			ProjectID:     string(project.ID),
			ServiceID:     string(webService.ID),
			EnvironmentID: string(production.ID),
			State:         deploy.DeploymentActive,
			Active:        true,
			OperationID:   "op-web",
			CreatedAt:     now.Add(-time.Hour),
			UpdatedAt:     now.Add(-time.Hour),
		},
		{
			ID:            "dep-api-other-environment",
			ProjectID:     string(project.ID),
			ServiceID:     string(apiService.ID),
			EnvironmentID: "env_other",
			State:         deploy.DeploymentActive,
			Active:        true,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}}
	endpoints := environmentEndpointEvidenceStore{items: []PersistedEndpointEvidence{
		{
			ProjectID:     string(project.ID),
			EnvironmentID: string(production.ID),
			ServiceID:     string(apiService.ID),
			DeploymentID:  "dep-api-active",
			Host:          "api.example.test",
			URL:           "https://api.example.test",
			TLS:           true,
			EvidenceAt:    now,
			Persisted:     true,
		},
		{
			ProjectID:     string(project.ID),
			EnvironmentID: string(production.ID),
			ServiceID:     string(webService.ID),
			DeploymentID:  "dep-web-active",
			Host:          "intent-only.example.test",
			EvidenceAt:    now,
			Persisted:     false,
		},
	}}
	replicas := environmentReplicaEvidenceStore{items: []PersistedReplicaEvidence{
		{
			ProjectID:       string(project.ID),
			EnvironmentID:   string(production.ID),
			ServiceID:       string(apiService.ID),
			DeploymentID:    "dep-api-active",
			DesiredReplicas: 3,
			ReadyReplicas:   3,
			RunningReplicas: 3,
			EvidenceAt:      now,
		},
		{
			ProjectID:       string(project.ID),
			EnvironmentID:   string(production.ID),
			ServiceID:       string(apiService.ID),
			DeploymentID:    "dep-api-latest",
			DesiredReplicas: 3,
			ReadyReplicas:   1,
			RunningReplicas: 2,
			EvidenceAt:      now,
		},
	}}
	sources := environmentSourceEvidenceStore{items: []PersistedSourceEvidence{{
		ProjectID:      string(project.ID),
		ServiceID:      string(apiService.ID),
		Type:           "github",
		Repository:     "acme/api",
		Ref:            "main",
		WatchPaths:     []string{"services/api/**", "packages/shared/**"},
		RootDir:        "services/api",
		ConfigPath:     "midnight.toml",
		WaitForCI:      true,
		RequiredChecks: []string{"test", "lint"},
		AutoDeploy:     true,
		VerifiedAt:     now.Add(-time.Hour),
		UpdatedAt:      now,
		TruthSource:    "canonical-github-link",
	}}}

	summary, err := BuildEnvironmentResourceSummary(ctx, EnvironmentResourceSummaryDependencies{
		Projects:     resources,
		Environments: environments,
		Services:     resources,
		Deployments:  batch,
		Endpoints:    endpoints,
		Replicas:     replicas,
		Sources:      sources,
	}, production.ID)
	if err != nil {
		t.Fatalf("BuildEnvironmentResourceSummary() error = %v", err)
	}
	if batch.calls != 1 ||
		batch.projectID != string(project.ID) ||
		batch.environmentID != string(production.ID) {
		t.Fatalf("batch calls/scope = %d %q/%q, want one environment query", batch.calls, batch.projectID, batch.environmentID)
	}
	if summary.Project.ID != string(project.ID) || summary.Environment.ID != string(production.ID) {
		t.Fatalf("summary identities = %#v/%#v", summary.Project, summary.Environment)
	}
	if len(summary.Resources) != 2 ||
		summary.Resources[0].Service.ID != string(apiService.ID) ||
		summary.Resources[1].Service.ID != string(webService.ID) {
		t.Fatalf("resources = %#v, want slug-sorted project services", summary.Resources)
	}
	apiResource := summary.Resources[0]
	if apiResource.LatestDeployment == nil ||
		apiResource.LatestDeployment.ID != "dep-api-latest" ||
		apiResource.LatestDeployment.Status != "DEPLOYING" ||
		apiResource.LatestDeployment.Active ||
		apiResource.LatestDeployment.Operation == nil ||
		apiResource.LatestDeployment.Operation.ID != "op-api-latest" ||
		apiResource.LatestDeployment.Operation.State != orailtypes.OperationRunning ||
		apiResource.LatestDeployment.Repository != "acme/api" ||
		apiResource.LatestDeployment.Revision != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		apiResource.LatestDeployment.DockerfilePath != "deploy/api.Dockerfile" ||
		apiResource.LatestDeployment.Replicas == nil ||
		apiResource.LatestDeployment.Replicas.Health != "degraded" ||
		apiResource.LatestDeployment.TruthSource != "canonical" {
		t.Fatalf("api latest deployment = %#v", apiResource.LatestDeployment)
	}
	if apiResource.ActiveDeployment == nil ||
		apiResource.ActiveDeployment.ID != "dep-api-active" ||
		apiResource.ActiveDeployment.Replicas == nil ||
		apiResource.ActiveDeployment.Replicas.Health != "healthy" {
		t.Fatalf("api active deployment = %#v", apiResource.ActiveDeployment)
	}
	if apiResource.Source == nil ||
		apiResource.Source.Repository != "acme/api" ||
		apiResource.Source.Revision != "" ||
		apiResource.Source.AutoDeploy == nil ||
		!*apiResource.Source.AutoDeploy ||
		apiResource.Source.WaitForCI == nil ||
		!*apiResource.Source.WaitForCI ||
		len(apiResource.Source.WatchPaths) != 2 ||
		len(apiResource.Source.RequiredChecks) != 2 ||
		apiResource.Source.ConfigPath != "midnight.toml" {
		t.Fatalf("api source = %#v", apiResource.Source)
	}
	if apiResource.ActiveDeployment.Revision != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("api active revision = %q", apiResource.ActiveDeployment.Revision)
	}
	if apiResource.PrivateEndpoint.Host != "api.midnight.internal" {
		t.Fatalf("api private endpoint = %#v", apiResource.PrivateEndpoint)
	}
	if apiResource.Endpoint == nil ||
		apiResource.Endpoint.DeploymentID != "dep-api-active" ||
		apiResource.Endpoint.Host != "api.example.test" {
		t.Fatalf("api endpoint = %#v, want persisted active evidence", apiResource.Endpoint)
	}
	if summary.Resources[1].Endpoint != nil {
		t.Fatalf("web endpoint = %#v, want unpersisted intent omitted", summary.Resources[1].Endpoint)
	}
}

func TestBuildEnvironmentResourceSummaryUsesScopedLegacyFallbackOnlyWhenCanonicalMissing(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	apiService, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "api"})
	if err != nil {
		t.Fatal(err)
	}
	workerService, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	production, err := environments.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "production"})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := environments.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	batch := &environmentBatchDeploymentStore{records: []DeploymentRecord{{
		ID:            "dep-api-canonical",
		ProjectID:     string(project.ID),
		ServiceID:     string(apiService.ID),
		EnvironmentID: string(production.ID),
		State:         deploy.DeploymentFailed,
		OperationID:   "op-api",
		FailureCode:   "IMAGE_SIGNATURE_INVALID",
		CreatedAt:     now.Add(-time.Hour),
		UpdatedAt:     now.Add(-time.Hour),
	}}}
	operations := NewMemoryOperationStore()
	seedEnvironmentLegacyDeployment(
		t,
		operations,
		project,
		workerService,
		production,
		"op-worker-production",
		"dep-worker-production",
		now.Add(-30*time.Minute),
	)
	seedEnvironmentLegacyDeployment(
		t,
		operations,
		project,
		workerService,
		preview,
		"op-worker-preview",
		"dep-worker-preview",
		now,
	)

	summary, err := BuildEnvironmentResourceSummary(ctx, EnvironmentResourceSummaryDependencies{
		Projects:     resources,
		Environments: environments,
		Services:     resources,
		Deployments:  batch,
		Operations:   operations,
	}, production.ID)
	if err != nil {
		t.Fatalf("BuildEnvironmentResourceSummary() error = %v", err)
	}
	if batch.calls != 1 {
		t.Fatalf("batch calls = %d, want 1", batch.calls)
	}
	if len(summary.Resources) != 2 {
		t.Fatalf("resources = %#v", summary.Resources)
	}
	apiResource := summary.Resources[0]
	workerResource := summary.Resources[1]
	if apiResource.LatestDeployment == nil ||
		apiResource.LatestDeployment.ID != "dep-api-canonical" ||
		apiResource.LatestDeployment.TruthSource != "canonical" {
		t.Fatalf("api deployment = %#v, want canonical truth", apiResource.LatestDeployment)
	}
	if workerResource.LatestDeployment == nil ||
		workerResource.LatestDeployment.ID != "dep-worker-production" ||
		workerResource.LatestDeployment.TruthSource != "legacy-operation" ||
		!workerResource.LatestDeployment.Active {
		t.Fatalf("worker deployment = %#v, want production-scoped legacy fallback", workerResource.LatestDeployment)
	}
	if workerResource.Endpoint != nil {
		t.Fatalf("legacy route event must not become endpoint without persisted endpoint evidence: %#v", workerResource.Endpoint)
	}
}

func TestBuildEnvironmentResourceSummaryReadsCanonicalMemoryDeployments(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 13, 30, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	production, err := environments.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID,
		Slug:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	deployments := NewMemoryDeploymentStore(func() time.Time { return now })
	deployment, err := deployments.CreateDeployment(ctx, DeploymentRecord{
		ProjectID:     string(project.ID),
		ServiceID:     string(service.ID),
		EnvironmentID: string(production.ID),
		Action:        deploy.ActionDeployImage,
		OperationID:   "op_web",
	})
	if err != nil {
		t.Fatal(err)
	}

	summary, err := BuildEnvironmentResourceSummary(ctx, EnvironmentResourceSummaryDependencies{
		Projects:     resources,
		Environments: environments,
		Services:     resources,
		Deployments:  deployments,
	}, production.ID)
	if err != nil {
		t.Fatalf("BuildEnvironmentResourceSummary() error = %v", err)
	}
	if len(summary.Resources) != 1 ||
		summary.Resources[0].LatestDeployment == nil ||
		summary.Resources[0].LatestDeployment.ID != deployment.ID ||
		summary.Resources[0].LatestDeployment.Status != "QUEUED" ||
		summary.Resources[0].LatestDeployment.TruthSource != "canonical" {
		t.Fatalf("resource = %#v, want canonical memory deployment", summary.Resources)
	}
}

func TestEnvironmentResourceSummaryHandlerReturnsEnvironmentScopedContract(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"}); err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	production, err := environments.CreateEnvironment(ctx, Environment{ProjectID: project.ID, Slug: "production"})
	if err != nil {
		t.Fatal(err)
	}
	handler := EnvironmentResourceSummaryHandler(EnvironmentResourceSummaryDependencies{
		Projects:     resources,
		Environments: environments,
		Services:     resources,
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/environments/"+string(production.ID)+"/resources", nil)
	request.SetPathValue("environment_id", string(production.ID))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var summary EnvironmentResourceSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != EnvironmentResourceSummarySchema ||
		summary.Project.ID != string(project.ID) ||
		summary.Environment.ID != string(production.ID) ||
		len(summary.Resources) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Resources[0].LatestDeployment != nil || summary.Resources[0].Endpoint != nil {
		t.Fatalf("empty durable state must remain absent: %#v", summary.Resources[0])
	}
}

func TestRouterRegistersEnvironmentResourceSummary(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"}); err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(nil)
	production, err := environments.CreateEnvironment(ctx, Environment{
		ProjectID: project.ID,
		Slug:      "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore:     resources,
		ServiceStore:     resources,
		EnvironmentStore: environments,
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/environments/"+string(production.ID)+"/resources",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var summary EnvironmentResourceSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Environment.ID != string(production.ID) || len(summary.Resources) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

type environmentBatchDeploymentStore struct {
	DeploymentStore
	calls         int
	projectID     string
	environmentID string
	records       []DeploymentRecord
}

func (s *environmentBatchDeploymentStore) ListDeploymentsByEnvironment(
	_ context.Context,
	projectID, environmentID string,
) ([]DeploymentRecord, error) {
	s.calls++
	s.projectID = projectID
	s.environmentID = environmentID
	return append([]DeploymentRecord(nil), s.records...), nil
}

type environmentEndpointEvidenceStore struct {
	items []PersistedEndpointEvidence
}

func (s environmentEndpointEvidenceStore) ListPersistedEndpointsByEnvironment(
	context.Context,
	string,
	string,
) ([]PersistedEndpointEvidence, error) {
	return append([]PersistedEndpointEvidence(nil), s.items...), nil
}

type environmentReplicaEvidenceStore struct {
	items []PersistedReplicaEvidence
}

func (s environmentReplicaEvidenceStore) ListPersistedReplicasByEnvironment(
	context.Context,
	string,
	string,
) ([]PersistedReplicaEvidence, error) {
	return append([]PersistedReplicaEvidence(nil), s.items...), nil
}

type environmentSourceEvidenceStore struct {
	items []PersistedSourceEvidence
}

func (s environmentSourceEvidenceStore) ListPersistedSourcesByProject(
	context.Context,
	string,
	string,
) ([]PersistedSourceEvidence, error) {
	return append([]PersistedSourceEvidence(nil), s.items...), nil
}

func seedEnvironmentLegacyDeployment(
	t *testing.T,
	store *MemoryOperationStore,
	project Project,
	service Service,
	environment Environment,
	operationID, deploymentID string,
	at time.Time,
) {
	t.Helper()
	operation, err := store.Create(context.Background(), orailtypes.Operation{
		ID:    orailtypes.ID(operationID),
		Kind:  "deploy",
		State: orailtypes.OperationSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []OperationEvent{
		{
			OperationID: operation.ID,
			State:       operation.State,
			Message: "deploy.intake service_id=" + string(service.ID) +
				" project_id=" + string(project.ID) +
				" environment_id=" + string(environment.ID) +
				" deployment_id=" + deploymentID,
			CreatedAt: at.Add(-time.Minute),
		},
		{
			OperationID: operation.ID,
			State:       operation.State,
			Message: "deploy worker step promote succeeded service_id=" + string(service.ID) +
				" deployment_id=" + deploymentID +
				" host=" + service.Slug + ".example.test",
			CreatedAt: at,
		},
	} {
		if _, err := store.AppendEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
}
