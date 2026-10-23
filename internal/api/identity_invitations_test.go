// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type identityInvitationCatalogStub struct {
	request    identity.ListInvitationsRequest
	getRequest identity.GetInvitationRequest
	items      []identity.Invitation
	item       identity.Invitation
	err        error
}

func (s *identityInvitationCatalogStub) Get(_ context.Context, request identity.GetInvitationRequest) (identity.Invitation, error) {
	s.getRequest = request
	return s.item, s.err
}

func (s *identityInvitationCatalogStub) List(_ context.Context, request identity.ListInvitationsRequest) ([]identity.Invitation, error) {
	s.request = request
	return append([]identity.Invitation(nil), s.items...), s.err
}

type identityInvitationCoordinatorStub struct {
	createRequest  IdentityInvitationCreateMutationRequest
	reissueRequest IdentityInvitationReissueMutationRequest
	revokeRequest  IdentityInvitationRevokeMutationRequest
	acceptRequest  IdentityInvitationAcceptMutationRequest
	result         TenantMutationResult
	err            error
}

func (s *identityInvitationCoordinatorStub) CreateIdentityInvitation(_ context.Context, request IdentityInvitationCreateMutationRequest) (TenantMutationResult, error) {
	s.createRequest = request
	return s.result, s.err
}

func (s *identityInvitationCoordinatorStub) ReissueIdentityInvitation(_ context.Context, request IdentityInvitationReissueMutationRequest) (TenantMutationResult, error) {
	s.reissueRequest = request
	return s.result, s.err
}

func (s *identityInvitationCoordinatorStub) RevokeIdentityInvitation(_ context.Context, request IdentityInvitationRevokeMutationRequest) (TenantMutationResult, error) {
	s.revokeRequest = request
	return s.result, s.err
}

func (s *identityInvitationCoordinatorStub) AcceptIdentityInvitation(_ context.Context, request IdentityInvitationAcceptMutationRequest) (TenantMutationResult, error) {
	s.acceptRequest = request
	return s.result, s.err
}

func TestIdentityInvitationCreatePassesVerifiedSessionScopeAndFingerprint(t *testing.T) {
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	body := `{"email":"invited@example.com","workspace_id":"wrk_1","workspace_role":"member","expires_at":"` +
		expiresAt.Format(time.RFC3339) + `"}`
	request := humanIdentityInvitationRequest(http.MethodPost, "/api/v1alpha/invitations", body)
	request.Pattern = "/api/v1alpha/invitations"
	request.Header.Set("Idempotency-Key", "invite-create-1")
	responseBody := []byte(`{"invitation_token":"midn_inv_once","invitation":{"id":"inv_1"}}` + "\n")
	coordinator := &identityInvitationCoordinatorStub{result: TenantMutationResult{
		Response: idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       responseBody,
		},
	}}
	response := httptest.NewRecorder()
	identityInvitationCollectionHandler(nil, coordinator, CursorCodec{}).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Body.String() != string(responseBody) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	got := coordinator.createRequest
	if got.Principal.UserID != "usr_1" || got.Session.ID != "ses_1" ||
		got.WorkspaceID != "wrk_1" || got.WorkspaceRole != identity.WorkspaceRoleMember ||
		got.Email != "invited@example.com" || got.Key != "invite-create-1" ||
		got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) {
		t.Fatalf("request = %#v", got)
	}
}

func TestIdentityInvitationListIsExactScopeAndMetadataOnly(t *testing.T) {
	catalog := &identityInvitationCatalogStub{items: []identity.Invitation{{
		ID: "inv_1", WorkspaceID: "wrk_1", ProjectID: "prj_1",
	}}}
	request := humanIdentityInvitationRequest(
		http.MethodGet,
		"/api/v1alpha/invitations?workspace_id=wrk_1&project_id=prj_1",
		"",
	)
	response := httptest.NewRecorder()
	identityInvitationCollectionHandler(catalog, nil, CursorCodec{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		catalog.request.WorkspaceID != "wrk_1" ||
		catalog.request.ProjectID != "prj_1" ||
		catalog.request.ActorUserID != "usr_1" ||
		strings.Contains(response.Body.String(), "invitation_token") {
		t.Fatalf("response=%d %s request=%#v", response.Code, response.Body.String(), catalog.request)
	}
}

func TestIdentityInvitationItemGetETagAndConditionalRevoke(t *testing.T) {
	catalog := &identityInvitationCatalogStub{item: identity.Invitation{
		ID: "inv_1", WorkspaceID: "wrk_1", ResourceVersion: 7,
	}}
	get := humanIdentityInvitationRequest(
		http.MethodGet,
		"/api/v1alpha/invitations/inv_1?workspace_id=wrk_1",
		"",
	)
	get.SetPathValue("invitation_id", "inv_1")
	response := httptest.NewRecorder()
	identityInvitationResourceHandler(catalog, nil).ServeHTTP(response, get)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"7"` ||
		catalog.getRequest.ActorUserID != "usr_1" ||
		strings.Contains(response.Body.String(), "invitation_token") {
		t.Fatalf("response=%d headers=%v body=%s request=%+v", response.Code, response.Header(), response.Body.String(), catalog.getRequest)
	}

	missing := humanIdentityInvitationRequest(
		http.MethodDelete,
		"/api/v1alpha/invitations/inv_1?workspace_id=wrk_1",
		"",
	)
	missing.SetPathValue("invitation_id", "inv_1")
	missing.Header.Set("Idempotency-Key", "invitation-revoke-missing")
	response = httptest.NewRecorder()
	identityInvitationResourceHandler(catalog, &identityInvitationCoordinatorStub{}).ServeHTTP(response, missing)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match response=%d %s", response.Code, response.Body.String())
	}

	coordinator := &identityInvitationCoordinatorStub{result: TenantMutationResult{Response: idempotency.StoredResponse{
		StatusCode: http.StatusNoContent,
		Header:     http.Header{"Cache-Control": []string{"no-store"}},
	}}}
	revoke := humanIdentityInvitationRequest(
		http.MethodDelete,
		"/api/v1alpha/invitations/inv_1?workspace_id=wrk_1",
		"",
	)
	revoke.SetPathValue("invitation_id", "inv_1")
	revoke.Header.Set("If-Match", `"7"`)
	revoke.Header.Set("Idempotency-Key", "invitation-revoke-1")
	response = httptest.NewRecorder()
	identityInvitationResourceHandler(catalog, coordinator).ServeHTTP(response, revoke)
	if response.Code != http.StatusNoContent || coordinator.revokeRequest.ExpectedVersion != 7 {
		t.Fatalf("response=%d %s request=%+v", response.Code, response.Body.String(), coordinator.revokeRequest)
	}
	if coordinator.revokeRequest.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(`"7"`)) {
		t.Fatalf("conditional fingerprint=%+v", coordinator.revokeRequest.Fingerprint)
	}

	coordinator.err = identity.ErrInvitationVersionConflict
	stale := revoke.Clone(revoke.Context())
	stale.Header.Set("Idempotency-Key", "invitation-revoke-stale")
	response = httptest.NewRecorder()
	identityInvitationResourceHandler(catalog, coordinator).ServeHTTP(response, stale)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale response=%d %s", response.Code, response.Body.String())
	}
}

func TestIdentityInvitationAcceptRejectsUnknownAndTrailingJSON(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus int
	}{
		"unknown member":    {`{"invitation_token":"midn_inv_value","email":"caller@example.com"}`, http.StatusBadRequest},
		"duplicate member":  {`{"invitation_token":"first","invitation_token":"midn_inv_value"}`, http.StatusBadRequest},
		"trailing document": {`{"invitation_token":"midn_inv_value"} {}`, http.StatusBadRequest},
		"oversized":         {`{"invitation_token":"` + strings.Repeat("x", int(sensitiveJSONBodyLimitBytes)) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			request := humanIdentityInvitationRequest(http.MethodPost, "/api/v1alpha/invitations/accept", test.body)
			request.Header.Set("Idempotency-Key", "invite-accept-1")
			response := httptest.NewRecorder()
			identityInvitationAcceptHandler(&identityInvitationCoordinatorStub{}).ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response=%d %s, want %d", response.Code, response.Body.String(), test.wantStatus)
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

func TestIdentityInvitationScopeRejectsDuplicateUnknownAndEmptyQueryValues(t *testing.T) {
	for _, target := range []string{
		"/api/v1alpha/invitations?workspace_id=wrk_1&workspace_id=wrk_2",
		"/api/v1alpha/invitations?workspace_id=wrk_1&project_id=",
		"/api/v1alpha/invitations?workspace_id=wrk_1&unexpected=value",
		"/api/v1alpha/invitations?workspace_id=wrk_1&bad=%zz",
	} {
		request := humanIdentityInvitationRequest(http.MethodGet, target, "")
		response := httptest.NewRecorder()
		identityInvitationCollectionHandler(&identityInvitationCatalogStub{}, nil, CursorCodec{}).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("target=%q response=%d %s", target, response.Code, response.Body.String())
		}
	}
}

func TestIdentityInvitationListUsesBoundedSignedCursorPagination(t *testing.T) {
	codec := NewCursorCodec([]byte("identity-invitation-pagination-test-key"))
	cursor, err := codec.Encode(CursorToken{Kind: CursorTokenSigned, Offset: 3})
	if err != nil {
		t.Fatal(err)
	}
	catalog := &identityInvitationCatalogStub{items: []identity.Invitation{
		{ID: "inv_4", WorkspaceID: "wrk_1"},
		{ID: "inv_5", WorkspaceID: "wrk_1"},
	}}
	request := humanIdentityInvitationRequest(
		http.MethodGet,
		"/api/v1alpha/invitations?workspace_id=wrk_1&limit=1&cursor="+cursor,
		"",
	)
	response := httptest.NewRecorder()
	identityInvitationCollectionHandler(catalog, nil, codec).ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		catalog.request.Limit != 2 ||
		catalog.request.Offset != 3 {
		t.Fatalf("response=%d %s request=%#v", response.Code, response.Body.String(), catalog.request)
	}
	var payload identityInvitationListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].ID != "inv_4" ||
		payload.NextCursor == "" {
		t.Fatalf("payload=%#v", payload)
	}
	next, err := codec.Decode(payload.NextCursor)
	if err != nil || next.Offset != 4 || next.Kind != CursorTokenSigned {
		t.Fatalf("next=%#v err=%v", next, err)
	}
}

func TestIdentityInvitationManagementRoutesAreExact(t *testing.T) {
	stack, _ := productReadDataFixture(t)
	stack.IdentityInvitationCatalog = &identityInvitationCatalogStub{}
	stack.IdentityInvitationMutations = &identityInvitationCoordinatorStub{}
	mux, _ := buildRouterWithDataStack(stack)
	for _, route := range []struct {
		method  string
		target  string
		pattern string
	}{
		{http.MethodGet, "/api/v1alpha/invitations?workspace_id=wrk_a", "/api/v1alpha/invitations"},
		{http.MethodPost, "/api/v1alpha/invitations/accept", "/api/v1alpha/invitations/accept"},
		{http.MethodPost, "/api/v1alpha/invitations/inv_a/reissue", "/api/v1alpha/invitations/{invitation_id}/reissue"},
		{http.MethodDelete, "/api/v1alpha/invitations/inv_a?workspace_id=wrk_a", "/api/v1alpha/invitations/{invitation_id}"},
		{http.MethodGet, "/api/v1alpha/invitations/inv_a?workspace_id=wrk_a", "/api/v1alpha/invitations/{invitation_id}"},
	} {
		request := httptest.NewRequest(route.method, route.target, nil)
		_, pattern := mux.Handler(request)
		if pattern != route.pattern {
			t.Fatalf("%s %s matched %q, want %q", route.method, route.target, pattern, route.pattern)
		}
	}
}

func humanIdentityInvitationRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	principal := identity.Principal{
		Kind: "user", UserID: "usr_1", SessionID: "ses_1",
		AuthMethod: "password", Email: "user@example.com",
	}
	session := identity.Session{ID: "ses_1", UserID: "usr_1", SessionGeneration: 1}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	return request.WithContext(ctx)
}
