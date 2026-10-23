// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/integrations/github"
)

type recordingGitHubGrantRecovery struct {
	view   GitHubGrantRecoveryView
	result GitHubGrantRecoveryResult
	err    error

	getWorkspaceID    string
	getInstallationID int64
	putRequest        GitHubGrantRecoveryRequest
	deleteRequest     GitHubGrantRecoveryRequest
	getCalls          int
	putCalls          int
	deleteCalls       int
}

func (c *recordingGitHubGrantRecovery) Get(
	_ context.Context,
	workspaceID string,
	installationID int64,
) (GitHubGrantRecoveryView, error) {
	c.getCalls++
	c.getWorkspaceID = workspaceID
	c.getInstallationID = installationID
	return c.view, c.err
}

func (c *recordingGitHubGrantRecovery) Put(
	_ context.Context,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, error) {
	c.putCalls++
	c.putRequest = request
	return c.result, c.err
}

func (c *recordingGitHubGrantRecovery) Delete(
	_ context.Context,
	request GitHubGrantRecoveryRequest,
) (GitHubGrantRecoveryResult, error) {
	c.deleteCalls++
	c.deleteRequest = request
	return c.result, c.err
}

func TestGitHubGrantRecoveryHandlerStrictMutationContract(t *testing.T) {
	const path = "/api/v1alpha/github/installations/4201/workspace-grant"
	validResponse := idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte("{\"workspace_id\":\"wrk_acme\"}\n"),
	}

	tests := []struct {
		name       string
		method     string
		body       string
		key        string
		wantStatus int
	}{
		{
			name:       "missing idempotency key",
			method:     http.MethodPut,
			body:       `{"workspace_id":"wrk_acme","repository_ids":[5101]}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty body",
			method:     http.MethodPut,
			key:        "grant-empty",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "null body",
			method:     http.MethodPut,
			body:       `null`,
			key:        "grant-null",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown metadata field",
			method:     http.MethodPut,
			body:       `{"workspace_id":"wrk_acme","repository_ids":[5101],"repo":"spoofed/name"}`,
			key:        "grant-spoofed",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple json documents",
			method:     http.MethodPut,
			body:       `{"workspace_id":"wrk_acme","repository_ids":[5101]} {}`,
			key:        "grant-trailing",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "delete rejects repository ids",
			method:     http.MethodDelete,
			body:       `{"workspace_id":"wrk_acme","repository_ids":[5101]}`,
			key:        "grant-delete-repositories",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "put accepts only workspace and repository ids",
			method:     http.MethodPut,
			body:       `{"workspace_id":"  wrk_acme  ","repository_ids":[5101,5102]}`,
			key:        "grant-put",
			wantStatus: http.StatusOK,
		},
		{
			name:       "delete accepts workspace only",
			method:     http.MethodDelete,
			body:       `{"workspace_id":"wrk_acme"}`,
			key:        "grant-delete",
			wantStatus: http.StatusOK,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingGitHubGrantRecovery{
				result: GitHubGrantRecoveryResult{Response: validResponse},
			}
			request := httptest.NewRequest(test.method, path, strings.NewReader(test.body))
			request.SetPathValue("installation_id", "4201")
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()

			githubGrantRecoveryHandler(coordinator).ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if test.wantStatus != http.StatusOK {
				if coordinator.putCalls != 0 || coordinator.deleteCalls != 0 {
					t.Fatalf("invalid request reached coordinator: put=%d delete=%d", coordinator.putCalls, coordinator.deleteCalls)
				}
				return
			}
			if test.method == http.MethodPut {
				if coordinator.putCalls != 1 {
					t.Fatalf("put calls = %d, want 1", coordinator.putCalls)
				}
				got := coordinator.putRequest
				if got.WorkspaceID != "wrk_acme" ||
					got.InstallationID != 4201 ||
					len(got.RepositoryIDs) != 2 ||
					got.RepositoryIDs[0] != 5101 ||
					got.RepositoryIDs[1] != 5102 {
					t.Fatalf("put request = %#v", got)
				}
				if got.Key != idempotency.Key(test.key) ||
					got.Fingerprint.Method != http.MethodPut ||
					got.Fingerprint.Path != path ||
					got.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256([]byte(test.body)) {
					t.Fatalf("put idempotency request = %#v", got)
				}
			} else if coordinator.deleteCalls != 1 {
				t.Fatalf("delete calls = %d, want 1", coordinator.deleteCalls)
			}
		})
	}
}

func TestGitHubGrantRecoveryHandlerBodyLimitAndReplay(t *testing.T) {
	const path = "/api/v1alpha/github/installations/4201/workspace-grant"
	coordinator := &recordingGitHubGrantRecovery{
		result: GitHubGrantRecoveryResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       []byte("{}\n"),
			},
			Replayed: true,
		},
	}
	oversized := `{"workspace_id":"` + strings.Repeat("w", int(githubGrantRecoveryBodyLimit)) + `","repository_ids":[5101]}`
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(oversized))
	request.SetPathValue("installation_id", "4201")
	request.Header.Set("Idempotency-Key", "oversized")
	response := httptest.NewRecorder()
	githubGrantRecoveryHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d body=%s", response.Code, response.Body.String())
	}
	if coordinator.putCalls != 0 {
		t.Fatalf("oversized request reached coordinator")
	}

	body := []byte(`{"workspace_id":"wrk_acme","repository_ids":[5101]}`)
	request = httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	request.SetPathValue("installation_id", "4201")
	request.Header.Set("Idempotency-Key", "replay")
	response = httptest.NewRecorder()
	githubGrantRecoveryHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestGitHubGrantRecoveryOperatorRouteProtection(t *testing.T) {
	const (
		token = "operator-recovery-token"
		path  = "/api/v1alpha/github/installations/4201/workspace-grant?workspace_id=wrk_acme"
	)
	coordinator := &recordingGitHubGrantRecovery{
		view: GitHubGrantRecoveryView{
			WorkspaceID:  "wrk_acme",
			Installation: github.Installation{ID: 4201, Account: "acme"},
		},
	}
	router := NewRouterWithDataStack(DataStack{
		APIBearerToken:      token,
		GitHubGrantRecovery: coordinator,
	})

	for _, test := range []struct {
		name          string
		authorization string
		wantStatus    int
		wantCalls     int
	}{
		{name: "missing operator credential", wantStatus: http.StatusUnauthorized},
		{name: "wrong operator credential", authorization: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "operator credential", authorization: "Bearer " + token, wantStatus: http.StatusOK, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator.getCalls = 0
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if coordinator.getCalls != test.wantCalls {
				t.Fatalf("coordinator calls=%d, want %d", coordinator.getCalls, test.wantCalls)
			}
		})
	}
}

func TestGitHubGrantRecoveryHandlerGetIsWorkspaceScoped(t *testing.T) {
	coordinator := &recordingGitHubGrantRecovery{
		view: GitHubGrantRecoveryView{
			WorkspaceID:  "wrk_acme",
			Installation: github.Installation{ID: 4201, Account: "acme"},
		},
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1alpha/github/installations/4201/workspace-grant?workspace_id=%20wrk_acme%20",
		nil,
	)
	request.SetPathValue("installation_id", "4201")
	response := httptest.NewRecorder()
	githubGrantRecoveryHandler(coordinator).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if coordinator.getCalls != 1 ||
		coordinator.getWorkspaceID != "wrk_acme" ||
		coordinator.getInstallationID != 4201 {
		t.Fatalf("get call = workspace %q installation %d calls %d", coordinator.getWorkspaceID, coordinator.getInstallationID, coordinator.getCalls)
	}
	var view GitHubGrantRecoveryView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if view.WorkspaceID != "wrk_acme" || view.Installation.ID != 4201 {
		t.Fatalf("view = %#v", view)
	}
}
