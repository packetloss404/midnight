// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
)

const tenantGeneratedDomainBodyLimitBytes = 4 << 10

var (
	ErrTenantGeneratedDomainNotFound     = errors.New("tenant generated domain not found")
	ErrTenantGeneratedDomainConflict     = errors.New("tenant generated domain already exists")
	ErrTenantGeneratedDomainRetired      = errors.New("tenant generated domain identity is retired")
	ErrTenantGeneratedDomainsUnavailable = errors.New("tenant generated domains are unavailable")
	ErrTenantGeneratedAppsDomainMissing  = errors.New("generated apps domain is not configured")
)

// TenantGeneratedDomainVersionConflict reports a failed positive-version
// compare-and-swap without exposing any cross-tenant state.
type TenantGeneratedDomainVersionConflict struct {
	Expected int64
	Current  int64
}

func (e *TenantGeneratedDomainVersionConflict) Error() string {
	return fmt.Sprintf("generated domain version conflict: expected %d, current %d", e.Expected, e.Current)
}

// TenantGeneratedDomainView is the strict, redaction-safe product projection.
// Hostname and route state are desired/local ingress evidence; they do not
// prove public DNS or Internet reachability.
type TenantGeneratedDomainView struct {
	ID              string                 `json:"id"`
	WorkspaceID     string                 `json:"workspace_id"`
	ProjectID       string                 `json:"project_id"`
	ServiceID       string                 `json:"service_id"`
	EnvironmentID   string                 `json:"environment_id"`
	Kind            router.DomainKind      `json:"kind"`
	Hostname        string                 `json:"hostname"`
	Path            string                 `json:"path"`
	TargetPort      int                    `json:"target_port"`
	Public          bool                   `json:"public"`
	TLS             bool                   `json:"tls"`
	Version         int64                  `json:"version"`
	RouteApplyState router.RouteApplyState `json:"route_apply_state"`
	RouteReachable  bool                   `json:"route_reachable"`
	CreatedAt       time.Time              `json:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at"`
}

// TenantGeneratedDomainReadRequest carries only the verified human tuple and
// nested route identity. Workspace/project ownership is coordinator-derived.
type TenantGeneratedDomainReadRequest struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     string
	EnvironmentID string
	DomainID      string
}

// TenantGeneratedDomainMutationMeta is the complete redaction-safe transport
// evidence needed for authorization, exact replay, and audit.
type TenantGeneratedDomainMutationMeta struct {
	Principal     identity.Principal
	Session       identity.Session
	ServiceID     string
	EnvironmentID string
	DomainID      string
	Method        string
	Key           idempotency.Key
	RawBody       []byte
	RequestID     string
	SourceIP      string
	UserAgent     string
}

type TenantGeneratedDomainCreateRequest struct {
	TenantGeneratedDomainMutationMeta
}

type TenantGeneratedDomainDeleteRequest struct {
	TenantGeneratedDomainMutationMeta
	ExpectedVersion int64
}

// TenantGeneratedDomainCoordinator independently admits only generated HTTP
// domain list/create/get/delete for a verified human session. It cannot admit
// custom hostnames, paths, ports, TLS, project input, or the flat recovery API.
type TenantGeneratedDomainCoordinator interface {
	ListGeneratedDomains(context.Context, TenantGeneratedDomainReadRequest) ([]TenantGeneratedDomainView, error)
	GetGeneratedDomain(context.Context, TenantGeneratedDomainReadRequest) (TenantGeneratedDomainView, error)
	CreateGeneratedDomain(context.Context, TenantGeneratedDomainCreateRequest) (TenantMutationResult, error)
	DeleteGeneratedDomain(context.Context, TenantGeneratedDomainDeleteRequest) (TenantMutationResult, error)
}

type tenantGeneratedDomainCreateBody struct{}

type tenantGeneratedDomainDeleteBody struct {
	ExpectedVersion *int64 `json:"expected_version"`
}

func tenantGeneratedDomainsHandler(coordinator TenantGeneratedDomainCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productPrincipal, classified := productPrincipalFromContext(r.Context())
		if !classified || productPrincipal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "generated domains not found", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant generated domains are unavailable", nil)
			return
		}

		principal, principalOK := identityPrincipalFromContext(r.Context())
		session, sessionOK := identitySessionFromContext(r.Context())
		if !principalOK || !sessionOK || principal.UserID == "" || session.UserID != principal.UserID {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
			return
		}

		serviceID := strings.TrimSpace(r.PathValue("service_id"))
		environmentID := strings.TrimSpace(r.PathValue("environment_id"))
		domainID := strings.TrimSpace(r.PathValue("domain_id"))
		if serviceID == "" || environmentID == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service or environment not found", nil)
			return
		}
		read := TenantGeneratedDomainReadRequest{
			Principal:     principal,
			Session:       session,
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			DomainID:      domainID,
		}

		if domainID == "" {
			switch r.Method {
			case http.MethodGet:
				values, err := coordinator.ListGeneratedDomains(r.Context(), read)
				if writeTenantGeneratedDomainError(w, r, err) {
					return
				}
				if values == nil {
					values = []TenantGeneratedDomainView{}
				}
				WriteJSON(w, http.StatusOK, values)
			case http.MethodPost:
				createTenantGeneratedDomain(w, r, coordinator, read)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}

		switch r.Method {
		case http.MethodGet:
			value, err := coordinator.GetGeneratedDomain(r.Context(), read)
			if writeTenantGeneratedDomainError(w, r, err) {
				return
			}
			w.Header().Set("ETag", strongVersionETag(value.Version))
			WriteJSON(w, http.StatusOK, value)
		case http.MethodDelete:
			deleteTenantGeneratedDomain(w, r, coordinator, read)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func createTenantGeneratedDomain(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantGeneratedDomainCoordinator,
	read TenantGeneratedDomainReadRequest,
) {
	var body tenantGeneratedDomainCreateBody
	raw, ok := readStrictTenantGeneratedDomainBody(w, r, &body)
	if !ok {
		return
	}
	key, ok := requireTenantGeneratedDomainIdempotencyKey(w, r)
	if !ok {
		return
	}
	result, err := coordinator.CreateGeneratedDomain(r.Context(), TenantGeneratedDomainCreateRequest{
		TenantGeneratedDomainMutationMeta: tenantGeneratedDomainMutationMeta(r, read, key, raw),
	})
	if writeTenantGeneratedDomainError(w, r, err) {
		return
	}
	writeTenantGeneratedDomainMutationResult(w, result)
}

func deleteTenantGeneratedDomain(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantGeneratedDomainCoordinator,
	read TenantGeneratedDomainReadRequest,
) {
	var body tenantGeneratedDomainDeleteBody
	raw, ok := readStrictTenantGeneratedDomainBody(w, r, &body)
	if !ok {
		return
	}
	if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be at least 1", map[string]any{"field": "expected_version"})
		return
	}
	if !requireMatchingExpectedVersion(w, r, *body.ExpectedVersion) {
		return
	}
	key, ok := requireTenantGeneratedDomainIdempotencyKey(w, r)
	if !ok {
		return
	}
	result, err := coordinator.DeleteGeneratedDomain(r.Context(), TenantGeneratedDomainDeleteRequest{
		TenantGeneratedDomainMutationMeta: tenantGeneratedDomainMutationMeta(r, read, key, raw),
		ExpectedVersion:                   *body.ExpectedVersion,
	})
	if writeTenantGeneratedDomainError(w, r, err) {
		return
	}
	writeTenantGeneratedDomainMutationResult(w, result)
}

func tenantGeneratedDomainMutationMeta(
	r *http.Request,
	read TenantGeneratedDomainReadRequest,
	key idempotency.Key,
	raw []byte,
) TenantGeneratedDomainMutationMeta {
	return TenantGeneratedDomainMutationMeta{
		Principal:     read.Principal,
		Session:       read.Session,
		ServiceID:     read.ServiceID,
		EnvironmentID: read.EnvironmentID,
		DomainID:      read.DomainID,
		Method:        r.Method,
		Key:           key,
		RawBody:       append([]byte(nil), raw...),
		RequestID:     middleware.FromContext(r.Context()),
		SourceIP:      r.RemoteAddr,
		UserAgent:     r.UserAgent(),
	}
}

func readStrictTenantGeneratedDomainBody(w http.ResponseWriter, r *http.Request, target any) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, tenantGeneratedDomainBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "generated domain request body exceeds the configured limit", map[string]any{"limit": tenantGeneratedDomainBodyLimitBytes})
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "generated domain request body must be one JSON object", nil)
		return nil, false
	}
	if err := rejectDuplicateTopLevelJSONFields(body); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid generated domain request body", nil)
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid generated domain request body", nil)
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "generated domain request body must contain exactly one JSON object", nil)
		return nil, false
	}
	return body, true
}

func requireTenantGeneratedDomainIdempotencyKey(w http.ResponseWriter, r *http.Request) (idempotency.Key, bool) {
	key, failure := singletonIdempotencyKey(r.Header)
	switch failure {
	case "":
		return key, true
	case failurecodes.IdempotencyKeyRequired:
		writeRequestError(w, r, http.StatusBadRequest, failure, "exactly one Idempotency-Key header is required", nil)
	default:
		writeRequestError(w, r, http.StatusBadRequest, failure, "invalid idempotency key", nil)
	}
	return "", false
}

func writeTenantGeneratedDomainMutationResult(w http.ResponseWriter, result TenantMutationResult) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func writeTenantGeneratedDomainError(w http.ResponseWriter, r *http.Request, err error) bool {
	var versionConflict *TenantGeneratedDomainVersionConflict
	switch {
	case err == nil:
		return false
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, ErrTenantGeneratedDomainNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service, environment, or generated domain not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to access generated domains", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with a different request", nil)
	case errors.As(err, &versionConflict):
		writeIfMatchFailed(w, r, "generated domain version changed", map[string]any{"expected_version": versionConflict.Expected, "current_version": versionConflict.Current})
	case errors.Is(err, ErrTenantGeneratedDomainConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "generated domain already exists", nil)
	case errors.Is(err, ErrTenantGeneratedDomainRetired):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "generated domain identity was retired and cannot be reused", nil)
	case errors.Is(err, ErrTenantGeneratedAppsDomainMissing):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "generated domains require the server apps domain to be configured", nil)
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	case errors.Is(err, ErrTenantGeneratedDomainsUnavailable):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant generated domains are unavailable", nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant generated domains are unavailable", nil)
	}
	return true
}
