// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type tenantCustomDomainCoordinatorFake struct {
	createResult TenantMutationResult
	verifyResult TenantMutationResult
	deleteResult TenantMutationResult
	err          error

	createRequest TenantCustomDomainCreateRequest
	verifyRequest TenantCustomDomainVerifyRequest
	deleteRequest TenantCustomDomainDeleteRequest
}

func (f *tenantCustomDomainCoordinatorFake) ListCustomDomains(_ context.Context, _ TenantGeneratedDomainReadRequest) ([]TenantCustomDomainView, error) {
	return []TenantCustomDomainView{}, f.err
}
func (f *tenantCustomDomainCoordinatorFake) GetCustomDomain(_ context.Context, _ TenantGeneratedDomainReadRequest) (TenantCustomDomainView, error) {
	return TenantCustomDomainView{}, f.err
}
func (f *tenantCustomDomainCoordinatorFake) CreateCustomDomain(_ context.Context, request TenantCustomDomainCreateRequest) (TenantMutationResult, error) {
	f.createRequest = request
	return f.createResult, f.err
}
func (f *tenantCustomDomainCoordinatorFake) VerifyCustomDomain(_ context.Context, request TenantCustomDomainVerifyRequest) (TenantMutationResult, error) {
	f.verifyRequest = request
	return f.verifyResult, f.err
}
func (f *tenantCustomDomainCoordinatorFake) DeleteCustomDomain(_ context.Context, request TenantCustomDomainDeleteRequest) (TenantMutationResult, error) {
	f.deleteRequest = request
	return f.deleteResult, f.err
}

func TestTenantCustomDomainsHandlerCreateAndVerifyStrictly(t *testing.T) {
	fake := &tenantCustomDomainCoordinatorFake{
		createResult: TenantMutationResult{Response: idempotency.StoredResponse{StatusCode: http.StatusCreated, Header: http.Header{}, Body: []byte(`{"id":"dom_custom"}`)}},
		verifyResult: TenantMutationResult{Response: idempotency.StoredResponse{StatusCode: http.StatusOK, Header: http.Header{}, Body: []byte(`{"ownership_state":"verified"}`)}},
	}
	handler := tenantCustomDomainsHandler(fake)

	create := tenantCustomDomainRequest(t, http.MethodPost, "", `{"hostname":"api.example.com","tls":true}`, "custom-create")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, create)
	if recorder.Code != http.StatusCreated || fake.createRequest.Hostname != "api.example.com" || !fake.createRequest.TLS || fake.createRequest.Key != "custom-create" {
		t.Fatalf("create response=%d body=%q request=%#v", recorder.Code, recorder.Body.String(), fake.createRequest)
	}

	verify := tenantCustomDomainRequest(t, http.MethodPost, "dom_custom", `{"expected_version":4}`, "custom-verify")
	verify.URL.Path += "/verify"
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, verify)
	if recorder.Code != http.StatusOK || fake.verifyRequest.DomainID != "dom_custom" || fake.verifyRequest.ExpectedVersion != 4 || fake.verifyRequest.Key != "custom-verify" {
		t.Fatalf("verify response=%d body=%q request=%#v", recorder.Code, recorder.Body.String(), fake.verifyRequest)
	}
}

func TestTenantCustomDomainsHandlerRejectsLooseBodies(t *testing.T) {
	handler := tenantCustomDomainsHandler(&tenantCustomDomainCoordinatorFake{})
	for _, body := range []string{"", "null", "[]", `{"hostname":"api.example.com","unknown":true}`, `{"hostname":"api.example.com"} {}`} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, tenantCustomDomainRequest(t, http.MethodPost, "", body, "custom-invalid"))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q response=%d body=%q", body, recorder.Code, recorder.Body.String())
		}
	}

	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, tenantCustomDomainRequest(t, http.MethodPost, "", `{"hostname":"api.example.com"}`, ""))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key response=%d", missingKey.Code)
	}
}

func TestTenantCustomDomainsHandlerMapsTypedErrors(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{err: identity.ErrSessionInvalid, status: http.StatusUnauthorized, code: "AUTH_SESSION_INVALID"},
		{err: identity.ErrAuthorizationDenied, status: http.StatusForbidden, code: "AUTHORIZATION_DENIED"},
		{err: ErrTenantCustomDomainValidation, status: http.StatusBadRequest, code: "RESOURCE_VALIDATION_FAILED"},
		{err: ErrTenantCustomDomainConflict, status: http.StatusConflict, code: "RESOURCE_CONFLICT"},
		{err: ErrTenantCustomDomainUnavailable, status: http.StatusServiceUnavailable, code: "INTERNAL_ERROR"},
	} {
		t.Run(test.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			tenantCustomDomainsHandler(&tenantCustomDomainCoordinatorFake{err: test.err}).ServeHTTP(recorder, tenantCustomDomainRequest(t, http.MethodGet, "", "", ""))
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"`+test.code+`"`) {
				t.Fatalf("response=%d body=%q want=%d %s", recorder.Code, recorder.Body.String(), test.status, test.code)
			}
		})
	}
}

func tenantCustomDomainRequest(t *testing.T, method, domainID, body, key string) *http.Request {
	t.Helper()
	path := "/api/v1alpha/services/svc_test/environments/env_test/custom-domains"
	if domainID != "" {
		path += "/" + domainID
	}
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodDelete {
		setTestIfMatchFromExpectedVersion(t, request, body)
	}
	request.SetPathValue("service_id", " svc_test ")
	request.SetPathValue("environment_id", " env_test ")
	request.SetPathValue("domain_id", " "+domainID+" ")
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

func TestTenantCustomDomainErrorSentinelsRemainDistinct(t *testing.T) {
	if errors.Is(ErrTenantCustomDomainValidation, ErrTenantCustomDomainUnavailable) ||
		errors.Is(ErrTenantCustomDomainConflict, ErrTenantCustomDomainNotFound) {
		t.Fatal("tenant custom domain error sentinels must remain distinct")
	}
}

func TestCustomDomainRoutingDNSViewSeparatesSubdomainAndApexGuidance(t *testing.T) {
	subdomain := customDomainRoutingDNSView("api.example.com", "apps.midnight.test")
	if subdomain.RecordType != "CNAME" || subdomain.RecordValue != "apps.midnight.test" || subdomain.State != "operator_required" {
		t.Fatalf("subdomain routing DNS = %#v", subdomain)
	}
	apex := customDomainRoutingDNSView("example.com", "apps.midnight.test")
	if apex.RecordType != "A/ALIAS/ANAME" || apex.RecordValue != "" || apex.State != "operator_required" {
		t.Fatalf("apex routing DNS = %#v", apex)
	}
	missing := customDomainRoutingDNSView("api.example.com", "")
	if missing.State != "unavailable" || missing.RecordValue != "" {
		t.Fatalf("missing apps-domain routing DNS = %#v", missing)
	}
}
