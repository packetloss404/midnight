// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type managedDeployRouteTestStore struct {
	*memoryDomainWorkflow
	selected         router.DurableDesiredRoute
	err              error
	selectedScope    router.RouteScope
	selectedSelector router.ManagedRouteSelector
}

func (s *managedDeployRouteTestStore) SelectManagedServiceRoute(_ context.Context, scope router.RouteScope, selector router.ManagedRouteSelector) (router.DurableDesiredRoute, error) {
	s.selectedScope = scope
	s.selectedSelector = selector
	if s.err != nil {
		return router.DurableDesiredRoute{}, s.err
	}
	return s.selected, nil
}

func (s *managedDeployRouteTestStore) BindManagedServiceRouteToDeployment(context.Context, string, router.RouteScope, router.ManagedRouteSelector, time.Time) (router.DurableDesiredRoute, error) {
	return s.selected, s.err
}

func generatedDeployRouteTestStore(projectID, environmentID, serviceID string) *managedDeployRouteTestStore {
	scope := router.RouteScope{ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID}
	return &managedDeployRouteTestStore{
		memoryDomainWorkflow: &memoryDomainWorkflow{},
		selected: router.DurableDesiredRoute{
			Domain: router.DurableDomain{ID: "dom_managed_generated", RouteScope: scope, Kind: router.DomainKindGenerated, NormalizedHostname: "web.apps.example.test", TLSState: router.DomainTLSDisabled},
			Route:  router.DurableHTTPRoute{ID: "rte_managed_generated", DomainID: "dom_managed_generated", RouteScope: scope, Name: "web", PathPrefix: "/", TargetPort: 8080, Public: true},
		},
	}
}

func customDeployRouteTestStore(projectID, environmentID, serviceID string) *managedDeployRouteTestStore {
	scope := router.RouteScope{ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID}
	return &managedDeployRouteTestStore{
		memoryDomainWorkflow: &memoryDomainWorkflow{},
		selected: router.DurableDesiredRoute{
			Domain: router.DurableDomain{ID: "dom_managed_custom", RouteScope: scope, Kind: router.DomainKindCustom, NormalizedHostname: "web.example.test", TLSDesired: true, TLSState: router.DomainTLSReady},
			Route:  router.DurableHTTPRoute{ID: "rte_managed_custom", DomainID: "dom_managed_custom", RouteScope: scope, Name: "web-custom", PathPrefix: "/", TargetPort: 8080, Public: true},
		},
	}
}

func deployIntakeTestFingerprint(serviceID, body string) idempotency.RequestFingerprint {
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/services/"+serviceID+"/deployments",
		nil,
	)
	return idempotencyRequestFingerprintForRoute(
		request,
		v1alphaPrefix+"/services/{service_id}/deployments",
		idempotency.BodyHashSHA256([]byte(body)),
	)
}

func TestAcceptedDeploymentTriggerIdentityIsOperationScoped(t *testing.T) {
	service := Service{
		ID:        "svc_api",
		ProjectID: "prj_acme",
	}
	request := DeployIntakeRequest{
		EnvironmentID: "env_prod",
		Image:         "registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	first := canonicalDeploymentTriggerKey(
		service,
		request,
		acceptedDeploymentIntentKey("op_first"),
	)
	second := canonicalDeploymentTriggerKey(
		service,
		request,
		acceptedDeploymentIntentKey("op_second"),
	)
	if first == second {
		t.Fatalf("accepted work trigger collision = %q", first)
	}
	clientDerived := canonicalDeploymentTriggerKey(
		service,
		request,
		idempotency.Key("same-client-key"),
	)
	if first == clientDerived || second == clientDerived {
		t.Fatalf("server work trigger aliased client-derived trigger: first=%q second=%q client=%q", first, second, clientDerived)
	}
}

func TestDeployIntakeCreatesOperationBindsIdempotencyAndQueuesJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	idem := idempotency.NewMemoryStore()
	queueStore := &recordingQueueStore{jobID: "job_deploy_1"}
	chain := audit.NewMemoryChain()
	now := time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idem,
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		AuditChain:       chain,
		Clock:            func() time.Time { return now },
	})

	requestBody := `{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(requestBody))
	req.Header.Set("Idempotency-Key", "deploy-intake-1")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.ID == "" || operation.Kind != "deploy" || operation.State != orailtypes.OperationQueued {
		t.Fatalf("operation = %#v, want queued deploy operation", operation)
	}
	reservation, err := idem.Load(
		idempotency.WithActor(context.Background(), idempotency.ActorLocalDeveloper),
		"deploy-intake-1",
		deployIntakeTestFingerprint(string(service.ID), requestBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.OperationID != string(operation.ID) || reservation.State != idempotency.StateSucceeded {
		t.Fatalf("reservation = %#v, want succeeded binding to operation", reservation)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(queueStore.requests))
	}
	enqueued := queueStore.requests[0]
	if enqueued.Queue != queue.Deploy || enqueued.PartitionKey != string(project.ID) || !enqueued.AvailableAt.Equal(now) {
		t.Fatalf("enqueue request = %#v, want deploy partitioned by project at fixed time", enqueued)
	}
	if enqueued.ParentOperationID != string(operation.ID) {
		t.Fatalf("parent operation id = %q, want %q", enqueued.ParentOperationID, operation.ID)
	}
	var payload map[string]any
	if err := json.Unmarshal(enqueued.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["schema_version"] != deployJobPayloadSchema || payload["operation_id"] != string(operation.ID) || payload["service_id"] != string(service.ID) || payload["project_id"] != string(project.ID) || payload["environment_id"] != "env_prod" {
		t.Fatalf("payload = %#v, want traceable operation/service/project metadata", payload)
	}
	events, err := operationStore.ListEvents(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].Message != "queued" ||
		events[1].Message != "deploy job enqueued: job_id=job_deploy_1 operation_id="+string(operation.ID) ||
		!strings.Contains(events[2].Message, "deploy.intake") ||
		!strings.Contains(events[2].Message, "service_id="+string(service.ID)) ||
		!strings.Contains(events[2].Message, "job_id=job_deploy_1") {
		t.Fatalf("events = %#v, want queued plus job trace", events)
	}
	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	after := rows[0].After
	if rows[0].Action != "deploy.intake.accept" || after["job_id"] != "job_deploy_1" || after["operation_id"] != string(operation.ID) || after["parent_operation_id"] != string(operation.ID) {
		t.Fatalf("audit row = %#v, want deploy intake traceability", rows[0])
	}
}

func TestDeployIntakeRejectsHostBearingCustomRouteBeforeAcceptedWork(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{jobID: "job_deploy_1"}
	deploymentStore := NewMemoryDeploymentStore(func() time.Time {
		return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	})
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		DeploymentStore:  deploymentStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		DomainStore:      &managedDeployRouteTestStore{memoryDomainWorkflow: &memoryDomainWorkflow{}, err: router.ErrDurableRouteNotFound},
		Clock:            func() time.Time { return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC) },
	})

	body := `{
		"environment_id":"env_prod",
		"image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"deployment_id":"dep_web_20260430",
		"instance_id":"inst_web_20260430_0",
		"route":{
			"name":"web",
			"group":"prj_acme_prod",
			"host":"web.example.test",
			"path":"/",
			"public":true,
			"tls":true
		},
		"previous_active":{
			"deployment_id":"dep_web_20260429",
			"instance_id":"inst_web_20260429_0"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-candidate-route")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed ||
		envelope.Error.Details["field"] != "route.host" ||
		envelope.Error.Details["required_action"] == "" {
		t.Fatalf("error envelope = %#v, want custom-domain lifecycle guidance", envelope.Error)
	}
	if len(queueStore.requests) != 0 {
		t.Fatalf("queue requests = %#v, want no work for unbound custom hostname", queueStore.requests)
	}
	if records, err := deploymentStore.ListDeploymentsByService(context.Background(), string(service.ID)); err != nil || len(records) != 0 {
		t.Fatalf("canonical deployments = %#v err=%v, want none", records, err)
	}
}

func TestDeployIntakeSelectsExistingCustomRouteForOperator(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	queueStore := &recordingQueueStore{jobID: "job_deploy_custom"}
	deploymentStore := NewMemoryDeploymentStore(func() time.Time {
		return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	})
	routeStore := customDeployRouteTestStore(string(project.ID), "env_prod", string(service.ID))
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		OperationStore:   NewMemoryOperationStore(),
		DeploymentStore:  deploymentStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		DomainStore:      routeStore,
		Clock:            func() time.Time { return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC) },
	})

	body := `{
		"environment_id":"env_prod",
		"image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"route":{"host":"web.example.test","public":true,"tls":true}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-owned-custom-route")
	req.Header.Set("Authorization", "Bearer operator-recovery-token")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if routeStore.selectedScope != (router.RouteScope{ProjectID: string(project.ID), EnvironmentID: "env_prod", ServiceID: string(service.ID)}) ||
		routeStore.selectedSelector != (router.ManagedRouteSelector{Host: "web.example.test", TLSDesired: true}) {
		t.Fatalf("selector scope=%#v selector=%#v, want exact owned custom binding", routeStore.selectedScope, routeStore.selectedSelector)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(queueStore.requests))
	}
	var payload deployJobPayload
	if err := json.Unmarshal(queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Route == nil || *payload.Route != (DeployRouteIntent{Name: "web-custom", Host: "web.example.test", Path: "/", Public: true, TLS: true}) {
		t.Fatalf("route = %#v, want canonical existing custom-route selector", payload.Route)
	}
}

func TestDeployIntakeQueuesCandidateScheduleAndRouteIntent(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{jobID: "job_deploy_1"}
	deploymentStore := NewMemoryDeploymentStore(func() time.Time {
		return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	})
	router := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		OperationStore:   operationStore,
		DeploymentStore:  deploymentStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		DomainStore:      generatedDeployRouteTestStore(string(project.ID), "env_prod", string(service.ID)),
		Clock:            func() time.Time { return time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC) },
	})

	body := `{
		"environment_id":"env_prod",
		"image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"deployment_id":"dep_web_20260430",
		"instance_id":"inst_web_20260430_0",
		"route":{"public":true},
		"previous_active":{
			"deployment_id":"dep_web_20260429",
			"instance_id":"inst_web_20260429_0"
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-candidate-route")
	req.Header.Set("Authorization", "Bearer operator-recovery-token")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(queueStore.requests))
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	var payload deployJobPayload
	if err := json.Unmarshal(queueStore.requests[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Candidate.DeploymentID == "" ||
		payload.Candidate.DeploymentID == "dep_web_20260430" ||
		payload.Candidate.InstanceID != payload.Candidate.DeploymentID+"-r0" ||
		payload.Candidate.ProjectID != string(project.ID) ||
		payload.Candidate.EnvironmentID != "env_prod" ||
		payload.Candidate.ServiceID != string(service.ID) ||
		payload.Candidate.Image != "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		payload.Candidate.IdempotencyKey != string(operation.ID) {
		t.Fatalf("candidate = %#v, want schedule intent from request/service metadata", payload.Candidate)
	}
	if payload.Route == nil || *payload.Route != (DeployRouteIntent{Name: "web", Path: "/", Public: true}) {
		t.Fatalf("route = %#v, want exact hostless generated-route selector", payload.Route)
	}
	if payload.PreviousActive != nil {
		t.Fatalf("previous_active = %#v, want server-resolved empty active pointer", payload.PreviousActive)
	}
	if payload.Request.DeploymentID != payload.Candidate.DeploymentID ||
		payload.Request.Route == nil ||
		!payload.Request.Route.Public ||
		payload.Request.Route.Host != "" {
		t.Fatalf("request = %#v, want canonical identity and hostless route signal", payload.Request)
	}
	canonical, err := deploymentStore.GetDeploymentByOperation(context.Background(), string(operation.ID))
	if err != nil {
		t.Fatal(err)
	}
	if canonical.ID != payload.Candidate.DeploymentID ||
		canonical.State != deploy.DeploymentQueued ||
		canonical.ProjectID != string(project.ID) ||
		canonical.EnvironmentID != "env_prod" ||
		canonical.OperationID != string(operation.ID) ||
		canonical.TriggerKey == "" {
		t.Fatalf("canonical deployment = %#v, want queued truth before worker handoff", canonical)
	}
}

func TestDeploymentResourceAPIListsGetsAndFindsLatestActive(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	otherService, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	first := seedDeploymentOperation(t, operationStore, service, "op-dep-1", "dep_web_1", "inst_web_1", orailtypes.OperationSucceeded, true, time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	second := seedDeploymentOperation(t, operationStore, service, "op-dep-2", "dep_web_2", "inst_web_2", orailtypes.OperationRunning, false, time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC))
	seedDeploymentOperation(t, operationStore, otherService, "op-other", "dep_other_1", "inst_other_1", orailtypes.OperationSucceeded, true, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC))
	router := NewRouterWithDataStack(DataStack{
		OperationStore: operationStore,
		ProjectStore:   resources,
		ServiceStore:   resources,
	})

	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/deployments", nil))
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want 200", listRR.Code, listRR.Body.String())
	}
	var page DeploymentResourcePage
	if err := json.Unmarshal(listRR.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	listed := page.Items
	if len(listed) != 2 || listed[0].ID != "dep_web_2" || listed[1].ID != "dep_web_1" {
		t.Fatalf("deployments = %#v, want only service deployments newest first", listed)
	}
	if !listed[1].Active || listed[1].Status != "active" || listed[1].Route != "web.example.test" || listed[1].OperationID != string(first.ID) {
		t.Fatalf("second deployment = %#v, want promoted active deployment", listed[1])
	}
	if listed[0].Active || listed[0].Status != "deploying" || listed[0].OperationID != string(second.ID) {
		t.Fatalf("first deployment = %#v, want running inactive deployment", listed[0])
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/deployments/dep_web_2", nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s, want 200", getRR.Code, getRR.Body.String())
	}
	var got DeploymentResource
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "dep_web_2" || got.Status != "deploying" || got.ServiceID != string(service.ID) {
		t.Fatalf("deployment = %#v, want dep_web_2", got)
	}

	activeRR := httptest.NewRecorder()
	router.ServeHTTP(activeRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/deployments/latest-active", nil))
	if activeRR.Code != http.StatusOK {
		t.Fatalf("latest active status = %d body=%s, want 200", activeRR.Code, activeRR.Body.String())
	}
	var active DeploymentResource
	if err := json.Unmarshal(activeRR.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.ID != "dep_web_1" || !active.Active || active.Route != "web.example.test" {
		t.Fatalf("active deployment = %#v, want dep_web_1", active)
	}
}

func TestDeploymentResourceAPILatestActiveReturns404WhenNoPromotedRuntimeState(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	seedDeploymentOperation(t, operationStore, service, "op-dep-1", "dep_web_1", "inst_web_1", orailtypes.OperationSucceeded, false, time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	router := NewRouterWithDataStack(DataStack{
		OperationStore: operationStore,
		ProjectStore:   resources,
		ServiceStore:   resources,
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/deployments/latest-active", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceNotFound {
		t.Fatalf("code = %q, want %q", envelope.Error.Code, failurecodes.ResourceNotFound)
	}
}

func TestDeploymentResourceAPISurfacesFailureCodeAndReason(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	operation, err := operationStore.Create(context.Background(), orailtypes.Operation{ID: "op-dep-failed", Kind: "deploy", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.AppendEvent(context.Background(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message: "deploy.intake service_id=" + string(service.ID) +
			" deployment_id=dep_failed instance_id=inst_failed image=registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CreatedAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.Fail(context.Background(), operation.ID, orailtypes.FailureCode(failurecodes.RoutePublicationFailed), "promote failed: route publisher rejected config"); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore: operationStore,
		ProjectStore:   resources,
		ServiceStore:   resources,
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/"+string(service.ID)+"/deployments", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var page DeploymentResourcePage
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	listed := page.Items
	if len(listed) != 1 ||
		listed[0].Status != "failed" ||
		listed[0].FailureCode != orailtypes.FailureCode(failurecodes.RoutePublicationFailed) ||
		listed[0].FailureReason != "promote failed: route publisher rejected config" {
		t.Fatalf("deployment = %#v, want failed deployment with route publication reason", listed)
	}
}

func TestDeployIntakeReplaysAcceptedOperationWithoutDuplicateJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{jobID: "job_deploy_1"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	doPost := func() (orailtypes.Operation, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
		req.Header.Set("Idempotency-Key", "deploy-intake-replay")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		var operation orailtypes.Operation
		if rr.Code == http.StatusAccepted {
			if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
				t.Fatal(err)
			}
		}
		return operation, rr
	}

	first, firstRR := doPost()
	if firstRR.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s, want 202", firstRR.Code, firstRR.Body.String())
	}
	firstBody := firstRR.Body.String()
	second, secondRR := doPost()
	if secondRR.Code != http.StatusAccepted {
		t.Fatalf("second status = %d body=%s, want 202", secondRR.Code, secondRR.Body.String())
	}
	if second != first ||
		secondRR.Header().Get("Idempotent-Replayed") != "true" ||
		secondRR.Header().Get("Content-Type") != firstRR.Header().Get("Content-Type") ||
		secondRR.Header().Get("Location") != firstRR.Header().Get("Location") ||
		secondRR.Body.String() != firstBody {
		t.Fatalf("replay = %#v/%q/%q/%q/%s, want exact %#v/%q/%q/%s", second, secondRR.Header().Get("Idempotent-Replayed"), secondRR.Header().Get("Content-Type"), secondRR.Header().Get("Location"), secondRR.Body.String(), first, firstRR.Header().Get("Content-Type"), firstRR.Header().Get("Location"), firstBody)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want no duplicate enqueue on replay", len(queueStore.requests))
	}
}

func seedDeploymentOperation(t *testing.T, store OperationStore, service Service, operationID string, deploymentID string, instanceID string, state orailtypes.OperationState, promoted bool, at time.Time) orailtypes.Operation {
	t.Helper()
	operation, err := store.Create(context.Background(), orailtypes.Operation{ID: orailtypes.ID(operationID), Kind: "deploy", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(context.Background(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "queued", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(context.Background(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message: "deploy.intake service_id=" + string(service.ID) +
			" project_id=" + string(service.ProjectID) +
			" queue=deploy job_id=job_" + operationID +
			" job_state=queued deployment_id=" + deploymentID +
			" instance_id=" + instanceID +
			" environment_id=env_prod image=registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa route_intent=web.example.test",
		CreatedAt: at.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if state == orailtypes.OperationRunning || state == orailtypes.OperationSucceeded {
		operation, err = store.Transition(context.Background(), operation.ID, orailtypes.OperationRunning)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendEvent(context.Background(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "running", CreatedAt: at.Add(2 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if promoted {
		if _, err := store.AppendEvent(context.Background(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "deploy worker step promote succeeded host=web.example.test", CreatedAt: at.Add(3 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if state == orailtypes.OperationSucceeded {
		operation, err = store.Transition(context.Background(), operation.ID, orailtypes.OperationSucceeded)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendEvent(context.Background(), OperationEvent{OperationID: operation.ID, State: operation.State, Message: "succeeded", CreatedAt: at.Add(4 * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	return operation
}

func TestDeployJobRequestCarriesParentOperationIDAndPayloadFallback(t *testing.T) {
	service := Service{ID: "svc_1", ProjectID: "prj_1"}
	enqueuedAt := time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC)
	payload, err := buildDeployJobPayload("op_1", Service{ID: "svc_1", ProjectID: "prj_1"}, DeployIntakeRequest{EnvironmentID: "env_1"}, time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	request := buildDeployEnqueueRequest("op_1", service, enqueuedAt, payload)
	if request.ParentOperationID != "op_1" {
		t.Fatalf("parent operation id = %q, want op_1", request.ParentOperationID)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got["operation_id"] != "op_1" || got["service_id"] != "svc_1" || got["project_id"] != "prj_1" {
		t.Fatalf("payload = %#v, want operation linkage in payload", got)
	}
}

func TestDeployIntakeMarksOperationFailedWhenEnqueueFails(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	idem := idempotency.NewMemoryStore()
	queueStore := &recordingQueueStore{err: errors.New("queue down")}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idem,
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	requestBody := `{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(requestBody))
	req.Header.Set("Idempotency-Key", "deploy-intake-enqueue-fails")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.DeployJobEnqueueFailed || envelope.Error.Details["operation_id"] == nil {
		t.Fatalf("error envelope = %#v, want deploy job enqueue failure with operation id", envelope.Error)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 ||
		operations[0].State != orailtypes.OperationFailed ||
		operations[0].Code != orailtypes.FailureCode(failurecodes.DeployJobEnqueueFailed) ||
		operations[0].Reason != "deploy job enqueue failed" {
		t.Fatalf("operations = %#v, want failed deploy operation", operations)
	}
	events, err := operationStore.ListEvents(context.Background(), operations[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Message != "deploy job enqueue failed" {
		t.Fatalf("events = %#v, want enqueue failure trace", events)
	}
	reservation, err := idem.Load(
		idempotency.WithActor(context.Background(), idempotency.ActorLocalDeveloper),
		"deploy-intake-enqueue-fails",
		deployIntakeTestFingerprint(string(service.ID), requestBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.State != idempotency.StateFailed || reservation.OperationID != string(operations[0].ID) {
		t.Fatalf("reservation = %#v, want failed binding to operation", reservation)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want one failed enqueue attempt", len(queueStore.requests))
	}

	replayReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	replayReq.Header.Set("Idempotency-Key", "deploy-intake-enqueue-fails")
	replayRR := httptest.NewRecorder()
	router.ServeHTTP(replayRR, replayReq)
	if replayRR.Code != rr.Code ||
		replayRR.Header().Get("Idempotent-Replayed") != "true" ||
		replayRR.Header().Get("Content-Type") != rr.Header().Get("Content-Type") ||
		replayRR.Body.String() != rr.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%s, want exact stored %d/%q/%s", replayRR.Code, replayRR.Header().Get("Idempotent-Replayed"), replayRR.Header().Get("Content-Type"), replayRR.Body.String(), rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	var replayed ErrorResponse
	if err := json.Unmarshal(replayRR.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.Error.Code != failurecodes.DeployJobEnqueueFailed || replayed.Status != http.StatusInternalServerError {
		t.Fatalf("replayed problem = %#v, want stored enqueue failure", replayed)
	}
}

func TestDeployIntakeServiceNotFoundIsTerminalWithoutOperationOrJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	operationStore := NewMemoryOperationStore()
	idem := idempotency.NewMemoryStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idem,
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/svc_missing/deployments", strings.NewReader(`{"source_ref":"refs/heads/main"}`))
		req.Header.Set("Idempotency-Key", "deploy-intake-missing-service")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusNotFound {
		t.Fatalf("first status = %d body=%s, want 404", first.Code, first.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(first.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceNotFound || envelope.Error.Details["service_id"] != "svc_missing" {
		t.Fatalf("error envelope = %#v, want service not found", envelope.Error)
	}
	second := doPost()
	if second.Code != http.StatusNotFound || second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status/header = %d/%q body=%s, want 404 replay", second.Code, second.Header().Get("Idempotent-Replayed"), second.Body.String())
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("operations/queue = %#v/%#v, want no operation or job for missing service", operations, queueStore.requests)
	}
	reservation, err := idem.Load(
		idempotency.WithActor(context.Background(), idempotency.ActorLocalDeveloper),
		"deploy-intake-missing-service",
		deployIntakeTestFingerprint("svc_missing", `{"source_ref":"refs/heads/main"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.State != idempotency.StateFailed || reservation.OperationID != "" || reservation.Response == nil || reservation.Response.StatusCode != http.StatusNotFound {
		t.Fatalf("reservation = %#v, want terminal failed service-not-found response", reservation)
	}
}

func TestDeployIntakeRejectsUnpinnedImageWithoutOperationOrJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	idem := idempotency.NewMemoryStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idem,
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	requestBody := `{"image":"registry.example/web:latest"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(requestBody))
	req.Header.Set("Idempotency-Key", "deploy-intake-unpinned-image")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed || envelope.Error.Details["field"] != "image" {
		t.Fatalf("error envelope = %#v, want image validation failure", envelope.Error)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("operations/queue = %#v/%#v, want no operation or job for invalid deploy request", operations, queueStore.requests)
	}
	reservation, err := idem.Load(
		idempotency.WithActor(context.Background(), idempotency.ActorLocalDeveloper),
		"deploy-intake-unpinned-image",
		deployIntakeTestFingerprint(string(service.ID), requestBody),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.State != idempotency.StateFailed || reservation.OperationID != "" || reservation.Response == nil || reservation.Response.StatusCode != http.StatusBadRequest {
		t.Fatalf("reservation = %#v, want terminal failed validation response", reservation)
	}
}

func TestDeployIntakeRejectsMalformedDigestImageWithoutOperationOrJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"image":"registry.example/web@sha256:not-a-digest"}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-bad-digest")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed || envelope.Error.Details["field"] != "image" {
		t.Fatalf("error envelope = %#v, want image validation failure", envelope.Error)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("operations/queue = %#v/%#v, want no operation or job for invalid digest", operations, queueStore.requests)
	}
}

func TestDeployIntakeRejectsMissingArtifactWithoutOperationOrJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"environment_id":"env_prod"}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-no-artifact")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed || envelope.Error.Details["field"] != "image" {
		t.Fatalf("error envelope = %#v, want missing artifact validation failure", envelope.Error)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("operations/queue = %#v/%#v, want no operation or job for missing artifact", operations, queueStore.requests)
	}
}

func TestDeployIntakeRejectsSourceRefUntilBuildIntegrationExists(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"source_ref":"refs/heads/main"}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-source-only")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.DeploySourceRefUnsupported ||
		envelope.Error.Details["field"] != "source_ref" ||
		envelope.Error.Details["required_capability"] != "source-build-to-digest-image" ||
		envelope.Error.Details["accepted_alternative"] != "pass a digest-pinned image without source_ref" {
		t.Fatalf("error envelope = %#v, want explicit source_ref blocked response", envelope.Error)
	}
	operations, err := operationStore.List(context.Background(), OperationListFilter{Kind: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 || len(queueStore.requests) != 0 {
		t.Fatalf("operations/queue = %#v/%#v, want no operation or job for source-only deploy", operations, queueStore.requests)
	}
}

func TestDeployIntakeRejectsSourceRefEvenWithImageUntilBuildIntegrationExists(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"source_ref":"refs/heads/main","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-source-plus-image")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.DeploySourceRefUnsupported ||
		envelope.Error.Details["field"] != "source_ref" ||
		envelope.Error.Details["required_capability"] != "source-build-to-digest-image" {
		t.Fatalf("error envelope = %#v, want explicit source_ref blocked response", envelope.Error)
	}
	if len(queueStore.requests) != 0 {
		t.Fatalf("queue requests = %#v, want no job for source-ref deploy", queueStore.requests)
	}
}

func TestDeployIntakeInvalidJSONReplaysStoredFailure(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{},
	})

	doPost := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"image":`))
		req.Header.Set("Idempotency-Key", "deploy-intake-invalid-json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}

	first := doPost()
	if first.Code != http.StatusBadRequest {
		t.Fatalf("first status = %d body=%s, want 400", first.Code, first.Body.String())
	}
	second := doPost()
	if second.Code != http.StatusBadRequest || second.Header().Get("Idempotent-Replayed") != "true" || second.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d/%q/%s, want verbatim 400 replay %s", second.Code, second.Header().Get("Idempotent-Replayed"), second.Body.String(), first.Body.String())
	}
}

func TestDeployIntakeAcceptsStructuredSourceAndEnqueuesBuildJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(context.Background(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(context.Background(), Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	idem := idempotency.NewMemoryStore()
	queueStore := &recordingQueueStore{jobID: "job_build_1"}
	chain := audit.NewMemoryChain()
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idem,
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
		AuditChain:       chain,
		Clock:            func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) },
	})

	body := `{
		"environment_id":"env_prod",
		"source":{"repo":"https://github.com/midnight/sample.git","ref":"main","subpath":"apps/web"},
		"deployment_id":"dep_web_20260517",
		"instance_id":"inst_web_20260517_0"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-source-build")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", rr.Code, rr.Body.String())
	}
	var operation orailtypes.Operation
	if err := json.Unmarshal(rr.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Kind != "build" || operation.State != orailtypes.OperationQueued {
		t.Fatalf("operation = %#v, want queued build operation", operation)
	}
	if len(queueStore.requests) != 1 {
		t.Fatalf("queue requests = %d, want 1", len(queueStore.requests))
	}
	enqueued := queueStore.requests[0]
	if enqueued.Queue != queue.Build {
		t.Fatalf("queue = %q, want %q", enqueued.Queue, queue.Build)
	}
	if enqueued.ParentOperationID != string(operation.ID) || enqueued.PartitionKey != string(project.ID) {
		t.Fatalf("enqueue = %#v, want partition by project and parent op", enqueued)
	}
	var payload map[string]any
	if err := json.Unmarshal(enqueued.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	requestEcho, ok := payload["request"].(map[string]any)
	if !ok {
		t.Fatalf("payload.request not an object: %#v", payload)
	}
	source, ok := requestEcho["source"].(map[string]any)
	if !ok {
		t.Fatalf("payload.request.source not an object: %#v", requestEcho)
	}
	if source["repo"] != "https://github.com/midnight/sample.git" || source["ref"] != "main" || source["subpath"] != "apps/web" {
		t.Fatalf("source = %#v, want repo/ref/subpath", source)
	}
	rows, err := chain.Read(context.Background(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	after := rows[0].After
	if after["queue"] != "build" || after["operation_kind"] != "build" {
		t.Fatalf("audit row = %#v, want build queue+kind", after)
	}
}

func TestDeployIntakeRejectsStructuredSourceMissingRepoOrRef(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"source":{"repo":"https://github.com/midnight/sample.git"}}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-source-missing-ref")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.DeploySourceRefUnsupported || envelope.Error.Details["field"] != "source" {
		t.Fatalf("error envelope = %#v, want source field error", envelope.Error)
	}
	if len(queueStore.requests) != 0 {
		t.Fatalf("queue requests = %#v, want none on validation failure", queueStore.requests)
	}
}

func TestDeployIntakeRejectsCredentialBearingSourceURLBeforePersistence(t *testing.T) {
	for name, repo := range map[string]string{
		"userinfo": "https://user:supersecret@github.com/acme/private.git",
		"query":    "https://github.com/acme/private.git?token=supersecret",
		"fragment": "https://github.com/acme/private.git#supersecret",
	} {
		t.Run(name, func(t *testing.T) {
			code, _, _, ok := validateDeployIntakeRequest(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source: &DeploySourceIntent{
					Provider:       "github",
					Repository:     "acme/private",
					InstallationID: 42,
					RepositoryID:   4242,
					Repo:           repo,
					Ref:            "main",
				},
			})
			if ok || code != failurecodes.ResourceValidationFailed {
				t.Fatalf("validateDeployIntakeRequest() = ok %v code %q", ok, code)
			}
		})
	}
}

func TestDeployIntakeRejectsWhitespaceWrappedSourceIdentity(t *testing.T) {
	for name, source := range map[string]*DeploySourceIntent{
		"repo": {
			Repo: " https://github.com/acme/public.git ",
			Ref:  "main",
		},
		"ref": {
			Repo: "https://github.com/acme/public.git",
			Ref:  " main ",
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source:        source,
			})
			wantField := "source." + name
			if ok || code != failurecodes.ResourceValidationFailed || details["field"] != wantField {
				t.Fatalf("validate = ok %v code %q details=%#v, want canonical source rejection", ok, code, details)
			}
		})
	}
}

func TestDeployIntakeRejectsNonCanonicalProviderSpelling(t *testing.T) {
	for _, provider := range []string{"Git", " git ", "GITHUB", "github "} {
		t.Run(provider, func(t *testing.T) {
			source := &DeploySourceIntent{
				Provider: provider,
				Repo:     "https://github.com/acme/public.git",
				Ref:      "main",
			}
			if strings.EqualFold(strings.TrimSpace(provider), "github") {
				source.Repository = "acme/public"
				source.InstallationID = 42
			}
			code, _, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source:        source,
			})
			if ok || code != failurecodes.ResourceValidationFailed || details["field"] != "source.provider" {
				t.Fatalf("provider %q = ok %v code %q details=%#v, want canonical rejection", provider, ok, code, details)
			}
		})
	}
}

func TestDeployIntakeRejectsReservedInternalSourceSchemesBeforeAcceptedWork(t *testing.T) {
	tests := map[string]string{
		"source artifact mixed case": " \tSoUrCeArTiFaCt://sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa ",
		"local mixed case":           " LoCaL:///var/lib/midnight/uploads/owned ",
		"whitespace before colon":    "sourceartifact \t://sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"percent encoded delimiter":  "LOCAL%3a%2f%2f%2fvar%2flib%2fmidnight%2fuploads%2fowned",
	}
	for name, repo := range tests {
		t.Run(name, func(t *testing.T) {
			resources := NewMemoryResourceStore()
			project, err := resources.CreateProject(t.Context(), Project{WorkspaceID: DefaultWorkspaceID, Slug: "reserved"})
			if err != nil {
				t.Fatal(err)
			}
			service, err := resources.CreateService(t.Context(), Service{ProjectID: project.ID, Slug: "web"})
			if err != nil {
				t.Fatal(err)
			}
			operations := NewMemoryOperationStore()
			durable := &faultRetryDeployIntake{}
			directQueue := &recordingQueueStore{jobID: "must-not-enqueue"}
			router := NewRouterWithDataStack(DataStack{
				OperationStore:     operations,
				DeploymentStore:    NewMemoryDeploymentStore(nil),
				IdempotencyStore:   idempotency.NewMemoryStore(),
				ProjectStore:       resources,
				ServiceStore:       resources,
				QueueStore:         directQueue,
				DeployIntakeOutbox: durable,
			})
			body, err := json.Marshal(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source: &DeploySourceIntent{
					Repo: repo,
					Ref:  "main",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1alpha/services/"+string(service.ID)+"/deployments",
				bytes.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "reserved-source-"+strings.ReplaceAll(name, " ", "-"))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", response.Code, response.Body.String())
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != failurecodes.ResourceValidationFailed ||
				envelope.Error.Details["field"] != "source.repo" {
				t.Fatalf("error=%#v, want reserved source.repo validation failure", envelope.Error)
			}
			listed, err := operations.List(t.Context(), OperationListFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != 0 || len(durable.commits) != 0 || len(directQueue.requests) != 0 {
				t.Fatalf(
					"operations=%d durable commits=%d direct queue=%d, want zero accepted work",
					len(listed),
					len(durable.commits),
					len(directQueue.requests),
				)
			}
		})
	}
}

func TestDeployIntakeGenericGitRequiresPublicHostHTTPSRepository(t *testing.T) {
	rejected := map[string]string{
		"file transport":              "FiLe:///var/lib/midnight/checkout",
		"encoded file transport":      "fIlE%3a%2f%2f%2fvar%2flib%2fmidnight%2fcheckout",
		"ssh transport whitespace":    " \tSsH ://git@example.com/acme/web.git ",
		"git ssh transport":           "git+ssh://git@example.com/acme/web.git",
		"git ext transport":           "ext::sh -c touch /tmp/pwned",
		"fd transport":                "fd://3/acme/web.git",
		"unix transport":              "unix:///run/git.sock",
		"absolute path":               "/var/lib/midnight/checkout",
		"relative path":               "./checkout",
		"scp path":                    "git@example.com:acme/web.git",
		"ipv4 literal":                "https://127.0.0.1/acme/web.git",
		"ipv6 literal":                "https://[::1]/acme/web.git",
		"integer ipv4":                "https://2130706433/acme/web.git",
		"hex ipv4":                    "https://0x7f.0x0.0x0.0x1/acme/web.git",
		"localhost case":              "https://LOCALHOST/acme/web.git",
		"localhost trailing root dot": "https://localhost./acme/web.git",
		"internal suffix":             "https://metadata.google.internal/acme/web.git",
		"local suffix":                "https://git.repo.local/acme/web.git",
		"service suffix":              "https://git.default.svc/acme/web.git",
		"special use suffix":          "https://git.example.test/acme/web.git",
		"single label":                "https://gitserver/acme/web.git",
		"explicit port":               "https://github.com:8443/acme/web.git",
		"empty repository path":       "https://github.com/",
		"credentials":                 "https://user:secret@github.com/acme/web.git",
		"query":                       "https://github.com/acme/web.git?token=secret",
		"fragment":                    "https://github.com/acme/web.git#secret",
		"surrounding whitespace":      " https://github.com/acme/web.git ",
	}
	for name, repo := range rejected {
		t.Run(name, func(t *testing.T) {
			code, _, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source: &DeploySourceIntent{
					Provider: "git",
					Repo:     repo,
					Ref:      "main",
				},
			})
			if ok || code != failurecodes.ResourceValidationFailed {
				t.Fatalf("validate repo %q = ok %v code %q details=%#v, want rejection", repo, ok, code, details)
			}
			if details["field"] != "source.repo" {
				t.Fatalf("details=%#v, want source.repo", details)
			}
		})
	}

	for name, repo := range map[string]string{
		"ordinary git":    "https://gitlab.com/acme/web.git",
		"case normalized": "HTTPS://GitHub.COM/acme/web.git",
	} {
		t.Run("accept "+name, func(t *testing.T) {
			code, message, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
				EnvironmentID: "env_prod",
				Source: &DeploySourceIntent{
					Provider: "git",
					Repo:     repo,
					Ref:      "main",
				},
			})
			if !ok {
				t.Fatalf("validate repo %q = code %q message %q details=%#v", repo, code, message, details)
			}
		})
	}
}

func TestDeployIntakeRejectsInternalLegacyLocalProvider(t *testing.T) {
	code, _, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
		EnvironmentID: "env_prod",
		Source: &DeploySourceIntent{
			Provider: DeploySourceProviderLegacyLocal,
			Repo:     "local:///var/lib/midnight/uploads/midnight-up-src-owned",
			Ref:      "local-tarball",
		},
	})
	if ok || code != failurecodes.ResourceValidationFailed {
		t.Fatalf("validate internal provider = ok %v code %q details=%#v, want rejection", ok, code, details)
	}
	if details["field"] != "source.repo" {
		t.Fatalf("details=%#v, want source.repo reserved-scheme rejection", details)
	}
}

func TestDeployIntakeRejectsCallerSuppliedGitHubSourceDescriptor(t *testing.T) {
	code, message, details, ok := validateDeployIntakeRequest(DeployIntakeRequest{
		EnvironmentID: "env_prod",
		Source: &DeploySourceIntent{
			Provider:       "github",
			Repository:     "acme/private",
			InstallationID: 42,
			RepositoryID:   4242,
			Repo:           "https://github.com/acme/private.git",
			Ref:            "deadbeef",
			RootDirectory:  "apps/web",
			ConfigPath:     "deploy/midnight.toml",
		},
	})
	if ok ||
		code != failurecodes.ResourceValidationFailed ||
		details["field"] != "source.provider" ||
		details["provider"] != "github" {
		t.Fatalf(
			"validateDeployIntakeRequest() = ok %v code %q message %q details %#v, want caller-supplied GitHub provider rejection",
			ok,
			code,
			message,
			details,
		)
	}
}

func TestDeployIntakeRejectsForeignGitHubCoordinatesBeforeDurableWork(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(t.Context(), Project{
		WorkspaceID: "ws_automation",
		Slug:        "automation",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(t.Context(), Service{
		ProjectID: project.ID,
		Slug:      "web",
	})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	durable := &faultRetryDeployIntake{}
	directQueue := &recordingQueueStore{jobID: "must-not-enqueue"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:     operations,
		DeploymentStore:    NewMemoryDeploymentStore(nil),
		IdempotencyStore:   idempotency.NewMemoryStore(),
		ProjectStore:       resources,
		ServiceStore:       resources,
		QueueStore:         directQueue,
		DeployIntakeOutbox: durable,
	})
	body := `{
		"environment_id":"env_prod",
		"source":{
			"provider":"github",
			"repository":"other-tenant/private",
			"installation_id":9001,
			"repository_id":424242,
			"repo":"https://github.com/other-tenant/private.git",
			"ref":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		}
	}`
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/services/"+string(service.ID)+"/deployments",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "reject-foreign-github-coordinates")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", response.Code, response.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed ||
		envelope.Error.Details["field"] != "source.provider" ||
		envelope.Error.Details["provider"] != "github" {
		t.Fatalf("error=%#v, want generic GitHub provider rejection", envelope.Error)
	}
	listed, err := operations.List(t.Context(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 || len(durable.commits) != 0 || len(directQueue.requests) != 0 {
		t.Fatalf(
			"operations=%d durable commits=%d direct queue=%d, want zero privileged work",
			len(listed),
			len(durable.commits),
			len(directQueue.requests),
		)
	}
}

func TestDeployIntakeRejectsBothSourceAndImage(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   NewMemoryOperationStore(),
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       &recordingQueueStore{},
	})

	body := `{"source":{"repo":"https://x","ref":"main"},"image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "deploy-intake-source-plus-image-struct")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("code = %q, want resource validation failure", envelope.Error.Code)
	}
}

func TestDeployIntakeRejectsMissingEnvironmentWithoutOperationOrJob(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	jobs := &recordingQueueStore{jobID: "must-not-enqueue"}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operations,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       jobs,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	req.Header.Set("Idempotency-Key", "deploy-missing-environment")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed || envelope.Error.Details["field"] != "environment_id" {
		t.Fatalf("error = %#v, want environment_id validation failure", envelope.Error)
	}
	listed, err := operations.List(context.Background(), OperationListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 || len(jobs.requests) != 0 {
		t.Fatalf("operations=%d jobs=%d, want no side effects", len(listed), len(jobs.requests))
	}
}

func TestDeployIntakeRejectsTLSRouteWithoutPublicOptIn(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	operationStore := NewMemoryOperationStore()
	queueStore := &recordingQueueStore{}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:   operationStore,
		IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore:     resources,
		ServiceStore:     resources,
		QueueStore:       queueStore,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", strings.NewReader(`{"environment_id":"env_prod","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","route":{"path":"/","tls":true}}`))
	req.Header.Set("Idempotency-Key", "deploy-intake-tls-no-host")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.ResourceValidationFailed || envelope.Error.Details["field"] != "route.public" {
		t.Fatalf("error envelope = %#v, want explicit public opt-in validation failure", envelope.Error)
	}
	if len(queueStore.requests) != 0 {
		t.Fatalf("queue requests = %#v, want no job for invalid route", queueStore.requests)
	}
}

type recordingQueueStore struct {
	requests []queue.EnqueueRequest
	jobID    string
	err      error
}

func (s *recordingQueueStore) Enqueue(_ context.Context, request queue.EnqueueRequest) (queue.Job, error) {
	s.requests = append(s.requests, request)
	if s.err != nil {
		return queue.Job{}, s.err
	}
	id := s.jobID
	if id == "" {
		id = "job_test"
	}
	return queue.Job{
		ID:                id,
		Queue:             request.Queue,
		State:             queue.JobQueued,
		Priority:          request.Priority,
		PartitionKey:      request.PartitionKey,
		ParentOperationID: request.ParentOperationID,
		AvailableAt:       request.AvailableAt,
		Payload:           append([]byte(nil), request.Payload...),
	}, nil
}

func (s *recordingQueueStore) Lease(context.Context, queue.LeaseRequest) (queue.Job, error) {
	return queue.Job{}, queue.ErrNoWork
}

func (s *recordingQueueStore) Heartbeat(context.Context, string, string, time.Time) error {
	return nil
}

func (s *recordingQueueStore) Complete(context.Context, string, string) error {
	return nil
}

func (s *recordingQueueStore) Fail(context.Context, string, string, string) error {
	return nil
}
