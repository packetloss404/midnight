//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresAutomationTokenRouterLifecycleAndAdmission(t *testing.T) {
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

	now := time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)
	password := "automation token correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, display_name, password_hash, session_generation,
			created_at, updated_at
		) VALUES (
			'usr_ci_admin', 'ci-admin@example.com', 'CI Admin', $1, 1, $2, $2
		)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatalf("seed automation token user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspaces (
			id, slug, display_name, created_at, updated_at
		) VALUES (
			'wrk_ci', 'ci-workspace', 'CI Workspace', $1, $1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (
			workspace_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES (
			'wrk_ci', 'usr_ci_admin', 'admin', $1, $1, $1, $1
		)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token membership: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (
			id, workspace_id, slug, display_name, created_at, updated_at
		) VALUES
			('prj_ci', 'wrk_ci', 'ci-project', 'CI Project', $1, $1),
			('prj_other', 'wrk_ci', 'other-project', 'Other Project', $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token projects: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO project_members (
			project_id, user_id, role, invited_at, accepted_at,
			created_at, updated_at
		) VALUES
			('prj_ci', 'usr_ci_admin', 'owner', $1, $1, $1, $1),
			('prj_other', 'usr_ci_admin', 'owner', $1, $1, $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token project roles: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO environments (
			id, project_id, slug, name, tier, created_at, updated_at
		) VALUES
			('env_ci', 'prj_ci', 'production', 'Production', 'production', $1, $1),
			('env_other', 'prj_other', 'production', 'Production', 'production', $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token environments: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, project_id, slug, display_name, created_at, updated_at
		) VALUES
			('svc_ci', 'prj_ci', 'api', 'API', $1, $1),
			('svc_other', 'prj_other', 'other-api', 'Other API', $1, $1)`,
		now,
	); err != nil {
		t.Fatalf("seed automation token services: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	idempotencyStore, ok := stack.IdempotencyStore.(*idempotency.PostgresStore)
	if !ok {
		t.Fatalf("IdempotencyStore = %T, want *idempotency.PostgresStore", stack.IdempotencyStore)
	}
	auditChain, ok := stack.AuditChain.(*audit.PostgresChain)
	if !ok {
		t.Fatalf("AuditChain = %T, want *audit.PostgresChain", stack.AuditChain)
	}
	tokenStore := automationtoken.NewPostgresStore(pool)
	tokenManager, err := automationtoken.NewManager(automationtoken.ManagerConfig{
		Store:  tokenStore,
		MACKey: []byte("automation-token-router-integration-mac-key-v1"),
		Clock:  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	tokenMutations := NewPostgresAutomationTokenMutationCoordinator(
		pool,
		stack.IdentityAccess,
		tokenManager,
		tokenStore,
		idempotencyStore,
		auditChain,
		func() time.Time { return now },
	)
	stack.AutomationTokens = tokenManager
	stack.AutomationTokenCatalog = tokenManager
	stack.AutomationTokenMutations = tokenMutations
	handler := NewRouterWithDataStack(stack)

	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
		Email:      "ci-admin@example.com",
		Password:   password,
		ClientKind: "api",
		RemoteAddr: "192.0.2.20:443",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	humanAuthorization := "Bearer " + login.AccessToken
	expiresAt := now.Add(24 * time.Hour).Format(time.RFC3339)
	mintBody := `{"name":"release deploy","expires_at":"` + expiresAt + `"}`
	mintPath := v1alphaPrefix + "/projects/prj_ci/automation-tokens"
	firstMint := doRouterRequest(
		handler,
		http.MethodPost,
		mintPath,
		mintBody,
		map[string]string{
			"Authorization":   humanAuthorization,
			"Idempotency-Key": "mint-ci-release",
		},
	)
	if firstMint.Code != http.StatusCreated ||
		firstMint.Header().Get("Cache-Control") != "no-store" ||
		firstMint.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf(
			"first mint status=%d cache=%q replay=%q body=%s",
			firstMint.Code,
			firstMint.Header().Get("Cache-Control"),
			firstMint.Header().Get("Idempotent-Replayed"),
			firstMint.Body.String(),
		)
	}
	firstMintBytes := append([]byte(nil), firstMint.Body.Bytes()...)
	var minted automationtoken.MintResult
	if err := json.Unmarshal(firstMintBytes, &minted); err != nil {
		t.Fatalf("decode mint: %v body=%s", err, firstMintBytes)
	}
	if !strings.HasPrefix(minted.AccessToken, automationtoken.TokenPrefix) ||
		minted.Token.ID == "" ||
		minted.Token.ProjectID != "prj_ci" ||
		minted.Token.WorkspaceID != "wrk_ci" ||
		minted.Token.Profile != automationtoken.ProfileCIDeployV1 {
		t.Fatalf("mint result = %#v", minted)
	}

	var (
		tokenHash      []byte
		storedPrefix   string
		responseStatus bool
		responseBody   bool
		responseHeader bool
		responseStore  string
		responseWrap   []byte
		responseCipher []byte
		createAudits   int
		createAuditRaw string
	)
	if err := pool.QueryRow(ctx, `
		SELECT token_hash, token_prefix
		FROM project_automation_tokens
		WHERE id=$1`,
		minted.Token.ID,
	).Scan(&tokenHash, &storedPrefix); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT response_status IS NULL,
		       response_body IS NULL,
		       response_headers IS NULL,
		       response_storage,
		       response_wrapped_dek,
		       response_ciphertext
		FROM idempotency_keys
		WHERE key='mint-ci-release' AND state='SUCCEEDED'`,
	).Scan(
		&responseStatus,
		&responseBody,
		&responseHeader,
		&responseStore,
		&responseWrap,
		&responseCipher,
	); err != nil {
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
		WHERE action='automation_token.create'
		  AND actor_id='usr_ci_admin'
		  AND workspace_id='wrk_ci'
		  AND project_id='prj_ci'
		  AND policy_decision='allowed'`,
	).Scan(&createAudits, &createAuditRaw); err != nil {
		t.Fatal(err)
	}
	if len(tokenHash) != 32 ||
		storedPrefix != minted.Token.TokenPrefix ||
		bytes.Contains(tokenHash, []byte(minted.AccessToken)) ||
		!responseStatus || !responseBody || !responseHeader ||
		responseStore != "envelope-v1" ||
		len(responseWrap) == 0 || len(responseCipher) == 0 ||
		bytes.Contains(responseWrap, []byte(minted.AccessToken)) ||
		bytes.Contains(responseCipher, []byte(minted.AccessToken)) ||
		createAudits != 1 ||
		strings.Contains(createAuditRaw, minted.AccessToken) {
		t.Fatalf(
			"unsafe durable mint hash=%d prefix=%q nulls=%t/%t/%t storage=%q wrap=%d cipher=%d audits=%d",
			len(tokenHash),
			storedPrefix,
			responseStatus,
			responseBody,
			responseHeader,
			responseStore,
			len(responseWrap),
			len(responseCipher),
			createAudits,
		)
	}

	replayedMint := doRouterRequest(
		handler,
		http.MethodPost,
		mintPath,
		mintBody,
		map[string]string{
			"Authorization":   humanAuthorization,
			"Idempotency-Key": "mint-ci-release",
		},
	)
	if replayedMint.Code != firstMint.Code ||
		!bytes.Equal(replayedMint.Body.Bytes(), firstMintBytes) ||
		replayedMint.Header().Get("Location") != firstMint.Header().Get("Location") ||
		replayedMint.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"mint replay status=%d location=%q replay=%q body=%s; first status=%d location=%q body=%s",
			replayedMint.Code,
			replayedMint.Header().Get("Location"),
			replayedMint.Header().Get("Idempotent-Replayed"),
			replayedMint.Body.String(),
			firstMint.Code,
			firstMint.Header().Get("Location"),
			firstMint.Body.String(),
		)
	}
	var tokenCount, createAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM project_automation_tokens
			  WHERE project_id='prj_ci'),
			(SELECT COUNT(*) FROM audit_logs
			  WHERE action='automation_token.create'
			    AND project_id='prj_ci')`,
	).Scan(&tokenCount, &createAuditCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 1 || createAuditCount != 1 {
		t.Fatalf("replay minted tokens=%d create audits=%d, want 1/1", tokenCount, createAuditCount)
	}

	listed := doRouterRequest(
		handler,
		http.MethodGet,
		mintPath,
		"",
		map[string]string{"Authorization": humanAuthorization},
	)
	if listed.Code != http.StatusOK ||
		listed.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(listed.Body.String(), minted.AccessToken) ||
		strings.Contains(listed.Body.String(), `"access_token"`) {
		t.Fatalf("metadata list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var catalog automationTokenListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode token list: %v body=%s", err, listed.Body.String())
	}
	if len(catalog.Items) != 1 ||
		catalog.Items[0].ID != minted.Token.ID ||
		catalog.Items[0].TokenPrefix != minted.Token.TokenPrefix {
		t.Fatalf("token list = %#v", catalog)
	}

	automationAuthorization := "Bearer " + minted.AccessToken
	deployBody := `{"environment_id":"env_ci","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	deployed := doRouterRequest(
		handler,
		http.MethodPost,
		v1alphaPrefix+"/services/svc_ci/deployments",
		deployBody,
		map[string]string{
			"Authorization":   automationAuthorization,
			"Idempotency-Key": "ci-deploy-1",
		},
	)
	if deployed.Code != http.StatusAccepted {
		t.Fatalf("scoped CI deploy status=%d body=%s", deployed.Code, deployed.Body.String())
	}
	var deployOperation orailtypes.Operation
	if err := json.Unmarshal(deployed.Body.Bytes(), &deployOperation); err != nil {
		t.Fatalf("decode deploy operation: %v body=%s", err, deployed.Body.String())
	}
	if deployOperation.ID == "" {
		t.Fatalf("deploy operation = %#v", deployOperation)
	}
	var deploymentCount, outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM deployments
			  WHERE project_id='prj_ci'
			    AND service_id='svc_ci'
			    AND environment_id='env_ci'),
			(SELECT COUNT(*) FROM deploy_intake_outbox)`,
	).Scan(&deploymentCount, &outboxCount); err != nil {
		t.Fatal(err)
	}
	if deploymentCount != 1 || outboxCount != 1 {
		t.Fatalf("durable CI deploy deployments=%d outbox=%d, want 1/1", deploymentCount, outboxCount)
	}
	operationRead := doRouterRequest(
		handler,
		http.MethodGet,
		v1alphaPrefix+"/operations/"+string(deployOperation.ID),
		"",
		map[string]string{"Authorization": automationAuthorization},
	)
	if operationRead.Code != http.StatusOK ||
		!strings.Contains(operationRead.Body.String(), string(deployOperation.ID)) {
		t.Fatalf(
			"linked operation read status=%d body=%s",
			operationRead.Code,
			operationRead.Body.String(),
		)
	}

	crossProject := doRouterRequest(
		handler,
		http.MethodGet,
		v1alphaPrefix+"/projects/prj_other",
		"",
		map[string]string{"Authorization": automationAuthorization},
	)
	if crossProject.Code != http.StatusNotFound ||
		strings.Contains(crossProject.Body.String(), minted.AccessToken) {
		t.Fatalf("cross-project read status=%d body=%s", crossProject.Code, crossProject.Body.String())
	}
	forbidden := doRouterRequest(
		handler,
		http.MethodDelete,
		v1alphaPrefix+"/services/svc_ci",
		"",
		map[string]string{
			"Authorization":   automationAuthorization,
			"Idempotency-Key": "forbidden-service-delete",
		},
	)
	if forbidden.Code != http.StatusForbidden ||
		strings.Contains(forbidden.Body.String(), minted.AccessToken) {
		t.Fatalf("forbidden route status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}

	// Authorization is re-evaluated before an encrypted replay is released.
	// Removing both authority paths must conceal the original one-time bearer,
	// even though the caller presents the exact mint key and body.
	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		SET role='viewer', updated_at=$1, resource_version=resource_version+1
		WHERE workspace_id='wrk_ci' AND user_id='usr_ci_admin'`,
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE project_members
		SET role='viewer', updated_at=$1, resource_version=resource_version+1
		WHERE project_id='prj_ci' AND user_id='usr_ci_admin'`,
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	deniedReplay := doRouterRequest(
		handler,
		http.MethodPost,
		mintPath,
		mintBody,
		map[string]string{
			"Authorization":   humanAuthorization,
			"Idempotency-Key": "mint-ci-release",
		},
	)
	if deniedReplay.Code != http.StatusForbidden ||
		deniedReplay.Header().Get("Idempotent-Replayed") != "" ||
		strings.Contains(deniedReplay.Body.String(), minted.AccessToken) {
		t.Fatalf(
			"grant-removed replay status=%d replay=%q body=%s",
			deniedReplay.Code,
			deniedReplay.Header().Get("Idempotent-Replayed"),
			deniedReplay.Body.String(),
		)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE workspace_members
		SET role='admin', updated_at=$1, resource_version=resource_version+1
		WHERE workspace_id='wrk_ci' AND user_id='usr_ci_admin'`,
		now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE project_members
		SET role='owner', updated_at=$1, resource_version=resource_version+1
		WHERE project_id='prj_ci' AND user_id='usr_ci_admin'`,
		now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}

	revoke := doRouterRequest(
		handler,
		http.MethodDelete,
		mintPath+"/"+minted.Token.ID,
		"",
		map[string]string{
			"Authorization":   humanAuthorization,
			"Idempotency-Key": "revoke-ci-release",
		},
	)
	if revoke.Code != http.StatusNoContent ||
		revoke.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	revokeReplay := doRouterRequest(
		handler,
		http.MethodDelete,
		mintPath+"/"+minted.Token.ID,
		"",
		map[string]string{
			"Authorization":   humanAuthorization,
			"Idempotency-Key": "revoke-ci-release",
		},
	)
	if revokeReplay.Code != revoke.Code ||
		revokeReplay.Body.String() != revoke.Body.String() ||
		revokeReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf(
			"revoke replay status=%d replay=%q body=%q; first status=%d body=%q",
			revokeReplay.Code,
			revokeReplay.Header().Get("Idempotent-Replayed"),
			revokeReplay.Body.String(),
			revoke.Code,
			revoke.Body.String(),
		)
	}
	rejectedAfterRevoke := doRouterRequest(
		handler,
		http.MethodGet,
		v1alphaPrefix+"/projects/prj_ci",
		"",
		map[string]string{"Authorization": automationAuthorization},
	)
	if rejectedAfterRevoke.Code != http.StatusUnauthorized ||
		strings.Contains(rejectedAfterRevoke.Body.String(), minted.AccessToken) {
		t.Fatalf(
			"post-revoke request status=%d body=%s",
			rejectedAfterRevoke.Code,
			rejectedAfterRevoke.Body.String(),
		)
	}

	var (
		revokedAt      *time.Time
		revokedBy      string
		revokedReason  string
		revokeAudits   int
		revokeAuditRaw string
		forbiddenRows  int
	)
	if err := pool.QueryRow(ctx, `
		SELECT revoked_at, revoked_by_user_id, revoked_reason
		FROM project_automation_tokens
		WHERE id=$1`,
		minted.Token.ID,
	).Scan(&revokedAt, &revokedBy, &revokedReason); err != nil {
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
		WHERE action='automation_token.revoke'
		  AND resource_id=$1
		  AND actor_id='usr_ci_admin'
		  AND workspace_id='wrk_ci'
		  AND project_id='prj_ci'
		  AND policy_decision='allowed'`,
		minted.Token.ID,
	).Scan(&revokeAudits, &revokeAuditRaw); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM idempotency_keys
			  WHERE key='forbidden-service-delete') +
			(SELECT COUNT(*) FROM operations
			  WHERE idempotency_key='forbidden-service-delete')`,
	).Scan(&forbiddenRows); err != nil {
		t.Fatal(err)
	}
	if revokedAt == nil ||
		revokedBy != "usr_ci_admin" ||
		revokedReason != "user-requested" ||
		revokeAudits != 1 ||
		strings.Contains(revokeAuditRaw, minted.AccessToken) ||
		forbiddenRows != 0 {
		t.Fatalf(
			"revocation/evidence revoked=%v by=%q reason=%q audits=%d forbidden_rows=%d",
			revokedAt,
			revokedBy,
			revokedReason,
			revokeAudits,
			forbiddenRows,
		)
	}

	report, err := audit.Verify(
		ctx,
		audit.SourceFromChain{Chain: auditChain},
		nil,
	)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if report.Outcome != audit.OutcomeOK || report.RowsChecked < 2 {
		t.Fatalf("audit verification = %#v", report)
	}
}
