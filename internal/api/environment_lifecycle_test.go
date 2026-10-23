// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
)

func TestEnvironmentLifecycleCreateUpdateDuplicateAndDelete(t *testing.T) {
	router := NewRouter()
	project := createEnvironmentLifecycleProject(t, router)
	create := func(body string) Environment {
		rr := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/environments", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "environment-"+idempotency.BodyHashSHA256([]byte(body)))
		router.ServeHTTP(rr, request)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
		}
		var env Environment
		if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	env := create(`{"project_id":"` + project + `","slug":"staging","name":"Staging","tier":"staging"}`)
	rr := httptest.NewRecorder()
	updateRequest := httptest.NewRequest(http.MethodPatch, "/api/v1alpha/environments/"+string(env.ID), strings.NewReader(`{"name":"Preprod","is_protected":true}`))
	updateRequest.Header.Set("If-Match", `"1"`)
	router.ServeHTTP(rr, updateRequest)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	var updated Environment
	_ = json.Unmarshal(rr.Body.Bytes(), &updated)
	if updated.Name != "Preprod" || !updated.IsProtected {
		t.Fatalf("updated=%#v", updated)
	}

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/environments/"+string(env.ID)+"/duplicate", strings.NewReader(`{"slug":"staging-copy"}`)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("duplicate status=%d body=%s", rr.Code, rr.Body.String())
	}
	var duplicate Environment
	_ = json.Unmarshal(rr.Body.Bytes(), &duplicate)
	if duplicate.ProjectID != env.ProjectID || duplicate.Slug != "staging-copy" || duplicate.IsProtected {
		t.Fatalf("duplicate=%#v", duplicate)
	}

	rr = httptest.NewRecorder()
	deleteProtected := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/environments/"+string(env.ID), nil)
	deleteProtected.Header.Set("If-Match", `"2"`)
	router.ServeHTTP(rr, deleteProtected)
	if rr.Code != http.StatusConflict {
		t.Fatalf("protected delete status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	deleteDuplicate := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/environments/"+string(duplicate.ID), nil)
	deleteDuplicate.Header.Set("If-Match", `"1"`)
	router.ServeHTTP(rr, deleteDuplicate)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEnvironmentDuplicateConfigIsExplicitlyUnavailable(t *testing.T) {
	router := NewRouter()
	project := createEnvironmentLifecycleProject(t, router)
	rr := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/environments", strings.NewReader(`{"project_id":"`+project+`","slug":"dev"}`))
	request.Header.Set("Idempotency-Key", "environment-dev")
	router.ServeHTTP(rr, request)
	var env Environment
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1alpha/environments/"+string(env.ID)+"/duplicate", strings.NewReader(`{"slug":"next","copy_config":true}`)))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"state":"unavailable"`) {
		t.Fatalf("config duplicate status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEnvironmentDuplicateConfigUsesDurableCapabilityAndReturnsHonestCounts(t *testing.T) {
	environments := NewMemoryEnvironmentStore(nil)
	source, err := environments.CreateEnvironment(context.Background(), Environment{
		ID: "env_source", ProjectID: "prj_one", Slug: "staging", Name: "Staging", Tier: "staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &environmentDuplicatorStub{result: EnvironmentDuplicateResult{
		Environment:            Environment{ID: "env_target", ProjectID: source.ProjectID, Slug: "qa", Name: "QA", Tier: "staging"},
		CopyConfig:             true,
		CopiedServiceConfigs:   2,
		CopiedVariables:        3,
		CopiedVariableVersions: 5,
		OmittedResources: []EnvironmentDuplicateOmission{
			{Resource: "domains", Count: 1, Reason: "create explicitly"},
			{Resource: "volumes", Count: 0, Reason: "create explicitly"},
			{Resource: "deployments", Count: 4, Reason: "history stays in source"},
		},
	}}
	handler := environmentsHandler(environments, fake)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/environments/"+string(source.ID)+"/duplicate",
		strings.NewReader(`{"slug":"qa","name":"QA","copy_config":true}`),
	)
	request.Header.Set("Idempotency-Key", "duplicate-qa")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if fake.request.SourceID != source.ID || fake.request.Slug != "qa" ||
		fake.request.IdempotencyKey != idempotency.Key("duplicate-qa") {
		t.Fatalf("duplicator request=%#v", fake.request)
	}
	var result EnvironmentDuplicateResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "env_target" || result.CopiedVariables != 3 ||
		result.CopiedVariableVersions != 5 || len(result.OmittedResources) != 3 {
		t.Fatalf("result=%#v", result)
	}
	if strings.Contains(response.Body.String(), "ciphertext") ||
		strings.Contains(response.Body.String(), "wrapped_dek") ||
		strings.Contains(response.Body.String(), "first-value") {
		t.Fatalf("response exposed encrypted/plaintext material: %s", response.Body.String())
	}
}

func TestEnvironmentDuplicateRejectsUnsafeResourceCloneFlags(t *testing.T) {
	environments := NewMemoryEnvironmentStore(nil)
	source, err := environments.CreateEnvironment(context.Background(), Environment{
		ID: "env_source", ProjectID: "prj_one", Slug: "staging", Name: "Staging", Tier: "staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	environmentsHandler(environments).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1alpha/environments/"+string(source.ID)+"/duplicate",
			strings.NewReader(`{"slug":"unsafe","copy_volumes":true,"copy_deployments":true}`),
		),
	)
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"unsupported_features"`) ||
		!strings.Contains(response.Body.String(), `"volumes"`) ||
		!strings.Contains(response.Body.String(), `"deployments"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEnvironmentDuplicateConfigRequiresIdempotencyKey(t *testing.T) {
	environments := NewMemoryEnvironmentStore(nil)
	source, err := environments.CreateEnvironment(context.Background(), Environment{
		ID: "env_source", ProjectID: "prj_one", Slug: "staging", Name: "Staging", Tier: "staging",
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &environmentDuplicatorStub{}
	response := httptest.NewRecorder()
	environmentsHandler(environments, fake).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/api/v1alpha/environments/"+string(source.ID)+"/duplicate",
			strings.NewReader(`{"slug":"qa","copy_config":true}`),
		),
	)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"code":"IDEMPOTENCY_KEY_REQUIRED"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if fake.request.SourceID != "" {
		t.Fatalf("duplicator was called: %#v", fake.request)
	}
}

type environmentDuplicatorStub struct {
	request EnvironmentDuplicateRequest
	result  EnvironmentDuplicateResult
	err     error
}

func (s *environmentDuplicatorStub) DuplicateEnvironment(_ context.Context, request EnvironmentDuplicateRequest) (EnvironmentDuplicateResult, error) {
	s.request = request
	return s.result, s.err
}

var _ EnvironmentDuplicator = (*environmentDuplicatorStub)(nil)

func createEnvironmentLifecycleProject(t *testing.T, router http.Handler) string {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"env-lifecycle"}`))
	req.Header.Set("Idempotency-Key", "env-lifecycle-project")
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("project status=%d body=%s", rr.Code, rr.Body.String())
	}
	var project Project
	if err := json.Unmarshal(rr.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	return string(project.ID)
}
