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

	"github.com/midnight/midnight/internal/identity"
)

type onboardingStub struct {
	calls   int
	request InvitationOnboardingRequest
	err     error
}

func (s *onboardingStub) OnboardInvitation(_ context.Context, request InvitationOnboardingRequest) (InvitationOnboardingResult, error) {
	s.calls++
	s.request = request
	return InvitationOnboardingResult{UserID: "usr_invited", Email: "new@example.test", Invitation: identity.Invitation{ID: "inv_one", WorkspaceID: "wrk_one", ProjectID: "prj_one"}}, s.err
}

func TestInvitationOnboardingPublicStrictBodyAndNoSecrets(t *testing.T) {
	body := `{"invitation_token":"private-invitation-material","display_name":" New invitee ","password":"a sufficiently private password"}`
	for _, test := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"public", http.MethodPost, invitationOnboardingPath, body, 201},
		{"wrong-method", http.MethodGet, invitationOnboardingPath, "", 405},
		{"query-secret", http.MethodPost, invitationOnboardingPath + "?invitation_token=private-query", body, 400},
		{"forged-role", http.MethodPost, invitationOnboardingPath, strings.TrimSuffix(body, "}") + `,"project_role":"owner"}`, 400},
		{"forged-scope", http.MethodPost, invitationOnboardingPath, strings.TrimSuffix(body, "}") + `,"workspace_id":"wrk_other"}`, 400},
		{"forged-email", http.MethodPost, invitationOnboardingPath, strings.TrimSuffix(body, "}") + `,"email":"someone-else@example.test"}`, 400},
		{"secret-field-name", http.MethodPost, invitationOnboardingPath, strings.TrimSuffix(body, "}") + `,"private-invitation-material":false}`, 400},
		{"short-password", http.MethodPost, invitationOnboardingPath, strings.ReplaceAll(body, "a sufficiently private password", "short"), 400},
		{"oversized", http.MethodPost, invitationOnboardingPath, strings.ReplaceAll(body, "a sufficiently private password", strings.Repeat("x", 5000)), 413},
		{"trailing", http.MethodPost, invitationOnboardingPath, body + `{}`, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &onboardingStub{}
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			invitationOnboardingHandler(stub).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d", response.Code, test.status)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("credential-bearing response may be cached")
			}
			for _, secret := range []string{"private-invitation-material", "a sufficiently private password", "private-query"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatal("credential echoed")
				}
			}
			if test.status == 201 && (stub.calls != 1 || stub.request.DisplayName != "New invitee" || stub.request.RemoteAddr == "") {
				t.Fatal("public request not passed correctly")
			}
			if test.status != 201 && stub.calls != 0 {
				t.Fatal("invalid input reached account creation")
			}
		})
	}
}

func TestInvitationOnboardingErrorsFailClosed(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{identity.ErrInvitationAccountExists, 409, "INVITATION_ACCOUNT_EXISTS"},
		{identity.ErrInvitationRevoked, 409, "RESOURCE_CONFLICT"},
		{identity.ErrInvitationExpired, 409, "RESOURCE_CONFLICT"},
		{identity.ErrInvitationConsumed, 409, "RESOURCE_CONFLICT"},
		{&identity.LoginRateLimitError{RetryAfter: time.Minute}, 429, "AUTH_RATE_LIMITED"},
		{errors.New("private upstream details"), 503, "INTERNAL_ERROR"},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, invitationOnboardingPath, strings.NewReader(`{"invitation_token":"not-echoed","display_name":"Invitee","password":"a long private password"}`))
		invitationOnboardingHandler(&onboardingStub{err: test.err}).ServeHTTP(response, request)
		var problem struct {
			Error struct {
				Code string `json:"code"`
			}
		}
		_ = json.Unmarshal(response.Body.Bytes(), &problem)
		if response.Code != test.status || problem.Error.Code != test.code {
			t.Fatalf("unexpected public error status=%d code=%s", response.Code, problem.Error.Code)
		}
		if strings.Contains(response.Body.String(), "private upstream details") {
			t.Fatal("upstream details exposed")
		}
	}
}
