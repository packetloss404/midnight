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
)

type tenantEnvironmentUpdateCoordinatorStub struct {
	request TenantEnvironmentUpdateRequest
	result  TenantMutationResult
	err     error
	calls   int
}

func (s *tenantEnvironmentUpdateCoordinatorStub) UpdateEnvironment(
	_ context.Context,
	request TenantEnvironmentUpdateRequest,
) (TenantMutationResult, error) {
	s.calls++
	s.request = request
	return s.result, s.err
}

func TestUpdateTenantEnvironmentDelegatesExactTupleFingerprintAndResponse(t *testing.T) {
	body := `{"expected_version":7,"slug":" next ","name":" Next ","tier":" staging "}`
	request, principal, session := tenantEnvironmentUpdateRequest(
		t,
		"env_api",
		body,
		"environment-update-1",
	)
	request.Header.Set("User-Agent", "midnight-environment-update-test")
	request.RemoteAddr = "192.0.2.61:443"
	responseBody := []byte(
		`{"id":"env_api","project_id":"prj_api","slug":"next","name":"Next","tier":"staging","is_protected":true,"resource_version":8}` + "\n",
	)
	coordinator := &tenantEnvironmentUpdateCoordinatorStub{
		result: TenantMutationResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       responseBody,
			},
			Replayed: true,
		},
	}
	response := httptest.NewRecorder()

	updateEnvironment(
		response,
		request,
		NewMemoryEnvironmentStore(nil),
		"env_api",
		coordinator,
	)

	if response.Code != http.StatusOK ||
		response.Body.String() != string(responseBody) ||
		response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("response=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	got := coordinator.request
	if coordinator.calls != 1 ||
		got.Principal != principal ||
		got.Session.ID != session.ID ||
		got.Session.SessionGeneration != session.SessionGeneration ||
		got.EnvironmentID != "env_api" ||
		got.Update.ExpectedVersion != 7 ||
		got.Update.Slug == nil || *got.Update.Slug != "next" ||
		got.Update.Name == nil || *got.Update.Name != "Next" ||
		got.Update.Tier == nil || *got.Update.Tier != "staging" ||
		got.Key != "environment-update-1" ||
		got.Fingerprint.Method != http.MethodPatch ||
		got.Fingerprint.Path != v1alphaPrefix+"/environments/env_api" ||
		got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(body)) ||
		got.SourceIP != "192.0.2.61:443" ||
		got.UserAgent != "midnight-environment-update-test" {
		t.Fatalf("coordinator request=%#v calls=%d", got, coordinator.calls)
	}
}

func TestUpdateTenantEnvironmentRejectsInvalidBodiesBeforeCoordinator(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "empty", body: "", wantStatus: http.StatusBadRequest},
		{name: "whitespace", body: " \n\t", wantStatus: http.StatusBadRequest},
		{name: "null object", body: "null", wantStatus: http.StatusBadRequest},
		{name: "array", body: `[]`, wantStatus: http.StatusBadRequest},
		{name: "unknown", body: `{"expected_version":1,"name":"Next","extra":true}`, wantStatus: http.StatusBadRequest},
		{name: "project immutable", body: `{"expected_version":1,"name":"Next","project_id":"prj_other"}`, wantStatus: http.StatusBadRequest},
		{name: "protected forbidden", body: `{"expected_version":1,"name":"Next","is_protected":true}`, wantStatus: http.StatusBadRequest},
		{name: "duplicate forbidden", body: `{"expected_version":1,"name":"Next","copy_config":true}`, wantStatus: http.StatusBadRequest},
		{name: "trailing object", body: `{"expected_version":1,"name":"Next"} {}`, wantStatus: http.StatusBadRequest},
		{name: "missing version", body: `{"name":"Next"}`, wantStatus: http.StatusBadRequest},
		{name: "null version", body: `{"expected_version":null,"name":"Next"}`, wantStatus: http.StatusBadRequest},
		{name: "zero version", body: `{"expected_version":0,"name":"Next"}`, wantStatus: http.StatusBadRequest},
		{name: "negative version", body: `{"expected_version":-1,"name":"Next"}`, wantStatus: http.StatusBadRequest},
		{name: "no metadata", body: `{"expected_version":1}`, wantStatus: http.StatusBadRequest},
		{name: "null slug", body: `{"expected_version":1,"slug":null}`, wantStatus: http.StatusBadRequest},
		{name: "null name", body: `{"expected_version":1,"name":null}`, wantStatus: http.StatusBadRequest},
		{name: "null tier", body: `{"expected_version":1,"tier":null}`, wantStatus: http.StatusBadRequest},
		{
			name:       "over body limit",
			body:       `{"expected_version":1,"name":"` + strings.Repeat("x", environmentCreateBodyLimitBytes) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, _, _ := tenantEnvironmentUpdateRequest(
				t,
				"env_api",
				tt.body,
				"environment-update-invalid",
			)
			coordinator := &tenantEnvironmentUpdateCoordinatorStub{}
			response := httptest.NewRecorder()

			updateEnvironment(
				response,
				request,
				NewMemoryEnvironmentStore(nil),
				"env_api",
				coordinator,
			)

			if response.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), tt.wantStatus)
			}
			if coordinator.calls != 0 {
				t.Fatalf("coordinator called %d time(s): %#v", coordinator.calls, coordinator.request)
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error response: %v body=%s", err, response.Body.String())
			}
			if envelope.Error.Code != failurecodes.ResourceValidationFailed {
				t.Fatalf("code=%q, want %q", envelope.Error.Code, failurecodes.ResourceValidationFailed)
			}
		})
	}
}

func TestUpdateTenantEnvironmentRequiresSingletonIdempotencyKey(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		wantCode failurecodes.Code
	}{
		{name: "missing", wantCode: failurecodes.IdempotencyKeyRequired},
		{name: "repeated", values: []string{"first", "second"}, wantCode: failurecodes.IdempotencyKeyRequired},
		{name: "coalesced", values: []string{"first,second"}, wantCode: failurecodes.IdempotencyKeyInvalid},
		{name: "blank", values: []string{" "}, wantCode: failurecodes.IdempotencyKeyInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, _, _ := tenantEnvironmentUpdateRequest(
				t,
				"env_api",
				`{"expected_version":1,"name":"Next"}`,
				"",
			)
			for _, value := range tt.values {
				request.Header.Add("Idempotency-Key", value)
			}
			coordinator := &tenantEnvironmentUpdateCoordinatorStub{}
			response := httptest.NewRecorder()

			updateEnvironment(response, request, NewMemoryEnvironmentStore(nil), "env_api", coordinator)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if coordinator.calls != 0 {
				t.Fatalf("coordinator called %d time(s)", coordinator.calls)
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tt.wantCode {
				t.Fatalf("code=%q, want %q", envelope.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestUpdateTenantEnvironmentMapsCoordinatorErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   failurecodes.Code
	}{
		{name: "session", err: identity.ErrSessionInvalid, wantStatus: http.StatusUnauthorized, wantCode: failurecodes.AuthSessionInvalid},
		{name: "concealed", err: identity.ErrAccessNotFound, wantStatus: http.StatusNotFound, wantCode: failurecodes.ResourceNotFound},
		{name: "role", err: identity.ErrAuthorizationDenied, wantStatus: http.StatusForbidden, wantCode: failurecodes.AuthorizationDenied},
		{name: "in progress", err: idempotency.ErrRequestInProgress, wantStatus: http.StatusConflict, wantCode: failurecodes.IdempotencyRequestInProgress},
		{name: "body reuse", err: idempotency.ErrKeyReusedDifferentBody, wantStatus: http.StatusConflict, wantCode: failurecodes.IdempotencyKeyReusedDifferentBody},
		{name: "route reuse", err: idempotency.ErrKeyReused, wantStatus: http.StatusConflict, wantCode: failurecodes.IdempotencyKeyReused},
		{name: "resource conflict", err: ErrResourceConflict, wantStatus: http.StatusConflict, wantCode: failurecodes.ResourceConflict},
		{name: "invalid", err: ErrResourceInvalid, wantStatus: http.StatusBadRequest, wantCode: failurecodes.ResourceValidationFailed},
		{name: "dependency", err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable, wantCode: failurecodes.InternalError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, _, _ := tenantEnvironmentUpdateRequest(
				t,
				"env_api",
				`{"expected_version":1,"name":"Next"}`,
				"environment-update-errors",
			)
			coordinator := &tenantEnvironmentUpdateCoordinatorStub{err: tt.err}
			response := httptest.NewRecorder()

			updateEnvironment(response, request, NewMemoryEnvironmentStore(nil), "env_api", coordinator)

			if response.Code != tt.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), tt.wantStatus)
			}
			var envelope ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != tt.wantCode {
				t.Fatalf("code=%q, want %q", envelope.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestTenantEnvironmentUpdateCoordinatorValidatesAuthorityAndMetadata(t *testing.T) {
	authorityPool := &pg.Pool{}
	otherPool := &pg.Pool{}
	base := TenantEnvironmentUpdateRequest{
		Principal: identity.Principal{
			Kind: "user", UserID: "usr_api", SessionID: "ses_api",
		},
		Session: identity.Session{
			ID: "ses_api", UserID: "usr_api", SessionGeneration: 1,
		},
		EnvironmentID: "env_api",
		Update: TenantEnvironmentUpdate{
			ExpectedVersion: 1,
			Name:            stringPointer("Next"),
		},
		Key: "environment-update-validation",
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPatch,
			Path:       v1alphaPrefix + "/environments/env_api",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	}
	coordinator := &PostgresTenantMutationCoordinator{
		Pool: authorityPool,
		Resources: &PostgresResourceStore{
			Pool: authorityPool,
		},
		Idempotency: idempotency.NewPostgresStore(authorityPool),
		Audit:       audit.NewPostgresChain(authorityPool),
		Now:         func() time.Time { return time.Now().UTC() },
	}
	tests := []struct {
		name   string
		mutate func(*TenantEnvironmentUpdateRequest)
	}{
		{name: "missing metadata", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.Name = nil }},
		{name: "invalid slug", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.Slug = stringPointer("Bad_Slug") }},
		{name: "blank name", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.Name = stringPointer(" ") }},
		{name: "long name", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.Name = stringPointer(strings.Repeat("界", 201)) }},
		{name: "invalid tier", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.Tier = stringPointer("critical") }},
		{name: "zero version", mutate: func(r *TenantEnvironmentUpdateRequest) { r.Update.ExpectedVersion = 0 }},
		{name: "wrong route", mutate: func(r *TenantEnvironmentUpdateRequest) {
			r.Fingerprint.Path = v1alphaPrefix + "/environments/env_other"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := base
			tt.mutate(&request)
			if err := coordinator.validateEnvironmentUpdate(request); !errors.Is(err, ErrResourceInvalid) {
				t.Fatalf("error=%v, want ErrResourceInvalid", err)
			}
		})
	}

	coordinator.Resources = &PostgresResourceStore{Pool: otherPool}
	if err := coordinator.validateEnvironmentUpdate(base); err == nil ||
		!strings.Contains(err.Error(), "does not share one postgres authority") {
		t.Fatalf("authority error=%v", err)
	}
}

func tenantEnvironmentUpdateRequest(
	t *testing.T,
	environmentID string,
	body string,
	key string,
) (*http.Request, identity.Principal, identity.Session) {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPatch,
		v1alphaPrefix+"/environments/"+environmentID,
		strings.NewReader(body),
	)
	setTestIfMatchFromExpectedVersion(t, request, body)
	request.Pattern = v1alphaPrefix + "/environments/{environment_id}"
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	principal := identity.Principal{
		Kind:       "user",
		UserID:     "usr_api",
		SessionID:  "ses_api",
		AuthMethod: "password",
	}
	session := identity.Session{
		ID:                "ses_api",
		UserID:            "usr_api",
		SessionGeneration: 1,
	}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	ctx = withProductPrincipal(ctx, productPrincipal{
		Kind:  productPrincipalHuman,
		Human: principal,
	})
	return request.WithContext(ctx), principal, session
}

func stringPointer(value string) *string {
	return &value
}

var _ TenantEnvironmentUpdateCoordinator = (*tenantEnvironmentUpdateCoordinatorStub)(nil)
