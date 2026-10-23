package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/tcpproxy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTCPProxyRESTCreateListGetAndWithdrawKeepsExternalClaimsFalse(t *testing.T) {
	handler, scope := tcpProxyTestRouter(t)
	body, _ := json.Marshal(CreateTCPProxyRequest{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID,
		BackendPort:        5432,
		AllowedSourceCIDRs: []string{"192.0.2.0/24", "127.0.0.1/32"},
	})
	create := httptest.NewRequest(http.MethodPost, "/api/v1alpha/tcp-proxies", bytes.NewReader(body))
	create.Header.Set("Idempotency-Key", "tcp-create-test")
	createRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createRecorder, create)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created TCPProxyView
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ApplyState != tcpproxy.ApplyPending || created.ListenerBound ||
		created.BackendReachable || created.DNSProvisioned || created.PublicReachabilityVerified {
		t.Fatalf("create overclaimed evidence: %#v", created)
	}
	if created.PublicAddress == "" || created.ExternalPort != 24000 {
		t.Fatalf("created address = %#v", created)
	}

	query := "?project_id=" + scope.ProjectID + "&environment_id=" + scope.EnvironmentID + "&service_id=" + scope.ServiceID
	list := httptest.NewRequest(http.MethodGet, "/api/v1alpha/tcp-proxies"+query, nil)
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, list)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var listed []TCPProxyView
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list = %#v", listed)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1alpha/tcp-proxies/"+created.ID, nil)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", getRecorder.Code, getRecorder.Body.String())
	}

	remove := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/tcp-proxies/"+created.ID, nil)
	remove.Header.Set("Idempotency-Key", "tcp-withdraw-test")
	removeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(removeRecorder, remove)
	if removeRecorder.Code != http.StatusAccepted {
		t.Fatalf("withdraw status=%d body=%s", removeRecorder.Code, removeRecorder.Body.String())
	}
	var withdrawn TCPProxyView
	if err := json.Unmarshal(removeRecorder.Body.Bytes(), &withdrawn); err != nil {
		t.Fatal(err)
	}
	if withdrawn.DesiredEnabled || withdrawn.ApplyState != tcpproxy.ApplyWithdrawn {
		t.Fatalf("withdrawn = %#v", withdrawn)
	}
}

func TestTCPProxyRESTAllowsPublicEndpointWithoutPeerRestriction(t *testing.T) {
	handler, scope := tcpProxyTestRouter(t)
	body, _ := json.Marshal(CreateTCPProxyRequest{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID,
		BackendPort: 5432,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/tcp-proxies", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "tcp-public-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created TCPProxyView
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.AllowedSourceCIDRs == nil || len(created.AllowedSourceCIDRs) != 0 {
		t.Fatalf("allowed_source_cidrs=%#v, want explicit unrestricted slice", created.AllowedSourceCIDRs)
	}
}

func TestTCPProxyRESTRejectsCallerSuppliedBackendHost(t *testing.T) {
	handler, scope := tcpProxyTestRouter(t)
	body := `{"project_id":"` + scope.ProjectID + `","environment_id":"` + scope.EnvironmentID +
		`","service_id":"` + scope.ServiceID +
		`","backend_host":"169.254.169.254","backend_port":80,"allowed_source_cidrs":["192.0.2.0/24"]}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/tcp-proxies", bytes.NewBufferString(body))
	request.Header.Set("Idempotency-Key", "tcp-host-injection-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestTCPProxyRESTReturnsConflictWithoutReadyActiveBackend(t *testing.T) {
	handler, scope := tcpProxyTestRouterWithResolver(t, staticTCPBackendResolver{err: tcpproxy.ErrBackendUnavailable})
	body, _ := json.Marshal(CreateTCPProxyRequest{
		ProjectID: scope.ProjectID, EnvironmentID: scope.EnvironmentID, ServiceID: scope.ServiceID,
		BackendPort: 5432,
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/tcp-proxies", bytes.NewReader(body))
	request.Header.Set("Idempotency-Key", "tcp-no-backend-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func tcpProxyTestRouter(t *testing.T) (http.Handler, tcpproxy.Scope) {
	return tcpProxyTestRouterWithResolver(t, staticTCPBackendResolver{value: []tcpproxy.Backend{{
		DeploymentID: "dep_active", InstanceID: "dep_active-r0", Host: "10.42.0.8",
	}}})
}

func tcpProxyTestRouterWithResolver(t *testing.T, backendResolver tcpproxy.BackendResolver) (http.Handler, tcpproxy.Scope) {
	t.Helper()
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "tcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "database"})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(nil)
	environment, err := environments.CreateEnvironment(ctx, Environment{
		ID: orailtypes.ID("env_tcp_test"), ProjectID: project.ID, Slug: "production", Name: "Production",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryTCPProxyStore{ports: tcpproxy.PortRange{Start: 24000, End: 24002}}
	return NewRouterWithDataStack(DataStack{
			OperationStore: NewMemoryOperationStore(), IdempotencyStore: idempotency.NewMemoryStore(),
			ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments,
			MutationScopeResolver:   NewMutationScopeResolver(resources, resources, environments),
			TCPProxyStore:           store,
			TCPProxyBackendResolver: backendResolver,
			TCPProxyBaseDomain:      "example.test",
		}), tcpproxy.Scope{
			ProjectID: string(project.ID), EnvironmentID: string(environment.ID), ServiceID: string(service.ID),
		}
}

type staticTCPBackendResolver struct {
	value []tcpproxy.Backend
	err   error
}

func (r staticTCPBackendResolver) ResolveBackends(context.Context, tcpproxy.Scope) ([]tcpproxy.Backend, error) {
	return r.value, r.err
}

type memoryTCPProxyStore struct {
	values map[string]tcpproxy.Endpoint
	ports  tcpproxy.PortRange
}

func (s *memoryTCPProxyStore) CreateEndpoint(_ context.Context, input tcpproxy.CreateEndpointInput) (tcpproxy.Endpoint, error) {
	var existing []tcpproxy.ExistingEndpoint
	for _, value := range s.values {
		if value.DesiredEnabled || value.ListenerBound {
			existing = append(existing, tcpproxy.ExistingEndpoint{Host: value.Host, ExternalPort: value.ExternalPort})
		}
	}
	plan, err := tcpproxy.PlanEndpoint(tcpproxy.PlanInput{
		Request: tcpproxy.EndpointRequest{
			Enabled: true, ProjectID: input.ProjectID, EnvironmentID: input.EnvironmentID, ServiceID: input.ServiceID,
			BaseDomain: input.BaseDomain, RequestedHost: input.RequestedHost,
			RequestedExternalPort: input.RequestedExternalPort,
			BackendHost:           "active-deployment.internal", BackendPort: input.BackendPort,
			SourceIPPolicy: tcpproxy.SourceIPPolicy{AllowedCIDRs: input.AllowedCIDRs},
		},
		Existing: existing, Ports: s.ports,
	})
	if err != nil {
		return tcpproxy.Endpoint{}, errorsJoinInvalid(err)
	}
	if s.values == nil {
		s.values = map[string]tcpproxy.Endpoint{}
	}
	now := time.Now().UTC()
	value := tcpproxy.Endpoint{
		ID: "tcp_test", Scope: input.Scope, Host: plan.Host, ExternalPort: plan.ExternalPort,
		BackendPort:  plan.BackendPort,
		AllowedCIDRs: plan.SourceIPPolicy.AllowedCIDRs, DesiredEnabled: true,
		ApplyState: tcpproxy.ApplyPending, CreatedAt: now, UpdatedAt: now,
	}
	s.values[value.ID] = value
	return value, nil
}

func errorsJoinInvalid(err error) error {
	return &tcpProxyInvalidError{cause: err}
}

type tcpProxyInvalidError struct{ cause error }

func (e *tcpProxyInvalidError) Error() string {
	return tcpproxy.ErrEndpointInvalid.Error() + ": " + e.cause.Error()
}
func (e *tcpProxyInvalidError) Unwrap() error { return tcpproxy.ErrEndpointInvalid }

func (s *memoryTCPProxyStore) GetEndpoint(_ context.Context, id string) (tcpproxy.Endpoint, error) {
	value, ok := s.values[id]
	if !ok {
		return tcpproxy.Endpoint{}, tcpproxy.ErrEndpointNotFound
	}
	return value, nil
}
func (s *memoryTCPProxyStore) ListEndpoints(_ context.Context, scope tcpproxy.Scope) ([]tcpproxy.Endpoint, error) {
	var out []tcpproxy.Endpoint
	for _, value := range s.values {
		if value.Scope == scope {
			out = append(out, value)
		}
	}
	return out, nil
}
func (s *memoryTCPProxyStore) ListRuntimeEndpoints(context.Context) ([]tcpproxy.Endpoint, error) {
	var out []tcpproxy.Endpoint
	for _, value := range s.values {
		out = append(out, value)
	}
	return out, nil
}
func (s *memoryTCPProxyStore) DisableEndpoint(_ context.Context, id string) (tcpproxy.Endpoint, error) {
	value, ok := s.values[id]
	if !ok {
		return tcpproxy.Endpoint{}, tcpproxy.ErrEndpointNotFound
	}
	value.DesiredEnabled = false
	value.ApplyState = tcpproxy.ApplyWithdrawn
	value.ListenerBound = false
	s.values[id] = value
	return value, nil
}
func (s *memoryTCPProxyStore) RecordObservation(context.Context, string, bool, tcpproxy.Observation) (bool, error) {
	return false, nil
}
