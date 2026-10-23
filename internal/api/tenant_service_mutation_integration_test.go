//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestTenantServiceMutationAuthorizationReplayAndRetryIdentity(t *testing.T) {
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

	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	password := "tenant service correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_service_alice','alice-service@example.com','Alice',$1,1,$2,$2),
			('usr_service_viewer','viewer-service@example.com','Viewer',$1,1,$2,$2),
			('usr_service_session','session-service@example.com','Session',$1,1,$2,$2),
			('usr_service_grant','grant-service@example.com','Grant',$1,1,$2,$2),
			('usr_service_race','race-service@example.com','Race',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_service_a','service-a','Service A',$1,$1),
			('wrk_service_b','service-b','Service B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id, workspace_id, slug, display_name, created_at, updated_at)
		VALUES
			('prj_service_a','wrk_service_a','api','API',$1,$1),
			('prj_service_b','wrk_service_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		) VALUES
			('wrk_service_a','usr_service_alice','member',$1,$1,$1,$1),
			('wrk_service_a','usr_service_viewer','viewer',$1,$1,$1,$1),
			('wrk_service_a','usr_service_session','member',$1,$1,$1,$1),
			('wrk_service_a','usr_service_race','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES
			('prj_service_a','usr_service_grant','editor',$1,$1,$1,$1,1)`,
		now,
	); err != nil {
		t.Fatalf("seed project grants: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	coordinator, ok := stack.TenantMutations.(*PostgresTenantMutationCoordinator)
	if !ok {
		t.Fatalf("tenant coordinator = %T", stack.TenantMutations)
	}
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api",
			RemoteAddr: "192.0.2.30:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return result
	}
	aliceFirst := login("alice-service@example.com")
	aliceSecond := login("alice-service@example.com")
	viewer := login("viewer-service@example.com")
	sessionUser := login("session-service@example.com")
	grantUser := login("grant-service@example.com")
	raceUser := login("race-service@example.com")

	postService := func(token, key, body string) tenantServiceMutationHTTPResponse {
		t.Helper()
		response := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/services", body, map[string]string{
			"Authorization":   "Bearer " + token,
			"Idempotency-Key": key,
		})
		return tenantServiceMutationHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Location: response.Header().Get("Location"),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}

	aliceBody := `{"project_id":"prj_service_a","slug":"web","display_name":"Web"}`
	first := postService(aliceFirst.AccessToken, "service-shared-key", aliceBody)
	if first.Code != http.StatusCreated || first.Location == "" || first.Replayed != "" {
		t.Fatalf("alice create = %#v", first)
	}
	var aliceService Service
	if err := json.Unmarshal([]byte(first.Body), &aliceService); err != nil {
		t.Fatalf("decode alice service: %v body=%s", err, first.Body)
	}
	replay := postService(aliceSecond.AccessToken, "service-shared-key", aliceBody)
	if replay.Code != first.Code ||
		replay.Body != first.Body ||
		replay.Location != first.Location ||
		replay.Replayed != "true" {
		t.Fatalf("same-user cross-session replay=%#v first=%#v", replay, first)
	}

	viewerDenied := postService(
		viewer.AccessToken,
		"service-viewer-denied",
		`{"project_id":"prj_service_a","slug":"viewer-denied","display_name":"Denied"}`,
	)
	if viewerDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %#v", viewerDenied)
	}
	crossTenantDenied := postService(
		aliceFirst.AccessToken,
		"service-cross-denied",
		`{"project_id":"prj_service_b","slug":"cross-denied","display_name":"Denied"}`,
	)
	if crossTenantDenied.Code != http.StatusNotFound ||
		strings.Contains(crossTenantDenied.Body, "wrk_service_b") {
		t.Fatalf("cross-tenant create = %#v", crossTenantDenied)
	}

	sessionBody := `{"project_id":"prj_service_a","slug":"session-owned","display_name":"Session"}`
	sessionCreated := postService(sessionUser.AccessToken, "service-session-key", sessionBody)
	if sessionCreated.Code != http.StatusCreated {
		t.Fatalf("session user create = %#v", sessionCreated)
	}
	sessionPrincipal, _, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+sessionUser.AccessToken)
	if err != nil {
		t.Fatalf("authenticate session user: %v", err)
	}
	if err := stack.IdentitySessions.Revoke(ctx, sessionPrincipal, sessionUser.Session.ID, "service-session-stale"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	staleSessionReplay := postService(sessionUser.AccessToken, "service-session-key", sessionBody)
	if staleSessionReplay.Code != http.StatusUnauthorized ||
		staleSessionReplay.Replayed != "" {
		t.Fatalf("stale session replay = %#v", staleSessionReplay)
	}
	_, err = coordinator.CreateService(ctx, TenantServiceCreateRequest{
		Principal: sessionPrincipal,
		Session:   sessionUser.Session,
		Service: Service{
			ProjectID: "prj_service_a", Slug: "stale-coordinator", DisplayName: "Stale",
		},
		Key: "service-stale-coordinator",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/services",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{"stale":true}`)),
		},
		RequestID: "req_stale_coordinator",
	})
	if !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("coordinator accepted stale authenticated tuple: %v", err)
	}

	grantBody := `{"project_id":"prj_service_a","slug":"grant-owned","display_name":"Grant"}`
	grantCreated := postService(grantUser.AccessToken, "service-grant-key", grantBody)
	if grantCreated.Code != http.StatusCreated {
		t.Fatalf("project editor create = %#v", grantCreated)
	}
	var grantService Service
	if err := json.Unmarshal([]byte(grantCreated.Body), &grantService); err != nil {
		t.Fatal(err)
	}
	grantTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:wrk_service_a"); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "project-members:prj_service_a"); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `
		DELETE FROM project_members
		WHERE project_id='prj_service_a' AND user_id='usr_service_grant'`); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := grantTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	staleGrantReplay := postService(grantUser.AccessToken, "service-grant-key", grantBody)
	if staleGrantReplay.Code != http.StatusNotFound ||
		staleGrantReplay.Replayed != "" ||
		strings.Contains(staleGrantReplay.Body, string(grantService.ID)) {
		t.Fatalf("stale grant replay leaked cached result = %#v", staleGrantReplay)
	}

	var deniedReservations, deniedServices, deniedAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
			   'service-viewer-denied',
			   'service-cross-denied',
			   'service-stale-coordinator'
			 )),
			(SELECT COUNT(*) FROM services
			 WHERE slug IN ('viewer-denied','cross-denied','stale-coordinator')),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='service.create'
			   AND actor_id IN ('usr_service_viewer','usr_service_alice')
			   AND policy_decision='denied')`,
	).Scan(&deniedReservations, &deniedServices, &deniedAudits); err != nil {
		t.Fatal(err)
	}
	if deniedReservations != 0 || deniedServices != 0 || deniedAudits != 0 {
		t.Fatalf(
			"authorization denials left reservations=%d services=%d audits=%d",
			deniedReservations,
			deniedServices,
			deniedAudits,
		)
	}

	// Force a compatibility writer to win after the coordinator's first
	// SERIALIZABLE snapshot. The first insert observes a unique race and the
	// second attempt stores the canonical conflict response. IDFactory must be
	// called once for the whole request, never once per transaction attempt.
	compatTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compatTx.Exec(ctx, `
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES (
			'svc_compatibility_winner','prj_service_a','retry-race','Compatibility',$1,$1
		)`,
		now,
	); err != nil {
		_ = compatTx.Rollback(ctx)
		t.Fatal(err)
	}
	var generated atomic.Int32
	var invalidPrefix atomic.Bool
	coordinator.IDFactory = func(prefix orailtypes.IDPrefix) orailtypes.ID {
		if prefix != orailtypes.IDPrefixService {
			invalidPrefix.Store(true)
		}
		generated.Add(1)
		return "svc_retry_prepared_once"
	}
	raceResult := make(chan tenantServiceMutationHTTPResponse, 1)
	go func() {
		raceResult <- postService(
			raceUser.AccessToken,
			"service-retry-key",
			`{"project_id":"prj_service_a","slug":"retry-race","display_name":"Race"}`,
		)
	}()
	select {
	case early := <-raceResult:
		_ = compatTx.Rollback(ctx)
		t.Fatalf("service mutation did not wait on compatibility insert: %#v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := compatTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	retryConflict := <-raceResult
	if retryConflict.Code != http.StatusConflict ||
		retryConflict.Replayed != "" ||
		generated.Load() != 1 ||
		invalidPrefix.Load() {
		t.Fatalf("retry conflict=%#v generated=%d invalid_prefix=%t", retryConflict, generated.Load(), invalidPrefix.Load())
	}

	var preparedRows, compatibilityRows, retryReservations, retryAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM services WHERE id='svc_retry_prepared_once'),
			(SELECT COUNT(*) FROM services
			 WHERE id='svc_compatibility_winner' AND slug='retry-race'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='service-retry-key' AND state='FAILED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='service.create'
			   AND actor_id='usr_service_race'
			   AND policy_decision='denied'
			   AND reason_code='RESOURCE_CONFLICT')`,
	).Scan(&preparedRows, &compatibilityRows, &retryReservations, &retryAudits); err != nil {
		t.Fatal(err)
	}
	if preparedRows != 0 ||
		compatibilityRows != 1 ||
		retryReservations != 1 ||
		retryAudits != 1 {
		t.Fatalf(
			"retry artifacts prepared=%d compatibility=%d reservations=%d audits=%d",
			preparedRows,
			compatibilityRows,
			retryReservations,
			retryAudits,
		)
	}

	var aliceRows, aliceReservations, aliceAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM services WHERE id=$1),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='service-shared-key' AND state='SUCCEEDED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='service.create'
			   AND actor_id='usr_service_alice'
			   AND resource_id=$1
			   AND policy_decision='allowed')`,
		string(aliceService.ID),
	).Scan(&aliceRows, &aliceReservations, &aliceAudits); err != nil {
		t.Fatal(err)
	}
	if aliceRows != 1 || aliceReservations != 1 || aliceAudits != 1 {
		t.Fatalf(
			"exact replay artifacts services=%d reservations=%d audits=%d",
			aliceRows,
			aliceReservations,
			aliceAudits,
		)
	}
}

type tenantServiceMutationHTTPResponse struct {
	Code     int
	Body     string
	Location string
	Replayed string
}
