package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/identity"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
)

type stubGitHubSetupCoordinator struct {
	initiateRequest GitHubSetupInitiateRequest
	callbackRequest GitHubSetupCallbackRequest
	initiateResult  GitHubSetupInitiateResult
	callbackResult  GitHubSetupCallbackResult
	err             error
}

func (s *stubGitHubSetupCoordinator) Initiate(_ context.Context, request GitHubSetupInitiateRequest) (GitHubSetupInitiateResult, error) {
	s.initiateRequest = request
	return s.initiateResult, s.err
}

func (s *stubGitHubSetupCoordinator) Complete(_ context.Context, request GitHubSetupCallbackRequest) (GitHubSetupCallbackResult, error) {
	s.callbackRequest = request
	return s.callbackResult, s.err
}

func TestGitHubSetupCallbackRejectsOwnershipAndDuplicateQueryFields(t *testing.T) {
	for name, target := range map[string]string{
		"untrusted owner": "/api/v1alpha/github/setup/callback?state=signed&code=code&installation_id=42&owner=acme",
		"duplicate state": "/api/v1alpha/github/setup/callback?state=one&state=two&code=code&installation_id=42",
	} {
		t.Run(name, func(t *testing.T) {
			coordinator := &stubGitHubSetupCoordinator{}
			request := httptest.NewRequest(http.MethodGet, target, nil)
			response := httptest.NewRecorder()
			githubSetupCallbackHandler(coordinator).ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if coordinator.callbackRequest.State != "" {
				t.Fatal("invalid callback reached the coordinator")
			}
		})
	}
}

func TestGitHubSetupCallbackRedirectsOnlyAfterVerifiedCompletion(t *testing.T) {
	coordinator := &stubGitHubSetupCoordinator{callbackResult: GitHubSetupCallbackResult{
		WorkspaceID:  "wrk_one",
		Installation: githubapp.Installation{ID: 42, Account: "server-owner"},
	}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/github/setup/callback?state=signed&code=oauth&installation_id=42&setup_action=install",
		nil,
	)
	response := httptest.NewRecorder()
	githubSetupCallbackHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?github_setup=connected" {
		t.Fatalf("callback response = %d %q", response.Code, response.Header().Get("Location"))
	}
	if coordinator.callbackRequest.State != "signed" || coordinator.callbackRequest.OAuthCode != "oauth" || coordinator.callbackRequest.InstallationID != 42 {
		t.Fatalf("callback request = %#v", coordinator.callbackRequest)
	}
	if response.Header().Get("Referrer-Policy") != "no-referrer" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("callback redirect omitted security headers")
	}
}

func TestGitHubSetupInitiateRequiresEmptyBodyAndBoundHumanContext(t *testing.T) {
	expires := time.Date(2026, 8, 30, 12, 10, 0, 0, time.UTC)
	coordinator := &stubGitHubSetupCoordinator{initiateResult: GitHubSetupInitiateResult{
		InstallURL: "https://github.com/apps/midnight/installations/new?state=signed",
		ExpiresAt:  expires,
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/workspaces/wrk_one/github/setup", nil)
	request.SetPathValue("workspace_id", "wrk_one")
	principal := identity.Principal{Kind: "user", UserID: "usr_one", SessionID: "ses_one", AuthMethod: "password+totp"}
	session := identity.Session{ID: "ses_one", UserID: "usr_one"}
	ctx := context.WithValue(request.Context(), identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	githubSetupInitiateHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || coordinator.initiateRequest.WorkspaceID != "wrk_one" {
		t.Fatalf("initiate response = %d %s, request = %#v", response.Code, response.Body.String(), coordinator.initiateRequest)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1alpha/workspaces/wrk_one/github/setup", strings.NewReader(`{"workspace_id":"wrk_other"}`))
	request.SetPathValue("workspace_id", "wrk_one")
	response = httptest.NewRecorder()
	githubSetupInitiateHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("non-empty body status = %d", response.Code)
	}
}
