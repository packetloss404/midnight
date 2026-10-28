//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

// Administrative fixture rows establish authority only. Every target identity
// below must be created through the public canonical router, never a DB seed.
func TestPostgresInvitationOnboardingAtomicPublicLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, query := range []string{
		`INSERT INTO users(id,email,display_name,created_at,updated_at) VALUES('usr_onboard_admin','onboard-admin@example.test','Fixture administrator',$1,$1)`,
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES('wrk_onboard','onboarding','Onboarding',$1,$1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES('wrk_onboard','usr_onboard_admin','admin',$1,$1,$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES('prj_onboard','wrk_onboard','onboarding','Onboarding',$1,$1),('prj_onboard_other','wrk_onboard','onboarding-other','Other',$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, query, now); err != nil {
			t.Fatal(err)
		}
	}
	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.IdentityInvitationOnboarding.(*PostgresInvitationOnboardingCoordinator)
	if !ok {
		t.Fatal("canonical data stack did not wire durable onboarding")
	}
	service := stack.IdentityInvitationCatalog.(*identity.InvitationService)
	handler := NewRouterWithDataStack(stack)
	create := func(email, project string) identity.CreateInvitationResult {
		t.Helper()
		request := identity.CreateInvitationRequest{Email: email, WorkspaceID: "wrk_onboard", ProjectID: project, InvitedByUserID: "usr_onboard_admin", ExpiresAt: now.Add(24 * time.Hour)}
		if project == "" {
			request.WorkspaceRole = identity.WorkspaceRoleViewer
		} else {
			request.ProjectRole = identity.ProjectRoleViewer
		}
		result, err := service.Create(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	password := "onboarding correct horse battery staple"
	post := func(token string) int {
		body, _ := json.Marshal(map[string]string{"invitation_token": token, "display_name": "New invited person", "password": password})
		response := doRouterRequest(handler, http.MethodPost, invitationOnboardingPath, string(body), nil)
		if strings.Contains(response.Body.String(), password) || strings.Contains(response.Body.String(), token) {
			t.Error("public onboarding echoed a secret")
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Error("onboarding response not private")
		}
		return response.Code
	}

	target := create("new-onboarding@example.test", "prj_onboard")
	coordinator.beforeCommit = func() error { return errors.New("injected precommit failure") }
	if got := post(target.InvitationToken); got != 503 {
		t.Fatalf("faulted registration status=%d", got)
	}
	coordinator.beforeCommit = nil
	var users, grants, accepted, evidence int
	if err := pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM users WHERE email='new-onboarding@example.test'),(SELECT COUNT(*) FROM project_members WHERE user_id IN(SELECT id FROM users WHERE email='new-onboarding@example.test')),(SELECT COUNT(*) FROM identity_invitations WHERE id=$1 AND accepted_at IS NOT NULL),(SELECT COUNT(*) FROM audit_logs WHERE action='identity.invitation.onboard')`, target.Invitation.ID).Scan(&users, &grants, &accepted, &evidence); err != nil {
		t.Fatal(err)
	}
	if users+grants+accepted+evidence != 0 {
		t.Fatal("account, membership, acceptance or audit survived rollback")
	}
	if got := post(target.InvitationToken); got != 201 {
		t.Fatalf("registration status=%d", got)
	}
	user, err := coordinator.Identity.UserByEmail(ctx, "new-onboarding@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if user.InstanceAdmin || user.Disabled || user.MustRotatePassword || bootstrap.VerifyPassword(password, user.PasswordHash) != nil {
		t.Fatal("new account was privileged or password hashing changed")
	}
	if got := post(target.InvitationToken); got != 409 {
		t.Fatalf("replayed registration status=%d", got)
	}
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: user.Email, Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal("new account could not use normal login")
	}
	projects := doRouterRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	var visible []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(projects.Body.Bytes(), &visible)
	if projects.Code != 200 || len(visible) != 1 || visible[0].ID != "prj_onboard" {
		t.Fatal("new direct invitee received wrong project visibility")
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM workspace_members WHERE user_id=$1`, user.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("direct project invitation widened to workspace membership")
	}

	// A second invitation for an existing email cannot replace its password or
	// consume its token. The existing authenticated acceptance route still works.
	existing := create(user.Email, "prj_onboard_other")
	if got := post(existing.InvitationToken); got != 409 {
		t.Fatalf("existing account registration status=%d", got)
	}
	acceptBody, _ := json.Marshal(map[string]string{"invitation_token": existing.InvitationToken})
	accept := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+invitationRoute+"/accept", string(acceptBody), map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "onboarding-existing-accept"})
	if accept.Code != 200 {
		t.Fatalf("existing account acceptance status=%d", accept.Code)
	}
	unchanged, _ := coordinator.Identity.UserByEmail(ctx, user.Email)
	if unchanged.ID != user.ID || unchanged.PasswordHash != user.PasswordHash {
		t.Fatal("existing account was replaced")
	}

	revoked := create("revoked-onboarding@example.test", "prj_onboard")
	if _, err := service.Revoke(ctx, identity.RevokeInvitationRequest{InvitationID: revoked.Invitation.ID, WorkspaceID: "wrk_onboard", ProjectID: "prj_onboard", RevokedByUserID: "usr_onboard_admin"}); err != nil {
		t.Fatal(err)
	}
	if got := post(revoked.InvitationToken); got != 409 {
		t.Fatalf("revoked registration status=%d", got)
	}
	expired := create("expired-onboarding@example.test", "prj_onboard")
	coordinator.Clock = func() time.Time { return now.Add(48 * time.Hour) }
	if got := post(expired.InvitationToken); got != 409 {
		t.Fatalf("expired registration status=%d", got)
	}
	coordinator.Clock = func() time.Time { return now }

	race := create("race-onboarding@example.test", "prj_onboard")
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() { statuses <- post(race.InvitationToken) })
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent token redemption statuses=%v", counts)
	}
	workspace := create("workspace-onboarding@example.test", "")
	if got := post(workspace.InvitationToken); got != 201 {
		t.Fatalf("workspace onboarding status=%d", got)
	}
	var auditText string
	if err := pool.QueryRow(ctx, `SELECT COUNT(*),COALESCE(string_agg(COALESCE(after::text,'')||COALESCE(attrs::text,''),''),'') FROM audit_logs WHERE action='identity.invitation.onboard'`).Scan(&evidence, &auditText); err != nil {
		t.Fatal(err)
	}
	if evidence != 3 || strings.Contains(auditText, password) || strings.Contains(auditText, target.InvitationToken) {
		t.Fatal("onboarding audit was not atomic/redacted")
	}
	for _, email := range []string{"revoked-onboarding@example.test", "expired-onboarding@example.test"} {
		if _, err := coordinator.Identity.UserByEmail(ctx, email); !errors.Is(err, identity.ErrIdentityNotFound) {
			t.Fatal("invalid invitation left a partial user")
		}
	}
	// Durable limits apply before password hashing and retain only MAC buckets.
	for i := 0; i < 31; i++ {
		_, _ = service.ConsumeOnboardingAttempt(ctx, "192.0.2.44:1234", "invalid")
	}
	retry, err := service.ConsumeOnboardingAttempt(ctx, "192.0.2.44:1234", "another-invalid")
	if err != nil || retry <= 0 {
		t.Fatal("durable onboarding network rate limit missing")
	}
}
