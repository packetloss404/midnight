package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type memoryDomainWorkflow struct {
	domains  map[string]router.DurableDomain
	routes   map[string]router.DurableHTTPRoute
	bindings map[string]string // route id -> domain id
	bindErr  error
}

// BindServiceDomain implements router.ServiceDomainBinder so the operator
// domain-create path records the same service ownership binding the tenant
// coordinator writes.
func (s *memoryDomainWorkflow) BindServiceDomain(_ context.Context, domainID, routeID string, _ router.RouteScope, _ time.Time) error {
	if s.bindErr != nil {
		return s.bindErr
	}
	if s.bindings == nil {
		s.bindings = map[string]string{}
	}
	s.bindings[routeID] = domainID
	return nil
}

func (s *memoryDomainWorkflow) PutDomain(_ context.Context, value router.DurableDomain) (router.DurableDomain, error) {
	if s.domains == nil {
		s.domains = map[string]router.DurableDomain{}
	}
	s.domains[value.ID] = value
	return value, nil
}
func (s *memoryDomainWorkflow) PutHTTPRoute(_ context.Context, value router.DurableHTTPRoute) (router.DurableHTTPRoute, error) {
	if s.routes == nil {
		s.routes = map[string]router.DurableHTTPRoute{}
	}
	s.routes[value.ID] = value
	return value, nil
}
func (s *memoryDomainWorkflow) GetDomain(_ context.Context, id string) (router.DurableDomain, error) {
	value, ok := s.domains[id]
	if !ok {
		return router.DurableDomain{}, router.ErrDurableRouteNotFound
	}
	return value, nil
}
func (s *memoryDomainWorkflow) DeleteDomain(_ context.Context, id string) error {
	delete(s.domains, id)
	for routeID, routeValue := range s.routes {
		if routeValue.DomainID == id {
			delete(s.routes, routeID)
		}
	}
	return nil
}
func (s *memoryDomainWorkflow) EnsureDomainLifecycle(context.Context, router.DurableDomain) error {
	return nil
}
func (s *memoryDomainWorkflow) ListDomainRouteStates(_ context.Context, scope router.RouteScope) ([]router.DomainRouteState, error) {
	var out []router.DomainRouteState
	for _, domainValue := range s.domains {
		if domainValue.RouteScope != scope {
			continue
		}
		state := router.DomainRouteState{Domain: domainValue, ApplyState: router.RouteApplyPending}
		for _, routeValue := range s.routes {
			if routeValue.DomainID == domainValue.ID {
				state.Routes = append(state.Routes, routeValue)
			}
		}
		out = append(out, state)
	}
	return out, nil
}

func TestDomainsRESTGeneratedCreateListDeleteStaysEvidenceShaped(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "domain-test"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(nil)
	environment, err := environments.CreateEnvironment(ctx, Environment{
		ID: orailtypes.ID("env_domain_test"), ProjectID: project.ID, Slug: "production", Name: "Production",
	})
	if err != nil {
		t.Fatal(err)
	}
	domains := &memoryDomainWorkflow{}
	handler := NewRouterWithDataStack(DataStack{
		OperationStore: NewMemoryOperationStore(), IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments,
		MutationScopeResolver: NewMutationScopeResolver(resources, resources, environments),
		DomainStore:           domains, AppsDomain: "apps.example.test",
	})
	body, _ := json.Marshal(CreateDomainRequest{
		ProjectID: string(project.ID), EnvironmentID: string(environment.ID),
		ServiceID: string(service.ID), Kind: router.DomainKindGenerated,
		Public: true, TLS: false,
	})
	create := httptest.NewRequest(http.MethodPost, "/api/v1alpha/domains", bytes.NewReader(body))
	create.Header.Set("Idempotency-Key", "domain-create-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, create)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created DomainView
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Reachable || created.ApplyState != router.RouteApplyPending {
		t.Fatalf("created domain overclaimed reachability: %#v", created)
	}

	listPath := "/api/v1alpha/domains?project_id=" + string(project.ID) +
		"&environment_id=" + string(environment.ID) + "&service_id=" + string(service.ID)
	list := httptest.NewRequest(http.MethodGet, listPath, nil)
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, list)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var listed []DomainView
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed=%#v", listed)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/domains/"+created.ID, nil)
	remove.Header.Set("Idempotency-Key", "domain-delete-test")
	removeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(removeRecorder, remove)
	if removeRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", removeRecorder.Code, removeRecorder.Body.String())
	}
}

func newDomainBindTestStack(t *testing.T, store router.DomainWorkflowStore) (http.Handler, string, string, string) {
	t.Helper()
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "bind-test"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(nil)
	environment, err := environments.CreateEnvironment(ctx, Environment{
		ID: orailtypes.ID("env_bind_test"), ProjectID: project.ID, Slug: "production", Name: "Production",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(DataStack{
		OperationStore: NewMemoryOperationStore(), IdempotencyStore: idempotency.NewMemoryStore(),
		ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments,
		MutationScopeResolver: NewMutationScopeResolver(resources, resources, environments),
		DomainStore:           store, AppsDomain: "apps.example.test",
	})
	return handler, string(project.ID), string(environment.ID), string(service.ID)
}

func postGeneratedDomain(t *testing.T, handler http.Handler, projectID, environmentID, serviceID, key string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(CreateDomainRequest{
		ProjectID: projectID, EnvironmentID: environmentID, ServiceID: serviceID,
		Kind: router.DomainKindGenerated, Public: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/domains", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// A domain created without a service binding is dead on arrival: deploy
// intake resolves a public route intent against an existing binding, so an
// unbound route can never be attached to a deployment and stays blocked on a
// missing backend forever.
func TestCreateDomainBindsRouteToService(t *testing.T) {
	store := &memoryDomainWorkflow{}
	handler, projectID, environmentID, serviceID := newDomainBindTestStack(t, store)

	rec := postGeneratedDomain(t, handler, projectID, environmentID, serviceID, "domain-bind-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create domain status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(store.routes) != 1 {
		t.Fatalf("expected exactly one route, got %d", len(store.routes))
	}
	if len(store.bindings) != 1 {
		t.Fatalf("expected the created route to be bound to its service, got bindings = %v", store.bindings)
	}
	for routeID, domainID := range store.bindings {
		if _, ok := store.routes[routeID]; !ok {
			t.Fatalf("binding references unknown route %q", routeID)
		}
		if _, ok := store.domains[domainID]; !ok {
			t.Fatalf("binding references unknown domain %q", domainID)
		}
	}
}

func TestCreateDomainFailsWhenBindingFails(t *testing.T) {
	store := &memoryDomainWorkflow{bindErr: errors.New("bind unavailable")}
	handler, projectID, environmentID, serviceID := newDomainBindTestStack(t, store)

	rec := postGeneratedDomain(t, handler, projectID, environmentID, serviceID, "domain-bind-fail-1")
	if rec.Code < 400 {
		t.Fatalf("a failed binding must fail the request rather than create an unusable domain; status = %d", rec.Code)
	}
}
