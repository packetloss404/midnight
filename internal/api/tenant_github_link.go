// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantGitHubLinkBodyLimitBytes int64 = 16 << 10

var (
	ErrTenantGitHubSourceUnavailable  = errors.New("tenant github source dependency is unavailable")
	ErrTenantGitHubSourceUnconfigured = errors.New("tenant github app is not configured")
	ErrTenantGitHubSourceConflict     = errors.New("tenant github source identity conflicts with its workspace grant")
)

// GitHubSourceVerifier is the least-privilege live GitHub boundary used after
// local authorization. Its implementation mints tokens scoped to one numeric
// repository instead of exposing an installation-wide token to product paths.
type GitHubSourceVerifier interface {
	GetRepositoryForInstallation(context.Context, int64, int64) (github.Repo, error)
	ListBranchesForRepository(context.Context, int64, int64) ([]github.Branch, error)
	VerifyRepositoryBranch(context.Context, int64, int64, string) (github.Repo, error)
}

type TenantGitHubIdentityRequest struct {
	Principal identity.Principal
	Session   identity.Session
}

type TenantGitHubProjectRequest struct {
	TenantGitHubIdentityRequest
	ProjectID string
}

type TenantGitHubProjectRepositoryRequest struct {
	TenantGitHubProjectRequest
	RepositoryID int64
}

type TenantGitHubServiceRequest struct {
	TenantGitHubIdentityRequest
	ServiceID orailtypes.ID
}

type TenantGitHubLinkMutationRequest struct {
	TenantGitHubServiceRequest
	InstallationID int64
	RepositoryID   int64
	Branch         string
	Key            idempotency.Key
	Fingerprint    idempotency.RequestFingerprint
	RequestID      string
	SourceIP       string
	UserAgent      string
}

type TenantGitHubLinkDeleteRequest struct {
	TenantGitHubServiceRequest
	Key         idempotency.Key
	Fingerprint idempotency.RequestFingerprint
	RequestID   string
	SourceIP    string
	UserAgent   string
}

type TenantGitHubRepositoryView struct {
	InstallationID  int64  `json:"installation_id"`
	RepositoryID    int64  `json:"repository_id"`
	Repository      string `json:"repository_full_name"`
	AccountLogin    string `json:"account_login,omitempty"`
	Private         bool   `json:"private"`
	DefaultBranch   string `json:"default_branch"`
	ResourceVersion int64  `json:"resource_version,omitempty"`
}

type TenantGitHubLinkView struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Repository     string `json:"repository_full_name"`
	Branch         string `json:"branch"`
	BindingState   string `json:"binding_state"`

	RepositoryAccessVerifiedAt string `json:"repository_access_verified_at"`
	UpdatedAt                  string `json:"updated_at"`
}

type TenantGitHubLinkMutationResult struct {
	Response idempotency.StoredResponse
	Replayed bool
}

// TenantGitHubSourceLinkCoordinator keeps product routes closed until one
// implementation can prove current session, membership, capability, workspace
// grant, source identity, idempotency, write, and audit evidence.
type TenantGitHubSourceLinkCoordinator interface {
	ListProjectRepositories(context.Context, TenantGitHubProjectRequest) ([]TenantGitHubRepositoryView, error)
	ListProjectRepositoryBranches(context.Context, TenantGitHubProjectRepositoryRequest) ([]github.Branch, error)
	GetServiceLink(context.Context, TenantGitHubServiceRequest) (TenantGitHubLinkView, error)
	PutServiceLink(context.Context, TenantGitHubLinkMutationRequest) (TenantGitHubLinkMutationResult, error)
	DeleteServiceLink(context.Context, TenantGitHubLinkDeleteRequest) (TenantGitHubLinkMutationResult, error)
}

type tenantGitHubLinkBody struct {
	InstallationID int64  `json:"installation_id"`
	RepositoryID   int64  `json:"repository_id"`
	Branch         string `json:"branch"`
}

func tenantGitHubProjectRepositoriesHandler(
	coordinator TenantGitHubSourceLinkCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant GitHub source discovery is unavailable", nil)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		identityRequest, ok := tenantGitHubIdentityFromRequest(w, r)
		if !ok {
			return
		}
		projectID := strings.TrimSpace(r.PathValue("project_id"))
		repositoryIDText := strings.TrimSpace(r.PathValue("repository_id"))
		if projectID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project id is required", nil)
			return
		}
		if repositoryIDText == "" {
			items, err := coordinator.ListProjectRepositories(r.Context(), TenantGitHubProjectRequest{
				TenantGitHubIdentityRequest: identityRequest,
				ProjectID:                   projectID,
			})
			if tenantGitHubReadError(w, r, err, "GitHub repository discovery") {
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": items})
			return
		}
		repositoryID, err := strconv.ParseInt(repositoryIDText, 10, 64)
		if err != nil || repositoryID <= 0 {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "repository id must be a positive integer", nil)
			return
		}
		items, err := coordinator.ListProjectRepositoryBranches(r.Context(), TenantGitHubProjectRepositoryRequest{
			TenantGitHubProjectRequest: TenantGitHubProjectRequest{
				TenantGitHubIdentityRequest: identityRequest,
				ProjectID:                   projectID,
			},
			RepositoryID: repositoryID,
		})
		if tenantGitHubReadError(w, r, err, "GitHub branch discovery") {
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func tenantGitHubServiceLinkHandler(
	coordinator TenantGitHubSourceLinkCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant GitHub source linking is unavailable", nil)
			return
		}
		identityRequest, ok := tenantGitHubIdentityFromRequest(w, r)
		if !ok {
			return
		}
		serviceID := orailtypes.ID(strings.TrimSpace(r.PathValue("service_id")))
		if serviceID == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service id is required", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			view, err := coordinator.GetServiceLink(r.Context(), TenantGitHubServiceRequest{
				TenantGitHubIdentityRequest: identityRequest,
				ServiceID:                   serviceID,
			})
			if tenantGitHubReadError(w, r, err, "GitHub source link") {
				return
			}
			WriteJSON(w, http.StatusOK, view)
		case http.MethodPut:
			putTenantGitHubServiceLink(w, r, coordinator, identityRequest, serviceID)
		case http.MethodDelete:
			deleteTenantGitHubServiceLink(w, r, coordinator, identityRequest, serviceID)
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func putTenantGitHubServiceLink(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantGitHubSourceLinkCoordinator,
	identityRequest TenantGitHubIdentityRequest,
	serviceID orailtypes.ID,
) {
	key, ok := tenantGitHubIdempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := readTenantGitHubBody(w, r, false)
	if !ok {
		return
	}
	var request tenantGitHubLinkBody
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid GitHub source link request body", map[string]any{"reason": "json"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub source link request body must contain exactly one JSON document", map[string]any{"reason": "trailing_json"})
		return
	}
	request.Branch = strings.TrimSpace(request.Branch)
	if request.InstallationID <= 0 ||
		request.RepositoryID <= 0 ||
		request.Branch == "" ||
		strings.HasPrefix(request.Branch, "refs/") ||
		len(request.Branch) > 255 ||
		strings.IndexFunc(request.Branch, func(r rune) bool {
			return r <= 0x20 || r == 0x7f
		}) >= 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "installation_id, repository_id, and a canonical branch are required", nil)
		return
	}
	result, err := coordinator.PutServiceLink(r.Context(), TenantGitHubLinkMutationRequest{
		TenantGitHubServiceRequest: TenantGitHubServiceRequest{
			TenantGitHubIdentityRequest: identityRequest,
			ServiceID:                   serviceID,
		},
		InstallationID: request.InstallationID,
		RepositoryID:   request.RepositoryID,
		Branch:         request.Branch,
		Key:            key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPut,
			Path:       tenantGitHubLinkIdempotencyPath(serviceID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	tenantGitHubWriteResult(w, r, result, err)
}

func deleteTenantGitHubServiceLink(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantGitHubSourceLinkCoordinator,
	identityRequest TenantGitHubIdentityRequest,
	serviceID orailtypes.ID,
) {
	key, ok := tenantGitHubIdempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := readTenantGitHubBody(w, r, true)
	if !ok {
		return
	}
	result, err := coordinator.DeleteServiceLink(r.Context(), TenantGitHubLinkDeleteRequest{
		TenantGitHubServiceRequest: TenantGitHubServiceRequest{
			TenantGitHubIdentityRequest: identityRequest,
			ServiceID:                   serviceID,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodDelete,
			Path:       tenantGitHubLinkIdempotencyPath(serviceID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	tenantGitHubWriteResult(w, r, result, err)
}

func tenantGitHubIdentityFromRequest(
	w http.ResponseWriter,
	r *http.Request,
) (TenantGitHubIdentityRequest, bool) {
	productPrincipal, classified := productPrincipalFromContext(r.Context())
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !classified ||
		productPrincipal.Kind != productPrincipalHuman ||
		!principalOK ||
		!sessionOK ||
		principal.UserID == "" ||
		session.UserID != principal.UserID {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "a valid human session is required", nil)
		return TenantGitHubIdentityRequest{}, false
	}
	return TenantGitHubIdentityRequest{Principal: principal, Session: session}, true
}

func tenantGitHubIdempotencyKey(w http.ResponseWriter, r *http.Request) (idempotency.Key, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) != 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "exactly one Idempotency-Key header is required", nil)
		return "", false
	}
	key := idempotency.Key(values[0])
	if strings.Contains(string(key), ",") || idempotency.ValidateKey(key) != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return "", false
	}
	return key, true
}

func readTenantGitHubBody(w http.ResponseWriter, r *http.Request, mustBeEmpty bool) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, tenantGitHubLinkBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "GitHub source link request body exceeds the configured limit", map[string]any{"limit": tenantGitHubLinkBodyLimitBytes})
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	if mustBeEmpty && len(body) != 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub source unlink requests must have an empty body", nil)
		return nil, false
	}
	if !mustBeEmpty && len(bytes.TrimSpace(body)) == 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "GitHub source link request body is required", nil)
		return nil, false
	}
	return body, true
}

func tenantGitHubReadError(w http.ResponseWriter, r *http.Request, err error, subject string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound),
		errors.Is(err, github.ErrSourceLinkNotFound),
		errors.Is(err, github.ErrRepositoryGrantNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, subject+" was not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to access "+subject, nil)
	case errors.Is(err, ErrTenantGitHubSourceUnconfigured):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "GitHub App integration is not configured", nil)
	case errors.Is(err, ErrTenantGitHubSourceConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "GitHub repository identity changed; a recovery operator must refresh its workspace grant", nil)
	case errors.Is(err, ErrTenantGitHubSourceUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, subject+" is temporarily unavailable", nil)
	default:
		var appErr *github.AppHTTPError
		if errors.As(err, &appErr) && appErr.StatusCode == http.StatusNotFound {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "GitHub repository or branch was not found", nil)
			break
		}
		if errors.Is(err, github.ErrAppHTTP) {
			writeRequestError(w, r, http.StatusBadGateway, failurecodes.InternalError, "GitHub rejected the source verification request", nil)
			break
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, subject+" failed", nil)
	}
	return true
}

func tenantGitHubWriteResult(
	w http.ResponseWriter,
	r *http.Request,
	result TenantGitHubLinkMutationResult,
	err error,
) {
	switch {
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint),
		errors.Is(err, github.ErrLinkInvalid):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid GitHub source link request", nil)
	case err != nil:
		_ = tenantGitHubReadError(w, r, err, "GitHub source link")
	default:
		if result.Response.StatusCode < 100 {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "GitHub source link mutation failed", nil)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func tenantGitHubLinkIdempotencyPath(serviceID orailtypes.ID) string {
	return v1alphaPrefix + "/services/" + string(serviceID) + "/github"
}
