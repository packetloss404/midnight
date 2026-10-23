// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/monitors"
)

type monitorStoreFake struct {
	definition    monitors.Definition
	channel       monitors.Channel
	subscribed    []string
	incidentQuery monitors.IncidentQuery
	deliveryQuery monitors.DeliveryStatusQuery
	retried       []string
	retryErr      error
}

func (store *monitorStoreFake) CreateDefinition(_ context.Context, definition monitors.Definition) (monitors.Definition, error) {
	definition.ID = "mon_1"
	store.definition = definition
	return definition, nil
}

func (store *monitorStoreFake) ListDefinitions(context.Context, string) ([]monitors.Definition, error) {
	return []monitors.Definition{store.definition}, nil
}

func (store *monitorStoreFake) DeleteDefinition(context.Context, string, string) error {
	return nil
}

func (store *monitorStoreFake) CreateChannel(_ context.Context, channel monitors.Channel) (monitors.Channel, error) {
	channel.ID = "mch_1"
	store.channel = channel
	return channel, nil
}

func (store *monitorStoreFake) ListChannels(context.Context, string) ([]monitors.Channel, error) {
	return []monitors.Channel{store.channel}, nil
}

func (store *monitorStoreFake) Subscribe(_ context.Context, projectID, monitorID, channelID string) error {
	store.subscribed = []string{projectID, monitorID, channelID}
	return nil
}

func (store *monitorStoreFake) ListIncidents(_ context.Context, query monitors.IncidentQuery) ([]monitors.IncidentStatus, error) {
	store.incidentQuery = query
	return []monitors.IncidentStatus{{
		ID: "minc_1", MonitorID: "mon_1", MonitorName: "CPU high",
		State: monitors.IncidentStateOpen, OpenedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
		DeliveryCount: 1, DeadCount: 1,
	}}, nil
}

func (store *monitorStoreFake) ListDeliveryStatuses(_ context.Context, query monitors.DeliveryStatusQuery) ([]monitors.DeliveryStatus, error) {
	store.deliveryQuery = query
	return []monitors.DeliveryStatus{monitorDeliveryStatusFixture()}, nil
}

func (store *monitorStoreFake) RetryDeadDelivery(_ context.Context, projectID, deliveryID string, now time.Time) (monitors.DeliveryStatus, error) {
	store.retried = []string{projectID, deliveryID, now.Format(time.RFC3339)}
	if store.retryErr != nil {
		return monitors.DeliveryStatus{}, store.retryErr
	}
	value := monitorDeliveryStatusFixture()
	value.State = monitors.DeliveryStatePending
	value.Attempts = 0
	value.ManualRetries++
	value.ErrorSummary = ""
	return value, nil
}

func monitorDeliveryStatusFixture() monitors.DeliveryStatus {
	status := 503
	return monitors.DeliveryStatus{
		ID: "mdl_1", IncidentID: "minc_1", MonitorID: "mon_1", MonitorName: "CPU high",
		ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1",
		ChannelID: "mch_1", ChannelName: "on-call", ChannelType: "webhook",
		EventType: monitors.EventFiring, State: monitors.DeliveryStateDead,
		Attempts: monitors.MaxDeliveryAttempts, ResponseStatus: &status,
		ErrorSummary: "remote endpoint returned a non-success status",
		AvailableAt:  time.Date(2026, 7, 28, 1, 1, 0, 0, time.UTC),
		CreatedAt:    time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
		UpdatedAt:    time.Date(2026, 7, 28, 1, 2, 0, 0, time.UTC),
	}
}

func TestMonitorsRESTCreatesMetricDefinition(t *testing.T) {
	store := &monitorStoreFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/monitors", strings.NewReader(`{
		"project_id":"prj_1",
		"environment_id":"env_1",
		"service_id":"svc_1",
		"name":"CPU high",
		"signal":"cpu_percent",
		"operator":"gt",
		"threshold":80,
		"window_seconds":300,
		"evaluation_interval_seconds":60,
		"consecutive_failures":2,
		"consecutive_recoveries":2
	}`))
	request.Header.Set("Idempotency-Key", "create-monitor")
	response := httptest.NewRecorder()

	monitorsHandler(store, idempotency.NewMemoryStore())(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.definition.Scope.ServiceID != "svc_1" ||
		store.definition.Window.Seconds() != 300 ||
		store.definition.ConsecutiveFailures != 2 {
		t.Fatalf("definition = %#v", store.definition)
	}
	if !strings.Contains(response.Body.String(), `"window_seconds":300`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestMonitorChannelRESTStoresOnlyVariableReferences(t *testing.T) {
	store := &monitorStoreFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/monitor-channels", strings.NewReader(`{
		"project_id":"prj_1",
		"display_name":"on-call",
		"endpoint_variable_id":"var_endpoint",
		"signing_variable_id":"var_signing"
	}`))
	request.Header.Set("Idempotency-Key", "create-channel")
	response := httptest.NewRecorder()

	monitorChannelsHandler(store, idempotency.NewMemoryStore())(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.channel.EndpointVariableID != "var_endpoint" ||
		store.channel.SigningVariableID != "var_signing" {
		t.Fatalf("channel = %#v", store.channel)
	}
	if strings.Contains(response.Body.String(), "https://") {
		t.Fatalf("response leaked endpoint plaintext: %s", response.Body.String())
	}
}

func TestMonitorIncidentAndDeliveryRESTAreScopedAndMetadataOnly(t *testing.T) {
	store := &monitorStoreFake{}

	incidentRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/monitor-incidents?project_id=prj_1&environment_id=env_1&service_id=svc_1&state=open&limit=25",
		nil,
	)
	incidentResponse := httptest.NewRecorder()
	monitorIncidentsHandler(store)(incidentResponse, incidentRequest)
	if incidentResponse.Code != http.StatusOK {
		t.Fatalf("incident status = %d body = %s", incidentResponse.Code, incidentResponse.Body.String())
	}
	if store.incidentQuery.ProjectID != "prj_1" || store.incidentQuery.State != "open" ||
		store.incidentQuery.Limit != 25 {
		t.Fatalf("incident query = %#v", store.incidentQuery)
	}

	deliveryRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/monitor-deliveries?project_id=prj_1&service_id=svc_1&state=dead",
		nil,
	)
	deliveryResponse := httptest.NewRecorder()
	monitorDeliveriesHandler(store)(deliveryResponse, deliveryRequest)
	if deliveryResponse.Code != http.StatusOK {
		t.Fatalf("delivery status = %d body = %s", deliveryResponse.Code, deliveryResponse.Body.String())
	}
	var deliveries []map[string]any
	if err := json.Unmarshal(deliveryResponse.Body.Bytes(), &deliveries); err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0]["state"] != "dead" {
		t.Fatalf("deliveries = %#v", deliveries)
	}
	for _, forbidden := range []string{
		"payload", "endpoint_variable_id", "signing_variable_id", "lease_owner", "https://",
	} {
		if strings.Contains(strings.ToLower(deliveryResponse.Body.String()), forbidden) {
			t.Fatalf("delivery response exposed %q: %s", forbidden, deliveryResponse.Body.String())
		}
	}
}

func TestMonitorDeliveryRetryAcceptsOnlyScopedDeadLetterRequest(t *testing.T) {
	store := &monitorStoreFake{}
	now := time.Date(2026, 7, 28, 3, 4, 5, 0, time.UTC)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/monitor-deliveries/mdl_1/retry",
		strings.NewReader(`{"project_id":"prj_1"}`),
	)
	request.SetPathValue("delivery_id", "mdl_1")
	request.Header.Set("Idempotency-Key", "retry-dead-delivery")
	response := httptest.NewRecorder()

	retryMonitorDeliveryHandler(store, idempotency.NewMemoryStore(), func() time.Time { return now })(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if len(store.retried) != 3 || store.retried[0] != "prj_1" || store.retried[1] != "mdl_1" {
		t.Fatalf("retried = %#v", store.retried)
	}
	if !strings.Contains(response.Body.String(), `"state":"pending"`) ||
		strings.Contains(response.Body.String(), "remote endpoint") {
		t.Fatalf("retry response = %s", response.Body.String())
	}
}

func TestMonitorDeliveryRetryRejectsNonDeadRows(t *testing.T) {
	store := &monitorStoreFake{retryErr: monitors.ErrDeliveryNotDead}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/monitor-deliveries/mdl_1/retry",
		strings.NewReader(`{"project_id":"prj_1"}`),
	)
	request.SetPathValue("delivery_id", "mdl_1")
	request.Header.Set("Idempotency-Key", "retry-pending-delivery")
	response := httptest.NewRecorder()

	retryMonitorDeliveryHandler(store, idempotency.NewMemoryStore(), nil)(response, request)

	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"RESOURCE_CONFLICT"`) {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}
