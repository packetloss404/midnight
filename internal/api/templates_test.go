// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTemplatesListReturnsBuiltins(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Templates []struct {
			Slug             string   `json:"slug"`
			Name             string   `json:"name"`
			Version          string   `json:"version"`
			Summary          string   `json:"summary"`
			Tags             []string `json:"tags"`
			Provisionable    bool     `json:"provisionable"`
			CapabilityReason string   `json:"capability_reason"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, rr.Body.String())
	}
	if len(payload.Templates) != 4 {
		t.Fatalf("templates len = %d, want 4", len(payload.Templates))
	}
	wantSlugs := map[string]bool{
		"postgres": false,
		"redis":    false,
		"mysql":    false,
		"mongodb":  false,
	}
	for _, tmpl := range payload.Templates {
		if _, ok := wantSlugs[tmpl.Slug]; !ok {
			t.Fatalf("unexpected template slug %q", tmpl.Slug)
		}
		wantSlugs[tmpl.Slug] = true
		if tmpl.Version == "" || tmpl.Summary == "" {
			t.Fatalf("template %q metadata missing: %+v", tmpl.Slug, tmpl)
		}
		if tmpl.Provisionable || tmpl.CapabilityReason == "" {
			t.Fatalf("memory-mode template %q overstated runtime capability: %+v", tmpl.Slug, tmpl)
		}
	}
	for slug, found := range wantSlugs {
		if !found {
			t.Fatalf("expected template slug %q in list", slug)
		}
	}
}

type testTemplateProvisioner func(context.Context, TemplateProvisionRequest) (TemplateProvisionResult, error)

func (f testTemplateProvisioner) ProvisionTemplate(ctx context.Context, request TemplateProvisionRequest) (TemplateProvisionResult, error) {
	return f(ctx, request)
}

func TestTemplatesListReportsExecutableOfficialDatabaseTemplates(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{
		TemplateProvisioner: testTemplateProvisioner(func(context.Context, TemplateProvisionRequest) (TemplateProvisionResult, error) {
			return TemplateProvisionResult{}, nil
		}),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Templates []templateSummaryResponse `json:"templates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, tmpl := range payload.Templates {
		if !isOfficialRuntimeTemplate(tmpl.Slug) {
			t.Fatalf("unexpected non-runtime template = %+v", tmpl)
		}
		if !tmpl.Provisionable || tmpl.CapabilityReason != "" {
			t.Fatalf("%s capability = %+v", tmpl.Slug, tmpl)
		}
	}
}

func TestTemplatesGetReturnsFullManifest(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates/postgres", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"slug":"postgres"`) {
		t.Fatalf("body missing slug: %s", body)
	}
	if !strings.Contains(body, `"image":"postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"`) {
		t.Fatalf("body missing image: %s", body)
	}
	if !strings.Contains(body, `"DATABASE_URL"`) || !strings.Contains(body, `"POSTGRES_PASSWORD"`) {
		t.Fatalf("body missing env declarations: %s", body)
	}
}

func TestTemplatesGetUnknownReturns404(t *testing.T) {
	router := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/templates/sqlite", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplatesDeployRequiresIdempotencyKey(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/postgres/instances", strings.NewReader(`{"project_id":"prj_demo"}`))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplatesDeployRequiresProjectID(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/postgres/instances", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "tpl-deploy-needs-project")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplatesDeployUnknownSlugReturns404(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/sqlite/instances", strings.NewReader(`{"project_id":"prj_demo"}`))
	req.Header.Set("Idempotency-Key", "tpl-deploy-unknown")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplatesDeployPostgresMaterializesService(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	router := NewRouter()

	// Pre-create the project so the materializer can reference it.
	projectReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"demo"}`))
	projectReq.Header.Set("Idempotency-Key", "tpl-deploy-project")
	projectRR := httptest.NewRecorder()
	router.ServeHTTP(projectRR, projectReq)
	if projectRR.Code != http.StatusCreated {
		t.Fatalf("project create status = %d body=%s", projectRR.Code, projectRR.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(projectRR.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	body := `{"project_id":"` + project.ID + `","environment_id":"env_dev"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/postgres/instances", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "tpl-deploy-postgres")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplatesDeployReplaysIdempotent(t *testing.T) {
	t.Setenv(experimentalEnv, "1")
	router := NewRouter()
	projectReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"workspace_id":"wrk_default","slug":"replay"}`))
	projectReq.Header.Set("Idempotency-Key", "tpl-replay-project")
	projectRR := httptest.NewRecorder()
	router.ServeHTTP(projectRR, projectReq)
	if projectRR.Code != http.StatusCreated {
		t.Fatalf("project create status = %d body=%s", projectRR.Code, projectRR.Body.String())
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(projectRR.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	body := `{"project_id":"` + project.ID + `"}`
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/redis/instances", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "tpl-replay-redis")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		return rr
	}
	first := post()
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := post()
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("replay status = %d body=%s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("unavailable template unexpectedly reserved/replayed idempotency: %q", second.Header().Get("Idempotent-Replayed"))
	}
}

func TestTemplatesDeployFailsClosedByDefault(t *testing.T) {
	t.Setenv(experimentalEnv, "")
	router := NewRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/templates/postgres/instances", strings.NewReader(`{"project_id":"prj_demo"}`))
	req.Header.Set("Idempotency-Key", "tpl-disabled")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"code":"CAPABILITY_UNAVAILABLE"`) ||
		!strings.Contains(rr.Body.String(), `"capability":"one-click-template-deploy"`) ||
		!strings.Contains(rr.Body.String(), `"state":"unavailable"`) {
		t.Fatalf("response does not expose typed unavailable capability: %s", rr.Body.String())
	}

	servicesReq := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services", nil)
	servicesRR := httptest.NewRecorder()
	router.ServeHTTP(servicesRR, servicesReq)
	if servicesRR.Code != http.StatusOK {
		t.Fatalf("service list status = %d body=%s", servicesRR.Code, servicesRR.Body.String())
	}
	if strings.TrimSpace(servicesRR.Body.String()) != "[]" {
		t.Fatalf("disabled template deploy created service side effects: %s", servicesRR.Body.String())
	}
}
