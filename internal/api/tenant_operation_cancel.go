// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
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

func cancelTenantOperation(
	w http.ResponseWriter,
	r *http.Request,
	operationID orailtypes.ID,
	coordinator TenantOperationCancelCoordinator,
) {
	if coordinator == nil {
		writeRequestError(
			w,
			r,
			http.StatusServiceUnavailable,
			failurecodes.InternalError,
			"tenant operation cancellation is unavailable",
			nil,
		)
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
		writeRequestError(
			w,
			r,
			http.StatusUnauthorized,
			failurecodes.AuthSessionInvalid,
			"session is invalid, expired, or revoked",
			nil,
		)
		return
	}
	if strings.TrimSpace(string(operationID)) == "" {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			"operation id is required",
			nil,
		)
		return
	}
	keyValues := r.Header.Values("Idempotency-Key")
	if len(keyValues) != 1 {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.IdempotencyKeyRequired,
			"exactly one Idempotency-Key header is required",
			nil,
		)
		return
	}
	key := idempotency.Key(keyValues[0])
	if strings.Contains(string(key), ",") {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.IdempotencyKeyInvalid,
			"invalid idempotency key",
			nil,
		)
		return
	}
	if err := idempotency.ValidateKey(key); err != nil {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.IdempotencyKeyInvalid,
			"invalid idempotency key",
			nil,
		)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			"operation cancellation requests must have an empty body",
			nil,
		)
		return
	}

	result, err := coordinator.CancelTenantOperation(
		r.Context(),
		TenantOperationCancelRequest{
			Principal:   principal,
			Session:     session,
			OperationID: operationID,
			Key:         key,
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       tenantOperationCancelIdempotencyPath(operationID),
				BodySHA256: idempotency.BodyHashSHA256(nil),
			},
			RequestID: middleware.FromContext(r.Context()),
			SourceIP:  r.RemoteAddr,
			UserAgent: r.UserAgent(),
		},
	)
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(
			w,
			r,
			http.StatusUnauthorized,
			failurecodes.AuthSessionInvalid,
			"session is invalid, expired, or revoked",
			nil,
		)
	case errors.Is(err, identity.ErrAccessNotFound),
		errors.Is(err, ErrOperationNotFound):
		writeRequestError(
			w,
			r,
			http.StatusNotFound,
			failurecodes.OperationNotFound,
			"operation not found",
			map[string]any{"operation_id": operationID},
		)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(
			w,
			r,
			http.StatusForbidden,
			failurecodes.AuthorizationDenied,
			"you do not have permission to cancel this operation",
			nil,
		)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.IdempotencyRequestInProgress,
			"idempotency request in progress",
			nil,
		)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.IdempotencyKeyReusedDifferentBody,
			"idempotency key reused with different body",
			nil,
		)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.IdempotencyKeyReused,
			"idempotency key reused with different request",
			nil,
		)
	case errors.Is(err, ErrOperationInvalidTransition):
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.OperationInvalidTransition,
			"operation cannot be canceled from its current state",
			map[string]any{"operation_id": operationID},
		)
	case errors.Is(err, ErrTenantOperationCancelUnsafe):
		writeRequestError(
			w,
			r,
			http.StatusConflict,
			failurecodes.DeployCancelUnsafe,
			"operation has no cancelable durable work or observable worker safe point",
			map[string]any{"operation_id": operationID},
		)
	case errors.Is(err, ErrResourceInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			"invalid tenant operation cancellation",
			nil,
		)
	case errors.Is(err, ErrTenantOperationCancelUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(
			w,
			r,
			http.StatusServiceUnavailable,
			failurecodes.InternalError,
			"tenant operation cancellation dependency is unavailable",
			nil,
		)
	case err != nil:
		writeRequestError(
			w,
			r,
			http.StatusInternalServerError,
			failurecodes.InternalError,
			"tenant operation cancellation failed",
			nil,
		)
	default:
		if result.Response.StatusCode < 100 {
			writeRequestError(
				w,
				r,
				http.StatusInternalServerError,
				failurecodes.InternalError,
				"tenant operation cancellation failed",
				nil,
			)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}
