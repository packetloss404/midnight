//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTenantDeploymentAndOperationProgressAuthorization(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)
	password := "tenant progress correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_progress_member','member-progress@example.com','Member',$1,1,$2,$2),
			('usr_progress_viewer','viewer-progress@example.com','Viewer',$1,1,$2,$2),
			('usr_progress_grant','grant-progress@example.com','Grant',$1,1,$2,$2),
			('usr_progress_session','session-progress@example.com','Session',$1,1,$2,$2),
			('usr_progress_admin','admin-progress@example.com','Admin',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_progress_a','progress-a','Progress A',$1,$1),
			('wrk_progress_b','progress-b','Progress B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_progress_a','wrk_progress_a','app','App',$1,$1),
			('prj_progress_b','wrk_progress_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('wrk_progress_a','usr_progress_member','member',$1,$1,$1,$1),
			('wrk_progress_a','usr_progress_viewer','viewer',$1,$1,$1,$1),
			('wrk_progress_a','usr_progress_session','member',$1,$1,$1,$1),
			('wrk_progress_a','usr_progress_admin','admin',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES
			('prj_progress_a','usr_progress_grant','editor',$1,$1,$1,$1,1)`,
		now,
	); err != nil {
		t.Fatalf("seed project grant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted,
			created_at, updated_at
		) VALUES
			('env_progress_public','prj_progress_a','production','Production','production',FALSE,$1,$1),
			('env_progress_restricted','prj_progress_a','secret','Secret','staging',TRUE,$1,$1),
			('env_progress_foreign','prj_progress_b','production','Production','production',FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES
			('svc_progress_a','prj_progress_a','api','API',$1,$1),
			('svc_progress_page','prj_progress_a','page-api','Page API',$1,$1),
			('svc_progress_b','prj_progress_b','foreign-api','Foreign API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_accounts (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES (
			'sa_progress_ci','wrk_progress_a','ci','CI',$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed service account: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	stack.APIBearerToken = "tenant-progress-operator"
	operationStore, ok := stack.OperationStore.(*PostgresOperationStore)
	if !ok {
		t.Fatalf("OperationStore=%T, want *PostgresOperationStore", stack.OperationStore)
	}
	deploymentStore, ok := stack.DeploymentStore.(*PostgresDeploymentStore)
	if !ok {
		t.Fatalf("DeploymentStore=%T, want *PostgresDeploymentStore", stack.DeploymentStore)
	}
	seedProgressDeployment(
		t,
		ctx,
		operationStore,
		deploymentStore,
		"op_progress_public",
		"dep_progress_public",
		"prj_progress_a",
		"svc_progress_a",
		"env_progress_public",
		now,
	)
	seedProgressDeployment(
		t,
		ctx,
		operationStore,
		deploymentStore,
		"op_progress_restricted",
		"dep_progress_restricted",
		"prj_progress_a",
		"svc_progress_a",
		"env_progress_restricted",
		now.Add(time.Minute),
	)
	seedProgressDeployment(
		t,
		ctx,
		operationStore,
		deploymentStore,
		"op_progress_foreign",
		"dep_progress_foreign",
		"prj_progress_b",
		"svc_progress_b",
		"env_progress_foreign",
		now.Add(2*time.Minute),
	)
	for index, deploymentID := range []string{
		"dep_progress_page_1",
		"dep_progress_page_2",
		"dep_progress_page_3",
	} {
		deploymentStore.Now = func() time.Time {
			return now.Add(time.Duration(index+10) * time.Minute)
		}
		if _, err := deploymentStore.CreateDeployment(ctx, DeploymentRecord{
			ID:            deploymentID,
			ProjectID:     "prj_progress_a",
			ServiceID:     "svc_progress_page",
			EnvironmentID: "env_progress_public",
			Action:        deploy.ActionDeployImage,
		}); err != nil {
			t.Fatalf("create paged deployment %s: %v", deploymentID, err)
		}
	}

	login := func(email string) identity.LoginResult {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email:      email,
			Password:   password,
			ClientKind: "api",
			RemoteAddr: "192.0.2.40:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return result
	}
	member := login("member-progress@example.com")
	viewer := login("viewer-progress@example.com")
	grant := login("grant-progress@example.com")
	sessionUser := login("session-progress@example.com")
	admin := login("admin-progress@example.com")

	handler := NewRouterWithDataStack(stack)
	servicePath := v1alphaPrefix + "/services/svc_progress_a/deployments"
	pagedServicePath := v1alphaPrefix + "/services/svc_progress_page/deployments"

	firstPaged := tenantProgressRequest(
		handler,
		pagedServicePath+"?environment_id=env_progress_public&limit=2",
		member.AccessToken,
	)
	if firstPaged.Code != http.StatusOK {
		t.Fatalf(
			"first bounded deployment page status=%d body=%s",
			firstPaged.Code,
			firstPaged.Body.String(),
		)
	}
	var boundedPage DeploymentResourcePage
	if err := json.Unmarshal(firstPaged.Body.Bytes(), &boundedPage); err != nil {
		t.Fatal(err)
	}
	if len(boundedPage.Items) != 2 ||
		boundedPage.Items[0].ID != "dep_progress_page_3" ||
		boundedPage.Items[1].ID != "dep_progress_page_2" ||
		boundedPage.NextCursor == "" {
		t.Fatalf("first bounded deployment page=%#v", boundedPage)
	}
	secondPaged := tenantProgressRequest(
		handler,
		pagedServicePath+"?environment_id=env_progress_public&limit=2&cursor="+
			url.QueryEscape(boundedPage.NextCursor),
		member.AccessToken,
	)
	if secondPaged.Code != http.StatusOK {
		t.Fatalf(
			"second bounded deployment page status=%d body=%s",
			secondPaged.Code,
			secondPaged.Body.String(),
		)
	}
	boundedPage = DeploymentResourcePage{}
	if err := json.Unmarshal(secondPaged.Body.Bytes(), &boundedPage); err != nil {
		t.Fatal(err)
	}
	if len(boundedPage.Items) != 1 ||
		boundedPage.Items[0].ID != "dep_progress_page_1" ||
		boundedPage.NextCursor != "" {
		t.Fatalf("second bounded deployment page=%#v", boundedPage)
	}

	memberList := tenantProgressRequest(
		handler,
		servicePath+"?environment_id=env_progress_public&limit=10",
		member.AccessToken,
	)
	if memberList.Code != http.StatusOK {
		t.Fatalf(
			"member deployment list status=%d body=%s",
			memberList.Code,
			memberList.Body.String(),
		)
	}
	var deploymentPage DeploymentResourcePage
	if err := json.Unmarshal(memberList.Body.Bytes(), &deploymentPage); err != nil {
		t.Fatal(err)
	}
	listed := deploymentPage.Items
	if len(listed) != 1 ||
		listed[0].ID != "dep_progress_public" ||
		listed[0].EnvironmentID != "env_progress_public" {
		t.Fatalf("member deployment list=%#v", listed)
	}
	unfiltered := tenantProgressRequest(handler, servicePath, member.AccessToken)
	if unfiltered.Code != http.StatusOK {
		t.Fatalf(
			"member unfiltered list status=%d body=%s",
			unfiltered.Code,
			unfiltered.Body.String(),
		)
	}
	deploymentPage = DeploymentResourcePage{}
	if err := json.Unmarshal(unfiltered.Body.Bytes(), &deploymentPage); err != nil {
		t.Fatal(err)
	}
	listed = deploymentPage.Items
	if len(listed) != 1 || listed[0].ID != "dep_progress_public" {
		t.Fatalf("member unfiltered deployments=%#v, restricted row leaked", listed)
	}
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			servicePath+"/latest-active?environment_id=env_progress_public",
			member.AccessToken,
		),
		http.StatusOK,
		"member latest public deployment",
	)
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			servicePath+"/dep_progress_restricted?environment_id=env_progress_restricted",
			member.AccessToken,
		),
		http.StatusNotFound,
		"member restricted deployment",
	)
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/services/svc_progress_b/deployments",
			member.AccessToken,
		),
		http.StatusNotFound,
		"member foreign service deployments",
	)

	for _, suffix := range []string{"", "/events", "/logs"} {
		assertTenantProgressStatus(
			t,
			tenantProgressRequest(
				handler,
				v1alphaPrefix+"/operations/op_progress_public"+suffix,
				member.AccessToken,
			),
			http.StatusOK,
			"member operation"+suffix,
		)
	}
	for _, operationID := range []string{
		"op_progress_restricted",
		"op_progress_foreign",
	} {
		for _, suffix := range []string{"", "/events", "/logs"} {
			assertTenantProgressStatus(
				t,
				tenantProgressRequest(
					handler,
					v1alphaPrefix+"/operations/"+operationID+suffix,
					member.AccessToken,
				),
				http.StatusNotFound,
				"member concealed operation "+operationID+suffix,
			)
		}
	}
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public",
			viewer.AccessToken,
		),
		http.StatusOK,
		"viewer operation status",
	)
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public/events",
			viewer.AccessToken,
		),
		http.StatusOK,
		"viewer operation events",
	)
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public/logs",
			viewer.AccessToken,
		),
		http.StatusNotFound,
		"viewer logs capability denial",
	)

	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public",
			sessionUser.AccessToken,
		),
		http.StatusOK,
		"session user before revocation",
	)
	sessionPrincipal, _, err := stack.IdentitySessions.Authenticate(
		ctx,
		"Bearer "+sessionUser.AccessToken,
	)
	if err != nil {
		t.Fatalf("authenticate session user: %v", err)
	}
	if err := stack.IdentitySessions.Revoke(
		ctx,
		sessionPrincipal,
		sessionUser.Session.ID,
		"tenant-progress-test",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public",
			sessionUser.AccessToken,
		),
		http.StatusUnauthorized,
		"revoked session operation",
	)

	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public/logs",
			grant.AccessToken,
		),
		http.StatusOK,
		"project editor before grant removal",
	)
	grantPage := tenantProgressRequest(
		handler,
		pagedServicePath+"?environment_id=env_progress_public&limit=1",
		grant.AccessToken,
	)
	if grantPage.Code != http.StatusOK {
		t.Fatalf(
			"project editor deployment page status=%d body=%s",
			grantPage.Code,
			grantPage.Body.String(),
		)
	}
	var grantDeploymentPage DeploymentResourcePage
	if err := json.Unmarshal(grantPage.Body.Bytes(), &grantDeploymentPage); err != nil {
		t.Fatal(err)
	}
	if grantDeploymentPage.NextCursor == "" {
		t.Fatalf("project editor deployment page=%#v, want continuation", grantDeploymentPage)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM project_members
		WHERE project_id='prj_progress_a'
		  AND user_id='usr_progress_grant'`); err != nil {
		t.Fatalf("remove project grant: %v", err)
	}
	for _, suffix := range []string{"", "/events", "/logs"} {
		removedGrant := tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_public"+suffix,
			grant.AccessToken,
		)
		if removedGrant.Code != http.StatusNotFound ||
			strings.Contains(removedGrant.Body.String(), "prj_progress_a") ||
			strings.Contains(removedGrant.Body.String(), "svc_progress_a") {
			t.Fatalf(
				"removed grant%s status=%d body=%s, want concealed 404",
				suffix,
				removedGrant.Code,
				removedGrant.Body.String(),
			)
		}
	}
	removedGrantPage := tenantProgressRequest(
		handler,
		pagedServicePath+"?environment_id=env_progress_public&limit=1&cursor="+
			url.QueryEscape(grantDeploymentPage.NextCursor),
		grant.AccessToken,
	)
	if removedGrantPage.Code != http.StatusNotFound ||
		strings.Contains(removedGrantPage.Body.String(), "dep_progress_page") {
		t.Fatalf(
			"removed grant deployment continuation status=%d body=%s, want concealed 404",
			removedGrantPage.Code,
			removedGrantPage.Body.String(),
		)
	}

	adminRestricted := tenantProgressRequest(
		handler,
		servicePath+"?environment_id=env_progress_restricted",
		admin.AccessToken,
	)
	if adminRestricted.Code != http.StatusOK {
		t.Fatalf(
			"admin restricted list status=%d body=%s",
			adminRestricted.Code,
			adminRestricted.Body.String(),
		)
	}
	deploymentPage = DeploymentResourcePage{}
	if err := json.Unmarshal(adminRestricted.Body.Bytes(), &deploymentPage); err != nil {
		t.Fatal(err)
	}
	listed = deploymentPage.Items
	if len(listed) != 1 || listed[0].ID != "dep_progress_restricted" {
		t.Fatalf("admin restricted deployments=%#v", listed)
	}
	assertTenantProgressStatus(
		t,
		tenantProgressRequest(
			handler,
			v1alphaPrefix+"/operations/op_progress_restricted/logs",
			admin.AccessToken,
		),
		http.StatusOK,
		"admin restricted operation logs",
	)

	operatorAuthorization := "Bearer tenant-progress-operator"
	assertTenantProgressStatus(
		t,
		doRouterRequest(
			handler,
			http.MethodGet,
			servicePath+"?environment_id=env_progress_restricted",
			"",
			map[string]string{"Authorization": operatorAuthorization},
		),
		http.StatusOK,
		"operator restricted deployment list",
	)
	assertTenantProgressStatus(
		t,
		doRouterRequest(
			handler,
			http.MethodGet,
			v1alphaPrefix+"/operations/op_progress_foreign/logs",
			"",
			map[string]string{"Authorization": operatorAuthorization},
		),
		http.StatusOK,
		"operator foreign operation logs",
	)

	tokenManager, ok := stack.AutomationTokens.(*automationtoken.Manager)
	if !ok {
		t.Fatalf("AutomationTokens=%T, want *automationtoken.Manager", stack.AutomationTokens)
	}
	minted, err := tokenManager.Mint(ctx, automationtoken.MintRequest{
		ServiceAccountID: "sa_progress_ci",
		WorkspaceID:      "wrk_progress_a",
		ProjectID:        "prj_progress_a",
		Name:             "progress reader",
		Profile:          automationtoken.ProfileCIDeployV1,
		CreatedByUserID:  "usr_progress_admin",
		ExpiresAt:        now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("mint automation token: %v", err)
	}
	for _, path := range []string{
		servicePath + "?environment_id=env_progress_public",
		v1alphaPrefix + "/operations/op_progress_public",
		v1alphaPrefix + "/operations/op_progress_public/events",
		v1alphaPrefix + "/operations/op_progress_public/logs",
	} {
		assertTenantProgressStatus(
			t,
			tenantProgressRequest(handler, path, minted.AccessToken),
			http.StatusOK,
			"automation own-scope read "+path,
		)
	}
	for _, path := range []string{
		v1alphaPrefix + "/services/svc_progress_b/deployments",
		v1alphaPrefix + "/operations/op_progress_foreign",
	} {
		assertTenantProgressStatus(
			t,
			tenantProgressRequest(handler, path, minted.AccessToken),
			http.StatusNotFound,
			"automation cross-scope read "+path,
		)
	}
}

func seedProgressDeployment(
	t *testing.T,
	ctx context.Context,
	operations *PostgresOperationStore,
	deployments *PostgresDeploymentStore,
	operationID string,
	deploymentID string,
	projectID string,
	serviceID string,
	environmentID string,
	createdAt time.Time,
) {
	t.Helper()
	operations.Now = func() time.Time { return createdAt }
	deployments.Now = func() time.Time { return createdAt }
	operation, err := operations.Create(ctx, orailtypes.Operation{
		ID:    orailtypes.ID(operationID),
		Kind:  "deploy",
		State: orailtypes.OperationSucceeded,
	})
	if err != nil {
		t.Fatalf("create operation %s: %v", operationID, err)
	}
	if _, err := operations.AppendEvent(ctx, OperationEvent{
		OperationID: operation.ID,
		State:       operation.State,
		Message: "deploy.intake service_id=" + serviceID +
			" environment_id=" + environmentID +
			" deployment_id=" + deploymentID,
		CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("append operation event %s: %v", operationID, err)
	}
	if _, err := deployments.CreateDeployment(ctx, DeploymentRecord{
		ID:            deploymentID,
		ProjectID:     projectID,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		Action:        deploy.ActionDeployImage,
		OperationID:   operationID,
	}); err != nil {
		t.Fatalf("create deployment %s: %v", deploymentID, err)
	}
	if err := deployments.MarkDeploymentBuilding(ctx, deploymentID); err != nil {
		t.Fatalf("mark deployment building %s: %v", deploymentID, err)
	}
	if err := deployments.RecordDeploymentBuild(
		ctx,
		deploymentID,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"dockerfile",
		"registry.example/"+serviceID,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); err != nil {
		t.Fatalf("record deployment build %s: %v", deploymentID, err)
	}
	if err := deployments.MarkDeploymentDeploying(
		ctx,
		deploymentID,
		deploy.DeploymentBuilding,
	); err != nil {
		t.Fatalf("mark deployment deploying %s: %v", deploymentID, err)
	}
	if err := deployments.PromoteDeployment(ctx, deploymentID, ""); err != nil {
		t.Fatalf("promote deployment %s: %v", deploymentID, err)
	}
}

func tenantProgressRequest(
	handler http.Handler,
	path string,
	token string,
) *httptest.ResponseRecorder {
	return doRouterRequest(
		handler,
		http.MethodGet,
		path,
		"",
		map[string]string{"Authorization": "Bearer " + token},
	)
}

func assertTenantProgressStatus(
	t *testing.T,
	response *httptest.ResponseRecorder,
	want int,
	label string,
) {
	t.Helper()
	if response.Code != want {
		t.Fatalf(
			"%s status=%d body=%s, want %d",
			label,
			response.Code,
			response.Body.String(),
			want,
		)
	}
}
