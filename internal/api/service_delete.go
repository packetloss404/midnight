// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// Deleting a service is deliberately a refusal-first operation.
//
// A service is the root of a small tree of live intent: encrypted variables,
// per-environment configuration, published HTTP routes and TCP listeners,
// persistent volumes, and a GitHub auto-deploy link. Removing the service row
// while any of that is still driving a container would leave running
// infrastructure with nothing in the control plane pointing at it, which is
// exactly the state a delete is supposed to end.
//
// So the contract is: **the runtime must already be stopped.** The delete
// never stops, drains, or removes a container itself. If the service still has
// running or in-flight deployment intent the request is refused with
// SERVICE_DELETE_RUNNING and the caller is told to stop the deployment first
// through the existing deployment-action route, which is the one path that
// owns runtime teardown. Once nothing is running, the delete removes the
// service and its dependent records in one transaction.
//
// Deployment history is kept. Deployments, their events, and the audit chain
// stay readable after the service is gone; they are evidence, not live intent.

// ServiceDeletionCounts reports what one service delete removed. Every field
// is a count of rows the delete is responsible for, so a caller can show the
// user what actually happened rather than a bare 204.
type ServiceDeletionCounts struct {
	Variables          int `json:"variables"`
	Volumes            int `json:"volumes"`
	PublishedDomains   int `json:"published_domains"`
	Routes             int `json:"routes"`
	TCPProxies         int `json:"tcp_proxies"`
	EnvironmentConfigs int `json:"environment_configs"`
	SourceLinks        int `json:"source_links"`
	PendingChanges     int `json:"pending_changes"`
	Monitors           int `json:"monitors"`
}

// ServiceDeletionReceipt is the response body of a successful delete.
type ServiceDeletionReceipt struct {
	ServiceID string    `json:"service_id"`
	ProjectID string    `json:"project_id"`
	Slug      string    `json:"slug"`
	DeletedAt time.Time `json:"deleted_at"`
	// Removed counts the live intent this delete tore down.
	Removed ServiceDeletionCounts `json:"removed"`
	// RetainedDeployments is the number of finished deployment records kept
	// as history. They are readable but no longer schedulable.
	RetainedDeployments int `json:"retained_deployments"`
}

// TenantServiceDeleteRequest carries a server-verified human identity tuple,
// the exact service route, the resource-version precondition, and the
// canonical replay fingerprint. Project and workspace lineage is always
// resolved again inside the transaction.
type TenantServiceDeleteRequest struct {
	Principal       identity.Principal
	Session         identity.Session
	ServiceID       string
	ExpectedVersion int64
	Key             idempotency.Key
	Fingerprint     idempotency.RequestFingerprint
	RequestID       string
	SourceIP        string
	UserAgent       string
}

// TenantServiceDeleteCoordinator is an independently admitted human seam for
// exactly one route: DELETE /services/{id}. Installing it cannot widen any
// other product mutation, and it can never start or stop a container.
type TenantServiceDeleteCoordinator interface {
	DeleteService(context.Context, TenantServiceDeleteRequest) (TenantMutationResult, error)
}

// errServiceDeleteUnsupportedStore is returned when the installed service
// store cannot apply a conditional delete. It is an honest capability outage,
// not a 404.
var errServiceDeleteUnsupportedStore = errors.New("conditional service deletion is unavailable")

// serviceDeleteHandler serves DELETE /api/v1alpha/services/{service_id}.
//
// A human session is routed to the atomic tenant coordinator, which commits
// authorization, replay, the removal, and audit evidence together. Any other
// principal takes the operator recovery path against the service store
// directly, mirroring how conditional environment deletion already works.
func serviceDeleteHandler(
	store ServiceStore,
	idem idempotency.Store,
	emitter *auditEmitter,
	coordinator TenantServiceDeleteCoordinator,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serviceID := strings.TrimSpace(r.PathValue("service_id"))
		if serviceID == "" {
			serviceID = strings.TrimSpace(strings.TrimPrefix(
				strings.TrimPrefix(r.URL.Path, v1alphaPrefix),
				resourceCollectionServices+"/",
			))
		}
		if serviceID == "" || strings.Contains(serviceID, "/") {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", nil)
			return
		}
		expectedVersion, ok := requireIfMatchVersion(w, r)
		if !ok {
			return
		}
		if expectedVersion < 1 {
			writeIfMatchFailed(w, r, "If-Match must identify a resource version of at least 1", nil)
			return
		}
		key := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if strings.TrimSpace(string(key)) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", nil)
			return
		}
		if err := idempotency.ValidateKey(key); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}

		if principal, classified := productPrincipalFromContext(r.Context()); classified && principal.Kind == productPrincipalHuman {
			deleteTenantService(w, r, coordinator, serviceID, expectedVersion, key)
			return
		}
		deleteServiceWithRecoveryStore(w, r, store, idem, emitter, serviceID, expectedVersion, key)
	}
}

// deleteTenantService is the human-session path. The coordinator owns every
// decision; this function only shapes the HTTP response.
func deleteTenantService(
	w http.ResponseWriter,
	r *http.Request,
	coordinator TenantServiceDeleteCoordinator,
	serviceID string,
	expectedVersion int64,
	key idempotency.Key,
) {
	if coordinator == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "service deletion is not configured on this server", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK || principal.UserID == "" || session.UserID != principal.UserID {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	result, err := coordinator.DeleteService(r.Context(), TenantServiceDeleteRequest{
		Principal:       principal,
		Session:         session,
		ServiceID:       serviceID,
		ExpectedVersion: expectedVersion,
		Key:             key,
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodDelete,
			Path:       serviceDeleteIdempotencyPath(serviceID),
			BodySHA256: serviceDeleteFingerprintBody(expectedVersion),
		},
		RequestID: middleware.FromContext(r.Context()),
		SourceIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrSessionInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
		return
	case errors.Is(err, identity.ErrAccessNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", nil)
		return
	case errors.Is(err, identity.ErrAuthorizationDenied):
		writeRequestError(w, r, http.StatusForbidden, failurecodes.AuthorizationDenied, "you do not have permission to delete services in this project", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrInvalidFingerprint):
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
		return
	case errors.Is(err, ErrResourceNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "service deletion is unavailable", nil)
		return
	}
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeStoredResponse(w, result.Response)
}

// deleteServiceWithRecoveryStore is the operator recovery path. It applies the
// same resource-version precondition and the same refusal rules, but it does
// not evaluate tenant membership because the caller already presented the
// installation recovery credential.
func deleteServiceWithRecoveryStore(
	w http.ResponseWriter,
	r *http.Request,
	store ServiceStore,
	idem idempotency.Store,
	emitter *auditEmitter,
	serviceID string,
	expectedVersion int64,
	key idempotency.Key,
) {
	conditional, supported := store.(ConditionalServiceStore)
	if !supported {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "conditional service deletion is unavailable", nil)
		return
	}
	fingerprint := idempotency.RequestFingerprint{
		Method:     http.MethodDelete,
		Path:       serviceDeleteIdempotencyPath(serviceID),
		BodySHA256: serviceDeleteFingerprintBody(expectedVersion),
	}
	reservation, err := idem.Reserve(r.Context(), key, fingerprint, 24*time.Hour)
	switch {
	case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
		return
	case errors.Is(err, idempotency.ErrKeyReused):
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with different request", nil)
		return
	case errors.Is(err, idempotency.ErrRequestInProgress):
		w.Header().Set("Retry-After", "1")
		writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "idempotency reservation failed", nil)
		return
	}
	if reservation.IsTerminal() && reservation.Response != nil {
		w.Header().Set("Idempotent-Replayed", "true")
		writeStoredResponse(w, reservation.Response.Clone())
		return
	}
	response := newIdempotencyResponseCapture(w)
	w = response
	defer response.Complete(r, idem, reservation.Reference())

	receipt, err := conditional.DeleteServiceCAS(r.Context(), orailtypes.ID(serviceID), expectedVersion)
	if err != nil {
		// A refusal is a decision, so it belongs on the audit chain next to
		// the deletions that succeeded. Faults are not decisions and are left
		// to the error response and the server log.
		if _, code, refusal := serviceDeleteRefusal(err); refusal {
			emitter.emit(r.Context(), audit.Row{
				ResourceKind:   "service",
				ResourceID:     serviceID,
				Action:         serviceDeleteAuditAction,
				ReasonCode:     string(code),
				PolicyDecision: "denied",
				Attrs: map[string]any{
					"capability":       string(identity.CapabilityServiceDelete),
					"expected_version": expectedVersion,
				},
			})
		}
		writeServiceDeleteStoreError(w, r, err, serviceID)
		return
	}
	emitter.emit(r.Context(), audit.Row{
		ResourceKind:   "service",
		ResourceID:     receipt.ServiceID,
		ProjectID:      receipt.ProjectID,
		Action:         serviceDeleteAuditAction,
		PolicyDecision: "allowed",
		Before: map[string]any{
			"slug":             receipt.Slug,
			"resource_version": expectedVersion,
		},
		After: serviceDeletionAuditAttrs(receipt),
	})
	WriteJSON(w, http.StatusOK, receipt)
}

// writeServiceDeleteStoreError maps a store-level delete failure onto the
// stable problem taxonomy. It reports true when a response was written.
func writeServiceDeleteStoreError(w http.ResponseWriter, r *http.Request, err error, serviceID string) bool {
	details := map[string]any{"service_id": serviceID}
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrResourceNotFound):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "service not found", details)
	case errors.Is(err, ErrResourcePreconditionFailed):
		writeIfMatchFailed(w, r, "service version changed", details)
	case errors.Is(err, ErrServiceDeleteRunning):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ServiceDeleteRunning, err.Error(), details)
	case errors.Is(err, ErrServiceDeleteBlocked):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ServiceDeleteBlocked, err.Error(), details)
	case errors.Is(err, errServiceDeleteUnsupportedStore):
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "conditional service deletion is unavailable", details)
	default:
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "service delete failed", details)
	}
	return true
}

// ErrServiceDeleteRunning reports that a service still has running or
// in-flight deployment intent, so deleting it now would strand containers or
// published routes.
var ErrServiceDeleteRunning = errors.New("service still has running or in-flight deployment intent")

// ErrServiceDeleteBlocked reports that a dependent record the delete refuses
// to discard on the caller's behalf still references the service.
var ErrServiceDeleteBlocked = errors.New("service delete is blocked by a dependent record")

// serviceDeleteAuditAction is the audit chain action for every attempt,
// allowed or denied.
const serviceDeleteAuditAction = "service.delete"

// serviceDeleteIdempotencyPath binds a caller key to the exact service. The
// identifier is not in a body (the request has none), so a route template
// would let one key replay across two services in the same project.
func serviceDeleteIdempotencyPath(serviceID string) string {
	return v1alphaPrefix + resourceCollectionServices + "/" + serviceID
}

// serviceDeleteFingerprintBody digests the only caller-supplied precondition,
// so reusing a key with a different If-Match is a conflict rather than a
// silent replay of the earlier delete.
func serviceDeleteFingerprintBody(expectedVersion int64) string {
	return idempotency.BodyHashSHA256([]byte(
		"service-delete\nexpected_version=" + strconv.FormatInt(expectedVersion, 10),
	))
}

// serviceDeletionAuditAttrs flattens a receipt for the audit `after` block.
func serviceDeletionAuditAttrs(receipt ServiceDeletionReceipt) map[string]any {
	return map[string]any{
		"deleted":              true,
		"slug":                 receipt.Slug,
		"variables":            receipt.Removed.Variables,
		"volumes":              receipt.Removed.Volumes,
		"published_domains":    receipt.Removed.PublishedDomains,
		"routes":               receipt.Removed.Routes,
		"tcp_proxies":          receipt.Removed.TCPProxies,
		"environment_configs":  receipt.Removed.EnvironmentConfigs,
		"source_links":         receipt.Removed.SourceLinks,
		"pending_changes":      receipt.Removed.PendingChanges,
		"monitors":             receipt.Removed.Monitors,
		"retained_deployments": receipt.RetainedDeployments,
	}
}
