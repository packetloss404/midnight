// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/observability"
)

type observabilityHealthFixture struct {
	observability.Store
	page       observability.LogPage
	health     []observability.CollectorHealth
	healthErr  error
	askedKinds []string
}

func (s *observabilityHealthFixture) QueryLogs(context.Context, observability.LogQuery) (observability.LogPage, error) {
	return s.page, nil
}

func (s *observabilityHealthFixture) CollectorHealth(_ context.Context, kinds []string, _ time.Duration) ([]observability.CollectorHealth, error) {
	s.askedKinds = append([]string(nil), kinds...)
	return s.health, s.healthErr
}

func TestObservabilityLogsExposeSurfaceRelevantCollectorHealth(t *testing.T) {
	store := &observabilityHealthFixture{
		page: observability.LogPage{Records: []observability.LogRecord{}},
		health: []observability.CollectorHealth{{
			Kind: observability.CollectorKindDocker, Status: "unavailable", HeartbeatGap: true,
			Reason: "collector has not reported a heartbeat",
		}},
	}
	handler := observabilityLogsHandler(
		store,
		NewCursorCodec([]byte("0123456789abcdef0123456789abcdef")),
		func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/observability/logs?project_id=prj_a&surface=build,runtime",
		nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !slices.Equal(store.askedKinds, []string{observability.CollectorKindDocker}) {
		t.Fatalf("collector kinds=%v, want Docker only", store.askedKinds)
	}
	var payload observabilityLogsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.CollectorHealth) != 1 || !payload.CollectorHealth[0].HeartbeatGap ||
		payload.CollectorHealth[0].Kind != observability.CollectorKindDocker {
		t.Fatalf("collector health=%#v", payload.CollectorHealth)
	}
}

func TestHumanObservabilityReadsConcealUnauthorizedProjectScopes(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	router := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, manager, "alice@example.com")

	for _, path := range []string{
		v1alphaPrefix + "/observability/logs?project_id=prj_b",
		v1alphaPrefix + "/observability/logs/stream?project_id=prj_b",
		v1alphaPrefix + "/observability/metrics?project_id=prj_b&service_id=svc_b",
	} {
		response := authenticatedIdentityRequest(router, http.MethodGet, path, alice)
		if response.Code != http.StatusNotFound ||
			!strings.Contains(response.Body.String(), `"code":"`+string(failurecodes.ResourceNotFound)+`"`) {
			t.Fatalf("unauthorized observability read %s: status=%d body=%s", path, response.Code, response.Body.String())
		}
	}

	authorized := authenticatedIdentityRequest(
		router, http.MethodGet, v1alphaPrefix+"/observability/logs?project_id=prj_a", alice,
	)
	if authorized.Code != http.StatusServiceUnavailable ||
		!strings.Contains(authorized.Body.String(), "durable observability store is not configured") {
		t.Fatalf("authorized project did not reach observability handler: status=%d body=%s", authorized.Code, authorized.Body.String())
	}
}
