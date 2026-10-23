// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestProjectsHandlerCreatesResourceWithProjectPrefix(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"acme"}`))
	req.Header.Set("Idempotency-Key", "create-acme")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	if !strings.HasPrefix(location, "/api/v1alpha/projects/prj_") {
		t.Fatalf("Location = %q, want prj_ prefix", location)
	}
	var project Project
	if err := json.Unmarshal(rr.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(project.ID), string(orailtypes.IDPrefixProject)+"_") {
		t.Fatalf("project id = %q, want prj_ prefix", project.ID)
	}
	if project.Slug != "acme" || project.DisplayName != "acme" {
		t.Fatalf("project = %#v, want slug acme display acme", project)
	}

	envRR := httptest.NewRecorder()
	router.ServeHTTP(envRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/environments?project_id="+string(project.ID), nil))
	if envRR.Code != http.StatusOK {
		t.Fatalf("environment list status = %d body=%s", envRR.Code, envRR.Body.String())
	}
	var environments []Environment
	if err := json.Unmarshal(envRR.Body.Bytes(), &environments); err != nil {
		t.Fatal(err)
	}
	if len(environments) != 1 || environments[0].Slug != "production" || environments[0].Tier != "production" {
		t.Fatalf("default environments = %#v, want one production environment", environments)
	}
}

func TestProjectsHandlerReplaysExactCreatedResponse(t *testing.T) {
	router := NewRouter()
	body := `{"workspace_id":"wrk_default","slug":"exact-replay"}`
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/projects", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "project-exact-replay")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	created := request()
	replayed := request()
	if created.Code != http.StatusCreated || replayed.Code != created.Code {
		t.Fatalf("created=%d replayed=%d, want 201", created.Code, replayed.Code)
	}
	if replayed.Body.String() != created.Body.String() {
		t.Fatalf("replayed body=%q, want exact %q", replayed.Body.String(), created.Body.String())
	}
	if replayed.Header().Get("Location") != created.Header().Get("Location") ||
		replayed.Header().Get("Content-Type") != created.Header().Get("Content-Type") {
		t.Fatalf("replayed headers=%v, want Location and Content-Type from %v", replayed.Header(), created.Header())
	}
	if replayed.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("Idempotent-Replayed=%q, want true", replayed.Header().Get("Idempotent-Replayed"))
	}
}

func TestServicesHandlerReplaysPostReservationValidationFailure(t *testing.T) {
	router := NewRouter()
	body := `{"project_id":"prj_missing","slug":"web"}`
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "service-invalid-parent-replay")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	failed := request()
	replayed := request()
	if failed.Code != http.StatusBadRequest || replayed.Code != failed.Code {
		t.Fatalf("failed=%d replayed=%d, want 400", failed.Code, replayed.Code)
	}
	if replayed.Body.String() != failed.Body.String() ||
		replayed.Header().Get("Content-Type") != failed.Header().Get("Content-Type") {
		t.Fatalf("replayed response=%d %v %q, want exact %d %v %q", replayed.Code, replayed.Header(), replayed.Body.String(), failed.Code, failed.Header(), failed.Body.String())
	}
	if replayed.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("Idempotent-Replayed=%q, want true", replayed.Header().Get("Idempotent-Replayed"))
	}
}

func TestProjectsHandlerRequiresIdempotencyKey(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"x"}`)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyRequired {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyRequired)
	}
}

func TestProjectsHandlerRejectsMissingSlug(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "missing-slug")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceValidationFailed)
	}
}

func TestProjectsHandlerSurfacesConflict(t *testing.T) {
	router := NewRouter()
	first := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"dup"}`))
	first.Header.Set("Idempotency-Key", "first")
	router.ServeHTTP(httptest.NewRecorder(), first)

	second := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"dup"}`))
	second.Header.Set("Idempotency-Key", "second")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, second)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceConflict {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceConflict)
	}
}

func TestProjectsHandlerGetReturnsCreatedResource(t *testing.T) {
	router := NewRouter()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"alpha","display_name":"Alpha"}`))
	createReq.Header.Set("Idempotency-Key", "create-alpha")
	createRR := httptest.NewRecorder()
	router.ServeHTTP(createRR, createReq)

	var created Project
	if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects/"+string(created.ID), nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRR.Code, getRR.Body.String())
	}
	var got Project
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.DisplayName != "Alpha" {
		t.Fatalf("got = %#v, want created %#v", got, created)
	}
}

func TestProjectsHandlerGetMissingReturnsResourceNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	NewRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects/prj_missing", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceNotFound {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceNotFound)
	}
}

func TestServicesHandlerCreatesAndListsByParent(t *testing.T) {
	router := NewRouter()
	createProjectReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"a"}`))
	createProjectReq.Header.Set("Idempotency-Key", "create-project")
	projectRR := httptest.NewRecorder()
	router.ServeHTTP(projectRR, createProjectReq)
	var project Project
	if err := json.Unmarshal(projectRR.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	createServiceReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services",
		strings.NewReader(`{"project_id":"`+string(project.ID)+`","slug":"web"}`))
	createServiceReq.Header.Set("Idempotency-Key", "create-svc")
	svcRR := httptest.NewRecorder()
	router.ServeHTTP(svcRR, createServiceReq)
	if svcRR.Code != http.StatusCreated {
		t.Fatalf("create service status = %d, body = %s", svcRR.Code, svcRR.Body.String())
	}
	var service Service
	if err := json.Unmarshal(svcRR.Body.Bytes(), &service); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(service.ID), string(orailtypes.IDPrefixService)+"_") {
		t.Fatalf("service id = %q, want svc_ prefix", service.ID)
	}

	listRR := httptest.NewRecorder()
	router.ServeHTTP(listRR, httptest.NewRequest(http.MethodGet, "/api/v1alpha/services?project_id="+string(project.ID), nil))
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRR.Code, listRR.Body.String())
	}
	var services []Service
	if err := json.Unmarshal(listRR.Body.Bytes(), &services); err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ID != service.ID {
		t.Fatalf("services = %#v, want one matching id", services)
	}
}

func TestServicesHandlerRejectsUnknownProject(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services", strings.NewReader(`{"project_id":"prj_missing","slug":"web"}`))
	req.Header.Set("Idempotency-Key", "svc-bad-project")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.ResourceValidationFailed)
	}
}

func TestServicesHandlerValidatesDocumentedServiceFields(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "uppercase slug", body: `{"slug":"Web"}`, field: "slug"},
		{name: "underscore slug", body: `{"slug":"web_api"}`, field: "slug"},
		{name: "trailing hyphen", body: `{"slug":"web-"}`, field: "slug"},
		{name: "slug over 63 characters", body: `{"slug":"` + strings.Repeat("a", 64) + `"}`, field: "slug"},
		{name: "display name over 200 characters", body: `{"slug":"web","display_name":"` + strings.Repeat("界", 201) + `"}`, field: "display_name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(tt.body))
			req.Header.Set("Idempotency-Key", "invalid-service-field")
			rr := httptest.NewRecorder()

			NewRouter().ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var got ErrorResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != failurecodes.ResourceValidationFailed || got.Error.Details["field"] != tt.field {
				t.Fatalf("error = %#v, want validation failure for %s", got.Error, tt.field)
			}
		})
	}
}

func TestServicesHandlerRejectsBodyOverLimit(t *testing.T) {
	body := `{"slug":"web","padding":"` + strings.Repeat("x", serviceCreateBodyLimitBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "oversized-service")
	rr := httptest.NewRecorder()

	NewRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s, want 413", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed ||
		got.Error.Details["limit"] != float64(serviceCreateBodyLimitBytes) {
		t.Fatalf("error = %#v, want body limit validation failure", got.Error)
	}
}

func TestServicesHandlerPreservesLegacyOperatorUnparentedCreate(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(`{"slug":"legacy-worker"}`))
	req.Header.Set("Idempotency-Key", "legacy-unparented-service")
	rr := httptest.NewRecorder()

	NewRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got Service
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "" || got.Slug != "legacy-worker" || got.DisplayName != "legacy-worker" {
		t.Fatalf("service = %#v, want legacy unparented service with normalized display name", got)
	}
}

func TestServicesAPIOperatorPathUsesServiceFieldValidation(t *testing.T) {
	services := &ServicesAPI{ServiceStore: NewMemoryResourceStore()}
	_, err := services.CreateServiceService(t.Context(), CreateServiceRequest{
		Slug:        "Bad_Slug",
		DisplayName: "Bad",
	})
	if err == nil || err.Status != http.StatusBadRequest ||
		err.Code != string(failurecodes.ResourceValidationFailed) ||
		err.Details["field"] != "slug" {
		t.Fatalf("error = %#v, want centralized slug validation failure", err)
	}
}

func TestMemoryResourceStoreEnforcesULIDPrefix(t *testing.T) {
	store := NewMemoryResourceStore()
	if _, err := store.CreateProject(context.Background(), Project{ID: "bad_id", WorkspaceID: DefaultWorkspaceID, Slug: "x"}); err == nil {
		t.Fatalf("expected create with non-prefixed id to be rejected")
	}
}
