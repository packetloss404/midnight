//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

func TestTenantProjectMutationAuthorizationReplayAndFencing(t *testing.T) {
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

	now := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	password := "tenant project correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_alice','alice-project@example.com','Alice',$1,1,$2,$2),
			('usr_alice_guard','alice-guard@example.com','Alice guard',$1,1,$2,$2),
			('usr_bob','bob-project@example.com','Bob',$1,1,$2,$2),
			('usr_viewer','viewer-project@example.com','Viewer',$1,1,$2,$2),
			('usr_race','race-project@example.com','Race',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed tenant mutation users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_alice','alice','Alice',$1,$1),
			('wrk_bob','bob','Bob',$1,$1),
			('wrk_race','race','Race',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed tenant mutation workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at, created_at, updated_at
		) VALUES
			('wrk_alice','usr_alice','admin',$1,$1,$1,$1),
			('wrk_alice','usr_alice_guard','admin',$1,$1,$1,$1),
			('wrk_alice','usr_viewer','viewer',$1,$1,$1,$1),
			('wrk_bob','usr_bob','admin',$1,$1,$1,$1),
			('wrk_race','usr_race','admin',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed tenant mutation identities: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api",
			RemoteAddr: "192.0.2.10:443",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return result
	}
	aliceFirst := login("alice-project@example.com")
	aliceSecond := login("alice-project@example.com")
	aliceGuard := login("alice-guard@example.com")
	bob := login("bob-project@example.com")
	viewer := login("viewer-project@example.com")
	race := login("race-project@example.com")

	postProject := func(token, key, body string) *tenantMutationHTTPResponse {
		t.Helper()
		response := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/projects", body, map[string]string{
			"Authorization":   "Bearer " + token,
			"Idempotency-Key": key,
		})
		return &tenantMutationHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Location: response.Header().Get("Location"),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}

	aliceBody := `{"workspace_id":"wrk_alice","slug":"api","display_name":"API"}`
	first := postProject(aliceFirst.AccessToken, "shared-create-key", aliceBody)
	if first.Code != http.StatusCreated || first.Location == "" || first.Replayed != "" {
		t.Fatalf("alice create = %#v", first)
	}
	var aliceProject Project
	if err := json.Unmarshal([]byte(first.Body), &aliceProject); err != nil {
		t.Fatalf("decode alice project: %v body=%s", err, first.Body)
	}
	replay := postProject(aliceSecond.AccessToken, "shared-create-key", aliceBody)
	if replay.Code != first.Code || replay.Body != first.Body || replay.Location != first.Location || replay.Replayed != "true" {
		t.Fatalf("same actor cross-session replay=%#v first=%#v", replay, first)
	}
	conflict := postProject(aliceGuard.AccessToken, "stored-conflict-key", aliceBody)
	if conflict.Code != http.StatusConflict || conflict.Replayed != "" {
		t.Fatalf("project conflict = %#v", conflict)
	}
	conflictReplay := postProject(aliceGuard.AccessToken, "stored-conflict-key", aliceBody)
	if conflictReplay.Code != conflict.Code ||
		conflictReplay.Body != conflict.Body ||
		conflictReplay.Replayed != "true" {
		t.Fatalf("conflict replay=%#v first=%#v", conflictReplay, conflict)
	}

	bobCreate := postProject(
		bob.AccessToken,
		"shared-create-key",
		`{"workspace_id":"wrk_bob","slug":"api","display_name":"API"}`,
	)
	if bobCreate.Code != http.StatusCreated || bobCreate.Replayed != "" || bobCreate.Location == first.Location {
		t.Fatalf("different actor/tenant create = %#v", bobCreate)
	}
	bobPrincipal, _, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+bob.AccessToken)
	if err != nil {
		t.Fatalf("authenticate bob before revoke: %v", err)
	}
	if err := stack.IdentitySessions.Revoke(ctx, bobPrincipal, bob.Session.ID, "integration-replay-gate"); err != nil {
		t.Fatalf("revoke bob: %v", err)
	}
	revokedReplay := postProject(
		bob.AccessToken,
		"shared-create-key",
		`{"workspace_id":"wrk_bob","slug":"api","display_name":"API"}`,
	)
	if revokedReplay.Code != http.StatusUnauthorized || revokedReplay.Replayed != "" {
		t.Fatalf("revoked session received replay = %#v", revokedReplay)
	}

	viewerDenied := postProject(
		viewer.AccessToken,
		"viewer-denied-key",
		`{"workspace_id":"wrk_alice","slug":"viewer-denied","display_name":"Denied"}`,
	)
	if viewerDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer create = %#v", viewerDenied)
	}
	crossTenantDenied := postProject(
		aliceFirst.AccessToken,
		"cross-tenant-denied-key",
		`{"workspace_id":"wrk_bob","slug":"cross-denied","display_name":"Denied"}`,
	)
	if crossTenantDenied.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant create = %#v", crossTenantDenied)
	}

	var deniedReservations, deniedProjects int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			  WHERE key IN ('viewer-denied-key','cross-tenant-denied-key')) AS reservations,
			(SELECT COUNT(*) FROM projects
			  WHERE slug IN ('viewer-denied','cross-denied')) AS projects`,
	).Scan(&deniedReservations, &deniedProjects); err != nil {
		t.Fatal(err)
	}
	if deniedReservations != 0 || deniedProjects != 0 {
		t.Fatalf("denied writes left reservations=%d projects=%d", deniedReservations, deniedProjects)
	}

	// Reauthorization precedes replay release. Demoting Alice after the first
	// commit must deny the cached response rather than disclosing it.
	if _, err := stack.IdentityAccess.PutWorkspaceMember(
		ctx, "wrk_alice", "usr_alice", identity.WorkspaceRoleViewer,
		"usr_alice", now.Add(time.Second),
	); err != nil {
		t.Fatalf("demote alice: %v", err)
	}
	deniedReplay := postProject(aliceSecond.AccessToken, "shared-create-key", aliceBody)
	if deniedReplay.Code != http.StatusForbidden || deniedReplay.Replayed != "" ||
		strings.Contains(deniedReplay.Body, string(aliceProject.ID)) {
		t.Fatalf("demoted replay leaked cached result: %#v", deniedReplay)
	}

	// Hold the same workspace-members advisory lock used by membership writes,
	// stage a demotion, and prove the project coordinator decides only after
	// that change commits.
	lockTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "workspace-members:wrk_race"); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(ctx, `
		UPDATE workspace_members
		SET role='viewer', updated_at=$1, resource_version=resource_version+1
		WHERE workspace_id='wrk_race' AND user_id='usr_race'`,
		now.Add(2*time.Second),
	); err != nil {
		_ = lockTx.Rollback(ctx)
		t.Fatal(err)
	}
	raceResult := make(chan *tenantMutationHTTPResponse, 1)
	go func() {
		raceResult <- postProject(
			race.AccessToken,
			"race-demotion-key",
			`{"workspace_id":"wrk_race","slug":"must-not-exist","display_name":"Denied"}`,
		)
	}()
	select {
	case early := <-raceResult:
		_ = lockTx.Rollback(ctx)
		t.Fatalf("project mutation bypassed pending membership change: %#v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	raced := <-raceResult
	if raced.Code != http.StatusForbidden {
		t.Fatalf("post-demotion race result = %#v", raced)
	}

	var projectCount, environmentCount, ownerCount int
	var successfulReservations, failedReservations, allowedAudits, deniedAudits, raceArtifacts int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM projects WHERE id=$1),
			(SELECT COUNT(*) FROM environments
			  WHERE project_id=$1 AND slug='production' AND is_protected=TRUE),
			(SELECT COUNT(*) FROM project_members
			  WHERE project_id=$1 AND user_id='usr_alice' AND role='owner'),
			(SELECT COUNT(*) FROM idempotency_keys
			  WHERE key='shared-create-key' AND state='SUCCEEDED'),
			(SELECT COUNT(*) FROM idempotency_keys
			  WHERE key='stored-conflict-key' AND state='FAILED'),
			(SELECT COUNT(*) FROM audit_logs
			  WHERE action='project.create'
			    AND workspace_id='wrk_alice'
			    AND policy_decision='allowed'),
			(SELECT COUNT(*) FROM audit_logs
			  WHERE action='project.create'
			    AND workspace_id='wrk_alice'
			    AND policy_decision='denied'
			    AND reason_code='RESOURCE_CONFLICT'),
			(SELECT COUNT(*) FROM projects WHERE slug='must-not-exist') +
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='race-demotion-key')`,
		string(aliceProject.ID),
	).Scan(
		&projectCount,
		&environmentCount,
		&ownerCount,
		&successfulReservations,
		&failedReservations,
		&allowedAudits,
		&deniedAudits,
		&raceArtifacts,
	); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 || environmentCount != 1 || ownerCount != 1 ||
		successfulReservations != 2 || failedReservations != 1 ||
		allowedAudits != 1 || deniedAudits != 1 || raceArtifacts != 0 {
		t.Fatalf(
			"durable result project=%d env=%d owner=%d success=%d failed=%d allowed_audit=%d denied_audit=%d race=%d",
			projectCount,
			environmentCount,
			ownerCount,
			successfulReservations,
			failedReservations,
			allowedAudits,
			deniedAudits,
			raceArtifacts,
		)
	}
}

type tenantMutationHTTPResponse struct {
	Code     int
	Body     string
	Location string
	Replayed string
}
