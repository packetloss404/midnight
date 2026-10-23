//go:build integration

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTenantImageDeployIntakeAtomicAuthorizationAndReplay(t *testing.T) {
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

	now := time.Date(2026, 7, 28, 21, 0, 0, 0, time.UTC)
	password := "tenant deploy correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_deploy_member','member-deploy@example.com','Member',$1,1,$2,$2),
			('usr_deploy_viewer','viewer-deploy@example.com','Viewer',$1,1,$2,$2),
			('usr_deploy_admin','admin-deploy@example.com','Admin',$1,1,$2,$2),
			('usr_deploy_race','race-deploy@example.com','Race',$1,1,$2,$2),
			('usr_deploy_fault','fault-deploy@example.com','Fault',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_deploy_a','deploy-a','Deploy A',$1,$1),
			('wrk_deploy_b','deploy-b','Deploy B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name, created_at, updated_at)
		VALUES
			('prj_deploy_a','wrk_deploy_a','api','API',$1,$1),
			('prj_deploy_b','wrk_deploy_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (id, project_id, slug, display_name, created_at, updated_at)
		VALUES
			('svc_deploy_a','prj_deploy_a','web','Web',$1,$1),
			('svc_deploy_b','prj_deploy_a','worker','Worker',$1,$1),
			('svc_deploy_foreign','prj_deploy_b','foreign','Foreign',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed services: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, access_restricted, created_at, updated_at
		) VALUES
			('env_deploy_a','prj_deploy_a','production','Production','production',FALSE,$1,$1),
			('env_deploy_restricted','prj_deploy_a','restricted','Restricted','production',TRUE,$1,$1),
			('env_deploy_fault','prj_deploy_a','fault','Fault','development',FALSE,$1,$1),
			('env_deploy_foreign','prj_deploy_b','production','Production','production',FALSE,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		) VALUES
			('wrk_deploy_a','usr_deploy_member','member',$1,$1,$1,$1),
			('wrk_deploy_a','usr_deploy_viewer','viewer',$1,$1,$1,$1),
			('wrk_deploy_a','usr_deploy_admin','admin',$1,$1,$1,$1),
			('wrk_deploy_a','usr_deploy_race','member',$1,$1,$1,$1),
			('wrk_deploy_a','usr_deploy_fault','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace members: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO operations (
			id, kind, state, actor, expires_at, created_at, updated_at
		) VALUES (
			'op_deploy_existing','deploy','SUCCEEDED','system',$2,$1,$1
		)`,
		now,
		now.Add(24*time.Hour),
	); err != nil {
		t.Fatalf("seed active operation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployments (
			id, project_id, service_id, environment_id, action,
			image_ref, image_digest, state, active, operation_id, trigger_key,
			created_at, updated_at
		) VALUES (
			'dep_deploy_existing','prj_deploy_a','svc_deploy_a','env_deploy_a',
			'deploy_image',
			'registry.example/web@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			'sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
			'queued',FALSE,'op_deploy_existing','trigger:existing',$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed active deployment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE deployments
		SET state='deploying', updated_at=$1
		WHERE id='dep_deploy_existing'`,
		now,
	); err != nil {
		t.Fatalf("advance active deployment to deploying: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE deployments
		SET state='active', active=TRUE, updated_at=$1
		WHERE id='dep_deploy_existing'`,
		now,
	); err != nil {
		t.Fatalf("advance active deployment to active: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_environment_state (
			service_id, environment_id, active_deployment_id,
			last_deployment_seq, version, updated_at
		) VALUES (
			'svc_deploy_a','env_deploy_a','dep_deploy_existing',1,1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed active pointer: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	if _, ok := stack.AuditChain.(*audit.PostgresChain); !ok {
		t.Fatalf("audit chain = %T, want postgres", stack.AuditChain)
	}
	coordinator, ok := stack.TenantDeployIntake.(*PostgresTenantDeployIntakeCoordinator)
	if !ok {
		t.Fatalf("tenant deploy coordinator = %T, want postgres", stack.TenantDeployIntake)
	}
	coordinator.AppsDomain = "apps.midnight.test"
	stack.AppsDomain = coordinator.AppsDomain
	var operationSequence, deploymentSequence, domainSequence, outboxSequence, eventSequence atomic.Int32
	coordinator.OperationIDFactory = func() orailtypes.ID {
		return orailtypes.ID(fmt.Sprintf("op_tenant_deploy_%02d", operationSequence.Add(1)))
	}
	coordinator.DeploymentIDFactory = func() string {
		return fmt.Sprintf("dep_tenant_deploy_%02d", deploymentSequence.Add(1))
	}
	coordinator.DomainIDFactory = func() string {
		return fmt.Sprintf("dom_tenant_deploy_%02d", domainSequence.Add(1))
	}
	coordinator.OutboxIDFactory = func() string {
		return fmt.Sprintf("dox_tenant_deploy_%02d", outboxSequence.Add(1))
	}
	coordinator.EventIDFactory = func() string {
		return fmt.Sprintf("dev_tenant_deploy_%02d", eventSequence.Add(1))
	}

	type authenticatedHuman struct {
		principal identity.Principal
		session   identity.Session
		token     string
	}
	login := func(email string) authenticatedHuman {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api",
			RemoteAddr: "192.0.2.60:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		principal, session, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+result.AccessToken)
		if err != nil {
			t.Fatalf("authenticate %s: %v", email, err)
		}
		return authenticatedHuman{principal: principal, session: session, token: result.AccessToken}
	}
	memberFirst := login("member-deploy@example.com")
	memberSecond := login("member-deploy@example.com")
	viewer := login("viewer-deploy@example.com")
	admin := login("admin-deploy@example.com")
	race := login("race-deploy@example.com")
	fault := login("fault-deploy@example.com")

	imageA := "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	imageB := "registry.example/web@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	handler := NewRouterWithDataStack(stack)
	routeBody := `{"environment_id":"env_deploy_a","image":"` + imageB + `","route":{"public":true}}`
	serveRouteWithKey := func(key string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			v1alphaPrefix+"/services/svc_deploy_b/deployments",
			strings.NewReader(routeBody),
		)
		request.Header.Set("Authorization", "Bearer "+memberFirst.token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	serveRoute := func() *httptest.ResponseRecorder {
		return serveRouteWithKey("deploy-live-route")
	}
	firstRoute := serveRoute()
	replayRoute := serveRoute()
	if firstRoute.Code != http.StatusAccepted ||
		replayRoute.Code != http.StatusAccepted ||
		firstRoute.Body.String() != replayRoute.Body.String() ||
		firstRoute.Header().Get("Idempotent-Replayed") != "" ||
		replayRoute.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"live route/replay = %d/%q/%s then %d/%q/%s",
			firstRoute.Code,
			firstRoute.Header().Get("Idempotent-Replayed"),
			firstRoute.Body.String(),
			replayRoute.Code,
			replayRoute.Header().Get("Idempotent-Replayed"),
			replayRoute.Body.String(),
		)
	}

	startConcurrentRoute := make(chan struct{})
	concurrentRouteResults := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-startConcurrentRoute
			concurrentRouteResults <- serveRouteWithKey("deploy-live-concurrent")
		}()
	}
	close(startConcurrentRoute)
	concurrentFirst := <-concurrentRouteResults
	concurrentSecond := <-concurrentRouteResults
	if concurrentFirst.Code != http.StatusAccepted ||
		concurrentSecond.Code != http.StatusAccepted ||
		concurrentFirst.Body.String() != concurrentSecond.Body.String() {
		t.Fatalf(
			"concurrent route results = %d/%s then %d/%s",
			concurrentFirst.Code,
			concurrentFirst.Body.String(),
			concurrentSecond.Code,
			concurrentSecond.Body.String(),
		)
	}
	replayedConcurrent := 0
	for _, response := range []*httptest.ResponseRecorder{concurrentFirst, concurrentSecond} {
		if response.Header().Get("Idempotent-Replayed") == "true" {
			replayedConcurrent++
		}
	}
	if replayedConcurrent != 1 {
		t.Fatalf("concurrent replay headers = %q/%q, want exactly one replay",
			concurrentFirst.Header().Get("Idempotent-Replayed"),
			concurrentSecond.Header().Get("Idempotent-Replayed"),
		)
	}
	var concurrentOperations, concurrentDeployments, concurrentOutbox, concurrentReservations int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations
			 WHERE idempotency_key='deploy-live-concurrent'),
			(SELECT COUNT(*) FROM deployments d
			 JOIN operations op ON op.id=d.operation_id
			 WHERE op.idempotency_key='deploy-live-concurrent'),
			(SELECT COUNT(*) FROM deploy_intake_outbox o
			 JOIN operations op ON op.id=o.operation_id
			 WHERE op.idempotency_key='deploy-live-concurrent'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='deploy-live-concurrent'
			   AND path_template=$1)`,
		tenantDeployIdempotencyPath("svc_deploy_b"),
	).Scan(
		&concurrentOperations,
		&concurrentDeployments,
		&concurrentOutbox,
		&concurrentReservations,
	); err != nil {
		t.Fatal(err)
	}
	if concurrentOperations != 1 ||
		concurrentDeployments != 1 ||
		concurrentOutbox != 1 ||
		concurrentReservations != 1 {
		t.Fatalf(
			"concurrent durable lineage operations=%d deployments=%d outbox=%d reservations=%d, want one each",
			concurrentOperations,
			concurrentDeployments,
			concurrentOutbox,
			concurrentReservations,
		)
	}
	var generatedDomains, generatedRoutes, generatedBindings int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM domains
			 WHERE project_id='prj_deploy_a'
			   AND service_id='svc_deploy_b'
			   AND environment_id='env_deploy_a'
			   AND kind='generated'),
			(SELECT COUNT(*) FROM http_routes
			 WHERE project_id='prj_deploy_a'
			   AND service_id='svc_deploy_b'
			   AND environment_id='env_deploy_a'
			   AND public_enabled=TRUE),
			(SELECT COUNT(*)
			 FROM deployment_route_intents i
			 JOIN deployments d ON d.id=i.deployment_id
			 JOIN operations op ON op.id=d.operation_id
			 WHERE op.idempotency_key IN ('deploy-live-route','deploy-live-concurrent'))`,
	).Scan(&generatedDomains, &generatedRoutes, &generatedBindings); err != nil {
		t.Fatal(err)
	}
	if generatedDomains != 1 || generatedRoutes != 1 || generatedBindings != 2 {
		t.Fatalf(
			"generated route artifacts domains=%d routes=%d bindings=%d, want 1/1/2",
			generatedDomains,
			generatedRoutes,
			generatedBindings,
		)
	}
	deploymentReadRequest := httptest.NewRequest(
		http.MethodGet,
		v1alphaPrefix+"/services/svc_deploy_b/deployments?environment_id=env_deploy_a",
		nil,
	)
	deploymentReadRequest.Header.Set("Authorization", "Bearer "+memberFirst.token)
	deploymentReadResponse := httptest.NewRecorder()
	handler.ServeHTTP(deploymentReadResponse, deploymentReadRequest)
	if deploymentReadResponse.Code != http.StatusOK {
		t.Fatalf(
			"generated deployment read status=%d body=%s",
			deploymentReadResponse.Code,
			deploymentReadResponse.Body.String(),
		)
	}
	var generatedPage DeploymentResourcePage
	if err := json.Unmarshal(deploymentReadResponse.Body.Bytes(), &generatedPage); err != nil {
		t.Fatal(err)
	}
	if len(generatedPage.Items) != 2 {
		t.Fatalf("generated deployment page=%#v, want two exact deployments", generatedPage)
	}
	for _, item := range generatedPage.Items {
		if !strings.HasSuffix(item.RouteIntent, ".apps.midnight.test") ||
			item.Route != "" ||
			item.RouteApplyState != "pending" ||
			item.RouteReachable {
			t.Fatalf("pending generated route overclaimed state: %#v", item)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_domain_bindings (
			route_id,domain_id,project_id,environment_id,service_id,created_at
		)
		SELECT r.id,r.domain_id,r.project_id,r.environment_id,r.service_id,$1
		  FROM http_routes r
		  JOIN domains d ON d.id=r.domain_id
		 WHERE r.project_id='prj_deploy_a'
		   AND r.environment_id='env_deploy_a'
		   AND r.service_id='svc_deploy_b'
		   AND d.kind='generated'
		ON CONFLICT (route_id) DO NOTHING`,
		now,
	); err != nil {
		t.Fatalf("seed service-managed generated route: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO domains (
			id,project_id,environment_id,service_id,kind,hostname,
			normalized_hostname,apps_domain,tls_desired,tls_state,
			desired_revision,created_at,updated_at
		) VALUES (
			'dom_deploy_custom_recovery','prj_deploy_a','env_deploy_a',
			'svc_deploy_b','custom','recovery.example.test',
			'recovery.example.test','',FALSE,'disabled',1,$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed custom recovery domain: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO http_routes (
			id,domain_id,project_id,environment_id,service_id,name,
			path_prefix,target_port,public_enabled,desired_revision,
			created_at,updated_at
		) VALUES (
			'rte_deploy_custom_recovery','dom_deploy_custom_recovery',
			'prj_deploy_a','env_deploy_a','svc_deploy_b',
			'recovery-custom','/',8080,TRUE,1,$1,$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed custom recovery HTTP route: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_domain_bindings (
			route_id,domain_id,project_id,environment_id,service_id,created_at
		) VALUES (
			'rte_deploy_custom_recovery','dom_deploy_custom_recovery',
			'prj_deploy_a','env_deploy_a','svc_deploy_b',$1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed custom recovery service binding: %v", err)
	}
	managedBody := `{"environment_id":"env_deploy_a","image":"` + imageA + `"}`
	managedRequest := httptest.NewRequest(
		http.MethodPost,
		v1alphaPrefix+"/services/svc_deploy_b/deployments",
		strings.NewReader(managedBody),
	)
	managedRequest.Header.Set("Authorization", "Bearer "+memberFirst.token)
	managedRequest.Header.Set("Content-Type", "application/json")
	managedRequest.Header.Set("Idempotency-Key", "deploy-service-managed-route")
	managedResponse := httptest.NewRecorder()
	handler.ServeHTTP(managedResponse, managedRequest)
	if managedResponse.Code != http.StatusAccepted {
		t.Fatalf(
			"service-managed future deploy status=%d body=%s",
			managedResponse.Code,
			managedResponse.Body.String(),
		)
	}
	var managedBindings, managedCustomBindings int
	var managedRoutePublic bool
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(i.route_id),
			COUNT(i.route_id) FILTER (WHERE d.kind='custom'),
			COALESCE((o.payload->'route'->>'public')::boolean,FALSE)
		  FROM operations op
		  JOIN deployments dep ON dep.operation_id=op.id
		  JOIN deploy_intake_outbox o ON o.deployment_id=dep.id
		  LEFT JOIN deployment_route_intents i ON i.deployment_id=dep.id
		  LEFT JOIN domains d ON d.id=i.domain_id
		 WHERE op.idempotency_key='deploy-service-managed-route'
		 GROUP BY o.payload`,
	).Scan(
		&managedBindings,
		&managedCustomBindings,
		&managedRoutePublic,
	); err != nil {
		t.Fatal(err)
	}
	if managedBindings != 1 || managedCustomBindings != 0 || !managedRoutePublic {
		t.Fatalf(
			"service-managed future deployment bindings=%d custom=%d route_public=%t, want 1/0/true",
			managedBindings,
			managedCustomBindings,
			managedRoutePublic,
		)
	}
	makeRequest := func(
		human authenticatedHuman,
		serviceID orailtypes.ID,
		environmentID string,
		image string,
		key idempotency.Key,
		requestID string,
	) TenantImageDeployRequest {
		body := []byte(fmt.Sprintf(
			`{"environment_id":%q,"image":%q}`,
			environmentID,
			image,
		))
		return TenantImageDeployRequest{
			Principal: human.principal,
			Session:   human.session,
			ServiceID: serviceID,
			Intent: DeployIntakeRequest{
				EnvironmentID: environmentID,
				Image:         image,
			},
			Key: key,
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       tenantDeployIdempotencyPath(serviceID),
				BodySHA256: idempotency.BodyHashSHA256(body),
			},
			RequestID: requestID,
			SourceIP:  "192.0.2.60:443",
			UserAgent: "midnight-integration",
		}
	}
	requestGeneratedRoute := func(request *TenantImageDeployRequest) {
		request.Intent.Route = &DeployRouteIntent{Public: true}
		request.Fingerprint.BodySHA256 = idempotency.BodyHashSHA256([]byte(fmt.Sprintf(
			`{"environment_id":%q,"image":%q,"route":{"public":true}}`,
			request.Intent.EnvironmentID,
			request.Intent.Image,
		)))
	}

	firstRequest := makeRequest(
		memberFirst,
		"svc_deploy_a",
		"env_deploy_a",
		imageA,
		"deploy-shared-key",
		"req_deploy_first",
	)
	first, err := coordinator.DeployImmutableImage(ctx, firstRequest)
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if first.Replayed ||
		first.Response.StatusCode != http.StatusAccepted ||
		first.Response.Header.Get("Location") == "" ||
		first.Response.Header.Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("first result = %#v", first)
	}
	var firstOperation orailtypes.Operation
	if err := json.Unmarshal(first.Response.Body, &firstOperation); err != nil {
		t.Fatalf("decode first operation: %v body=%s", err, first.Response.Body)
	}
	var persistedCreatedAt, persistedUpdatedAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT created_at, updated_at FROM operations WHERE id=$1`,
		string(firstOperation.ID),
	).Scan(&persistedCreatedAt, &persistedUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if firstOperation.CreatedAt.IsZero() ||
		!firstOperation.CreatedAt.Equal(persistedCreatedAt.UTC()) ||
		!firstOperation.UpdatedAt.Equal(persistedUpdatedAt.UTC()) {
		t.Fatalf("response timestamps=%s/%s persisted=%s/%s, want exact parity", firstOperation.CreatedAt, firstOperation.UpdatedAt, persistedCreatedAt, persistedUpdatedAt)
	}

	replayRequest := firstRequest
	replayRequest.Principal = memberSecond.principal
	replayRequest.Session = memberSecond.session
	replay, err := coordinator.DeployImmutableImage(ctx, replayRequest)
	if err != nil {
		t.Fatalf("same-user cross-session replay: %v", err)
	}
	if !replay.Replayed ||
		replay.Response.StatusCode != first.Response.StatusCode ||
		string(replay.Response.Body) != string(first.Response.Body) ||
		replay.Response.Header.Get("Location") != first.Response.Header.Get("Location") ||
		replay.Response.Header.Get("X-Midnight-Delivery-State") != "pending" {
		t.Fatalf("replay=%#v first=%#v", replay, first)
	}

	// The request body intentionally omits service_id. The concrete canonical
	// idempotency path must prevent the same actor/project/key/body from
	// replaying service A's accepted operation for service B.
	crossServiceRequest := makeRequest(
		memberSecond,
		"svc_deploy_b",
		"env_deploy_a",
		imageA,
		"deploy-shared-key",
		"req_deploy_cross_service",
	)
	crossService, err := coordinator.DeployImmutableImage(ctx, crossServiceRequest)
	if err != nil {
		t.Fatalf("cross-service same-key/body: %v", err)
	}
	var crossServiceOperation orailtypes.Operation
	if err := json.Unmarshal(crossService.Response.Body, &crossServiceOperation); err != nil {
		t.Fatal(err)
	}
	if crossService.Replayed || crossServiceOperation.ID == firstOperation.ID {
		t.Fatalf(
			"cross-service result replayed=%t operation=%s first=%s",
			crossService.Replayed,
			crossServiceOperation.ID,
			firstOperation.ID,
		)
	}

	differentBody := firstRequest
	differentBody.Intent.Image = imageB
	differentBody.Fingerprint.BodySHA256 = idempotency.BodyHashSHA256([]byte(fmt.Sprintf(
		`{"environment_id":%q,"image":%q}`,
		differentBody.Intent.EnvironmentID,
		differentBody.Intent.Image,
	)))
	if _, err := coordinator.DeployImmutableImage(ctx, differentBody); !errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		t.Fatalf("different-body replay error = %v", err)
	}

	viewerRequest := makeRequest(
		viewer,
		"svc_deploy_a",
		"env_deploy_a",
		imageA,
		"deploy-viewer-denied",
		"req_deploy_viewer",
	)
	requestGeneratedRoute(&viewerRequest)
	if _, err := coordinator.DeployImmutableImage(ctx, viewerRequest); !errors.Is(err, identity.ErrAuthorizationDenied) {
		t.Fatalf("viewer deploy error = %v", err)
	}
	restrictedMemberRequest := makeRequest(
		memberSecond,
		"svc_deploy_a",
		"env_deploy_restricted",
		imageA,
		"deploy-restricted-concealed",
		"req_deploy_restricted",
	)
	requestGeneratedRoute(&restrictedMemberRequest)
	if _, err := coordinator.DeployImmutableImage(ctx, restrictedMemberRequest); !errors.Is(err, identity.ErrAccessNotFound) {
		t.Fatalf("restricted environment error = %v, want concealed not found", err)
	}
	foreignRequest := makeRequest(
		memberSecond,
		"svc_deploy_foreign",
		"env_deploy_foreign",
		imageA,
		"deploy-foreign-concealed",
		"req_deploy_foreign",
	)
	requestGeneratedRoute(&foreignRequest)
	if _, err := coordinator.DeployImmutableImage(ctx, foreignRequest); !errors.Is(err, identity.ErrAccessNotFound) {
		t.Fatalf("foreign service error = %v", err)
	}
	var deniedRouteArtifacts int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM domains
			 WHERE (service_id='svc_deploy_a' AND environment_id IN ('env_deploy_a','env_deploy_restricted'))
			    OR service_id='svc_deploy_foreign')
		  + (SELECT COUNT(*) FROM http_routes
			 WHERE (service_id='svc_deploy_a' AND environment_id IN ('env_deploy_a','env_deploy_restricted'))
			    OR service_id='svc_deploy_foreign')
		  + (SELECT COUNT(*) FROM deployment_route_intents
			 WHERE (service_id='svc_deploy_a' AND environment_id IN ('env_deploy_a','env_deploy_restricted'))
			    OR service_id='svc_deploy_foreign')`,
	).Scan(&deniedRouteArtifacts); err != nil {
		t.Fatal(err)
	}
	if deniedRouteArtifacts != 0 {
		t.Fatalf("denied generated-route requests left %d artifacts", deniedRouteArtifacts)
	}
	routeRequest := makeRequest(
		memberSecond,
		"svc_deploy_a",
		"env_deploy_a",
		imageA,
		"deploy-route-rejected",
		"req_deploy_route",
	)
	routeRequest.Intent.Route = &DeployRouteIntent{Host: "app.example.test", Public: true, TLS: true}
	if _, err := coordinator.DeployImmutableImage(ctx, routeRequest); !errors.Is(err, ErrResourceInvalid) {
		t.Fatalf("route-bearing deploy error = %v", err)
	}

	adminRestrictedRequest := makeRequest(
		admin,
		"svc_deploy_a",
		"env_deploy_restricted",
		imageB,
		"deploy-admin-restricted",
		"req_deploy_admin_restricted",
	)
	if _, err := coordinator.DeployImmutableImage(ctx, adminRestrictedRequest); err != nil {
		t.Fatalf("admin restricted deploy: %v", err)
	}
	if err := stack.IdentitySessions.Revoke(
		ctx,
		admin.principal,
		admin.session.ID,
		"tenant-deploy-replay-gate",
	); err != nil {
		t.Fatalf("revoke admin session: %v", err)
	}
	if _, err := coordinator.DeployImmutableImage(ctx, adminRestrictedRequest); !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("revoked session replay error = %v", err)
	}

	// Reauthorization precedes replay release.
	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		SET role='viewer', updated_at=$1, resource_version=resource_version+1
		WHERE workspace_id='wrk_deploy_a' AND user_id='usr_deploy_member'`,
		now.Add(time.Second),
	); err != nil {
		t.Fatalf("demote member: %v", err)
	}
	if _, err := coordinator.DeployImmutableImage(ctx, replayRequest); !errors.Is(err, identity.ErrAuthorizationDenied) {
		t.Fatalf("demoted replay error = %v", err)
	}

	// Hold the grant-writer advisory lock, commit a demotion, and prove intake
	// decides only after the current grant becomes visible.
	raceTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raceTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:wrk_deploy_a"); err != nil {
		_ = raceTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := raceTx.Exec(ctx, `
		UPDATE workspace_members
		SET role='viewer', updated_at=$1, resource_version=resource_version+1
		WHERE workspace_id='wrk_deploy_a' AND user_id='usr_deploy_race'`,
		now.Add(2*time.Second),
	); err != nil {
		_ = raceTx.Rollback(ctx)
		t.Fatal(err)
	}
	raceRequest := makeRequest(
		race,
		"svc_deploy_a",
		"env_deploy_a",
		imageA,
		"deploy-race-denied",
		"req_deploy_race",
	)
	raceResult := make(chan error, 1)
	go func() {
		_, err := coordinator.DeployImmutableImage(ctx, raceRequest)
		raceResult <- err
	}()
	select {
	case early := <-raceResult:
		_ = raceTx.Rollback(ctx)
		t.Fatalf("tenant deploy bypassed pending grant change: %v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := raceTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-raceResult; !errors.Is(err, identity.ErrAuthorizationDenied) {
		t.Fatalf("raced demotion deploy error = %v", err)
	}

	// Inject failure at the final audit write. Every earlier write in the
	// coordinator transaction, including active-state initialization and the
	// exact idempotency response, must roll back.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION midnight_test_fail_tenant_deploy_audit()
		RETURNS TRIGGER LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'injected tenant deploy audit failure';
		END
		$$`,
	); err != nil {
		t.Fatalf("install audit fault function: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TRIGGER midnight_test_fail_tenant_deploy_audit
		BEFORE INSERT ON audit_logs
		FOR EACH ROW
		WHEN (NEW.request_id = 'req_deploy_fault')
		EXECUTE FUNCTION midnight_test_fail_tenant_deploy_audit()`,
	); err != nil {
		t.Fatalf("install audit fault: %v", err)
	}
	faultRequest := makeRequest(
		fault,
		"svc_deploy_a",
		"env_deploy_fault",
		imageA,
		"deploy-audit-fault",
		"req_deploy_fault",
	)
	requestGeneratedRoute(&faultRequest)
	if _, err := coordinator.DeployImmutableImage(ctx, faultRequest); err == nil {
		t.Fatal("audit fault deploy unexpectedly committed")
	}
	var faultOperations, faultDeployments, faultOutbox, faultReservations, faultAudit, faultActiveState, faultRouteArtifacts int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE idempotency_key='deploy-audit-fault'),
			(SELECT COUNT(*) FROM deployments
			 WHERE service_id='svc_deploy_a' AND environment_id='env_deploy_fault'),
			(SELECT COUNT(*) FROM deploy_intake_outbox o
			 JOIN operations op ON op.id=o.operation_id
			 WHERE op.idempotency_key='deploy-audit-fault'),
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='deploy-audit-fault'),
			(SELECT COUNT(*) FROM audit_logs WHERE request_id='req_deploy_fault'),
			(SELECT COUNT(*) FROM service_environment_state
			 WHERE service_id='svc_deploy_a' AND environment_id='env_deploy_fault'),
			(SELECT COUNT(*) FROM domains
			 WHERE service_id='svc_deploy_a' AND environment_id='env_deploy_fault')
			  + (SELECT COUNT(*) FROM http_routes
			 WHERE service_id='svc_deploy_a' AND environment_id='env_deploy_fault')
			  + (SELECT COUNT(*) FROM deployment_route_intents
			 WHERE service_id='svc_deploy_a' AND environment_id='env_deploy_fault')`,
	).Scan(
		&faultOperations,
		&faultDeployments,
		&faultOutbox,
		&faultReservations,
		&faultAudit,
		&faultActiveState,
		&faultRouteArtifacts,
	); err != nil {
		t.Fatal(err)
	}
	if faultOperations != 0 ||
		faultDeployments != 0 ||
		faultOutbox != 0 ||
		faultReservations != 0 ||
		faultAudit != 0 ||
		faultActiveState != 0 ||
		faultRouteArtifacts != 0 {
		t.Fatalf(
			"audit failure left operations=%d deployments=%d outbox=%d reservations=%d audits=%d active_state=%d route_artifacts=%d",
			faultOperations,
			faultDeployments,
			faultOutbox,
			faultReservations,
			faultAudit,
			faultActiveState,
			faultRouteArtifacts,
		)
	}

	var (
		operationKind, operationState, operationActor, targetKind, targetID, operationKey string
		deploymentState, expectedActiveID, deploymentImage, deploymentDigest              string
		outboxState, payloadDeploymentID, payloadPreviousActive                           string
		reservationState, reservationActor, reservationTenant, reservationPath            string
		responseStatus                                                                    int
		responseBody                                                                      []byte
		operationEvents, deploymentEvents, allowedAudits                                  int
	)
	if err := pool.QueryRow(ctx, `
		SELECT kind, state, actor, target_resource_kind, target_resource_id, idempotency_key
		FROM operations
		WHERE id=$1`,
		string(firstOperation.ID),
	).Scan(
		&operationKind,
		&operationState,
		&operationActor,
		&targetKind,
		&targetID,
		&operationKey,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT state, COALESCE(expected_active_deployment_id, ''), image_ref, image_digest
		FROM deployments
		WHERE operation_id=$1`,
		string(firstOperation.ID),
	).Scan(&deploymentState, &expectedActiveID, &deploymentImage, &deploymentDigest); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT state,
		       payload->'request'->>'deployment_id',
		       COALESCE(payload->'previous_active'->>'deployment_id', '')
		FROM deploy_intake_outbox
		WHERE operation_id=$1`,
		string(firstOperation.ID),
	).Scan(&outboxState, &payloadDeploymentID, &payloadPreviousActive); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT state, actor, tenant, path_template, response_status, response_body
		FROM idempotency_keys
		WHERE key='deploy-shared-key'
		  AND path_template=$1`,
		tenantDeployIdempotencyPath("svc_deploy_a"),
	).Scan(
		&reservationState,
		&reservationActor,
		&reservationTenant,
		&reservationPath,
		&responseStatus,
		&responseBody,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operation_events WHERE operation_id=$1),
			(SELECT COUNT(*) FROM deployment_events d
			 JOIN deployments dep ON dep.id=d.deployment_id
			 WHERE dep.operation_id=$1),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='deploy.intake.accept'
			   AND resource_id=$1
			   AND actor_id='usr_deploy_member'
			   AND policy_decision='allowed')`,
		string(firstOperation.ID),
	).Scan(&operationEvents, &deploymentEvents, &allowedAudits); err != nil {
		t.Fatal(err)
	}
	if operationKind != "deploy" ||
		operationState != "QUEUED" ||
		operationActor != "usr_deploy_member" ||
		targetKind != "service" ||
		targetID != "svc_deploy_a" ||
		operationKey != "deploy-shared-key" ||
		deploymentState != "queued" ||
		expectedActiveID != "dep_deploy_existing" ||
		deploymentImage != imageA ||
		deploymentDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		outboxState != "pending" ||
		payloadDeploymentID == "" ||
		payloadPreviousActive != "dep_deploy_existing" ||
		reservationState != "SUCCEEDED" ||
		reservationActor != "user:usr_deploy_member" ||
		reservationTenant != string(projectScopedIdempotencyTenant("wrk_deploy_a", "prj_deploy_a")) ||
		reservationPath != tenantDeployIdempotencyPath("svc_deploy_a") ||
		responseStatus != http.StatusAccepted ||
		string(responseBody) != string(first.Response.Body) ||
		operationEvents != 1 ||
		deploymentEvents != 1 ||
		allowedAudits != 1 {
		t.Fatalf(
			"atomic artifacts operation=(%s,%s,%s,%s,%s,%s) deployment=(%s,%s,%s,%s) outbox=(%s,%s,%s) reservation=(%s,%s,%s,%s,%d,%q) events=(%d,%d) audits=%d",
			operationKind,
			operationState,
			operationActor,
			targetKind,
			targetID,
			operationKey,
			deploymentState,
			expectedActiveID,
			deploymentImage,
			deploymentDigest,
			outboxState,
			payloadDeploymentID,
			payloadPreviousActive,
			reservationState,
			reservationActor,
			reservationTenant,
			reservationPath,
			responseStatus,
			responseBody,
			operationEvents,
			deploymentEvents,
			allowedAudits,
		)
	}
	rollback, err := NewPostgresDeploymentStore(pool).CreateRollbackDeployment(ctx, payloadDeploymentID)
	if err != nil {
		t.Fatalf("accepted immutable image is not rollback-eligible: %v", err)
	}
	if rollback.RollbackSourceID != payloadDeploymentID ||
		rollback.ImageDigest != deploymentDigest ||
		rollback.ImageRef != deploymentImage {
		t.Fatalf("rollback record = %#v, want accepted immutable image identity", rollback)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		   SET role='member',updated_at=$1,
		       resource_version=resource_version+1
		 WHERE workspace_id='wrk_deploy_a'
		   AND user_id='usr_deploy_member'`,
		now.Add(3*time.Second),
	); err != nil {
		t.Fatalf("restore member role for action gate: %v", err)
	}
	serveAction := func(
		human authenticatedHuman,
		serviceID string,
		deploymentID string,
		action string,
		key string,
	) *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			tenantDeploymentActionIdempotencyPath(
				serviceID,
				deploymentID,
				action,
			),
			nil,
		)
		request.Header.Set("Authorization", "Bearer "+human.token)
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	var generatedSourceID string
	if err := pool.QueryRow(ctx, `
		SELECT d.id
		  FROM deployments d
		  JOIN operations op ON op.id=d.operation_id
		 WHERE op.idempotency_key='deploy-live-route'`,
	).Scan(&generatedSourceID); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"building", "deploying", "active"} {
		active := state == "active"
		if _, err := pool.Exec(ctx, `
			UPDATE deployments
			   SET state=$1,active=$2,updated_at=$3
			 WHERE id=$4`,
			state,
			active,
			now.Add(4*time.Second),
			generatedSourceID,
		); err != nil {
			t.Fatalf("advance generated source to %s: %v", state, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE service_environment_state
		   SET active_deployment_id=$1,updated_at=$2
		 WHERE service_id='svc_deploy_b'
		   AND environment_id='env_deploy_a'`,
		generatedSourceID,
		now.Add(4*time.Second),
	); err != nil {
		t.Fatalf("set generated route replay active pointer: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,config_version,
			effective_config,variable_versions,snapshot_sha256
		) VALUES (
			$1,'prj_deploy_a','svc_deploy_b','env_deploy_a',1,
			'{}'::jsonb,'[]'::jsonb,$2
		)`,
		generatedSourceID,
		strings.Repeat("a", 64),
	); err != nil {
		t.Fatalf("prepare generated route replay source: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO http_routes (
			id,domain_id,project_id,environment_id,service_id,name,
			path_prefix,target_port,public_enabled,desired_revision,
			created_at,updated_at
		)
		SELECT
			'rte_deploy_local_generated',b.domain_id,b.project_id,
			b.environment_id,b.service_id,'deploy-local-generated',
			'/legacy',8080,TRUE,1,$1,$1
		  FROM service_domain_bindings b
		  JOIN domains d ON d.id=b.domain_id
		 WHERE b.project_id='prj_deploy_a'
		   AND b.environment_id='env_deploy_a'
		   AND b.service_id='svc_deploy_b'
		   AND d.kind='generated'`,
		now,
	); err != nil {
		t.Fatalf("seed deployment-local generated route: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_route_intents (
			deployment_id,route_id,domain_id,project_id,
			environment_id,service_id,created_at
		)
		SELECT $1,r.id,r.domain_id,r.project_id,
		       r.environment_id,r.service_id,$2
		  FROM http_routes r
		 WHERE r.id IN (
			'rte_deploy_local_generated',
			'rte_deploy_custom_recovery'
		 )
		ON CONFLICT (deployment_id,route_id) DO NOTHING`,
		generatedSourceID,
		now,
	); err != nil {
		t.Fatalf("seed complete source route set: %v", err)
	}
	generatedReplay := serveAction(
		memberFirst,
		"svc_deploy_b",
		generatedSourceID,
		"redeploy",
		"deployment-action-generated-route",
	)
	if generatedReplay.Code != http.StatusAccepted {
		t.Fatalf(
			"generated route replay status=%d body=%s",
			generatedReplay.Code,
			generatedReplay.Body.String(),
		)
	}
	var generatedReplayResult DeploymentActionResult
	if err := json.Unmarshal(generatedReplay.Body.Bytes(), &generatedReplayResult); err != nil {
		t.Fatal(err)
	}
	var clonedBindings, clonedCustomBindings int
	var replayRoutePublic bool
	var replayRouteHost, replayRouteName, replayRoutePath string
	if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM deployment_route_intents
				 WHERE deployment_id=$1),
				(SELECT COUNT(*)
				   FROM deployment_route_intents i
				   JOIN domains d ON d.id=i.domain_id
				  WHERE i.deployment_id=$1
				    AND d.kind='custom'),
				(payload->'route'->>'public')::boolean,
			COALESCE(payload->'route'->>'host',''),
			COALESCE(payload->'route'->>'name',''),
			COALESCE(payload->'route'->>'path','')
		  FROM staged_work_outbox
		 WHERE deployment_id=$1`,
		generatedReplayResult.Deployment.ID,
	).Scan(
		&clonedBindings,
		&clonedCustomBindings,
		&replayRoutePublic,
		&replayRouteHost,
		&replayRouteName,
		&replayRoutePath,
	); err != nil {
		t.Fatal(err)
	}
	if clonedBindings != 2 ||
		clonedCustomBindings != 0 ||
		!replayRoutePublic ||
		replayRouteHost != "" ||
		replayRouteName == "" ||
		replayRoutePath == "" {
		t.Fatalf(
			"generated replay bindings=%d custom=%d payload route=(public=%t host=%q name=%q path=%q)",
			clonedBindings,
			clonedCustomBindings,
			replayRoutePublic,
			replayRouteHost,
			replayRouteName,
			replayRoutePath,
		)
	}
	actionStart := make(chan struct{})
	actionResults := make(chan *httptest.ResponseRecorder, 2)
	for _, human := range []authenticatedHuman{memberFirst, memberSecond} {
		human := human
		go func() {
			<-actionStart
			actionResults <- serveAction(
				human,
				"svc_deploy_a",
				"dep_deploy_existing",
				"restart",
				"deployment-action-shared",
			)
		}()
	}
	close(actionStart)
	actionFirst := <-actionResults
	actionReplay := <-actionResults
	if actionFirst.Header().Get("Idempotent-Replayed") == "true" {
		actionFirst, actionReplay = actionReplay, actionFirst
	}
	if actionFirst.Code != http.StatusAccepted ||
		actionReplay.Code != http.StatusAccepted ||
		actionFirst.Body.String() != actionReplay.Body.String() ||
		actionFirst.Header().Get("Idempotent-Replayed") != "" ||
		actionReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"human action/replay = %d/%q/%s then %d/%q/%s",
			actionFirst.Code,
			actionFirst.Header().Get("Idempotent-Replayed"),
			actionFirst.Body.String(),
			actionReplay.Code,
			actionReplay.Header().Get("Idempotent-Replayed"),
			actionReplay.Body.String(),
		)
	}
	var acceptedAction DeploymentActionResult
	if err := json.Unmarshal(actionFirst.Body.Bytes(), &acceptedAction); err != nil {
		t.Fatal(err)
	}
	if acceptedAction.ActionID == "" ||
		acceptedAction.OutboxID == "" ||
		!acceptedAction.Created ||
		acceptedAction.Deployment.ID != "dep_deploy_existing" {
		t.Fatalf("accepted runtime action = %#v", acceptedAction)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,config_version,
			effective_config,variable_versions,snapshot_sha256
		) VALUES (
			'dep_deploy_existing','prj_deploy_a','svc_deploy_a','env_deploy_a',1,
			'{}'::jsonb,'[]'::jsonb,$1
		)`,
		strings.Repeat("f", 64),
	); err != nil {
		t.Fatalf("seed action replay snapshot: %v", err)
	}
	imageReplay := serveAction(
		memberFirst,
		"svc_deploy_a",
		"dep_deploy_existing",
		"redeploy",
		"deployment-action-image",
	)
	if imageReplay.Code != http.StatusAccepted {
		t.Fatalf(
			"human image replay status=%d body=%s",
			imageReplay.Code,
			imageReplay.Body.String(),
		)
	}
	var acceptedImageReplay DeploymentActionResult
	if err := json.Unmarshal(imageReplay.Body.Bytes(), &acceptedImageReplay); err != nil {
		t.Fatal(err)
	}
	if !acceptedImageReplay.Created ||
		acceptedImageReplay.Deployment.ID == "dep_deploy_existing" ||
		acceptedImageReplay.Deployment.RollbackSourceID != "dep_deploy_existing" ||
		acceptedImageReplay.Deployment.ImageDigest !=
			"sha256:"+strings.Repeat("e", 64) {
		t.Fatalf("accepted image replay = %#v", acceptedImageReplay)
	}

	viewerAction := serveAction(
		viewer,
		"svc_deploy_a",
		"dep_deploy_existing",
		"restart",
		"deployment-action-viewer",
	)
	raceAction := serveAction(
		race,
		"svc_deploy_a",
		"dep_deploy_existing",
		"restart",
		"deployment-action-race",
	)
	crossServiceAction := serveAction(
		memberFirst,
		"svc_deploy_b",
		"dep_deploy_existing",
		"restart",
		"deployment-action-cross-service",
	)
	pendingRuntimeAction := serveAction(
		memberFirst,
		"svc_deploy_a",
		"dep_deploy_existing",
		"stop",
		"deployment-action-pending-conflict",
	)
	inflightImageReplay := serveAction(
		memberFirst,
		"svc_deploy_a",
		payloadDeploymentID,
		"redeploy",
		"deployment-action-inflight-conflict",
	)
	if viewerAction.Code != http.StatusForbidden ||
		raceAction.Code != http.StatusForbidden ||
		crossServiceAction.Code != http.StatusNotFound ||
		pendingRuntimeAction.Code != http.StatusConflict ||
		inflightImageReplay.Code != http.StatusConflict {
		t.Fatalf(
			"action denial viewer=%d/%s race=%d/%s cross=%d/%s pending=%d/%s inflight=%d/%s",
			viewerAction.Code,
			viewerAction.Body.String(),
			raceAction.Code,
			raceAction.Body.String(),
			crossServiceAction.Code,
			crossServiceAction.Body.String(),
			pendingRuntimeAction.Code,
			pendingRuntimeAction.Body.String(),
			inflightImageReplay.Code,
			inflightImageReplay.Body.String(),
		)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		   SET role='viewer',updated_at=$1,
		       resource_version=resource_version+1
		 WHERE workspace_id='wrk_deploy_a'
		   AND user_id='usr_deploy_member'`,
		now.Add(4*time.Second),
	); err != nil {
		t.Fatalf("demote action member: %v", err)
	}
	deniedActionReplay := serveAction(
		memberSecond,
		"svc_deploy_a",
		"dep_deploy_existing",
		"restart",
		"deployment-action-shared",
	)
	if deniedActionReplay.Code != http.StatusForbidden {
		t.Fatalf(
			"demoted action replay status=%d body=%s",
			deniedActionReplay.Code,
			deniedActionReplay.Body.String(),
		)
	}

	actionCoordinator, ok := stack.TenantDeploymentActions.(*PostgresTenantDeploymentActionCoordinator)
	if !ok {
		t.Fatalf(
			"tenant action coordinator = %T, want postgres",
			stack.TenantDeploymentActions,
		)
	}
	faultActionPath := tenantDeploymentActionIdempotencyPath(
		"svc_deploy_a",
		"dep_deploy_existing",
		"redeploy",
	)
	_, actionFaultErr := actionCoordinator.CommitTenantDeploymentAction(
		ctx,
		TenantDeploymentActionRequest{
			Principal:    fault.principal,
			Session:      fault.session,
			ServiceID:    "svc_deploy_a",
			DeploymentID: "dep_deploy_existing",
			Action:       "redeploy",
			Key:          "deployment-action-audit-fault",
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       faultActionPath,
				BodySHA256: idempotency.BodyHashSHA256(nil),
			},
			RequestID: "req_deploy_fault",
			SourceIP:  "192.0.2.60:443",
			UserAgent: "midnight-action-integration",
		},
	)
	if actionFaultErr == nil {
		t.Fatal("deployment action audit fault unexpectedly committed")
	}

	var (
		actionOperations, actionRows, actionOutbox, actionReservations int
		actionAudits, runtimeCapabilityAudits, replayCapabilityAudits  int
		deniedActionArtifacts, faultActionArtifacts                    int
		actionActor, actionTargetKind, actionTargetID                  string
		actionKey, actionReservationActor, actionReservationTenant     string
		actionReservationPath, actionReservationState                  string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM operations WHERE id=$1),
			(SELECT COUNT(*) FROM deployment_runtime_actions
			 WHERE operation_id=$1),
			(SELECT COUNT(*) FROM staged_work_outbox
			 WHERE operation_id=$1),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='deployment-action-shared'
			   AND path_template=$2),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='deployment.action.accept'
			   AND resource_id='dep_deploy_existing'
			   AND actor_id='usr_deploy_member'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='deployment.action.accept'
			   AND resource_id='dep_deploy_existing'
			   AND actor_id='usr_deploy_member'
			   AND after->>'action'='restart'
			   AND attrs->>'capability'='service.write'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='deployment.action.accept'
			   AND resource_id='dep_deploy_existing'
			   AND actor_id='usr_deploy_member'
			   AND after->>'action'='redeploy'
			   AND attrs->>'capability'='deployment.trigger.cli'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
				'deployment-action-viewer',
				'deployment-action-race',
				'deployment-action-cross-service',
				'deployment-action-pending-conflict',
				'deployment-action-inflight-conflict'
			 )),
			(
			 (SELECT COUNT(*) FROM operations
			   WHERE idempotency_key='deployment-action-audit-fault') +
			 (SELECT COUNT(*) FROM idempotency_keys
			   WHERE key='deployment-action-audit-fault') +
			 (SELECT COUNT(*) FROM audit_logs
			   WHERE request_id='req_deploy_fault')
			)`,
		string(acceptedAction.Operation.ID),
		tenantDeploymentActionIdempotencyPath(
			"svc_deploy_a",
			"dep_deploy_existing",
			"restart",
		),
	).Scan(
		&actionOperations,
		&actionRows,
		&actionOutbox,
		&actionReservations,
		&actionAudits,
		&runtimeCapabilityAudits,
		&replayCapabilityAudits,
		&deniedActionArtifacts,
		&faultActionArtifacts,
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT o.actor,o.target_resource_kind,o.target_resource_id,o.idempotency_key,
		       i.actor,i.tenant,i.path_template,i.state
		  FROM operations o
		  JOIN idempotency_keys i ON i.operation_id=o.id
		 WHERE o.id=$1`,
		string(acceptedAction.Operation.ID),
	).Scan(
		&actionActor,
		&actionTargetKind,
		&actionTargetID,
		&actionKey,
		&actionReservationActor,
		&actionReservationTenant,
		&actionReservationPath,
		&actionReservationState,
	); err != nil {
		t.Fatal(err)
	}
	if actionOperations != 1 ||
		actionRows != 1 ||
		actionOutbox != 1 ||
		actionReservations != 1 ||
		actionAudits != 2 ||
		runtimeCapabilityAudits != 1 ||
		replayCapabilityAudits != 1 ||
		deniedActionArtifacts != 0 ||
		faultActionArtifacts != 0 ||
		actionActor != "usr_deploy_member" ||
		actionTargetKind != "deployment" ||
		actionTargetID != "dep_deploy_existing" ||
		actionKey != "deployment-action-shared" ||
		actionReservationActor != "user:usr_deploy_member" ||
		actionReservationTenant != string(projectScopedIdempotencyTenant(
			"wrk_deploy_a",
			"prj_deploy_a",
		)) ||
		actionReservationPath != tenantDeploymentActionIdempotencyPath(
			"svc_deploy_a",
			"dep_deploy_existing",
			"restart",
		) ||
		actionReservationState != "SUCCEEDED" {
		t.Fatalf(
			"action lineage rows=(%d,%d,%d,%d,%d) typed_audits=(%d,%d) denied=%d fault=%d operation=(%s,%s,%s,%s) reservation=(%s,%s,%s,%s)",
			actionOperations,
			actionRows,
			actionOutbox,
			actionReservations,
			actionAudits,
			runtimeCapabilityAudits,
			replayCapabilityAudits,
			deniedActionArtifacts,
			faultActionArtifacts,
			actionActor,
			actionTargetKind,
			actionTargetID,
			actionKey,
			actionReservationActor,
			actionReservationTenant,
			actionReservationPath,
			actionReservationState,
		)
	}

	var deniedReservations, deniedOperations, deniedDeployments, deniedOutbox, deniedAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
				'deploy-viewer-denied',
				'deploy-restricted-concealed',
				'deploy-foreign-concealed',
				'deploy-route-rejected',
				'deploy-race-denied'
			 )),
			(SELECT COUNT(*) FROM operations
			 WHERE idempotency_key IN (
				'deploy-viewer-denied',
				'deploy-restricted-concealed',
				'deploy-foreign-concealed',
				'deploy-route-rejected',
				'deploy-race-denied'
			 )),
			(SELECT COUNT(*) FROM deployments d
			 JOIN operations op ON op.id=d.operation_id
			 WHERE op.idempotency_key IN (
				'deploy-viewer-denied',
				'deploy-restricted-concealed',
				'deploy-foreign-concealed',
				'deploy-route-rejected',
				'deploy-race-denied'
			 )),
			(SELECT COUNT(*) FROM deploy_intake_outbox o
			 JOIN operations op ON op.id=o.operation_id
			 WHERE op.idempotency_key IN (
				'deploy-viewer-denied',
				'deploy-restricted-concealed',
				'deploy-foreign-concealed',
				'deploy-route-rejected',
				'deploy-race-denied'
			 )),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE request_id IN (
				'req_deploy_viewer',
				'req_deploy_restricted',
				'req_deploy_foreign',
				'req_deploy_route',
				'req_deploy_race'
			 ))`,
	).Scan(
		&deniedReservations,
		&deniedOperations,
		&deniedDeployments,
		&deniedOutbox,
		&deniedAudits,
	); err != nil {
		t.Fatal(err)
	}
	if deniedReservations != 0 ||
		deniedOperations != 0 ||
		deniedDeployments != 0 ||
		deniedOutbox != 0 ||
		deniedAudits != 0 {
		t.Fatalf(
			"denied requests left reservations=%d operations=%d deployments=%d outbox=%d audits=%d",
			deniedReservations,
			deniedOperations,
			deniedDeployments,
			deniedOutbox,
			deniedAudits,
		)
	}
}
