//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/migrations"
)

func TestEnvironmentChangesetRefreshAtomicApplyReplayAndRedaction(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 13, 20, 0, 0, 0, time.UTC)
	password := "changeset correct horse battery staple"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation,created_at,updated_at) VALUES ('usr_changeset','changeset@example.com','Changeset User',$1,1,$2,$2)`, hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation,created_at,updated_at) VALUES ('usr_changeset_other','changeset-other@example.com','Other User',$1,1,$2,$2)`, hash, now); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES ('wrk_changeset','changeset','Changeset',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_changeset','wrk_changeset','app','App',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_changeset_other','wrk_changeset','other','Other',$1,$1)`,
		`INSERT INTO environments(id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at) VALUES ('env_changeset','prj_changeset','main','Main','development',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_changeset','prj_changeset','api','API',$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_changeset_other','prj_changeset_other','other','Other',$1,$1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES ('wrk_changeset','usr_changeset','member',$1,$1,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	stack := postgresIdentityIntegrationStack(t, pool, now)
	if stack.EnvironmentChangesets == nil {
		t.Fatal("changeset coordinator is not installed")
	}
	handler := NewRouterWithDataStack(stack)
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "changeset@example.com", Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	otherLogin, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "changeset-other@example.com", Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, ok := stack.EnvironmentChangesets.(*PostgresTenantMutationCoordinator)
	if !ok {
		t.Fatalf("changeset coordinator type = %T", stack.EnvironmentChangesets)
	}
	coordinator.AppsDomain = "apps.midnight.test"
	keyProvider := &countingEnvironmentChangesetKeyProvider{KeyProvider: coordinator.VariableKeyProvider}
	coordinator.VariableKeyProvider = keyProvider
	var authorizationNow atomic.Int64
	authorizationNow.Store(now.UnixNano())
	coordinator.Now = func() time.Time { return time.Unix(0, authorizationNow.Load()).UTC() }
	headers := map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-put-1", "If-Match": "\"0\""}
	secret := "changeset-secret-never-persist-plain"
	secondSecret := "changeset-second-secret-never-persist-plain"
	putBody := `{"expected_revision":0,"note":"rotate runtime credential","changes":[{"kind":"variable","operation":"add","service_id":"svc_changeset","name":"API_TOKEN","scope":"sealed","value":"` + secret + `"},{"kind":"variable","operation":"add","service_id":"svc_changeset","name":"SECOND_TOKEN","scope":"secret","value":"` + secondSecret + `"}]}`
	unauthorized := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, map[string]string{"Authorization": "Bearer " + otherLogin.AccessToken, "Idempotency-Key": "changeset-unauthorized-1", "If-Match": "\"0\""})
	if unauthorized.Code != http.StatusNotFound || keyProvider.sealCalls.Load() != 0 {
		t.Fatalf("unauthorized put status=%d seal calls=%d body=%s", unauthorized.Code, keyProvider.sealCalls.Load(), unauthorized.Body.String())
	}
	keyProvider.beforeFirstWrap = func() { authorizationNow.Store(now.Add(25 * time.Hour).UnixNano()) }
	expiredDuringSeal := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-expired-during-seal", "If-Match": "\"0\""})
	if expiredDuringSeal.Code != http.StatusUnauthorized {
		t.Fatalf("session expiry during seal status=%d body=%s", expiredDuringSeal.Code, expiredDuringSeal.Body.String())
	}
	var draftsAfterExpiry int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM environment_changesets WHERE environment_id='env_changeset' AND state='draft'`).Scan(&draftsAfterExpiry); err != nil || draftsAfterExpiry != 0 {
		t.Fatalf("drafts after session expiry=%d err=%v", draftsAfterExpiry, err)
	}
	authorizationNow.Store(now.UnixNano())
	keyProvider.beforeFirstWrap = nil
	put := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, headers)
	if put.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	if strings.Contains(put.Body.String(), secret) || strings.Contains(put.Body.String(), secondSecret) || !strings.Contains(put.Body.String(), "value set") {
		t.Fatalf("put response is not redaction safe: %s", put.Body.String())
	}
	var draft EnvironmentChangesetView
	if err := json.Unmarshal(put.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Changeset == nil || draft.Changeset.Revision != 1 || !draft.Validation.Valid {
		t.Fatalf("draft=%#v", draft)
	}
	if _, err := pool.Exec(ctx, `UPDATE environment_changeset_items SET service_id='svc_changeset_other' WHERE changeset_id=$1 AND ordinal=0`, draft.Changeset.ID); err == nil {
		t.Fatal("cross-project service target bypassed the changeset item lineage constraint")
	}
	sealCalls := keyProvider.sealCalls.Load()
	putReplay := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, headers)
	if putReplay.Code != put.Code || putReplay.Body.String() != put.Body.String() || putReplay.Header().Get("Idempotent-Replayed") != "true" || keyProvider.sealCalls.Load() != sealCalls {
		t.Fatalf("put replay status=%d replay=%q seal calls=%d/%d body=%s", putReplay.Code, putReplay.Header().Get("Idempotent-Replayed"), keyProvider.sealCalls.Load(), sealCalls, putReplay.Body.String())
	}
	validate := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/validate", `{"expected_revision":1}`, map[string]string{"Authorization": "Bearer " + login.AccessToken})
	if validate.Code != http.StatusOK {
		t.Fatalf("read-only validate without idempotency key status=%d body=%s", validate.Code, validate.Body.String())
	}
	var ciphertext, wrapped []byte
	if err := pool.QueryRow(ctx, `SELECT value_ciphertext,wrapped_dek FROM environment_changeset_items WHERE changeset_id=$1 AND variable_name='SECOND_TOKEN'`, draft.Changeset.ID).Scan(&ciphertext, &wrapped); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) == 0 || len(wrapped) == 0 || bytes.Contains(ciphertext, []byte(secret)) {
		t.Fatal("draft envelope is empty or contains plaintext")
	}
	get := doRouterRequest(handler, http.MethodGet, v1alphaPrefix+"/environments/env_changeset/changeset", "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), secret) || strings.Contains(get.Body.String(), secondSecret) || !strings.Contains(get.Body.String(), draft.Changeset.ID) {
		t.Fatalf("refresh status=%d body=%s", get.Code, get.Body.String())
	}
	staleHeaders := map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-stale-1", "If-Match": "\"0\""}
	stale := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, staleHeaders)
	if stale.Code != http.StatusPreconditionFailed || !strings.Contains(stale.Body.String(), `"current_revision":1`) {
		t.Fatalf("stale CAS status=%d body=%s", stale.Code, stale.Body.String())
	}
	staleReplay := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_changeset/changeset", putBody, staleHeaders)
	if staleReplay.Code != stale.Code || staleReplay.Body.String() != stale.Body.String() {
		t.Fatalf("stale CAS replay status=%d body=%s", staleReplay.Code, staleReplay.Body.String())
	}
	applyHeaders := map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-apply-1", "If-Match": "\"1\""}
	applyBody := `{"expected_revision":1}`
	// Corrupt the second envelope so apply fails after the first item has been
	// written inside the transaction. The write and idempotency reservation must
	// roll back together, leaving the draft resumable with no partial variables.
	if _, err := pool.Exec(ctx, `UPDATE environment_changeset_items SET value_ciphertext='corrupt'::bytea WHERE changeset_id=$1 AND variable_name='SECOND_TOKEN'`, draft.Changeset.ID); err != nil {
		t.Fatal(err)
	}
	fault := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", applyBody, applyHeaders)
	if fault.Code != http.StatusServiceUnavailable {
		t.Fatalf("fault apply status=%d body=%s", fault.Code, fault.Body.String())
	}
	var partialCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE environment_id='env_changeset' AND service_id='svc_changeset' AND name IN ('API_TOKEN','SECOND_TOKEN') AND deleted_at IS NULL`).Scan(&partialCount); err != nil || partialCount != 0 {
		t.Fatalf("partial variables=%d err=%v", partialCount, err)
	}
	var draftState string
	if err := pool.QueryRow(ctx, `SELECT state FROM environment_changesets WHERE id=$1`, draft.Changeset.ID).Scan(&draftState); err != nil || draftState != "draft" {
		t.Fatalf("draft state=%q err=%v", draftState, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE environment_changeset_items SET value_ciphertext=$2 WHERE changeset_id=$1 AND variable_name='SECOND_TOKEN'`, draft.Changeset.ID, ciphertext); err != nil {
		t.Fatal(err)
	}
	// The same key can now resume because the failed transaction never completed
	// its reservation. Once successful, the exact terminal response is replayed.
	apply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", applyBody, applyHeaders)
	if apply.Code != http.StatusOK || !strings.Contains(apply.Body.String(), `"state":"applied"`) {
		t.Fatalf("apply status=%d body=%s", apply.Code, apply.Body.String())
	}
	var activeCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE environment_id='env_changeset' AND service_id='svc_changeset' AND name IN ('API_TOKEN','SECOND_TOKEN') AND deleted_at IS NULL`).Scan(&activeCount); err != nil || activeCount != 2 {
		t.Fatalf("active variables=%d err=%v", activeCount, err)
	}
	if err := pool.QueryRow(ctx, `SELECT value_ciphertext,wrapped_dek FROM environment_changeset_items WHERE changeset_id=$1 AND variable_name='SECOND_TOKEN'`, draft.Changeset.ID).Scan(&ciphertext, &wrapped); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) != 0 || len(wrapped) != 0 {
		t.Fatal("applied draft retained transient envelope")
	}
	replay := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", applyBody, applyHeaders)
	if replay.Code != http.StatusOK || replay.Body.String() != apply.Body.String() {
		t.Fatalf("apply replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	after := doRouterRequest(handler, http.MethodGet, v1alphaPrefix+"/environments/env_changeset/changeset", "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"changeset":null`) {
		t.Fatalf("post-apply get status=%d body=%s", after.Code, after.Body.String())
	}

	// Config sync is a draft producer, not a second mutation authority. Stage
	// runtime, generated-domain, and storage intent together, then prove the
	// separate changeset apply commits every canonical store in one transition.
	mixedDocument := configSyncDocument{
		APIVersion:       "midnight/v1alpha",
		EnvironmentID:    "env_changeset",
		ExpectedRevision: int64Pointer(0),
		Note:             "stage mixed desired state",
		Changes: []EnvironmentChangesetInput{
			{Kind: "variable", Operation: "add", ServiceID: "svc_changeset", Name: "MIXED_TOKEN", Scope: mixedScopePointer(secrets.ScopeSecret), Value: mixedStringPointer("mixed-secret-value")}, // not-a-failure-code
			{Kind: "runtime", Operation: "add", ServiceID: "svc_changeset", Name: "service configuration", Runtime: &EnvironmentRuntimeSpec{}},
			{Kind: "networking", Operation: "add", ServiceID: "svc_changeset", Name: "generated public domain", Networking: &EnvironmentNetworkingSpec{Kind: "generated", Public: true}},
			{Kind: "storage", Operation: "add", ServiceID: "svc_changeset", Name: "data", Storage: &EnvironmentStorageSpec{SizeBytes: 1 << 30, MountPath: "/data"}},
		},
	}
	mixedJSON, err := json.Marshal(mixedDocument)
	if err != nil {
		t.Fatal(err)
	}
	mixed := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/config/sync", string(mixedJSON), map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-sync-mixed-1", "If-Match": `"0"`})
	if mixed.Code != http.StatusOK || !strings.Contains(mixed.Body.String(), `"kind":"variable"`) || !strings.Contains(mixed.Body.String(), `"kind":"runtime"`) || !strings.Contains(mixed.Body.String(), `"kind":"networking"`) || !strings.Contains(mixed.Body.String(), `"kind":"storage"`) || strings.Contains(mixed.Body.String(), "mixed-secret-value") {
		t.Fatalf("mixed sync status=%d body=%s", mixed.Code, mixed.Body.String())
	}
	mixedApply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", `{"expected_revision":1}`, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-apply-mixed-1", "If-Match": `"1"`})
	if mixedApply.Code != http.StatusOK {
		t.Fatalf("mixed apply status=%d body=%s", mixedApply.Code, mixedApply.Body.String())
	}
	var configVersion int64
	var targetPort int
	if err := pool.QueryRow(ctx, `SELECT version,target_port FROM service_environment_configs WHERE project_id='prj_changeset' AND environment_id='env_changeset' AND service_id='svc_changeset'`).Scan(&configVersion, &targetPort); err != nil || configVersion != 1 || targetPort != serviceconfig.DefaultTargetPort {
		t.Fatalf("runtime config version=%d target_port=%d err=%v", configVersion, targetPort, err)
	}
	var volumeID string
	var volumeVersion int64
	if err := pool.QueryRow(ctx, `SELECT id,resource_version FROM volumes WHERE project_id='prj_changeset' AND environment_id='env_changeset' AND service_id='svc_changeset' AND name='data'`).Scan(&volumeID, &volumeVersion); err != nil || volumeVersion != 1 {
		t.Fatalf("volume resource_version=%d err=%v", volumeVersion, err)
	}
	var generatedDomains int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM domains WHERE project_id='prj_changeset' AND environment_id='env_changeset' AND service_id='svc_changeset' AND kind='generated'`).Scan(&generatedDomains); err != nil || generatedDomains != 1 {
		t.Fatalf("generated domains=%d err=%v", generatedDomains, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE project_id='prj_changeset' AND environment_id='env_changeset' AND service_id='svc_changeset' AND name='MIXED_TOKEN' AND deleted_at IS NULL`).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("mixed variable count=%d err=%v", activeCount, err)
	}
	var volumeGauge int64
	if err := pool.QueryRow(ctx, `SELECT quantity FROM usage_events WHERE workspace_id='wrk_changeset' AND dimension='volume_count' AND value_kind='gauge' ORDER BY sequence DESC LIMIT 1`).Scan(&volumeGauge); err != nil || volumeGauge != 1 {
		t.Fatalf("volume gauge=%d err=%v", volumeGauge, err)
	}

	// An enforceable logical-volume limit denies a positive delta before any
	// canonical row is written and commits the exact 409 response for replay.
	if _, err := pool.Exec(ctx, `UPDATE usage_policy_limits SET limit_value=1,enforceable=TRUE WHERE profile_key='self_hosted' AND dimension='volume_count'`); err != nil {
		t.Fatal(err)
	}
	limitedDocument := configSyncDocument{APIVersion: "midnight/v1alpha", EnvironmentID: "env_changeset", ExpectedRevision: int64Pointer(0), Changes: []EnvironmentChangesetInput{
		{Kind: "storage", Operation: "add", ServiceID: "svc_changeset", Name: "cache", Storage: &EnvironmentStorageSpec{SizeBytes: 1 << 30, MountPath: "/cache"}},
	}}
	limitedJSON, err := json.Marshal(limitedDocument)
	if err != nil {
		t.Fatal(err)
	}
	limited := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/config/sync", string(limitedJSON), map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-sync-limited-1", "If-Match": `"0"`})
	if limited.Code != http.StatusOK {
		t.Fatalf("limited sync status=%d body=%s", limited.Code, limited.Body.String())
	}
	limitedApplyHeaders := map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-apply-limited-1", "If-Match": `"1"`}
	limitedApply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", `{"expected_revision":1}`, limitedApplyHeaders)
	if limitedApply.Code != http.StatusConflict || !strings.Contains(limitedApply.Body.String(), "workspace volume limit exceeded") {
		t.Fatalf("limited apply status=%d body=%s", limitedApply.Code, limitedApply.Body.String())
	}
	limitedReplay := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", `{"expected_revision":1}`, limitedApplyHeaders)
	if limitedReplay.Code != limitedApply.Code || limitedReplay.Body.String() != limitedApply.Body.String() || limitedReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("limited replay status=%d replay=%q body=%s", limitedReplay.Code, limitedReplay.Header().Get("Idempotent-Replayed"), limitedReplay.Body.String())
	}
	var activeVolumes, deniedDecisions int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM volumes WHERE project_id='prj_changeset' AND status IN ('provisioning','ready','attached')`).Scan(&activeVolumes); err != nil || activeVolumes != 1 {
		t.Fatalf("volumes after denial=%d err=%v", activeVolumes, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_admission_decisions WHERE workspace_id='wrk_changeset' AND dimension='volume_count' AND decision='deny'`).Scan(&deniedDecisions); err != nil || deniedDecisions != 1 {
		t.Fatalf("volume denial decisions=%d err=%v", deniedDecisions, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workspace_members SET role='admin',updated_at=$3 WHERE workspace_id=$1 AND user_id=$2`, "wrk_changeset", "usr_changeset", now); err != nil {
		t.Fatal(err)
	}

	// Replacing the denied draft with a remove+add swap has zero net delta.
	// Removals execute first, so both admission and the table trigger allow it
	// while the final gauge remains one.
	neutralDocument := configSyncDocument{APIVersion: "midnight/v1alpha", EnvironmentID: "env_changeset", ExpectedRevision: int64Pointer(1), Changes: []EnvironmentChangesetInput{
		{Kind: "storage", Operation: "remove", ServiceID: "svc_changeset", TargetID: volumeID, Name: "data", ExpectedVersion: int64Pointer(volumeVersion)},
		{Kind: "storage", Operation: "add", ServiceID: "svc_changeset", Name: "cache", Storage: &EnvironmentStorageSpec{SizeBytes: 1 << 30, MountPath: "/cache"}},
	}}
	neutralJSON, err := json.Marshal(neutralDocument)
	if err != nil {
		t.Fatal(err)
	}
	neutral := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/config/sync", string(neutralJSON), map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-sync-neutral-1", "If-Match": `"1"`})
	if neutral.Code != http.StatusOK {
		t.Fatalf("neutral sync status=%d body=%s", neutral.Code, neutral.Body.String())
	}
	neutralApply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_changeset/changeset/apply", `{"expected_revision":2}`, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "changeset-apply-neutral-1", "If-Match": `"2"`})
	if neutralApply.Code != http.StatusOK {
		t.Fatalf("neutral apply status=%d body=%s", neutralApply.Code, neutralApply.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM volumes WHERE project_id='prj_changeset' AND status IN ('provisioning','ready','attached')`).Scan(&activeVolumes); err != nil || activeVolumes != 1 {
		t.Fatalf("volumes after neutral swap=%d err=%v", activeVolumes, err)
	}
	if err := pool.QueryRow(ctx, `SELECT quantity FROM usage_events WHERE workspace_id='wrk_changeset' AND dimension='volume_count' AND value_kind='gauge' ORDER BY sequence DESC LIMIT 1`).Scan(&volumeGauge); err != nil || volumeGauge != 1 {
		t.Fatalf("volume gauge after neutral swap=%d err=%v", volumeGauge, err)
	}
}

func int64Pointer(value int64) *int64                      { return &value }
func mixedStringPointer(value string) *string              { return &value }
func mixedScopePointer(value secrets.Scope) *secrets.Scope { return &value }

type countingEnvironmentChangesetKeyProvider struct {
	secrets.KeyProvider
	sealCalls       atomic.Int64
	wrapCallbackRan atomic.Bool
	beforeFirstWrap func()
}

func (p *countingEnvironmentChangesetKeyProvider) Wrap(ctx context.Context, dek []byte) (secrets.KEKID, []byte, error) {
	p.sealCalls.Add(1)
	if p.beforeFirstWrap != nil && p.wrapCallbackRan.CompareAndSwap(false, true) {
		p.beforeFirstWrap()
	}
	return p.KeyProvider.Wrap(ctx, dek)
}

func (p *countingEnvironmentChangesetKeyProvider) Fingerprint(ctx context.Context) (string, error) {
	p.sealCalls.Add(1)
	return p.KeyProvider.Fingerprint(ctx)
}
