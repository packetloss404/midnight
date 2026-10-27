package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/identity"
	githubapp "github.com/midnight/midnight/internal/integrations/github"
	"github.com/midnight/midnight/internal/secrets"
)

// Canonical router admission is real; the source-link coordinator is a local
// boundary double. PostgreSQL coordinator RBAC/grant checks have separate API
// integration coverage. This test must not imply external GitHub installation.
func TestCanonicalGitHubReadsAcceptOrdinaryHumanAuthority(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	hash, err := bootstrap.HashPassword("fixture-correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	users, access := identity.NewMemoryAuthority(identity.UserRecord{ID: "usr_qa", Email: "qa@example.invalid", PasswordHash: hash, SessionGeneration: 1})
	manager, err := identity.NewManager(identity.ManagerConfig{Store: users, MACKey: []byte("0123456789abcdef0123456789abcdef"), Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	login, err := manager.Login(ctx, identity.LoginRequest{Email: "qa@example.invalid", Password: "fixture-correct-horse-battery", ClientKind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if login.User.InstanceAdmin {
		t.Fatal("fixture must be an ordinary human tenant")
	}
	access.SeedWorkspace(identity.Workspace{ID: "ws_qa"}, identity.WorkspaceMember{WorkspaceID: "ws_qa", User: identity.User{ID: "usr_qa"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &now})
	access.SeedWorkspace(identity.Workspace{ID: "ws_other"})
	access.SeedProject("prj_qa", "ws_qa", identity.ProjectMember{ProjectID: "prj_qa", User: identity.User{ID: "usr_qa"}, Role: identity.ProjectRoleOwner, AcceptedAt: &now})
	access.SeedProject("prj_other", "ws_other")
	access.SeedEnvironment("env_qa", "prj_qa", false)
	access.SeedEnvironment("env_other", "prj_other", false)
	resources := api.NewMemoryResourceStore()
	for _, project := range []api.Project{{ID: "prj_qa", WorkspaceID: "ws_qa", Slug: "qa", DisplayName: "QA"}, {ID: "prj_other", WorkspaceID: "ws_other", Slug: "other", DisplayName: "Other"}} {
		if _, err := resources.CreateProject(ctx, project); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []api.Service{{ID: "svc_qa", ProjectID: "prj_qa", Slug: "qa", DisplayName: "QA"}, {ID: "svc_other", ProjectID: "prj_other", Slug: "other", DisplayName: "Other"}} {
		if _, err := resources.CreateService(ctx, service); err != nil {
			t.Fatal(err)
		}
	}
	environments := api.NewMemoryEnvironmentStore(func() time.Time { return now })
	for _, environment := range []api.Environment{{ID: "env_qa", ProjectID: "prj_qa", Slug: "production", Name: "Production", Tier: "production"}, {ID: "env_other", ProjectID: "prj_other", Slug: "production", Name: "Production", Tier: "production"}} {
		if _, err := environments.CreateEnvironment(ctx, environment); err != nil {
			t.Fatal(err)
		}
	}
	key, err := secrets.NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	links := &canonicalGitHubCoordinator{t: t}
	handler := api.NewRouterWithDataStack(api.DataStack{APIBearerToken: "fixture-operator-credential", ProductionAuthRequired: true, IdentitySessions: manager, IdentityAccess: access, ProjectStore: resources, ServiceStore: resources, EnvironmentStore: environments, MutationScopeResolver: api.NewMutationScopeResolver(resources, resources, environments), VariableStore: secrets.NewVault(key, secrets.DenyByDefault{}), TenantGitHubSourceLinks: links, Clock: func() time.Time { return now }})
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, tc := range []struct {
		path, token string
		want        int
	}{{"/services/svc_qa/github", login.AccessToken, 200}, {"/services/svc_qa/preview-environments", login.AccessToken, 200}, {"/services/svc_other/preview-environments", login.AccessToken, 404}, {"/services/svc_qa/github", "", 401}, {"/services/svc_qa/preview-environments", "", 401}} {
		request, _ := http.NewRequest("GET", server.URL+"/api/v1alpha"+tc.path, nil)
		if tc.token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.token)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("canonical GitHub read admission=%d want%d at %s", response.StatusCode, tc.want, tc.path)
		}
	}
	if links.reads != 1 || links.writes != 0 {
		t.Fatal("canonical scoped read bypassed coordinator or attempted mutation")
	}
}

type canonicalGitHubCoordinator struct {
	t             *testing.T
	reads, writes int
}

func (c *canonicalGitHubCoordinator) GetServiceLink(_ context.Context, request api.TenantGitHubServiceRequest) (api.TenantGitHubLinkView, error) {
	c.reads++
	if request.Principal.UserID != "usr_qa" || request.Principal.InstanceAdmin || request.Session.UserID != request.Principal.UserID || request.Session.ID == "" || request.ServiceID != "svc_qa" {
		c.t.Error("router did not install verified ordinary human/service lineage")
		return api.TenantGitHubLinkView{}, identity.ErrAuthorizationDenied
	}
	return api.TenantGitHubLinkView{InstallationID: 11, RepositoryID: 22, Repository: "qa-owner/disposable-qa", Branch: "qa-midnight-main", BindingState: "bound"}, nil
}
func (c *canonicalGitHubCoordinator) ListProjectRepositories(context.Context, api.TenantGitHubProjectRequest) ([]api.TenantGitHubRepositoryView, error) {
	return nil, errors.New("unused")
}
func (c *canonicalGitHubCoordinator) ListProjectRepositoryBranches(context.Context, api.TenantGitHubProjectRepositoryRequest) ([]githubapp.Branch, error) {
	return nil, errors.New("unused")
}
func (c *canonicalGitHubCoordinator) PutServiceLink(context.Context, api.TenantGitHubLinkMutationRequest) (api.TenantGitHubLinkMutationResult, error) {
	c.writes++
	return api.TenantGitHubLinkMutationResult{}, errors.New("unused")
}
func (c *canonicalGitHubCoordinator) DeleteServiceLink(context.Context, api.TenantGitHubLinkDeleteRequest) (api.TenantGitHubLinkMutationResult, error) {
	c.writes++
	return api.TenantGitHubLinkMutationResult{}, errors.New("unused")
}
