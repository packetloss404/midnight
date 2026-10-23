// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

type recordingDeploymentActions struct {
	imageRequest   ImageReplayRequest
	runtimeRequest RuntimeActionRequest
	result         DeploymentActionResult
	err            error
}

type recordingTenantDeploymentActions struct {
	request TenantDeploymentActionRequest
	result  TenantDeployIntakeResult
	err     error
	calls   int
}

func (f *recordingTenantDeploymentActions) CommitTenantDeploymentAction(
	_ context.Context,
	request TenantDeploymentActionRequest,
) (TenantDeployIntakeResult, error) {
	f.calls++
	f.request = request
	return f.result, f.err
}

func (f *recordingDeploymentActions) CommitImageReplay(_ context.Context, request ImageReplayRequest) (DeploymentActionResult, error) {
	f.imageRequest = request
	return f.result, f.err
}

func (f *recordingDeploymentActions) CommitRuntimeAction(_ context.Context, request RuntimeActionRequest) (DeploymentActionResult, error) {
	f.runtimeRequest = request
	return f.result, f.err
}

func TestDeploymentActionRESTUsesDurableImmutableReplay(t *testing.T) {
	deployments := NewMemoryDeploymentStore(nil)
	_, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ID: "dpl_source", ProjectID: "prj_1", ServiceID: "svc_1",
		EnvironmentID: "env_1", Action: deploy.ActionDeployImage,
	})
	if err != nil {
		t.Fatal(err)
	}
	actions := &recordingDeploymentActions{
		result: DeploymentActionResult{Created: true, OutboxID: "wox_1"},
	}
	handler := deploymentActionHandler(actions, deployments)
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/services/svc_1/deployments/dpl_source/actions/redeploy", nil)
	request.SetPathValue("service_id", "svc_1")
	request.SetPathValue("deployment_id", "dpl_source")
	request.SetPathValue("action", "redeploy")
	request.Header.Set("Idempotency-Key", "retry-safe")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if actions.imageRequest.Action != deploy.ActionRedeployImage ||
		actions.imageRequest.SourceID != "dpl_source" ||
		actions.imageRequest.ServiceID != "svc_1" {
		t.Fatalf("image replay request = %#v", actions.imageRequest)
	}
	if actions.imageRequest.TriggerKey != "rest:deployment-action:redeploy:svc_1:dpl_source:retry-safe" {
		t.Fatalf("trigger key = %q", actions.imageRequest.TriggerKey)
	}
	var result DeploymentActionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OutboxID != "wox_1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestDeploymentActionRESTRejectsCrossServiceTarget(t *testing.T) {
	deployments := NewMemoryDeploymentStore(nil)
	if _, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ID: "dpl_other", ProjectID: "prj_1", ServiceID: "svc_other",
		EnvironmentID: "env_1", Action: deploy.ActionDeployImage,
	}); err != nil {
		t.Fatal(err)
	}
	actions := &recordingDeploymentActions{}
	request := httptest.NewRequest(http.MethodPost, "/actions", nil)
	request.SetPathValue("service_id", "svc_1")
	request.SetPathValue("deployment_id", "dpl_other")
	request.SetPathValue("action", "stop")
	request.Header.Set("Idempotency-Key", "scope-proof")
	response := httptest.NewRecorder()

	deploymentActionHandler(actions, deployments)(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if actions.runtimeRequest.TargetID != "" {
		t.Fatalf("runtime action was invoked: %#v", actions.runtimeRequest)
	}
}

func TestDeploymentActionRESTReplaysWithoutDuplicatingWork(t *testing.T) {
	deployments := NewMemoryDeploymentStore(nil)
	if _, err := deployments.CreateDeployment(context.Background(), DeploymentRecord{
		ID: "dpl_active", ProjectID: "prj_1", ServiceID: "svc_1",
		EnvironmentID: "env_1", Action: deploy.ActionDeployImage,
	}); err != nil {
		t.Fatal(err)
	}
	actions := &recordingDeploymentActions{
		result: DeploymentActionResult{Created: false, OutboxID: "wox_existing"},
	}
	request := httptest.NewRequest(http.MethodPost, "/actions", nil)
	request.SetPathValue("service_id", "svc_1")
	request.SetPathValue("deployment_id", "dpl_active")
	request.SetPathValue("action", "restart")
	request.Header.Set("Idempotency-Key", "same")
	response := httptest.NewRecorder()

	deploymentActionHandler(actions, deployments)(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("Idempotent-Replay = %q", response.Header().Get("Idempotent-Replay"))
	}
	if actions.runtimeRequest.Action != deploy.RuntimeActionRestart {
		t.Fatalf("runtime request = %#v", actions.runtimeRequest)
	}
}

func TestHumanDeploymentActionUsesOnlyCoordinatedEmptyBodyRoute(t *testing.T) {
	stored := idempotency.StoredResponse{
		StatusCode: http.StatusAccepted,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Location":     []string{v1alphaPrefix + "/operations/op_human_action"},
		},
		Body: []byte("{\"created\":true}\n"),
	}
	coordinator := &recordingTenantDeploymentActions{
		result: TenantDeployIntakeResult{Response: stored},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		tenantDeploymentActionIdempotencyPath(
			"svc_human",
			"dep_human",
			"restart",
		),
		nil,
	)
	request.SetPathValue("service_id", "svc_human")
	request.SetPathValue("deployment_id", "dep_human")
	request.SetPathValue("action", "restart")
	request.Header.Set("Idempotency-Key", "human-action")
	principal := identity.Principal{
		Kind:       "user",
		UserID:     "usr_human",
		SessionID:  "ses_human",
		AuthMethod: "password",
	}
	session := identity.Session{
		ID:                "ses_human",
		UserID:            "usr_human",
		SessionGeneration: 1,
	}
	ctx := withProductPrincipal(
		request.Context(),
		productPrincipal{Kind: productPrincipalHuman, Human: principal},
	)
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()

	deploymentActionHandler(nil, nil, coordinator)(response, request)

	if response.Code != http.StatusAccepted ||
		coordinator.calls != 1 ||
		coordinator.request.ServiceID != "svc_human" ||
		coordinator.request.DeploymentID != "dep_human" ||
		coordinator.request.Action != "restart" ||
		coordinator.request.Key != "human-action" ||
		coordinator.request.Fingerprint.Path != request.URL.Path ||
		coordinator.request.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256(nil) {
		t.Fatalf(
			"response=%d calls=%d request=%#v body=%s",
			response.Code,
			coordinator.calls,
			coordinator.request,
			response.Body.String(),
		)
	}

	nonEmpty := request.Clone(request.Context())
	nonEmpty.Body = io.NopCloser(strings.NewReader("{}"))
	nonEmpty.Header.Set("Idempotency-Key", "human-action-body")
	nonEmptyResponse := httptest.NewRecorder()
	deploymentActionHandler(nil, nil, coordinator)(nonEmptyResponse, nonEmpty)
	if nonEmptyResponse.Code != http.StatusBadRequest || coordinator.calls != 1 {
		t.Fatalf(
			"non-empty response=%d calls=%d body=%s",
			nonEmptyResponse.Code,
			coordinator.calls,
			nonEmptyResponse.Body.String(),
		)
	}
}

func TestRouterAdmitsExactHumanDeploymentActionWithoutOperatorFallback(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	coordinator := &recordingTenantDeploymentActions{
		result: TenantDeployIntakeResult{Response: idempotency.StoredResponse{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte("{\"created\":true}\n"),
		}},
	}
	stack.TenantDeploymentActions = coordinator
	handler := NewRouterWithDataStack(stack)
	token := loginIdentityForTest(t, manager, "alice@example.com")
	request := httptest.NewRequest(
		http.MethodPost,
		tenantDeploymentActionIdempotencyPath(
			"svc_a",
			"dep_a",
			"redeploy",
		),
		nil,
	)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", "router-human-action")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted ||
		coordinator.calls != 1 ||
		coordinator.request.Action != "redeploy" {
		t.Fatalf(
			"status=%d calls=%d request=%#v body=%s",
			response.Code,
			coordinator.calls,
			coordinator.request,
			response.Body.String(),
		)
	}
}
