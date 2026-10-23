// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantDeployTestImage = "registry.example/acme/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type recordingTenantDeployCoordinator struct {
	mu       sync.Mutex
	requests []TenantImageDeployRequest
	result   TenantDeployIntakeResult
	err      error
	run      func(int, TenantImageDeployRequest) (TenantDeployIntakeResult, error)
}

func (c *recordingTenantDeployCoordinator) DeployImmutableImage(
	_ context.Context,
	request TenantImageDeployRequest,
) (TenantDeployIntakeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	if c.run != nil {
		return c.run(len(c.requests), request)
	}
	return c.result, c.err
}

func (c *recordingTenantDeployCoordinator) captured() []TenantImageDeployRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]TenantImageDeployRequest(nil), c.requests...)
}

func TestCreateTenantImageDeployPassesExactAuthorityTupleAndReplaysStoredResponse(t *testing.T) {
	response := idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type":              []string{"application/json"},
			"Location":                  []string{v1alphaPrefix + "/operations/op_human"},
			"X-Midnight-Delivery-State": []string{"pending"},
		},
		Body: []byte("{\"id\":\"op_human\",\"kind\":\"deploy\",\"state\":\"QUEUED\"}\n"),
	}
	coordinator := &recordingTenantDeployCoordinator{
		run: func(call int, _ TenantImageDeployRequest) (TenantDeployIntakeResult, error) {
			return TenantDeployIntakeResult{Response: response, Replayed: call > 1}, nil
		},
	}
	body := "{\n  \"environment_id\":\"env_prod\",\n  \"image\":\"" + tenantDeployTestImage + "\"\n}"

	first := serveTenantImageDeploy(t, coordinator, http.MethodPost, "svc_human", body, []string{"deploy-human-1"})
	second := serveTenantImageDeploy(t, coordinator, http.MethodPost, "svc_human", body, []string{"deploy-human-1"})
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("statuses = %d/%d bodies=%s/%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if first.Body.String() != string(response.Body) || second.Body.String() != string(response.Body) {
		t.Fatalf("stored response was not replayed exactly: first=%q second=%q", first.Body.String(), second.Body.String())
	}
	if first.Header().Get("Idempotent-Replayed") != "" ||
		second.Header().Get("Idempotent-Replayed") != "true" ||
		second.Header().Get("Location") != response.Header.Get("Location") ||
		second.Header().Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("response headers first=%v second=%v", first.Header(), second.Header())
	}

	requests := coordinator.captured()
	if len(requests) != 2 {
		t.Fatalf("coordinator calls = %d, want 2", len(requests))
	}
	got := requests[0]
	if got.Principal.UserID != "usr_human" ||
		got.Session.ID != "ses_human" ||
		got.ServiceID != "svc_human" ||
		got.Intent.EnvironmentID != "env_prod" ||
		got.Intent.Image != tenantDeployTestImage ||
		got.Key != "deploy-human-1" {
		t.Fatalf("coordinator request = %#v", got)
	}
	if got.Fingerprint.Method != http.MethodPost ||
		got.Fingerprint.Path != v1alphaPrefix+"/services/svc_human/deployments" ||
		got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) {
		t.Fatalf("fingerprint = %#v", got.Fingerprint)
	}
	if got.SourceIP != "192.0.2.80:443" || got.UserAgent != "midnight-human-test" {
		t.Fatalf("request evidence source=%q user-agent=%q", got.SourceIP, got.UserAgent)
	}
}

func TestCreateTenantImageDeployPassesOnlyGeneratedPublicRouteOptIn(t *testing.T) {
	coordinator := &recordingTenantDeployCoordinator{
		result: TenantDeployIntakeResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusAccepted,
				Body:       []byte("{\"id\":\"op_generated\"}\n"),
			},
		},
	}
	body := `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{"public":true}}`
	response := serveTenantImageDeploy(
		t,
		coordinator,
		http.MethodPost,
		"svc_human",
		body,
		[]string{"deploy-generated-1"},
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	requests := coordinator.captured()
	if len(requests) != 1 ||
		requests[0].Intent.Route == nil ||
		!requests[0].Intent.Route.Public ||
		requests[0].Intent.Route.Name != "" ||
		requests[0].Intent.Route.Group != "" ||
		requests[0].Intent.Route.Host != "" ||
		requests[0].Intent.Route.Path != "" ||
		requests[0].Intent.Route.TLS {
		t.Fatalf("generated route request = %#v", requests)
	}
	if requests[0].Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) {
		t.Fatalf("fingerprint = %#v", requests[0].Fingerprint)
	}
}

func TestCreateTenantImageDeployRejectsNonCanonicalTransportBeforeCoordinator(t *testing.T) {
	valid := `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `"}`
	uppercaseDigest := "registry.example/acme/web@sha256:" + strings.Repeat("A", 64)
	cases := []struct {
		name       string
		body       string
		keys       []string
		wantStatus int
	}{
		{name: "missing key", body: valid, wantStatus: http.StatusBadRequest},
		{name: "duplicate key", body: valid, keys: []string{"key-one", "key-two"}, wantStatus: http.StatusBadRequest},
		{name: "comma coalesced duplicate key", body: valid, keys: []string{"key-one, key-two"}, wantStatus: http.StatusBadRequest},
		{name: "invalid key", body: valid, keys: []string{"key with spaces"}, wantStatus: http.StatusBadRequest},
		{name: "empty body", body: "", keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "null body", body: "null", keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "unknown source", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","source":{"repo":"x","ref":"main"}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route false", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{"public":false}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route empty", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route null", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":null}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route custom host", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{"public":true,"host":"app.example.test"}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route custom path", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{"public":true,"path":"/api"}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "route tls", body: `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `","route":{"public":true,"tls":true}}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "trailing document", body: valid + `{}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "mutable image", body: `{"environment_id":"env_prod","image":"registry.example/acme/web:latest"}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "malformed digest", body: `{"environment_id":"env_prod","image":"registry.example/acme/web@sha256:abc"}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "uppercase digest", body: `{"environment_id":"env_prod","image":"` + uppercaseDigest + `"}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "missing environment", body: `{"image":"` + tenantDeployTestImage + `"}`, keys: []string{"key-one"}, wantStatus: http.StatusBadRequest},
		{name: "oversize", body: strings.Repeat("x", int(tenantImageDeployBodyLimitBytes)+1), keys: []string{"key-one"}, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coordinator := &recordingTenantDeployCoordinator{}
			response := serveTenantImageDeploy(t, coordinator, http.MethodPost, "svc_human", tc.body, tc.keys)
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if calls := len(coordinator.captured()); calls != 0 {
				t.Fatalf("coordinator calls=%d, want zero", calls)
			}
		})
	}
}

func TestCreateTenantImageDeployMapsCoordinatorErrorsWithoutLeaks(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		wantStatus    int
		wantCode      string
		wantChallenge bool
		wantRetry     bool
	}{
		{name: "session", err: identity.ErrSessionInvalid, wantStatus: http.StatusUnauthorized, wantCode: "AUTH_SESSION_INVALID", wantChallenge: true},
		{name: "foreign or restricted", err: identity.ErrAccessNotFound, wantStatus: http.StatusNotFound, wantCode: "RESOURCE_NOT_FOUND"},
		{name: "role denied", err: identity.ErrAuthorizationDenied, wantStatus: http.StatusForbidden, wantCode: "AUTHORIZATION_DENIED"},
		{name: "in progress", err: idempotency.ErrRequestInProgress, wantStatus: http.StatusConflict, wantCode: "IDEMPOTENCY_REQUEST_IN_PROGRESS", wantRetry: true},
		{name: "different terminal body", err: idempotency.ErrKeyReusedDifferentBody, wantStatus: http.StatusConflict, wantCode: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY"},
		{name: "different in-flight request", err: idempotency.ErrKeyReused, wantStatus: http.StatusConflict, wantCode: "IDEMPOTENCY_KEY_REUSED"},
		{name: "dependency", err: ErrTenantDeployIntakeUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "INTERNAL_ERROR"},
		{name: "generated route config", err: ErrTenantGeneratedRouteUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "CAPABILITY_UNAVAILABLE"},
		{name: "retired generated route", err: ErrTenantGeneratedDomainRetired, wantStatus: http.StatusConflict, wantCode: "RESOURCE_CONFLICT"},
		{name: "internal", err: errors.New("secret postgres detail"), wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			coordinator := &recordingTenantDeployCoordinator{err: tc.err}
			response := serveTenantImageDeploy(
				t,
				coordinator,
				http.MethodPost,
				"svc_human",
				`{"environment_id":"env_prod","image":"`+tenantDeployTestImage+`"}`,
				[]string{"key-one"},
			)
			if response.Code != tc.wantStatus || !strings.Contains(response.Body.String(), `"code":"`+tc.wantCode+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret postgres detail") {
				t.Fatalf("internal error leaked: %s", response.Body.String())
			}
			if tc.wantChallenge != (response.Header().Get("WWW-Authenticate") == "Bearer") {
				t.Fatalf("WWW-Authenticate=%q", response.Header().Get("WWW-Authenticate"))
			}
			if tc.wantRetry != (response.Header().Get("Retry-After") == "1") {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
		})
	}
}

func TestCreateTenantImageDeployRequiresHumanContextAndCoordinator(t *testing.T) {
	body := `{"environment_id":"env_prod","image":"` + tenantDeployTestImage + `"}`
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services/svc_human/deployments", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "key-one")
	response := httptest.NewRecorder()
	createTenantImageDeploy(response, request, "svc_human", &recordingTenantDeployCoordinator{})
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("missing context status=%d header=%q body=%s", response.Code, response.Header().Get("WWW-Authenticate"), response.Body.String())
	}

	response = serveTenantImageDeploy(t, nil, http.MethodPost, "svc_human", body, []string{"key-one"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil coordinator status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHumanServiceSubrouteBoundaryAdmitsOnlyInstalledImageDeployCollection(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, enabled := range []bool{false, true} {
		request := tenantDeployBoundaryRequest(http.MethodPost, v1alphaPrefix+"/services/svc_test/deployments")
		response := httptest.NewRecorder()
		humanServiceDeploymentReadBoundaryMiddleware(next, enabled).ServeHTTP(response, request)
		want := http.StatusForbidden
		if enabled {
			want = http.StatusNoContent
		}
		if response.Code != want {
			t.Fatalf("enabled=%v status=%d body=%s", enabled, response.Code, response.Body.String())
		}
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, v1alphaPrefix + "/services/svc_test/up"},
		{http.MethodPost, v1alphaPrefix + "/services/svc_test/github"},
		{http.MethodPut, v1alphaPrefix + "/services/svc_test/github"},
		{http.MethodPost, v1alphaPrefix + "/services/svc_test/deployments/dep_test/actions"},
		{http.MethodPost, v1alphaPrefix + "/services/svc_test/deployments/dep_test"},
		{http.MethodDelete, v1alphaPrefix + "/services/svc_test/deployments"},
	} {
		response := httptest.NewRecorder()
		humanServiceDeploymentReadBoundaryMiddleware(next, true).ServeHTTP(
			response,
			tenantDeployBoundaryRequest(route.method, route.path),
		)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
}

func TestRouterAdmitsOnlyHumanImmutableImageDeployAndPreservesOperatorDispatch(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	coordinator := &recordingTenantDeployCoordinator{
		result: TenantDeployIntakeResult{Response: idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte("{\"id\":\"op_router\",\"kind\":\"deploy\",\"state\":\"QUEUED\"}\n"),
		}},
	}
	stack.TenantDeployIntake = coordinator
	handler := NewRouterWithDataStack(stack)
	alice := loginIdentityForTest(t, manager, "alice@example.com")

	human := authenticatedTenantDeployRequest(
		handler,
		alice,
		v1alphaPrefix+"/services/svc_a/deployments",
		`{"environment_id":"env_a","image":"`+tenantDeployTestImage+`"}`,
		"human-router-image",
	)
	if human.Code != http.StatusAccepted || len(coordinator.captured()) != 1 {
		t.Fatalf("human image status=%d calls=%d body=%s", human.Code, len(coordinator.captured()), human.Body.String())
	}

	source := authenticatedTenantDeployRequest(
		handler,
		alice,
		v1alphaPrefix+"/services/svc_a/deployments",
		`{"environment_id":"env_a","source":{"repo":"https://example.test/repo.git","ref":"main"}}`,
		"human-router-source",
	)
	if source.Code != http.StatusBadRequest || len(coordinator.captured()) != 1 {
		t.Fatalf("human source status=%d calls=%d body=%s", source.Code, len(coordinator.captured()), source.Body.String())
	}
	for _, path := range []string{
		v1alphaPrefix + "/services/svc_a/up",
		v1alphaPrefix + "/services/svc_a/github",
		v1alphaPrefix + "/services/svc_a/deployments/dep_test/actions",
	} {
		denied := authenticatedTenantDeployRequest(handler, alice, path, `{}`, "human-router-denied")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("human %s status=%d body=%s", path, denied.Code, denied.Body.String())
		}
	}
	if calls := len(coordinator.captured()); calls != 1 {
		t.Fatalf("closed human routes reached coordinator %d times, want 1", calls)
	}

	operator := authenticatedTenantDeployRequest(
		handler,
		"operator-recovery-token",
		v1alphaPrefix+"/services/svc_a/deployments",
		`{"environment_id":"env_a","image":"`+tenantDeployTestImage+`"}`,
		"operator-router-image",
	)
	if operator.Code != http.StatusServiceUnavailable ||
		!strings.Contains(operator.Body.String(), "deploy intake queue is not wired") {
		t.Fatalf("legacy operator dispatch changed: status=%d body=%s", operator.Code, operator.Body.String())
	}
	if calls := len(coordinator.captured()); calls != 1 {
		t.Fatalf("operator request reached human coordinator: calls=%d", calls)
	}
}

func serveTenantImageDeploy(
	t *testing.T,
	coordinator TenantDeployIntakeCoordinator,
	method string,
	serviceID orailtypes.ID,
	body string,
	keys []string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, tenantDeployIdempotencyPath(serviceID), strings.NewReader(body))
	request.RemoteAddr = "192.0.2.80:443"
	request.Header.Set("User-Agent", "midnight-human-test")
	for _, key := range keys {
		request.Header.Add("Idempotency-Key", key)
	}
	principal := identity.Principal{Kind: "user", UserID: "usr_human", SessionID: "ses_human", AuthMethod: "password"}
	session := identity.Session{ID: "ses_human", UserID: "usr_human", SessionGeneration: 1}
	ctx := withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal})
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	createTenantImageDeploy(response, request, serviceID, coordinator)
	return response
}

func tenantDeployBoundaryRequest(method, path string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	return request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman}))
}

func authenticatedTenantDeployRequest(handler http.Handler, token, path, body, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
