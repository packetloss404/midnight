// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type recordingTenantGitHubSourceLinks struct {
	repositories []TenantGitHubRepositoryView
	branches     []github.Branch
	link         TenantGitHubLinkView
	mutation     TenantGitHubLinkMutationResult

	listRepositoriesErr error
	listBranchesErr     error
	getErr              error
	putErr              error
	deleteErr           error

	listRepositoriesRequest TenantGitHubProjectRequest
	listBranchesRequest     TenantGitHubProjectRepositoryRequest
	getRequest              TenantGitHubServiceRequest
	putRequest              TenantGitHubLinkMutationRequest
	deleteRequest           TenantGitHubLinkDeleteRequest

	listRepositoriesCalls int
	listBranchesCalls     int
	getCalls              int
	putCalls              int
	deleteCalls           int
}

func (c *recordingTenantGitHubSourceLinks) ListProjectRepositories(
	_ context.Context,
	request TenantGitHubProjectRequest,
) ([]TenantGitHubRepositoryView, error) {
	c.listRepositoriesCalls++
	c.listRepositoriesRequest = request
	return c.repositories, c.listRepositoriesErr
}

func (c *recordingTenantGitHubSourceLinks) ListProjectRepositoryBranches(
	_ context.Context,
	request TenantGitHubProjectRepositoryRequest,
) ([]github.Branch, error) {
	c.listBranchesCalls++
	c.listBranchesRequest = request
	return c.branches, c.listBranchesErr
}

func (c *recordingTenantGitHubSourceLinks) GetServiceLink(
	_ context.Context,
	request TenantGitHubServiceRequest,
) (TenantGitHubLinkView, error) {
	c.getCalls++
	c.getRequest = request
	return c.link, c.getErr
}

func (c *recordingTenantGitHubSourceLinks) PutServiceLink(
	_ context.Context,
	request TenantGitHubLinkMutationRequest,
) (TenantGitHubLinkMutationResult, error) {
	c.putCalls++
	c.putRequest = request
	return c.mutation, c.putErr
}

func (c *recordingTenantGitHubSourceLinks) DeleteServiceLink(
	_ context.Context,
	request TenantGitHubLinkDeleteRequest,
) (TenantGitHubLinkMutationResult, error) {
	c.deleteCalls++
	c.deleteRequest = request
	return c.mutation, c.deleteErr
}

func humanTenantGitHubRequest(
	t *testing.T,
	method string,
	path string,
	body *bytes.Reader,
) *http.Request {
	t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, body)
	}
	principal := identity.Principal{
		Kind:       "user",
		UserID:     "usr_github",
		SessionID:  "ses_github",
		AuthMethod: "password",
	}
	session := identity.Session{
		ID:                principal.SessionID,
		UserID:            principal.UserID,
		SessionGeneration: 7,
	}
	ctx := withProductPrincipal(
		request.Context(),
		productPrincipal{Kind: productPrincipalHuman, Human: principal},
	)
	ctx = context.WithValue(ctx, identityPrincipalContextKey{}, principal)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
	return request.WithContext(ctx)
}

func TestTenantGitHubHandlersRequireHumanSessionContext(t *testing.T) {
	coordinator := &recordingTenantGitHubSourceLinks{}
	request := httptest.NewRequest(
		http.MethodGet,
		v1alphaPrefix+"/services/svc_github/github",
		nil,
	)
	request.SetPathValue("service_id", "svc_github")
	response := httptest.NewRecorder()

	tenantGitHubServiceLinkHandler(coordinator)(response, request)

	if response.Code != http.StatusUnauthorized ||
		response.Header().Get("WWW-Authenticate") != "Bearer" ||
		coordinator.getCalls != 0 {
		t.Fatalf(
			"response=%d authenticate=%q calls=%d body=%s",
			response.Code,
			response.Header().Get("WWW-Authenticate"),
			coordinator.getCalls,
			response.Body.String(),
		)
	}
}

func TestTenantGitHubProjectDiscoveryUsesCollectionEnvelopes(t *testing.T) {
	coordinator := &recordingTenantGitHubSourceLinks{
		repositories: []TenantGitHubRepositoryView{{
			InstallationID: 91,
			RepositoryID:   92,
			Repository:     "midnight/source",
			Private:        true,
			DefaultBranch:  "main",
		}},
		branches: []github.Branch{{Name: "main"}, {Name: "release"}},
	}
	handler := tenantGitHubProjectRepositoriesHandler(coordinator)

	repositoriesRequest := humanTenantGitHubRequest(
		t,
		http.MethodGet,
		v1alphaPrefix+"/projects/prj_github/github/repositories",
		nil,
	)
	repositoriesRequest.SetPathValue("project_id", "prj_github")
	repositoriesResponse := httptest.NewRecorder()
	handler(repositoriesResponse, repositoriesRequest)

	if repositoriesResponse.Code != http.StatusOK ||
		coordinator.listRepositoriesCalls != 1 ||
		coordinator.listRepositoriesRequest.ProjectID != "prj_github" ||
		coordinator.listRepositoriesRequest.Principal.UserID != "usr_github" {
		t.Fatalf(
			"response=%d calls=%d request=%#v body=%s",
			repositoriesResponse.Code,
			coordinator.listRepositoriesCalls,
			coordinator.listRepositoriesRequest,
			repositoriesResponse.Body.String(),
		)
	}
	var repositoriesEnvelope struct {
		Items []TenantGitHubRepositoryView `json:"items"`
	}
	if err := json.Unmarshal(repositoriesResponse.Body.Bytes(), &repositoriesEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(repositoriesEnvelope.Items) != 1 ||
		repositoriesEnvelope.Items[0].RepositoryID != 92 {
		t.Fatalf("repositories envelope=%#v", repositoriesEnvelope)
	}

	branchesRequest := humanTenantGitHubRequest(
		t,
		http.MethodGet,
		v1alphaPrefix+"/projects/prj_github/github/repositories/92/branches",
		nil,
	)
	branchesRequest.SetPathValue("project_id", "prj_github")
	branchesRequest.SetPathValue("repository_id", "92")
	branchesResponse := httptest.NewRecorder()
	handler(branchesResponse, branchesRequest)

	if branchesResponse.Code != http.StatusOK ||
		coordinator.listBranchesCalls != 1 ||
		coordinator.listBranchesRequest.ProjectID != "prj_github" ||
		coordinator.listBranchesRequest.RepositoryID != 92 {
		t.Fatalf(
			"response=%d calls=%d request=%#v body=%s",
			branchesResponse.Code,
			coordinator.listBranchesCalls,
			coordinator.listBranchesRequest,
			branchesResponse.Body.String(),
		)
	}
	var branchesEnvelope struct {
		Items []github.Branch `json:"items"`
	}
	if err := json.Unmarshal(branchesResponse.Body.Bytes(), &branchesEnvelope); err != nil {
		t.Fatal(err)
	}
	if len(branchesEnvelope.Items) != 2 ||
		branchesEnvelope.Items[1].Name != "release" {
		t.Fatalf("branches envelope=%#v", branchesEnvelope)
	}
}

func TestTenantGitHubPutIsStrictAndFingerprinted(t *testing.T) {
	stored := idempotency.StoredResponse{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte("{\"binding_state\":\"bound\"}\n"),
	}
	coordinator := &recordingTenantGitHubSourceLinks{
		mutation: TenantGitHubLinkMutationResult{Response: stored},
	}
	handler := tenantGitHubServiceLinkHandler(coordinator)
	body := []byte(`{"installation_id":31,"repository_id":41,"branch":"main"}`)
	request := humanTenantGitHubRequest(
		t,
		http.MethodPut,
		v1alphaPrefix+"/services/svc_github/github",
		bytes.NewReader(body),
	)
	request.SetPathValue("service_id", "svc_github")
	request.Header.Set("Idempotency-Key", "github-source-put")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusOK ||
		coordinator.putCalls != 1 ||
		coordinator.putRequest.ServiceID != orailtypes.ID("svc_github") ||
		coordinator.putRequest.InstallationID != 31 ||
		coordinator.putRequest.RepositoryID != 41 ||
		coordinator.putRequest.Branch != "main" ||
		coordinator.putRequest.Key != "github-source-put" ||
		coordinator.putRequest.Fingerprint.Method != http.MethodPut ||
		coordinator.putRequest.Fingerprint.Path !=
			v1alphaPrefix+"/services/svc_github/github" ||
		coordinator.putRequest.Fingerprint.BodySHA256 !=
			idempotency.BodyHashSHA256(body) {
		t.Fatalf(
			"response=%d calls=%d request=%#v body=%s",
			response.Code,
			coordinator.putCalls,
			coordinator.putRequest,
			response.Body.String(),
		)
	}

	tests := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{
			name:       "unknown field",
			body:       []byte(`{"installation_id":31,"repository_id":41,"branch":"main","repo":"spoofed/name"}`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "trailing document",
			body:       []byte(`{"installation_id":31,"repository_id":41,"branch":"main"} {}`),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "oversize",
			body:       bytes.Repeat([]byte("x"), int(tenantGitHubLinkBodyLimitBytes)+1),
			wantStatus: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			strictRequest := humanTenantGitHubRequest(
				t,
				http.MethodPut,
				v1alphaPrefix+"/services/svc_github/github",
				bytes.NewReader(test.body),
			)
			strictRequest.SetPathValue("service_id", "svc_github")
			strictRequest.Header.Set("Idempotency-Key", "github-source-"+strings.ReplaceAll(test.name, " ", "-"))
			strictResponse := httptest.NewRecorder()

			handler(strictResponse, strictRequest)

			if strictResponse.Code != test.wantStatus || coordinator.putCalls != 1 {
				t.Fatalf(
					"response=%d calls=%d body=%s",
					strictResponse.Code,
					coordinator.putCalls,
					strictResponse.Body.String(),
				)
			}
		})
	}

	missingKey := humanTenantGitHubRequest(
		t,
		http.MethodPut,
		v1alphaPrefix+"/services/svc_github/github",
		bytes.NewReader(body),
	)
	missingKey.SetPathValue("service_id", "svc_github")
	missingKeyResponse := httptest.NewRecorder()
	handler(missingKeyResponse, missingKey)
	if missingKeyResponse.Code != http.StatusBadRequest || coordinator.putCalls != 1 {
		t.Fatalf(
			"missing key response=%d calls=%d body=%s",
			missingKeyResponse.Code,
			coordinator.putCalls,
			missingKeyResponse.Body.String(),
		)
	}
}

func TestTenantGitHubGetMapsNotFoundAndPermission(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "not found",
			err:        github.ErrSourceLinkNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "permission denied",
			err:        identity.ErrAuthorizationDenied,
			wantStatus: http.StatusForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := &recordingTenantGitHubSourceLinks{getErr: test.err}
			request := humanTenantGitHubRequest(
				t,
				http.MethodGet,
				v1alphaPrefix+"/services/svc_github/github",
				nil,
			)
			request.SetPathValue("service_id", "svc_github")
			response := httptest.NewRecorder()

			tenantGitHubServiceLinkHandler(coordinator)(response, request)

			if response.Code != test.wantStatus || coordinator.getCalls != 1 {
				t.Fatalf(
					"response=%d calls=%d body=%s",
					response.Code,
					coordinator.getCalls,
					response.Body.String(),
				)
			}
		})
	}
}

func TestTenantGitHubDeleteRequiresEmptyBodyAndMarksReplay(t *testing.T) {
	coordinator := &recordingTenantGitHubSourceLinks{
		mutation: TenantGitHubLinkMutationResult{
			Response: idempotency.StoredResponse{
				StatusCode: http.StatusNoContent,
				Header:     http.Header{},
			},
			Replayed: true,
		},
	}
	handler := tenantGitHubServiceLinkHandler(coordinator)
	request := humanTenantGitHubRequest(
		t,
		http.MethodDelete,
		v1alphaPrefix+"/services/svc_github/github",
		nil,
	)
	request.SetPathValue("service_id", "svc_github")
	request.Header.Set("Idempotency-Key", "github-source-delete")
	response := httptest.NewRecorder()

	handler(response, request)

	if response.Code != http.StatusNoContent ||
		response.Header().Get("Idempotent-Replayed") != "true" ||
		coordinator.deleteCalls != 1 ||
		coordinator.deleteRequest.ServiceID != "svc_github" ||
		coordinator.deleteRequest.Key != "github-source-delete" ||
		coordinator.deleteRequest.Fingerprint.Method != http.MethodDelete ||
		coordinator.deleteRequest.Fingerprint.Path !=
			v1alphaPrefix+"/services/svc_github/github" ||
		coordinator.deleteRequest.Fingerprint.BodySHA256 !=
			idempotency.BodyHashSHA256(nil) {
		t.Fatalf(
			"response=%d replay=%q calls=%d request=%#v body=%s",
			response.Code,
			response.Header().Get("Idempotent-Replayed"),
			coordinator.deleteCalls,
			coordinator.deleteRequest,
			response.Body.String(),
		)
	}

	nonEmptyRequest := humanTenantGitHubRequest(
		t,
		http.MethodDelete,
		v1alphaPrefix+"/services/svc_github/github",
		bytes.NewReader([]byte("{}")),
	)
	nonEmptyRequest.SetPathValue("service_id", "svc_github")
	nonEmptyRequest.Header.Set("Idempotency-Key", "github-source-delete-body")
	nonEmptyResponse := httptest.NewRecorder()
	handler(nonEmptyResponse, nonEmptyRequest)
	if nonEmptyResponse.Code != http.StatusBadRequest ||
		coordinator.deleteCalls != 1 {
		t.Fatalf(
			"response=%d calls=%d body=%s",
			nonEmptyResponse.Code,
			coordinator.deleteCalls,
			nonEmptyResponse.Body.String(),
		)
	}
}

func TestTenantGitHubServiceLinkRejectsPOST(t *testing.T) {
	coordinator := &recordingTenantGitHubSourceLinks{}
	request := humanTenantGitHubRequest(
		t,
		http.MethodPost,
		v1alphaPrefix+"/services/svc_github/github",
		bytes.NewReader([]byte("{}")),
	)
	request.SetPathValue("service_id", "svc_github")
	response := httptest.NewRecorder()

	tenantGitHubServiceLinkHandler(coordinator)(response, request)

	if response.Code != http.StatusMethodNotAllowed ||
		response.Header().Get("Allow") != "GET, PUT, DELETE" ||
		coordinator.getCalls != 0 ||
		coordinator.putCalls != 0 ||
		coordinator.deleteCalls != 0 {
		t.Fatalf(
			"response=%d allow=%q calls=%d/%d/%d body=%s",
			response.Code,
			response.Header().Get("Allow"),
			coordinator.getCalls,
			coordinator.putCalls,
			coordinator.deleteCalls,
			response.Body.String(),
		)
	}
}

func TestTenantGitHubReadErrorDoesNotCollapseUnrelatedErrors(t *testing.T) {
	coordinator := &recordingTenantGitHubSourceLinks{
		getErr: errors.New("database unavailable"),
	}
	request := humanTenantGitHubRequest(
		t,
		http.MethodGet,
		v1alphaPrefix+"/services/svc_github/github",
		nil,
	)
	request.SetPathValue("service_id", "svc_github")
	response := httptest.NewRecorder()

	tenantGitHubServiceLinkHandler(coordinator)(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response=%d body=%s", response.Code, response.Body.String())
	}
}
