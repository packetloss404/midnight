package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agenttools "github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestHumanProductReadsAreTenantScopedAndMutationCapabilitiesStayHonest(t *testing.T) {
	handler, manager := productReadFixture(t)
	alice := loginIdentityForTest(t, manager, "alice@example.com")
	bob := loginIdentityForTest(t, manager, "bob@example.com")

	projects := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", alice)
	if projects.Code != http.StatusOK ||
		!strings.Contains(projects.Body.String(), `"id":"prj_a"`) ||
		strings.Contains(projects.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("tenant project list status=%d body=%s", projects.Code, projects.Body.String())
	}
	crossProject := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects/prj_b", alice)
	if crossProject.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant project status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}

	services := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services", alice)
	if services.Code != http.StatusOK ||
		!strings.Contains(services.Body.String(), `"id":"svc_a"`) ||
		strings.Contains(services.Body.String(), `"id":"svc_b"`) {
		t.Fatalf("tenant service list status=%d body=%s", services.Code, services.Body.String())
	}
	crossService := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/services/svc_b", alice)
	if crossService.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant service status=%d body=%s", crossService.Code, crossService.Body.String())
	}

	environmentResources := authenticatedIdentityRequest(
		handler,
		http.MethodGet,
		v1alphaPrefix+"/environments/env_a/resources",
		alice,
	)
	if environmentResources.Code != http.StatusOK ||
		!strings.Contains(environmentResources.Body.String(), `"id":"svc_a"`) ||
		!strings.Contains(environmentResources.Body.String(), `"host":"api-a.midnight.internal"`) ||
		strings.Contains(environmentResources.Body.String(), `"id":"svc_b"`) {
		t.Fatalf("tenant environment summary status=%d body=%s", environmentResources.Code, environmentResources.Body.String())
	}
	crossEnvironmentResources := authenticatedIdentityRequest(
		handler,
		http.MethodGet,
		v1alphaPrefix+"/environments/env_b/resources",
		alice,
	)
	if crossEnvironmentResources.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant environment summary status=%d body=%s", crossEnvironmentResources.Code, crossEnvironmentResources.Body.String())
	}

	// A direct project viewer can see the ordinary environment but the
	// restriction overlay conceals the restricted environment.
	environments := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/environments?project_id=prj_a", bob)
	if environments.Code != http.StatusOK ||
		!strings.Contains(environments.Body.String(), `"id":"env_a"`) ||
		strings.Contains(environments.Body.String(), `"id":"env_restricted"`) {
		t.Fatalf("restricted environment list status=%d body=%s", environments.Code, environments.Body.String())
	}
	restricted := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/environments/env_restricted", bob)
	if restricted.Code != http.StatusNotFound {
		t.Fatalf("restricted environment item status=%d body=%s", restricted.Code, restricted.Body.String())
	}
	restrictedResources := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/environments/env_restricted/resources", bob)
	if restrictedResources.Code != http.StatusNotFound {
		t.Fatalf("restricted environment summary status=%d body=%s", restrictedResources.Code, restrictedResources.Body.String())
	}

	projectCreate := authenticatedIdentityMutationRequest(handler, http.MethodPost, v1alphaPrefix+"/projects", alice,
		`{"workspace_id":"wrk_a","slug":"blocked","display_name":"Blocked"}`)
	if projectCreate.Code != http.StatusServiceUnavailable ||
		!strings.Contains(projectCreate.Body.String(), `"code":"INTERNAL_ERROR"`) ||
		!strings.Contains(projectCreate.Body.String(), "tenant project creation is unavailable") {
		t.Fatalf("unavailable project capability status=%d body=%s", projectCreate.Code, projectCreate.Body.String())
	}

	serviceCreate := authenticatedIdentityMutationRequest(handler, http.MethodPost, v1alphaPrefix+"/services", alice,
		`{"project_id":"prj_a","slug":"blocked","display_name":"Blocked"}`)
	if serviceCreate.Code != http.StatusForbidden ||
		!strings.Contains(serviceCreate.Body.String(), `"code":"AUTHORIZATION_DENIED"`) {
		t.Fatalf("quarantined service mutation status=%d body=%s", serviceCreate.Code, serviceCreate.Body.String())
	}

	environmentCreate := authenticatedIdentityMutationRequest(handler, http.MethodPost, v1alphaPrefix+"/environments", alice,
		`{"project_id":"prj_a","slug":"blocked","name":"Blocked","tier":"development"}`)
	if environmentCreate.Code != http.StatusForbidden ||
		!strings.Contains(environmentCreate.Body.String(), `"code":"AUTHORIZATION_DENIED"`) {
		t.Fatalf("quarantined environment mutation status=%d body=%s", environmentCreate.Code, environmentCreate.Body.String())
	}
}

func TestProjectAccessReportsRBACPermissionsWithoutRuntimeCapabilityClaims(t *testing.T) {
	handler, manager := productReadFixture(t)
	bob := loginIdentityForTest(t, manager, "bob@example.com")

	access := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects/prj_a/access", bob)
	if access.Code != http.StatusOK ||
		!strings.Contains(access.Body.String(), `"project_role":"viewer"`) ||
		!strings.Contains(access.Body.String(), `"project.read"`) ||
		!strings.Contains(access.Body.String(), `"logs.read"`) ||
		strings.Contains(access.Body.String(), `"project.delete"`) {
		t.Fatalf("project access status=%d body=%s", access.Code, access.Body.String())
	}
	restricted := authenticatedIdentityRequest(handler, http.MethodGet,
		v1alphaPrefix+"/projects/prj_a/access?environment_id=env_restricted", bob)
	if restricted.Code != http.StatusNotFound {
		t.Fatalf("restricted project access status=%d body=%s", restricted.Code, restricted.Body.String())
	}
	crossTenant := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects/prj_b/access", bob)
	if crossTenant.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant access status=%d body=%s", crossTenant.Code, crossTenant.Body.String())
	}
}

func TestProductRoutesAcceptOperatorAndRejectRevokedHumanSession(t *testing.T) {
	handler, manager := productReadFixture(t)
	alice := loginIdentityForTest(t, manager, "alice@example.com")

	operator := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", "operator-recovery-token")
	if operator.Code != http.StatusOK ||
		!strings.Contains(operator.Body.String(), `"id":"prj_a"`) ||
		!strings.Contains(operator.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("operator project list status=%d body=%s", operator.Code, operator.Body.String())
	}
	principal, session, err := manager.Authenticate(t.Context(), "Bearer "+alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(t.Context(), principal, session.ID, "test"); err != nil {
		t.Fatal(err)
	}
	revoked := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", alice)
	if revoked.Code != http.StatusUnauthorized ||
		!strings.Contains(revoked.Body.String(), `"code":"AUTH_SESSION_INVALID"`) {
		t.Fatalf("revoked product session status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestSessionTokenClassCannotBeElevatedByOperatorTokenConfiguration(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	alice := loginIdentityForTest(t, manager, "alice@example.com")
	stack.APIBearerToken = alice
	stack.ProductionAuthRequired = false
	stack.GraphQLHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	stack.MCPRegistry = agenttools.NewDefaultRegistry()
	handler := NewRouterWithDataStack(stack)

	unauthenticated := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", "")
	if unauthenticated.Code != http.StatusUnauthorized ||
		strings.Contains(unauthenticated.Body.String(), `"id":"prj_a"`) ||
		strings.Contains(unauthenticated.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("session-shaped operator configuration opened developer access: status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
	scoped := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", alice)
	if scoped.Code != http.StatusOK ||
		!strings.Contains(scoped.Body.String(), `"id":"prj_a"`) ||
		strings.Contains(scoped.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("session-shaped operator configuration elevated credential: status=%d body=%s", scoped.Code, scoped.Body.String())
	}
	principal, session, err := manager.Authenticate(t.Context(), "Bearer "+alice)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(t.Context(), principal, session.ID, "test"); err != nil {
		t.Fatal(err)
	}
	revoked := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", alice)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("session-shaped operator configuration bypassed revocation: status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	operatorOnly := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/variables", alice)
	if operatorOnly.Code != http.StatusUnauthorized {
		t.Fatalf("session-shaped operator configuration reached operator-only route: status=%d body=%s", operatorOnly.Code, operatorOnly.Body.String())
	}
	graphQL := authenticatedIdentityRequest(handler, http.MethodPost, "/graphql/v2", alice)
	if graphQL.Code != http.StatusUnauthorized {
		t.Fatalf("session-shaped operator configuration reached GraphQL: status=%d body=%s", graphQL.Code, graphQL.Body.String())
	}
	mcp := authenticatedIdentityRequest(handler, http.MethodPost, "/mcp/v1", alice)
	if mcp.Code != http.StatusUnauthorized {
		t.Fatalf("session-shaped operator configuration reached MCP: status=%d body=%s", mcp.Code, mcp.Body.String())
	}
	oauthAuthorize := authenticatedIdentityRequest(
		handler,
		http.MethodGet,
		"/auth/oauth/authorize?client_id=midnight-cli&code_challenge=test-challenge&code_challenge_method=S256&state=test&redirect_uri=http%3A%2F%2F127.0.0.1%3A18181%2Fcallback",
		alice,
	)
	if oauthAuthorize.Code != http.StatusUnauthorized ||
		!strings.Contains(oauthAuthorize.Body.String(), "Operator authentication required") {
		t.Fatalf("session-shaped operator configuration authorized OAuth: status=%d body=%s", oauthAuthorize.Code, oauthAuthorize.Body.String())
	}
	oauthToken := authenticatedIdentityRequest(handler, http.MethodPost, "/auth/oauth/token", alice)
	if oauthToken.Code != http.StatusServiceUnavailable ||
		!strings.Contains(oauthToken.Body.String(), `"error":"server_error"`) {
		t.Fatalf("session-shaped operator configuration was reissued by OAuth: status=%d body=%s", oauthToken.Code, oauthToken.Body.String())
	}
}

func TestRecognizedNonAdminTokenClassesCannotBecomeRecoveryOperators(t *testing.T) {
	for _, configured := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(configured, func(t *testing.T) {
			stack, _ := productReadDataFixture(t)
			stack.APIBearerToken = configured
			stack.ProductionAuthRequired = false
			handler := NewRouterWithDataStack(stack)

			product := authenticatedIdentityRequest(
				handler,
				http.MethodGet,
				v1alphaPrefix+"/projects",
				configured,
			)
			if product.Code != http.StatusUnauthorized {
				t.Fatalf(
					"scoped credential became product operator: status=%d body=%s",
					product.Code,
					product.Body.String(),
				)
			}
			operatorOnly := authenticatedIdentityRequest(
				handler,
				http.MethodGet,
				v1alphaPrefix+"/variables",
				configured,
			)
			if operatorOnly.Code != http.StatusUnauthorized {
				t.Fatalf(
					"scoped credential reached operator-only route: status=%d body=%s",
					operatorOnly.Code,
					operatorOnly.Body.String(),
				)
			}
			unauthenticated := authenticatedIdentityRequest(
				handler,
				http.MethodGet,
				v1alphaPrefix+"/projects",
				"",
			)
			if unauthenticated.Code != http.StatusUnauthorized {
				t.Fatalf(
					"invalid configured class opened developer fallback: status=%d body=%s",
					unauthenticated.Code,
					unauthenticated.Body.String(),
				)
			}
			if got := operatorRecoveryToken(configured); got != "" {
				t.Fatalf("operatorRecoveryToken(%q) = %q, want empty", configured, got)
			}
		})
	}
}

func TestRecoveryOperatorMiddlewarePreservesLegacyAndAdminCompatibility(t *testing.T) {
	for _, configured := range []string{
		"legacy-operator-secret",
		"midn_admin_recovery-secret",
	} {
		t.Run(configured, func(t *testing.T) {
			stack, _ := productReadDataFixture(t)
			stack.APIBearerToken = configured
			handler := NewRouterWithDataStack(stack)
			response := authenticatedIdentityRequest(
				handler,
				http.MethodGet,
				v1alphaPrefix+"/projects",
				configured,
			)
			if response.Code != http.StatusOK {
				t.Fatalf(
					"compatible recovery token rejected: status=%d body=%s",
					response.Code,
					response.Body.String(),
				)
			}
			if got := operatorRecoveryToken(configured); got != configured {
				t.Fatalf("operatorRecoveryToken(%q) = %q", configured, got)
			}
		})
	}
}

func TestInstallationAdminHumanSessionReplacesNormalRecoveryAccess(t *testing.T) {
	passwordHash, err := bootstrap.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)
	store := identity.NewMemoryStore(
		identity.UserRecord{
			ID: "usr_admin", Email: "admin@example.com", DisplayName: "Admin",
			InstanceAdmin: true, PasswordHash: passwordHash, SessionGeneration: 1,
		},
		identity.UserRecord{
			ID: "usr_member", Email: "member@example.com", DisplayName: "Member",
			PasswordHash: passwordHash, SessionGeneration: 1,
		},
	)
	counter := byte(0)
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
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
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken: "operator-recovery-token", ProductionAuthRequired: true,
		IdentitySessions: manager, VariableStore: newDefaultVariableStore(false),
	})
	admin := loginIdentityForTest(t, manager, "admin@example.com")
	member := loginIdentityForTest(t, manager, "member@example.com")

	adminSummary := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/dashboard/summary", admin)
	if adminSummary.Code != http.StatusOK {
		t.Fatalf("admin human summary status=%d body=%s", adminSummary.Code, adminSummary.Body.String())
	}
	withoutRecovery := NewRouterWithDataStack(DataStack{
		ProductionAuthRequired: true,
		IdentitySessions:       manager,
		VariableStore:          newDefaultVariableStore(false),
	})
	adminWithoutRecovery := authenticatedIdentityRequest(withoutRecovery, http.MethodGet, v1alphaPrefix+"/dashboard/summary", admin)
	if adminWithoutRecovery.Code != http.StatusOK {
		t.Fatalf("admin human depended on recovery credential: status=%d body=%s", adminWithoutRecovery.Code, adminWithoutRecovery.Body.String())
	}
	me := authenticatedIdentityRequest(handler, http.MethodGet, identityMePath, admin)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"instance_admin":true`) {
		t.Fatalf("admin me status=%d body=%s", me.Code, me.Body.String())
	}

	memberSummary := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/dashboard/summary", member)
	if memberSummary.Code != http.StatusForbidden ||
		!strings.Contains(memberSummary.Body.String(), `"code":"AUTHORIZATION_DENIED"`) {
		t.Fatalf("ordinary human reached installation settings: status=%d body=%s", memberSummary.Code, memberSummary.Body.String())
	}

	principal, session, err := manager.Authenticate(t.Context(), "Bearer "+admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(t.Context(), principal, session.ID, "test"); err != nil {
		t.Fatal(err)
	}
	revoked := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/dashboard/summary", admin)
	if revoked.Code != http.StatusUnauthorized ||
		!strings.Contains(revoked.Body.String(), `"code":"AUTH_SESSION_INVALID"`) {
		t.Fatalf("revoked admin session remained privileged: status=%d body=%s", revoked.Code, revoked.Body.String())
	}
}

func TestPostgresIndexedTenantReadsRequireSharedAuthorityPool(t *testing.T) {
	poolA := &pg.Pool{}
	poolB := &pg.Pool{}
	resources := NewPostgresResourceStore(poolA)
	if _, ok := tenantIndexedReadStore(resources, identity.NewPostgresStore(poolA)); !ok {
		t.Fatal("shared PostgreSQL authority did not enable indexed tenant reads")
	}
	if _, ok := tenantIndexedReadStore(resources, identity.NewPostgresStore(poolB)); ok {
		t.Fatal("split PostgreSQL authority enabled indexed tenant reads")
	}
	if _, ok := tenantIndexedReadStore(resources, identity.NewMemoryAccessStore()); ok {
		t.Fatal("non-PostgreSQL authority enabled indexed tenant reads")
	}
	if productResourceStoresShareAuthority(resources, resources, resources, identity.NewPostgresStore(poolB)) {
		t.Fatal("split PostgreSQL resource/access authority enabled product routes")
	}
}

func TestProductTenantReadsRequireOneVerifiedIdentityAuthority(t *testing.T) {
	stack, manager := productReadDataFixture(t)
	alice := loginIdentityForTest(t, manager, "alice@example.com")
	unrelatedAccess := identity.NewMemoryAccessStore()
	accepted := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	unrelatedAccess.SeedWorkspace(identity.Workspace{ID: "wrk_b"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_b", User: identity.User{ID: "usr_alice"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted,
	})
	unrelatedAccess.SeedProject("prj_b", "wrk_b",
		identity.ProjectMember{ProjectID: "prj_b", User: identity.User{ID: "usr_alice"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
	)
	stack.IdentityAccess = unrelatedAccess
	handler := NewRouterWithDataStack(stack)

	response := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", alice)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) ||
		!strings.Contains(response.Body.String(), "tenant authorization is unavailable") ||
		strings.Contains(response.Body.String(), `"id":"prj_a"`) ||
		strings.Contains(response.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("split identity authority response: status=%d body=%s", response.Code, response.Body.String())
	}

	operator := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", "operator-recovery-token")
	if operator.Code != http.StatusOK ||
		!strings.Contains(operator.Body.String(), `"id":"prj_a"`) ||
		!strings.Contains(operator.Body.String(), `"id":"prj_b"`) {
		t.Fatalf("split authority changed operator path: status=%d body=%s", operator.Code, operator.Body.String())
	}

	invalid := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", identity.SessionTokenPrefix+"invalid")
	if invalid.Code != http.StatusUnauthorized ||
		!strings.Contains(invalid.Body.String(), `"code":"AUTH_SESSION_INVALID"`) {
		t.Fatalf("invalid session reported as capability outage: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}

func authenticatedIdentityMutationRequest(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "product-auth-test-key")
	handler.ServeHTTP(response, request)
	return response
}

func productReadFixture(t *testing.T) (http.Handler, *identity.Manager) {
	t.Helper()
	stack, manager := productReadDataFixture(t)
	return NewRouterWithDataStack(stack), manager
}

func productReadDataFixture(t *testing.T) (DataStack, *identity.Manager) {
	t.Helper()
	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	passwordHash, err := bootstrap.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, access := identity.NewMemoryAuthority(
		identity.UserRecord{ID: "usr_alice", Email: "alice@example.com", PasswordHash: passwordHash, SessionGeneration: 1},
		identity.UserRecord{ID: "usr_bob", Email: "bob@example.com", PasswordHash: passwordHash, SessionGeneration: 1},
		identity.UserRecord{ID: "usr_carol", Email: "carol@example.com", PasswordHash: passwordHash, SessionGeneration: 1},
	)
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
	access.SeedWorkspace(identity.Workspace{ID: "wrk_a"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_a", User: identity.User{ID: "usr_alice"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted,
	})
	access.SeedWorkspace(identity.Workspace{ID: "wrk_b"}, identity.WorkspaceMember{
		WorkspaceID: "wrk_b", User: identity.User{ID: "usr_carol"}, Role: identity.WorkspaceRoleAdmin, AcceptedAt: &accepted,
	})
	access.SeedProject("prj_a", "wrk_a",
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_alice"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
		identity.ProjectMember{ProjectID: "prj_a", User: identity.User{ID: "usr_bob"}, Role: identity.ProjectRoleViewer, AcceptedAt: &accepted},
	)
	access.SeedProject("prj_b", "wrk_b",
		identity.ProjectMember{ProjectID: "prj_b", User: identity.User{ID: "usr_carol"}, Role: identity.ProjectRoleOwner, AcceptedAt: &accepted},
	)
	access.SeedEnvironment("env_a", "prj_a", false)
	access.SeedEnvironment("env_restricted", "prj_a", true)
	access.SeedEnvironment("env_b", "prj_b", false)

	resources := NewMemoryResourceStoreWithClock(func() time.Time { return now })
	for _, project := range []Project{
		{ID: "prj_a", WorkspaceID: "wrk_a", Slug: "a", DisplayName: "A"},
		{ID: "prj_b", WorkspaceID: "wrk_b", Slug: "b", DisplayName: "B"},
	} {
		if _, err := resources.CreateProject(t.Context(), project); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []Service{
		{ID: "svc_a", ProjectID: "prj_a", Slug: "api-a", DisplayName: "API A"},
		{ID: "svc_b", ProjectID: "prj_b", Slug: "api-b", DisplayName: "API B"},
	} {
		if _, err := resources.CreateService(t.Context(), service); err != nil {
			t.Fatal(err)
		}
	}
	environments := NewMemoryEnvironmentStore(func() time.Time { return now })
	for _, environment := range []Environment{
		{ID: orailtypes.ID("env_a"), ProjectID: "prj_a", Slug: "production", Name: "Production", Tier: "production"},
		{ID: orailtypes.ID("env_restricted"), ProjectID: "prj_a", Slug: "restricted", Name: "Restricted", Tier: "staging"},
		{ID: orailtypes.ID("env_b"), ProjectID: "prj_b", Slug: "production", Name: "Production", Tier: "production"},
	} {
		if _, err := environments.CreateEnvironment(t.Context(), environment); err != nil {
			t.Fatal(err)
		}
	}
	stack := DataStack{
		ProjectStore:           resources,
		ServiceStore:           resources,
		EnvironmentStore:       environments,
		MutationScopeResolver:  NewMutationScopeResolver(resources, resources, environments),
		IdentitySessions:       manager,
		IdentityAccess:         access,
		APIBearerToken:         "operator-recovery-token",
		ProductionAuthRequired: true,
		VariableStore:          newDefaultVariableStore(false),
		Clock:                  func() time.Time { return now },
	}
	return stack, manager
}
