// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	logrecords "github.com/midnight/midnight/internal/logs"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type recordingLogQueryStore struct {
	filters []logrecords.Filter
}

func (s *recordingLogQueryStore) Query(
	filter logrecords.Filter,
	_ logrecords.Cursor,
	_ int,
) (logrecords.QueryResult, error) {
	s.filters = append(s.filters, filter)
	return logrecords.QueryResult{}, nil
}

func TestHumanOperationLogsIgnoreForeignDeploymentIDInFreeFormEvent(t *testing.T) {
	operations := NewMemoryOperationStore()
	operation, err := operations.Create(context.Background(), orailtypes.Operation{
		ID:    "op_scoped_logs",
		Kind:  "deploy",
		State: orailtypes.OperationRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.AppendEvent(context.Background(), OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message:     "deploy.progress deployment_id=dep_foreign service_id=svc_foreign",
	}); err != nil {
		t.Fatal(err)
	}

	logStore := &recordingLogQueryStore{}
	request := httptest.NewRequest(
		http.MethodGet,
		v1alphaPrefix+"/operations/op_scoped_logs/logs",
		nil,
	)
	ctx := withProductPrincipal(request.Context(), productPrincipal{
		Kind: productPrincipalHuman,
	})
	ctx = withTenantOperationScope(ctx, tenantOperationScope{
		OperationID:   operation.ID,
		DeploymentID:  "dep_authorized",
		ServiceID:     "svc_authorized",
		ProjectID:     "prj_authorized",
		EnvironmentID: "env_authorized",
	})
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()

	writeOperationLogs(
		response,
		request,
		operations,
		logStore,
		NewCursorCodec([]byte("tenant-operation-log-cursor-secret")),
		operation.ID,
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(logStore.filters) != 4 {
		t.Fatalf("log queries=%d want=4", len(logStore.filters))
	}
	for index, filter := range logStore.filters[:3] {
		if !slices.Equal(filter.Deployments, []string{"dep_authorized"}) {
			t.Fatalf("app query %d deployments=%#v want authorized deployment only", index, filter.Deployments)
		}
		if len(filter.Operations) != 0 {
			t.Fatalf("app query %d operations=%#v want none", index, filter.Operations)
		}
		if slices.Contains(filter.Deployments, "dep_foreign") {
			t.Fatalf("app query %d widened to foreign deployment: %#v", index, filter)
		}
	}
	platform := logStore.filters[3]
	if !slices.Equal(platform.Operations, []string{"op_scoped_logs"}) ||
		len(platform.Deployments) != 0 {
		t.Fatalf("platform query=%#v want operation-only scope", platform)
	}
}

func TestHumanOperationLogsFailClosedWithoutMatchingAuthorizedScope(t *testing.T) {
	operations := NewMemoryOperationStore()
	operation, err := operations.Create(context.Background(), orailtypes.Operation{
		ID:    "op_scoped_logs",
		Kind:  "deploy",
		State: orailtypes.OperationRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	logStore := &recordingLogQueryStore{}

	for _, test := range []struct {
		name  string
		scope tenantOperationScope
	}{
		{name: "missing scope"},
		{name: "different operation", scope: tenantOperationScope{
			OperationID:   "op_other",
			DeploymentID:  "dep_authorized",
			ServiceID:     "svc_authorized",
			ProjectID:     "prj_authorized",
			EnvironmentID: "env_authorized",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodGet,
				v1alphaPrefix+"/operations/op_scoped_logs/logs",
				nil,
			)
			ctx := withProductPrincipal(request.Context(), productPrincipal{
				Kind: productPrincipalHuman,
			})
			if test.scope.valid() {
				ctx = withTenantOperationScope(ctx, test.scope)
			}
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()
			writeOperationLogs(
				response,
				request,
				operations,
				logStore,
				NewCursorCodec([]byte("tenant-operation-log-cursor-secret")),
				operation.ID,
			)
			if response.Code != http.StatusNotFound ||
				!strings.Contains(response.Body.String(), `"code":"OPERATION_NOT_FOUND"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if len(logStore.filters) != 0 {
		t.Fatalf("unauthorized log queries=%d want=0", len(logStore.filters))
	}
}
