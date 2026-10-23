// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
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
	"github.com/midnight/midnight/internal/webhook"
)

const (
	defaultWebhookDeliveryPageLimit = 50
	maxWebhookDeliveryPageLimit     = 200
)

// webhookDeliveryRecoveryStore is deliberately narrower than
// webhook.DurableInbox. The operator API can inspect and redrive durable
// metadata, but it cannot claim leases or read retained payload bytes.
type webhookDeliveryRecoveryStore interface {
	ListDeliveries(context.Context, webhook.DeliveryFilter) ([]webhook.DeliverySummary, error)
	GetDelivery(context.Context, string) (webhook.DeliveryDetail, error)
}

type webhookDeliveryPage struct {
	Items []webhook.DeliverySummary `json:"items"`
}

type webhookProjectDeliveryDetail struct {
	webhook.DeliverySummary
	Targets          []webhook.TargetSummary `json:"targets"`
	ExclusiveProject bool                    `json:"exclusive_project"`
}

func webhookDeliveryCollectionHandler(store webhookDeliveryRecoveryStore, accessStores ...identity.AccessStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeWebhookInboxNotConfigured(w, r)
			return
		}
		filter, ok := parseWebhookDeliveryFilter(w, r)
		if !ok {
			return
		}
		if _, ok := authorizeWebhookProjectScope(w, r, firstAccessStore(accessStores), filter.ProjectID, identity.CapabilityProjectRead); !ok {
			return
		}
		deliveries, err := store.ListDeliveries(r.Context(), filter)
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "webhook delivery list failed", nil)
			return
		}
		if deliveries == nil {
			deliveries = []webhook.DeliverySummary{}
		}
		WriteJSON(w, http.StatusOK, webhookDeliveryPage{Items: deliveries})
	}
}

func webhookDeliveryResourceHandler(store webhookDeliveryRecoveryStore, accessStores ...identity.AccessStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeWebhookInboxNotConfigured(w, r)
			return
		}
		projectID, ok := requireWebhookProjectQuery(w, r)
		if !ok {
			return
		}
		if _, ok := authorizeWebhookProjectScope(w, r, firstAccessStore(accessStores), projectID, identity.CapabilityProjectRead); !ok {
			return
		}
		id := strings.TrimSpace(r.PathValue("delivery_id"))
		if id == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		delivery, err := store.GetDelivery(r.Context(), id)
		if errors.Is(err, webhook.ErrInboxDeliveryNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "webhook delivery lookup failed", nil)
			return
		}
		projection, ok := projectWebhookDeliveryProjection(delivery, projectID)
		if !ok {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		WriteJSON(w, http.StatusOK, projection)
	}
}

func webhookDeliveryRedriveRoute(
	coordinator WebhookDeliveryRedriveCoordinator,
	store webhookDeliveryRecoveryStore,
	access identity.AccessStore,
) http.HandlerFunc {
	handler := webhookDeliveryRedriveHandler(coordinator, store, access)
	// An unwired memory stack must report the absent durable capability even
	// when a caller did not supply a mutation header for a capability that
	// does not exist.
	if coordinator == nil {
		return handler
	}
	return mutation("webhooks:write", handler)
}

func webhookDeliveryRedriveHandler(
	coordinator WebhookDeliveryRedriveCoordinator,
	store webhookDeliveryRecoveryStore,
	access identity.AccessStore,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if coordinator == nil {
			writeWebhookInboxNotConfigured(w, r)
			return
		}
		projectID, ok := requireWebhookProjectQuery(w, r)
		if !ok {
			return
		}
		workspaceID, ok := authorizeWebhookProjectScope(w, r, access, projectID, identity.CapabilityDeploymentTriggerGit)
		if !ok {
			return
		}
		if store == nil {
			writeWebhookInboxNotConfigured(w, r)
			return
		}
		body, ok := readEmptyWebhookRedriveBody(w, r)
		if !ok {
			return
		}
		key := idempotency.Key(r.Header.Get("Idempotency-Key"))
		if key == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyRequired, "Idempotency-Key header is required", nil)
			return
		}
		if err := idempotency.ValidateKey(key); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.IdempotencyKeyInvalid, "invalid idempotency key", nil)
			return
		}
		id := strings.TrimSpace(r.PathValue("delivery_id"))
		if id == "" {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		delivery, err := store.GetDelivery(r.Context(), id)
		if errors.Is(err, webhook.ErrInboxDeliveryNotFound) {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		if err != nil {
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "webhook delivery lookup failed", nil)
			return
		}
		if _, visible := projectWebhookDeliveryProjection(delivery, projectID); !visible {
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		}
		principal, _ := identityPrincipalFromContext(r.Context())
		result, err := coordinator.RedriveWebhookDelivery(
			r.Context(),
			WebhookDeliveryRedriveRequest{
				DeliveryID:  id,
				ProjectID:   projectID,
				WorkspaceID: workspaceID,
				Key:         key,
				Actor:       idempotency.ActorFromContext(r.Context()),
				Tenant:      webhookRedriveTenant(workspaceID, projectID),
				Principal:   principal,
				Fingerprint: idempotencyRequestFingerprint(
					r,
					idempotency.BodyHashSHA256(append([]byte(projectID+"\x00"), body...)),
				),
				RequestID: middleware.FromContext(r.Context()),
				SourceIP:  r.RemoteAddr,
				UserAgent: r.UserAgent(),
			},
		)
		switch {
		case errors.Is(err, idempotency.ErrKeyReusedDifferentBody):
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReusedDifferentBody, "idempotency key reused with different body", nil)
			return
		case errors.Is(err, idempotency.ErrKeyReused):
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyKeyReused, "idempotency key reused with a different request", nil)
			return
		case errors.Is(err, idempotency.ErrRequestInProgress):
			w.Header().Set("Retry-After", "1")
			writeRequestError(w, r, http.StatusConflict, failurecodes.IdempotencyRequestInProgress, "idempotency request in progress", nil)
			return
		case errors.Is(err, webhook.ErrInboxDeliveryNotFound):
			writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "webhook delivery not found", nil)
			return
		case errors.Is(err, webhook.ErrInboxDeliveryTerminal):
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "only dead-letter webhook deliveries can be redriven", nil)
			return
		case errors.Is(err, webhook.ErrInboxPayloadCorrupt):
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "retained webhook payload is unavailable or failed its integrity check", nil)
			return
		case errors.Is(err, webhook.ErrInboxDeliveryScopeMismatch):
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "webhook delivery targets are not exclusive to this project", nil)
			return
		case err != nil:
			writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "webhook delivery redrive failed", nil)
			return
		}
		if result.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeStoredResponse(w, result.Response)
	}
}

func parseWebhookDeliveryFilter(
	w http.ResponseWriter,
	r *http.Request,
) (webhook.DeliveryFilter, bool) {
	values := r.URL.Query()
	for key, entries := range values {
		switch key {
		case "provider", "state", "limit", "project_id":
		default:
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "unsupported webhook delivery filter", map[string]any{"field": key})
			return webhook.DeliveryFilter{}, false
		}
		if len(entries) != 1 || strings.TrimSpace(entries[0]) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, key+" must be provided exactly once and must not be empty", map[string]any{"field": key})
			return webhook.DeliveryFilter{}, false
		}
	}

	projectID := strings.TrimSpace(values.Get("project_id"))
	if projectID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return webhook.DeliveryFilter{}, false
	}
	filter := webhook.DeliveryFilter{
		Limit:     defaultWebhookDeliveryPageLimit,
		ProjectID: projectID,
	}
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxWebhookDeliveryPageLimit {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "limit must be an integer between 1 and 200", map[string]any{"field": "limit"})
			return webhook.DeliveryFilter{}, false
		}
		filter.Limit = limit
	}

	if raw := strings.TrimSpace(values.Get("provider")); raw != "" {
		filter.Provider = webhook.Provider(raw)
		switch filter.Provider {
		case webhook.ProviderGitHub,
			webhook.ProviderGitLab,
			webhook.ProviderBitbucket,
			webhook.ProviderMidnightIncoming:
		default:
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "provider is unsupported", map[string]any{"field": "provider"})
			return webhook.DeliveryFilter{}, false
		}
	}

	if raw := strings.TrimSpace(values.Get("state")); raw != "" {
		filter.State = webhook.InboxState(raw)
		switch filter.State {
		case webhook.InboxPending,
			webhook.InboxProcessing,
			webhook.InboxRetryWait,
			webhook.InboxSucceeded,
			webhook.InboxIgnored,
			webhook.InboxDeadLetter:
		default:
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "state is unsupported", map[string]any{"field": "state"})
			return webhook.DeliveryFilter{}, false
		}
	}
	return filter, true
}

func requireWebhookProjectQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.URL.Query()
	for key, entries := range values {
		if key != "project_id" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "unsupported webhook delivery filter", map[string]any{"field": key})
			return "", false
		}
		if len(entries) != 1 || strings.TrimSpace(entries[0]) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id must be provided exactly once and must not be empty", map[string]any{"field": "project_id"})
			return "", false
		}
	}
	projectID := strings.TrimSpace(values.Get("project_id"))
	if projectID == "" {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", map[string]any{"field": "project_id"})
		return "", false
	}
	return projectID, true
}

func firstAccessStore(stores []identity.AccessStore) identity.AccessStore {
	if len(stores) == 0 {
		return nil
	}
	return stores[0]
}

func authorizeWebhookProjectScope(
	w http.ResponseWriter,
	r *http.Request,
	store identity.AccessStore,
	projectID string,
	capability identity.Capability,
) (string, bool) {
	principal, human := identityPrincipalFromContext(r.Context())
	if !human {
		return "", true
	}
	if store == nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "project authorization is unavailable", nil)
		return "", false
	}
	grant, _, err := identity.Authorize(
		r.Context(),
		store,
		principal.UserID,
		identity.AccessTarget{ProjectID: projectID},
		capability,
		false,
	)
	if err != nil {
		writeIdentityAuthorizationError(w, r, err)
		return "", false
	}
	return grant.Target.WorkspaceID, true
}

func projectWebhookDeliveryProjection(
	delivery webhook.DeliveryDetail,
	projectID string,
) (webhookProjectDeliveryDetail, bool) {
	targets := make([]webhook.TargetSummary, 0, len(delivery.Targets))
	for _, target := range delivery.Targets {
		if target.ProjectID == projectID {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return webhookProjectDeliveryDetail{}, false
	}
	return webhookProjectDeliveryDetail{
		DeliverySummary:  delivery.DeliverySummary,
		Targets:          targets,
		ExclusiveProject: len(targets) == len(delivery.Targets),
	}, true
}

func webhookRedriveTenant(workspaceID, projectID string) idempotency.Tenant {
	if strings.TrimSpace(workspaceID) == "" {
		return idempotency.TenantInstallation
	}
	return projectScopedIdempotencyTenant(workspaceID, projectID)
}

func readEmptyWebhookRedriveBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.ContentLength > 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "webhook delivery redrive request body must be empty", nil)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "webhook delivery redrive request body could not be read", nil)
		return nil, false
	}
	if len(body) != 0 {
		writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "webhook delivery redrive request body must be empty", nil)
		return nil, false
	}
	return body, true
}

func webhookDeliveryStoredResponse(status int, delivery webhook.DeliveryDetail, projectID string) (idempotency.StoredResponse, error) {
	projection, ok := projectWebhookDeliveryProjection(delivery, projectID)
	if !ok || !projection.ExclusiveProject {
		return idempotency.StoredResponse{}, webhook.ErrInboxDeliveryScopeMismatch
	}
	body, err := json.Marshal(projection)
	if err != nil {
		return idempotency.StoredResponse{}, err
	}
	return idempotency.StoredResponse{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Location":     []string{v1alphaPrefix + "/webhook-deliveries/" + delivery.ID + "?project_id=" + projectID},
		},
		Body: append(body, '\n'),
	}, nil
}

func writeWebhookInboxNotConfigured(w http.ResponseWriter, r *http.Request) {
	writeRequestError(
		w,
		r,
		http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable,
		"durable webhook delivery history requires PostgreSQL storage",
		map[string]any{"capability": "durable_webhook_inbox"},
	)
}
