// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const invitationRoute = "/invitations"

// IdentityInvitationCatalog lists exact-scope, non-secret invitation metadata.
// Implementations own the authorization decision so tenant scope and actor
// authority are checked against the same durable state used for the read.
type IdentityInvitationCatalog interface {
	List(context.Context, identity.ListInvitationsRequest) ([]identity.Invitation, error)
	Get(context.Context, identity.GetInvitationRequest) (identity.Invitation, error)
}

// IdentityInvitationMutationCoordinator is the only HTTP write seam. Durable
// implementations linearize current session validity, authorization,
// invitation state, membership changes, idempotency, and audit evidence.
type IdentityInvitationMutationCoordinator interface {
	CreateIdentityInvitation(context.Context, IdentityInvitationCreateMutationRequest) (TenantMutationResult, error)
	ReissueIdentityInvitation(context.Context, IdentityInvitationReissueMutationRequest) (TenantMutationResult, error)
	RevokeIdentityInvitation(context.Context, IdentityInvitationRevokeMutationRequest) (TenantMutationResult, error)
	AcceptIdentityInvitation(context.Context, IdentityInvitationAcceptMutationRequest) (TenantMutationResult, error)
}

type identityInvitationMutationContext struct {
	Principal   identity.Principal
	Session     identity.Session
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type IdentityInvitationCreateMutationRequest struct {
	identityInvitationMutationContext
	Email         string
	WorkspaceID   string
	ProjectID     string
	WorkspaceRole identity.WorkspaceRole
	ProjectRole   identity.ProjectRole
	ExpiresAt     time.Time
}

type IdentityInvitationReissueMutationRequest struct {
	identityInvitationMutationContext
	InvitationID string
	WorkspaceID  string
	ProjectID    string
	ExpiresAt    time.Time
}

type IdentityInvitationRevokeMutationRequest struct {
	identityInvitationMutationContext
	InvitationID    string
	WorkspaceID     string
	ProjectID       string
	ExpectedVersion int64
}

type IdentityInvitationAcceptMutationRequest struct {
	identityInvitationMutationContext
	InvitationToken string
}

type identityInvitationCreateRequest struct {
	Email         string                 `json:"email"`
	WorkspaceID   string                 `json:"workspace_id"`
	ProjectID     string                 `json:"project_id,omitempty"`
	WorkspaceRole identity.WorkspaceRole `json:"workspace_role,omitempty"`
	ProjectRole   identity.ProjectRole   `json:"project_role,omitempty"`
	ExpiresAt     time.Time              `json:"expires_at"`
}

type identityInvitationReissueRequest struct {
	WorkspaceID string    `json:"workspace_id"`
	ProjectID   string    `json:"project_id,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type identityInvitationAcceptRequest struct {
	InvitationToken string `json:"invitation_token"`
}

type identityInvitationListResponse struct {
	Items      []identity.Invitation `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

func identityInvitationCollectionHandler(
	catalog IdentityInvitationCatalog,
	coordinator IdentityInvitationMutationCoordinator,
	cursorCodec CursorCodec,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		principal, session, ok := humanIdentityInvitationPrincipal(w, r)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			listIdentityInvitations(w, r, catalog, cursorCodec, principal)
		case http.MethodPost:
			createIdentityInvitation(w, r, coordinator, principal, session)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func identityInvitationResourceHandler(
	catalog IdentityInvitationCatalog,
	coordinator IdentityInvitationMutationCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, session, ok := humanIdentityInvitationPrincipal(w, r)
		if !ok {
			return
		}
		invitationID := strings.TrimSpace(r.PathValue("invitation_id"))
		workspaceID, projectID, scopeOK := identityInvitationScopeQuery(r, false)
		if invitationID == "" || !scopeOK {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation_id and workspace_id are required", nil)
			return
		}
		if catalog == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "identity invitation management is not configured", nil)
			return
		}
		invitation, err := catalog.Get(r.Context(), identity.GetInvitationRequest{
			InvitationID: invitationID,
			WorkspaceID:  workspaceID,
			ProjectID:    projectID,
			ActorUserID:  principal.UserID,
		})
		if err != nil {
			writeIdentityInvitationReadError(w, r, err)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", strongVersionETag(invitation.ResourceVersion))
			WriteJSON(w, http.StatusOK, invitation)
			return
		}
		expectedVersion, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		mutationContext, ok := identityInvitationRequestContext(
			w, r, principal, session, []byte(strongVersionETag(expectedVersion)),
		)
		if !ok {
			return
		}
		result, err := identityInvitationCoordinatorOrUnavailable(coordinator).RevokeIdentityInvitation(
			r.Context(),
			IdentityInvitationRevokeMutationRequest{
				identityInvitationMutationContext: mutationContext,
				InvitationID:                      invitationID,
				WorkspaceID:                       workspaceID,
				ProjectID:                         projectID,
				ExpectedVersion:                   expectedVersion,
			},
		)
		writeIdentityInvitationMutationResult(w, r, result, err, "revoke")
	}
}

func identityInvitationReissueHandler(coordinator IdentityInvitationMutationCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, session, ok := humanIdentityInvitationPrincipal(w, r)
		if !ok {
			return
		}
		if !identityInvitationHasNoQuery(r) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation reissue does not accept query parameters", nil)
			return
		}
		invitationID := strings.TrimSpace(r.PathValue("invitation_id"))
		body, ok := readStrictIdentityInvitationJSON(w, r, "reissue", &identityInvitationReissueRequest{})
		if !ok {
			return
		}
		request := body.(*identityInvitationReissueRequest)
		request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
		request.ProjectID = strings.TrimSpace(request.ProjectID)
		if invitationID == "" || request.WorkspaceID == "" || request.ExpiresAt.IsZero() {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation_id, workspace_id, and expires_at are required", nil)
			return
		}
		mutationContext, ok := identityInvitationRequestContext(w, r, principal, session, requestRawBody(r.Context()))
		if !ok {
			return
		}
		result, err := identityInvitationCoordinatorOrUnavailable(coordinator).ReissueIdentityInvitation(
			r.Context(),
			IdentityInvitationReissueMutationRequest{
				identityInvitationMutationContext: mutationContext,
				InvitationID:                      invitationID,
				WorkspaceID:                       request.WorkspaceID,
				ProjectID:                         request.ProjectID,
				ExpiresAt:                         request.ExpiresAt.UTC(),
			},
		)
		writeIdentityInvitationMutationResult(w, r, result, err, "reissue")
	}
}

func identityInvitationAcceptHandler(coordinator IdentityInvitationMutationCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		principal, session, ok := humanIdentityInvitationPrincipal(w, r)
		if !ok {
			return
		}
		if !identityInvitationHasNoQuery(r) {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation acceptance does not accept query parameters", nil)
			return
		}
		body, ok := readStrictIdentityInvitationJSON(w, r, "accept", &identityInvitationAcceptRequest{})
		if !ok {
			return
		}
		request := body.(*identityInvitationAcceptRequest)
		request.InvitationToken = strings.TrimSpace(request.InvitationToken)
		if request.InvitationToken == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation_token is required", nil)
			return
		}
		mutationContext, ok := identityInvitationRequestContext(w, r, principal, session, requestRawBody(r.Context()))
		if !ok {
			return
		}
		result, err := identityInvitationCoordinatorOrUnavailable(coordinator).AcceptIdentityInvitation(
			r.Context(),
			IdentityInvitationAcceptMutationRequest{
				identityInvitationMutationContext: mutationContext,
				InvitationToken:                   request.InvitationToken,
			},
		)
		writeIdentityInvitationMutationResult(w, r, result, err, "accept")
	}
}

func listIdentityInvitations(
	w http.ResponseWriter,
	r *http.Request,
	catalog IdentityInvitationCatalog,
	cursorCodec CursorCodec,
	principal identity.Principal,
) {
	workspaceID, projectID, ok := identityInvitationScopeQuery(r, true)
	if !ok {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "workspace_id is required", nil)
		return
	}
	limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
	if errors.Is(err, orailtypes.ErrPageLimitExceeded) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.PaginationLimitExceeded, "page limit exceeds the locked maximum", map[string]any{"max": orailtypes.MaxPageLimit})
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid page limit", nil)
		return
	}
	offset := 0
	if rawCursor := r.URL.Query().Get("cursor"); rawCursor != "" {
		token, err := cursorCodec.Decode(rawCursor)
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.CursorInvalid, "invalid invitation cursor", nil)
			return
		}
		offset = token.Offset
	}
	if catalog == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "identity invitation management is not configured", nil)
		return
	}
	items, err := catalog.List(r.Context(), identity.ListInvitationsRequest{
		WorkspaceID: workspaceID,
		ProjectID:   projectID,
		ActorUserID: principal.UserID,
		Limit:       limit + 1,
		Offset:      offset,
	})
	if err != nil {
		writeIdentityInvitationReadError(w, r, err)
		return
	}
	if items == nil {
		items = []identity.Invitation{}
	}
	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		nextCursor, err = cursorCodec.Encode(CursorToken{
			Kind:   CursorTokenSigned,
			Offset: offset + limit,
		})
		if err != nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity invitation pagination is unavailable", nil)
			return
		}
	}
	WriteJSON(w, http.StatusOK, identityInvitationListResponse{
		Items:      items,
		NextCursor: nextCursor,
	})
}

func createIdentityInvitation(
	w http.ResponseWriter,
	r *http.Request,
	coordinator IdentityInvitationMutationCoordinator,
	principal identity.Principal,
	session identity.Session,
) {
	if !identityInvitationHasNoQuery(r) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invitation creation does not accept query parameters", nil)
		return
	}
	body, ok := readStrictIdentityInvitationJSON(w, r, "create", &identityInvitationCreateRequest{})
	if !ok {
		return
	}
	request := body.(*identityInvitationCreateRequest)
	request.Email = strings.ToLower(strings.TrimSpace(request.Email))
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	workspaceRole, workspaceRoleOK := identity.ParseWorkspaceRole(string(request.WorkspaceRole))
	projectRole, projectRoleOK := identity.ParseProjectRole(string(request.ProjectRole))
	validScope := (request.ProjectID == "" && workspaceRoleOK && request.ProjectRole == "") ||
		(request.ProjectID != "" && projectRoleOK && request.WorkspaceRole == "")
	if request.Email == "" || request.WorkspaceID == "" || request.ExpiresAt.IsZero() || !validScope {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "email, workspace_id, exactly one scope role, and expires_at are required", nil)
		return
	}
	if workspaceRoleOK {
		request.WorkspaceRole = workspaceRole
	}
	if projectRoleOK {
		request.ProjectRole = projectRole
	}
	mutationContext, ok := identityInvitationRequestContext(w, r, principal, session, requestRawBody(r.Context()))
	if !ok {
		return
	}
	result, err := identityInvitationCoordinatorOrUnavailable(coordinator).CreateIdentityInvitation(
		r.Context(),
		IdentityInvitationCreateMutationRequest{
			identityInvitationMutationContext: mutationContext,
			Email:                             request.Email,
			WorkspaceID:                       request.WorkspaceID,
			ProjectID:                         request.ProjectID,
			WorkspaceRole:                     request.WorkspaceRole,
			ProjectRole:                       request.ProjectRole,
			ExpiresAt:                         request.ExpiresAt.UTC(),
		},
	)
	writeIdentityInvitationMutationResult(w, r, result, err, "create")
}

func identityInvitationScopeQuery(r *http.Request, allowPagination bool) (string, string, bool) {
	if r == nil || r.URL == nil {
		return "", "", false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", false
	}
	for key := range values {
		if key != "workspace_id" && key != "project_id" &&
			(!allowPagination || (key != "limit" && key != "cursor")) {
			return "", "", false
		}
	}
	workspaces, ok := values["workspace_id"]
	if !ok || len(workspaces) != 1 {
		return "", "", false
	}
	workspaceID := strings.TrimSpace(workspaces[0])
	if workspaceID == "" {
		return "", "", false
	}
	projects := values["project_id"]
	if len(projects) > 1 {
		return "", "", false
	}
	projectID := ""
	if len(projects) == 1 {
		projectID = strings.TrimSpace(projects[0])
		if projectID == "" {
			return "", "", false
		}
	}
	for _, key := range []string{"limit", "cursor"} {
		if entries := values[key]; len(entries) > 1 {
			return "", "", false
		}
	}
	return workspaceID, projectID, true
}

func identityInvitationHasNoQuery(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	return err == nil && len(values) == 0
}

type identityInvitationRawBodyContextKey struct{}

func readStrictIdentityInvitationJSON(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	dst any,
) (any, bool) {
	body, ok := decodeStrictJSONRequestBodyWithLimit(
		w,
		r,
		dst,
		"invalid identity invitation "+action+" request",
		sensitiveJSONBodyLimitBytes,
	)
	if !ok {
		return nil, false
	}
	*r = *r.WithContext(context.WithValue(r.Context(), identityInvitationRawBodyContextKey{}, append([]byte(nil), body...)))
	return dst, true
}

func requestRawBody(ctx context.Context) []byte {
	body, _ := ctx.Value(identityInvitationRawBodyContextKey{}).([]byte)
	return append([]byte(nil), body...)
}

func identityInvitationRequestContext(
	w http.ResponseWriter,
	r *http.Request,
	principal identity.Principal,
	session identity.Session,
	body []byte,
) (identityInvitationMutationContext, bool) {
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return identityInvitationMutationContext{}, false
	}
	return identityInvitationMutationContext{
		Principal:   principal,
		Session:     session,
		Key:         key,
		Fingerprint: idempotencyRequestFingerprint(r, idempotency.BodyHashSHA256(body)),
		RequestID:   middleware.FromContext(r.Context()),
		SourceIP:    r.RemoteAddr,
		UserAgent:   r.UserAgent(),
	}, true
}

func humanIdentityInvitationPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, identity.Session, bool) {
	productPrincipal, classified := productPrincipalFromContext(r.Context())
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !classified || productPrincipal.Kind != productPrincipalHuman || !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "identity invitations can be managed only by a human session", nil)
		return identity.Principal{}, identity.Session{}, false
	}
	return principal, session, true
}

func writeIdentityInvitationReadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, identity.ErrInvitationNotFound), errors.Is(err, identity.ErrInvitationScopeNotActive):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "identity invitation scope not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage identity invitations", nil)
	case errors.Is(err, identity.ErrInvitationVersionConflict):
		writeIfMatchFailed(w, r, "If-Match does not identify the current identity invitation version", nil)
	case errors.Is(err, identity.ErrInvalidInvitation):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid identity invitation request", nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity invitations are unavailable", nil)
	}
}

func writeIdentityInvitationMutationResult(
	w http.ResponseWriter,
	r *http.Request,
	result TenantMutationResult,
	err error,
	action string,
) {
	switch {
	case errors.Is(err, errIdentityInvitationManagementUnavailable):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "identity invitation management is not configured", nil)
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound),
		errors.Is(err, identity.ErrInvitationNotFound),
		errors.Is(err, identity.ErrInvitationScopeNotActive):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "identity invitation scope not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage identity invitations", nil)
	case errors.Is(err, identity.ErrInvitationVersionConflict):
		writeIfMatchFailed(w, r, "If-Match does not identify the current identity invitation version", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, identity.ErrInvitationPending),
		errors.Is(err, identity.ErrInvitationExpired),
		errors.Is(err, identity.ErrInvitationRevoked),
		errors.Is(err, identity.ErrInvitationConsumed),
		errors.Is(err, identity.ErrInvitationReplaced),
		errors.Is(err, identity.ErrInvitationEmailMismatch),
		errors.Is(err, identity.ErrInvitationMemberExists):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "identity invitation "+action+" cannot be completed in its current state", nil)
	case errors.Is(err, identity.ErrInvalidInvitation), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid identity invitation "+action+" request", nil)
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity invitation "+action+" is unavailable", nil)
	default:
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

var errIdentityInvitationManagementUnavailable = errors.New("identity invitation management is unavailable")

type unavailableIdentityInvitationCoordinator struct{}

func (unavailableIdentityInvitationCoordinator) CreateIdentityInvitation(context.Context, IdentityInvitationCreateMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errIdentityInvitationManagementUnavailable
}

func (unavailableIdentityInvitationCoordinator) ReissueIdentityInvitation(context.Context, IdentityInvitationReissueMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errIdentityInvitationManagementUnavailable
}

func (unavailableIdentityInvitationCoordinator) RevokeIdentityInvitation(context.Context, IdentityInvitationRevokeMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errIdentityInvitationManagementUnavailable
}

func (unavailableIdentityInvitationCoordinator) AcceptIdentityInvitation(context.Context, IdentityInvitationAcceptMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errIdentityInvitationManagementUnavailable
}

func identityInvitationCoordinatorOrUnavailable(coordinator IdentityInvitationMutationCoordinator) IdentityInvitationMutationCoordinator {
	if coordinator == nil {
		return unavailableIdentityInvitationCoordinator{}
	}
	return coordinator
}
