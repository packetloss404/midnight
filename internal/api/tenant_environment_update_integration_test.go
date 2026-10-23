//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

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

func TestTenantEnvironmentUpdateAuthorizationReplayCASConflictAndRollback(t *testing.T) {
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
	password := "tenant environment update correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_env_update_alice','alice-env-update@example.com','Alice',$1,1,$2,$2),
			('usr_env_update_viewer','viewer-env-update@example.com','Viewer',$1,1,$2,$2),
			('usr_env_update_admin','admin-env-update@example.com','Admin',$1,1,$2,$2),
			('usr_env_update_editor','editor-env-update@example.com','Editor',$1,1,$2,$2),
			('usr_env_update_session','session-env-update@example.com','Session',$1,1,$2,$2),
			('usr_env_update_grant','grant-env-update@example.com','Grant',$1,1,$2,$2),
			('usr_env_update_audit','audit-env-update@example.com','Audit',$1,1,$2,$2),
			('usr_env_update_race','race-env-update@example.com','Race',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (id, slug, display_name, created_at, updated_at)
		VALUES
			('wrk_env_update_a','env-update-a','Environment Update A',$1,$1),
			('wrk_env_update_b','env-update-b','Environment Update B',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_env_update_a','wrk_env_update_a','api','API',$1,$1),
			('prj_env_update_b','wrk_env_update_b','api','API',$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('wrk_env_update_a','usr_env_update_alice','member',$1,$1,$1,$1),
			('wrk_env_update_a','usr_env_update_viewer','viewer',$1,$1,$1,$1),
			('wrk_env_update_a','usr_env_update_admin','admin',$1,$1,$1,$1),
			('wrk_env_update_a','usr_env_update_session','member',$1,$1,$1,$1),
			('wrk_env_update_a','usr_env_update_audit','member',$1,$1,$1,$1),
			('wrk_env_update_a','usr_env_update_race','member',$1,$1,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at, resource_version
		) VALUES
			('prj_env_update_a','usr_env_update_editor','editor',$1,$1,$1,$1,1),
			('prj_env_update_a','usr_env_update_grant','editor',$1,$1,$1,$1,1)`,
		now,
	); err != nil {
		t.Fatalf("seed project grants: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, is_protected,
			access_restricted, deleted_at, created_at, updated_at
		) VALUES
			('env_update_main','prj_env_update_a','main','Main','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_protected','prj_env_update_a','protected','Protected','production',TRUE,FALSE,NULL,$1,$1),
			('env_update_restricted','prj_env_update_a','restricted','Restricted','production',FALSE,TRUE,NULL,$1,$1),
			('env_update_replay','prj_env_update_a','replay','Replay','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_session','prj_env_update_a','session','Session','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_grant','prj_env_update_a','grant','Grant','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_concurrent','prj_env_update_a','concurrent','Concurrent','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_concurrent_cas','prj_env_update_a','concurrent-cas','Concurrent CAS','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_cas','prj_env_update_a','cas','CAS','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_slug_source','prj_env_update_a','slug-source','Slug source','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_slug_taken','prj_env_update_a','slug-taken','Slug taken','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_target_a','prj_env_update_a','target-a','Target A','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_target_b','prj_env_update_a','target-b','Target B','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_audit','prj_env_update_a','audit','Audit','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_race_target','prj_env_update_a','race-target','Race target','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_race_winner','prj_env_update_a','race-winner','Race winner','development',FALSE,FALSE,NULL,$1,$1),
			('env_update_deleted_slug','prj_env_update_a','retired','Retired','development',FALSE,FALSE,$1,$1,$1),
			('env_update_other','prj_env_update_b','other','Other','development',FALSE,FALSE,NULL,$1,$1)`,
		now,
	); err != nil {
		t.Fatalf("seed environments: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, loginErr := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email:      email,
			Password:   password,
			ClientKind: "api",
			RemoteAddr: "192.0.2.71:443",
			UserAgent:  "midnight-environment-update-integration",
		})
		if loginErr != nil {
			t.Fatalf("login %s: %v", email, loginErr)
		}
		return result
	}
	aliceFirst := login("alice-env-update@example.com")
	aliceSecond := login("alice-env-update@example.com")
	viewer := login("viewer-env-update@example.com")
	admin := login("admin-env-update@example.com")
	editor := login("editor-env-update@example.com")
	sessionUser := login("session-env-update@example.com")
	grantUser := login("grant-env-update@example.com")
	auditUser := login("audit-env-update@example.com")
	raceUser := login("race-env-update@example.com")

	patchEnvironment := func(token, key, environmentID, body string) tenantEnvironmentUpdateHTTPResponse {
		response := doRouterRequest(
			handler,
			http.MethodPatch,
			v1alphaPrefix+"/environments/"+environmentID,
			body,
			map[string]string{
				"Authorization":   "Bearer " + token,
				"Idempotency-Key": key,
			},
		)
		return tenantEnvironmentUpdateHTTPResponse{
			Code:     response.Code,
			Body:     response.Body.String(),
			Replayed: response.Header().Get("Idempotent-Replayed"),
		}
	}
	decodeEnvironment := func(response tenantEnvironmentUpdateHTTPResponse) Environment {
		t.Helper()
		var environment Environment
		if decodeErr := json.Unmarshal([]byte(response.Body), &environment); decodeErr != nil {
			t.Fatalf("decode environment: %v body=%s", decodeErr, response.Body)
		}
		return environment
	}

	editorUpdated := patchEnvironment(
		editor.AccessToken,
		"environment-update-editor",
		"env_update_main",
		`{"expected_version":1,"name":"Editor updated","tier":"staging"}`,
	)
	if editorUpdated.Code != http.StatusOK || editorUpdated.Replayed != "" {
		t.Fatalf("editor update=%#v", editorUpdated)
	}
	editorEnvironment := decodeEnvironment(editorUpdated)
	if editorEnvironment.ID != "env_update_main" ||
		editorEnvironment.ProjectID != "prj_env_update_a" ||
		editorEnvironment.Slug != "main" ||
		editorEnvironment.Name != "Editor updated" ||
		editorEnvironment.Tier != "staging" ||
		editorEnvironment.IsProtected ||
		editorEnvironment.ResourceVersion != 2 {
		t.Fatalf("editor environment=%#v", editorEnvironment)
	}

	viewerDenied := patchEnvironment(
		viewer.AccessToken,
		"environment-update-viewer",
		"env_update_target_a",
		`{"expected_version":1,"name":"Viewer denied"}`,
	)
	crossTenantDenied := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-cross",
		"env_update_other",
		`{"expected_version":1,"name":"Cross denied"}`,
	)
	restrictedDenied := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-restricted-member",
		"env_update_restricted",
		`{"expected_version":1,"name":"Restricted denied"}`,
	)
	if viewerDenied.Code != http.StatusForbidden ||
		crossTenantDenied.Code != http.StatusNotFound ||
		restrictedDenied.Code != http.StatusNotFound ||
		strings.Contains(crossTenantDenied.Body, "prj_env_update_b") {
		t.Fatalf(
			"denials viewer=%#v cross=%#v restricted=%#v",
			viewerDenied,
			crossTenantDenied,
			restrictedDenied,
		)
	}
	restrictedAdmin := patchEnvironment(
		admin.AccessToken,
		"environment-update-restricted-admin",
		"env_update_restricted",
		`{"expected_version":1,"name":"Restricted admin"}`,
	)
	if restrictedAdmin.Code != http.StatusOK {
		t.Fatalf("restricted admin=%#v", restrictedAdmin)
	}
	if got := decodeEnvironment(restrictedAdmin); got.Name != "Restricted admin" || got.ResourceVersion != 2 {
		t.Fatalf("restricted admin environment=%#v", got)
	}

	protectedUpdated := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-protected",
		"env_update_protected",
		`{"expected_version":1,"name":"Protected renamed","tier":"staging"}`,
	)
	if protectedUpdated.Code != http.StatusOK {
		t.Fatalf("protected metadata update=%#v", protectedUpdated)
	}
	protectedEnvironment := decodeEnvironment(protectedUpdated)
	if !protectedEnvironment.IsProtected ||
		protectedEnvironment.ProjectID != "prj_env_update_a" ||
		protectedEnvironment.ResourceVersion != 2 {
		t.Fatalf("protected state changed=%#v", protectedEnvironment)
	}

	replayBody := `{"expected_version":1,"slug":"replay-renamed","name":"Replay renamed"}`
	first := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-shared",
		"env_update_replay",
		replayBody,
	)
	replay := patchEnvironment(
		aliceSecond.AccessToken,
		"environment-update-shared",
		"env_update_replay",
		replayBody,
	)
	if first.Code != http.StatusOK ||
		first.Replayed != "" ||
		replay.Code != first.Code ||
		replay.Body != first.Body ||
		replay.Replayed != "true" {
		t.Fatalf("first=%#v replay=%#v", first, replay)
	}
	if got := decodeEnvironment(first); got.ResourceVersion != 2 || got.Slug != "replay-renamed" {
		t.Fatalf("replayed environment=%#v", got)
	}
	differentBody := patchEnvironment(
		aliceSecond.AccessToken,
		"environment-update-shared",
		"env_update_replay",
		`{"expected_version":1,"name":"Different body"}`,
	)
	if differentBody.Code != http.StatusConflict ||
		differentBody.Replayed != "" ||
		!strings.Contains(differentBody.Body, string(idempotencyFailureCodeDifferentBody)) {
		t.Fatalf("same key different body=%#v", differentBody)
	}

	targetBody := `{"expected_version":1,"name":"Target bound"}`
	targetA := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-target-bound",
		"env_update_target_a",
		targetBody,
	)
	targetB := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-target-bound",
		"env_update_target_b",
		targetBody,
	)
	if targetA.Code != http.StatusOK ||
		targetB.Code != http.StatusOK ||
		targetA.Replayed != "" ||
		targetB.Replayed != "" ||
		decodeEnvironment(targetA).ID != "env_update_target_a" ||
		decodeEnvironment(targetB).ID != "env_update_target_b" {
		t.Fatalf("target-bound A=%#v B=%#v", targetA, targetB)
	}

	sessionBody := `{"expected_version":1,"name":"Session updated"}`
	sessionUpdated := patchEnvironment(
		sessionUser.AccessToken,
		"environment-update-session",
		"env_update_session",
		sessionBody,
	)
	if sessionUpdated.Code != http.StatusOK {
		t.Fatalf("session update=%#v", sessionUpdated)
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
		"environment-update-replay-test",
	); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	staleSessionReplay := patchEnvironment(
		sessionUser.AccessToken,
		"environment-update-session",
		"env_update_session",
		sessionBody,
	)
	if staleSessionReplay.Code != http.StatusUnauthorized || staleSessionReplay.Replayed != "" {
		t.Fatalf("stale session replay=%#v", staleSessionReplay)
	}

	grantBody := `{"expected_version":1,"name":"Grant updated"}`
	grantUpdated := patchEnvironment(
		grantUser.AccessToken,
		"environment-update-grant",
		"env_update_grant",
		grantBody,
	)
	if grantUpdated.Code != http.StatusOK {
		t.Fatalf("grant update=%#v", grantUpdated)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		if _, lockErr := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`,
			"workspace-members:wrk_env_update_a",
		); lockErr != nil {
			return lockErr
		}
		if _, lockErr := tx.Exec(
			ctx,
			`SELECT pg_advisory_xact_lock(hashtext($1))`,
			"project-members:prj_env_update_a",
		); lockErr != nil {
			return lockErr
		}
		_, deleteErr := tx.Exec(ctx, `
			DELETE FROM project_members
			WHERE project_id='prj_env_update_a'
			  AND user_id='usr_env_update_grant'`)
		return deleteErr
	}); err != nil {
		t.Fatalf("remove project grant: %v", err)
	}
	staleGrantReplay := patchEnvironment(
		grantUser.AccessToken,
		"environment-update-grant",
		"env_update_grant",
		grantBody,
	)
	if staleGrantReplay.Code != http.StatusNotFound ||
		staleGrantReplay.Replayed != "" ||
		strings.Contains(staleGrantReplay.Body, "Grant updated") {
		t.Fatalf("stale grant replay=%#v", staleGrantReplay)
	}

	casBody := `{"expected_version":2,"name":"Stale CAS"}`
	casConflict := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-cas",
		"env_update_cas",
		casBody,
	)
	casReplay := patchEnvironment(
		aliceSecond.AccessToken,
		"environment-update-cas",
		"env_update_cas",
		casBody,
	)
	if casConflict.Code != http.StatusConflict ||
		!strings.Contains(casConflict.Body, `"conflict":"version"`) ||
		casConflict.Replayed != "" ||
		casReplay.Code != casConflict.Code ||
		casReplay.Body != casConflict.Body ||
		casReplay.Replayed != "true" {
		t.Fatalf("cas conflict=%#v replay=%#v", casConflict, casReplay)
	}

	slugBody := `{"expected_version":1,"slug":"slug-taken"}`
	slugConflict := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-slug",
		"env_update_slug_source",
		slugBody,
	)
	slugReplay := patchEnvironment(
		aliceSecond.AccessToken,
		"environment-update-slug",
		"env_update_slug_source",
		slugBody,
	)
	if slugConflict.Code != http.StatusConflict ||
		!strings.Contains(slugConflict.Body, `"conflict":"slug"`) ||
		slugConflict.Replayed != "" ||
		slugReplay.Code != slugConflict.Code ||
		slugReplay.Body != slugConflict.Body ||
		slugReplay.Replayed != "true" {
		t.Fatalf("slug conflict=%#v replay=%#v", slugConflict, slugReplay)
	}
	deletedSlugConflict := patchEnvironment(
		aliceFirst.AccessToken,
		"environment-update-deleted-slug",
		"env_update_slug_source",
		`{"expected_version":1,"slug":"retired"}`,
	)
	if deletedSlugConflict.Code != http.StatusConflict ||
		!strings.Contains(deletedSlugConflict.Body, `"conflict":"slug"`) {
		t.Fatalf("deleted slug conflict=%#v", deletedSlugConflict)
	}

	concurrentBody := `{"expected_version":1,"name":"Concurrent updated"}`
	concurrentStart := make(chan struct{})
	concurrentResults := make(chan tenantEnvironmentUpdateHTTPResponse, 2)
	var concurrentWait sync.WaitGroup
	for range 2 {
		concurrentWait.Add(1)
		go func() {
			defer concurrentWait.Done()
			<-concurrentStart
			concurrentResults <- patchEnvironment(
				aliceFirst.AccessToken,
				"environment-update-concurrent",
				"env_update_concurrent",
				concurrentBody,
			)
		}()
	}
	close(concurrentStart)
	concurrentWait.Wait()
	close(concurrentResults)
	var concurrentResponses []tenantEnvironmentUpdateHTTPResponse
	for response := range concurrentResults {
		concurrentResponses = append(concurrentResponses, response)
	}
	if len(concurrentResponses) != 2 ||
		concurrentResponses[0].Code != http.StatusOK ||
		concurrentResponses[1].Code != http.StatusOK ||
		concurrentResponses[0].Body != concurrentResponses[1].Body {
		t.Fatalf("concurrent same key=%#v", concurrentResponses)
	}
	replayedCount := 0
	for _, response := range concurrentResponses {
		if response.Replayed == "true" {
			replayedCount++
		}
	}
	if replayedCount != 1 {
		t.Fatalf("concurrent replay count=%d responses=%#v", replayedCount, concurrentResponses)
	}

	distinctStart := make(chan struct{})
	distinctResults := make(chan tenantEnvironmentUpdateHTTPResponse, 2)
	var distinctWait sync.WaitGroup
	for index, name := range []string{"Distinct first", "Distinct second"} {
		distinctWait.Add(1)
		go func(index int, name string) {
			defer distinctWait.Done()
			<-distinctStart
			distinctResults <- patchEnvironment(
				aliceFirst.AccessToken,
				"environment-update-distinct-"+string(rune('a'+index)),
				"env_update_concurrent_cas",
				`{"expected_version":1,"name":"`+name+`"}`,
			)
		}(index, name)
	}
	close(distinctStart)
	distinctWait.Wait()
	close(distinctResults)
	var distinctOK, distinctConflict int
	for response := range distinctResults {
		switch response.Code {
		case http.StatusOK:
			distinctOK++
		case http.StatusConflict:
			distinctConflict++
			if !strings.Contains(response.Body, `"conflict":"version"`) {
				t.Fatalf("distinct conflict body=%s", response.Body)
			}
		default:
			t.Fatalf("distinct response=%#v", response)
		}
	}
	if distinctOK != 1 || distinctConflict != 1 {
		t.Fatalf("distinct results success=%d conflict=%d", distinctOK, distinctConflict)
	}

	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_environment_update_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action='environment.update'
			   AND NEW.actor_id='usr_env_update_audit' THEN
				RAISE EXCEPTION 'forced environment update audit failure';
			END IF;
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_environment_update_audit
			BEFORE INSERT ON audit_logs
			FOR EACH ROW EXECUTE FUNCTION fail_environment_update_audit()`); err != nil {
		t.Fatalf("install audit failure trigger: %v", err)
	}
	auditFailure := patchEnvironment(
		auditUser.AccessToken,
		"environment-update-audit-failure",
		"env_update_audit",
		`{"expected_version":1,"name":"Must roll back"}`,
	)
	if auditFailure.Code != http.StatusServiceUnavailable || auditFailure.Replayed != "" {
		t.Fatalf("audit failure=%#v", auditFailure)
	}
	var auditName string
	var auditVersion int64
	var auditReservations, auditRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT name FROM environments WHERE id='env_update_audit'),
			(SELECT resource_version FROM environments WHERE id='env_update_audit'),
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='environment-update-audit-failure'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.update'
			   AND actor_id='usr_env_update_audit')`,
	).Scan(&auditName, &auditVersion, &auditReservations, &auditRows); err != nil {
		t.Fatal(err)
	}
	if auditName != "Audit" || auditVersion != 1 || auditReservations != 0 || auditRows != 0 {
		t.Fatalf(
			"audit rollback name=%q version=%d reservations=%d audits=%d",
			auditName,
			auditVersion,
			auditReservations,
			auditRows,
		)
	}

	compatibilityTx, err := pool.Inner().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compatibilityTx.Exec(ctx, `
		UPDATE environments
		SET slug='raced-slug'
		WHERE id='env_update_race_winner'`); err != nil {
		_ = compatibilityTx.Rollback(ctx)
		t.Fatal(err)
	}
	raceResult := make(chan tenantEnvironmentUpdateHTTPResponse, 1)
	go func() {
		raceResult <- patchEnvironment(
			raceUser.AccessToken,
			"environment-update-slug-race",
			"env_update_race_target",
			`{"expected_version":1,"slug":"raced-slug"}`,
		)
	}()
	select {
	case early := <-raceResult:
		_ = compatibilityTx.Rollback(ctx)
		t.Fatalf("environment update did not wait on compatibility slug writer: %#v", early)
	case <-time.After(100 * time.Millisecond):
	}
	if err := compatibilityTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	racedConflict := <-raceResult
	if racedConflict.Code != http.StatusConflict ||
		!strings.Contains(racedConflict.Body, `"conflict":"slug"`) ||
		racedConflict.Replayed != "" {
		t.Fatalf("raced slug conflict=%#v", racedConflict)
	}

	var (
		mainVersion               int64
		protectedState            bool
		protectedVersion          int64
		concurrentVersion         int64
		concurrentReservations    int
		concurrentAudits          int
		distinctVersion           int64
		distinctReservations      int
		distinctAudits            int
		raceTargetSlug            string
		raceTargetVersion         int64
		raceReservations          int
		raceConflictAudits        int
		authorizationReservations int
		authorizationAudits       int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT resource_version FROM environments WHERE id='env_update_main'),
			(SELECT is_protected FROM environments WHERE id='env_update_protected'),
			(SELECT resource_version FROM environments WHERE id='env_update_protected'),
			(SELECT resource_version FROM environments WHERE id='env_update_concurrent'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-update-concurrent' AND state='SUCCEEDED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.update'
			   AND resource_id='env_update_concurrent'
			   AND policy_decision='allowed'),
			(SELECT resource_version FROM environments WHERE id='env_update_concurrent_cas'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN ('environment-update-distinct-a','environment-update-distinct-b')),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.update'
			   AND resource_id='env_update_concurrent_cas'),
			(SELECT slug FROM environments WHERE id='env_update_race_target'),
			(SELECT resource_version FROM environments WHERE id='env_update_race_target'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key='environment-update-slug-race' AND state='FAILED'),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.update'
			   AND resource_id='env_update_race_target'
			   AND policy_decision='denied'
			   AND reason_code='RESOURCE_CONFLICT'),
			(SELECT COUNT(*) FROM idempotency_keys
			 WHERE key IN (
			   'environment-update-viewer',
			   'environment-update-cross',
			   'environment-update-restricted-member'
			 )),
			(SELECT COUNT(*) FROM audit_logs
			 WHERE action='environment.update'
			   AND (
			     (actor_id='usr_env_update_viewer'
			      AND resource_id='env_update_target_a')
			     OR
			     (actor_id='usr_env_update_alice'
			      AND resource_id IN ('env_update_other','env_update_restricted'))
			   ))`,
	).Scan(
		&mainVersion,
		&protectedState,
		&protectedVersion,
		&concurrentVersion,
		&concurrentReservations,
		&concurrentAudits,
		&distinctVersion,
		&distinctReservations,
		&distinctAudits,
		&raceTargetSlug,
		&raceTargetVersion,
		&raceReservations,
		&raceConflictAudits,
		&authorizationReservations,
		&authorizationAudits,
	); err != nil {
		t.Fatal(err)
	}
	if mainVersion != 2 ||
		!protectedState ||
		protectedVersion != 2 ||
		concurrentVersion != 2 ||
		concurrentReservations != 1 ||
		concurrentAudits != 1 ||
		distinctVersion != 2 ||
		distinctReservations != 2 ||
		distinctAudits != 2 ||
		raceTargetSlug != "race-target" ||
		raceTargetVersion != 1 ||
		raceReservations != 1 ||
		raceConflictAudits != 1 ||
		authorizationReservations != 0 ||
		authorizationAudits != 0 {
		t.Fatalf(
			"proof main=%d protected=%t/%d concurrent=%d/%d/%d distinct=%d/%d/%d race=%s/%d/%d/%d auth=%d/%d",
			mainVersion,
			protectedState,
			protectedVersion,
			concurrentVersion,
			concurrentReservations,
			concurrentAudits,
			distinctVersion,
			distinctReservations,
			distinctAudits,
			raceTargetSlug,
			raceTargetVersion,
			raceReservations,
			raceConflictAudits,
			authorizationReservations,
			authorizationAudits,
		)
	}

	var allowedBefore, allowedAfter map[string]any
	if err := pool.QueryRow(ctx, `
		SELECT before, after
		FROM audit_logs
		WHERE action='environment.update'
		  AND resource_id='env_update_main'
		  AND policy_decision='allowed'`,
	).Scan(&allowedBefore, &allowedAfter); err != nil {
		t.Fatalf("read audit before/after: %v", err)
	}
	if allowedBefore["name"] != "Main" ||
		allowedBefore["resource_version"] != float64(1) ||
		allowedAfter["name"] != "Editor updated" ||
		allowedAfter["resource_version"] != float64(2) {
		t.Fatalf("audit before=%#v after=%#v", allowedBefore, allowedAfter)
	}

	resourceStore, ok := stack.EnvironmentStore.(*PostgresResourceStore)
	if !ok {
		t.Fatalf("environment store=%T", stack.EnvironmentStore)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		_, updateErr := resourceStore.UpdateEnvironmentMetadataInTx(
			ctx,
			tx,
			Environment{
				ID:        "env_update_protected",
				ProjectID: "prj_env_update_a",
				Slug:      "protected",
				Name:      "Stale direct update",
				Tier:      "staging",
			},
			1,
		)
		if !errors.Is(updateErr, ErrResourceConflict) {
			return errors.New("direct stale CAS did not return ErrResourceConflict")
		}
		var transactionUsable int
		if queryErr := tx.QueryRow(ctx, `SELECT 1`).Scan(&transactionUsable); queryErr != nil {
			return queryErr
		}
		if transactionUsable != 1 {
			return errors.New("direct stale CAS left an unusable transaction")
		}
		return nil
	}); err != nil {
		t.Fatalf("direct stale CAS seam: %v", err)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		updated, updateErr := resourceStore.UpdateEnvironmentMetadataInTx(
			ctx,
			tx,
			Environment{
				ID:          "env_update_protected",
				ProjectID:   "prj_env_update_a",
				Slug:        "protected-direct",
				Name:        "Protected direct",
				Tier:        "development",
				IsProtected: false,
			},
			2,
		)
		if updateErr != nil {
			return updateErr
		}
		if updated.ProjectID != "prj_env_update_a" ||
			!updated.IsProtected ||
			updated.ResourceVersion != 3 {
			return errors.New("direct metadata seam changed project/protection or returned the wrong version")
		}
		return nil
	}); err != nil {
		t.Fatalf("direct metadata seam: %v", err)
	}
	directEnvironment, err := resourceStore.GetEnvironment(ctx, "env_update_protected")
	if err != nil {
		t.Fatalf("get direct environment: %v", err)
	}
	if directEnvironment.ProjectID != "prj_env_update_a" ||
		!directEnvironment.IsProtected ||
		directEnvironment.Slug != "protected-direct" ||
		directEnvironment.ResourceVersion != 3 {
		t.Fatalf("direct environment=%#v", directEnvironment)
	}

	coordinator, ok := stack.TenantEnvironmentUpdates.(*PostgresTenantMutationCoordinator)
	if !ok {
		t.Fatalf("tenant environment update coordinator=%T", stack.TenantEnvironmentUpdates)
	}
	_, err = coordinator.UpdateEnvironment(ctx, TenantEnvironmentUpdateRequest{})
	if !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("invalid coordinator tuple error=%v", err)
	}
}

type tenantEnvironmentUpdateHTTPResponse struct {
	Code     int
	Body     string
	Replayed string
}
