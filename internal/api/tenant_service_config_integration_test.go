//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/migrations"
)

func TestTenantServiceConfigAuthorizationReplayRollbackAndSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	runner := &pg.MigrationRunner{
		Pool:         pool,
		FS:           migrations.FS,
		ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	password := "tenant service config correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_config_alice','alice-config@example.com','Alice',$1,1,$2,$2),
			('usr_config_viewer','viewer-config@example.com','Viewer',$1,1,$2,$2),
			('usr_config_editor','editor-config@example.com','Editor',$1,1,$2,$2),
			('usr_config_admin','admin-config@example.com','Admin',$1,1,$2,$2),
			('usr_config_session','session-config@example.com','Session',$1,1,$2,$2),
			('usr_config_grant','grant-config@example.com','Grant',$1,1,$2,$2),
			('usr_config_audit','audit-config@example.com','Audit',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_config_a','config-a','Config A',$1,$1),
			('wrk_config_b','config-b','Config B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_config_a','wrk_config_a','api','API',$1,$1),
			('prj_config_b','wrk_config_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, is_protected,
			access_restricted, created_at, updated_at
		) VALUES
			('env_config_main','prj_config_a','main','Main','development',FALSE,FALSE,$1,$1),
			('env_config_secondary','prj_config_a','secondary','Secondary','development',FALSE,FALSE,$1,$1),
			('env_config_restricted','prj_config_a','restricted','Restricted','production',TRUE,TRUE,$1,$1),
			('env_config_b','prj_config_b','main','Main','development',FALSE,FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES
			('svc_config_main','prj_config_a','main','Main',$1,$1),
			('svc_config_editor','prj_config_a','editor','Editor',$1,$1),
			('svc_config_session','prj_config_a','session','Session',$1,$1),
			('svc_config_grant','prj_config_a','grant','Grant',$1,$1),
			('svc_config_concurrent','prj_config_a','concurrent','Concurrent',$1,$1),
			('svc_config_target','prj_config_a','target','Target',$1,$1),
			('svc_config_audit','prj_config_a','audit','Audit',$1,$1),
			('svc_config_b','prj_config_b','main','Main',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('wrk_config_a','usr_config_alice','member',$1,$1,$1,$1),
			('wrk_config_a','usr_config_viewer','viewer',$1,$1,$1,$1),
			('wrk_config_a','usr_config_admin','admin',$1,$1,$1,$1),
			('wrk_config_a','usr_config_session','member',$1,$1,$1,$1),
			('wrk_config_a','usr_config_audit','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES
			('prj_config_a','usr_config_editor','editor',$1,$1,$1,$1,1),
			('prj_config_a','usr_config_grant','editor',$1,$1,$1,$1,1)`,
		now,
	); err != nil {
		t.Fatalf("seed project grants: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.TenantServiceConfigs.(*PostgresTenantMutationCoordinator)
	if !ok {
		t.Fatalf("tenant service config coordinator = %T", stack.TenantServiceConfigs)
	}
	configs, ok := stack.ServiceConfigStore.(*serviceconfig.Store)
	if !ok {
		t.Fatalf("service config store = %T", stack.ServiceConfigStore)
	}
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, loginErr := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email:      email,
			Password:   password,
			ClientKind: "api",
			RemoteAddr: "192.0.2.55:443",
			UserAgent:  "midnight-service-config-integration",
		})
		if loginErr != nil {
			t.Fatalf("login %s: %v", email, loginErr)
		}
		return result
	}
	aliceFirst := login("alice-config@example.com")
	aliceSecond := login("alice-config@example.com")
	viewer := login("viewer-config@example.com")
	editor := login("editor-config@example.com")
	admin := login("admin-config@example.com")
	sessionUser := login("session-config@example.com")
	grantUser := login("grant-config@example.com")
	auditUser := login("audit-config@example.com")

	getConfig := func(token, serviceID, environmentID string) tenantServiceConfigHTTPResponse {
		t.Helper()
		response := doRouterRequest(
			handler,
			http.MethodGet,
			tenantServiceConfigIdempotencyPath(serviceID, environmentID),
			"",
			map[string]string{"Authorization": "Bearer " + token},
		)
		return tenantServiceConfigHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}
	putConfig := func(token, key, serviceID, environmentID, body string) tenantServiceConfigHTTPResponse {
		t.Helper()
		response := doRouterRequest(
			handler,
			http.MethodPut,
			tenantServiceConfigIdempotencyPath(serviceID, environmentID),
			body,
			integrationVersionHeaders(body, map[string]string{
				"Authorization":   "Bearer " + token,
				"Idempotency-Key": key,
			}),
		)
		return tenantServiceConfigHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}
	configBody := func(expectedVersion int64, port int) string {
		return fmt.Sprintf(
			`{"expected_version":%d,"target_port":%d}`,
			expectedVersion,
			port,
		)
	}

	viewerDefault := getConfig(
		viewer.AccessToken,
		"svc_config_main",
		"env_config_main",
	)
	if viewerDefault.Code != http.StatusOK {
		t.Fatalf("viewer default GET = %#v", viewerDefault)
	}
	var defaultView ServiceConfigView
	if err := json.Unmarshal([]byte(viewerDefault.Body), &defaultView); err != nil {
		t.Fatal(err)
	}
	if defaultView.Explicit ||
		defaultView.Version != 0 ||
		defaultView.ProjectID != "prj_config_a" ||
		defaultView.ServiceID != "svc_config_main" ||
		defaultView.EnvironmentID != "env_config_main" ||
		defaultView.TargetPort != serviceconfig.DefaultTargetPort {
		t.Fatalf("default view = %#v", defaultView)
	}
	viewerDenied := putConfig(
		viewer.AccessToken,
		"config-viewer-denied",
		"svc_config_main",
		"env_config_secondary",
		configBody(0, 8082),
	)
	if viewerDenied.Code != http.StatusForbidden || viewerDenied.Replayed != "" {
		t.Fatalf("viewer PUT = %#v", viewerDenied)
	}

	editorAllowed := putConfig(
		editor.AccessToken,
		"config-editor-allowed",
		"svc_config_editor",
		"env_config_main",
		configBody(0, 8083),
	)
	if editorAllowed.Code != http.StatusOK {
		t.Fatalf("editor PUT = %#v", editorAllowed)
	}

	restrictedMemberGet := getConfig(
		aliceFirst.AccessToken,
		"svc_config_main",
		"env_config_restricted",
	)
	restrictedMemberPut := putConfig(
		aliceFirst.AccessToken,
		"config-restricted-member",
		"svc_config_main",
		"env_config_restricted",
		configBody(0, 8443),
	)
	if restrictedMemberGet.Code != http.StatusNotFound ||
		restrictedMemberPut.Code != http.StatusNotFound {
		t.Fatalf(
			"restricted member GET=%#v PUT=%#v",
			restrictedMemberGet,
			restrictedMemberPut,
		)
	}
	restrictedAdminGet := getConfig(
		admin.AccessToken,
		"svc_config_main",
		"env_config_restricted",
	)
	restrictedAdminPut := putConfig(
		admin.AccessToken,
		"config-restricted-admin",
		"svc_config_main",
		"env_config_restricted",
		configBody(0, 8443),
	)
	if restrictedAdminGet.Code != http.StatusOK ||
		restrictedAdminPut.Code != http.StatusOK {
		t.Fatalf(
			"restricted admin GET=%#v PUT=%#v",
			restrictedAdminGet,
			restrictedAdminPut,
		)
	}

	for name, target := range map[string][2]string{
		"foreign environment": {"svc_config_main", "env_config_b"},
		"foreign service":     {"svc_config_b", "env_config_main"},
		"foreign workspace":   {"svc_config_b", "env_config_b"},
	} {
		t.Run(name+" concealed", func(t *testing.T) {
			response := getConfig(aliceFirst.AccessToken, target[0], target[1])
			if response.Code != http.StatusNotFound ||
				strings.Contains(response.Body, "prj_config_b") ||
				strings.Contains(response.Body, "wrk_config_b") {
				t.Fatalf("response = %#v", response)
			}
		})
	}
	foreignPut := putConfig(
		aliceFirst.AccessToken,
		"config-foreign-workspace",
		"svc_config_b",
		"env_config_b",
		configBody(0, 8084),
	)
	if foreignPut.Code != http.StatusNotFound || foreignPut.Replayed != "" {
		t.Fatalf("foreign workspace PUT = %#v", foreignPut)
	}

	aliceBody := configBody(0, 8081)
	first := putConfig(
		aliceFirst.AccessToken,
		"config-shared-key",
		"svc_config_main",
		"env_config_main",
		aliceBody,
	)
	if first.Code != http.StatusOK || first.Replayed != "" {
		t.Fatalf("alice PUT = %#v", first)
	}
	replay := putConfig(
		aliceSecond.AccessToken,
		"config-shared-key",
		"svc_config_main",
		"env_config_main",
		aliceBody,
	)
	if replay.Code != first.Code ||
		replay.Body != first.Body ||
		replay.Replayed != "true" {
		t.Fatalf("cross-session replay=%#v first=%#v", replay, first)
	}
	differentBody := putConfig(
		aliceSecond.AccessToken,
		"config-shared-key",
		"svc_config_main",
		"env_config_main",
		configBody(1, 8181),
	)
	if differentBody.Code != http.StatusConflict ||
		!strings.Contains(differentBody.Body, string(idempotencyFailureCodeDifferentBody)) ||
		differentBody.Replayed != "" {
		t.Fatalf("same key different body = %#v", differentBody)
	}

	targetBody := configBody(0, 8282)
	targetMain := putConfig(
		aliceFirst.AccessToken,
		"config-target-bound-key",
		"svc_config_target",
		"env_config_main",
		targetBody,
	)
	targetSecondary := putConfig(
		aliceFirst.AccessToken,
		"config-target-bound-key",
		"svc_config_target",
		"env_config_secondary",
		targetBody,
	)
	if targetMain.Code != http.StatusOK ||
		targetSecondary.Code != http.StatusOK ||
		targetMain.Replayed != "" ||
		targetSecondary.Replayed != "" {
		t.Fatalf(
			"target-bound main=%#v secondary=%#v",
			targetMain,
			targetSecondary,
		)
	}
	var targetMainView, targetSecondaryView ServiceConfigView
	if err := json.Unmarshal([]byte(targetMain.Body), &targetMainView); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(targetSecondary.Body), &targetSecondaryView); err != nil {
		t.Fatal(err)
	}
	if targetMainView.EnvironmentID != "env_config_main" ||
		targetSecondaryView.EnvironmentID != "env_config_secondary" {
		t.Fatalf(
			"cross-target response replay main=%#v secondary=%#v",
			targetMainView,
			targetSecondaryView,
		)
	}

	sessionBody := configBody(0, 8381)
	sessionCreated := putConfig(
		sessionUser.AccessToken,
		"config-session-key",
		"svc_config_session",
		"env_config_main",
		sessionBody,
	)
	if sessionCreated.Code != http.StatusOK {
		t.Fatalf("session config create = %#v", sessionCreated)
	}
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
		"service-config-session-stale",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	staleSessionReplay := putConfig(
		sessionUser.AccessToken,
		"config-session-key",
		"svc_config_session",
		"env_config_main",
		sessionBody,
	)
	if staleSessionReplay.Code != http.StatusUnauthorized ||
		staleSessionReplay.Replayed != "" {
		t.Fatalf("stale session replay = %#v", staleSessionReplay)
	}
	_, err = coordinator.PutServiceConfig(ctx, TenantServiceConfigPutRequest{
		Principal: sessionPrincipal,
		Session:   sessionUser.Session,
		Config: serviceconfig.Config{
			ServiceID: "svc_config_session", EnvironmentID: "env_config_main",
		},
		Key: "config-session-direct",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPut,
			Path: tenantServiceConfigIdempotencyPath(
				"svc_config_session",
				"env_config_main",
			),
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{}`)),
		},
	})
	if !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("coordinator accepted revoked session: %v", err)
	}

	grantBody := configBody(0, 8382)
	grantCreated := putConfig(
		grantUser.AccessToken,
		"config-grant-key",
		"svc_config_grant",
		"env_config_main",
		grantBody,
	)
	if grantCreated.Code != http.StatusOK {
		t.Fatalf("grant config create = %#v", grantCreated)
	}
	grantTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"workspace-members:wrk_config_a",
	); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"project-members:prj_config_a",
	); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `
		DELETE FROM project_members
		WHERE project_id='prj_config_a'
		  AND user_id='usr_config_grant'`); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := grantTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	staleGrantReplay := putConfig(
		grantUser.AccessToken,
		"config-grant-key",
		"svc_config_grant",
		"env_config_main",
		grantBody,
	)
	if staleGrantReplay.Code != http.StatusNotFound ||
		staleGrantReplay.Replayed != "" ||
		staleGrantReplay.Body == grantCreated.Body {
		t.Fatalf("stale grant replay = %#v", staleGrantReplay)
	}

	concurrentBody := configBody(0, 8484)
	start := make(chan struct{})
	responses := make(chan tenantServiceConfigHTTPResponse, 2)
	for _, token := range []string{aliceFirst.AccessToken, aliceSecond.AccessToken} {
		go func(accessToken string) {
			<-start
			responses <- putConfig(
				accessToken,
				"config-concurrent-key",
				"svc_config_concurrent",
				"env_config_main",
				concurrentBody,
			)
		}(token)
	}
	close(start)
	concurrentFirst, concurrentSecond := <-responses, <-responses
	if concurrentFirst.Code != http.StatusOK ||
		concurrentSecond.Code != http.StatusOK ||
		concurrentFirst.Body != concurrentSecond.Body ||
		(concurrentFirst.Replayed == "true") == (concurrentSecond.Replayed == "true") {
		t.Fatalf(
			"concurrent responses first=%#v second=%#v",
			concurrentFirst,
			concurrentSecond,
		)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (id, kind, state)
		VALUES ('op_config_snapshot','deploy','QUEUED');
		INSERT INTO deployments (
			id, project_id, service_id, environment_id,
			action, state, operation_id
		) VALUES (
			'dep_config_snapshot',
			'prj_config_a',
			'svc_config_main',
			'env_config_main',
			'deploy_image',
			'queued',
			'op_config_snapshot'
		)`); err != nil {
		t.Fatalf("seed snapshot deployment: %v", err)
	}
	snapshot, err := configs.ResolveDeployment(
		ctx,
		"dep_config_snapshot",
		"prj_config_a",
		"env_config_main",
		"svc_config_main",
	)
	if err != nil {
		t.Fatalf("resolve initial deployment snapshot: %v", err)
	}
	if snapshot.ConfigVersion != 1 || snapshot.Config.TargetPort != 8081 {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	update := putConfig(
		aliceFirst.AccessToken,
		"config-update-key",
		"svc_config_main",
		"env_config_main",
		configBody(1, 8181),
	)
	if update.Code != http.StatusOK {
		t.Fatalf("config update = %#v", update)
	}
	snapshotReplay, err := configs.ResolveDeployment(
		ctx,
		"dep_config_snapshot",
		"prj_config_a",
		"env_config_main",
		"svc_config_main",
	)
	if err != nil {
		t.Fatalf("resolve deployment snapshot replay: %v", err)
	}
	if snapshotReplay.SHA256 != snapshot.SHA256 ||
		snapshotReplay.ConfigVersion != 1 ||
		snapshotReplay.Config.TargetPort != 8081 {
		t.Fatalf(
			"snapshot drifted first=%#v replay=%#v",
			snapshot,
			snapshotReplay,
		)
	}

	casBody := configBody(0, 8585)
	casConflict := putConfig(
		aliceFirst.AccessToken,
		"config-cas-conflict",
		"svc_config_main",
		"env_config_main",
		casBody,
	)
	casReplay := putConfig(
		aliceSecond.AccessToken,
		"config-cas-conflict",
		"svc_config_main",
		"env_config_main",
		casBody,
	)
	if casConflict.Code != http.StatusPreconditionFailed ||
		casConflict.Replayed != "" ||
		casReplay.Code != casConflict.Code ||
		casReplay.Body != casConflict.Body ||
		casReplay.Replayed != "true" {
		t.Fatalf(
			"CAS conflict=%#v replay=%#v",
			casConflict,
			casReplay,
		)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_service_config_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action='service.config.put'
			   AND NEW.actor_id='usr_config_audit' THEN
				RAISE EXCEPTION 'forced service config audit failure';
			END IF;
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_service_config_audit
			BEFORE INSERT ON audit_logs
			FOR EACH ROW EXECUTE FUNCTION fail_service_config_audit()`); err != nil {
		t.Fatalf("install service config audit failure trigger: %v", err)
	}
	auditFailure := putConfig(
		auditUser.AccessToken,
		"config-audit-failure",
		"svc_config_audit",
		"env_config_main",
		configBody(0, 8686),
	)
	if auditFailure.Code != http.StatusServiceUnavailable ||
		auditFailure.Replayed != "" {
		t.Fatalf("audit failure = %#v", auditFailure)
	}

	var (
		auditFailureConfigs int
		auditFailureKeys    int
		auditFailureAudits  int
		deniedKeys          int
		concurrentConfigs   int
		concurrentKeys      int
		mainVersion         int64
		mainPort            int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM service_environment_configs
			 WHERE service_id='svc_config_audit'
			   AND environment_id='env_config_main'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='config-audit-failure'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='service.config.put'
			   AND actor_id='usr_config_audit'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
			   'config-viewer-denied',
			   'config-restricted-member',
			   'config-foreign-workspace',
			   'config-session-direct'
			 )),
			(SELECT COUNT(*) FROM service_environment_configs
			 WHERE service_id='svc_config_concurrent'
			   AND environment_id='env_config_main'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='config-concurrent-key'
			   AND state='SUCCEEDED'),
			(SELECT version FROM service_environment_configs
			 WHERE service_id='svc_config_main'
			   AND environment_id='env_config_main'),
			(SELECT target_port FROM service_environment_configs
			 WHERE service_id='svc_config_main'
			   AND environment_id='env_config_main')`,
	).Scan(
		&auditFailureConfigs,
		&auditFailureKeys,
		&auditFailureAudits,
		&deniedKeys,
		&concurrentConfigs,
		&concurrentKeys,
		&mainVersion,
		&mainPort,
	); err != nil {
		t.Fatal(err)
	}
	if auditFailureConfigs != 0 ||
		auditFailureKeys != 0 ||
		auditFailureAudits != 0 ||
		deniedKeys != 0 ||
		concurrentConfigs != 1 ||
		concurrentKeys != 1 ||
		mainVersion != 2 ||
		mainPort != 8181 {
		t.Fatalf(
			"artifacts audit=(%d,%d,%d) denied=%d concurrent=(%d,%d) main=(%d,%d)",
			auditFailureConfigs,
			auditFailureKeys,
			auditFailureAudits,
			deniedKeys,
			concurrentConfigs,
			concurrentKeys,
			mainVersion,
			mainPort,
		)
	}
}

type tenantServiceConfigHTTPResponse struct {
	Code     int
	Body     string
	Replayed string
}

const idempotencyFailureCodeDifferentBody = "IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_BODY"
