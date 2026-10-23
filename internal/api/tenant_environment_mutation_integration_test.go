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

func TestTenantEnvironmentMutationAuthorizationReplayRollbackAndRetryIdentity(t *testing.T) {
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

	now := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	password := "tenant environment correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_environment_alice','alice-environment@example.com','Alice',$1,1,$2,$2),
			('usr_environment_viewer','viewer-environment@example.com','Viewer',$1,1,$2,$2),
			('usr_environment_session','session-environment@example.com','Session',$1,1,$2,$2),
			('usr_environment_grant','grant-environment@example.com','Grant',$1,1,$2,$2),
			('usr_environment_race','race-environment@example.com','Race',$1,1,$2,$2),
			('usr_environment_audit','audit-environment@example.com','Audit',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_environment_a','environment-a','Environment A',$1,$1),
			('wrk_environment_b','environment-b','Environment B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_environment_a','wrk_environment_a','api','API',$1,$1),
			('prj_environment_b','wrk_environment_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('wrk_environment_a','usr_environment_alice','member',$1,$1,$1,$1),
			('wrk_environment_a','usr_environment_viewer','viewer',$1,$1,$1,$1),
			('wrk_environment_a','usr_environment_session','member',$1,$1,$1,$1),
			('wrk_environment_a','usr_environment_race','member',$1,$1,$1,$1),
			('wrk_environment_a','usr_environment_audit','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES
			('prj_environment_a','usr_environment_grant','editor',$1,$1,$1,$1,1)`,
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
		result, loginErr := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email:      email,
			Password:   password,
			ClientKind: "api",
			RemoteAddr: "192.0.2.31:443",
			UserAgent:  "midnight-environment-test",
		})
		if loginErr != nil {
			t.Fatalf("login %s: %v", email, loginErr)
		}
		return result
	}
	aliceFirst := login("alice-environment@example.com")
	aliceSecond := login("alice-environment@example.com")
	viewer := login("viewer-environment@example.com")
	sessionUser := login("session-environment@example.com")
	grantUser := login("grant-environment@example.com")
	raceUser := login("race-environment@example.com")
	auditUser := login("audit-environment@example.com")

	postEnvironment := func(token, key, body string) tenantEnvironmentMutationHTTPResponse {
		t.Helper()
		response := doRouterRequest(
			handler,
			http.MethodPost,
			v1alphaPrefix+"/environments",
			body,
			map[string]string{
				"Authorization":   "Bearer " + token,
				"Idempotency-Key": key,
			},
		)
		return tenantEnvironmentMutationHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Location: response.Header().Get("Location"),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}

	aliceBody := `{"project_id":"prj_environment_a","slug":"staging","name":"Staging","tier":"staging","is_protected":false}`
	first := postEnvironment(aliceFirst.AccessToken, "environment-shared-key", aliceBody)
	if first.Code != http.StatusCreated || first.Location == "" || first.Replayed != "" {
		t.Fatalf("alice create = %#v", first)
	}
	var aliceEnvironment Environment
	if err := json.Unmarshal([]byte(first.Body), &aliceEnvironment); err != nil {
		t.Fatalf("decode alice environment: %v body=%s", err, first.Body)
	}
	if aliceEnvironment.IsProtected {
		t.Fatalf("ordinary member created protected environment: %#v", aliceEnvironment)
	}
	replay := postEnvironment(aliceSecond.AccessToken, "environment-shared-key", aliceBody)
	if replay.Code != first.Code ||
		replay.Body != first.Body ||
		replay.Location != first.Location ||
		replay.Replayed != "true" {
		t.Fatalf("same-user cross-session replay=%#v first=%#v", replay, first)
	}

	protectedDenied := postEnvironment(
		aliceFirst.AccessToken,
		"environment-protected-denied",
		`{"project_id":"prj_environment_a","slug":"production","name":"Production","tier":"production","is_protected":true}`,
	)
	if protectedDenied.Code != http.StatusForbidden || protectedDenied.Replayed != "" {
		t.Fatalf("ordinary protected create = %#v", protectedDenied)
	}
	viewerDenied := postEnvironment(
		viewer.AccessToken,
		"environment-viewer-denied",
		`{"project_id":"prj_environment_a","slug":"viewer-denied","name":"Denied","tier":"development","is_protected":false}`,
	)
	if viewerDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %#v", viewerDenied)
	}
	crossTenantDenied := postEnvironment(
		aliceFirst.AccessToken,
		"environment-cross-denied",
		`{"project_id":"prj_environment_b","slug":"cross-denied","name":"Denied","tier":"development","is_protected":false}`,
	)
	if crossTenantDenied.Code != http.StatusNotFound ||
		strings.Contains(crossTenantDenied.Body, "wrk_environment_b") {
		t.Fatalf("cross-tenant create = %#v", crossTenantDenied)
	}

	sessionBody := `{"project_id":"prj_environment_a","slug":"session-owned","name":"Session","tier":"development","is_protected":false}`
	sessionCreated := postEnvironment(
		sessionUser.AccessToken,
		"environment-session-key",
		sessionBody,
	)
	if sessionCreated.Code != http.StatusCreated {
		t.Fatalf("session user create = %#v", sessionCreated)
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
		"environment-session-stale",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	staleSessionReplay := postEnvironment(
		sessionUser.AccessToken,
		"environment-session-key",
		sessionBody,
	)
	if staleSessionReplay.Code != http.StatusUnauthorized ||
		staleSessionReplay.Replayed != "" {
		t.Fatalf("stale session replay = %#v", staleSessionReplay)
	}
	_, err = coordinator.CreateEnvironment(ctx, TenantEnvironmentCreateRequest{
		Principal: sessionPrincipal,
		Session:   sessionUser.Session,
		Environment: Environment{
			ProjectID: "prj_environment_a",
			Slug:      "stale-coordinator",
			Name:      "Stale",
			Tier:      "development",
		},
		Key: "environment-stale-coordinator",
		Fingerprint: idempotency.RequestFingerprint{
			Method: http.MethodPost, Path: v1alphaPrefix + "/environments",
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{"stale":true}`)),
		},
		RequestID: "req_environment_stale_coordinator",
	})
	if !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("coordinator accepted stale authenticated tuple: %v", err)
	}

	grantBody := `{"project_id":"prj_environment_a","slug":"grant-owned","name":"Grant","tier":"development","is_protected":false}`
	grantCreated := postEnvironment(
		grantUser.AccessToken,
		"environment-grant-key",
		grantBody,
	)
	if grantCreated.Code != http.StatusCreated {
		t.Fatalf("project editor create = %#v", grantCreated)
	}
	var grantEnvironment Environment
	if err := json.Unmarshal([]byte(grantCreated.Body), &grantEnvironment); err != nil {
		t.Fatal(err)
	}
	grantTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"workspace-members:wrk_environment_a",
	); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"project-members:prj_environment_a",
	); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := grantTx.Exec(ctx, `
		DELETE FROM project_members
		WHERE project_id='prj_environment_a'
		  AND user_id='usr_environment_grant'`); err != nil {
		_ = grantTx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := grantTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	staleGrantReplay := postEnvironment(
		grantUser.AccessToken,
		"environment-grant-key",
		grantBody,
	)
	if staleGrantReplay.Code != http.StatusNotFound ||
		staleGrantReplay.Replayed != "" ||
		strings.Contains(staleGrantReplay.Body, string(grantEnvironment.ID)) {
		t.Fatalf("stale grant replay leaked cached result = %#v", staleGrantReplay)
	}

	duplicateBody := `{"project_id":"prj_environment_a","slug":"duplicate","name":"Duplicate","tier":"development","is_protected":false}`
	duplicateSeed := postEnvironment(
		aliceFirst.AccessToken,
		"environment-duplicate-seed",
		duplicateBody,
	)
	if duplicateSeed.Code != http.StatusCreated {
		t.Fatalf("duplicate seed create = %#v", duplicateSeed)
	}
	duplicateConflict := postEnvironment(
		aliceFirst.AccessToken,
		"environment-duplicate-conflict",
		duplicateBody,
	)
	if duplicateConflict.Code != http.StatusConflict ||
		duplicateConflict.Replayed != "" {
		t.Fatalf("duplicate conflict = %#v", duplicateConflict)
	}
	duplicateReplay := postEnvironment(
		aliceSecond.AccessToken,
		"environment-duplicate-conflict",
		duplicateBody,
	)
	if duplicateReplay.Code != duplicateConflict.Code ||
		duplicateReplay.Body != duplicateConflict.Body ||
		duplicateReplay.Location != duplicateConflict.Location ||
		duplicateReplay.Replayed != "true" {
		t.Fatalf(
			"duplicate replay=%#v conflict=%#v",
			duplicateReplay,
			duplicateConflict,
		)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_environment_create_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action='environment.create'
			   AND NEW.actor_id='usr_environment_audit' THEN
				RAISE EXCEPTION 'forced environment audit failure';
			END IF;
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_environment_create_audit
			BEFORE INSERT ON audit_logs
			FOR EACH ROW EXECUTE FUNCTION fail_environment_create_audit()`); err != nil {
		t.Fatalf("install environment audit failure trigger: %v", err)
	}
	auditFailure := postEnvironment(
		auditUser.AccessToken,
		"environment-audit-failure",
		`{"project_id":"prj_environment_a","slug":"audit-failure","name":"Audit failure","tier":"development","is_protected":false}`,
	)
	if auditFailure.Code != http.StatusServiceUnavailable ||
		auditFailure.Replayed != "" {
		t.Fatalf("audit failure response = %#v", auditFailure)
	}
	var auditFailureEnvironments, auditFailureReservations, auditFailureAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM environments
			 WHERE project_id='prj_environment_a' AND slug='audit-failure'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-audit-failure'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.create'
			   AND actor_id='usr_environment_audit')`,
	).Scan(
		&auditFailureEnvironments,
		&auditFailureReservations,
		&auditFailureAudits,
	); err != nil {
		t.Fatal(err)
	}
	if auditFailureEnvironments != 0 ||
		auditFailureReservations != 0 ||
		auditFailureAudits != 0 {
		t.Fatalf(
			"audit rollback environments=%d reservations=%d audits=%d",
			auditFailureEnvironments,
			auditFailureReservations,
			auditFailureAudits,
		)
	}

	var deniedReservations, deniedEnvironments, deniedAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
			   'environment-protected-denied',
			   'environment-viewer-denied',
			   'environment-cross-denied',
			   'environment-stale-coordinator'
			 )),
			(SELECT COUNT(*) FROM environments
			 WHERE slug IN (
			   'production',
			   'viewer-denied',
			   'cross-denied',
			   'stale-coordinator'
			 )),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.create'
			   AND actor_id IN (
			     'usr_environment_viewer',
			     'usr_environment_alice'
			   )
			   AND policy_decision='denied'
			   AND COALESCE(reason_code, '') <> 'RESOURCE_CONFLICT')`,
	).Scan(&deniedReservations, &deniedEnvironments, &deniedAudits); err != nil {
		t.Fatal(err)
	}
	if deniedReservations != 0 || deniedEnvironments != 0 || deniedAudits != 0 {
		t.Fatalf(
			"authorization denials left reservations=%d environments=%d audits=%d",
			deniedReservations,
			deniedEnvironments,
			deniedAudits,
		)
	}

	// Force a compatibility writer to win after the coordinator's first
	// SERIALIZABLE snapshot. The retry must reuse the one pre-minted identity
	// and store the canonical terminal conflict response.
	compatTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compatTx.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, is_protected,
			created_at, updated_at
		) VALUES (
			'env_compatibility_winner',
			'prj_environment_a',
			'retry-race',
			'Compatibility',
			'development',
			FALSE,
			$1,
			$1
		)`,
		now,
	); err != nil {
		_ = compatTx.Rollback(ctx)
		t.Fatal(err)
	}
	var generated atomic.Int32
	var invalidPrefix atomic.Bool
	coordinator.IDFactory = func(prefix orailtypes.IDPrefix) orailtypes.ID {
		if prefix != orailtypes.IDPrefixEnvironment {
			invalidPrefix.Store(true)
		}
		generated.Add(1)
		return "env_retry_prepared_once"
	}
	raceResult := make(chan tenantEnvironmentMutationHTTPResponse, 1)
	go func() {
		raceResult <- postEnvironment(
			raceUser.AccessToken,
			"environment-retry-key",
			`{"project_id":"prj_environment_a","slug":"retry-race","name":"Race","tier":"development","is_protected":false}`,
		)
	}()
	select {
	case early := <-raceResult:
		_ = compatTx.Rollback(ctx)
		t.Fatalf("environment mutation did not wait on compatibility insert: %#v", early)
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
		t.Fatalf(
			"retry conflict=%#v generated=%d invalid_prefix=%t",
			retryConflict,
			generated.Load(),
			invalidPrefix.Load(),
		)
	}

	var (
		preparedRows      int
		compatibilityRows int
		retryReservations int
		retryAudits       int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM environments
			 WHERE id='env_retry_prepared_once'),
			(SELECT COUNT(*) FROM environments
			 WHERE id='env_compatibility_winner' AND slug='retry-race'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-retry-key' AND state='FAILED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.create'
			   AND actor_id='usr_environment_race'
			   AND policy_decision='denied'
			   AND reason_code='RESOURCE_CONFLICT')`,
	).Scan(
		&preparedRows,
		&compatibilityRows,
		&retryReservations,
		&retryAudits,
	); err != nil {
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

	var (
		aliceRows          int
		aliceReservations  int
		aliceAudits        int
		duplicateRows      int
		duplicateFailures  int
		duplicateAuditRows int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM environments WHERE id=$1),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-shared-key' AND state='SUCCEEDED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.create'
			   AND actor_id='usr_environment_alice'
			   AND resource_id=$1
			   AND policy_decision='allowed'),
			(SELECT COUNT(*) FROM environments
			 WHERE project_id='prj_environment_a' AND slug='duplicate'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-duplicate-conflict' AND state='FAILED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.create'
			   AND actor_id='usr_environment_alice'
			   AND reason_code='RESOURCE_CONFLICT')`,
		string(aliceEnvironment.ID),
	).Scan(
		&aliceRows,
		&aliceReservations,
		&aliceAudits,
		&duplicateRows,
		&duplicateFailures,
		&duplicateAuditRows,
	); err != nil {
		t.Fatal(err)
	}
	if aliceRows != 1 ||
		aliceReservations != 1 ||
		aliceAudits != 1 ||
		duplicateRows != 1 ||
		duplicateFailures != 1 ||
		duplicateAuditRows != 1 {
		t.Fatalf(
			"artifacts alice=(%d,%d,%d) duplicate=(%d,%d,%d)",
			aliceRows,
			aliceReservations,
			aliceAudits,
			duplicateRows,
			duplicateFailures,
			duplicateAuditRows,
		)
	}
}

type tenantEnvironmentMutationHTTPResponse struct {
	Code     int
	Body     string
	Location string
	Replayed string
}
