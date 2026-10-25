//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/migrations"
)

func TestTenantGeneratedDomainLifecycleAndCurrentActiveAttachment(t *testing.T) {
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
		Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml",
	}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	password := "tenant generated domain correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO users (
			id,email,display_name,password_hash,session_generation,created_at,updated_at
		) VALUES
			('usr_domain_member','member-domain@example.com','Member',$1,1,$2,$2),
			('usr_domain_viewer','viewer-domain@example.com','Viewer',$1,1,$2,$2)`,
		`INSERT INTO workspaces (id,slug,display_name,created_at,updated_at)
		 VALUES ('wrk_domain_a','domains-a','Domains A',$1,$1)`,
		`INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at)
		 VALUES ('prj_domain_a','wrk_domain_a','api','API',$1,$1)`,
		`INSERT INTO environments (
			id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at
		 ) VALUES ('env_domain_main','prj_domain_a','main','Main','development',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
		 VALUES
			('svc_domain_idle','prj_domain_a','idle','Idle',$1,$1),
			('svc_domain_active','prj_domain_a','active','Active',$1,$1),
			('svc_domain_recovery','prj_domain_a','recovery','Recovery',$1,$1)`,
		`INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		 ) VALUES
			('wrk_domain_a','usr_domain_member','member',$1,$1,$1,$1),
			('wrk_domain_a','usr_domain_viewer','viewer',$1,$1,$1,$1)`,
	} {
		args := []any{now}
		if strings.Contains(statement, "password_hash") {
			args = []any{passwordHash, now}
		}
		if _, err := pool.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed tenant graph: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,
			created_at,updated_at
		) VALUES (
			'dep_domain_idle_queued','prj_domain_a','svc_domain_idle',
			'env_domain_main','deploy_image','queued',$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed queued idle deployment: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.TenantGeneratedDomains.(*PostgresTenantMutationCoordinator)
	if !ok {
		t.Fatalf("tenant generated domain coordinator=%T", stack.TenantGeneratedDomains)
	}
	coordinator.AppsDomain = "apps.midnight.test"
	stack.AppsDomain = coordinator.AppsDomain
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api",
			RemoteAddr: "192.0.2.44:443", UserAgent: "tenant-domain-integration",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return result
	}
	member := login("member-domain@example.com")
	viewer := login("viewer-domain@example.com")
	request := func(token, method, path, key, body string) *routerResponse {
		t.Helper()
		headers := map[string]string{"Authorization": "Bearer " + token}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		response := doRouterRequest(handler, method, path, body, integrationVersionHeaders(body, headers))
		return &routerResponse{
			Code: response.Code, Body: response.Body.String(),
			Header: response.Header(),
		}
	}

	recoveryScope := router.RouteScope{
		ProjectID:     "prj_domain_a",
		EnvironmentID: "env_domain_main",
		ServiceID:     "svc_domain_recovery",
	}
	recoveryDomain, seedRecoveryRoute, err := router.BuildDomainRouteIntent(
		router.DomainRouteIntentInput{
			Scope:      recoveryScope,
			Kind:       router.DomainKindGenerated,
			AppsDomain: coordinator.AppsDomain,
			PathPrefix: "/",
			TargetPort: 8080,
			Public:     true,
			TLSDesired: false,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	recoveryDomain.ID = "dom_recovery_actual"
	seedRecoveryRoute.DomainID = recoveryDomain.ID
	recoveryDomain, err = coordinator.Routes.PutDomain(ctx, recoveryDomain)
	if err != nil {
		t.Fatalf("seed recovery generated domain: %v", err)
	}
	if err := coordinator.Routes.EnsureDomainLifecycle(ctx, recoveryDomain); err != nil {
		t.Fatalf("seed recovery generated lifecycle: %v", err)
	}
	if _, err := coordinator.Routes.PutHTTPRoute(ctx, seedRecoveryRoute); err != nil {
		t.Fatalf("seed recovery generated route: %v", err)
	}
	recoveryAdopted := request(
		member.AccessToken,
		http.MethodPost,
		tenantGeneratedDomainCollectionPath(
			"svc_domain_recovery",
			"env_domain_main",
		),
		"domain-recovery-adopt",
		`{}`,
	)
	if recoveryAdopted.Code != http.StatusCreated {
		t.Fatalf("recovery adoption=%#v", recoveryAdopted)
	}
	var adopted TenantGeneratedDomainView
	if err := json.Unmarshal([]byte(recoveryAdopted.Body), &adopted); err != nil {
		t.Fatal(err)
	}
	if adopted.ID != recoveryDomain.ID ||
		adopted.Hostname != recoveryDomain.NormalizedHostname ||
		adopted.Version != 1 {
		t.Fatalf("adopted recovery domain=%#v, want %#v", adopted, recoveryDomain)
	}
	recoveryItem := tenantGeneratedDomainItemPath(
		"svc_domain_recovery",
		"env_domain_main",
		adopted.ID,
	)
	recoveryDeleted := request(
		member.AccessToken,
		http.MethodDelete,
		recoveryItem,
		"domain-recovery-delete",
		`{"expected_version":1}`,
	)
	if recoveryDeleted.Code != http.StatusNoContent {
		t.Fatalf("recovery-backed product delete=%#v", recoveryDeleted)
	}
	var retiredRecoveryID int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM retired_product_domain_ids
		 WHERE domain_id=$1
		   AND project_id=$2
		   AND environment_id=$3
		   AND service_id=$4`,
		adopted.ID,
		recoveryScope.ProjectID,
		recoveryScope.EnvironmentID,
		recoveryScope.ServiceID,
	).Scan(&retiredRecoveryID); err != nil || retiredRecoveryID != 1 {
		t.Fatalf(
			"retired recovery-backed product identity=%d err=%v",
			retiredRecoveryID,
			err,
		)
	}
	recoveryDomain, err = coordinator.Routes.PutDomain(ctx, recoveryDomain)
	if err != nil {
		t.Fatalf("recreate retired recovery identity: %v", err)
	}
	if err := coordinator.Routes.EnsureDomainLifecycle(ctx, recoveryDomain); err != nil {
		t.Fatalf("recreate retired recovery lifecycle: %v", err)
	}
	if _, err := coordinator.Routes.PutHTTPRoute(ctx, seedRecoveryRoute); err != nil {
		t.Fatalf("recreate retired recovery route: %v", err)
	}
	retiredAdoption := request(
		member.AccessToken,
		http.MethodPost,
		tenantGeneratedDomainCollectionPath(
			"svc_domain_recovery",
			"env_domain_main",
		),
		"domain-recovery-retired-adopt",
		`{}`,
	)
	if retiredAdoption.Code != http.StatusConflict {
		t.Fatalf("retired recovery adoption=%#v, want conflict", retiredAdoption)
	}
	staleRecoveryDelete := request(
		member.AccessToken,
		http.MethodDelete,
		recoveryItem,
		"domain-recovery-stale-delete",
		`{"expected_version":1}`,
	)
	if staleRecoveryDelete.Code != http.StatusNotFound {
		t.Fatalf("stale recovery-backed delete=%#v, want not found", staleRecoveryDelete)
	}
	recoveryAfterStaleDelete, err := coordinator.Routes.GetDomain(
		ctx,
		recoveryDomain.ID,
	)
	if err != nil || recoveryAfterStaleDelete.ID != recoveryDomain.ID {
		t.Fatalf(
			"recovery row after stale product delete=%#v err=%v",
			recoveryAfterStaleDelete,
			err,
		)
	}
	var retiredRecoveryBinding int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM service_domain_bindings
		 WHERE domain_id=$1`,
		recoveryDomain.ID,
	).Scan(&retiredRecoveryBinding); err != nil || retiredRecoveryBinding != 0 {
		t.Fatalf(
			"retired recovery identity product bindings=%d err=%v",
			retiredRecoveryBinding,
			err,
		)
	}

	idleCollection := tenantGeneratedDomainCollectionPath(
		"svc_domain_idle",
		"env_domain_main",
	)
	created := request(
		member.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-idle-create",
		`{}`,
	)
	if created.Code != http.StatusCreated ||
		created.Header.Get("Location") == "" ||
		created.Header.Get("ETag") != "" {
		t.Fatalf("idle create=%#v", created)
	}
	var idle TenantGeneratedDomainView
	if err := json.Unmarshal([]byte(created.Body), &idle); err != nil {
		t.Fatal(err)
	}
	if idle.WorkspaceID != "wrk_domain_a" ||
		idle.ProjectID != "prj_domain_a" ||
		idle.ServiceID != "svc_domain_idle" ||
		idle.EnvironmentID != "env_domain_main" ||
		idle.TargetPort != 8080 ||
		idle.RouteApplyState != "blocked_backend" ||
		idle.RouteReachable ||
		idle.TLS ||
		!idle.Public {
		t.Fatalf("idle projection=%#v", idle)
	}
	var queuedBindingCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM deployment_route_intents
		 WHERE deployment_id='dep_domain_idle_queued'
		   AND domain_id=$1`,
		idle.ID,
	).Scan(&queuedBindingCount); err != nil || queuedBindingCount != 1 {
		t.Fatalf(
			"queued deployment generated binding=%d err=%v",
			queuedBindingCount,
			err,
		)
	}
	replay := request(
		member.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-idle-create",
		`{}`,
	)
	if replay.Code != created.Code ||
		replay.Body != created.Body ||
		replay.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay=%#v created=%#v", replay, created)
	}
	existing := request(
		member.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-idle-existing",
		`{}`,
	)
	if existing.Code != http.StatusOK || existing.Body != created.Body {
		t.Fatalf("different-key convergence=%#v", existing)
	}
	viewerRead := request(
		viewer.AccessToken,
		http.MethodGet,
		tenantGeneratedDomainItemPath(
			"svc_domain_idle",
			"env_domain_main",
			idle.ID,
		),
		"",
		"",
	)
	if viewerRead.Code != http.StatusOK ||
		viewerRead.Header.Get("ETag") != "" {
		t.Fatalf("viewer read=%#v", viewerRead)
	}
	viewerWrite := request(
		viewer.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-viewer-write",
		`{}`,
	)
	if viewerWrite.Code != http.StatusForbidden {
		t.Fatalf("viewer write=%#v", viewerWrite)
	}
	item := tenantGeneratedDomainItemPath(
		"svc_domain_idle",
		"env_domain_main",
		idle.ID,
	)
	states, err := coordinator.Routes.ListDomainRouteStates(
		ctx,
		router.RouteScope{
			ProjectID:     "prj_domain_a",
			EnvironmentID: "env_domain_main",
			ServiceID:     "svc_domain_idle",
		},
	)
	if err != nil || len(states) != 1 || len(states[0].Routes) != 1 {
		t.Fatalf("recovery route state=%#v err=%v", states, err)
	}
	recoveryRoute := states[0].Routes[0]
	recoveryRoute.TargetPort = 9090
	if _, err := coordinator.Routes.PutHTTPRoute(ctx, recoveryRoute); err != nil {
		t.Fatalf("recovery route edit: %v", err)
	}
	converged := request(
		member.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-idle-converge",
		`{}`,
	)
	var convergedIdle TenantGeneratedDomainView
	if converged.Code != http.StatusOK {
		t.Fatalf("existing convergence=%#v", converged)
	}
	if err := json.Unmarshal([]byte(converged.Body), &convergedIdle); err != nil {
		t.Fatal(err)
	}
	if convergedIdle.TargetPort != 8080 || convergedIdle.Version != 3 {
		t.Fatalf("converged recovery edit=%#v", convergedIdle)
	}
	stale := request(
		member.AccessToken,
		http.MethodDelete,
		item,
		"domain-idle-stale",
		`{"expected_version":2}`,
	)
	if stale.Code != http.StatusPreconditionFailed ||
		!strings.Contains(stale.Body, `"current_version":3`) {
		t.Fatalf("stale delete=%#v", stale)
	}
	deleted := request(
		member.AccessToken,
		http.MethodDelete,
		item,
		"domain-idle-delete",
		`{"expected_version":3}`,
	)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%#v", deleted)
	}
	var idleArtifacts int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM domains WHERE id=$1) +
			(SELECT count(*) FROM http_routes WHERE domain_id=$1) +
			(SELECT count(*) FROM service_domain_bindings WHERE domain_id=$1)`,
		idle.ID,
	).Scan(&idleArtifacts); err != nil {
		t.Fatal(err)
	}
	if idleArtifacts != 0 {
		t.Fatalf("deleted domain left %d artifacts", idleArtifacts)
	}
	recreatedResponse := request(
		member.AccessToken,
		http.MethodPost,
		idleCollection,
		"domain-idle-recreate",
		`{}`,
	)
	if recreatedResponse.Code != http.StatusCreated {
		t.Fatalf("recreate=%#v", recreatedResponse)
	}
	var recreated TenantGeneratedDomainView
	if err := json.Unmarshal([]byte(recreatedResponse.Body), &recreated); err != nil {
		t.Fatal(err)
	}
	if recreated.ID == idle.ID ||
		recreated.Hostname != idle.Hostname ||
		recreated.Version != 1 {
		t.Fatalf(
			"recreated domain=%#v, want fresh identity, stable hostname, version 1 after %#v",
			recreated,
			idle,
		)
	}
	staleIncarnationDelete := request(
		member.AccessToken,
		http.MethodDelete,
		item,
		"domain-idle-old-incarnation-delete",
		`{"expected_version":3}`,
	)
	if staleIncarnationDelete.Code != http.StatusNotFound {
		t.Fatalf("old-incarnation delete=%#v, want not found", staleIncarnationDelete)
	}
	currentAfterStaleDelete := request(
		viewer.AccessToken,
		http.MethodGet,
		tenantGeneratedDomainItemPath(
			"svc_domain_idle",
			"env_domain_main",
			recreated.ID,
		),
		"",
		"",
	)
	if currentAfterStaleDelete.Code != http.StatusOK ||
		!strings.Contains(currentAfterStaleDelete.Body, recreated.ID) {
		t.Fatalf(
			"recreated domain after stale old-incarnation delete=%#v",
			currentAfterStaleDelete,
		)
	}

	seedActiveDomainDeployment(t, ctx, pool, now)
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,state,
			expected_active_deployment_id,created_at,updated_at
		) VALUES (
			'dep_domain_candidate','prj_domain_a','svc_domain_active',
			'env_domain_main','deploy_image','queued',
			'dep_domain_active',$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed in-flight domain candidate: %v", err)
	}
	for _, state := range []string{"building", "deploying"} {
		if _, err := pool.Exec(ctx, `
			UPDATE deployments
			   SET state=$1,updated_at=$2
			 WHERE id='dep_domain_candidate'`,
			state,
			now,
		); err != nil {
			t.Fatalf("advance in-flight domain candidate to %s: %v", state, err)
		}
	}
	activeCollection := tenantGeneratedDomainCollectionPath(
		"svc_domain_active",
		"env_domain_main",
	)
	activeCreated := request(
		member.AccessToken,
		http.MethodPost,
		activeCollection,
		"domain-active-create",
		`{}`,
	)
	if activeCreated.Code != http.StatusCreated {
		t.Fatalf("active create=%#v", activeCreated)
	}
	var active TenantGeneratedDomainView
	if err := json.Unmarshal([]byte(activeCreated.Body), &active); err != nil {
		t.Fatal(err)
	}
	if active.TargetPort != 9090 ||
		active.RouteApplyState != "pending" ||
		active.RouteReachable {
		t.Fatalf("active projection=%#v", active)
	}
	var bindingCount, candidateBindingCount, backendCount, auditCount int
	var serviceURL string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*)
			   FROM deployment_route_intents
			  WHERE deployment_id='dep_domain_active'
			    AND domain_id=$1),
			(SELECT count(*)
			   FROM route_backends
			  WHERE deployment_id='dep_domain_active'
			    AND route_id IN (
			        SELECT route_id FROM service_domain_bindings WHERE domain_id=$1
			    )
			    AND ready=TRUE),
			(SELECT count(*)
			   FROM deployment_route_intents
			  WHERE deployment_id='dep_domain_candidate'
			    AND domain_id=$1),
			COALESCE((
			    SELECT service_url
			      FROM route_backends
			     WHERE deployment_id='dep_domain_active'
			     LIMIT 1
			),''),
			(SELECT count(*)
			   FROM audit_logs
			  WHERE action='domain.generated.create'
			    AND resource_id=$1
			    AND policy_decision='allowed')`,
		active.ID,
	).Scan(
		&bindingCount,
		&backendCount,
		&candidateBindingCount,
		&serviceURL,
		&auditCount,
	); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 ||
		candidateBindingCount != 1 ||
		backendCount != 2 ||
		!strings.HasSuffix(serviceURL, ":9090") ||
		auditCount != 1 {
		t.Fatalf(
			"active artifacts binding=%d candidate_binding=%d backends=%d url=%q audits=%d",
			bindingCount,
			candidateBindingCount,
			backendCount,
			serviceURL,
			auditCount,
		)
	}
	deployments := NewPostgresDeploymentStore(pool)
	domainLockTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domainLockTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"service-environment-deploy:svc_domain_active:env_domain_main",
	); err != nil {
		_ = domainLockTx.Rollback(ctx)
		t.Fatal(err)
	}
	promotionResult := make(chan error, 1)
	go func() {
		promotionResult <- deployments.PromoteDeployment(
			ctx,
			"dep_domain_candidate",
			"dep_domain_active",
		)
	}()
	select {
	case early := <-promotionResult:
		_ = domainLockTx.Rollback(ctx)
		t.Fatalf(
			"promotion bypassed service/environment lock: %v",
			early,
		)
	case <-time.After(100 * time.Millisecond):
	}
	candidateProbeTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		_ = domainLockTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := candidateProbeTx.Exec(ctx, `
		SELECT id
		  FROM deployments
		 WHERE id='dep_domain_candidate'
		 FOR UPDATE NOWAIT`); err != nil {
		_ = candidateProbeTx.Rollback(ctx)
		_ = domainLockTx.Rollback(ctx)
		t.Fatalf(
			"promotion locked candidate before service/environment lock: %v",
			err,
		)
	}
	if err := candidateProbeTx.Rollback(ctx); err != nil {
		_ = domainLockTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := domainLockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-promotionResult; !errors.Is(err, ErrPromotionConflict) {
		t.Fatalf(
			"unpublished generated-route promotion error=%v, want conflict",
			err,
		)
	}
	var activeID string
	if err := pool.QueryRow(ctx, `
		SELECT active_deployment_id
		  FROM service_environment_state
		 WHERE service_id='svc_domain_active'
		   AND environment_id='env_domain_main'`,
	).Scan(&activeID); err != nil || activeID != "dep_domain_active" {
		t.Fatalf("active deployment after fenced race=%q err=%v", activeID, err)
	}
	for _, statement := range []string{
		`INSERT INTO operations (
			id,kind,state,actor,created_at,updated_at
		) VALUES (
			'op_domain_candidate','deploy','RUNNING','system',$1,$1
		)`,
		`UPDATE deployments
		    SET state='superseded',active=FALSE,updated_at=$1
		  WHERE id='dep_domain_active'`,
		`UPDATE deployments
		    SET state='active',active=TRUE,operation_id='op_domain_candidate',
		        updated_at=$1
		  WHERE id='dep_domain_candidate'`,
		`UPDATE service_environment_state
		    SET active_deployment_id='dep_domain_candidate',updated_at=$1
		  WHERE service_id='svc_domain_active'
		    AND environment_id='env_domain_main'`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed canonical-only ambiguous commit state: %v", err)
		}
	}
	proved, err := deployments.DeploymentPromotionCommitted(
		ctx,
		"op_domain_candidate",
		"dep_domain_candidate",
		"dep_domain_active",
	)
	if err != nil || proved {
		t.Fatalf(
			"canonical-only unverified route proof=%v err=%v, want false",
			proved,
			err,
		)
	}
}

type routerResponse struct {
	Code   int
	Body   string
	Header http.Header
}

func seedActiveDomainDeployment(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	now time.Time,
) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO operations (
			id,kind,state,actor,created_at,updated_at
		) VALUES ('op_domain_active','deploy','SUCCEEDED','system',$1,$1)`,
		`INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,
			image_ref,image_digest,state,active,operation_id,trigger_key,
			created_at,updated_at
		) VALUES (
			'dep_domain_active','prj_domain_a','svc_domain_active','env_domain_main',
			'deploy_image',
			'registry.example/active@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
			'queued',FALSE,'op_domain_active','trigger:domain-active',$1,$1
		)`,
		`UPDATE deployments
		   SET state='deploying',updated_at=$1
		 WHERE id='dep_domain_active'`,
		`UPDATE deployments
		   SET state='active',active=TRUE,updated_at=$1
		 WHERE id='dep_domain_active'`,
		`INSERT INTO service_environment_state (
			service_id,environment_id,active_deployment_id,
			last_deployment_seq,version,updated_at
		) VALUES (
			'svc_domain_active','env_domain_main','dep_domain_active',1,1,$1
		)`,
		`INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,
			config_version,effective_config,variable_versions,
			snapshot_sha256,created_at
		) VALUES (
			'dep_domain_active','prj_domain_a','svc_domain_active','env_domain_main',
			0,'{"target_port":9090}'::jsonb,'[]'::jsonb,
			repeat('a',64),$1
		)`,
		`INSERT INTO deployment_instances (
			deployment_id,instance_id,replica_ordinal,desired_state,
			observed_state,container_id,container_ip,ready,created_at,updated_at
		) VALUES
			('dep_domain_active','dep_domain_active-r0',0,'running','running','ctr-domain-0','10.44.0.10',TRUE,$1,$1),
			('dep_domain_active','dep_domain_active-r1',1,'running','running','ctr-domain-1','10.44.0.11',TRUE,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed active deployment: %v", err)
		}
	}
}
