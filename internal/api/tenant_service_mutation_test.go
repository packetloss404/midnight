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

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type tenantServiceMutationCoordinatorStub struct {
	request TenantServiceCreateRequest
	result  TenantMutationResult
	err     error
}

func (s *tenantServiceMutationCoordinatorStub) CreateProject(context.Context, TenantProjectCreateRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, nil
}

func (s *tenantServiceMutationCoordinatorStub) CreateService(_ context.Context, request TenantServiceCreateRequest) (TenantMutationResult, error) {
	s.request = request
	return s.result, s.err
}

func (s *tenantServiceMutationCoordinatorStub) CreateEnvironment(context.Context, TenantEnvironmentCreateRequest) (TenantMutationResult, error) {
	return TenantMutationResult{}, nil
}

func TestCreateTenantServiceDelegatesVerifiedTupleAndReplaysExactResponse(t *testing.T) {
	body := `{"project_id":"prj_api","slug":"web","display_name":"Web"}`
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(body))
	request.Pattern = v1alphaPrefix + "/services"
	request.Header.Set("Idempotency-Key", "service-create-1")
	request.Header.Set("User-Agent", "midnight-test")
	request.RemoteAddr = "192.0.2.20:443"
	principal := identity.Principal{
		Kind:       "user",
		UserID:     "usr_alice",
		SessionID:  "ses_alice",
		AuthMethod: "password",
	}
	session := identity.Session{
		ID:                "ses_alice",
		UserID:            "usr_alice",
		SessionGeneration: 3,
	}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	request = request.WithContext(ctx)

	responseBody := []byte(`{"id":"svc_exact","project_id":"prj_api","slug":"web","display_name":"Web"}` + "\n")
	coordinator := &tenantServiceMutationCoordinatorStub{result: TenantMutationResult{
		Response: idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{v1alphaPrefix + "/services/svc_exact"},
			},
			Body: responseBody,
		},
		Replayed: true,
	}}
	response := httptest.NewRecorder()

	createService(response, request, NewMemoryResourceStore(), idempotency.NewMemoryStore(), nil, coordinator)

	if response.Code != http.StatusCreated ||
		response.Body.String() != string(responseBody) ||
		response.Header().Get("Location") != v1alphaPrefix+"/services/svc_exact" ||
		response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("response = %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	got := coordinator.request
	if got.Principal != principal ||
		got.Session.ID != session.ID ||
		got.Session.SessionGeneration != session.SessionGeneration ||
		got.Service.ProjectID != "prj_api" ||
		got.Service.Slug != "web" ||
		got.Service.DisplayName != "Web" ||
		got.Key != "service-create-1" ||
		got.Fingerprint.Method != http.MethodPost ||
		got.Fingerprint.Path != v1alphaPrefix+"/services" ||
		got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) ||
		got.SourceIP != "192.0.2.20:443" ||
		got.UserAgent != "midnight-test" {
		t.Fatalf("coordinator request = %#v", got)
	}
}

func TestCreateTenantServiceRequiresProjectBeforeCoordinator(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services", strings.NewReader(`{"slug":"web"}`))
	request.Pattern = v1alphaPrefix + "/services"
	request.Header.Set("Idempotency-Key", "service-create-1")
	principal := identity.Principal{
		Kind: "user", UserID: "usr_alice", SessionID: "ses_alice",
	}
	session := identity.Session{
		ID: "ses_alice", UserID: "usr_alice", SessionGeneration: 3,
	}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	request = request.WithContext(ctx)
	coordinator := &tenantServiceMutationCoordinatorStub{}
	response := httptest.NewRecorder()

	createService(response, request, NewMemoryResourceStore(), idempotency.NewMemoryStore(), nil, coordinator)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
	}
	if coordinator.request.Service.Slug != "" {
		t.Fatalf("coordinator called with %#v, want validation before delegation", coordinator.request)
	}
	var got ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed ||
		got.Error.Details["field"] != "project_id" {
		t.Fatalf("error = %#v, want project_id validation failure", got.Error)
	}
}

func TestTenantServiceCoordinatorFailsClosedAcrossPostgresAuthorities(t *testing.T) {
	authorityPool := &pg.Pool{}
	productPool := &pg.Pool{}
	coordinator := &PostgresTenantMutationCoordinator{
		Pool: authorityPool,
		Resources: &PostgresResourceStore{
			Pool:      productPool,
			IDFactory: defaultResourceID,
		},
		Idempotency: idempotency.NewPostgresStore(authorityPool),
		Audit:       audit.NewPostgresChain(authorityPool),
		Now:         func() time.Time { return time.Now().UTC() },
		IDFactory:   func(orailtypes.IDPrefix) orailtypes.ID { return "svc_must_not_be_minted" },
	}
	request := TenantServiceCreateRequest{
		Principal: identity.Principal{
			Kind: "user", UserID: "usr_alice", SessionID: "ses_alice",
		},
		Session: identity.Session{
			ID: "ses_alice", UserID: "usr_alice", SessionGeneration: 1,
		},
		Service: Service{
			ProjectID: "prj_api", Slug: "web", DisplayName: "Web",
		},
		Key: "service-create-1",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/services",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	}

	if _, err := coordinator.CreateService(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "does not share one postgres authority") {
		t.Fatalf("CreateService error = %v, want fail-closed authority error", err)
	}
}

func TestTenantServiceCoordinatorUsesCentralServiceFieldValidation(t *testing.T) {
	pool := &pg.Pool{}
	coordinator := &PostgresTenantMutationCoordinator{
		Pool: pool,
		Resources: &PostgresResourceStore{
			Pool:      pool,
			IDFactory: defaultResourceID,
		},
		Idempotency: idempotency.NewPostgresStore(pool),
		Audit:       audit.NewPostgresChain(pool),
		Now:         func() time.Time { return time.Now().UTC() },
		IDFactory:   func(orailtypes.IDPrefix) orailtypes.ID { return "svc_validation" },
	}
	base := TenantServiceCreateRequest{
		Principal: identity.Principal{
			Kind: "user", UserID: "usr_alice", SessionID: "ses_alice",
		},
		Session: identity.Session{
			ID: "ses_alice", UserID: "usr_alice", SessionGeneration: 1,
		},
		Service: Service{
			ProjectID: "prj_api", Slug: "web", DisplayName: "Web",
		},
		Key: "service-create-validation",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/services",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	}

	tests := []struct {
		name   string
		mutate func(*TenantServiceCreateRequest)
	}{
		{
			name: "invalid slug",
			mutate: func(request *TenantServiceCreateRequest) {
				request.Service.Slug = "Web_API"
			},
		},
		{
			name: "long display name",
			mutate: func(request *TenantServiceCreateRequest) {
				request.Service.DisplayName = strings.Repeat("界", 201)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := base
			tt.mutate(&request)
			if err := coordinator.validateServiceCreate(request); !errors.Is(err, ErrResourceInvalid) {
				t.Fatalf("error = %v, want ErrResourceInvalid", err)
			}
		})
	}
}
