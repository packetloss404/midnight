// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
)

type tenantServiceConfigCoordinatorStub struct {
	readRequest TenantServiceConfigReadRequest
	readView    ServiceConfigView
	readErr     error
	readCalls   int
	putRequest  TenantServiceConfigPutRequest
	putResult   TenantMutationResult
	putErr      error
	putCalls    int
}

func (s *tenantServiceConfigCoordinatorStub) GetServiceConfig(
	_ context.Context,
	request TenantServiceConfigReadRequest,
) (ServiceConfigView, error) {
	s.readRequest = request
	s.readCalls++
	return s.readView, s.readErr
}

func (s *tenantServiceConfigCoordinatorStub) PutServiceConfig(
	_ context.Context,
	request TenantServiceConfigPutRequest,
) (TenantMutationResult, error) {
	s.putRequest = request
	s.putCalls++
	return s.putResult, s.putErr
}

func TestTenantServiceConfigHandlerDelegatesExactVerifiedTuple(t *testing.T) {
	principal := identity.Principal{
		Kind:       "user",
		UserID:     "usr_config",
		SessionID:  "ses_config",
		AuthMethod: "password",
	}
	session := identity.Session{
		ID:                "ses_config",
		UserID:            "usr_config",
		SessionGeneration: 4,
	}
	t.Run("get", func(t *testing.T) {
		coordinator := &tenantServiceConfigCoordinatorStub{
			readView: ServiceConfigView{
				ProjectID:     "prj_api",
				ServiceID:     "svc_api",
				EnvironmentID: "env_api",
				Version:       0,
				Explicit:      false,
				TargetPort:    8080,
			},
		}
		request := tenantServiceConfigHandlerRequest(
			t,
			http.MethodGet,
			"",
			principal,
			session,
		)
		response := httptest.NewRecorder()

		serviceConfigHandler(nil, nil, nil, nil, coordinator)(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
		if coordinator.readCalls != 1 ||
			coordinator.readRequest.Principal != principal ||
			coordinator.readRequest.Session.ID != session.ID ||
			coordinator.readRequest.Session.SessionGeneration != session.SessionGeneration ||
			coordinator.readRequest.ServiceID != "svc_api" ||
			coordinator.readRequest.EnvironmentID != "env_api" {
			t.Fatalf("read request = %#v calls=%d", coordinator.readRequest, coordinator.readCalls)
		}
		var view ServiceConfigView
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Explicit || view.Version != 0 || view.TargetPort != 8080 {
			t.Fatalf("view = %#v", view)
		}
	})

	t.Run("put exact response and replay metadata", func(t *testing.T) {
		body := `{"expected_version":3,"target_port":9090,"start_command":"bin/api"}`
		exactBody := []byte(`{"service_id":"svc_api","environment_id":"env_api","version":4,"exact":true}` + "\n")
		coordinator := &tenantServiceConfigCoordinatorStub{
			putResult: TenantMutationResult{
				Response: idempotency.StoredResponse{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       exactBody,
				},
				Replayed: true,
			},
		}
		request := tenantServiceConfigHandlerRequest(
			t,
			http.MethodPut,
			body,
			principal,
			session,
		)
		request.Header.Set("Idempotency-Key", "service-config-exact-1")
		request.Header.Set("User-Agent", "midnight-service-config-test")
		request.RemoteAddr = "192.0.2.44:443"
		response := httptest.NewRecorder()

		serviceConfigHandler(nil, nil, nil, nil, coordinator)(response, request)

		if response.Code != http.StatusOK ||
			response.Body.String() != string(exactBody) ||
			response.Header().Get("Idempotent-Replayed") != "true" {
			t.Fatalf(
				"response = %d headers=%v body=%q",
				response.Code,
				response.Header(),
				response.Body.String(),
			)
		}
		got := coordinator.putRequest
		if coordinator.putCalls != 1 ||
			got.Principal != principal ||
			got.Session.ID != session.ID ||
			got.Session.SessionGeneration != session.SessionGeneration ||
			got.Config.ProjectID != "" ||
			got.Config.ServiceID != "svc_api" ||
			got.Config.EnvironmentID != "env_api" ||
			got.Config.TargetPort != 9090 ||
			got.Config.StartCommand != "bin/api" ||
			got.ExpectedVersion != 3 ||
			got.Key != "service-config-exact-1" ||
			got.Fingerprint.Method != http.MethodPut ||
			got.Fingerprint.Path != tenantServiceConfigIdempotencyPath("svc_api", "env_api") ||
			got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) ||
			got.SourceIP != "192.0.2.44:443" ||
			got.UserAgent != "midnight-service-config-test" {
			t.Fatalf("put request = %#v calls=%d", got, coordinator.putCalls)
		}
	})
}

func TestTenantServiceConfigHandlerRejectsNonCanonicalPutInputs(t *testing.T) {
	principal := identity.Principal{
		Kind: "user", UserID: "usr_config", SessionID: "ses_config",
	}
	session := identity.Session{
		ID: "ses_config", UserID: "usr_config", SessionGeneration: 1,
	}
	tests := []struct {
		name       string
		body       string
		header     func(http.Header)
		wantStatus int
	}{
		{
			name:       "body exceeds 32 KiB",
			body:       strings.Repeat("x", serviceConfigBodyLimitBytes+1),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "unknown field",
			body:       `{"unknown":true}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "trailing object",
			body:       `{} {}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "null body",
			body:       `null`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing expected version",
			body:       `{"target_port":9090}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "null expected version",
			body:       `{"expected_version":null}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "repeated idempotency header",
			body: `{"expected_version":0}`,
			header: func(header http.Header) {
				header.Add("Idempotency-Key", "config-one")
				header.Add("Idempotency-Key", "config-two")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "coalesced idempotency header",
			body: `{"expected_version":0}`,
			header: func(header http.Header) {
				header.Set("Idempotency-Key", "config-one,config-two")
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coordinator := &tenantServiceConfigCoordinatorStub{}
			request := tenantServiceConfigHandlerRequest(
				t,
				http.MethodPut,
				tt.body,
				principal,
				session,
			)
			if tt.header != nil {
				tt.header(request.Header)
			} else {
				request.Header.Set("Idempotency-Key", "config-strict-input")
			}
			response := httptest.NewRecorder()

			serviceConfigHandler(nil, nil, nil, nil, coordinator)(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf(
					"status = %d body=%s, want %d",
					response.Code,
					response.Body.String(),
					tt.wantStatus,
				)
			}
			if coordinator.putCalls != 0 {
				t.Fatalf("coordinator called %d times with %#v", coordinator.putCalls, coordinator.putRequest)
			}
		})
	}
}

func TestTenantServiceConfigCoordinatorFailsClosedWithoutSharedAuthority(t *testing.T) {
	principal := identity.Principal{
		Kind: "user", UserID: "usr_config", SessionID: "ses_config",
	}
	session := identity.Session{
		ID: "ses_config", UserID: "usr_config", SessionGeneration: 1,
	}
	read := TenantServiceConfigReadRequest{
		Principal: principal, Session: session,
		ServiceID: "svc_api", EnvironmentID: "env_api",
	}

	var nilCoordinator *PostgresTenantMutationCoordinator
	if _, err := nilCoordinator.GetServiceConfig(context.Background(), read); err == nil {
		t.Fatal("nil coordinator GetServiceConfig succeeded")
	}

	authorityPool := &pg.Pool{}
	configPool := &pg.Pool{}
	coordinator := &PostgresTenantMutationCoordinator{
		Pool:           authorityPool,
		ServiceConfigs: serviceconfig.NewStore(configPool),
	}
	if _, err := coordinator.GetServiceConfig(context.Background(), read); err == nil ||
		!strings.Contains(err.Error(), "does not share one postgres config authority") {
		t.Fatalf("GetServiceConfig authority error = %v", err)
	}

	coordinator.ServiceConfigs = serviceconfig.NewStore(authorityPool)
	coordinator.Idempotency = idempotency.NewPostgresStore(configPool)
	coordinator.Audit = audit.NewPostgresChain(authorityPool)
	put := TenantServiceConfigPutRequest{
		Principal: principal,
		Session:   session,
		Config: serviceconfig.Config{
			ServiceID: "svc_api", EnvironmentID: "env_api",
		},
		Key: "config-authority",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPut,
			Path:   tenantServiceConfigIdempotencyPath("svc_api", "env_api"),
			BodySHA256: idempotency.BodyHashSHA256(
				[]byte(`{}`),
			),
		},
	}
	if _, err := coordinator.PutServiceConfig(context.Background(), put); err == nil ||
		!strings.Contains(err.Error(), "does not share idempotency and audit authority") {
		t.Fatalf("PutServiceConfig authority error = %v", err)
	}
}

func TestTenantServiceConfigHandlerFailsClosedWithoutCoordinator(t *testing.T) {
	principal := identity.Principal{
		Kind: "user", UserID: "usr_config", SessionID: "ses_config",
	}
	session := identity.Session{
		ID: "ses_config", UserID: "usr_config", SessionGeneration: 1,
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			request := tenantServiceConfigHandlerRequest(
				t,
				method,
				`{}`,
				principal,
				session,
			)
			request.Header.Set("Idempotency-Key", "config-unavailable")
			response := httptest.NewRecorder()

			serviceConfigHandler(nil, nil, nil, nil)(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s, want 503", response.Code, response.Body.String())
			}
		})
	}
}

func tenantServiceConfigHandlerRequest(
	t *testing.T,
	method string,
	body string,
	principal identity.Principal,
	session identity.Session,
) *http.Request {
	t.Helper()
	path := v1alphaPrefix + "/services/svc_api/environments/env_api/config"
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPut {
		setTestIfMatchFromExpectedVersion(t, request, body)
	}
	request.Pattern = v1alphaPrefix + "/services/{service_id}/environments/{environment_id}/config"
	request.SetPathValue("service_id", "svc_api")
	request.SetPathValue("environment_id", "env_api")
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{
		Kind:  productPrincipalHuman,
		Human: principal,
	})
	return request.WithContext(ctx)
}
