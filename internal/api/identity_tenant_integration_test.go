//go:build integration

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

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresIdentityClaimSessionRestartAndTenantConcealment(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, expectedVersion := range []int{53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40, 39, 38} {
		down, err := runner.Down(ctx)
		if err != nil || down == nil || down.Version != expectedVersion {
			t.Fatalf(
				"roll back migration %d: file=%#v err=%v",
				expectedVersion,
				down,
				err,
			)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name)
		VALUES ('usr_migration_default', 'hostile@example.com', 'Hostile sentinel')`); err != nil {
		t.Fatalf("seed hostile sentinel: %v", err)
	}
	if _, err := runner.Up(ctx); err == nil || !strings.Contains(err.Error(), "reserved migration user id already exists") {
		t.Fatalf("sentinel collision migration error = %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id='usr_migration_default'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name)
		VALUES ('wrk_default', 'hostile-default', 'Hostile sentinel')`); err != nil {
		t.Fatalf("seed hostile workspace sentinel: %v", err)
	}
	if _, err := runner.Up(ctx); err == nil || !strings.Contains(err.Error(), "reserved default workspace id already exists") {
		t.Fatalf("workspace sentinel collision migration error = %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id='wrk_default'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, is_admin, created_at, updated_at)
		VALUES ('usr_legacy', 'legacy@example.com', 'Legacy owner', TRUE, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO workspaces (id, slug, display_name)
		VALUES ('wrk_legacy', 'legacy-workspace', 'Legacy workspace');
		INSERT INTO workspace_members (workspace_id, user_id, role)
		VALUES ('wrk_legacy', 'usr_legacy', 'owner');
		INSERT INTO projects (id, slug, display_name)
		VALUES ('prj_legacy', 'legacy', 'Legacy project')`); err != nil {
		t.Fatalf("seed legacy identity/project state: %v", err)
	}
	applied, err := runner.Up(ctx)
	if err != nil ||
		len(applied) != 16 ||
		applied[0].Version != 38 ||
		applied[1].Version != 39 ||
		applied[2].Version != 40 ||
		applied[3].Version != 41 ||
		applied[4].Version != 42 ||
		applied[5].Version != 43 ||
		applied[6].Version != 44 ||
		applied[7].Version != 45 ||
		applied[8].Version != 46 ||
		applied[9].Version != 47 ||
		applied[10].Version != 48 ||
		applied[11].Version != 49 ||
		applied[12].Version != 50 ||
		applied[13].Version != 51 ||
		applied[14].Version != 52 ||
		applied[15].Version != 53 {
		t.Fatalf("upgrade legacy state: applied=%#v err=%v", applied, err)
	}
	var legacyRole string
	var legacyAccepted *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT role, accepted_at
		FROM workspace_members
		WHERE workspace_id='wrk_legacy' AND user_id='usr_legacy'`,
	).Scan(&legacyRole, &legacyAccepted); err != nil {
		t.Fatal(err)
	}
	if legacyRole != string(identity.WorkspaceRoleAdmin) || legacyAccepted == nil {
		t.Fatalf("legacy membership role=%q accepted=%v", legacyRole, legacyAccepted)
	}
	var projectWorkspaceDefault *string
	if err := pool.QueryRow(ctx, `
		SELECT column_default
		FROM information_schema.columns
		WHERE table_schema=current_schema()
		  AND table_name='projects'
		  AND column_name='workspace_id'`,
	).Scan(&projectWorkspaceDefault); err != nil {
		t.Fatal(err)
	}
	if projectWorkspaceDefault != nil {
		t.Fatalf("projects.workspace_id default = %q, want no silent tenant default", *projectWorkspaceDefault)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted
		)
		VALUES ('env_restricted', 'prj_legacy', 'restricted', 'Restricted', 'production', TRUE)`); err != nil {
		t.Fatal(err)
	}
	for _, expectedVersion := range []int{53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40, 39} {
		down, err := runner.Down(ctx)
		if err != nil || down == nil || down.Version != expectedVersion {
			t.Fatalf(
				"roll back migration %d before identity refusal: file=%#v err=%v",
				expectedVersion,
				down,
				err,
			)
		}
	}
	if down, err := runner.Down(ctx); err == nil || down != nil || !strings.Contains(err.Error(), "restricted environments exist") {
		t.Fatalf("identity rollback with restricted environment: file=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE environments SET access_restricted=FALSE WHERE id='env_restricted'`); err != nil {
		t.Fatal(err)
	}
	if applied, err := runner.Up(ctx); err != nil ||
		len(applied) != 15 ||
		applied[0].Version != 39 ||
		applied[1].Version != 40 ||
		applied[2].Version != 41 ||
		applied[3].Version != 42 ||
		applied[4].Version != 43 ||
		applied[5].Version != 44 ||
		applied[6].Version != 45 ||
		applied[7].Version != 46 ||
		applied[8].Version != 47 ||
		applied[9].Version != 48 ||
		applied[10].Version != 49 ||
		applied[11].Version != 50 ||
		applied[12].Version != 51 ||
		applied[13].Version != 52 ||
		applied[14].Version != 53 {
		t.Fatalf("restore identity follow-up migrations: applied=%#v err=%v", applied, err)
	}

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	stack := postgresIdentityIntegrationStack(t, pool, now)
	stack.APIBearerToken = "operator-recovery-token"
	handler := NewRouterWithDataStack(stack)

	totpSecretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	totpSecret, err := bootstrap.DecodeTOTPSecret(totpSecretBase32)
	if err != nil {
		t.Fatal(err)
	}
	totpCode, err := bootstrap.GenerateTOTP(totpSecret, now)
	if err != nil {
		t.Fatal(err)
	}
	claimDocument := identityClaimRequest{
		Email:                "owner@example.com",
		DisplayName:          "Owner",
		Password:             "correct horse battery staple",
		TOTPSecret:           totpSecretBase32,
		TOTPCode:             totpCode,
		WorkspaceSlug:        "acme-platform",
		WorkspaceDisplayName: "Acme Platform",
	}
	invalidClaimDocument := claimDocument
	invalidClaimDocument.TOTPCode = "000000"
	invalidClaimBody, _ := json.Marshal(invalidClaimDocument)
	invalidClaim := httptest.NewRecorder()
	invalidClaimRequest := httptest.NewRequest(http.MethodPost, identityClaimPath, strings.NewReader(string(invalidClaimBody)))
	invalidClaimRequest.Header.Set("Authorization", "Bearer operator-recovery-token")
	invalidClaimRequest.Header.Set("Idempotency-Key", "invalid-claim-default-workspace")
	invalidClaimRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(invalidClaim, invalidClaimRequest)
	if invalidClaim.Code != http.StatusBadRequest {
		t.Fatalf("invalid MFA claim status=%d body=%s", invalidClaim.Code, invalidClaim.Body.String())
	}
	claimBody, _ := json.Marshal(claimDocument)
	claim := httptest.NewRecorder()
	claimRequest := httptest.NewRequest(http.MethodPost, identityClaimPath, strings.NewReader(string(claimBody)))
	claimRequest.Header.Set("Authorization", "Bearer operator-recovery-token")
	claimRequest.Header.Set("Idempotency-Key", "claim-default-workspace")
	claimRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(claim, claimRequest)
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	var claimed identity.ClaimDefaultWorkspaceResult
	if err := json.Unmarshal(claim.Body.Bytes(), &claimed); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if claimed.User.ID == "" || claimed.Workspace.ID != identity.DefaultWorkspaceID ||
		claimed.Workspace.Slug != "acme-platform" || claimed.Workspace.Role != identity.WorkspaceRoleAdmin {
		t.Fatalf("claim result = %#v", claimed)
	}
	var migratedWorkspace, ownerRole string
	if err := pool.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id='prj_legacy'`).Scan(&migratedWorkspace); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id='prj_legacy' AND user_id=$1`, claimed.User.ID).Scan(&ownerRole); err != nil {
		t.Fatal(err)
	}
	if migratedWorkspace != identity.DefaultWorkspaceID || ownerRole != string(identity.ProjectRoleOwner) {
		t.Fatalf("legacy ownership workspace=%q role=%q", migratedWorkspace, ownerRole)
	}
	var migrationMemberships int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM workspace_members WHERE user_id='usr_migration_default') +
			(SELECT COUNT(*) FROM project_members WHERE user_id='usr_migration_default')`,
	).Scan(&migrationMemberships); err != nil {
		t.Fatal(err)
	}
	if migrationMemberships != 0 {
		t.Fatalf("migration identity retains %d memberships after claim", migrationMemberships)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, created_at, updated_at)
		VALUES
			('usr_target', 'target@example.com', 'Target', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('usr_actor', 'actor@example.com', 'Actor', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		)
		VALUES
			('wrk_default', 'usr_target', 'member', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('wrk_default', 'usr_actor', 'admin', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES ('prj_target_owned', 'wrk_default', 'target-owned', 'Target owned');
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		)
		VALUES ('prj_target_owned', 'usr_target', 'owner', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed membership invariant proof: %v", err)
	}
	if err := stack.IdentityAccess.RemoveWorkspaceMember(ctx, identity.DefaultWorkspaceID, "usr_target", claimed.User.ID, now); !errors.Is(err, identity.ErrLastProjectOwner) {
		t.Fatalf("remove workspace member who is sole project owner: %v", err)
	}
	var retainedTarget int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM workspace_members
		WHERE workspace_id='wrk_default' AND user_id='usr_target'`,
	).Scan(&retainedTarget); err != nil {
		t.Fatal(err)
	}
	if retainedTarget != 1 {
		t.Fatal("rejected workspace removal was not atomic")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		)
		VALUES ('prj_target_owned', $1, 'owner', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		claimed.User.ID,
	); err != nil {
		t.Fatal(err)
	}
	if err := stack.IdentityAccess.RemoveWorkspaceMember(ctx, identity.DefaultWorkspaceID, "usr_target", claimed.User.ID, now); err != nil {
		t.Fatalf("remove workspace member after alternate owner: %v", err)
	}

	lockTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:"+identity.DefaultWorkspaceID); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `
		UPDATE workspace_members
		SET role='member', updated_at=$2, resource_version=resource_version+1
		WHERE workspace_id=$1 AND user_id='usr_actor'`,
		identity.DefaultWorkspaceID,
		now,
	); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatal(err)
	}
	mutationResult := make(chan error, 1)
	go func() {
		_, mutationErr := stack.IdentityAccess.PutWorkspaceMember(
			ctx,
			identity.DefaultWorkspaceID,
			claimed.User.ID,
			identity.WorkspaceRoleViewer,
			"usr_actor",
			now,
		)
		mutationResult <- mutationErr
	}()
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-mutationResult; !errors.Is(err, identity.ErrAuthorizationDenied) {
		t.Fatalf("mutation authorized after actor demotion committed: %v", err)
	}

	login := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, identitySessionsPath, strings.NewReader(`{
		"email":"owner@example.com",
		"password":"correct horse battery staple",
		"totp_code":"`+totpCode+`",
		"client_kind":"cli"
	}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusCreated {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var loginResult identity.LoginResult
	if err := json.Unmarshal(login.Body.Bytes(), &loginResult); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if !strings.HasPrefix(loginResult.AccessToken, "midn_user_") {
		t.Fatalf("access token prefix = %q", loginResult.AccessToken)
	}
	var tokenHash []byte
	var storedPrefix string
	if err := pool.QueryRow(ctx, `SELECT token_hash, token_prefix FROM auth_sessions WHERE id=$1`, loginResult.Session.ID).Scan(&tokenHash, &storedPrefix); err != nil {
		t.Fatal(err)
	}
	if len(tokenHash) != 32 || storedPrefix == "" ||
		strings.Contains(string(tokenHash), loginResult.AccessToken) {
		t.Fatalf("unsafe session persistence hash_len=%d prefix=%q", len(tokenHash), storedPrefix)
	}

	// Rebuild the data stack to prove the session is durable rather than
	// process-local, then resolve only the caller's tenant.
	restartedStack := postgresIdentityIntegrationStack(t, pool, now.Add(time.Minute))
	restartedStack.APIBearerToken = "operator-recovery-token"
	restarted := NewRouterWithDataStack(restartedStack)
	me := authenticatedIdentityRequest(restarted, http.MethodGet, identityMePath, loginResult.AccessToken)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), claimed.User.ID) {
		t.Fatalf("restarted session status=%d body=%s", me.Code, me.Body.String())
	}
	var generationBefore int64
	if err := pool.QueryRow(ctx, `SELECT session_generation FROM users WHERE id=$1`, claimed.User.ID).Scan(&generationBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET must_rotate_password=TRUE WHERE id=$1`, claimed.User.ID); err != nil {
		t.Fatal(err)
	}
	var generationAfter int64
	if err := pool.QueryRow(ctx, `SELECT session_generation FROM users WHERE id=$1`, claimed.User.ID).Scan(&generationAfter); err != nil {
		t.Fatal(err)
	}
	if generationAfter != generationBefore+1 {
		t.Fatalf("credential transition generation=%d, want %d", generationAfter, generationBefore+1)
	}
	invalidated := authenticatedIdentityRequest(restarted, http.MethodGet, identityMePath, loginResult.AccessToken)
	if invalidated.Code != http.StatusUnauthorized {
		t.Fatalf("credential transition left prior session active: status=%d body=%s", invalidated.Code, invalidated.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET must_rotate_password=FALSE WHERE id=$1`, claimed.User.ID); err != nil {
		t.Fatal(err)
	}
	replacementTOTP, err := bootstrap.GenerateTOTP(totpSecret, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	replacementLogin, err := restartedStack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "owner@example.com", Password: "correct horse battery staple",
		TOTPCode: replacementTOTP, ClientKind: "cli",
	})
	if err != nil {
		t.Fatalf("replacement login after credential transition: %v", err)
	}
	activeToken := replacementLogin.AccessToken
	activeSessionID := replacementLogin.Session.ID

	workspaces := authenticatedIdentityRequest(restarted, http.MethodGet, identityWorkspacesPath, activeToken)
	if workspaces.Code != http.StatusOK ||
		!strings.Contains(workspaces.Body.String(), `"id":"wrk_default"`) ||
		strings.Contains(workspaces.Body.String(), `"id":"wrk_other"`) {
		t.Fatalf("workspace scope status=%d body=%s", workspaces.Code, workspaces.Body.String())
	}

	// A real, unrelated tenant exists, but a guessed project ID is concealed
	// from the first tenant's session.
	viewerPasswordHash, err := bootstrap.HashPassword("viewer correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		)
		VALUES (
			'usr_direct_viewer', 'viewer@example.com', 'Direct viewer', $1, 1,
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)`,
		viewerPasswordHash,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name)
		VALUES ('wrk_other', 'other', 'Other');
		INSERT INTO projects (id, workspace_id, slug, display_name)
		VALUES ('prj_other', 'wrk_other', 'other', 'Other');
		INSERT INTO services (id, project_id, slug, display_name)
		VALUES
			('svc_legacy', 'prj_legacy', 'legacy-api', 'Legacy API'),
			('svc_other', 'prj_other', 'other-api', 'Other API');
		INSERT INTO environments (id, project_id, slug, name, tier)
		VALUES
			('env_visible', 'prj_legacy', 'visible', 'Visible', 'staging'),
			('env_other', 'prj_other', 'production', 'Production', 'production');
		UPDATE environments SET access_restricted=TRUE WHERE id='env_restricted';
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		)
		VALUES (
			'prj_legacy', 'usr_direct_viewer', 'viewer',
			CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatal(err)
	}
	crossTenant := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects/prj_other/members", activeToken)
	if crossTenant.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant guessed id status=%d body=%s", crossTenant.Code, crossTenant.Body.String())
	}
	productProjects := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects", activeToken)
	if productProjects.Code != http.StatusOK ||
		!strings.Contains(productProjects.Body.String(), `"id":"prj_legacy"`) ||
		strings.Contains(productProjects.Body.String(), `"id":"prj_other"`) {
		t.Fatalf("tenant product projects status=%d body=%s", productProjects.Code, productProjects.Body.String())
	}
	productServices := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/services", activeToken)
	if productServices.Code != http.StatusOK ||
		!strings.Contains(productServices.Body.String(), `"id":"svc_legacy"`) ||
		strings.Contains(productServices.Body.String(), `"id":"svc_other"`) {
		t.Fatalf("tenant product services status=%d body=%s", productServices.Code, productServices.Body.String())
	}
	productEnvironments := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/environments", activeToken)
	if productEnvironments.Code != http.StatusOK ||
		!strings.Contains(productEnvironments.Body.String(), `"id":"env_restricted"`) ||
		strings.Contains(productEnvironments.Body.String(), `"id":"env_other"`) {
		t.Fatalf("tenant product environments status=%d body=%s", productEnvironments.Code, productEnvironments.Body.String())
	}
	productAccess := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects/prj_legacy/access", activeToken)
	if productAccess.Code != http.StatusOK ||
		!strings.Contains(productAccess.Body.String(), `"workspace_role":"admin"`) ||
		!strings.Contains(productAccess.Body.String(), `"project.read"`) {
		t.Fatalf("tenant product access status=%d body=%s", productAccess.Code, productAccess.Body.String())
	}
	crossProduct := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects/prj_other", activeToken)
	if crossProduct.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant product project status=%d body=%s", crossProduct.Code, crossProduct.Body.String())
	}
	viewerLogin, err := restartedStack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "viewer@example.com", Password: "viewer correct horse battery staple", ClientKind: "browser",
	})
	if err != nil {
		t.Fatalf("direct viewer login: %v", err)
	}
	viewerProjects := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects", viewerLogin.AccessToken)
	if viewerProjects.Code != http.StatusOK ||
		!strings.Contains(viewerProjects.Body.String(), `"id":"prj_legacy"`) ||
		strings.Contains(viewerProjects.Body.String(), `"id":"prj_other"`) {
		t.Fatalf("direct viewer projects status=%d body=%s", viewerProjects.Code, viewerProjects.Body.String())
	}
	viewerServices := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/services?project_id=prj_legacy", viewerLogin.AccessToken)
	if viewerServices.Code != http.StatusOK ||
		!strings.Contains(viewerServices.Body.String(), `"id":"svc_legacy"`) ||
		strings.Contains(viewerServices.Body.String(), `"id":"svc_other"`) {
		t.Fatalf("direct viewer services status=%d body=%s", viewerServices.Code, viewerServices.Body.String())
	}
	viewerService := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/services/svc_legacy", viewerLogin.AccessToken)
	if viewerService.Code != http.StatusOK || !strings.Contains(viewerService.Body.String(), `"id":"svc_legacy"`) {
		t.Fatalf("direct viewer service status=%d body=%s", viewerService.Code, viewerService.Body.String())
	}
	viewerEnvironments := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/environments?project_id=prj_legacy", viewerLogin.AccessToken)
	if viewerEnvironments.Code != http.StatusOK ||
		!strings.Contains(viewerEnvironments.Body.String(), `"id":"env_visible"`) ||
		strings.Contains(viewerEnvironments.Body.String(), `"id":"env_restricted"`) {
		t.Fatalf("direct viewer environments status=%d body=%s", viewerEnvironments.Code, viewerEnvironments.Body.String())
	}
	viewerRestricted := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/environments/env_restricted", viewerLogin.AccessToken)
	if viewerRestricted.Code != http.StatusNotFound {
		t.Fatalf("direct viewer restricted environment status=%d body=%s", viewerRestricted.Code, viewerRestricted.Body.String())
	}
	viewerRestrictedAccess := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects/prj_legacy/access?environment_id=env_restricted", viewerLogin.AccessToken)
	if viewerRestrictedAccess.Code != http.StatusNotFound {
		t.Fatalf("direct viewer restricted access status=%d body=%s", viewerRestrictedAccess.Code, viewerRestrictedAccess.Body.String())
	}

	revoke := authenticatedIdentityRequest(restarted, http.MethodDelete, identitySessionsPath+"/"+activeSessionID, activeToken)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	afterRevoke := authenticatedIdentityRequest(restarted, http.MethodGet, identityMePath, activeToken)
	if afterRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d body=%s", afterRevoke.Code, afterRevoke.Body.String())
	}
	afterProductRevoke := authenticatedIdentityRequest(restarted, http.MethodGet, v1alphaPrefix+"/projects", activeToken)
	if afterProductRevoke.Code != http.StatusUnauthorized {
		t.Fatalf("revoked product session status=%d body=%s", afterProductRevoke.Code, afterProductRevoke.Body.String())
	}
	boundedNow := now.Add(2 * time.Minute)
	boundedManager, err := identity.NewManager(identity.ManagerConfig{
		Store:       identity.NewPostgresStore(pool),
		KeyProvider: testVariableKeyProvider(t),
		MACKey:      deriveIdentitySessionMACKey([]byte("identity-integration-cursor-key-000000")),
		Clock:       func() time.Time { return boundedNow },
		MaxSessions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	boundedTokens := make([]string, 0, 3)
	for range 3 {
		code, err := bootstrap.GenerateTOTP(totpSecret, boundedNow)
		if err != nil {
			t.Fatal(err)
		}
		result, err := boundedManager.Login(ctx, identity.LoginRequest{
			Email: "owner@example.com", Password: "correct horse battery staple",
			TOTPCode: code, ClientKind: "api", RemoteAddr: "192.0.2.44:443",
		})
		if err != nil {
			t.Fatalf("bounded session login: %v", err)
		}
		boundedTokens = append(boundedTokens, result.AccessToken)
		boundedNow = boundedNow.Add(time.Second)
	}
	if _, _, err := boundedManager.Authenticate(ctx, "Bearer "+boundedTokens[0]); !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("oldest PostgreSQL session remained active: %v", err)
	}
	for _, token := range boundedTokens[1:] {
		if _, _, err := boundedManager.Authenticate(ctx, "Bearer "+token); err != nil {
			t.Fatalf("bounded PostgreSQL session rejected: %v", err)
		}
	}
	var activeSessions int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM auth_sessions
		WHERE user_id=$1
		  AND revoked_at IS NULL
		  AND expires_at>$2
		  AND idle_expires_at>$2`,
		claimed.User.ID,
		boundedNow,
	).Scan(&activeSessions); err != nil {
		t.Fatal(err)
	}
	if activeSessions != 2 {
		t.Fatalf("active PostgreSQL sessions=%d, want 2", activeSessions)
	}
	var identityAuditRows int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM audit_logs
		WHERE action IN (
			'identity.default-workspace.claim',
			'identity.login.succeeded',
			'identity.session.revoke'
		)
		  AND actor_id=$1
		  AND actor_type='user'`,
		claimed.User.ID,
	).Scan(&identityAuditRows); err != nil {
		t.Fatal(err)
	}
	if identityAuditRows != 3 {
		t.Fatalf("durable identity audit rows=%d, want 3", identityAuditRows)
	}
	for _, expectedVersion := range []int{53, 52, 51, 50, 49, 48, 47, 46, 45, 44, 43, 42, 41, 40} {
		down, err := runner.Down(ctx)
		if err != nil || down == nil || down.Version != expectedVersion {
			t.Fatalf(
				"roll back migration %d before scoped idempotency refusal: file=%#v err=%v",
				expectedVersion,
				down,
				err,
			)
		}
	}
	if down, err := runner.Down(ctx); err == nil || down != nil || !strings.Contains(err.Error(), "cannot downgrade scoped idempotency") {
		t.Fatalf("scoped idempotency rollback with actor attempts: file=%#v err=%v", down, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM idempotency_keys`); err != nil {
		t.Fatal(err)
	}
	if down, err := runner.Down(ctx); err != nil || down == nil || down.Version != 39 {
		t.Fatalf("roll back empty scoped idempotency migration: file=%#v err=%v", down, err)
	}
	if down, err := runner.Down(ctx); err == nil || down != nil || !strings.Contains(err.Error(), "auth_sessions contains rows") {
		t.Fatalf("identity rollback with durable sessions: file=%#v err=%v", down, err)
	}
	var schemaVersion int
	if err := pool.QueryRow(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != 38 {
		t.Fatalf("failed down changed schema version to %d", schemaVersion)
	}
}

func postgresIdentityIntegrationStack(t *testing.T, pool *pg.Pool, now time.Time) DataStack {
	t.Helper()
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("identity-integration-cursor-key-000000"),
		AutomationTokenMACKey: []byte("identity-integration-automation-key"),
		InvitationMACKey:      []byte("identity-integration-invitation-key"),
		VariableIntentMACKey:  []byte("identity-integration-variable-intent-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
		Clock:                 func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new postgres data stack: %v", err)
	}
	return stack
}
