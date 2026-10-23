// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/webhook"
)

type webhookDeliveryRecoveryFake struct {
	filter  webhook.DeliveryFilter
	list    []webhook.DeliverySummary
	detail  webhook.DeliveryDetail
	listErr error
	getErr  error
	getID   string
}

func (store *webhookDeliveryRecoveryFake) ListDeliveries(
	_ context.Context,
	filter webhook.DeliveryFilter,
) ([]webhook.DeliverySummary, error) {
	store.filter = filter
	return store.list, store.listErr
}

func (store *webhookDeliveryRecoveryFake) GetDelivery(
	_ context.Context,
	id string,
) (webhook.DeliveryDetail, error) {
	store.getID = id
	return store.detail, store.getErr
}

type webhookDeliveryRedriveFake struct {
	result   WebhookDeliveryRedriveResult
	err      error
	request  WebhookDeliveryRedriveRequest
	calls    int
	replayed bool
}

func (coordinator *webhookDeliveryRedriveFake) RedriveWebhookDelivery(
	_ context.Context,
	request WebhookDeliveryRedriveRequest,
) (WebhookDeliveryRedriveResult, error) {
	coordinator.request = request
	coordinator.calls++
	result := coordinator.result
	if coordinator.replayed && coordinator.calls > 1 {
		result.Replayed = true
	}
	return result, coordinator.err
}

func webhookDeliveryFixture() webhook.DeliveryDetail {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	terminal := now.Add(time.Minute)
	return webhook.DeliveryDetail{
		DeliverySummary: webhook.DeliverySummary{
			ID:                 "wbd_delivery_1",
			Provider:           webhook.ProviderGitHub,
			EndpointID:         "github",
			ProviderDeliveryID: "provider-delivery-1",
			EventType:          "push",
			EventHash:          strings.Repeat("a", 64),
			PayloadSHA256:      strings.Repeat("b", 64),
			PayloadBytes:       512,
			State:              webhook.InboxDeadLetter,
			RouteAttempt:       8,
			MaxRouteAttempts:   8,
			ManualRetries:      0,
			ReasonCode:         "WEBHOOK_ROUTE_FAILED",
			LastError:          "database temporarily unavailable",
			ReceivedAt:         now,
			LastReceivedAt:     now,
			AvailableAt:        now,
			TerminalAt:         &terminal,
			UpdatedAt:          terminal,
		},
		Targets: []webhook.TargetSummary{{
			ID:           "wbt_target_1",
			TargetKey:    "target-1",
			WorkspaceID:  "wrk_1",
			ProjectID:    "prj_1",
			ServiceID:    "svc_1",
			RepositoryID: 42,
			Repository:   "midnight/example",
			Branch:       "main",
			CommitSHA:    strings.Repeat("c", 40),
			State:        webhook.InboxDeadLetter,
			Attempt:      8,
			MaxAttempts:  8,
			ReasonCode:   "WEBHOOK_TARGET_DISPATCH_FAILED",
			LastError:    "database temporarily unavailable",
			AvailableAt:  now,
			TerminalAt:   &terminal,
			CreatedAt:    now,
			UpdatedAt:    terminal,
		}},
	}
}

func TestWebhookDeliveryCollectionValidatesBoundedFiltersAndReturnsPage(t *testing.T) {
	detail := webhookDeliveryFixture()
	store := &webhookDeliveryRecoveryFake{list: []webhook.DeliverySummary{detail.DeliverySummary}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/webhook-deliveries?project_id=prj_1&provider=github&state=dead_letter&limit=25",
		nil,
	)
	response := httptest.NewRecorder()

	webhookDeliveryCollectionHandler(store)(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if store.filter.Provider != webhook.ProviderGitHub ||
		store.filter.State != webhook.InboxDeadLetter ||
		store.filter.Limit != 25 ||
		store.filter.ProjectID != "prj_1" {
		t.Fatalf("filter = %#v", store.filter)
	}
	var page webhookDeliveryPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != detail.ID {
		t.Fatalf("page = %#v", page)
	}
	assertWebhookRecoveryResponseIsMetadataOnly(t, response.Body.String())
}

func TestWebhookDeliveryCollectionRejectsInvalidFilters(t *testing.T) {
	tests := []string{
		"",
		"?project_id=prj_1&provider=unknown",
		"?project_id=prj_1&provider=github&provider=gitlab-generic",
		"?project_id=prj_1&state=dead",
		"?project_id=prj_1&limit=0",
		"?project_id=prj_1&limit=201",
		"?project_id=prj_1&cursor=opaque",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			store := &webhookDeliveryRecoveryFake{}
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries"+query, nil)

			webhookDeliveryCollectionHandler(store)(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			if store.filter != (webhook.DeliveryFilter{}) {
				t.Fatalf("store was called with %#v", store.filter)
			}
		})
	}
}

func TestWebhookDeliveryDetailIsMetadataOnly(t *testing.T) {
	store := &webhookDeliveryRecoveryFake{detail: webhookDeliveryFixture()}
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries/wbd_delivery_1?project_id=prj_1", nil)
	request.SetPathValue("delivery_id", "wbd_delivery_1")
	response := httptest.NewRecorder()

	webhookDeliveryResourceHandler(store)(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if store.getID != "wbd_delivery_1" {
		t.Fatalf("get id = %q", store.getID)
	}
	if !strings.Contains(response.Body.String(), `"targets"`) {
		t.Fatalf("detail = %s", response.Body.String())
	}
	assertWebhookRecoveryResponseIsMetadataOnly(t, response.Body.String())
}

func TestWebhookDeliveryDetailProjectsOnlyRequestedProjectTargets(t *testing.T) {
	detail := webhookDeliveryFixture()
	foreign := detail.Targets[0]
	foreign.ID = "wbt_foreign_secret"
	foreign.ProjectID = "prj_foreign"
	foreign.ServiceID = "svc_foreign_secret"
	foreign.Repository = "private/foreign-secret"
	detail.Targets = append(detail.Targets, foreign)
	store := &webhookDeliveryRecoveryFake{detail: detail}
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries/wbd_delivery_1?project_id=prj_1", nil)
	request.SetPathValue("delivery_id", detail.ID)
	response := httptest.NewRecorder()

	webhookDeliveryResourceHandler(store)(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var projection webhookProjectDeliveryDetail
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.ExclusiveProject || len(projection.Targets) != 1 || projection.Targets[0].ProjectID != "prj_1" {
		t.Fatalf("projection = %#v", projection)
	}
	for _, secret := range []string{"prj_foreign", "svc_foreign_secret", "private/foreign-secret", "wbt_foreign_secret"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, response.Body.String())
		}
	}
}

func TestWebhookDeliveryHumanAuthorizationUsesExactProjectCapabilities(t *testing.T) {
	viewer, viewerAccess := webhookProjectPrincipal(t, identity.ProjectRoleViewer)
	editor, editorAccess := webhookProjectPrincipal(t, identity.ProjectRoleEditor)
	detail := webhookDeliveryFixture()
	store := &webhookDeliveryRecoveryFake{detail: detail, list: []webhook.DeliverySummary{detail.DeliverySummary}}

	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries?project_id=prj_1", nil)
	readRequest = readRequest.WithContext(context.WithValue(readRequest.Context(), identityPrincipalContextKey{}, viewer))
	readResponse := httptest.NewRecorder()
	webhookDeliveryCollectionHandler(store, viewerAccess)(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("viewer read status = %d body = %s", readResponse.Code, readResponse.Body.String())
	}

	coordinator := &webhookDeliveryRedriveFake{result: WebhookDeliveryRedriveResult{Response: mustWebhookStoredResponse(t, detail, "prj_1")}}
	deniedRequest := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1", nil)
	deniedRequest.SetPathValue("delivery_id", detail.ID)
	deniedRequest.Header.Set("Idempotency-Key", "viewer-redrive-denied")
	deniedRequest = deniedRequest.WithContext(context.WithValue(deniedRequest.Context(), identityPrincipalContextKey{}, viewer))
	deniedResponse := httptest.NewRecorder()
	webhookDeliveryRedriveHandler(coordinator, store, viewerAccess)(deniedResponse, deniedRequest)
	if deniedResponse.Code != http.StatusForbidden || coordinator.calls != 0 {
		t.Fatalf("viewer redrive status = %d calls = %d body = %s", deniedResponse.Code, coordinator.calls, deniedResponse.Body.String())
	}

	allowedRequest := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1", nil)
	allowedRequest.SetPathValue("delivery_id", detail.ID)
	allowedRequest.Header.Set("Idempotency-Key", "editor-redrive-allowed")
	allowedContext := context.WithValue(allowedRequest.Context(), identityPrincipalContextKey{}, editor)
	allowedContext = idempotency.WithActor(allowedContext, idempotency.UserActor(editor.UserID))
	allowedRequest = allowedRequest.WithContext(allowedContext)
	allowedResponse := httptest.NewRecorder()
	webhookDeliveryRedriveHandler(coordinator, store, editorAccess)(allowedResponse, allowedRequest)
	if allowedResponse.Code != http.StatusAccepted || coordinator.calls != 1 {
		t.Fatalf("editor redrive status = %d calls = %d body = %s", allowedResponse.Code, coordinator.calls, allowedResponse.Body.String())
	}
	if coordinator.request.ProjectID != "prj_1" ||
		coordinator.request.WorkspaceID != "wrk_1" ||
		coordinator.request.Actor != idempotency.UserActor(editor.UserID) ||
		coordinator.request.Tenant != projectScopedIdempotencyTenant("wrk_1", "prj_1") ||
		coordinator.request.Principal.UserID != editor.UserID {
		t.Fatalf("trusted request scope = %#v", coordinator.request)
	}
}

func webhookProjectPrincipal(t *testing.T, role identity.ProjectRole) (identity.Principal, *identity.MemoryAccessStore) {
	t.Helper()
	acceptedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	user := identity.User{ID: "usr_" + string(role), Email: string(role) + "@example.test"}
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_1", Slug: "workspace"})
	store.SeedProject("prj_1", "wrk_1", identity.ProjectMember{ProjectID: "prj_1", User: user, Role: role, AcceptedAt: &acceptedAt})
	return identity.Principal{Kind: "user", UserID: user.ID, SessionID: "ses_" + user.ID, AuthMethod: "password", InstanceAdmin: true}, store
}

func mustWebhookStoredResponse(t *testing.T, detail webhook.DeliveryDetail, projectID string) idempotency.StoredResponse {
	t.Helper()
	response, err := webhookDeliveryStoredResponse(http.StatusAccepted, detail, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestWebhookDeliveryTargetlessDetailsFailClosed(t *testing.T) {
	detail := webhookDeliveryFixture()
	detail.Targets = nil
	store := &webhookDeliveryRecoveryFake{detail: detail}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/webhook-deliveries/wbd_delivery_1?project_id=prj_1",
		nil,
	)
	request.SetPathValue("delivery_id", detail.ID)
	response := httptest.NewRecorder()

	webhookDeliveryResourceHandler(store)(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}

	if _, err := webhookDeliveryStoredResponse(http.StatusAccepted, detail, "prj_1"); !errors.Is(err, webhook.ErrInboxDeliveryScopeMismatch) {
		t.Fatalf("stored response error = %v, want scope mismatch", err)
	}
}

func TestWebhookDeliveryRedriveConcealsForeignDeliveryIDs(t *testing.T) {
	detail := webhookDeliveryFixture()
	detail.Targets[0].ProjectID = "prj_foreign"
	store := &webhookDeliveryRecoveryFake{detail: detail}
	coordinator := &webhookDeliveryRedriveFake{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1", nil)
	request.SetPathValue("delivery_id", detail.ID)
	request.Header.Set("Idempotency-Key", "conceal-foreign-delivery")
	response := httptest.NewRecorder()

	webhookDeliveryRedriveHandler(coordinator, store, nil)(response, request)

	if response.Code != http.StatusNotFound || coordinator.calls != 0 {
		t.Fatalf("status = %d calls = %d body = %s", response.Code, coordinator.calls, response.Body.String())
	}
}

func TestWebhookDeliveryRedriveRequiresEmptyBodyAndReplaysExactResponse(t *testing.T) {
	detail := webhookDeliveryFixture()
	detail.State = webhook.InboxPending
	detail.RouteAttempt = 0
	detail.ManualRetries = 1
	detail.TerminalAt = nil
	storedResponse, err := webhookDeliveryStoredResponse(http.StatusAccepted, detail, "prj_1")
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &webhookDeliveryRedriveFake{
		result:   WebhookDeliveryRedriveResult{Response: storedResponse},
		replayed: true,
	}
	handler := webhookDeliveryRedriveHandler(coordinator, &webhookDeliveryRecoveryFake{detail: detail}, nil)

	firstRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1",
		nil,
	)
	firstRequest.SetPathValue("delivery_id", "wbd_delivery_1")
	firstRequest.Header.Set("Idempotency-Key", "redrive-webhook-delivery")
	firstResponse := httptest.NewRecorder()
	handler(firstResponse, firstRequest)

	if firstResponse.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body = %s", firstResponse.Code, firstResponse.Body.String())
	}
	if coordinator.calls != 1 || coordinator.request.DeliveryID != "wbd_delivery_1" {
		t.Fatalf("redrive calls = %d request = %#v", coordinator.calls, coordinator.request)
	}
	if coordinator.request.Key != idempotency.Key("redrive-webhook-delivery") ||
		coordinator.request.Fingerprint.Method != http.MethodPost ||
		coordinator.request.ProjectID != "prj_1" ||
		coordinator.request.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte("prj_1\x00")) {
		t.Fatalf("coordinator request = %#v", coordinator.request)
	}
	if location := firstResponse.Header().Get("Location"); location != "/api/v1alpha/webhook-deliveries/wbd_delivery_1?project_id=prj_1" {
		t.Fatalf("Location = %q", location)
	}

	replayRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1",
		nil,
	)
	replayRequest.SetPathValue("delivery_id", "wbd_delivery_1")
	replayRequest.Header.Set("Idempotency-Key", "redrive-webhook-delivery")
	replayResponse := httptest.NewRecorder()
	handler(replayResponse, replayRequest)

	if replayResponse.Code != http.StatusAccepted ||
		replayResponse.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status = %d headers = %#v body = %s", replayResponse.Code, replayResponse.Header(), replayResponse.Body.String())
	}
	if coordinator.calls != 2 {
		t.Fatalf("redrive coordinator calls after replay = %d", coordinator.calls)
	}
	if replayResponse.Body.String() != firstResponse.Body.String() {
		t.Fatalf("replay body = %q, first = %q", replayResponse.Body.String(), firstResponse.Body.String())
	}
	assertWebhookRecoveryResponseIsMetadataOnly(t, replayResponse.Body.String())

	bodyRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1alpha/webhook-deliveries/wbd_delivery_1/redrive?project_id=prj_1",
		strings.NewReader("{}"),
	)
	bodyRequest.SetPathValue("delivery_id", "wbd_delivery_1")
	bodyRequest.Header.Set("Idempotency-Key", "redrive-webhook-with-body")
	bodyResponse := httptest.NewRecorder()
	handler(bodyResponse, bodyRequest)
	if bodyResponse.Code != http.StatusBadRequest {
		t.Fatalf("body status = %d body = %s", bodyResponse.Code, bodyResponse.Body.String())
	}
	if coordinator.calls != 2 {
		t.Fatalf("redrive called for non-empty body: %d", coordinator.calls)
	}
}

func TestWebhookDeliveryRecoveryMapsStoreErrors(t *testing.T) {
	t.Run("detail not found", func(t *testing.T) {
		store := &webhookDeliveryRecoveryFake{getErr: webhook.ErrInboxDeliveryNotFound}
		request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries/missing?project_id=prj_1", nil)
		request.SetPathValue("delivery_id", "missing")
		response := httptest.NewRecorder()
		webhookDeliveryResourceHandler(store)(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
		}
	})
	t.Run("redrive conflict", func(t *testing.T) {
		coordinator := &webhookDeliveryRedriveFake{err: webhook.ErrInboxDeliveryTerminal}
		request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/webhook-deliveries/wbd_1/redrive?project_id=prj_1", nil)
		request.SetPathValue("delivery_id", "wbd_1")
		request.Header.Set("Idempotency-Key", "redrive-non-dead")
		response := httptest.NewRecorder()
		webhookDeliveryRedriveHandler(coordinator, &webhookDeliveryRecoveryFake{detail: webhookDeliveryFixture()}, nil)(response, request)
		if response.Code != http.StatusConflict ||
			!strings.Contains(response.Body.String(), `"code":"RESOURCE_CONFLICT"`) {
			t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
		}
	})
	t.Run("unexpected list failure", func(t *testing.T) {
		store := &webhookDeliveryRecoveryFake{listErr: errors.New("database unavailable")}
		request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries?project_id=prj_1", nil)
		response := httptest.NewRecorder()
		webhookDeliveryCollectionHandler(store)(response, request)
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
		}
	})
}

func TestWebhookDeliveryRecoveryRoutesAreProtectedAndHonestWhenUnwired(t *testing.T) {
	router := NewRouterWithDataStack(DataStack{APIBearerToken: "operator-recovery-token"})

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodGet, "/api/v1alpha/webhook-deliveries", nil),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body = %s", unauthorized.Code, unauthorized.Body.String())
	}

	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1alpha/webhook-deliveries"},
		{http.MethodGet, "/api/v1alpha/webhook-deliveries/wbd_1"},
		{http.MethodPost, "/api/v1alpha/webhook-deliveries/wbd_1/redrive"},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(test.method, test.path, nil)
		request.Header.Set("Authorization", "Bearer operator-recovery-token")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable ||
			!strings.Contains(response.Body.String(), `"capability":"durable_webhook_inbox"`) {
			t.Fatalf("%s %s status = %d body = %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func assertWebhookTargetsAreEmptyArray(t *testing.T, body []byte) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	targets, ok := document["targets"].([]any)
	if !ok || len(targets) != 0 {
		t.Fatalf("targets = %#v, want non-null empty array; body=%s", document["targets"], body)
	}
}

func assertWebhookRecoveryResponseIsMetadataOnly(t *testing.T, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, forbidden := range []string{
		`"payload":`,
		`"signature_key_id":`,
		`"signature":`,
		`"lease_token":`,
		`"lease_owner":`,
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("response exposed %q: %s", forbidden, body)
		}
	}
}
