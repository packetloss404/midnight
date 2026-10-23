package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

func TestIdentityAccessRoutesEnforceWorkspaceAndProjectRoles(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	users := []identity.UserRecord{
		{ID: "usr_alice", Email: "alice@example.com", DisplayName: "Alice", PasswordHash: passwordHash, SessionGeneration: 1},
		{ID: "usr_bob", Email: "bob@example.com", DisplayName: "Bob", PasswordHash: passwordHash, SessionGeneration: 1},
		{ID: "usr_carol", Email: "carol@example.com", DisplayName: "Carol", PasswordHash: passwordHash, SessionGeneration: 1},
		{ID: "usr_dave", Email: "dave@example.com", DisplayName: "Dave", PasswordHash: passwordHash, SessionGeneration: 1},
	}
	sessionStore := identity.NewMemoryStore(users...)
	counter := byte(0)
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store: sessionStore, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now },
		Random: func(dst []byte) error {
			counter++
			for i := range dst {
				dst[i] = counter
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted := now
	access := identity.NewMemoryAccessStore()
	access.SeedWorkspace(identity.Workspace{ID: "wrk_a", Slug: "a", DisplayName: "A"}, []identity.WorkspaceMember{
		{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_alice", Email: "alice@example.com"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted},
		{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_bob", Email: "bob@example.com"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted},
	}...)
	access.SeedWorkspace(identity.Workspace{ID: "wrk_b", Slug: "b", DisplayName: "B"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_b", User: identity.User{ID: "usr_carol", Email: "carol@example.com"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted,
	})
	access.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_alice"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_bob"}, Role: identity.ProjectRoleEditor, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_carol"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_dave"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted},
	)
	access.SeedProject("prj_b", "wrk_b",
		identity.ProjectMember{ProjectID: "prj_b", User: identity.User{ID: "usr_carol"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
	)
	chain := audit.NewMemoryChain()
	handler := NewRouterWithDataStack(DataStack{
		IdentitySessions: manager,
		IdentityAccess:   access,
		AuditChain:       chain,
		Clock:            func() time.Time { return now },
	})
	alice := loginIdentityForTest(t, manager, "alice@example.com")
	bob := loginIdentityForTest(t, manager, "bob@example.com")

	workspaces := authenticatedIdentityRequest(handler, http.MethodGet, identityWorkspacesPath, alice)
	if workspaces.Code != http.StatusOK || !strings.Contains(workspaces.Body.String(), `"id":"wrk_a"`) || strings.Contains(workspaces.Body.String(), `"id":"wrk_b"`) {
		t.Fatalf("workspace list status=%d body=%s", workspaces.Code, workspaces.Body.String())
	}
	members := authenticatedIdentityRequest(handler, http.MethodGet, identityWorkspacesPath+"/wrk_a/members", alice)
	if members.Code != http.StatusOK || !strings.Contains(members.Body.String(), `"usr_bob"`) {
		t.Fatalf("workspace members status=%d body=%s", members.Code, members.Body.String())
	}
	denied := authenticatedIdentityRequest(handler, http.MethodGet, identityWorkspacesPath+"/wrk_a/members", bob)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("member workspace admin route status=%d body=%s", denied.Code, denied.Body.String())
	}
	crossTenant := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects/prj_b/members", alice)
	if crossTenant.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant project status=%d body=%s", crossTenant.Code, crossTenant.Body.String())
	}

	promote := authenticatedIdentityBodyRequest(handler, http.MethodPut, v1alphaPrefix+"/projects/prj_a/members/usr_dave", bob, `{"role":"owner"}`)
	if promote.Code != http.StatusForbidden {
		t.Fatalf("editor owner grant status=%d body=%s", promote.Code, promote.Body.String())
	}
	demoteOwner := authenticatedIdentityBodyRequest(handler, http.MethodPut, v1alphaPrefix+"/projects/prj_a/members/usr_carol", bob, `{"role":"editor"}`)
	if demoteOwner.Code != http.StatusForbidden {
		t.Fatalf("editor owner demotion status=%d body=%s", demoteOwner.Code, demoteOwner.Body.String())
	}
	editor := authenticatedIdentityBodyRequest(handler, http.MethodPut, v1alphaPrefix+"/projects/prj_a/members/usr_dave", bob, `{"role":"editor"}`)
	if editor.Code != http.StatusOK || !strings.Contains(editor.Body.String(), `"role":"editor"`) {
		t.Fatalf("editor role change status=%d body=%s", editor.Code, editor.Body.String())
	}
	unknown := authenticatedIdentityBodyRequest(handler, http.MethodPut, v1alphaPrefix+"/projects/prj_a/members/usr_unknown", alice, `{"role":"viewer"}`)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown membership upsert status=%d body=%s", unknown.Code, unknown.Body.String())
	}
	rows, err := chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("membership audit rows=%d, want 4: %#v", len(rows), rows)
	}
	var allowedCount, deniedCount int
	for _, row := range rows {
		if !strings.HasPrefix(row.Action, "identity.project-member.") || row.ActorID == "" {
			t.Fatalf("membership audit row = %#v", row)
		}
		if row.PolicyDecision == "allowed" {
			allowedCount++
		}
		if row.PolicyDecision == "denied" {
			deniedCount++
		}
	}
	if allowedCount != 1 || deniedCount != 3 {
		t.Fatalf("membership decision counts allowed=%d denied=%d rows=%#v", allowedCount, deniedCount, rows)
	}
}

func TestMemoryAccessStoreProtectsLastAdminAndOwner(t *testing.T) {
	now := time.Now().UTC()
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_a", User: identity.User{ID: "usr_a"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted,
	})
	store.SeedProject("prj_a", "wrk_a", identity.ProjectMember{
		ProjectID: "prj_a", User: identity.User{ID: "usr_a"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted,
	})
	if err := store.RemoveWorkspaceMember(t.Context(), "wrk_a", "usr_a", "usr_a", now); err != identity.ErrLastWorkspaceAdmin {
		t.Fatalf("remove last admin error = %v", err)
	}
	if err := store.RemoveProjectMember(t.Context(), "prj_a", "usr_a", "usr_a", now); err != identity.ErrLastProjectOwner {
		t.Fatalf("remove last owner error = %v", err)
	}
}

func TestMemoryAccessStoreWorkspaceRemovalPreservesEveryProjectOwner(t *testing.T) {
	now := time.Now().UTC()
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_actor"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_target"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted},
	)
	store.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_target"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
	)

	if err := store.RemoveWorkspaceMember(t.Context(), "wrk_a", "usr_target", "usr_actor", now); err != identity.ErrLastProjectOwner {
		t.Fatalf("remove workspace member owning project error = %v", err)
	}
	members, err := store.ListWorkspaceMembers(t.Context(), "wrk_a")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("workspace membership changed after rejected removal: %#v", members)
	}

	store.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_target"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_actor"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
	)
	if err := store.RemoveWorkspaceMember(t.Context(), "wrk_a", "usr_target", "usr_actor", now); err != nil {
		t.Fatalf("remove workspace member with alternate owner: %v", err)
	}
}

func TestMemoryAccessStoreReauthorizesMembershipMutations(t *testing.T) {
	now := time.Now().UTC()
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_root"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_actor"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_target"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted},
	)
	store.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_root"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_actor"}, Role: identity.ProjectRoleEditor, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_target"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted},
	)
	if _, err := store.PutWorkspaceMember(t.Context(), "wrk_a", "usr_actor", identity.WorkspaceRoleMember, "usr_root", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWorkspaceMember(t.Context(), "wrk_a", "usr_target", identity.WorkspaceRoleViewer, "usr_actor", now); err != identity.ErrAuthorizationDenied {
		t.Fatalf("demoted actor workspace mutation error = %v", err)
	}
	if _, err := store.PutProjectMember(t.Context(), "prj_a", "usr_root", identity.ProjectRoleEditor, "usr_actor", now); err != identity.ErrAuthorizationDenied {
		t.Fatalf("editor owner mutation error = %v", err)
	}
}

func TestIdentityMemberItemsUseStrongETagAndAtomicCAS(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_admin"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted, Version: 1},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_target"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted, Version: 1},
	)
	store.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_admin"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted, Version: 1},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_target"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted, Version: 1},
	)

	workspace, err := store.GetWorkspaceMember(t.Context(), "wrk_a", "usr_target")
	if err != nil || workspace.Version != 1 {
		t.Fatalf("workspace member = %#v, err=%v", workspace, err)
	}
	updated, err := store.PutWorkspaceMemberCAS(t.Context(), "wrk_a", "usr_target", identity.WorkspaceRoleViewer, "usr_admin", 1, now.Add(time.Second))
	if err != nil || updated.Version != 2 {
		t.Fatalf("workspace update = %#v, err=%v", updated, err)
	}
	if _, err := store.PutWorkspaceMemberCAS(t.Context(), "wrk_a", "usr_target", identity.WorkspaceRoleMember, "usr_admin", 1, now.Add(2*time.Second)); !errors.Is(err, identity.ErrMembershipVersionConflict) {
		t.Fatalf("stale workspace update error = %v", err)
	}

	project, err := store.GetProjectMember(t.Context(), "prj_a", "usr_target")
	if err != nil || project.Version != 1 {
		t.Fatalf("project member = %#v, err=%v", project, err)
	}
	if err := store.RemoveProjectMemberCAS(t.Context(), "prj_a", "usr_target", "usr_admin", 2, now); !errors.Is(err, identity.ErrMembershipVersionConflict) {
		t.Fatalf("stale project delete error = %v", err)
	}
	if err := store.RemoveProjectMemberCAS(t.Context(), "prj_a", "usr_target", "usr_admin", 1, now); err != nil {
		t.Fatalf("project delete: %v", err)
	}
}

func TestMemoryMembershipCASHasExactlyOneWinner(t *testing.T) {
	now := time.Now().UTC()
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_admin"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted, Version: 1},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_target"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted, Version: 1},
	)
	errs := make(chan error, 2)
	for _, role := range []identity.WorkspaceRole{identity.WorkspaceRoleViewer, identity.WorkspaceRoleDeployer} {
		go func(role identity.WorkspaceRole) {
			_, err := store.PutWorkspaceMemberCAS(context.Background(), "wrk_a", "usr_target", role, "usr_admin", 1, now)
			errs <- err
		}(role)
	}
	var successes, stale int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			successes++
		case errors.Is(err, identity.ErrMembershipVersionConflict):
			stale++
		default:
			t.Fatalf("unexpected CAS error: %v", err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("successes/stale = %d/%d", successes, stale)
	}
}

func TestIdentityMembershipItemHTTPConditionalContract(t *testing.T) {
	now := time.Date(2026, 8, 13, 13, 0, 0, 0, time.UTC)
	accepted := now
	store := identity.NewMemoryAccessStore()
	store.SeedWorkspace(identity.Workspace{ID: "wrk_a"},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_admin"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted, Version: 1},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_workspace"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted, Version: 1},
		identity.WorkspaceMember{WorkspaceID: "wrk_a", User: identity.User{ID: "usr_project"}, Role: identity.WorkspaceRoleMember, AcceptedAt: &accepted, Version: 1},
	)
	store.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_admin"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted, Version: 1},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_project"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted, Version: 1},
	)
	principal := identity.Principal{Kind: "user", UserID: "usr_admin"}

	workspaceRequest := func(method, body, ifMatch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, v1alphaPrefix+"/workspaces/wrk_a/members/usr_workspace", strings.NewReader(body))
		request.SetPathValue("workspace_id", "wrk_a")
		request.SetPathValue("user_id", "usr_workspace")
		request = request.WithContext(context.WithValue(request.Context(), identityPrincipalContextKey{}, principal))
		if ifMatch != "" {
			request.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		mutateIdentityWorkspaceMember(response, request, store, func() time.Time { return now }, nil)
		return response
	}
	if response := workspaceRequest(http.MethodGet, "", ""); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("workspace GET = %d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	assertConditionalProblem(t, workspaceRequest(http.MethodPut, `{"role":"viewer"}`, ""), http.StatusPreconditionRequired, failurecodes.PreconditionRequired)
	assertConditionalProblem(t, workspaceRequest(http.MethodPut, `{"role":"viewer"}`, `"2"`), http.StatusPreconditionFailed, failurecodes.PreconditionFailed)
	if response := workspaceRequest(http.MethodPut, `{"role":"viewer"}`, `"1"`); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("workspace PUT = %d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	assertConditionalProblem(t, workspaceRequest(http.MethodDelete, "", `"1"`), http.StatusPreconditionFailed, failurecodes.PreconditionFailed)
	if response := workspaceRequest(http.MethodDelete, "", `"2"`); response.Code != http.StatusNoContent {
		t.Fatalf("workspace DELETE = %d body=%s", response.Code, response.Body.String())
	}

	projectRequest := func(method, body, ifMatch string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, v1alphaPrefix+"/projects/prj_a/members/usr_project", strings.NewReader(body))
		request.SetPathValue("project_id", "prj_a")
		request.SetPathValue("user_id", "usr_project")
		request = request.WithContext(context.WithValue(request.Context(), identityPrincipalContextKey{}, principal))
		if ifMatch != "" {
			request.Header.Set("If-Match", ifMatch)
		}
		response := httptest.NewRecorder()
		mutateIdentityProjectMember(response, request, store, func() time.Time { return now }, nil)
		return response
	}
	if response := projectRequest(http.MethodGet, "", ""); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("project GET = %d ETag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	assertConditionalProblem(t, projectRequest(http.MethodPut, `{"role":"editor"}`, ""), http.StatusPreconditionRequired, failurecodes.PreconditionRequired)
	assertConditionalProblem(t, projectRequest(http.MethodDelete, "", `"2"`), http.StatusPreconditionFailed, failurecodes.PreconditionFailed)
	if response := projectRequest(http.MethodDelete, "", `"1"`); response.Code != http.StatusNoContent {
		t.Fatalf("project DELETE = %d body=%s", response.Code, response.Body.String())
	}
}

func loginIdentityForTest(t *testing.T, manager *identity.Manager, email string) string {
	t.Helper()
	result, err := manager.Login(t.Context(), identity.LoginRequest{Email: email, Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	return result.AccessToken
}

func authenticatedIdentityBodyRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	if method == http.MethodPut {
		request.Header.Set("If-Match", `"1"`)
	}
	handler.ServeHTTP(response, request)
	return response
}
