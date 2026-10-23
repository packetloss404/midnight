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

	agenttools "github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type automationAuthenticatorFunc func(context.Context, string) (automationtoken.Principal, error)

func (f automationAuthenticatorFunc) Authenticate(ctx context.Context, authorization string) (automationtoken.Principal, error) {
	return f(ctx, authorization)
}

func TestCIDeployV1RouteRegistryIsExact(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		target     string
		allowed    bool
		capability automationRouteCapability
	}{
		{name: "project item", method: http.MethodGet, target: "/api/v1alpha/projects/prj_a", allowed: true, capability: automationCapabilityProjectRead},
		{name: "service list scoped", method: http.MethodGet, target: "/api/v1alpha/services?project_id=prj_a", allowed: true, capability: automationCapabilityServiceList},
		{name: "service item", method: http.MethodGet, target: "/api/v1alpha/services/svc_a", allowed: true, capability: automationCapabilityServiceRead},
		{name: "environment list scoped", method: http.MethodGet, target: "/api/v1alpha/environments?project_id=prj_a", allowed: true, capability: automationCapabilityEnvironmentList},
		{name: "environment item", method: http.MethodGet, target: "/api/v1alpha/environments/env_a", allowed: true, capability: automationCapabilityEnvironmentRead},
		{name: "deploy image", method: http.MethodPost, target: "/api/v1alpha/services/svc_a/deployments", allowed: true, capability: automationCapabilityDeployCreate},
		{name: "deploy source", method: http.MethodPost, target: "/api/v1alpha/services/svc_a/up", allowed: true, capability: automationCapabilitySourceDeploy},
		{name: "deployment list", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/deployments", allowed: true, capability: automationCapabilityDeploymentList},
		{name: "deployment item", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/deployments/dpl_a", allowed: true, capability: automationCapabilityDeploymentRead},
		{name: "active deployment", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/deployments/latest-active", allowed: true, capability: automationCapabilityDeploymentRead},
		{name: "operation item", method: http.MethodGet, target: "/api/v1alpha/operations/op_a", allowed: true, capability: automationCapabilityOperationRead},
		{name: "operation events", method: http.MethodGet, target: "/api/v1alpha/operations/op_a/events", allowed: true, capability: automationCapabilityOperationEvents},
		{name: "operation logs", method: http.MethodGet, target: "/api/v1alpha/operations/op_a/logs", allowed: true, capability: automationCapabilityOperationLogs},

		{name: "project collection", method: http.MethodGet, target: "/api/v1alpha/projects"},
		{name: "project create", method: http.MethodPost, target: "/api/v1alpha/projects"},
		{name: "project access", method: http.MethodGet, target: "/api/v1alpha/projects/prj_a/access"},
		{name: "service list unscoped", method: http.MethodGet, target: "/api/v1alpha/services"},
		{name: "service list duplicate scope", method: http.MethodGet, target: "/api/v1alpha/services?project_id=prj_a&project_id=prj_b"},
		{name: "service create", method: http.MethodPost, target: "/api/v1alpha/services"},
		{name: "up read", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/up"},
		{name: "deployment replace", method: http.MethodPut, target: "/api/v1alpha/services/svc_a/deployments"},
		{name: "deployment delete", method: http.MethodDelete, target: "/api/v1alpha/services/svc_a/deployments/dpl_a"},
		{name: "deployment action", method: http.MethodPost, target: "/api/v1alpha/services/svc_a/deployments/dpl_a/actions/redeploy"},
		{name: "service github", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/github"},
		{name: "service config", method: http.MethodGet, target: "/api/v1alpha/services/svc_a/environments/env_a/config"},
		{name: "environment list unscoped", method: http.MethodGet, target: "/api/v1alpha/environments"},
		{name: "environment update", method: http.MethodPatch, target: "/api/v1alpha/environments/env_a"},
		{name: "environment duplicate", method: http.MethodPost, target: "/api/v1alpha/environments/env_a/duplicate"},
		{name: "operation list", method: http.MethodGet, target: "/api/v1alpha/operations"},
		{name: "operation cancel", method: http.MethodPost, target: "/api/v1alpha/operations/op_a/cancel"},
		{name: "operation fail", method: http.MethodPost, target: "/api/v1alpha/operations/op_a/fail"},
		{name: "head is not get", method: http.MethodHead, target: "/api/v1alpha/services/svc_a/deployments"},
		{name: "graphql", method: http.MethodPost, target: "/graphql/v2"},
		{name: "mcp", method: http.MethodPost, target: "/mcp/v1"},
		{name: "admin dashboard", method: http.MethodGet, target: "/api/v1alpha/dashboard/summary"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, nil)
			admission, allowed := ciDeployV1Admission(request)
			if allowed != test.allowed {
				t.Fatalf("allowed = %v admission=%#v, want %v", allowed, admission, test.allowed)
			}
			if allowed && admission.Capability != test.capability {
				t.Fatalf("capability = %q, want %q", admission.Capability, test.capability)
			}
		})
	}
}

func TestAutomationAuthenticationBindsTypedPrincipalActorAndTenant(t *testing.T) {
	want := automationtoken.Principal{
		Kind:             "automation",
		TokenID:          "tok_a",
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		Profile:          automationtoken.ProfileCIDeployV1,
		ExpiresAt:        time.Now().Add(time.Hour),
	}
	authenticator := automationAuthenticatorFunc(func(_ context.Context, authorization string) (automationtoken.Principal, error) {
		if authorization != "Bearer midn_sa_fixture" {
			t.Fatalf("authorization = %q", authorization)
		}
		return want, nil
	})
	resources := NewMemoryResourceStore()
	if _, err := resources.CreateProject(t.Context(), Project{
		ID:          "prj_a",
		WorkspaceID: "wrk_a",
		Slug:        "a",
		DisplayName: "A",
	}); err != nil {
		t.Fatal(err)
	}

	called := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := productPrincipalFromContext(r.Context())
		if !ok || principal.Kind != productPrincipalAutomation || principal.Automation != want {
			t.Fatalf("principal = %#v ok=%v", principal, ok)
		}
		if actor := idempotency.ActorFromContext(r.Context()); actor != "service-account:sa_ci" {
			t.Fatalf("actor = %q", actor)
		}
		if tenant := idempotency.TenantFromContext(r.Context()); tenant != projectScopedIdempotencyTenant("wrk_a", "prj_a") {
			t.Fatalf("tenant = %q", tenant)
		}
		admission, ok := automationRouteAdmissionFromContext(r.Context())
		if !ok || admission.Capability != automationCapabilityProjectRead || admission.ProjectID != "prj_a" {
			t.Fatalf("admission = %#v ok=%v", admission, ok)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	admitted := automationAdmissionMiddleware(NewMutationScopeResolver(resources, resources, nil), nil, downstream)
	handler := productAuthenticationMiddleware("operator-secret", true, nil, authenticator)(admitted)

	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects/prj_a", nil)
	request.Header.Set("Authorization", "Bearer midn_sa_fixture")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !called || response.Code != http.StatusNoContent {
		t.Fatalf("called=%v status=%d body=%s", called, response.Code, response.Body.String())
	}
}

func TestAutomationAuthenticationFailsClosed(t *testing.T) {
	backendErr := errors.New("token store unavailable")
	tests := []struct {
		name          string
		authenticator AutomationTokenAuthenticator
		wantStatus    int
	}{
		{
			name: "verifier unavailable",
			authenticator: automationAuthenticatorFunc(func(context.Context, string) (automationtoken.Principal, error) {
				return automationtoken.Principal{}, backendErr
			}),
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "invalid token",
			authenticator: automationAuthenticatorFunc(func(context.Context, string) (automationtoken.Principal, error) {
				return automationtoken.Principal{}, automationtoken.ErrInvalidToken
			}),
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "malformed verified principal",
			authenticator: automationAuthenticatorFunc(func(context.Context, string) (automationtoken.Principal, error) {
				return automationtoken.Principal{Kind: "automation", Profile: automationtoken.ProfileCIDeployV1}, nil
			}),
			wantStatus: http.StatusUnauthorized,
		},
		{name: "missing verifier", wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := productAuthenticationMiddleware("operator-secret", true, nil, test.authenticator)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects/prj_a", nil)
			request.Header.Set("Authorization", "Bearer midn_sa_fixture")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if called || response.Code != test.wantStatus {
				t.Fatalf("called=%v status=%d body=%s, want status=%d", called, response.Code, response.Body.String(), test.wantStatus)
			}
			if strings.Contains(response.Body.String(), "midn_sa_fixture") {
				t.Fatalf("response leaked bearer: %s", response.Body.String())
			}
		})
	}
}

func TestProjectAutomationTokenDeploysAndCannotEscapeRouterCapabilities(t *testing.T) {
	t.Setenv("MIDNIGHT_UP_EXTRACTION_ROOT", t.TempDir())
	stack, _ := productReadDataFixture(t)
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	tokenStore := automationtoken.NewMemoryStore(automationtoken.Scope{
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
	})
	tokenManager, err := automationtoken.NewManager(automationtoken.ManagerConfig{
		Store:  tokenStore,
		MACKey: []byte("automation-admission-test-key-32bytes"),
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	minted, err := tokenManager.Mint(t.Context(), automationtoken.MintRequest{
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		Name:             "CI",
		Profile:          automationtoken.ProfileCIDeployV1,
		CreatedByUserID:  "usr_alice",
		ExpiresAt:        now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	operations := NewMemoryOperationStore()
	deployments := NewMemoryDeploymentStore(func() time.Time { return now })
	queueStore := &recordingQueueStore{jobID: "job_automation"}
	stack.AutomationTokens = tokenManager
	stack.OperationStore = operations
	stack.DeploymentStore = deployments
	stack.QueueStore = queueStore
	stack.IdempotencyStore = idempotency.NewMemoryStore()
	routeStore := customDeployRouteTestStore("prj_a", "env_a", "svc_a")
	stack.DomainStore = routeStore
	graphQLCalled := false
	stack.GraphQLHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		graphQLCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	stack.MCPRegistry = agenttools.NewDefaultRegistry()
	handler := NewRouterWithDataStack(stack)

	allowedReads := []string{
		"/api/v1alpha/projects/prj_a",
		"/api/v1alpha/services?project_id=prj_a",
		"/api/v1alpha/services/svc_a",
		"/api/v1alpha/environments?project_id=prj_a",
		"/api/v1alpha/environments/env_a",
	}
	for _, target := range allowedReads {
		response := automationRequest(handler, http.MethodGet, target, minted.AccessToken, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}

	deployBody := []byte(`{"environment_id":"env_a","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","route":{"host":"web.example.test","public":true,"tls":true}}`)
	deployResponse := automationRequest(handler, http.MethodPost, "/api/v1alpha/services/svc_a/deployments", minted.AccessToken, deployBody, map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": "automation-deploy",
	})
	if deployResponse.Code != http.StatusAccepted {
		t.Fatalf("deploy status=%d body=%s", deployResponse.Code, deployResponse.Body.String())
	}
	var deployPayload deployJobPayload
	if err := json.Unmarshal(queueStore.requests[0].Payload, &deployPayload); err != nil {
		t.Fatal(err)
	}
	if routeStore.selectedScope != (router.RouteScope{ProjectID: "prj_a", EnvironmentID: "env_a", ServiceID: "svc_a"}) ||
		routeStore.selectedSelector != (router.ManagedRouteSelector{Host: "web.example.test", TLSDesired: true}) {
		t.Fatalf("automation route selector scope=%#v selector=%#v", routeStore.selectedScope, routeStore.selectedSelector)
	}
	if deployPayload.Route == nil || deployPayload.Route.Host != "web.example.test" || deployPayload.Route.Name != "web-custom" || deployPayload.Route.Path != "/" || !deployPayload.Route.TLS {
		t.Fatalf("automation route selector payload=%#v", deployPayload.Route)
	}
	var deployOperation orailtypes.Operation
	if err := json.Unmarshal(deployResponse.Body.Bytes(), &deployOperation); err != nil {
		t.Fatal(err)
	}

	upResponse := automationRequest(handler, http.MethodPost, "/api/v1alpha/services/svc_a/up", minted.AccessToken, buildSampleTarball(t), map[string]string{
		"Content-Type":           "application/gzip",
		"Idempotency-Key":        "automation-up",
		"X-Midnight-Environment": "env_a",
	})
	if upResponse.Code != http.StatusAccepted {
		t.Fatalf("up status=%d body=%s", upResponse.Code, upResponse.Body.String())
	}
	if len(queueStore.requests) != 2 {
		t.Fatalf("queue requests = %d, want deploy and up", len(queueStore.requests))
	}

	for _, target := range []string{
		"/api/v1alpha/services/svc_a/deployments",
		"/api/v1alpha/operations/" + string(deployOperation.ID),
		"/api/v1alpha/operations/" + string(deployOperation.ID) + "/events",
	} {
		response := automationRequest(handler, http.MethodGet, target, minted.AccessToken, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}

	crossScope := []string{
		"/api/v1alpha/projects/prj_b",
		"/api/v1alpha/services?project_id=prj_b",
		"/api/v1alpha/services/svc_b",
		"/api/v1alpha/environments?project_id=prj_b",
		"/api/v1alpha/environments/env_b",
	}
	for _, target := range crossScope {
		response := automationRequest(handler, http.MethodGet, target, minted.AccessToken, nil, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("cross-scope GET %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}

	orphanOperation, err := operations.Create(t.Context(), orailtypes.Operation{
		ID:    "op_orphan",
		Kind:  "deploy",
		State: orailtypes.OperationQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	orphanResponse := automationRequest(handler, http.MethodGet, "/api/v1alpha/operations/"+string(orphanOperation.ID), minted.AccessToken, nil, nil)
	if orphanResponse.Code != http.StatusNotFound {
		t.Fatalf("operation without canonical deployment status=%d body=%s", orphanResponse.Code, orphanResponse.Body.String())
	}
	foreignOperation, err := operations.Create(t.Context(), orailtypes.Operation{
		ID:    "op_foreign",
		Kind:  "deploy",
		State: orailtypes.OperationQueued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.CreateDeployment(t.Context(), DeploymentRecord{
		ID:            "dpl_foreign",
		ProjectID:     "prj_b",
		ServiceID:     "svc_b",
		EnvironmentID: "env_b",
		Action:        deploy.ActionDeployImage,
		OperationID:   string(foreignOperation.ID),
	}); err != nil {
		t.Fatal(err)
	}
	foreignResponse := automationRequest(handler, http.MethodGet, "/api/v1alpha/operations/"+string(foreignOperation.ID), minted.AccessToken, nil, nil)
	if foreignResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-project operation status=%d body=%s", foreignResponse.Code, foreignResponse.Body.String())
	}

	deniedProductMethods := []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/api/v1alpha/projects"},
		{method: http.MethodPost, target: "/api/v1alpha/services"},
		{method: http.MethodDelete, target: "/api/v1alpha/services/svc_a/deployments/dpl_a"},
		{method: http.MethodPost, target: "/api/v1alpha/services/svc_a/deployments/dpl_a/actions/redeploy"},
		{method: http.MethodPost, target: "/api/v1alpha/operations/" + string(deployOperation.ID) + "/cancel"},
		{method: http.MethodPost, target: "/api/v1alpha/operations/" + string(deployOperation.ID) + "/fail"},
		{method: http.MethodGet, target: "/api/v1alpha/services/svc_a/preview-environments"},
		{method: http.MethodGet, target: "/api/v1alpha/services/svc_a/environments/env_a/variables"},
		{method: http.MethodPost, target: "/api/v1alpha/services/svc_a/environments/env_a/variables"},
		{method: http.MethodGet, target: "/api/v1alpha/services/svc_a/environments/env_a/variables/var_a"},
		{method: http.MethodPatch, target: "/api/v1alpha/services/svc_a/environments/env_a/variables/var_a"},
		{method: http.MethodDelete, target: "/api/v1alpha/services/svc_a/environments/env_a/variables/var_a"},
	}
	for _, test := range deniedProductMethods {
		response := automationRequest(handler, test.method, test.target, minted.AccessToken, nil, nil)
		if response.Code != http.StatusForbidden {
			t.Fatalf("denied %s %s status=%d body=%s", test.method, test.target, response.Code, response.Body.String())
		}
	}

	operatorOnly := []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/api/v1alpha/operations"},
		{method: http.MethodGet, target: "/api/v1alpha/variables"},
		{method: http.MethodGet, target: "/api/v1alpha/dashboard/summary"},
		{method: http.MethodPost, target: "/graphql/v2"},
		{method: http.MethodPost, target: "/mcp/v1"},
	}
	for _, test := range operatorOnly {
		response := automationRequest(handler, test.method, test.target, minted.AccessToken, nil, nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("operator-only %s %s status=%d body=%s", test.method, test.target, response.Code, response.Body.String())
		}
	}
	if graphQLCalled {
		t.Fatal("automation token reached GraphQL handler")
	}
}

func automationRequest(handler http.Handler, method, target, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	handler.ServeHTTP(response, request)
	return response
}
