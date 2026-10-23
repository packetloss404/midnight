// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
)

func TestTenantRouteMutationsAcquireEnvironmentLockBeforeEntityLocks(t *testing.T) {
	domainSource, err := os.ReadFile("tenant_generated_domains_pg.go")
	if err != nil {
		t.Fatal(err)
	}
	deploySource, err := os.ReadFile("tenant_deploy_intake_pg.go")
	if err != nil {
		t.Fatal(err)
	}
	assertOrder := func(
		name string,
		source string,
		function string,
		later string,
	) {
		t.Helper()
		start := strings.Index(source, "func "+function)
		if start < 0 {
			t.Fatalf("%s function %s is missing", name, function)
		}
		next := strings.Index(source[start+len("func "):], "\nfunc ")
		end := len(source)
		if next >= 0 {
			end = start + len("func ") + next
		}
		body := source[start:end]
		lockIndex := strings.Index(
			body,
			"router.AcquireRouteEnvironmentMutationLock(",
		)
		laterIndex := strings.Index(body, later)
		if lockIndex < 0 || laterIndex < 0 || lockIndex >= laterIndex {
			t.Fatalf(
				"%s lock order invalid: lock=%d later=%d",
				name,
				lockIndex,
				laterIndex,
			)
		}
	}
	assertOrder(
		"generated domain create",
		string(domainSource),
		"(c *PostgresTenantMutationCoordinator) createTenantGeneratedDomainInTx(",
		"listTenantGeneratedDomainViewsInTx(",
	)
	assertOrder(
		"generated domain delete",
		string(domainSource),
		"(c *PostgresTenantMutationCoordinator) deleteTenantGeneratedDomainInTx(",
		"listTenantGeneratedDomainViewsInTx(",
	)
	assertOrder(
		"deploy intake",
		string(deploySource),
		"(c *PostgresTenantDeployIntakeCoordinator) deployTenantIntentInTx(",
		"lockTenantDeployActiveState(",
	)
}

type tenantGeneratedDomainCoordinatorFake struct {
	listResult   []TenantGeneratedDomainView
	getResult    TenantGeneratedDomainView
	createResult TenantMutationResult
	deleteResult TenantMutationResult
	err          error

	listRequest   TenantGeneratedDomainReadRequest
	getRequest    TenantGeneratedDomainReadRequest
	createRequest TenantGeneratedDomainCreateRequest
	deleteRequest TenantGeneratedDomainDeleteRequest
}

func (f *tenantGeneratedDomainCoordinatorFake) ListGeneratedDomains(
	_ context.Context,
	request TenantGeneratedDomainReadRequest,
) ([]TenantGeneratedDomainView, error) {
	f.listRequest = request
	return f.listResult, f.err
}

func (f *tenantGeneratedDomainCoordinatorFake) GetGeneratedDomain(
	_ context.Context,
	request TenantGeneratedDomainReadRequest,
) (TenantGeneratedDomainView, error) {
	f.getRequest = request
	return f.getResult, f.err
}

func (f *tenantGeneratedDomainCoordinatorFake) CreateGeneratedDomain(
	_ context.Context,
	request TenantGeneratedDomainCreateRequest,
) (TenantMutationResult, error) {
	f.createRequest = request
	return f.createResult, f.err
}

func (f *tenantGeneratedDomainCoordinatorFake) DeleteGeneratedDomain(
	_ context.Context,
	request TenantGeneratedDomainDeleteRequest,
) (TenantMutationResult, error) {
	f.deleteRequest = request
	return f.deleteResult, f.err
}

func TestTenantGeneratedDomainsHandlerHumanListAndGet(t *testing.T) {
	now := time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC)
	view := TenantGeneratedDomainView{
		ID: "dom_test", WorkspaceID: "wsp_test", ProjectID: "prj_test",
		ServiceID: "svc_test", EnvironmentID: "env_test",
		Kind: router.DomainKindGenerated, Hostname: "svc.apps.example.test",
		Path: "/", TargetPort: 8080, Public: true, TLS: false, Version: 7,
		RouteApplyState: router.RouteApplyPending, RouteReachable: false,
		CreatedAt: now, UpdatedAt: now,
	}
	fake := &tenantGeneratedDomainCoordinatorFake{getResult: view}
	handler := tenantGeneratedDomainsHandler(fake)

	listRequest := tenantGeneratedDomainRequest(t, http.MethodGet, "", "", "")
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK || strings.TrimSpace(listRecorder.Body.String()) != "[]" {
		t.Fatalf("list response = %d %q, want 200 []", listRecorder.Code, listRecorder.Body.String())
	}
	if fake.listRequest.ServiceID != "svc_test" || fake.listRequest.EnvironmentID != "env_test" ||
		fake.listRequest.Principal.UserID != "usr_test" {
		t.Fatalf("list request = %#v", fake.listRequest)
	}

	getRequest := tenantGeneratedDomainRequest(t, http.MethodGet, "dom_test", "", "")
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK || getRecorder.Header().Get("ETag") != `"7"` {
		t.Fatalf("get response = %d etag=%q body=%q", getRecorder.Code, getRecorder.Header().Get("ETag"), getRecorder.Body.String())
	}
	for _, required := range []string{
		`"kind":"generated"`,
		`"path":"/"`,
		`"target_port":8080`,
		`"tls":false`,
		`"route_reachable":false`,
	} {
		if !strings.Contains(getRecorder.Body.String(), required) {
			t.Fatalf("get response missing %s: %s", required, getRecorder.Body.String())
		}
	}
	if fake.getRequest.DomainID != "dom_test" {
		t.Fatalf("get domain id = %q", fake.getRequest.DomainID)
	}
}

func TestTenantGeneratedDomainsHandlerCreateWritesExactReplay(t *testing.T) {
	storedBody := []byte("{\"id\":\"dom_test\",\"version\":1}\n")
	fake := &tenantGeneratedDomainCoordinatorFake{
		createResult: TenantMutationResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusCreated,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"Location":     []string{"/api/v1alpha/services/svc_test/environments/env_test/domains/dom_test"},
				},
				Body: storedBody,
			},
			Replayed: true,
		},
	}
	request := tenantGeneratedDomainRequest(t, http.MethodPost, "", "{}", "domain-create-test")
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("User-Agent", "midnight-test")
	recorder := httptest.NewRecorder()
	tenantGeneratedDomainsHandler(fake).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated || recorder.Body.String() != string(storedBody) {
		t.Fatalf("create response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Location") == "" || recorder.Header().Get("ETag") != "" ||
		recorder.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("create headers = %#v", recorder.Header())
	}
	got := fake.createRequest.TenantGeneratedDomainMutationMeta
	if got.Method != http.MethodPost || got.Key != "domain-create-test" ||
		string(got.RawBody) != "{}" || got.ServiceID != "svc_test" ||
		got.EnvironmentID != "env_test" || got.DomainID != "" ||
		got.SourceIP != "192.0.2.10:1234" || got.UserAgent != "midnight-test" {
		t.Fatalf("create request = %#v", fake.createRequest)
	}
}

func TestTenantGeneratedDomainsHandlerDeleteRequiresPositiveVersion(t *testing.T) {
	fake := &tenantGeneratedDomainCoordinatorFake{
		deleteResult: TenantMutationResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{},
			},
		},
	}
	handler := tenantGeneratedDomainsHandler(fake)

	for _, body := range []string{`{}`, `{"expected_version":0}`, `{"expected_version":null}`} {
		request := tenantGeneratedDomainRequest(t, http.MethodDelete, "dom_test", body, "domain-delete-invalid")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("delete body %q response = %d %q", body, recorder.Code, recorder.Body.String())
		}
	}

	request := tenantGeneratedDomainRequest(t, http.MethodDelete, "dom_test", `{"expected_version":9}`, "domain-delete-test")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Body.Len() != 0 {
		t.Fatalf("delete response = %d %q", recorder.Code, recorder.Body.String())
	}
	if fake.deleteRequest.ExpectedVersion != 9 ||
		fake.deleteRequest.DomainID != "dom_test" ||
		fake.deleteRequest.Key != "domain-delete-test" {
		t.Fatalf("delete request = %#v", fake.deleteRequest)
	}
}

func TestTenantGeneratedDomainsHandlerRejectsNonHumanAndInvalidBodies(t *testing.T) {
	handler := tenantGeneratedDomainsHandler(&tenantGeneratedDomainCoordinatorFake{})

	unclassified := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/svc_test/environments/env_test/domains", nil)
	unclassified.SetPathValue("service_id", "svc_test")
	unclassified.SetPathValue("environment_id", "env_test")
	unclassifiedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unclassifiedRecorder, unclassified)
	if unclassifiedRecorder.Code != http.StatusNotFound {
		t.Fatalf("unclassified response = %d", unclassifiedRecorder.Code)
	}

	tests := []struct {
		name   string
		body   string
		key    string
		second bool
		status int
	}{
		{name: "empty", body: "", key: "domain-invalid", status: http.StatusBadRequest},
		{name: "null", body: "null", key: "domain-invalid", status: http.StatusBadRequest},
		{name: "array", body: "[]", key: "domain-invalid", status: http.StatusBadRequest},
		{name: "unknown hostname", body: `{"hostname":"example.test"}`, key: "domain-invalid", status: http.StatusBadRequest},
		{name: "unknown tls", body: `{"tls":true}`, key: "domain-invalid", status: http.StatusBadRequest},
		{name: "trailing", body: "{} {}", key: "domain-invalid", status: http.StatusBadRequest},
		{name: "missing key", body: "{}", status: http.StatusBadRequest},
		{name: "repeated key", body: "{}", key: "domain-invalid", second: true, status: http.StatusBadRequest},
		{name: "oversize", body: "{" + strings.Repeat(" ", tenantGeneratedDomainBodyLimitBytes) + "}", key: "domain-invalid", status: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := tenantGeneratedDomainRequest(t, http.MethodPost, "", test.body, test.key)
			if test.second {
				request.Header.Add("Idempotency-Key", "domain-invalid-second")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("response = %d %q, want %d", recorder.Code, recorder.Body.String(), test.status)
			}
		})
	}
}

func TestTenantGeneratedDomainsHandlerMapsTypedErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{name: "invalid session", err: identity.ErrSessionInvalid, status: http.StatusUnauthorized, code: "AUTH_SESSION_INVALID"},
		{name: "concealed access", err: identity.ErrAccessNotFound, status: http.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "domain not found", err: ErrTenantGeneratedDomainNotFound, status: http.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "denied", err: identity.ErrAuthorizationDenied, status: http.StatusForbidden, code: "AUTHORIZATION_DENIED"},
		{name: "in progress", err: idempotency.ErrRequestInProgress, status: http.StatusConflict, code: "IDEMPOTENCY_REQUEST_IN_PROGRESS", retryAfter: "1"},
		{name: "key body reused", err: idempotency.ErrKeyReusedDifferentBody, status: http.StatusConflict, code: "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY"},
		{name: "key request reused", err: idempotency.ErrKeyReused, status: http.StatusConflict, code: "IDEMPOTENCY_KEY_REUSED"},
		{name: "version conflict", err: &TenantGeneratedDomainVersionConflict{Expected: 2, Current: 3}, status: http.StatusPreconditionFailed, code: "PRECONDITION_FAILED"},
		{name: "already exists", err: ErrTenantGeneratedDomainConflict, status: http.StatusConflict, code: "RESOURCE_CONFLICT"},
		{name: "retired identity", err: ErrTenantGeneratedDomainRetired, status: http.StatusConflict, code: "RESOURCE_CONFLICT"},
		{name: "apps domain missing", err: ErrTenantGeneratedAppsDomainMissing, status: http.StatusServiceUnavailable, code: "CAPABILITY_UNAVAILABLE"},
		{name: "unavailable", err: ErrTenantGeneratedDomainsUnavailable, status: http.StatusServiceUnavailable, code: "INTERNAL_ERROR"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &tenantGeneratedDomainCoordinatorFake{err: test.err}
			request := tenantGeneratedDomainRequest(t, http.MethodGet, "", "", "")
			recorder := httptest.NewRecorder()
			tenantGeneratedDomainsHandler(fake).ServeHTTP(recorder, request)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"`+test.code+`"`) {
				t.Fatalf("response = %d %q, want %d %s", recorder.Code, recorder.Body.String(), test.status, test.code)
			}
			if recorder.Header().Get("Retry-After") != test.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", recorder.Header().Get("Retry-After"), test.retryAfter)
			}
		})
	}
}

func TestTenantGeneratedDomainsHandlerMethodAndCoordinatorGates(t *testing.T) {
	request := tenantGeneratedDomainRequest(t, http.MethodPatch, "dom_test", "{}", "domain-patch-test")
	recorder := httptest.NewRecorder()
	tenantGeneratedDomainsHandler(&tenantGeneratedDomainCoordinatorFake{}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, DELETE" {
		t.Fatalf("method response = %d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}

	request = tenantGeneratedDomainRequest(t, http.MethodGet, "", "", "")
	recorder = httptest.NewRecorder()
	tenantGeneratedDomainsHandler(nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil coordinator response = %d", recorder.Code)
	}
}

func tenantGeneratedDomainRequest(
	t *testing.T,
	method string,
	domainID string,
	body string,
	key string,
) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, "/api/v1alpha/services/svc_test/environments/env_test/domains", reader)
	if method == http.MethodDelete {
		setTestIfMatchFromExpectedVersion(t, request, body)
	}
	request.SetPathValue("service_id", " svc_test ")
	request.SetPathValue("environment_id", " env_test ")
	if domainID != "" {
		request.SetPathValue("domain_id", " "+domainID+" ")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	principal := identity.Principal{UserID: "usr_test", SessionID: "ses_test"}
	session := identity.Session{ID: "ses_test", UserID: "usr_test"}
	ctx := withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal})
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	return request.WithContext(ctx)
}

func TestTenantGeneratedDomainsErrorSentinelsRemainDistinct(t *testing.T) {
	if errors.Is(ErrTenantGeneratedDomainConflict, ErrTenantGeneratedDomainNotFound) ||
		errors.Is(ErrTenantGeneratedDomainRetired, ErrTenantGeneratedDomainConflict) ||
		errors.Is(ErrTenantGeneratedDomainsUnavailable, ErrTenantGeneratedAppsDomainMissing) {
		t.Fatal("tenant generated domain error sentinels must remain distinct")
	}
}
