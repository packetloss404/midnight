// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/topology"
)

func TestTopologyLayoutRoutePersistsWithCAS(t *testing.T) {
	resources := NewMemoryResourceStore()
	environments := NewMemoryEnvironmentStore(nil)
	project, err := resources.CreateProject(context.Background(), Project{ID: "prj_1", WorkspaceID: "wrk_1", Slug: "one", DisplayName: "One"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = environments.CreateEnvironment(context.Background(), Environment{ID: "env_1", ProjectID: project.ID, Slug: "production", Name: "Production"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.CreateService(context.Background(), Service{ID: "svc_1", ProjectID: project.ID, Slug: "web", DisplayName: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments, TopologyLayouts: topology.NewMemoryStore(nil)})
	path := "/api/v1alpha/projects/prj_1/environments/env_1/topology-layout"

	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, path, nil))
	if initial.Code != http.StatusOK || initial.Header().Get("ETag") != `"0"` {
		t.Fatalf("initial=%d etag=%q body=%s", initial.Code, initial.Header().Get("ETag"), initial.Body.String())
	}

	put := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"nodes":[{"service_id":"svc_1","x":120,"y":80}]}`))
	put.Header.Set("If-Match", `"0"`)
	updated := httptest.NewRecorder()
	handler.ServeHTTP(updated, put)
	if updated.Code != http.StatusOK || updated.Header().Get("ETag") != `"1"` {
		t.Fatalf("updated=%d etag=%q body=%s", updated.Code, updated.Header().Get("ETag"), updated.Body.String())
	}
	var layout topology.Layout
	if err := json.Unmarshal(updated.Body.Bytes(), &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Nodes) != 1 || layout.Nodes[0].X != 120 {
		t.Fatalf("layout=%+v", layout)
	}

	stale := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"nodes":[]}`))
	stale.Header.Set("If-Match", `"0"`)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale=%d body=%s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestTopologyLayoutRejectsCrossProjectNode(t *testing.T) {
	resources := NewMemoryResourceStore()
	environments := NewMemoryEnvironmentStore(nil)
	project, _ := resources.CreateProject(context.Background(), Project{ID: "prj_1", WorkspaceID: "wrk_1", Slug: "one"})
	other, _ := resources.CreateProject(context.Background(), Project{ID: "prj_2", WorkspaceID: "wrk_1", Slug: "two"})
	_, _ = environments.CreateEnvironment(context.Background(), Environment{ID: "env_1", ProjectID: project.ID, Slug: "production"})
	_, _ = resources.CreateService(context.Background(), Service{ID: "svc_other", ProjectID: other.ID, Slug: "other"})
	handler := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments, TopologyLayouts: topology.NewMemoryStore(nil)})
	request := httptest.NewRequest(http.MethodPut, "/api/v1alpha/projects/prj_1/environments/env_1/topology-layout", strings.NewReader(`{"nodes":[{"service_id":"svc_other","x":1,"y":2}]}`))
	request.Header.Set("If-Match", `"0"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTopologyLayoutRequiresExactHumanTenantAuthority(t *testing.T) {
	resources := NewMemoryResourceStore()
	environments := NewMemoryEnvironmentStore(nil)
	project, _ := resources.CreateProject(context.Background(), Project{ID: "prj_auth", WorkspaceID: "wrk_auth", Slug: "auth"})
	_, _ = environments.CreateEnvironment(context.Background(), Environment{ID: "env_auth", ProjectID: project.ID, Slug: "production"})
	_, _ = resources.CreateService(context.Background(), Service{ID: "svc_auth", ProjectID: project.ID, Slug: "web"})

	access := identity.NewMemoryAccessStore()
	accepted := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	viewer := identity.User{ID: "usr_viewer", Email: "viewer@example.com"}
	access.SeedWorkspace(identity.Workspace{ID: "wrk_auth", Slug: "auth"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_auth", User: viewer, Role: identity.WorkspaceRoleViewer, AcceptedAt: &accepted,
	})
	access.SeedProject("prj_auth", "wrk_auth")
	access.SeedEnvironment("env_auth", "prj_auth", false)
	store := topology.NewMemoryStore(nil)
	handler := topologyLayoutHandler(store, resources, environments, resources, access)
	path := "/api/v1alpha/projects/prj_auth/environments/env_auth/topology-layout"

	humanContext := func(request *http.Request, userID string) *http.Request {
		request.SetPathValue("project_id", "prj_auth")
		request.SetPathValue("environment_id", "env_auth")
		principal := identity.Principal{Kind: "user", UserID: userID, SessionID: "ses_" + userID}
		return request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal}))
	}
	viewerRead := httptest.NewRecorder()
	handler.ServeHTTP(viewerRead, humanContext(httptest.NewRequest(http.MethodGet, path, nil), viewer.ID))
	if viewerRead.Code != http.StatusOK {
		t.Fatalf("viewer read status=%d body=%s", viewerRead.Code, viewerRead.Body.String())
	}

	viewerWriteRequest := humanContext(httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"nodes":[]}`)), viewer.ID)
	viewerWriteRequest.Header.Set("If-Match", `"0"`)
	viewerWrite := httptest.NewRecorder()
	handler.ServeHTTP(viewerWrite, viewerWriteRequest)
	if viewerWrite.Code != http.StatusForbidden {
		t.Fatalf("viewer write status=%d body=%s", viewerWrite.Code, viewerWrite.Body.String())
	}

	outsider := httptest.NewRecorder()
	handler.ServeHTTP(outsider, humanContext(httptest.NewRequest(http.MethodGet, path, nil), "usr_outside"))
	if outsider.Code != http.StatusNotFound {
		t.Fatalf("outsider status=%d body=%s", outsider.Code, outsider.Body.String())
	}

	for _, kind := range []productPrincipalKind{productPrincipalOperator, productPrincipalAutomation} {
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"nodes":[]}`))
		request.SetPathValue("project_id", "prj_auth")
		request.SetPathValue("environment_id", "env_auth")
		request.Header.Set("If-Match", `"0"`)
		request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: kind}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", kind, response.Code, response.Body.String())
		}
	}
	if _, err := store.Get(context.Background(), "prj_auth", "env_auth"); !errors.Is(err, topology.ErrNotFound) {
		t.Fatalf("denied writes changed store: %v", err)
	}
}
