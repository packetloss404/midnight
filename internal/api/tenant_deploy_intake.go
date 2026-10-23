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

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const tenantImageDeployBodyLimitBytes int64 = 16 << 10

// ErrTenantDeployIntakeUnavailable distinguishes a failed backing authority
// from an unexpected coordinator defect. Handlers return the former as a
// retryable dependency outage and keep the latter as a non-leaking 500.
var ErrTenantDeployIntakeUnavailable = errors.New("tenant deploy intake dependency is unavailable")

// ErrTenantGeneratedRouteUnavailable means the request is valid but this
// server cannot derive an owned hostname because MIDNIGHT_APPS_DOMAIN is not
// configured. It is distinct from malformed client routing input.
var ErrTenantGeneratedRouteUnavailable = errors.New("tenant generated public route is unavailable")

// tenantImageDeployBody is deliberately not an alias of DeployIntakeRequest:
// human sessions may submit only one environment, one digest-pinned image, and
// an optional generated-route opt-in. Source, custom route properties, mode,
// and server-owned identities are rejected before coordinator admission.
type tenantImageDeployBody struct {
	EnvironmentID string `json:"environment_id"`
	Image         string `json:"image"`
	Route         *struct {
		Public bool `json:"public"`
	} `json:"route,omitempty"`
}

func createTenantImageDeploy(
	w http.ResponseWriter,
	r *http.Request,
	serviceID orailtypes.ID,
	coordinator TenantDeployIntakeCoordinator,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant image deploy is unavailable", nil)
		return
	}
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
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	}

	keyValues := r.Header.Values("Idempotency-Key")
	if len(keyValues) != 1 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "exactly one Idempotency-Key header is required", nil)
		return
	}
	key := idempotency.Key(keyValues[0])
	// Idempotency-Key is a singleton request header. Some proxies coalesce
	// duplicate fields into one comma-delimited value, so reject commas here
	// rather than accidentally treating two client keys as a third key.
	if strings.Contains(string(key), ",") {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	}
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	}

	body, ok := readTenantImageDeployBody(w, r)
	if !ok {
		return
	}
	var request tenantImageDeployBody
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid tenant image deploy request body", map[string]any{"reason": "json"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "tenant image deploy request body must contain exactly one JSON document", map[string]any{"reason": "trailing_json"})
		return
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawFields); err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid tenant image deploy request body", map[string]any{"reason": "json"})
		return
	}
	if rawRoute, present := rawFields["route"]; present &&
		bytes.Equal(bytes.TrimSpace(rawRoute), []byte("null")) {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			"route must be omitted for a private deployment",
			map[string]any{"field": "route"},
		)
		return
	}

	intent := DeployIntakeRequest{
		EnvironmentID: request.EnvironmentID,
		Image:         request.Image,
	}
	if request.Route != nil {
		if !request.Route.Public {
			writeRequestError(
				w,
				r,
				http.StatusBadRequest,
				failurecodes.ResourceValidationFailed,
				"route.public must be true when route is present; omit route for a private deployment",
				map[string]any{"field": "route.public"},
			)
			return
		}
		intent.Route = &DeployRouteIntent{Public: true}
	}
	if code, message, details, valid := validateDeployIntakeRequest(intent); !valid {
		writeRequestError(w, r, http.StatusBadRequest, code, message, details)
		return
	}

	result, err := coordinator.DeployImmutableImage(r.Context(), TenantImageDeployRequest{
		Principal: principal,
		Session:   session,
		ServiceID: serviceID,
		Intent: DeployIntakeRequest{
			EnvironmentID: strings.TrimSpace(request.EnvironmentID),
			Image:         strings.TrimSpace(request.Image),
			Route:         intent.Route,
		},
		Key: key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       tenantDeployIdempotencyPath(serviceID),
			BodySHA256: idempotency.BodyHashSHA256(body),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment target not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to deploy to this target", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, ErrResourceInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid tenant image deploy request", nil)
	case errors.Is(err, ErrTenantDeployIntakeUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant image deploy dependency is unavailable", nil)
	case errors.Is(err, ErrTenantGeneratedRouteUnavailable):
		writeRequestError(
			w,
			r,
			http.StatusServiceUnavailable,
			failurecodes.CapabilityUnavailable,
			"generated public routes require MIDNIGHT_APPS_DOMAIN to be configured",
			map[string]any{"setting": "MIDNIGHT_APPS_DOMAIN"}, // not-a-failure-code
		)
	case errors.Is(err, ErrTenantGeneratedDomainRetired):
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.ResourceConflict,
			"generated public route identity conflicts with retired product state",
			nil,
		)
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant image deploy failed", nil)
	default:
		if result.Response.StatusCode < 100 {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant image deploy failed", nil)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func readTenantImageDeployBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	limited := http.MaxBytesReader(w, r.Body, tenantImageDeployBodyLimitBytes)
	defer limited.Close()
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeRequestError(
				w,
				r,
				http.StatusRequestEntityTooLarge,
				failurecodes.ResourceValidationFailed,
				"tenant image deploy request body exceeds the configured limit",
				map[string]any{"limit": tenantImageDeployBodyLimitBytes},
			)
			return nil, false
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "request body read failed", nil)
		return nil, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "tenant image deploy request body is required", nil)
		return nil, false
	}
	return body, true
}
