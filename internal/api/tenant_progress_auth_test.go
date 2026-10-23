// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestHumanOperationReadAuthorizationUsesRouteCapabilityAndConcealsDenial(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_test", SessionID: "ses_test"}
	session := identity.Session{ID: "ses_test", UserID: "usr_test"}
	wantScope := tenantOperationScope{
		OperationID:   "op_test",
		DeploymentID:  "dep_test",
		ServiceID:     "svc_test",
		ProjectID:     "prj_test",
		EnvironmentID: "env_test",
	}
	var capabilities []identity.Capability
	authorizer := tenantOperationAuthorizerFunc(func(
		_ context.Context,
		gotPrincipal identity.Principal,
		gotSession identity.Session,
		operationID orailtypes.ID,
		capability identity.Capability,
		_ time.Time,
	) (tenantOperationScope, error) {
		if gotPrincipal.UserID != principal.UserID ||
			gotSession.ID != session.ID ||
			operationID != "op_test" {
			t.Fatalf(
				"authorization tuple = principal=%+v session=%+v operation=%q",
				gotPrincipal,
				gotSession,
				operationID,
			)
		}
		capabilities = append(capabilities, capability)
		return wantScope, nil
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScope, ok := tenantOperationScopeFromContext(r.Context())
		if !ok || gotScope != wantScope {
			t.Fatalf("authorized operation scope=%#v ok=%t, want %#v", gotScope, ok, wantScope)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := humanOperationReadAuthorizationMiddleware(
		authorizer,
		func() time.Time { return time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC) },
		next,
	)

	for _, test := range []struct {
		path       string
		capability identity.Capability
	}{
		{path: v1alphaPrefix + "/operations/op_test", capability: identity.CapabilityServiceRead},
		{path: v1alphaPrefix + "/operations/op_test/events", capability: identity.CapabilityServiceRead},
		{path: v1alphaPrefix + "/operations/op_test/logs", capability: identity.CapabilityLogsRead},
	} {
		request := humanOperationRequest(t, principal, session, test.path)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if got := capabilities[len(capabilities)-1]; got != test.capability {
			t.Fatalf("%s capability=%q want=%q", test.path, got, test.capability)
		}
	}

	denied := humanOperationReadAuthorizationMiddleware(
		tenantOperationAuthorizerFunc(func(
			context.Context,
			identity.Principal,
			identity.Session,
			orailtypes.ID,
			identity.Capability,
			time.Time,
		) (tenantOperationScope, error) {
			return tenantOperationScope{}, ErrOperationNotFound
		}),
		nil,
		next,
	)
	response := httptest.NewRecorder()
	denied.ServeHTTP(
		response,
		humanOperationRequest(t, principal, session, v1alphaPrefix+"/operations/op_test"),
	)
	if response.Code != http.StatusNotFound ||
		!strings.Contains(response.Body.String(), `"code":"OPERATION_NOT_FOUND"`) {
		t.Fatalf("denied status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHumanServiceSubrouteBoundaryAdmitsOnlyDeploymentReads(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := humanServiceDeploymentReadBoundaryMiddleware(next)
	for _, path := range []string{
		v1alphaPrefix + "/services/svc_test/deployments",
		v1alphaPrefix + "/services/svc_test/deployments/latest-active",
		v1alphaPrefix + "/services/svc_test/deployments/dep_test",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{
			Kind: productPrincipalHuman,
		}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: v1alphaPrefix + "/services/svc_test/github"},
		{method: http.MethodPost, path: v1alphaPrefix + "/services/svc_test/up"},
		{method: http.MethodPost, path: v1alphaPrefix + "/services/svc_test/deployments"},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request = request.WithContext(withProductPrincipal(request.Context(), productPrincipal{
			Kind: productPrincipalHuman,
		}))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestHumanOperationReadRevalidationPreservesInitialCanonicalScope(t *testing.T) {
	principal := identity.Principal{Kind: "user", UserID: "usr_test", SessionID: "ses_test"}
	session := identity.Session{ID: "ses_test", UserID: "usr_test"}
	initialScope := tenantOperationScope{
		OperationID:   "op_test",
		DeploymentID:  "dep_initial",
		ServiceID:     "svc_test",
		ProjectID:     "prj_test",
		EnvironmentID: "env_test",
	}
	changedScope := initialScope
	changedScope.DeploymentID = "dep_changed"
	calls := 0
	authorizer := tenantOperationAuthorizerFunc(func(
		context.Context,
		identity.Principal,
		identity.Session,
		orailtypes.ID,
		identity.Capability,
		time.Time,
	) (tenantOperationScope, error) {
		calls++
		if calls == 1 {
			return initialScope, nil
		}
		return changedScope, nil
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotScope, ok := tenantOperationScopeFromContext(r.Context())
		if !ok || gotScope != initialScope {
			t.Fatalf("context scope=%#v ok=%t, want %#v", gotScope, ok, initialScope)
		}
		guard, ok := operationStreamAccessGuardFromContext(r.Context())
		if !ok {
			t.Fatal("operation stream access guard missing")
		}
		if err := guard.Check(r.Context()); !errors.Is(err, ErrOperationNotFound) {
			t.Fatalf("guard lineage change error=%v, want ErrOperationNotFound", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := humanOperationReadAuthorizationMiddleware(authorizer, nil, next)
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		humanOperationRequest(
			t,
			principal,
			session,
			v1alphaPrefix+"/operations/op_test/events",
		),
	)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if calls != 2 {
		t.Fatalf("authorization calls=%d want=2", calls)
	}
}

func TestOperationLiveStreamRevalidatesBeforeEveryEvent(t *testing.T) {
	store := NewMemoryOperationStore()
	ctx := context.Background()
	if _, err := store.Create(ctx, orailtypes.Operation{
		ID:    "op_guarded",
		Kind:  "deploy",
		State: orailtypes.OperationSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	for _, event := range []OperationEvent{
		{OperationID: "op_guarded", State: orailtypes.OperationQueued, Message: "queued"},
		{OperationID: "op_guarded", State: orailtypes.OperationSucceeded, Message: "done"},
	} {
		if _, err := store.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	checks := 0
	request := httptest.NewRequest(
		http.MethodGet,
		v1alphaPrefix+"/operations/op_guarded/events?stream=sse",
		nil,
	)
	request = request.WithContext(withOperationStreamAccessGuard(
		request.Context(),
		operationStreamAccessGuard{
			Check: func(context.Context) error {
				checks++
				if checks > 2 {
					return ErrOperationNotFound
				}
				return nil
			},
			Interval: time.Hour,
		},
	))
	response := httptest.NewRecorder()
	writeOperationLiveEvents(
		response,
		request,
		store,
		newOperationStreamHub(),
		"op_guarded",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "id: 1\n") ||
		strings.Contains(response.Body.String(), "id: 2\n") {
		t.Fatalf("stream did not close on revalidation loss: %q", response.Body.String())
	}
	if checks != 3 {
		t.Fatalf("authorization checks=%d want=3", checks)
	}
}

func humanOperationRequest(
	t *testing.T,
	principal identity.Principal,
	session identity.Session,
	path string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	ctx := withProductPrincipal(request.Context(), productPrincipal{
		Kind:  productPrincipalHuman,
		Human: principal,
	})
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	return request.WithContext(ctx)
}
