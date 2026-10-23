// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

const automationTokenRouteSuffix = "/automation-tokens"

// AutomationTokenCatalog is the metadata-only read seam. The concrete
// automationtoken.Manager returns no verifier or recoverable credential.
type AutomationTokenCatalog interface {
	List(context.Context, string, string) ([]automationtoken.Token, error)
	Get(context.Context, string, string, string) (automationtoken.Token, error)
}

// AutomationTokenMutationCoordinator is the only write seam for managed
// project credentials. Implementations must linearize current session
// validity, project authorization, idempotency, credential storage, and audit.
type AutomationTokenMutationCoordinator interface {
	MintAutomationToken(context.Context, AutomationTokenMintMutationRequest) (TenantMutationResult, error)
	RevokeAutomationToken(context.Context, AutomationTokenRevokeMutationRequest) (TenantMutationResult, error)
}

type AutomationTokenMintMutationRequest struct {
	Principal   identity.Principal
	Session     identity.Session
	ProjectID   string
	Name        string
	ExpiresAt   time.Time
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type AutomationTokenRevokeMutationRequest struct {
	Principal       identity.Principal
	Session         identity.Session
	ProjectID       string
	TokenID         string
	ExpectedVersion int64
	Key             idempotency.Key
	Fingerprint     idempotency.RequestFingerprint
	RequestID       string
	SourceIP        string
	UserAgent       string
}

type automationTokenCreateRequest struct {
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expires_at"`
}

type automationTokenListResponse struct {
	Items []automationtoken.Token `json:"items"`
}

func automationTokenCollectionHandler(
	catalog AutomationTokenCatalog,
	access identity.AccessStore,
	coordinator AutomationTokenMutationCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		if projectID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", nil)
			return
		}
		principal, session, ok := humanAutomationTokenPrincipal(w, r)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			listAutomationTokens(w, r, catalog, access, principal, projectID)
		case http.MethodPost:
			mintAutomationToken(w, r, coordinator, principal, session, projectID)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func automationTokenResourceHandler(
	catalog AutomationTokenCatalog,
	access identity.AccessStore,
	coordinator AutomationTokenMutationCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		tokenID := strings.TrimSpace(r.PathValue("token_id"))
		if projectID == "" || tokenID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id and token_id are required", nil)
			return
		}
		principal, session, ok := humanAutomationTokenPrincipal(w, r)
		if !ok {
			return
		}
		token, ok := getAutomationTokenMetadata(w, r, catalog, access, principal, projectID, tokenID)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", strongVersionETag(token.ResourceVersion))
			WriteJSON(w, http.StatusOK, token)
			return
		}
		expectedVersion, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		key := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if err := idempotency.ValidateKey(key); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}
		result, err := coordinatorOrUnavailable(coordinator).RevokeAutomationToken(r.Context(), AutomationTokenRevokeMutationRequest{
			Principal:       principal,
			Session:         session,
			ProjectID:       projectID,
			TokenID:         tokenID,
			ExpectedVersion: expectedVersion,
			Key:             key,
			Fingerprint:     idempotencyRequestFingerprint(r, idempotency.BodyHashSHA256([]byte(strongVersionETag(expectedVersion)))),
			RequestID:       middleware.FromContext(r.Context()),
			SourceIP:        r.RemoteAddr,
			UserAgent:       r.UserAgent(),
		})
		writeAutomationTokenMutationResult(w, r, result, err, "revoke")
	}
}

func getAutomationTokenMetadata(
	w http.ResponseWriter,
	r *http.Request,
	catalog AutomationTokenCatalog,
	access identity.AccessStore,
	principal identity.Principal,
	projectID, tokenID string,
) (automationtoken.Token, bool) {
	if catalog == nil || access == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "project automation token management is not configured", nil)
		return automationtoken.Token{}, false
	}
	grant, _, err := identity.Authorize(r.Context(), access, principal.UserID, identity.AccessTarget{ProjectID: projectID}, identity.CapabilityProjectManage, false)
	if errors.Is(err, identity.ErrAccessNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
		return automationtoken.Token{}, false
	}
	if errors.Is(err, identity.ErrAuthorizationDenied) {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage project automation tokens", nil)
		return automationtoken.Token{}, false
	}
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project authorization is unavailable", nil)
		return automationtoken.Token{}, false
	}
	token, err := catalog.Get(r.Context(), grant.Target.WorkspaceID, projectID, tokenID)
	if errors.Is(err, automationtoken.ErrTokenNotFound) {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project automation token scope not found", nil)
		return automationtoken.Token{}, false
	}
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project automation tokens are unavailable", nil)
		return automationtoken.Token{}, false
	}
	return token, true
}

func listAutomationTokens(
	w http.ResponseWriter,
	r *http.Request,
	catalog AutomationTokenCatalog,
	access identity.AccessStore,
	principal identity.Principal,
	projectID string,
) {
	if catalog == nil || access == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "project automation token management is not configured", nil)
		return
	}
	grant, _, err := identity.Authorize(
		r.Context(),
		access,
		principal.UserID,
		identity.AccessTarget{ProjectID: projectID},
		identity.CapabilityProjectManage,
		false,
	)
	switch {
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project not found", nil)
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage project automation tokens", nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project authorization is unavailable", nil)
		return
	}
	tokens, err := catalog.List(r.Context(), grant.Target.WorkspaceID, projectID)
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project automation tokens are unavailable", nil)
		return
	}
	if tokens == nil {
		tokens = []automationtoken.Token{}
	}
	WriteJSON(w, http.StatusOK, automationTokenListResponse{Items: tokens})
}

func mintAutomationToken(
	w http.ResponseWriter,
	r *http.Request,
	coordinator AutomationTokenMutationCoordinator,
	principal identity.Principal,
	session identity.Session,
	projectID string,
) {
	var request automationTokenCreateRequest
	body, ok := decodeStrictJSONRequestBodyWithLimit(w, r, &request, "invalid automation token request", sensitiveJSONBodyLimitBytes)
	if !ok {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || len(request.Name) > 128 || request.ExpiresAt.IsZero() {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "name and a future expires_at are required", nil)
		return
	}
	key := idempotency.Key(r.Header.Get("Idempotency-Key"))
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	}
	result, err := coordinatorOrUnavailable(coordinator).MintAutomationToken(r.Context(), AutomationTokenMintMutationRequest{
		Principal:   principal,
		Session:     session,
		ProjectID:   projectID,
		Name:        request.Name,
		ExpiresAt:   request.ExpiresAt.UTC(),
		Key:         key,
		Fingerprint: idempotencyRequestFingerprint(r, idempotency.BodyHashSHA256(body)),
		RequestID:   middleware.FromContext(r.Context()),
		SourceIP:    r.RemoteAddr,
		UserAgent:   r.UserAgent(),
	})
	writeAutomationTokenMutationResult(w, r, result, err, "create")
}

func humanAutomationTokenPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, identity.Session, bool) {
	productPrincipal, classified := productPrincipalFromContext(r.Context())
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !classified || productPrincipal.Kind != productPrincipalHuman || !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "project automation tokens can be managed only by a human session", nil)
		return identity.Principal{}, identity.Session{}, false
	}
	return principal, session, true
}

func writeAutomationTokenMutationResult(w http.ResponseWriter, r *http.Request, result TenantMutationResult, err error, action string) {
	switch {
	case errors.Is(err, errAutomationTokenManagementUnavailable):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "project automation token management is not configured", nil)
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound),
		errors.Is(err, automationtoken.ErrTokenNotFound),
		errors.Is(err, automationtoken.ErrScopeNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "project automation token scope not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to manage project automation tokens", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, automationtoken.ErrInvalidRequest), errors.Is(err, idempotency.ErrInvalidKey):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid automation token "+action+" request", nil)
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "project automation token "+action+" is unavailable", nil)
	default:
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

var errAutomationTokenManagementUnavailable = errors.New("automation token management is unavailable")

type unavailableAutomationTokenCoordinator struct{}

func (unavailableAutomationTokenCoordinator) MintAutomationToken(context.Context, AutomationTokenMintMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errAutomationTokenManagementUnavailable
}

func (unavailableAutomationTokenCoordinator) RevokeAutomationToken(context.Context, AutomationTokenRevokeMutationRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, errAutomationTokenManagementUnavailable
}

func coordinatorOrUnavailable(coordinator AutomationTokenMutationCoordinator) AutomationTokenMutationCoordinator {
	if coordinator == nil {
		return unavailableAutomationTokenCoordinator{}
	}
	return coordinator
}
