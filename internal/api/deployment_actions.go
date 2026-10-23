// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

// deploymentActionHandler exposes the durable action intake over REST. Image
// replay actions create a new deployment from an immutable image/config
// snapshot; runtime actions mutate only the selected deployment's runtime
// intent. The service/deployment relationship is derived from durable state,
// never trusted from the request body.
func deploymentActionHandler(
	actions DeploymentActionIntake,
	deployments DeploymentStore,
	tenantCoordinators ...TenantDeploymentActionCoordinator,
) http.HandlerFunc {
	var tenantCoordinator TenantDeploymentActionCoordinator
	if len(tenantCoordinators) > 0 {
		tenantCoordinator = tenantCoordinators[0]
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if principal, ok := productPrincipalFromContext(r.Context()); ok &&
			principal.Kind == productPrincipalHuman {
			createTenantDeploymentAction(w, r, tenantCoordinator)
			return
		}
		if actions == nil || deployments == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable deployment actions are not configured", nil)
			return
		}

		serviceID := strings.TrimSpace(r.PathValue("service_id"))
		deploymentID := strings.TrimSpace(r.PathValue("deployment_id"))
		actionName := strings.TrimSpace(r.PathValue("action"))
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if serviceID == "" || deploymentID == "" || actionName == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "service, deployment, and action are required", nil)
			return
		}
		if idempotencyKey == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", nil)
			return
		}

		records, err := deployments.ListDeploymentsByService(r.Context(), serviceID)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment lookup failed", nil)
			return
		}
		found := false
		for _, record := range records {
			if record.ID == deploymentID {
				found = true
				break
			}
		}
		if !found {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment not found", map[string]any{
				"service_id": serviceID, "deployment_id": deploymentID,
			})
			return
		}

		triggerKey := "rest:deployment-action:" + actionName + ":" + serviceID + ":" + deploymentID + ":" + idempotencyKey
		var result DeploymentActionResult
		switch actionName {
		case "redeploy":
			result, err = actions.CommitImageReplay(r.Context(), ImageReplayRequest{
				ServiceID: serviceID, SourceID: deploymentID,
				Action: deploy.ActionRedeployImage, TriggerKey: triggerKey,
			})
		case "rollback":
			result, err = actions.CommitImageReplay(r.Context(), ImageReplayRequest{
				ServiceID: serviceID, SourceID: deploymentID,
				Action: deploy.ActionRollbackDigest, TriggerKey: triggerKey,
			})
		case "restart", "stop", "remove":
			result, err = actions.CommitRuntimeAction(r.Context(), RuntimeActionRequest{
				ServiceID: serviceID, TargetID: deploymentID,
				Action: deploy.RuntimeAction(actionName), TriggerKey: triggerKey,
			})
		default:
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "unsupported deployment action", map[string]any{
				"action":  actionName,
				"allowed": []string{"redeploy", "rollback", "restart", "stop", "remove"},
			})
			return
		}
		if err != nil {
			writeDeploymentActionRESTError(w, r, err)
			return
		}
		if !result.Created {
			w.Header().Set("Idempotent-Replay", "true")
		}
		WriteJSON(w, http.StatusAccepted, result)
	}
}

func createTenantDeploymentAction(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantDeploymentActionCoordinator,
) {
	if coordinator == nil {
		writeRequestError(
			w,
			r,
			http.StatusServiceUnavailable,
			failurecodes.InternalError,
			"tenant deployment actions are unavailable",
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

	serviceID := strings.TrimSpace(r.PathValue("service_id"))
	deploymentID := strings.TrimSpace(r.PathValue("deployment_id"))
	actionName := strings.TrimSpace(r.PathValue("action"))
	if serviceID == "" || deploymentID == "" || !validTenantDeploymentAction(actionName) {
		writeRequestError(
			w,
			r,
			http.StatusBadRequest,
			failurecodes.ResourceValidationFailed,
			"service, deployment, and a supported action are required",
			map[string]any{
				"action":  actionName,
				"allowed": []string{"redeploy", "rollback", "restart", "stop", "remove"},
			},
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
			"deployment action requests must have an empty body",
			nil,
		)
		return
	}

	result, err := coordinator.CommitTenantDeploymentAction(
		r.Context(),
		TenantDeploymentActionRequest{
			Principal:    principal,
			Session:      session,
			ServiceID:    serviceID,
			DeploymentID: deploymentID,
			Action:       actionName,
			Key:          key,
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       tenantDeploymentActionIdempotencyPath(serviceID, deploymentID, actionName),
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
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
	case errors.Is(err, identity.ErrAccessNotFound),
		errors.Is(err, ErrDeploymentNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment not found", nil)
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to act on this deployment", nil)
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
	case errors.Is(err, ErrDeploymentActionConflict),
		errors.Is(err, ErrDeploymentSnapshotAbsent):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), nil)
	case errors.Is(err, ErrResourceInvalid),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "invalid tenant deployment action", nil)
	case errors.Is(err, ErrTenantDeploymentActionUnavailable),
		errors.Is(err, context.DeadlineExceeded):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "tenant deployment action dependency is unavailable", nil)
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant deployment action failed", nil)
	default:
		if result.Response.StatusCode < 100 {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "tenant deployment action failed", nil)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func writeDeploymentActionRESTError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrDeploymentNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "deployment not found", nil)
	case errors.Is(err, ErrDeploymentActionConflict), errors.Is(err, ErrDeploymentSnapshotAbsent):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, err.Error(), nil)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "deployment action intake failed", nil)
	}
}
