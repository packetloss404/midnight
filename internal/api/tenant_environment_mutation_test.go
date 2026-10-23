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

type tenantEnvironmentMutationCoordinatorStub struct {
	request TenantEnvironmentCreateRequest
	result  TenantMutationResult
	err     error
}

func (s *tenantEnvironmentMutationCoordinatorStub) CreateProject(
	context.Context,
	TenantProjectCreateRequest,
) (TenantMutationResult, error) {
	return TenantMutationResult{}, nil
}

func (s *tenantEnvironmentMutationCoordinatorStub) CreateService(
	context.Context,
	TenantServiceCreateRequest,
) (TenantMutationResult, error) {
	return TenantMutationResult{}, nil
}

func (s *tenantEnvironmentMutationCoordinatorStub) CreateEnvironment(
	_ context.Context,
	request TenantEnvironmentCreateRequest,
) (TenantMutationResult, error) {
	s.request = request
	return s.result, s.err
}

func TestCreateTenantEnvironmentDelegatesVerifiedTupleAndReplaysExactResponse(t *testing.T) {
	body := `{"project_id":"prj_api","slug":"staging","name":"Staging","tier":"staging","is_protected":false}`
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/environments",
		strings.NewReader(body),
	)
	request.Pattern = v1alphaPrefix + "/environments"
	request.Header.Set("Idempotency-Key", "environment-create-1")
	request.Header.Set("User-Agent", "midnight-test")
	request.RemoteAddr = "192.0.2.21:443"
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

	responseBody := []byte(
		`{"id":"env_exact","project_id":"prj_api","slug":"staging","name":"Staging","tier":"staging","is_protected":false}` + "\n",
	)
	coordinator := &tenantEnvironmentMutationCoordinatorStub{result: TenantMutationResult{
		Response: idempotency.StoredResponse{
			StatusCode: http.StatusCreated,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Location":     []string{v1alphaPrefix + "/environments/env_exact"},
			},
			Body: responseBody,
		},
		Replayed: true,
	}}
	response := httptest.NewRecorder()

	createEnvironment(
		response,
		request,
		NewMemoryEnvironmentStore(nil),
		coordinator,
	)

	if response.Code != http.StatusCreated ||
		response.Body.String() != string(responseBody) ||
		response.Header().Get("Location") != v1alphaPrefix+"/environments/env_exact" ||
		response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"response = %d headers=%v body=%q",
			response.Code,
			response.Header(),
			response.Body.String(),
		)
	}
	got := coordinator.request
	if got.Principal != principal ||
		got.Session.ID != session.ID ||
		got.Session.SessionGeneration != session.SessionGeneration ||
		got.Environment.ProjectID != "prj_api" ||
		got.Environment.Slug != "staging" ||
		got.Environment.Name != "Staging" ||
		got.Environment.Tier != "staging" ||
		got.Environment.IsProtected ||
		got.Key != "environment-create-1" ||
		got.Fingerprint.Method != http.MethodPost ||
		got.Fingerprint.Path != v1alphaPrefix+"/environments" ||
		got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) ||
		got.SourceIP != "192.0.2.21:443" ||
		got.UserAgent != "midnight-test" {
		t.Fatalf("coordinator request = %#v", got)
	}
}

func TestCreateTenantEnvironmentRequiresProjectBeforeCoordinator(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/environments",
		strings.NewReader(`{"slug":"staging"}`),
	)
	request.Pattern = v1alphaPrefix + "/environments"
	request.Header.Set("Idempotency-Key", "environment-create-1")
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
	coordinator := &tenantEnvironmentMutationCoordinatorStub{}
	response := httptest.NewRecorder()

	createEnvironment(
		response,
		request,
		NewMemoryEnvironmentStore(nil),
		coordinator,
	)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
	}
	if coordinator.request.Environment.Slug != "" {
		t.Fatalf(
			"coordinator called with %#v, want validation before delegation",
			coordinator.request,
		)
	}
	var got ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error.Code != failurecodes.ResourceValidationFailed {
		t.Fatalf("error = %#v, want project validation failure", got.Error)
	}
}

func TestCreateTenantEnvironmentRejectsAmbiguousIdempotencyKeyBeforeCoordinator(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		wantCode failurecodes.Code
	}{
		{
			name:     "repeated header fields",
			values:   []string{"environment-first", "environment-second"},
			wantCode: failurecodes.IdempotencyKeyRequired,
		},
		{
			name:     "proxy coalesced fields",
			values:   []string{"environment-first,environment-second"},
			wantCode: failurecodes.IdempotencyKeyInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				v1alphaPrefix+"/environments",
				strings.NewReader(`{"project_id":"prj_api","slug":"staging"}`),
			)
			request.Pattern = v1alphaPrefix + "/environments"
			for _, value := range tt.values {
				request.Header.Add("Idempotency-Key", value)
			}
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
			coordinator := &tenantEnvironmentMutationCoordinatorStub{}
			response := httptest.NewRecorder()

			createEnvironment(response, request, NewMemoryEnvironmentStore(nil), coordinator)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", response.Code, response.Body.String())
			}
			if coordinator.request.Key != "" {
				t.Fatalf("coordinator called with key %q", coordinator.request.Key)
			}
			var got ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Error.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", got.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestCreateTenantEnvironmentMapsProtectedAuthorizationDenial(t *testing.T) {
	body := `{"project_id":"prj_api","slug":"production","name":"Production","tier":"production","is_protected":true}`
	request := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/environments",
		strings.NewReader(body),
	)
	request.Pattern = v1alphaPrefix + "/environments"
	request.Header.Set("Idempotency-Key", "environment-protected-1")
	principal := identity.Principal{
		Kind: "user", UserID: "usr_member", SessionID: "ses_member",
	}
	session := identity.Session{
		ID: "ses_member", UserID: "usr_member", SessionGeneration: 1,
	}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{Kind: productPrincipalHuman, Human: principal})
	request = request.WithContext(ctx)
	coordinator := &tenantEnvironmentMutationCoordinatorStub{
		err: identity.ErrAuthorizationDenied,
	}
	response := httptest.NewRecorder()

	createEnvironment(
		response,
		request,
		NewMemoryEnvironmentStore(nil),
		coordinator,
	)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", response.Code, response.Body.String())
	}
	if !coordinator.request.Environment.IsProtected {
		t.Fatalf("protected intent was not delegated: %#v", coordinator.request)
	}
}

func TestTenantEnvironmentCoordinatorFailsClosedAcrossPostgresAuthorities(t *testing.T) {
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
		IDFactory:   func(orailtypes.IDPrefix) orailtypes.ID { return "env_must_not_be_minted" },
	}
	request := TenantEnvironmentCreateRequest{
		Principal: identity.Principal{
			Kind: "user", UserID: "usr_alice", SessionID: "ses_alice",
		},
		Session: identity.Session{
			ID: "ses_alice", UserID: "usr_alice", SessionGeneration: 1,
		},
		Environment: Environment{
			ProjectID: "prj_api",
			Slug:      "staging",
			Name:      "Staging",
			Tier:      "staging",
		},
		Key: "environment-create-1",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/environments",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	}

	if _, err := coordinator.CreateEnvironment(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "does not share one postgres authority") {
		t.Fatalf("CreateEnvironment error = %v, want fail-closed authority error", err)
	}
}

func TestTenantEnvironmentCoordinatorUsesCentralEnvironmentFieldValidation(t *testing.T) {
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
		IDFactory:   func(orailtypes.IDPrefix) orailtypes.ID { return "env_validation" },
	}
	base := TenantEnvironmentCreateRequest{
		Principal: identity.Principal{
			Kind: "user", UserID: "usr_alice", SessionID: "ses_alice",
		},
		Session: identity.Session{
			ID: "ses_alice", UserID: "usr_alice", SessionGeneration: 1,
		},
		Environment: Environment{
			ProjectID: "prj_api",
			Slug:      "staging",
			Name:      "Staging",
			Tier:      "staging",
		},
		Key: "environment-create-validation",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/environments",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	}

	tests := []struct {
		name   string
		mutate func(*TenantEnvironmentCreateRequest)
	}{
		{
			name: "invalid slug",
			mutate: func(request *TenantEnvironmentCreateRequest) {
				request.Environment.Slug = "Staging_API"
			},
		},
		{
			name: "invalid tier",
			mutate: func(request *TenantEnvironmentCreateRequest) {
				request.Environment.Tier = "critical"
			},
		},
		{
			name: "long name",
			mutate: func(request *TenantEnvironmentCreateRequest) {
				request.Environment.Name = strings.Repeat("界", 201)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := base
			tt.mutate(&request)
			if err := coordinator.validateEnvironmentCreate(request); !errors.Is(err, ErrResourceInvalid) {
				t.Fatalf("error = %v, want ErrResourceInvalid", err)
			}
		})
	}
}
