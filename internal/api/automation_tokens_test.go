// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type automationTokenCatalogStub struct {
	workspaceID string
	projectID   string
	listCalls   int
	tokens      []automationtoken.Token
	token       automationtoken.Token
	err         error
}

func (s *automationTokenCatalogStub) Get(_ context.Context, workspaceID, projectID, tokenID string) (automationtoken.Token, error) {
	s.workspaceID, s.projectID = workspaceID, projectID
	if s.err != nil {
		return automationtoken.Token{}, s.err
	}
	if s.token.ID == "" || s.token.ID != tokenID {
		return automationtoken.Token{}, automationtoken.ErrTokenNotFound
	}
	return s.token, nil
}

func (s *automationTokenCatalogStub) List(_ context.Context, workspaceID, projectID string) ([]automationtoken.Token, error) {
	s.listCalls++
	s.workspaceID, s.projectID = workspaceID, projectID
	return append([]automationtoken.Token(nil), s.tokens...), nil
}

type automationTokenCoordinatorStub struct {
	mintRequest   AutomationTokenMintMutationRequest
	revokeRequest AutomationTokenRevokeMutationRequest
	result        TenantMutationResult
	err           error
}

func (s *automationTokenCoordinatorStub) MintAutomationToken(_ context.Context, request AutomationTokenMintMutationRequest) (TenantMutationResult, error) {
	s.mintRequest = request
	return s.result, s.err
}

func (s *automationTokenCoordinatorStub) RevokeAutomationToken(_ context.Context, request AutomationTokenRevokeMutationRequest) (TenantMutationResult, error) {
	s.revokeRequest = request
	return s.result, s.err
}

func TestAutomationTokenManagementRequiresHumanPrincipal(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects/prj_1/automation-tokens", nil)
	request.SetPathValue("project_id", "prj_1")
	request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalOperator}))
	response := httptest.NewRecorder()
	automationTokenCollectionHandler(nil, nil, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "human session") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestAutomationTokenMintPassesScopedSessionAndFingerprint(t *testing.T) {
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	body := `{"name":"release","expires_at":"` + expiresAt.Format(time.RFC3339) + `"}`
	request := humanAutomationTokenRequest(t, http.MethodPost, "/api/v1alpha/projects/prj_1/automation-tokens", body)
	request.Pattern = "/api/v1alpha/projects/{project_id}/automation-tokens"
	request.Header.Set("Idempotency-Key", "token-create-1")
	request.SetPathValue("project_id", "prj_1")
	responseBody := []byte(`{"access_token":"midn_sa_once","token":{"id":"tok_1"}}` + "\n")
	coordinator := &automationTokenCoordinatorStub{result: TenantMutationResult{
		Response: idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "Cache-Control": []string{"no-store"}},
			Body:       responseBody,
		},
	}}
	response := httptest.NewRecorder()
	automationTokenCollectionHandler(nil, nil, coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Body.String() != string(responseBody) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	got := coordinator.mintRequest
	wantFingerprint := idempotencyRequestFingerprint(request, idempotency.BodyHashSHA256([]byte(body)))
	if got.ProjectID != "prj_1" || got.Name != "release" || got.Key != "token-create-1" ||
		got.Principal.UserID != "usr_1" || got.Session.ID != "ses_1" ||
		got.Fingerprint.Path != "/api/v1alpha/projects/{project_id}/automation-tokens" ||
		got.Fingerprint.BodySHA256 != wantFingerprint.BodySHA256 {
		t.Fatalf("request = %#v", got)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func TestAutomationTokenRevokeReplaysExactNoContent(t *testing.T) {
	request := humanAutomationTokenRequest(t, http.MethodDelete, "/api/v1alpha/projects/prj_1/automation-tokens/tok_1", "")
	request.Header.Set("Idempotency-Key", "token-revoke-1")
	request.Header.Set("If-Match", `"1"`)
	request.SetPathValue("project_id", "prj_1")
	request.SetPathValue("token_id", "tok_1")
	coordinator := &automationTokenCoordinatorStub{result: TenantMutationResult{
		Response: idempotency.StoredResponse{StatusCode: http.StatusNoContent},
		Replayed: true,
	}}
	response := httptest.NewRecorder()
	catalog, access := automationTokenItemFixture(t, automationtoken.Token{ID: "tok_1", WorkspaceID: "wrk_1", ProjectID: "prj_1", ResourceVersion: 1})
	automationTokenResourceHandler(catalog, access, coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("response = %d replay=%q body=%s", response.Code, response.Header().Get("Idempotent-Replayed"), response.Body.String())
	}
	if coordinator.revokeRequest.ProjectID != "prj_1" || coordinator.revokeRequest.TokenID != "tok_1" || coordinator.revokeRequest.ExpectedVersion != 1 {
		t.Fatalf("request = %#v", coordinator.revokeRequest)
	}
}

func TestAutomationTokenItemGetETagAndRequiresIfMatch(t *testing.T) {
	token := automationtoken.Token{ID: "tok_1", WorkspaceID: "wrk_1", ProjectID: "prj_1", TokenPrefix: "midn_sa_public", ResourceVersion: 3}
	catalog, access := automationTokenItemFixture(t, token)
	get := humanAutomationTokenRequest(t, http.MethodGet, "/api/v1alpha/projects/prj_1/automation-tokens/tok_1", "")
	get.SetPathValue("project_id", "prj_1")
	get.SetPathValue("token_id", "tok_1")
	response := httptest.NewRecorder()
	automationTokenResourceHandler(catalog, access, nil).ServeHTTP(response, get)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` || strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	deleteRequest := humanAutomationTokenRequest(t, http.MethodDelete, "/api/v1alpha/projects/prj_1/automation-tokens/tok_1", "")
	deleteRequest.SetPathValue("project_id", "prj_1")
	deleteRequest.SetPathValue("token_id", "tok_1")
	deleteRequest.Header.Set("Idempotency-Key", "token-revoke-missing")
	response = httptest.NewRecorder()
	automationTokenResourceHandler(catalog, access, &automationTokenCoordinatorStub{}).ServeHTTP(response, deleteRequest)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAutomationTokenMintRejectsUnknownAndTrailingJSON(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus int
	}{
		"unknown member":    {`{"name":"release","expires_at":"2030-01-01T00:00:00Z","profile":"admin"}`, http.StatusBadRequest},
		"duplicate member":  {`{"name":"first","name":"release","expires_at":"2030-01-01T00:00:00Z"}`, http.StatusBadRequest},
		"trailing document": {`{"name":"release","expires_at":"2030-01-01T00:00:00Z"} {}`, http.StatusBadRequest},
		"oversized":         {`{"name":"` + strings.Repeat("x", int(sensitiveJSONBodyLimitBytes)) + `","expires_at":"2030-01-01T00:00:00Z"}`, http.StatusRequestEntityTooLarge},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := humanAutomationTokenRequest(t, http.MethodPost, "/api/v1alpha/projects/prj_1/automation-tokens", test.body)
			request.Header.Set("Idempotency-Key", "token-create-1")
			request.SetPathValue("project_id", "prj_1")
			response := httptest.NewRecorder()
			automationTokenCollectionHandler(nil, nil, &automationTokenCoordinatorStub{}).ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response = %d %s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type=%q", got)
			}
			var problem ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Code != failurecodes.ResourceValidationFailed {
				t.Fatalf("problem=%#v err=%v body=%s", problem, err, response.Body.String())
			}
		})
	}
}

func TestAutomationTokenListDenialDoesNotReachCatalog(t *testing.T) {
	_, access := identity.NewMemoryAuthority(identity.UserRecord{
		ID: "usr_1", Email: "viewer@example.com", SessionGeneration: 1,
	})
	accepted := time.Now().UTC()
	access.SeedWorkspace(identity.Workspace{ID: "wrk_1", Slug: "one"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_1",
		User:        identity.User{ID: "usr_1", Email: "viewer@example.com"},
		Role:        identity.WorkspaceRoleViewer,
		AcceptedAt:  &accepted,
	})
	access.SeedProject("prj_1", "wrk_1")
	catalog := &automationTokenCatalogStub{tokens: []automationtoken.Token{{ID: "tok_should_not_be_read"}}}
	request := humanAutomationTokenRequest(t, http.MethodGet, "/api/v1alpha/projects/prj_1/automation-tokens", "")
	request.SetPathValue("project_id", "prj_1")
	response := httptest.NewRecorder()

	automationTokenCollectionHandler(catalog, access, nil).ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
	if catalog.listCalls != 0 {
		t.Fatalf("catalog List calls=%d, want 0 after authorization denial", catalog.listCalls)
	}
	var problem ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("denial is not one JSON response: %v; body=%s", err, response.Body.String())
	}
}

func TestAutomationTokenListUsesCanonicalAuthorizedWorkspace(t *testing.T) {
	sessions, access := identity.NewMemoryAuthority(identity.UserRecord{
		ID: "usr_1", Email: "user@example.com", SessionGeneration: 1,
	})
	_ = sessions
	accepted := time.Now().UTC()
	access.SeedWorkspace(identity.Workspace{ID: "wrk_1", Slug: "one"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_1",
		User:        identity.User{ID: "usr_1", Email: "user@example.com"},
		Role:        identity.WorkspaceRoleAdmin,
		AcceptedAt:  &accepted,
	})
	access.SeedProject("prj_1", "wrk_1")
	catalog := &automationTokenCatalogStub{tokens: []automationtoken.Token{{ID: "tok_1"}}}
	request := humanAutomationTokenRequest(t, http.MethodGet, "/api/v1alpha/projects/prj_1/automation-tokens", "")
	request.SetPathValue("project_id", "prj_1")
	response := httptest.NewRecorder()
	automationTokenCollectionHandler(catalog, access, nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || catalog.workspaceID != "wrk_1" || catalog.projectID != "prj_1" {
		t.Fatalf("response=%d %s scope=%s/%s", response.Code, response.Body.String(), catalog.workspaceID, catalog.projectID)
	}
	var payload automationTokenListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Items) != 1 || payload.Items[0].ID != "tok_1" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func TestAutomationTokenMutationMapsAuthorizationAndSensitiveReplayErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"denied", identity.ErrAuthorizationDenied, http.StatusForbidden},
		{"revoked session", identity.ErrSessionInvalid, http.StatusUnauthorized},
		{"different body", idempotency.ErrKeyReusedDifferentBody, http.StatusConflict},
		{"missing sensitive key", errors.New("sensitive replay key unavailable"), http.StatusServiceUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			request := humanAutomationTokenRequest(t, http.MethodPost, "/api/v1alpha/projects/prj_1/automation-tokens", `{"name":"release","expires_at":"2030-01-01T00:00:00Z"}`)
			request.Header.Set("Idempotency-Key", "token-create-1")
			request.SetPathValue("project_id", "prj_1")
			response := httptest.NewRecorder()
			automationTokenCollectionHandler(nil, nil, &automationTokenCoordinatorStub{err: tt.err}).ServeHTTP(response, request)
			if response.Code != tt.status {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "sensitive replay key unavailable") {
				t.Fatalf("internal error leaked: %s", response.Body.String())
			}
		})
	}
}

func TestAutomationTokenManagementRoutesAreExactAndHumanOnly(t *testing.T) {
	stack, sessions := productReadDataFixture(t)
	catalog := &automationTokenCatalogStub{tokens: []automationtoken.Token{{
		ID:               "tok_a",
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_a",
		ProjectID:        "prj_a",
		Name:             "CI",
		TokenPrefix:      "midn_sa_public",
		Profile:          automationtoken.ProfileCIDeployV1,
	}}, token: automationtoken.Token{ID: "tok_a", WorkspaceID: "wrk_a", ProjectID: "prj_a", ResourceVersion: 1}}
	coordinator := &automationTokenCoordinatorStub{}
	stack.AutomationTokenCatalog = catalog
	stack.AutomationTokenMutations = coordinator
	mux, handler := buildRouterWithDataStack(stack)

	for _, route := range []struct {
		method  string
		target  string
		pattern string
	}{
		{
			method:  http.MethodGet,
			target:  "/api/v1alpha/projects/prj_a/automation-tokens",
			pattern: "/api/v1alpha/projects/{project_id}/automation-tokens",
		},
		{
			method:  http.MethodDelete,
			target:  "/api/v1alpha/projects/prj_a/automation-tokens/tok_a",
			pattern: "/api/v1alpha/projects/{project_id}/automation-tokens/{token_id}",
		},
	} {
		request := httptest.NewRequest(route.method, route.target, nil)
		_, pattern := mux.Handler(request)
		if pattern != route.pattern {
			t.Fatalf("%s %s matched %q, want %q", route.method, route.target, pattern, route.pattern)
		}
	}

	alice := loginIdentityForTest(t, sessions, "alice@example.com")
	list := authenticatedIdentityRequest(handler, http.MethodGet, "/api/v1alpha/projects/prj_a/automation-tokens", alice)
	if list.Code != http.StatusOK ||
		!strings.Contains(list.Body.String(), `"id":"tok_a"`) ||
		catalog.workspaceID != "wrk_a" ||
		catalog.projectID != "prj_a" {
		t.Fatalf("human list status=%d body=%s scope=%s/%s", list.Code, list.Body.String(), catalog.workspaceID, catalog.projectID)
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	coordinator.result = TenantMutationResult{Response: idempotency.StoredResponse{
		StatusCode: http.StatusCreated,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"access_token":"midn_sa_once","token":{"id":"tok_new"}}` + "\n"),
	}}
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/projects/prj_a/automation-tokens",
		strings.NewReader(`{"name":"release","expires_at":"`+expiresAt.Format(time.RFC3339)+`"}`),
	)
	createRequest.Header.Set("Authorization", "Bearer "+alice)
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Idempotency-Key", "router-token-create")
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated ||
		coordinator.mintRequest.ProjectID != "prj_a" ||
		coordinator.mintRequest.Fingerprint.Path != "/api/v1alpha/projects/{project_id}/automation-tokens" {
		t.Fatalf("human create status=%d body=%s request=%#v", createResponse.Code, createResponse.Body.String(), coordinator.mintRequest)
	}

	coordinator.result = TenantMutationResult{Response: idempotency.StoredResponse{StatusCode: http.StatusNoContent}}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1alpha/projects/prj_a/automation-tokens/tok_a", nil)
	deleteRequest.Header.Set("Authorization", "Bearer "+alice)
	deleteRequest.Header.Set("Idempotency-Key", "router-token-delete")
	deleteRequest.Header.Set("If-Match", `"1"`)
	deleteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent ||
		coordinator.revokeRequest.ProjectID != "prj_a" ||
		coordinator.revokeRequest.TokenID != "tok_a" ||
		coordinator.revokeRequest.Fingerprint.Path != "/api/v1alpha/projects/{project_id}/automation-tokens/{token_id}" {
		t.Fatalf("human delete status=%d body=%s request=%#v", deleteResponse.Code, deleteResponse.Body.String(), coordinator.revokeRequest)
	}

	bob := loginIdentityForTest(t, sessions, "bob@example.com")
	viewer := authenticatedIdentityRequest(handler, http.MethodGet, "/api/v1alpha/projects/prj_a/automation-tokens", bob)
	if viewer.Code != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", viewer.Code, viewer.Body.String())
	}
	for name, token := range map[string]string{
		"operator":   "operator-recovery-token",
		"automation": "midn_sa_not-a-human-session",
	} {
		t.Run(name, func(t *testing.T) {
			response := authenticatedIdentityRequest(handler, http.MethodGet, "/api/v1alpha/projects/prj_a/automation-tokens", token)
			if response.Code != http.StatusUnauthorized ||
				!strings.Contains(response.Body.String(), `"code":"AUTH_SESSION_INVALID"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func automationTokenItemFixture(t *testing.T, token automationtoken.Token) (*automationTokenCatalogStub, identity.AccessStore) {
	t.Helper()
	_, access := identity.NewMemoryAuthority(identity.UserRecord{ID: "usr_1", Email: "user@example.com", SessionGeneration: 1})
	accepted := time.Now().UTC()
	access.SeedWorkspace(identity.Workspace{ID: token.WorkspaceID, Slug: "fixture"}, identity.WorkspaceMember{
		WorkspaceID: token.WorkspaceID,
		User:        identity.User{ID: "usr_1", Email: "user@example.com"},
		Role:        identity.WorkspaceRoleAdmin,
		AcceptedAt:  &accepted,
	})
	access.SeedProject(token.ProjectID, token.WorkspaceID)
	return &automationTokenCatalogStub{token: token}, access
}

func humanAutomationTokenRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	principal := identity.Principal{Kind: "user", UserID: "usr_1", SessionID: "ses_1"}
	session := identity.Session{ID: "ses_1", UserID: "usr_1", SessionGeneration: 1}
	ctx := withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal})
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	return request.WithContext(ctx)
}
