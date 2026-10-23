// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

// newServiceForDelete creates a project and a service through the public API
// and returns the service plus the strong validator its GET publishes.
func newServiceForDelete(t *testing.T, router http.Handler, slug string) (Service, string) {
	t.Helper()
	projectRR := httptest.NewRecorder()
	projectReq := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/projects",
		strings.NewReader(`{"workspace_id":"wrk_default","slug":"`+slug+`-project"}`))
	projectReq.Header.Set("Idempotency-Key", "project-"+slug)
	router.ServeHTTP(projectRR, projectReq)
	if projectRR.Code != http.StatusCreated {
		t.Fatalf("project create status = %d body = %s", projectRR.Code, projectRR.Body.String())
	}
	var project Project
	if err := json.Unmarshal(projectRR.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}

	serviceRR := httptest.NewRecorder()
	serviceReq := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services",
		strings.NewReader(`{"project_id":"`+string(project.ID)+`","slug":"`+slug+`"}`))
	serviceReq.Header.Set("Idempotency-Key", "service-"+slug)
	router.ServeHTTP(serviceRR, serviceReq)
	if serviceRR.Code != http.StatusCreated {
		t.Fatalf("service create status = %d body = %s", serviceRR.Code, serviceRR.Body.String())
	}
	var service Service
	if err := json.Unmarshal(serviceRR.Body.Bytes(), &service); err != nil {
		t.Fatal(err)
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, v1alphaPrefix+"/services/"+string(service.ID), nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("service get status = %d body = %s", getRR.Code, getRR.Body.String())
	}
	etag := getRR.Header().Get("ETag")
	if etag == "" {
		t.Fatal("GET /services/{id} published no ETag; conditional delete has no validator to quote")
	}
	return service, etag
}

func deleteServiceRequest(id, etag, key string) *http.Request {
	request := httptest.NewRequest(http.MethodDelete, v1alphaPrefix+"/services/"+id, nil)
	if etag != "" {
		request.Header.Set("If-Match", etag)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	return request
}

func serviceDeleteProblemCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("problem body %q: %v", rr.Body.String(), err)
	}
	return body.Error.Code
}

func TestServiceGetPublishesStrongResourceVersionETag(t *testing.T) {
	router := NewRouter()
	service, etag := newServiceForDelete(t, router, "etagged")
	if service.ResourceVersion != 1 {
		t.Fatalf("resource_version = %d, want 1 on a fresh service", service.ResourceVersion)
	}
	if etag != `"1"` {
		t.Fatalf("ETag = %q, want \"1\"", etag)
	}
}

func TestServiceDeleteRemovesServiceAndReportsReceipt(t *testing.T) {
	router := NewRouter()
	service, etag := newServiceForDelete(t, router, "removable")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deleteServiceRequest(string(service.ID), etag, "delete-removable"))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", rr.Code, rr.Body.String())
	}
	var receipt ServiceDeletionReceipt
	if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ServiceID != string(service.ID) || receipt.Slug != "removable" {
		t.Fatalf("receipt = %#v, want the deleted service", receipt)
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, v1alphaPrefix+"/services/"+string(service.ID), nil))
	if getRR.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", getRR.Code)
	}
}

func TestServiceDeleteFreesTheSlugForReuse(t *testing.T) {
	// The failure this feature exists for: a failed template run leaves an
	// orphan service, and the retry must be able to use the same slug.
	router := NewRouter()
	service, etag := newServiceForDelete(t, router, "orphan")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deleteServiceRequest(string(service.ID), etag, "delete-orphan"))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", rr.Code, rr.Body.String())
	}

	recreate := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, v1alphaPrefix+"/services",
		strings.NewReader(`{"project_id":"`+string(service.ProjectID)+`","slug":"orphan"}`))
	request.Header.Set("Idempotency-Key", "service-orphan-again")
	router.ServeHTTP(recreate, request)
	if recreate.Code != http.StatusCreated {
		t.Fatalf("recreate status = %d body = %s", recreate.Code, recreate.Body.String())
	}
}

func TestServiceDeleteRequiresIfMatch(t *testing.T) {
	router := NewRouter()
	service, _ := newServiceForDelete(t, router, "no-precondition")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deleteServiceRequest(string(service.ID), "", "delete-no-precondition"))
	if rr.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want 428 body = %s", rr.Code, rr.Body.String())
	}
	if got := serviceDeleteProblemCode(t, rr); got != string(failurecodes.PreconditionRequired) {
		t.Fatalf("code = %q, want %q", got, failurecodes.PreconditionRequired)
	}
}

func TestServiceDeleteRejectsStaleAndWeakValidators(t *testing.T) {
	router := NewRouter()
	service, _ := newServiceForDelete(t, router, "stale")

	for name, validator := range map[string]string{
		"stale":    `"7"`,
		"weak":     `W/"1"`,
		"wildcard": `*`,
		"list":     `"1","2"`,
	} {
		t.Run(name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, deleteServiceRequest(string(service.ID), validator, "delete-stale-"+name))
			if rr.Code != http.StatusPreconditionFailed {
				t.Fatalf("status = %d, want 412 body = %s", rr.Code, rr.Body.String())
			}
			if got := serviceDeleteProblemCode(t, rr); got != string(failurecodes.PreconditionFailed) {
				t.Fatalf("code = %q, want %q", got, failurecodes.PreconditionFailed)
			}
		})
	}

	getRR := httptest.NewRecorder()
	router.ServeHTTP(getRR, httptest.NewRequest(http.MethodGet, v1alphaPrefix+"/services/"+string(service.ID), nil))
	if getRR.Code != http.StatusOK {
		t.Fatalf("service must survive every rejected precondition, got %d", getRR.Code)
	}
}

func TestServiceDeleteRequiresIdempotencyKey(t *testing.T) {
	router := NewRouter()
	service, etag := newServiceForDelete(t, router, "no-key")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deleteServiceRequest(string(service.ID), etag, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body = %s", rr.Code, rr.Body.String())
	}
	if got := serviceDeleteProblemCode(t, rr); got != string(failurecodes.IdempotencyKeyRequired) {
		t.Fatalf("code = %q, want %q", got, failurecodes.IdempotencyKeyRequired)
	}
}

func TestServiceDeleteReplaysStoredResponseForTheSameKey(t *testing.T) {
	router := NewRouter()
	service, etag := newServiceForDelete(t, router, "replayed")

	first := httptest.NewRecorder()
	router.ServeHTTP(first, deleteServiceRequest(string(service.ID), etag, "delete-replayed"))
	if first.Code != http.StatusOK {
		t.Fatalf("first delete status = %d body = %s", first.Code, first.Body.String())
	}

	second := httptest.NewRecorder()
	router.ServeHTTP(second, deleteServiceRequest(string(service.ID), etag, "delete-replayed"))
	if second.Code != http.StatusOK {
		t.Fatalf("replay status = %d body = %s", second.Code, second.Body.String())
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay did not set Idempotent-Replayed")
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay body = %s, want the stored %s", second.Body.String(), first.Body.String())
	}
}

func TestServiceDeleteMissingServiceIsNotFound(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, deleteServiceRequest("svc_absent", `"1"`, "delete-absent"))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body = %s", rr.Code, rr.Body.String())
	}
	if got := serviceDeleteProblemCode(t, rr); got != string(failurecodes.ResourceNotFound) {
		t.Fatalf("code = %q, want %q", got, failurecodes.ResourceNotFound)
	}
}

func TestServiceItemRouteAdvertisesDeleteInAllowHeader(t *testing.T) {
	router := NewRouter()
	service, _ := newServiceForDelete(t, router, "allow-header")

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPut, v1alphaPrefix+"/services/"+string(service.ID), nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 body = %s", rr.Code, rr.Body.String())
	}
	if allow := rr.Header().Get("Allow"); allow != "GET, DELETE" {
		t.Fatalf("Allow = %q, want \"GET, DELETE\"", allow)
	}
}

func TestMemoryResourceStoreDeleteServiceCAS(t *testing.T) {
	store := NewMemoryResourceStore()
	ctx := context.Background()
	project, err := store.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "p", DisplayName: "P"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := store.CreateService(ctx, Service{ProjectID: project.ID, Slug: "s", DisplayName: "S"})
	if err != nil {
		t.Fatal(err)
	}
	if service.ResourceVersion != 1 {
		t.Fatalf("resource_version = %d, want 1", service.ResourceVersion)
	}
	if _, err := store.DeleteServiceCAS(ctx, service.ID, 4); !errors.Is(err, ErrResourcePreconditionFailed) {
		t.Fatalf("stale delete err = %v, want ErrResourcePreconditionFailed", err)
	}
	if _, err := store.DeleteServiceCAS(ctx, "svc_missing", 1); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing delete err = %v, want ErrResourceNotFound", err)
	}
	receipt, err := store.DeleteServiceCAS(ctx, service.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Slug != "s" || receipt.ProjectID != string(project.ID) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if _, err := store.GetService(ctx, service.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("get after delete err = %v, want ErrResourceNotFound", err)
	}
	// The list index must stay consistent after the removal.
	services, err := store.ListServices(ctx, ResourceListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("services = %#v, want empty", services)
	}
	if _, err := store.CreateService(ctx, Service{ProjectID: project.ID, Slug: "s", DisplayName: "S"}); err != nil {
		t.Fatalf("slug reuse after delete: %v", err)
	}
}

func TestServiceDeleteRefusalTaxonomy(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   failurecodes.Code
		wantStored bool
	}{
		{
			name:       "running deployment",
			err:        fmt.Errorf("%w: svc still up", ErrServiceDeleteRunning),
			wantStatus: http.StatusConflict,
			wantCode:   failurecodes.ServiceDeleteRunning,
			wantStored: true,
		},
		{
			name:       "blocking dependent",
			err:        fmt.Errorf("%w: volume attached", ErrServiceDeleteBlocked),
			wantStatus: http.StatusConflict,
			wantCode:   failurecodes.ServiceDeleteBlocked,
			wantStored: true,
		},
		{
			name:       "stale validator",
			err:        ErrResourcePreconditionFailed,
			wantStatus: http.StatusPreconditionFailed,
			wantCode:   failurecodes.PreconditionFailed,
			wantStored: true,
		},
		{
			name:       "not found is not a stored refusal",
			err:        ErrResourceNotFound,
			wantStored: false,
		},
		{
			name:       "internal fault is not a stored refusal",
			err:        errors.New("connection reset"),
			wantStored: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, code, stored := serviceDeleteRefusal(testCase.err)
			if stored != testCase.wantStored {
				t.Fatalf("stored = %v, want %v", stored, testCase.wantStored)
			}
			if !stored {
				return
			}
			if status != testCase.wantStatus || code != testCase.wantCode {
				t.Fatalf("(%d, %q), want (%d, %q)", status, code, testCase.wantStatus, testCase.wantCode)
			}
			if !failurecodes.IsPermittedProblemStatus(code, status) {
				t.Fatalf("%s is not permitted at %d", code, status)
			}
		})
	}
}

func TestServiceDeleteStoreErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   failurecodes.Code
	}{
		{"not found", ErrResourceNotFound, http.StatusNotFound, failurecodes.ResourceNotFound},
		{"stale", ErrResourcePreconditionFailed, http.StatusPreconditionFailed, failurecodes.PreconditionFailed},
		{"running", fmt.Errorf("%w: up", ErrServiceDeleteRunning), http.StatusConflict, failurecodes.ServiceDeleteRunning},
		{"blocked", fmt.Errorf("%w: attached", ErrServiceDeleteBlocked), http.StatusConflict, failurecodes.ServiceDeleteBlocked},
		{"unsupported store", errServiceDeleteUnsupportedStore, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable},
		{"fault", errors.New("boom"), http.StatusInternalServerError, failurecodes.InternalError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodDelete, v1alphaPrefix+"/services/svc_x", nil)
			if !writeServiceDeleteStoreError(rr, request, testCase.err, "svc_x") {
				t.Fatal("expected a response to be written")
			}
			if rr.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, testCase.wantStatus)
			}
			if got := serviceDeleteProblemCode(t, rr); got != string(testCase.wantCode) {
				t.Fatalf("code = %q, want %q", got, testCase.wantCode)
			}
		})
	}
	if writeServiceDeleteStoreError(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/", nil), nil, "svc_x") {
		t.Fatal("a nil error must not write a response")
	}
}

func TestServiceDeleteIdempotencyFingerprintIsPerServiceAndPerVersion(t *testing.T) {
	if serviceDeleteIdempotencyPath("svc_a") == serviceDeleteIdempotencyPath("svc_b") {
		t.Fatal("two services share one idempotency path; a key could replay across resources")
	}
	if !strings.HasSuffix(serviceDeleteIdempotencyPath("svc_a"), "/services/svc_a") {
		t.Fatalf("path = %q, want a concrete service path", serviceDeleteIdempotencyPath("svc_a"))
	}
	if serviceDeleteFingerprintBody(1) == serviceDeleteFingerprintBody(2) {
		t.Fatal("two preconditions share one body hash; a stale retry would replay silently")
	}
}

func TestServiceDeleteIsRegisteredInTheRuntimeRouteCatalog(t *testing.T) {
	want := RuntimeRouteOperation{
		Method:  http.MethodDelete,
		Path:    v1alphaPrefix + "/services/{id}",
		Pattern: v1alphaPrefix + "/services/{service_id}",
	}
	for _, operation := range canonicalRuntimeRESTRoutes {
		if operation == want {
			return
		}
	}
	t.Fatalf("%s %s is missing from canonicalRuntimeRESTRoutes", want.Method, want.Path)
}

func TestServiceDeleteCapabilityIsNarrowerThanServiceWrite(t *testing.T) {
	// Deleting must not fall out of "can edit a service". A workspace member
	// and a project editor can both write services; neither may delete one.
	cases := []struct {
		name          string
		workspaceRole identity.WorkspaceRole
		projectRole   identity.ProjectRole
		wantAllowed   bool
	}{
		{"workspace admin", identity.WorkspaceRoleAdmin, "", true},
		{"project owner", "", identity.ProjectRoleOwner, true},
		{"workspace member", identity.WorkspaceRoleMember, "", false},
		{"project editor", "", identity.ProjectRoleEditor, false},
		{"project viewer", "", identity.ProjectRoleViewer, false},
		{"deployer", identity.WorkspaceRoleDeployer, "", false},
		{"no grant", "", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := identity.AccessRequest{
				WorkspaceRole: testCase.workspaceRole,
				ProjectRole:   testCase.projectRole,
				Capability:    identity.CapabilityServiceDelete,
			}
			if got := identity.EvaluateAccess(request).Allowed; got != testCase.wantAllowed {
				t.Fatalf("service.delete allowed = %v, want %v", got, testCase.wantAllowed)
			}
			if testCase.wantAllowed {
				return
			}
			// Sanity: the roles that are denied delete but can still write.
			write := request
			write.Capability = identity.CapabilityServiceWrite
			if testCase.workspaceRole == identity.WorkspaceRoleMember || testCase.projectRole == identity.ProjectRoleEditor {
				if !identity.EvaluateAccess(write).Allowed {
					t.Fatal("expected this role to retain service.write")
				}
			}
		})
	}
}

func TestServiceDeleteIsRefusedInsideARestrictedEnvironment(t *testing.T) {
	decision := identity.EvaluateAccess(identity.AccessRequest{
		ProjectRole:           identity.ProjectRoleOwner,
		Capability:            identity.CapabilityServiceDelete,
		EnvironmentRestricted: true,
	})
	if decision.Allowed {
		t.Fatalf("restricted environment allowed service.delete: %+v", decision)
	}
}
