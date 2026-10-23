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

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresIdentityInvitationRouterLifecycle(t *testing.T) {
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

	now := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	password := "invitation correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES
			('usr_inv_admin', 'admin@example.com', 'Admin', $1, 1, $2, $2),
			('usr_inv_join', 'join@example.com', 'Join', $1, 1, $2, $2),
			('usr_inv_revoke', 'revoke@example.com', 'Revoke', $1, 1, $2, $2),
			('usr_inv_race', 'race@example.com', 'Race', $1, 1, $2, $2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (
			id, slug, display_name, created_at, updated_at
		) VALUES
			('wrk_inv', 'invite-workspace', 'Invite Workspace', $1, $1),
			('wrk_inv_other', 'invite-other', 'Invite Other', $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspaces: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('wrk_inv', 'usr_inv_admin', 'admin', $1, $1, $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed workspace membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_inv', 'wrk_inv', 'invite-project', 'Invite Project', $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	adminLogin, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "admin@example.com", Password: password, ClientKind: "api",
	})
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	joinLogin, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "join@example.com", Password: password, ClientKind: "api",
	})
	if err != nil {
		t.Fatalf("join login: %v", err)
	}
	revokeLogin, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "revoke@example.com", Password: password, ClientKind: "api",
	})
	if err != nil {
		t.Fatalf("revoke login: %v", err)
	}
	raceLogin, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email: "race@example.com", Password: password, ClientKind: "api",
	})
	if err != nil {
		t.Fatalf("race login: %v", err)
	}
	adminAuthorization := "Bearer " + adminLogin.AccessToken
	joinAuthorization := "Bearer " + joinLogin.AccessToken
	revokeAuthorization := "Bearer " + revokeLogin.AccessToken
	raceAuthorization := "Bearer " + raceLogin.AccessToken

	createBody := `{"email":"join@example.com","workspace_id":"wrk_inv","project_id":"prj_inv","project_role":"editor","expires_at":"` +
		now.Add(24*time.Hour).Format(time.RFC3339) + `"}`
	createPath := v1alphaPrefix + invitationRoute
	create := doRouterRequest(handler, http.MethodPost, createPath, createBody, map[string]string{
		"Authorization": adminAuthorization, "Idempotency-Key": "invite-project-create",
	})
	if create.Code != http.StatusCreated ||
		create.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	var first identityInvitationCredentialResult
	if err := json.Unmarshal(create.Body.Bytes(), &first); err != nil ||
		!strings.HasPrefix(first.InvitationToken, identity.InvitationTokenPrefix) ||
		first.Invitation.ProjectID != "prj_inv" {
		t.Fatalf("create result=%#v err=%v", first, err)
	}
	createReplay := doRouterRequest(handler, http.MethodPost, createPath, createBody, map[string]string{
		"Authorization": adminAuthorization, "Idempotency-Key": "invite-project-create",
	})
	if createReplay.Code != create.Code ||
		createReplay.Body.String() != create.Body.String() ||
		createReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("create replay status=%d replay=%q body=%s", createReplay.Code, createReplay.Header().Get("Idempotent-Replayed"), createReplay.Body.String())
	}
	createConflict := doRouterRequest(
		handler,
		http.MethodPost,
		createPath,
		strings.Replace(createBody, "editor", "viewer", 1),
		map[string]string{
			"Authorization": adminAuthorization, "Idempotency-Key": "invite-project-create",
		},
	)
	if createConflict.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status=%d body=%s", createConflict.Code, createConflict.Body.String())
	}

	list := doRouterRequest(
		handler,
		http.MethodGet,
		createPath+"?workspace_id=wrk_inv&project_id=prj_inv",
		"",
		map[string]string{"Authorization": adminAuthorization},
	)
	if list.Code != http.StatusOK ||
		!strings.Contains(list.Body.String(), first.Invitation.ID) ||
		strings.Contains(list.Body.String(), first.InvitationToken) ||
		strings.Contains(list.Body.String(), "invitation_token") {
		t.Fatalf("metadata list status=%d body=%s", list.Code, list.Body.String())
	}

	reissueBody := `{"workspace_id":"wrk_inv","project_id":"prj_inv","expires_at":"` +
		now.Add(48*time.Hour).Format(time.RFC3339) + `"}`
	reissue := doRouterRequest(
		handler,
		http.MethodPost,
		createPath+"/"+first.Invitation.ID+"/reissue",
		reissueBody,
		map[string]string{
			"Authorization": adminAuthorization, "Idempotency-Key": "invite-project-reissue",
		},
	)
	if reissue.Code != http.StatusCreated {
		t.Fatalf("reissue status=%d body=%s", reissue.Code, reissue.Body.String())
	}
	var replacement identityInvitationCredentialResult
	if err := json.Unmarshal(reissue.Body.Bytes(), &replacement); err != nil ||
		replacement.Invitation.ReplacesInvitationID != first.Invitation.ID ||
		replacement.InvitationToken == first.InvitationToken {
		t.Fatalf("replacement=%#v err=%v", replacement, err)
	}

	oldAccept := doRouterRequest(
		handler,
		http.MethodPost,
		createPath+"/accept",
		`{"invitation_token":"`+first.InvitationToken+`"}`,
		map[string]string{
			"Authorization": joinAuthorization, "Idempotency-Key": "invite-old-accept",
		},
	)
	if oldAccept.Code != http.StatusConflict ||
		strings.Contains(oldAccept.Body.String(), first.InvitationToken) {
		t.Fatalf("old accept status=%d body=%s", oldAccept.Code, oldAccept.Body.String())
	}
	acceptBody := `{"invitation_token":"` + replacement.InvitationToken + `"}`
	accept := doRouterRequest(
		handler,
		http.MethodPost,
		createPath+"/accept",
		acceptBody,
		map[string]string{
			"Authorization": joinAuthorization, "Idempotency-Key": "invite-project-accept",
		},
	)
	if accept.Code != http.StatusOK ||
		strings.Contains(accept.Body.String(), replacement.InvitationToken) {
		t.Fatalf("accept status=%d body=%s", accept.Code, accept.Body.String())
	}
	acceptReplay := doRouterRequest(
		handler,
		http.MethodPost,
		createPath+"/accept",
		acceptBody,
		map[string]string{
			"Authorization": joinAuthorization, "Idempotency-Key": "invite-project-accept",
		},
	)
	if acceptReplay.Code != accept.Code ||
		acceptReplay.Body.String() != accept.Body.String() ||
		acceptReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("accept replay status=%d replay=%q body=%s", acceptReplay.Code, acceptReplay.Header().Get("Idempotent-Replayed"), acceptReplay.Body.String())
	}

	workspaceCreateBody := `{"email":"revoke@example.com","workspace_id":"wrk_inv","workspace_role":"viewer","expires_at":"` +
		now.Add(24*time.Hour).Format(time.RFC3339) + `"}`
	workspaceCreate := doRouterRequest(
		handler,
		http.MethodPost,
		createPath,
		workspaceCreateBody,
		map[string]string{
			"Authorization": adminAuthorization, "Idempotency-Key": "invite-workspace-create",
		},
	)
	var toRevoke identityInvitationCredentialResult
	if workspaceCreate.Code != http.StatusCreated ||
		json.Unmarshal(workspaceCreate.Body.Bytes(), &toRevoke) != nil {
		t.Fatalf("workspace create status=%d body=%s", workspaceCreate.Code, workspaceCreate.Body.String())
	}
	revoke := doRouterRequest(
		handler,
		http.MethodDelete,
		createPath+"/"+toRevoke.Invitation.ID+"?workspace_id=wrk_inv",
		"",
		map[string]string{
			"Authorization": adminAuthorization, "Idempotency-Key": "invite-workspace-revoke",
		},
	)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	revokedAccept := doRouterRequest(
		handler,
		http.MethodPost,
		createPath+"/accept",
		`{"invitation_token":"`+toRevoke.InvitationToken+`"}`,
		map[string]string{
			"Authorization": revokeAuthorization, "Idempotency-Key": "invite-revoked-accept",
		},
	)
	if revokedAccept.Code != http.StatusConflict {
		t.Fatalf("revoked accept status=%d body=%s", revokedAccept.Code, revokedAccept.Body.String())
	}

	raceCreateBody := `{"email":"race@example.com","workspace_id":"wrk_inv","project_id":"prj_inv","project_role":"viewer","expires_at":"` +
		now.Add(24*time.Hour).Format(time.RFC3339) + `"}`
	raceCreate := doRouterRequest(
		handler,
		http.MethodPost,
		createPath,
		raceCreateBody,
		map[string]string{
			"Authorization": adminAuthorization, "Idempotency-Key": "invite-race-create",
		},
	)
	var raceCredential identityInvitationCredentialResult
	if raceCreate.Code != http.StatusCreated ||
		json.Unmarshal(raceCreate.Body.Bytes(), &raceCredential) != nil {
		t.Fatalf("race create status=%d body=%s", raceCreate.Code, raceCreate.Body.String())
	}
	raceResults := make(chan int, 2)
	go func() {
		response := doRouterRequest(
			handler,
			http.MethodPost,
			createPath+"/accept",
			`{"invitation_token":"`+raceCredential.InvitationToken+`"}`,
			map[string]string{
				"Authorization": raceAuthorization, "Idempotency-Key": "invite-race-accept",
			},
		)
		raceResults <- response.Code
	}()
	go func() {
		response := doRouterRequest(
			handler,
			http.MethodPost,
			createPath+"/"+raceCredential.Invitation.ID+"/reissue",
			`{"workspace_id":"wrk_inv","project_id":"prj_inv","expires_at":"`+
				now.Add(48*time.Hour).Format(time.RFC3339)+`"}`,
			map[string]string{
				"Authorization": adminAuthorization, "Idempotency-Key": "invite-race-reissue",
			},
		)
		raceResults <- response.Code
	}()
	firstRaceStatus, secondRaceStatus := <-raceResults, <-raceResults
	successes, conflicts := 0, 0
	for _, status := range []int{firstRaceStatus, secondRaceStatus} {
		switch status {
		case http.StatusOK, http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("accept/reissue race returned status %d", status)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("accept/reissue race statuses=%d/%d", firstRaceStatus, secondRaceStatus)
	}

	var (
		projectMemberships int
		rawTokenRows       int
		actionCount        int
		auditMaterial      string
	)
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM project_members
		WHERE project_id='prj_inv'
		  AND user_id='usr_inv_join'
		  AND role='editor'
		  AND accepted_at IS NOT NULL`,
	).Scan(&projectMemberships); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM identity_invitations
		WHERE token_prefix=$1
		  AND (
		      token_prefix=$2
		      OR encode(token_hash, 'escape')=$2
		  )`,
		replacement.Invitation.TokenPrefix,
		replacement.InvitationToken,
	).Scan(&rawTokenRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(string_agg(
		           COALESCE(before::text,'') ||
		           COALESCE(after::text,'') ||
		           COALESCE(attrs::text,''),
		           ''
		       ), '')
		FROM audit_logs
		WHERE action = ANY($1)
		  AND actor_type='user'`,
		[]string{
			"identity.invitation.create",
			"identity.invitation.reissue",
			"identity.invitation.accept",
			"identity.invitation.revoke",
		},
	).Scan(&actionCount, &auditMaterial); err != nil {
		t.Fatal(err)
	}
	if projectMemberships != 1 ||
		rawTokenRows != 0 ||
		actionCount != 7 ||
		strings.Contains(auditMaterial, first.InvitationToken) ||
		strings.Contains(auditMaterial, replacement.InvitationToken) ||
		strings.Contains(auditMaterial, toRevoke.InvitationToken) ||
		strings.Contains(auditMaterial, raceCredential.InvitationToken) {
		t.Fatalf("evidence memberships=%d raw_rows=%d actions=%d", projectMemberships, rawTokenRows, actionCount)
	}

	stalePrincipal, staleSession, err := stack.IdentitySessions.Authenticate(ctx, adminAuthorization)
	if err != nil {
		t.Fatalf("authenticate session for freshness proof: %v", err)
	}
	if err := stack.IdentitySessions.Revoke(ctx, stalePrincipal, staleSession.ID, "freshness-proof"); err != nil {
		t.Fatalf("revoke session for freshness proof: %v", err)
	}
	coordinator, ok := stack.IdentityInvitationMutations.(*PostgresIdentityInvitationMutationCoordinator)
	if !ok {
		t.Fatalf("IdentityInvitationMutations = %T", stack.IdentityInvitationMutations)
	}
	_, err = coordinator.CreateIdentityInvitation(ctx, IdentityInvitationCreateMutationRequest{
		identityInvitationMutationContext: identityInvitationMutationContext{
			Principal: stalePrincipal,
			Session:   staleSession,
			Key:       "invite-stale-session",
			Fingerprint: idempotency.RequestFingerprint{
				Method:     http.MethodPost,
				Path:       v1alphaPrefix + invitationRoute,
				BodySHA256: idempotency.BodyHashSHA256([]byte("stale-session-proof")),
			},
		},
		Email:         "never-created@example.com",
		WorkspaceID:   "wrk_inv",
		WorkspaceRole: identity.WorkspaceRoleViewer,
		ExpiresAt:     now.Add(24 * time.Hour),
	})
	if !errors.Is(err, identity.ErrSessionInvalid) {
		t.Fatalf("stale-session mutation error=%v, want %v", err, identity.ErrSessionInvalid)
	}
	var staleRows int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM identity_invitations WHERE email='never-created@example.com') +
			(SELECT COUNT(*) FROM idempotency_keys WHERE key='invite-stale-session') +
			(SELECT COUNT(*) FROM audit_logs WHERE resource_kind='identity_invitation' AND after::text LIKE '%never-created@example.com%')`,
	).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 {
		t.Fatalf("stale session left %d mutation artifacts", staleRows)
	}

	auditChain, ok := stack.AuditChain.(*audit.PostgresChain)
	if !ok {
		t.Fatalf("AuditChain = %T", stack.AuditChain)
	}
	report, err := audit.Verify(ctx, audit.SourceFromChain{Chain: auditChain}, nil)
	if err != nil || report.Outcome != audit.OutcomeOK {
		t.Fatalf("audit verification=%#v err=%v", report, err)
	}
}
