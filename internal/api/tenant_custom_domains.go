// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const tenantCustomDomainBodyLimitBytes = 4 << 10

var (
	ErrTenantCustomDomainNotFound        = errors.New("tenant custom domain not found")
	ErrTenantCustomDomainConflict        = errors.New("tenant custom domain already exists")
	ErrTenantCustomDomainUnavailable     = errors.New("tenant custom domains are unavailable")
	ErrTenantCustomDomainVersionConflict = errors.New("tenant custom domain version changed")
	ErrTenantCustomDomainValidation      = errors.New("tenant custom domain request is invalid")
)

type TenantCustomDomainDNSView struct {
	RecordName  string     `json:"record_name"`
	RecordValue string     `json:"record_value"`
	State       string     `json:"state"`
	Attempts    int64      `json:"attempts"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	VerifiedAt  *time.Time `json:"verified_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type TenantCustomDomainRoutingDNSView struct {
	RecordType  string `json:"record_type"`
	RecordName  string `json:"record_name"`
	RecordValue string `json:"record_value,omitempty"`
	State       string `json:"state"`
	Note        string `json:"note"`
}

type TenantCustomDomainView struct {
	ID              string                           `json:"id"`
	WorkspaceID     string                           `json:"workspace_id"`
	ProjectID       string                           `json:"project_id"`
	ServiceID       string                           `json:"service_id"`
	EnvironmentID   string                           `json:"environment_id"`
	Kind            router.DomainKind                `json:"kind"`
	Hostname        string                           `json:"hostname"`
	Path            string                           `json:"path"`
	TargetPort      int                              `json:"target_port"`
	Public          bool                             `json:"public"`
	TLS             bool                             `json:"tls"`
	Version         int64                            `json:"version"`
	OwnershipState  string                           `json:"ownership_state"`
	RouteApplyState router.RouteApplyState           `json:"route_apply_state"`
	RouteReachable  bool                             `json:"route_reachable"`
	RoutingDNS      TenantCustomDomainRoutingDNSView `json:"routing_dns"`
	DNSChallenge    TenantCustomDomainDNSView        `json:"dns_challenge"`
	CreatedAt       time.Time                        `json:"created_at"`
	UpdatedAt       time.Time                        `json:"updated_at"`
}

type TenantCustomDomainCoordinator interface {
	ListCustomDomains(context.Context, TenantGeneratedDomainReadRequest) ([]TenantCustomDomainView, error)
	GetCustomDomain(context.Context, TenantGeneratedDomainReadRequest) (TenantCustomDomainView, error)
	CreateCustomDomain(context.Context, TenantCustomDomainCreateRequest) (TenantMutationResult, error)
	VerifyCustomDomain(context.Context, TenantCustomDomainVerifyRequest) (TenantMutationResult, error)
	DeleteCustomDomain(context.Context, TenantCustomDomainDeleteRequest) (TenantMutationResult, error)
}

type TenantCustomDomainCreateRequest struct {
	TenantGeneratedDomainMutationMeta
	Hostname string
	TLS      bool
}
type TenantCustomDomainVerifyRequest struct {
	TenantGeneratedDomainMutationMeta
	ExpectedVersion int64
}
type TenantCustomDomainDeleteRequest struct {
	TenantGeneratedDomainMutationMeta
	ExpectedVersion int64
}

type tenantCustomDomainCreateBody struct {
	Hostname string `json:"hostname"`
	TLS      bool   `json:"tls"`
}
type tenantCustomDomainVersionBody struct {
	ExpectedVersion *int64 `json:"expected_version"`
}

func tenantCustomDomainsHandler(coordinator TenantCustomDomainCoordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productPrincipal, classified := productPrincipalFromContext(r.Context())
		if !classified || productPrincipal.Kind != productPrincipalHuman {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "custom domains not found", nil)
			return
		}
		if coordinator == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant custom domains are unavailable", nil)
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
		read := TenantGeneratedDomainReadRequest{Principal: principal, Session: session, ServiceID: serviceID, EnvironmentID: environmentID, DomainID: domainID}
		if strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/verify") {
			var body tenantCustomDomainVersionBody
			raw, ok := readStrictCustomDomainBody(w, r, &body)
			if !ok {
				return
			}
			if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be at least 1", nil)
				return
			}
			key, ok := requireTenantCustomDomainIdempotencyKey(w, r)
			if !ok {
				return
			}
			result, err := coordinator.VerifyCustomDomain(r.Context(), TenantCustomDomainVerifyRequest{TenantGeneratedDomainMutationMeta: customDomainMutationMeta(r, read, key, raw), ExpectedVersion: *body.ExpectedVersion})
			if writeTenantCustomDomainError(w, r, err) {
				return
			}
			writeTenantCustomDomainMutationResult(w, result)
			return
		}
		if domainID == "" {
			switch r.Method {
			case http.MethodGet:
				values, err := coordinator.ListCustomDomains(r.Context(), read)
				if writeTenantCustomDomainError(w, r, err) {
					return
				}
				if values == nil {
					values = []TenantCustomDomainView{}
				}
				WriteJSON(w, http.StatusOK, values)
			case http.MethodPost:
				var body tenantCustomDomainCreateBody
				raw, ok := readStrictCustomDomainBody(w, r, &body)
				if !ok {
					return
				}
				key, ok := requireTenantCustomDomainIdempotencyKey(w, r)
				if !ok {
					return
				}
				result, err := coordinator.CreateCustomDomain(r.Context(), TenantCustomDomainCreateRequest{TenantGeneratedDomainMutationMeta: customDomainMutationMeta(r, read, key, raw), Hostname: body.Hostname, TLS: body.TLS})
				if writeTenantCustomDomainError(w, r, err) {
					return
				}
				writeTenantCustomDomainMutationResult(w, result)
			default:
				w.Header().Set("Allow", "GET, POST")
				writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			}
			return
		}
		switch r.Method {
		case http.MethodGet:
			value, err := coordinator.GetCustomDomain(r.Context(), read)
			if writeTenantCustomDomainError(w, r, err) {
				return
			}
			w.Header().Set("ETag", strongVersionETag(value.Version))
			WriteJSON(w, http.StatusOK, value)
		case http.MethodDelete:
			var body tenantCustomDomainVersionBody
			raw, ok := readStrictCustomDomainBody(w, r, &body)
			if !ok {
				return
			}
			if body.ExpectedVersion == nil || *body.ExpectedVersion < 1 {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "expected_version is required and must be at least 1", nil)
				return
			}
			if !requireMatchingExpectedVersion(w, r, *body.ExpectedVersion) {
				return
			}
			key, ok := requireTenantCustomDomainIdempotencyKey(w, r)
			if !ok {
				return
			}
			result, err := coordinator.DeleteCustomDomain(r.Context(), TenantCustomDomainDeleteRequest{TenantGeneratedDomainMutationMeta: customDomainMutationMeta(r, read, key, raw), ExpectedVersion: *body.ExpectedVersion})
			if writeTenantCustomDomainError(w, r, err) {
				return
			}
			writeTenantCustomDomainMutationResult(w, result)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func customDomainMutationMeta(r *http.Request, read TenantGeneratedDomainReadRequest, key idempotency.Key, raw []byte) TenantGeneratedDomainMutationMeta {
	return TenantGeneratedDomainMutationMeta{Principal: read.Principal, Session: read.Session, ServiceID: read.ServiceID, EnvironmentID: read.EnvironmentID, DomainID: read.DomainID, Method: r.Method, Key: key, RawBody: append([]byte(nil), raw...), RequestID: middleware.FromContext(r.Context()), SourceIP: r.RemoteAddr, UserAgent: r.UserAgent()}
}

func readStrictCustomDomainBody(w http.ResponseWriter, r *http.Request, target any) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, tenantCustomDomainBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(w, r, http.StatusRequestEntityTooLarge, failurecodes.ResourceValidationFailed, "custom domain request body exceeds the configured limit", nil)
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || rejectDuplicateTopLevelJSONFields(body) != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid custom domain request body", nil)
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid custom domain request body", nil)
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "custom domain request body must contain exactly one JSON object", nil)
		return nil, false
	}
	return body, true
}

func requireTenantCustomDomainIdempotencyKey(w http.ResponseWriter, r *http.Request) (idempotency.Key, bool) {
	key, failure := singletonIdempotencyKey(r.Header)
	if failure == "" {
		return key, true
	}
	writeRequestError(w, r, http.StatusBadRequest, failure, "exactly one Idempotency-Key header is required", nil)
	return "", false
}
func writeTenantCustomDomainMutationResult(w http.ResponseWriter, result TenantMutationResult) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

func writeTenantCustomDomainError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound), errors.Is(err, ErrTenantCustomDomainNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service, environment, or custom domain not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to access custom domains", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with a different request", nil)
	case errors.Is(err, ErrTenantCustomDomainVersionConflict):
		writeIfMatchFailed(w, r, "custom domain version changed", nil)
	case errors.Is(err, ErrTenantCustomDomainConflict):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "hostname unavailable", nil)
	case errors.Is(err, ErrTenantCustomDomainValidation):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid custom hostname", nil)
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
	case errors.Is(err, ErrTenantCustomDomainUnavailable):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant custom domains are unavailable", nil)
	default:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant custom domain operation unavailable", nil)
	}
	return true
}
