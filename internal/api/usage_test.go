// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/metering"
)

func TestUsageSummaryRequiresHumanAndReturnsEvidenceStates(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	store := metering.NewMemoryStore()
	_, err := store.Append(t.Context(), metering.LedgerEvent{EventID: "projects", Source: "resource_reconciler",
		SourceInstance: "wrk_one", SourceEventID: "projects:1", WorkspaceID: "wrk_one",
		Resource: metering.ResourceProjectCount, Quantity: 3, Kind: metering.ValueGauge, ObservedAt: now.Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	handler := usageSummaryHandler(store, func() time.Time { return now })
	request := httptest.NewRequest(http.MethodGet, "/api/v1alpha/usage/summary?workspace_id=wrk_one", nil)
	principal := identity.Principal{Kind: "user", UserID: "usr_one", SessionID: "ses_one", AuthMethod: "password"}
	request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var summary metering.Summary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.PolicyProfile != "self_hosted" || len(summary.Usage) != 10 {
		t.Fatalf("summary=%+v", summary)
	}
	for _, line := range summary.Usage {
		if line.Resource == metering.ResourceBuildSeconds && line.Coverage.Status != metering.CoveragePartial {
			t.Fatalf("build coverage=%+v", line.Coverage)
		}
	}

	operator := httptest.NewRequest(http.MethodGet, "/api/v1alpha/usage/summary?workspace_id=wrk_one", nil)
	operator = operator.WithContext(withProductPrincipal(operator.Context(), productPrincipal{Kind: productPrincipalOperator}))
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, operator)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("operator status=%d body=%s", denied.Code, denied.Body.String())
	}
}

func TestUsageSummaryRejectsRepeatedScopeAndLongWindows(t *testing.T) {
	handler := usageSummaryHandler(metering.NewMemoryStore(), nil)
	principal := identity.Principal{Kind: "user", UserID: "usr_one", SessionID: "ses_one"}
	for _, path := range []string{
		"/api/v1alpha/usage/summary?workspace_id=one&workspace_id=two",
		"/api/v1alpha/usage/summary?workspace_id=one&since=2024-01-01T00:00:00Z&until=2026-01-02T00:00:00Z",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{Kind: productPrincipalHuman, Human: principal}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("path=%s status=%d", path, response.Code)
		}
	}
}
