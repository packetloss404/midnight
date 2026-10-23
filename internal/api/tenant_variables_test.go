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
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
)

type tenantVariableCoordinatorStub struct {
	listRequest   TenantVariableReadRequest
	listResult    []secrets.VariableMeta
	listErr       error
	listCalls     int
	getRequest    TenantVariableReadRequest
	getResult     secrets.VariableMeta
	getErr        error
	getCalls      int
	createRequest TenantVariableCreateRequest
	createResult  TenantMutationResult
	createErr     error
	createCalls   int
	updateRequest TenantVariableUpdateRequest
	updateResult  TenantMutationResult
	updateErr     error
	updateCalls   int
	deleteRequest TenantVariableDeleteRequest
	deleteResult  TenantMutationResult
	deleteErr     error
	deleteCalls   int
}

func (s *tenantVariableCoordinatorStub) ListVariables(_ context.Context, request TenantVariableReadRequest) ([]secrets.VariableMeta, error) {
	s.listRequest = request
	s.listCalls++
	return s.listResult, s.listErr
}

func (s *tenantVariableCoordinatorStub) GetVariable(_ context.Context, request TenantVariableReadRequest) (secrets.VariableMeta, error) {
	s.getRequest = request
	s.getCalls++
	return s.getResult, s.getErr
}

func (s *tenantVariableCoordinatorStub) CreateVariable(_ context.Context, request TenantVariableCreateRequest) (TenantMutationResult, error) {
	request.Plaintext = append([]byte(nil), request.Plaintext...)
	request.RawBody = append([]byte(nil), request.RawBody...)
	s.createRequest = request
	s.createCalls++
	return s.createResult, s.createErr
}

func (s *tenantVariableCoordinatorStub) UpdateVariable(_ context.Context, request TenantVariableUpdateRequest) (TenantMutationResult, error) {
	request.Plaintext = append([]byte(nil), request.Plaintext...)
	request.RawBody = append([]byte(nil), request.RawBody...)
	s.updateRequest = request
	s.updateCalls++
	return s.updateResult, s.updateErr
}

func (s *tenantVariableCoordinatorStub) DeleteVariable(_ context.Context, request TenantVariableDeleteRequest) (TenantMutationResult, error) {
	s.deleteRequest = request
	s.deleteCalls++
	return s.deleteResult, s.deleteErr
}

func TestTenantVariablesHandlerDelegatesExactHumanScope(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_vars", SessionID: "ses_vars", AuthMethod: "passkey"}
	session := identity.Session{ID: "ses_vars", UserID: "usr_vars", SessionGeneration: 7}
	meta := secrets.VariableMeta{
		ID: "var_token", Scope: secrets.ScopeSecret, Workspace: "ws_actual",
		Project: "prj_actual", Environment: "env_prod", Service: "svc_api",
		Name: "TOKEN", Version: 1, LengthBucket: "1-16",
	}

	t.Run("metadata list", func(t *testing.T) {
		stub := &tenantVariableCoordinatorStub{listResult: []secrets.VariableMeta{meta}}
		request := tenantVariableHandlerRequest(t, http.MethodGet, "", "", principal, session)
		response := httptest.NewRecorder()
		tenantVariablesHandler(stub)(response, request)
		if response.Code != http.StatusOK || stub.listCalls != 1 {
			t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), stub.listCalls)
		}
		if stub.listRequest.Principal != principal || stub.listRequest.Session.ID != session.ID ||
			stub.listRequest.ServiceID != "svc_api" || stub.listRequest.EnvironmentID != "env_prod" {
			t.Fatalf("request=%#v", stub.listRequest)
		}
		if strings.Contains(response.Body.String(), "plaintext") || strings.Contains(response.Body.String(), "guessable") {
			t.Fatalf("response leaked value-bearing fields: %s", response.Body.String())
		}
	})

	t.Run("create exact metadata response", func(t *testing.T) {
		responseBody, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		stub := &tenantVariableCoordinatorStub{createResult: TenantMutationResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusCreated,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       append(responseBody, '\n'),
			},
			Replayed: true,
		}}
		request := tenantVariableHandlerRequest(
			t,
			http.MethodPost,
			"",
			`{"name":"TOKEN","scope":"secret","value":"${{shared.API_TOKEN}}"}`,
			principal,
			session,
		)
		request.Header.Set("Idempotency-Key", "tenant-variable-create-1")
		request.Header.Set("User-Agent", "midnight-variable-test")
		request.RemoteAddr = "192.0.2.10:443"
		response := httptest.NewRecorder()
		tenantVariablesHandler(stub)(response, request)
		if response.Code != http.StatusCreated || response.Header().Get("Idempotent-Replayed") != "true" {
			t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		got := stub.createRequest
		if stub.createCalls != 1 || got.Principal != principal || got.Session.ID != session.ID ||
			got.ServiceID != "svc_api" || got.EnvironmentID != "env_prod" ||
			got.VariableID != "" || got.Name != "TOKEN" || got.Scope != secrets.ScopeSecret ||
			string(got.Plaintext) != "${{shared.API_TOKEN}}" ||
			got.ValueKind != "reference" || len(got.References) != 1 ||
			got.Key != idempotency.Key("tenant-variable-create-1") ||
			got.UserAgent != "midnight-variable-test" {
			t.Fatalf("create request=%#v calls=%d", got, stub.createCalls)
		}
	})

	t.Run("update and delete bind item ID", func(t *testing.T) {
		updateMeta := meta
		updateMeta.Version = 2
		updateBody, _ := json.Marshal(updateMeta)
		stub := &tenantVariableCoordinatorStub{
			updateResult: TenantMutationResult{Response: idempotency.StoredResponse{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       append(updateBody, '\n'),
			}},
			deleteResult: TenantMutationResult{Response: idempotency.StoredResponse{StatusCode: http.StatusNoContent}},
		}
		update := tenantVariableHandlerRequest(t, http.MethodPatch, "var_token", `{"expected_version":1,"value":"rotated"}`, principal, session)
		update.Header.Set("Idempotency-Key", "tenant-variable-update-1")
		updateResponse := httptest.NewRecorder()
		tenantVariablesHandler(stub)(updateResponse, update)
		if updateResponse.Code != http.StatusOK || stub.updateCalls != 1 ||
			stub.updateRequest.VariableID != "var_token" ||
			stub.updateRequest.ExpectedVersion != 1 ||
			stub.updateRequest.Scope != nil ||
			string(stub.updateRequest.Plaintext) != "rotated" {
			t.Fatalf("update response=%d request=%#v", updateResponse.Code, stub.updateRequest)
		}

		deleteRequest := tenantVariableHandlerRequest(t, http.MethodDelete, "var_token", `{"expected_version":2}`, principal, session)
		deleteRequest.Header.Set("Idempotency-Key", "tenant-variable-delete-1")
		deleteResponse := httptest.NewRecorder()
		tenantVariablesHandler(stub)(deleteResponse, deleteRequest)
		if deleteResponse.Code != http.StatusNoContent || stub.deleteCalls != 1 ||
			stub.deleteRequest.VariableID != "var_token" ||
			stub.deleteRequest.ExpectedVersion != 2 {
			t.Fatalf("delete response=%d request=%#v", deleteResponse.Code, stub.deleteRequest)
		}
	})
}

func TestTenantVariablesHandlerRejectsNonCanonicalBodiesAndKeysBeforeCoordinator(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_vars", SessionID: "ses_vars"}
	session := identity.Session{ID: "ses_vars", UserID: "usr_vars", SessionGeneration: 1}
	tests := []struct {
		name       string
		method     string
		variableID string
		body       string
		keys       []string
		wantStatus int
	}{
		{name: "empty", method: http.MethodPost, body: "", keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "null", method: http.MethodPost, body: "null", keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "array", method: http.MethodPost, body: "[]", keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "unknown", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":"x","workspace_id":"forged"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "duplicate", method: http.MethodPost, body: `{"name":"A","name":"B","scope":"secret","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "trailing", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":"x"} {}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "value missing", method: http.MethodPost, body: `{"name":"A","scope":"secret"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "scope missing", method: http.MethodPost, body: `{"name":"A","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "name padded", method: http.MethodPost, body: `{"name":" A ","scope":"secret","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "name hyphen", method: http.MethodPost, body: `{"name":"API-TOKEN","scope":"secret","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "name leading digit", method: http.MethodPost, body: `{"name":"1TOKEN","scope":"secret","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "name too long", method: http.MethodPost, body: `{"name":"A` + strings.Repeat("B", tenantVariableNameLimitBytes) + `","scope":"secret","value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "update zero version", method: http.MethodPatch, variableID: "var_a", body: `{"expected_version":0,"value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "delete extra value", method: http.MethodDelete, variableID: "var_a", body: `{"expected_version":1,"value":"x"}`, keys: []string{"k"}, wantStatus: http.StatusBadRequest},
		{name: "missing key", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":""}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate key", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":""}`, keys: []string{"one", "two"}, wantStatus: http.StatusBadRequest},
		{name: "coalesced key", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":""}`, keys: []string{"one, two"}, wantStatus: http.StatusBadRequest},
		{name: "oversize", method: http.MethodPost, body: `{"name":"A","scope":"secret","value":"` + strings.Repeat("x", tenantVariableBodyLimitBytes) + `"}`, keys: []string{"k"}, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := &tenantVariableCoordinatorStub{}
			request := tenantVariableHandlerRequest(t, test.method, test.variableID, test.body, principal, session)
			for _, key := range test.keys {
				request.Header.Add("Idempotency-Key", key)
			}
			response := httptest.NewRecorder()
			tenantVariablesHandler(stub)(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if stub.createCalls+stub.updateCalls+stub.deleteCalls != 0 {
				t.Fatalf("coordinator mutation calls create=%d update=%d delete=%d", stub.createCalls, stub.updateCalls, stub.deleteCalls)
			}
		})
	}
}

func TestTenantVariablesHandlerNeverFallsBackForOperatorPrincipal(t *testing.T) {
	stub := &tenantVariableCoordinatorStub{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/services/svc_api/environments/env_prod/variables", nil)
	request.SetPathValue("service_id", "svc_api")
	request.SetPathValue("environment_id", "env_prod")
	request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalOperator}))
	response := httptest.NewRecorder()
	tenantVariablesHandler(stub)(response, request)
	if response.Code != http.StatusNotFound || stub.listCalls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, stub.listCalls, response.Body.String())
	}
}

func TestValidTenantVariableNamePortableBoundary(t *testing.T) {
	if !validTenantVariableName("_") ||
		!validTenantVariableName("a") ||
		!validTenantVariableName("A"+strings.Repeat("B", tenantVariableNameLimitBytes-1)) {
		t.Fatal("portable boundary name was rejected")
	}
	for _, invalid := range []string{
		"",
		" API_TOKEN",
		"API_TOKEN ",
		"API-TOKEN",
		"1TOKEN",
		"A" + strings.Repeat("B", tenantVariableNameLimitBytes),
	} {
		if validTenantVariableName(invalid) {
			t.Fatalf("invalid tenant variable name %q was accepted", invalid)
		}
	}
}

func tenantVariableHandlerRequest(
	t *testing.T,
	method string,
	variableID string,
	body string,
	principal identity.Principal,
	session identity.Session,
) *http.Request {
	t.Helper()
	target := "/api/v1alpha/services/svc_api/environments/env_prod/variables"
	if variableID != "" {
		target += "/" + variableID
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if method == http.MethodPatch || method == http.MethodDelete {
		setTestIfMatchFromExpectedVersion(t, request, body)
	}
	request.SetPathValue("service_id", "svc_api")
	request.SetPathValue("environment_id", "env_prod")
	request.SetPathValue("variable_id", variableID)
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	return request.WithContext(ctx)
}

var _ TenantVariableCoordinator = (*tenantVariableCoordinatorStub)(nil)
