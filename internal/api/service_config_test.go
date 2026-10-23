// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/serviceconfig"
)

func TestServiceConfigRESTDefaultsThenCASPersists(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{
		ID: "prj_config", WorkspaceID: DefaultWorkspaceID, Slug: "config", DisplayName: "Config",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := resources.CreateService(ctx, Service{
		ID: "svc_config", ProjectID: project.ID, Slug: "web", DisplayName: "Web",
	})
	if err != nil {
		t.Fatal(err)
	}
	environments := NewMemoryEnvironmentStore(nil)
	environment, err := environments.CreateEnvironment(ctx, Environment{
		ID: "env_config", ProjectID: project.ID, Slug: "production", Name: "Production",
	})
	if err != nil {
		t.Fatal(err)
	}
	configs := &serviceConfigStoreStub{}
	router := NewRouterWithDataStack(DataStack{
		ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments,
		MutationScopeResolver: NewMutationScopeResolver(resources, resources, environments),
		ServiceConfigStore:    configs, IdempotencyStore: idempotency.NewMemoryStore(),
	})
	path := v1alphaPrefix + "/services/" + string(service.ID) + "/environments/" + string(environment.ID) + "/config"

	get := httptest.NewRecorder()
	router.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	if get.Header().Get("ETag") != `"0"` {
		t.Fatalf("GET ETag=%q, want %q", get.Header().Get("ETag"), `"0"`)
	}
	var defaults ServiceConfigView
	if err := json.Unmarshal(get.Body.Bytes(), &defaults); err != nil {
		t.Fatal(err)
	}
	if defaults.Explicit || defaults.Version != 0 ||
		defaults.TargetPort != serviceconfig.DefaultTargetPort ||
		defaults.HealthKind != serviceconfig.HealthTCP {
		t.Fatalf("defaults = %#v", defaults)
	}

	body := `{
		"expected_version":0,
		"builder":"railpack",
		"dockerfile_path":"Dockerfile",
		"build_command":"npm run build",
		"start_command":"node server.js",
		"target_port":3000,
		"health_kind":"http",
		"health_path":"/health",
		"health_timeout_seconds":120,
		"health_interval_seconds":2,
		"restart_policy":"unless-stopped",
		"replicas":1,
		"overlap_enabled":true,
		"drain_grace_seconds":20
	}`
	putRequest := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(body))
	setTestIfMatchFromExpectedVersion(t, putRequest, body)
	putRequest.Header.Set("Idempotency-Key", "service-config-create")
	put := httptest.NewRecorder()
	router.ServeHTTP(put, putRequest)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", put.Code, put.Body.String())
	}
	if configs.expected != 0 || configs.saved.ProjectID != string(project.ID) ||
		configs.saved.BuildCommand != "npm run build" || configs.saved.TargetPort != 3000 {
		t.Fatalf("saved=%#v expected=%d", configs.saved, configs.expected)
	}

	tooMany := strings.Replace(body, `"expected_version":0`, `"expected_version":1`, 1)
	tooMany = strings.Replace(tooMany, `"replicas":1`, `"replicas":51`, 1)
	invalid := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewBufferString(tooMany))
	setTestIfMatchFromExpectedVersion(t, request, tooMany)
	request.Header.Set("Idempotency-Key", "service-config-too-many-replicas")
	router.ServeHTTP(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("PUT replicas=51 status=%d body=%s, want 400", invalid.Code, invalid.Body.String())
	}
}

type serviceConfigStoreStub struct {
	saved    serviceconfig.Config
	expected int64
}

func (s *serviceConfigStoreStub) Get(context.Context, string, string, string) (serviceconfig.Config, error) {
	if s.saved.Version == 0 {
		return serviceconfig.Config{}, serviceconfig.ErrNotFound
	}
	return s.saved, nil
}

func (s *serviceConfigStoreStub) Put(_ context.Context, cfg serviceconfig.Config, expected int64) (serviceconfig.Config, error) {
	s.expected = expected
	cfg = serviceconfig.Normalize(cfg)
	if err := serviceconfig.Validate(cfg); err != nil {
		return serviceconfig.Config{}, err
	}
	cfg.Version = expected + 1
	s.saved = cfg
	return cfg, nil
}
