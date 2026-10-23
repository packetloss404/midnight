// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/monitors"
	"github.com/midnight/midnight/internal/observability"
)

type MonitorStore interface {
	CreateDefinition(context.Context, monitors.Definition) (monitors.Definition, error)
	ListDefinitions(context.Context, string) ([]monitors.Definition, error)
	DeleteDefinition(context.Context, string, string) error
	CreateChannel(context.Context, monitors.Channel) (monitors.Channel, error)
	ListChannels(context.Context, string) ([]monitors.Channel, error)
	Subscribe(context.Context, string, string, string) error
	ListIncidents(context.Context, monitors.IncidentQuery) ([]monitors.IncidentStatus, error)
	ListDeliveryStatuses(context.Context, monitors.DeliveryStatusQuery) ([]monitors.DeliveryStatus, error)
	RetryDeadDelivery(context.Context, string, string, time.Time) (monitors.DeliveryStatus, error)
}

type monitorRequest struct {
	ProjectID             string  `json:"project_id"`
	EnvironmentID         string  `json:"environment_id"`
	ServiceID             string  `json:"service_id"`
	Name                  string  `json:"name"`
	Signal                string  `json:"signal"`
	Operator              string  `json:"operator"`
	Threshold             float64 `json:"threshold"`
	WindowSeconds         int     `json:"window_seconds"`
	EvaluationSeconds     int     `json:"evaluation_interval_seconds"`
	ConsecutiveFailures   int     `json:"consecutive_failures"`
	ConsecutiveRecoveries int     `json:"consecutive_recoveries"`
}

type monitorView struct {
	monitors.Definition
	WindowSeconds     int `json:"window_seconds"`
	EvaluationSeconds int `json:"evaluation_interval_seconds"`
}

type monitorChannelRequest struct {
	ProjectID          string `json:"project_id"`
	DisplayName        string `json:"display_name"`
	EndpointVariableID string `json:"endpoint_variable_id"`
	SigningVariableID  string `json:"signing_variable_id"`
	SigningKeyID       string `json:"signing_key_id,omitempty"`
}

func monitorsHandler(store MonitorStore, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "built-in monitors are not configured", nil)
			return
		}
		monitorID := strings.TrimSpace(r.PathValue("monitor_id"))
		switch {
		case monitorID == "" && r.Method == http.MethodGet:
			projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
			if projectID == "" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", nil)
				return
			}
			definitions, err := store.ListDefinitions(r.Context(), projectID)
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "monitor list failed", nil)
				return
			}
			views := make([]monitorView, 0, len(definitions))
			for _, definition := range definitions {
				views = append(views, monitorDefinitionView(definition))
			}
			WriteJSON(w, http.StatusOK, views)
		case monitorID == "" && r.Method == http.MethodPost:
			body, ok := readBoundedJSONRequestBody(w, r, "invalid monitor request")
			if !ok {
				return
			}
			reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
			if !ok {
				return
			}
			idempotentResponse := newIdempotencyResponseCapture(w)
			w = idempotentResponse
			defer idempotentResponse.Complete(r, idem, reservation.Reference())
			var request monitorRequest
			if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid monitor request") {
				return
			}
			definition, err := store.CreateDefinition(r.Context(), monitors.Definition{
				Name: request.Name,
				Scope: observability.Scope{
					ProjectID: request.ProjectID, EnvironmentID: request.EnvironmentID,
					ServiceID: request.ServiceID,
				},
				Signal: request.Signal, Operator: request.Operator, Threshold: request.Threshold,
				Window:                time.Duration(request.WindowSeconds) * time.Second,
				EvaluationInterval:    time.Duration(request.EvaluationSeconds) * time.Second,
				ConsecutiveFailures:   request.ConsecutiveFailures,
				ConsecutiveRecoveries: request.ConsecutiveRecoveries,
				Enabled:               true,
			})
			if err != nil {
				writeMonitorStoreError(w, r, "create", err)
				return
			}
			WriteJSON(w, http.StatusCreated, monitorDefinitionView(definition))
		case monitorID != "" && r.Method == http.MethodDelete:
			projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
			if projectID == "" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", nil)
				return
			}
			reservation, ok := reserveIdempotencyOrAbort(w, r, idem, nil)
			if !ok {
				return
			}
			idempotentResponse := newIdempotencyResponseCapture(w)
			w = idempotentResponse
			defer idempotentResponse.Complete(r, idem, reservation.Reference())
			if err := store.DeleteDefinition(r.Context(), projectID, monitorID); err != nil {
				writeMonitorStoreError(w, r, "delete", err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func monitorChannelsHandler(store MonitorStore, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "monitor channels are not configured", nil)
			return
		}
		switch r.Method {
		case http.MethodGet:
			projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
			if projectID == "" {
				writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", nil)
				return
			}
			channels, err := store.ListChannels(r.Context(), projectID)
			if err != nil {
				writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "monitor channel list failed", nil)
				return
			}
			WriteJSON(w, http.StatusOK, channels)
		case http.MethodPost:
			body, ok := readBoundedJSONRequestBody(w, r, "invalid monitor channel request")
			if !ok {
				return
			}
			reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
			if !ok {
				return
			}
			idempotentResponse := newIdempotencyResponseCapture(w)
			w = idempotentResponse
			defer idempotentResponse.Complete(r, idem, reservation.Reference())
			var request monitorChannelRequest
			if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid monitor channel request") {
				return
			}
			channel, err := store.CreateChannel(r.Context(), monitors.Channel{
				ProjectID: request.ProjectID, DisplayName: request.DisplayName, Type: "webhook",
				EndpointVariableID: request.EndpointVariableID,
				SigningVariableID:  request.SigningVariableID, SigningKeyID: request.SigningKeyID,
			})
			if err != nil {
				writeMonitorStoreError(w, r, "create channel", err)
				return
			}
			WriteJSON(w, http.StatusCreated, channel)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}
}

func monitorSubscriptionHandler(store MonitorStore, idem idempotency.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "monitor subscriptions are not configured", nil)
			return
		}
		body, ok := readBoundedJSONRequestBody(w, r, "invalid monitor subscription request")
		if !ok {
			return
		}
		reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
		if !ok {
			return
		}
		idempotentResponse := newIdempotencyResponseCapture(w)
		w = idempotentResponse
		defer idempotentResponse.Complete(r, idem, reservation.Reference())
		var request struct {
			ProjectID string `json:"project_id"`
			ChannelID string `json:"channel_id"`
		}
		if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid monitor subscription request") {
			return
		}
		if err := store.Subscribe(r.Context(), request.ProjectID, r.PathValue("monitor_id"), request.ChannelID); err != nil {
			writeMonitorStoreError(w, r, "subscribe", err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]string{
			"monitor_id": r.PathValue("monitor_id"), "channel_id": request.ChannelID,
		})
	}
}

func monitorIncidentsHandler(store MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "monitor incident history is not configured", nil)
			return
		}
		limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "limit must be between 1 and 200", nil)
			return
		}
		query := monitors.IncidentQuery{
			ProjectID:     strings.TrimSpace(r.URL.Query().Get("project_id")),
			EnvironmentID: strings.TrimSpace(r.URL.Query().Get("environment_id")),
			ServiceID:     strings.TrimSpace(r.URL.Query().Get("service_id")),
			MonitorID:     strings.TrimSpace(r.URL.Query().Get("monitor_id")),
			State:         strings.TrimSpace(r.URL.Query().Get("state")),
			Limit:         limit,
		}
		if err := query.Validate(); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		incidents, err := store.ListIncidents(r.Context(), query)
		if err != nil {
			writeMonitorStoreError(w, r, "list incidents", err)
			return
		}
		WriteJSON(w, http.StatusOK, incidents)
	}
}

func monitorDeliveriesHandler(store MonitorStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "monitor delivery history is not configured", nil)
			return
		}
		limit, err := parseStrictPageLimit(r.URL.Query().Get("limit"))
		if err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "limit must be between 1 and 200", nil)
			return
		}
		query := monitors.DeliveryStatusQuery{
			ProjectID:     strings.TrimSpace(r.URL.Query().Get("project_id")),
			EnvironmentID: strings.TrimSpace(r.URL.Query().Get("environment_id")),
			ServiceID:     strings.TrimSpace(r.URL.Query().Get("service_id")),
			MonitorID:     strings.TrimSpace(r.URL.Query().Get("monitor_id")),
			IncidentID:    strings.TrimSpace(r.URL.Query().Get("incident_id")),
			State:         strings.TrimSpace(r.URL.Query().Get("state")),
			Limit:         limit,
		}
		if err := query.Validate(); err != nil {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		deliveries, err := store.ListDeliveryStatuses(r.Context(), query)
		if err != nil {
			writeMonitorStoreError(w, r, "list deliveries", err)
			return
		}
		WriteJSON(w, http.StatusOK, deliveries)
	}
}

func retryMonitorDeliveryHandler(
	store MonitorStore,
	idem idempotency.Store,
	clock func() time.Time,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
			return
		}
		if store == nil {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "monitor delivery retry is not configured", nil)
			return
		}
		body, ok := readBoundedJSONRequestBody(w, r, "invalid monitor delivery retry request")
		if !ok {
			return
		}
		reservation, ok := reserveIdempotencyOrAbort(w, r, idem, body)
		if !ok {
			return
		}
		idempotentResponse := newIdempotencyResponseCapture(w)
		w = idempotentResponse
		defer idempotentResponse.Complete(r, idem, reservation.Reference())
		var request struct {
			ProjectID string `json:"project_id"`
		}
		if !decodeStrictJSONRequestBytes(w, r, body, &request, "invalid monitor delivery retry request") {
			return
		}
		if strings.TrimSpace(request.ProjectID) == "" {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, "project_id is required", nil)
			return
		}
		now := time.Now().UTC()
		if clock != nil {
			now = clock().UTC()
		}
		delivery, err := store.RetryDeadDelivery(
			r.Context(),
			request.ProjectID,
			strings.TrimSpace(r.PathValue("delivery_id")),
			now,
		)
		if errors.Is(err, monitors.ErrDeliveryNotDead) {
			writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "only dead-letter deliveries can be retried manually", nil)
			return
		}
		if err != nil {
			writeMonitorStoreError(w, r, "retry delivery", err)
			return
		}
		WriteJSON(w, http.StatusAccepted, delivery)
	}
}

func monitorDefinitionView(definition monitors.Definition) monitorView {
	return monitorView{
		Definition: definition, WindowSeconds: int(definition.Window / time.Second),
		EvaluationSeconds: int(definition.EvaluationInterval / time.Second),
	}
}

func writeMonitorStoreError(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "monitor resource not found", nil)
	case strings.Contains(strings.ToLower(err.Error()), "duplicate"),
		strings.Contains(strings.ToLower(err.Error()), "unique"):
		writeRequestError(w, r, http.StatusConflict, failurecodes.ResourceConflict, "monitor resource conflicts with existing state", nil)
	default:
		if strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "unsupported") ||
			strings.Contains(err.Error(), "at least") ||
			strings.Contains(err.Error(), "belong") {
			writeRequestError(w, r, http.StatusBadRequest, failurecodes.ResourceValidationFailed, err.Error(), nil)
			return
		}
		writeRequestError(w, r, http.StatusInternalServerError, failurecodes.InternalError, "monitor "+operation+" failed", nil)
	}
}
