// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/router"
)

type recordingGeneratedRouteProjectionStore struct {
	router.DomainWorkflowStore
	calls         int
	projectID     string
	serviceID     string
	deploymentIDs []string
	values        []router.GeneratedRouteProjection
	err           error
}

func (s *recordingGeneratedRouteProjectionStore) ListGeneratedRouteProjections(
	_ context.Context,
	projectID string,
	serviceID string,
	deploymentIDs []string,
) ([]router.GeneratedRouteProjection, error) {
	s.calls++
	s.projectID = projectID
	s.serviceID = serviceID
	s.deploymentIDs = append([]string(nil), deploymentIDs...)
	return append([]router.GeneratedRouteProjection(nil), s.values...), s.err
}

func TestDeploymentGeneratedRouteProjectionIsBatchedAndEvidenceHonest(t *testing.T) {
	store := &recordingGeneratedRouteProjectionStore{
		values: []router.GeneratedRouteProjection{
			{
				DeploymentID: "dep_pending",
				RouteScope: router.RouteScope{
					ProjectID: "prj_routes", EnvironmentID: "env_prod", ServiceID: "svc_routes",
				},
				Hostname: "pending.apps.example.test", Path: "/",
				ApplyState: router.RouteApplyPending,
			},
			{
				DeploymentID: "dep_ready",
				RouteScope: router.RouteScope{
					ProjectID: "prj_routes", EnvironmentID: "env_prod", ServiceID: "svc_routes",
				},
				Hostname: "ready.apps.example.test", Path: "/health",
				ApplyState: router.RouteApplyReady, Reachable: true,
			},
		},
	}
	resources := []DeploymentResource{
		{ID: "dep_pending", DeploymentID: "dep_pending", EnvironmentID: "env_prod"},
		{ID: "dep_ready", DeploymentID: "dep_ready", EnvironmentID: "env_prod"},
		{ID: "dep_private", DeploymentID: "dep_private", EnvironmentID: "env_prod"},
	}
	request := httptest.NewRequest(http.MethodGet, "/deployments", nil)
	if err := enrichDeploymentGeneratedRoutes(
		request,
		store,
		Service{ID: "svc_routes", ProjectID: "prj_routes"},
		resources,
	); err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 ||
		store.projectID != "prj_routes" ||
		store.serviceID != "svc_routes" ||
		strings.Join(store.deploymentIDs, ",") != "dep_pending,dep_ready,dep_private" {
		t.Fatalf(
			"projection call count=%d scope=%s/%s ids=%v",
			store.calls,
			store.projectID,
			store.serviceID,
			store.deploymentIDs,
		)
	}
	if resources[0].RouteIntent != "pending.apps.example.test" ||
		resources[0].Route != "" ||
		resources[0].RouteApplyState != router.RouteApplyPending ||
		resources[0].RouteReachable {
		t.Fatalf("pending projection overclaimed: %#v", resources[0])
	}
	if resources[1].RouteIntent != "ready.apps.example.test" ||
		resources[1].Route != "http://ready.apps.example.test/health" ||
		resources[1].RouteApplyState != router.RouteApplyReady ||
		!resources[1].RouteReachable {
		t.Fatalf("ready projection missing evidence: %#v", resources[1])
	}
	if resources[2].RouteIntent != "" ||
		resources[2].Route != "" ||
		resources[2].RouteApplyState != "" ||
		resources[2].RouteReachable {
		t.Fatalf("private deployment inferred a route: %#v", resources[2])
	}
}

func TestDeploymentResourceAPIScopesReadsToExactEnvironment(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(
		context.Background(),
		Project{WorkspaceID: DefaultWorkspaceID, Slug: "environment-filter"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(
		context.Background(),
		Service{ProjectID: project.ID, Slug: "web"},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
	deployments := NewMemoryDeploymentStore(func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	seedCanonicalActiveDeployment(
		t,
		deployments,
		DeploymentRecord{
			ID:            "dep_env_a_active",
			ProjectID:     string(project.ID),
			ServiceID:     string(service.ID),
			EnvironmentID: "env_a",
			Action:        deploy.ActionDeployImage,
			OperationID:   "op_env_a_active",
		},
	)
	seedCanonicalActiveDeployment(
		t,
		deployments,
		DeploymentRecord{
			ID:            "dep_env_b_active",
			ProjectID:     string(project.ID),
			ServiceID:     string(service.ID),
			EnvironmentID: "env_b",
			Action:        deploy.ActionDeployImage,
			OperationID:   "op_env_b_active",
		},
	)
	if _, err := deployments.CreateDeployment(
		context.Background(),
		DeploymentRecord{
			ID:            "dep_env_a_queued",
			ProjectID:     string(project.ID),
			ServiceID:     string(service.ID),
			EnvironmentID: "env_a",
			Action:        deploy.ActionDeployImage,
			OperationID:   "op_env_a_queued",
		},
	); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:  NewMemoryOperationStore(),
		DeploymentStore: deployments,
		ProjectStore:    resources,
		ServiceStore:    resources,
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) + "/deployments"

	list := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"?environment_id=env_a&limit=1",
	)
	if list.Code != http.StatusOK {
		t.Fatalf("filtered list status=%d body=%s", list.Code, list.Body.String())
	}
	var firstPage DeploymentResourcePage
	if err := json.Unmarshal(list.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 ||
		firstPage.Items[0].ID != "dep_env_a_queued" ||
		firstPage.Items[0].EnvironmentID != "env_a" ||
		firstPage.NextCursor == "" {
		t.Fatalf("filtered deployment page=%#v", firstPage)
	}
	secondPageResponse := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"?environment_id=env_a&limit=1&cursor="+
			url.QueryEscape(firstPage.NextCursor),
	)
	if secondPageResponse.Code != http.StatusOK {
		t.Fatalf(
			"second page status=%d body=%s",
			secondPageResponse.Code,
			secondPageResponse.Body.String(),
		)
	}
	var secondPage DeploymentResourcePage
	if err := json.Unmarshal(secondPageResponse.Body.Bytes(), &secondPage); err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 ||
		secondPage.Items[0].ID != "dep_env_a_active" ||
		secondPage.Items[0].EnvironmentID != "env_a" ||
		secondPage.NextCursor != "" {
		t.Fatalf("second filtered deployment page=%#v", secondPage)
	}

	latest := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"/latest-active?environment_id=env_b",
	)
	if latest.Code != http.StatusOK {
		t.Fatalf("filtered latest status=%d body=%s", latest.Code, latest.Body.String())
	}
	var active DeploymentResource
	if err := json.Unmarshal(latest.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.ID != "dep_env_b_active" || active.EnvironmentID != "env_b" {
		t.Fatalf("filtered latest=%#v", active)
	}

	concealedItem := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"/dep_env_b_active?environment_id=env_a",
	)
	if concealedItem.Code != http.StatusNotFound {
		t.Fatalf(
			"cross-environment item status=%d body=%s, want 404",
			concealedItem.Code,
			concealedItem.Body.String(),
		)
	}
}

func TestDeploymentResourceAPIRejectsAmbiguousEnvironmentQueries(t *testing.T) {
	resources := NewMemoryResourceStore()
	service, err := resources.CreateService(context.Background(), Service{Slug: "web"})
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore: NewMemoryOperationStore(),
		ProjectStore:   resources,
		ServiceStore:   resources,
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) + "/deployments"
	tests := []struct {
		name string
		path string
		code failurecodes.Code
	}{
		{
			name: "repeated environment",
			path: basePath + "?environment_id=env_a&environment_id=env_b",
			code: failurecodes.ResourceValidationFailed,
		},
		{
			name: "empty environment",
			path: basePath + "/latest-active?environment_id=",
			code: failurecodes.ResourceValidationFailed,
		},
		{
			name: "invalid limit",
			path: basePath + "?limit=101",
			code: failurecodes.ResourceValidationFailed,
		},
		{
			name: "pagination on exact resource",
			path: basePath + "/dep_1?limit=1",
			code: failurecodes.ResourceValidationFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := doDeploymentQueryRequest(router, http.MethodGet, test.path)
			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"status=%d body=%s, want 400",
					response.Code,
					response.Body.String(),
				)
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.code {
				t.Fatalf("code=%q, want %q", envelope.Error.Code, test.code)
			}
		})
	}
}

func TestDeploymentResourceAPIPaginatesCompleteCanonicalHistory(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(
		context.Background(),
		Project{WorkspaceID: DefaultWorkspaceID, Slug: "complete-array"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(
		context.Background(),
		Service{ProjectID: project.ID, Slug: "web"},
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 19, 0, 0, 0, time.UTC)
	deployments := NewMemoryDeploymentStore(func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	for index := 0; index < 55; index++ {
		if _, err := deployments.CreateDeployment(
			context.Background(),
			DeploymentRecord{
				ID:            fmt.Sprintf("dep_complete_%02d", index),
				ProjectID:     string(project.ID),
				ServiceID:     string(service.ID),
				EnvironmentID: "env_complete",
				Action:        deploy.ActionDeployImage,
				OperationID:   fmt.Sprintf("op_complete_%02d", index),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:  NewMemoryOperationStore(),
		DeploymentStore: deployments,
		ProjectStore:    resources,
		ServiceStore:    resources,
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) +
		"/deployments?environment_id=env_complete&limit=7"
	cursor := ""
	seen := make(map[string]struct{})
	for {
		path := basePath
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := doDeploymentQueryRequest(router, http.MethodGet, path)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var page DeploymentResourcePage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 || len(page.Items) > 7 {
			t.Fatalf("page item count=%d, want 1..7", len(page.Items))
		}
		for _, deployment := range page.Items {
			if _, duplicate := seen[deployment.ID]; duplicate {
				t.Fatalf("deployment %s repeated across keyset pages", deployment.ID)
			}
			seen[deployment.ID] = struct{}{}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			t.Fatal("server repeated deployment cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 55 {
		t.Fatalf("deployments=%d, want complete 55-row traversal", len(seen))
	}
}

func TestDeploymentCursorIsSignedAndBoundToServiceAndEnvironment(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(
		context.Background(),
		Project{WorkspaceID: DefaultWorkspaceID, Slug: "cursor-scope"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(
		context.Background(),
		Service{ProjectID: project.ID, Slug: "web"},
	)
	if err != nil {
		t.Fatal(err)
	}
	deployments := NewMemoryDeploymentStore(time.Now)
	for _, id := range []string{"dep_a", "dep_b"} {
		if _, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
			ID:            id,
			ProjectID:     string(project.ID),
			ServiceID:     string(service.ID),
			EnvironmentID: "env_a",
			Action:        deploy.ActionDeployImage,
			OperationID:   "op_" + id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:  NewMemoryOperationStore(),
		DeploymentStore: deployments,
		ProjectStore:    resources,
		ServiceStore:    resources,
		CursorCodec:     NewCursorCodec([]byte("deployment-cursor-test-secret")),
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) + "/deployments"
	first := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"?environment_id=env_a&limit=1",
	)
	var page DeploymentResourcePage
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" ||
		strings.Contains(page.NextCursor, "dep_") ||
		strings.Contains(page.NextCursor, "env_a") {
		t.Fatalf("cursor is not opaque: %q", page.NextCursor)
	}
	for _, test := range []struct {
		name string
		path string
	}{
		{
			name: "tampered",
			path: basePath + "?environment_id=env_a&limit=1&cursor=" +
				url.QueryEscape(page.NextCursor+"x"),
		},
		{
			name: "different environment",
			path: basePath + "?environment_id=env_b&limit=1&cursor=" +
				url.QueryEscape(page.NextCursor),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := doDeploymentQueryRequest(router, http.MethodGet, test.path)
			if response.Code != http.StatusBadRequest {
				t.Fatalf(
					"status=%d body=%s, want 400",
					response.Code,
					response.Body.String(),
				)
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != failurecodes.CursorInvalid {
				t.Fatalf("code=%q, want %q", envelope.Error.Code, failurecodes.CursorInvalid)
			}
		})
	}
}

func TestCanonicalDeploymentTruthSuppressesLegacyFallback(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(
		context.Background(),
		Project{WorkspaceID: DefaultWorkspaceID, Slug: "canonical-fallback"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(
		context.Background(),
		Service{ProjectID: project.ID, Slug: "web"},
	)
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	seedDeploymentOperation(
		t,
		operations,
		service,
		"op_legacy_active",
		"dep_legacy_active",
		"inst_legacy",
		"SUCCEEDED",
		true,
		time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC),
	)
	deployments := NewMemoryDeploymentStore(time.Now)
	if _, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ID:            "dep_canonical_queued",
		ProjectID:     string(project.ID),
		ServiceID:     string(service.ID),
		EnvironmentID: "env_canonical",
		Action:        deploy.ActionDeployImage,
		OperationID:   "op_canonical_queued",
	}); err != nil {
		t.Fatal(err)
	}
	router := NewRouterWithDataStack(DataStack{
		OperationStore:  operations,
		DeploymentStore: deployments,
		ProjectStore:    resources,
		ServiceStore:    resources,
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) + "/deployments"
	for _, path := range []string{
		basePath + "/latest-active?environment_id=env_canonical",
		basePath + "/dep_legacy_active?environment_id=env_canonical",
	} {
		response := doDeploymentQueryRequest(router, http.MethodGet, path)
		if response.Code != http.StatusNotFound {
			t.Fatalf(
				"path=%s status=%d body=%s, want canonical 404 without legacy fallback",
				path,
				response.Code,
				response.Body.String(),
			)
		}
	}
	emptyEnvironment := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"?environment_id=env_legacy",
	)
	if emptyEnvironment.Code != http.StatusOK {
		t.Fatalf(
			"empty canonical environment status=%d body=%s",
			emptyEnvironment.Code,
			emptyEnvironment.Body.String(),
		)
	}
	var page DeploymentResourcePage
	if err := json.Unmarshal(emptyEnvironment.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("legacy deployments leaked after canonical flip: %#v", page.Items)
	}
}

func TestLegacyDeploymentCursorFailsClosedWhenCanonicalTruthAppears(t *testing.T) {
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(
		context.Background(),
		Project{WorkspaceID: DefaultWorkspaceID, Slug: "cursor-truth-flip"},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(
		context.Background(),
		Service{ProjectID: project.ID, Slug: "web"},
	)
	if err != nil {
		t.Fatal(err)
	}
	operations := NewMemoryOperationStore()
	for index, deploymentID := range []string{"dep_legacy_1", "dep_legacy_2"} {
		seedDeploymentOperation(
			t,
			operations,
			service,
			"op_"+deploymentID,
			deploymentID,
			"inst_"+deploymentID,
			"SUCCEEDED",
			false,
			time.Date(2026, 7, 28, 21, index, 0, 0, time.UTC),
		)
	}
	deployments := NewMemoryDeploymentStore(time.Now)
	router := NewRouterWithDataStack(DataStack{
		OperationStore:  operations,
		DeploymentStore: deployments,
		ProjectStore:    resources,
		ServiceStore:    resources,
	})
	basePath := v1alphaPrefix + "/services/" + string(service.ID) +
		"/deployments?limit=1"
	first := doDeploymentQueryRequest(router, http.MethodGet, basePath)
	if first.Code != http.StatusOK {
		t.Fatalf("first legacy page status=%d body=%s", first.Code, first.Body.String())
	}
	var page DeploymentResourcePage
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Fatalf("first legacy page=%#v, want continuation", page)
	}
	if _, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ID:            "dep_canonical",
		ProjectID:     string(project.ID),
		ServiceID:     string(service.ID),
		EnvironmentID: "env_a",
		Action:        deploy.ActionDeployImage,
	}); err != nil {
		t.Fatal(err)
	}
	second := doDeploymentQueryRequest(
		router,
		http.MethodGet,
		basePath+"&cursor="+url.QueryEscape(page.NextCursor),
	)
	if second.Code != http.StatusBadRequest {
		t.Fatalf(
			"post-flip cursor status=%d body=%s, want 400",
			second.Code,
			second.Body.String(),
		)
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(second.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != failurecodes.CursorInvalid {
		t.Fatalf("code=%q, want %q", envelope.Error.Code, failurecodes.CursorInvalid)
	}
}

func doDeploymentQueryRequest(
	handler http.Handler,
	method string,
	path string,
) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	return response
}

func seedCanonicalActiveDeployment(
	t *testing.T,
	store *MemoryDeploymentStore,
	record DeploymentRecord,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.CreateDeployment(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentBuilding(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentBuild(
		ctx,
		record.ID,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"dockerfile",
		"registry.example/"+record.ID,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentDeploying(
		ctx,
		record.ID,
		deploy.DeploymentBuilding,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(ctx, record.ID, ""); err != nil {
		t.Fatal(err)
	}
}
