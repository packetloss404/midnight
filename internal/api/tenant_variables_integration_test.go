//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/migrations"
)

func TestTenantVariableLifecycleAuthorizationReplayEncryptionAndHistory(t *testing.T) {
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

	now := time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC)
	password := "tenant variable correct horse battery staple"
	passwordHash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users (
			id,email,display_name,password_hash,session_generation,created_at,updated_at
		) VALUES
			('usr_var_member','member-var@example.com','Member',$1,1,$2,$2),
			('usr_var_viewer','viewer-var@example.com','Viewer',$1,1,$2,$2),
			('usr_var_admin','admin-var@example.com','Admin',$1,1,$2,$2),
			('usr_var_revoke','revoke-var@example.com','Revoke',$1,1,$2,$2)`,
		passwordHash,
		now,
	); err != nil {
		t.Fatal(err)
	}
	tenantSeed := []string{
		`INSERT INTO workspaces (id,slug,display_name,created_at,updated_at)
		 VALUES ('wrk_var_a','variables-a','Variables A',$1,$1),
		        ('wrk_var_b','variables-b','Variables B',$1,$1)`,
		`INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at)
		 VALUES ('prj_var_a','wrk_var_a','api','API',$1,$1),
		        ('prj_var_b','wrk_var_b','api','API',$1,$1)`,
		`INSERT INTO environments (
			id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at
		 ) VALUES
			('env_var_main','prj_var_a','main','Main','development',FALSE,FALSE,$1,$1),
			('env_var_restricted','prj_var_a','restricted','Restricted','production',TRUE,TRUE,$1,$1),
			('env_var_b','prj_var_b','main','Main','development',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
		 VALUES ('svc_var_api','prj_var_a','api','API',$1,$1),
		        ('svc_var_restricted','prj_var_a','restricted','Restricted',$1,$1),
		        ('svc_var_revoke','prj_var_a','revoke','Revoke',$1,$1),
		        ('svc_var_b','prj_var_b','api','API',$1,$1)`,
		`INSERT INTO workspace_members (
			workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at
		 ) VALUES
			('wrk_var_a','usr_var_member','member',$1,$1,$1,$1),
			('wrk_var_a','usr_var_viewer','viewer',$1,$1,$1,$1),
			('wrk_var_a','usr_var_admin','admin',$1,$1,$1,$1),
			('wrk_var_a','usr_var_revoke','member',$1,$1,$1,$1)`,
	}
	for _, statement := range tenantSeed {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed tenant graph: %v", err)
		}
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	if stack.TenantVariables == nil {
		t.Fatal("tenant variable coordinator is not installed")
	}
	handler := NewRouterWithDataStack(stack)
	login := func(email string) identity.LoginResult {
		t.Helper()
		result, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{
			Email: email, Password: password, ClientKind: "api",
			RemoteAddr: "192.0.2.90:443", UserAgent: "tenant-variable-integration",
		})
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		return result
	}
	memberFirst := login("member-var@example.com")
	memberSecond := login("member-var@example.com")
	viewer := login("viewer-var@example.com")
	admin := login("admin-var@example.com")
	revoke := login("revoke-var@example.com")

	request := func(token, method, path, key, body string) tenantVariableHTTPResponse {
		t.Helper()
		headers := map[string]string{"Authorization": "Bearer " + token}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		response := doRouterRequest(handler, method, path, body, integrationVersionHeaders(body, headers))
		return tenantVariableHTTPResponse{
			Code: response.Code, Body: response.Body.String(),
			Replayed: response.Header().Get("Idempotent-Replayed"),
			Location: response.Header().Get("Location"),
		}
	}
	collection := tenantVariableCollectionPath("svc_var_api", "env_var_main")
	plaintext := "guessable-tenant-variable-secret"
	createBody := `{"name":"API_TOKEN","scope":"secret","value":"` + plaintext + `"}`
	created := request(memberFirst.AccessToken, http.MethodPost, collection, "variable-create-key", createBody)
	if created.Code != http.StatusCreated || created.Replayed != "" ||
		strings.Contains(created.Body, plaintext) || created.Location == "" {
		t.Fatalf("create=%#v", created)
	}
	var meta secrets.VariableMeta
	if err := json.Unmarshal([]byte(created.Body), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Workspace != "wrk_var_a" || meta.Project != "prj_var_a" ||
		meta.Environment != "env_var_main" || meta.Service != "svc_var_api" ||
		meta.Name != "API_TOKEN" || meta.Version != 1 {
		t.Fatalf("created metadata=%#v", meta)
	}

	listed := request(memberSecond.AccessToken, http.MethodGet, collection, "", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body, string(meta.ID)) ||
		strings.Contains(listed.Body, plaintext) {
		t.Fatalf("member list=%#v", listed)
	}
	viewerReadDenied := request(viewer.AccessToken, http.MethodGet, collection, "", "")
	if viewerReadDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer read=%#v", viewerReadDenied)
	}
	viewerDenied := request(
		viewer.AccessToken,
		http.MethodPost,
		collection,
		"viewer-variable-create",
		`{"name":"DENIED","scope":"secret","value":"must-not-write"}`,
	)
	if viewerDenied.Code != http.StatusForbidden {
		t.Fatalf("viewer mutation=%#v", viewerDenied)
	}
	foreign := request(
		memberFirst.AccessToken,
		http.MethodGet,
		tenantVariableCollectionPath("svc_var_b", "env_var_b"),
		"",
		"",
	)
	if foreign.Code != http.StatusNotFound ||
		strings.Contains(foreign.Body, "prj_var_b") ||
		strings.Contains(foreign.Body, "wrk_var_b") {
		t.Fatalf("foreign read=%#v", foreign)
	}
	restrictedMember := request(
		memberFirst.AccessToken,
		http.MethodGet,
		tenantVariableCollectionPath("svc_var_restricted", "env_var_restricted"),
		"",
		"",
	)
	restrictedAdmin := request(
		admin.AccessToken,
		http.MethodPost,
		tenantVariableCollectionPath("svc_var_restricted", "env_var_restricted"),
		"restricted-admin-variable",
		`{"name":"ADMIN_ONLY","scope":"sealed","value":"admin-secret"}`,
	)
	if restrictedMember.Code != http.StatusNotFound || restrictedAdmin.Code != http.StatusCreated {
		t.Fatalf("restricted member=%#v admin=%#v", restrictedMember, restrictedAdmin)
	}

	replay := request(memberSecond.AccessToken, http.MethodPost, collection, "variable-create-key", createBody)
	if replay.Code != created.Code || replay.Body != created.Body || replay.Location != created.Location ||
		replay.Replayed != "true" {
		t.Fatalf("replay=%#v created=%#v", replay, created)
	}
	changedBody := request(
		memberSecond.AccessToken,
		http.MethodPost,
		collection,
		"variable-create-key",
		`{"name":"API_TOKEN","scope":"secret","value":"different"}`,
	)
	if changedBody.Code != http.StatusConflict || changedBody.Replayed != "" {
		t.Fatalf("changed body=%#v", changedBody)
	}

	item := tenantVariableItemPath("svc_var_api", "env_var_main", meta.ID)
	updated := request(
		memberFirst.AccessToken,
		http.MethodPatch,
		item,
		"variable-update-key",
		`{"expected_version":1,"scope":"sealed","value":"rotated-secret"}`,
	)
	if updated.Code != http.StatusOK || strings.Contains(updated.Body, "rotated-secret") {
		t.Fatalf("update=%#v", updated)
	}
	var updatedMeta secrets.VariableMeta
	if err := json.Unmarshal([]byte(updated.Body), &updatedMeta); err != nil {
		t.Fatal(err)
	}
	if updatedMeta.ID != meta.ID || updatedMeta.Version != 2 || updatedMeta.Scope != secrets.ScopeSealed {
		t.Fatalf("updated metadata=%#v", updatedMeta)
	}
	stale := request(
		memberFirst.AccessToken,
		http.MethodPatch,
		item,
		"variable-stale-key",
		`{"expected_version":1,"value":"must-not-win"}`,
	)
	staleReplay := request(
		memberSecond.AccessToken,
		http.MethodPatch,
		item,
		"variable-stale-key",
		`{"expected_version":1,"value":"must-not-win"}`,
	)
	if stale.Code != http.StatusPreconditionFailed || staleReplay.Code != stale.Code ||
		staleReplay.Body != stale.Body || staleReplay.Replayed != "true" {
		t.Fatalf("stale=%#v replay=%#v", stale, staleReplay)
	}
	downgrade := request(
		memberFirst.AccessToken,
		http.MethodPatch,
		item,
		"variable-downgrade-key",
		`{"expected_version":2,"scope":"plain","value":"unsafe"}`,
	)
	downgradeReplay := request(
		memberSecond.AccessToken,
		http.MethodPatch,
		item,
		"variable-downgrade-key",
		`{"expected_version":2,"scope":"plain","value":"unsafe"}`,
	)
	if downgrade.Code != http.StatusBadRequest ||
		downgradeReplay.Code != downgrade.Code ||
		downgradeReplay.Body != downgrade.Body ||
		downgradeReplay.Replayed != "true" {
		t.Fatalf("sealed downgrade=%#v replay=%#v", downgrade, downgradeReplay)
	}

	revokeCollection := tenantVariableCollectionPath("svc_var_revoke", "env_var_main")
	revokeBody := `{"name":"REVOKE_ME","scope":"secret","value":"revoke-secret"}`
	revokeCreated := request(revoke.AccessToken, http.MethodPost, revokeCollection, "variable-revoke-key", revokeBody)
	if revokeCreated.Code != http.StatusCreated {
		t.Fatalf("revoke seed=%#v", revokeCreated)
	}
	revokePrincipal, _, err := stack.IdentitySessions.Authenticate(ctx, "Bearer "+revoke.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.IdentitySessions.Revoke(ctx, revokePrincipal, revoke.Session.ID, "tenant-variable-replay-test"); err != nil {
		t.Fatal(err)
	}
	revokedReplay := request(revoke.AccessToken, http.MethodPost, revokeCollection, "variable-revoke-key", revokeBody)
	if revokedReplay.Code != http.StatusUnauthorized || revokedReplay.Replayed != "" {
		t.Fatalf("revoked replay=%#v", revokedReplay)
	}

	deleted := request(
		memberFirst.AccessToken,
		http.MethodDelete,
		item,
		"variable-delete-key",
		`{"expected_version":2}`,
	)
	deleteReplay := request(
		memberSecond.AccessToken,
		http.MethodDelete,
		item,
		"variable-delete-key",
		`{"expected_version":2}`,
	)
	if deleted.Code != http.StatusNoContent || deleteReplay.Code != http.StatusNoContent ||
		deleteReplay.Replayed != "true" {
		t.Fatalf("delete=%#v replay=%#v", deleted, deleteReplay)
	}
	afterDelete := request(memberFirst.AccessToken, http.MethodGet, item, "", "")
	if afterDelete.Code != http.StatusNotFound {
		t.Fatalf("deleted item GET=%#v", afterDelete)
	}

	rawSHA := sha256.Sum256([]byte(createBody))
	rawSHAHex := hex.EncodeToString(rawSHA[:])
	var (
		activeCiphertext []byte
		activeDeleted    bool
		historyCount     int
		storedBodyMAC    string
		plaintextMatches int
		auditCount       int
	)
	if err := pool.QueryRow(ctx, `
		SELECT value_ciphertext, deleted_at IS NOT NULL
		FROM service_variables WHERE id=$1`,
		meta.ID,
	).Scan(&activeCiphertext, &activeDeleted); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM service_variable_versions WHERE variable_id=$1`,
		meta.ID,
	).Scan(&historyCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT body_hash FROM idempotency_keys
		WHERE key='variable-create-key' AND state='SUCCEEDED'`,
	).Scan(&storedBodyMAC); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM service_variables
			  WHERE encode(value_ciphertext,'escape') LIKE '%' || $1 || '%') +
			(SELECT COUNT(*) FROM service_variable_versions
			  WHERE encode(value_ciphertext,'escape') LIKE '%' || $1 || '%') +
			(SELECT COUNT(*) FROM audit_logs
			  WHERE before::text LIKE '%' || $1 || '%'
			     OR after::text LIKE '%' || $1 || '%'
			     OR attrs::text LIKE '%' || $1 || '%')`,
		plaintext,
	).Scan(&plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM audit_logs
		WHERE resource_kind='variable' AND resource_id=$1`,
		meta.ID,
	).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if !activeDeleted || len(activeCiphertext) != 0 || historyCount != 2 ||
		storedBodyMAC == rawSHAHex || len(storedBodyMAC) != 64 ||
		plaintextMatches != 0 || auditCount < 3 {
		t.Fatalf(
			"storage evidence deleted=%v cipher=%d history=%d mac=%q raw=%q plaintext=%d audit=%d",
			activeDeleted, len(activeCiphertext), historyCount, storedBodyMAC, rawSHAHex,
			plaintextMatches, auditCount,
		)
	}
}

type tenantVariableHTTPResponse struct {
	Code     int
	Body     string
	Replayed string
	Location string
}
