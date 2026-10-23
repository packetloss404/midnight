// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type recordingTenantOperationCancel struct {
	request TenantOperationCancelRequest
	result  TenantOperationCancelResult
	err     error
	calls   int
}

func (c *recordingTenantOperationCancel) CancelTenantOperation(
	_ context.Context,
	request TenantOperationCancelRequest,
) (TenantOperationCancelResult, error) {
	c.calls++
	c.request = request
	return c.result, c.err
}

func humanCancelRequest(
	t *testing.T,
	operationID orailtypes.ID,
	body io.Reader,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		tenantOperationCancelIdempotencyPath(operationID),
		body,
	)
	request.Header.Set("Idempotency-Key", "human-operation-cancel")
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
	return request.WithContext(ctx)
}

func TestHumanOperationCancelUsesCoordinatedEmptyBodyRoute(t *testing.T) {
	operationID := orailtypes.ID("op_cancel")
	stored, err := tenantOperationCancelStoredResponse(orailtypes.Operation{
		ID:    operationID,
		Kind:  "deploy",
		State: orailtypes.OperationCanceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingTenantOperationCancel{
		result: TenantOperationCancelResult{Response: stored},
	}
	request := humanCancelRequest(t, operationID, nil)
	response := httptest.NewRecorder()

	cancelOperationHandler(nil, nil, nil, nil, coordinator)(
		response,
		request,
		operationID,
	)

	if response.Code != http.StatusAccepted ||
		coordinator.calls != 1 ||
		coordinator.request.OperationID != operationID ||
		coordinator.request.Key != "human-operation-cancel" ||
		coordinator.request.Fingerprint.Path != request.URL.Path ||
		coordinator.request.Fingerprint.BodySHA256 !=
			idempotency.BodyHashSHA256(nil) {
		t.Fatalf(
			"response=%d calls=%d request=%#v body=%s",
			response.Code,
			coordinator.calls,
			coordinator.request,
			response.Body.String(),
		)
	}

	nonEmpty := humanCancelRequest(t, operationID, strings.NewReader("{}"))
	nonEmpty.Header.Set("Idempotency-Key", "human-operation-cancel-body")
	nonEmptyResponse := httptest.NewRecorder()
	cancelOperationHandler(nil, nil, nil, nil, coordinator)(
		nonEmptyResponse,
		nonEmpty,
		operationID,
	)
	if nonEmptyResponse.Code != http.StatusBadRequest || coordinator.calls != 1 {
		t.Fatalf(
			"non-empty response=%d calls=%d body=%s",
			nonEmptyResponse.Code,
			coordinator.calls,
			nonEmptyResponse.Body.String(),
		)
	}
}

func TestRouterAdmitsOnlyExactHumanOperationCancel(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	stored, err := tenantOperationCancelStoredResponse(orailtypes.Operation{
		ID:    "op_router_cancel",
		Kind:  "deploy",
		State: orailtypes.OperationCanceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &recordingTenantOperationCancel{
		result: TenantOperationCancelResult{Response: stored},
	}
	stack.TenantOperationCancel = coordinator
	handler := NewRouterWithDataStack(stack)
	token := loginIdentityForTest(t, manager, "alice@example.com")

	cancelRequest := httptest.NewRequest(
		http.MethodPost,
		tenantOperationCancelIdempotencyPath("op_router_cancel"),
		nil,
	)
	cancelRequest.Header.Set("Authorization", "Bearer "+token)
	cancelRequest.Header.Set("Idempotency-Key", "router-human-cancel")
	cancelResponse := httptest.NewRecorder()
	handler.ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusAccepted ||
		coordinator.calls != 1 ||
		coordinator.request.OperationID != "op_router_cancel" {
		t.Fatalf(
			"cancel response=%d calls=%d request=%#v body=%s",
			cancelResponse.Code,
			coordinator.calls,
			coordinator.request,
			cancelResponse.Body.String(),
		)
	}

	failRequest := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/operations/op_router_cancel/fail",
		nil,
	)
	failRequest.Header.Set("Authorization", "Bearer "+token)
	failResponse := httptest.NewRecorder()
	handler.ServeHTTP(failResponse, failRequest)
	if failResponse.Code != http.StatusForbidden || coordinator.calls != 1 {
		t.Fatalf(
			"fail response=%d calls=%d body=%s",
			failResponse.Code,
			coordinator.calls,
			failResponse.Body.String(),
		)
	}
}
