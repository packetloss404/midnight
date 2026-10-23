//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/migrations"
)

func TestEnvironmentApplyAndDeployBuildImpactRoutesRuntimeReplayAndPinnedRebuild(t *testing.T) {
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
	now := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	password := "build-impact-integration-password"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation,created_at,updated_at) VALUES ('usr_build_impact','build-impact@example.com','Build Impact',$1,1,$2,$2)`, hash, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES ('wrk_build_impact','build-impact','Build Impact',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_build_impact','wrk_build_impact','app','App',$1,$1)`,
		`INSERT INTO environments(id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at) VALUES ('env_build_impact','prj_build_impact','production','Production','production',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_build_impact','prj_build_impact','api','API',$1,$1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES ('wrk_build_impact','usr_build_impact','member',$1,$1,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed identity/resource: %v", err)
		}
	}
	const sourceSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,state,actor,expires_at,created_at,updated_at) VALUES ('op_build_impact_source','build','SUCCEEDED','system',$2,$1,$1)`, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deployments(id,project_id,service_id,environment_id,action,source_type,repo,ref,sha,root_directory,dockerfile_path,builder,image_ref,image_digest,state,active,operation_id,trigger_key,created_at,updated_at) VALUES ('dep_build_impact_source','prj_build_impact','svc_build_impact','env_build_impact','deploy_source','git','https://example.com/midnight/api.git','main',$1,'','Dockerfile','auto','registry.example/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','queued',FALSE,'op_build_impact_source','build-impact-source',$2,$2)`, sourceSHA, now); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"building", "deploying", "active"} {
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id='dep_build_impact_source'`, state, state == "active", now); err != nil {
			t.Fatalf("promote source %s: %v", state, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_environment_state(service_id,environment_id,active_deployment_id,last_deployment_seq,version,updated_at) VALUES ('svc_build_impact','env_build_impact','dep_build_impact_source',1,1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	configs := serviceconfig.NewStore(pool)
	configs.Now = func() time.Time { return now }
	base, err := configs.Put(ctx, serviceconfig.Default("prj_build_impact", "svc_build_impact", "env_build_impact"), 0)
	if err != nil {
		t.Fatalf("seed service config: %v", err)
	}
	if _, err := configs.ResolveDeployment(ctx, "dep_build_impact_source", "prj_build_impact", "env_build_impact", "svc_build_impact"); err != nil {
		t.Fatalf("seed immutable source snapshot: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "build-impact@example.com", Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	auth := "Bearer " + login.AccessToken

	runtimeOnly := base
	runtimeOnly.StartCommand = "./api serve"
	runtimeSpec := environmentRuntimeSpecFromConfig(runtimeOnly)
	runtimeDraftBody := environmentRuntimeDraftBody(t, base.Version, runtimeSpec)
	runtimeDraft := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_build_impact/changeset", runtimeDraftBody, map[string]string{"Authorization": auth, "Idempotency-Key": "build-impact-runtime-draft", "If-Match": `"0"`})
	if runtimeDraft.Code != http.StatusOK {
		t.Fatalf("runtime-only draft status=%d body=%s", runtimeDraft.Code, runtimeDraft.Body.String())
	}
	var runtimeView EnvironmentChangesetView
	if err := json.Unmarshal(runtimeDraft.Body.Bytes(), &runtimeView); err != nil {
		t.Fatal(err)
	}
	if len(runtimeView.Validation.AffectedResources) != 1 || runtimeView.Validation.AffectedResources[0].DeploymentAction != environmentDeploymentActionRedeploy {
		t.Fatalf("runtime-only impact=%#v", runtimeView.Validation.AffectedResources)
	}
	runtimeApply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_build_impact/changeset/apply-and-deploy", `{"expected_revision":1}`, map[string]string{"Authorization": auth, "Idempotency-Key": "build-impact-runtime-apply", "If-Match": `"1"`})
	var runtimeAccepted EnvironmentApplyAndDeployResponse
	if runtimeApply.Code != http.StatusAccepted || json.Unmarshal(runtimeApply.Body.Bytes(), &runtimeAccepted) != nil || len(runtimeAccepted.OperationGroup.Items) != 1 {
		t.Fatalf("runtime-only apply status=%d response=%#v body=%s", runtimeApply.Code, runtimeAccepted, runtimeApply.Body.String())
	}
	runtimeItem := runtimeAccepted.OperationGroup.Items[0]
	if runtimeItem.DeploymentAction != environmentDeploymentActionRedeploy || runtimeItem.IntakeState != "queued" {
		t.Fatalf("runtime-only operation item=%#v", runtimeItem)
	}
	var runtimeAction, runtimeImageDigest string
	if err := pool.QueryRow(ctx, `SELECT action,image_digest FROM deployments WHERE id=$1`, runtimeItem.DeploymentID).Scan(&runtimeAction, &runtimeImageDigest); err != nil {
		t.Fatal(err)
	}
	if runtimeAction != "redeploy_image" || runtimeImageDigest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("runtime-only deployment action=%q image=%q", runtimeAction, runtimeImageDigest)
	}

	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, "dep_build_impact_source", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"deploying", "active"} {
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id=$4`, state, state == "active", now.Add(time.Minute), runtimeItem.DeploymentID); err != nil {
			t.Fatalf("promote runtime-only replay %s: %v", state, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE service_environment_state SET active_deployment_id=$1,version=version+1,updated_at=$2 WHERE service_id='svc_build_impact' AND environment_id='env_build_impact'`, runtimeItem.DeploymentID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A manual rebuild remains available from a valid canonical GitHub link
	// even when webhook auto-deploy is intentionally disabled.
	for _, statement := range []string{
		`INSERT INTO github_workspace_installation_grants(workspace_id,installation_id,account_login,target_type,granted_by_actor,created_at,updated_at) VALUES ('wrk_build_impact',42,'acme','Organization','operator',$1,$1)`,
		`INSERT INTO github_workspace_repository_grants(workspace_id,installation_id,repository_id,repository_full_name,clone_url,default_branch,repository_private,repository_access_verified_at,granted_by_actor,created_at,updated_at) VALUES ('wrk_build_impact',42,4242,'acme/api','https://github.com/acme/api.git','main',TRUE,$1,'operator',$1,$1)`,
		`INSERT INTO service_github_links(service_id,project_id,workspace_id,installation_id,repository_id,repository_full_name,branch,auto_deploy,repository_access_verified_at,created_at,updated_at) VALUES ('svc_build_impact','prj_build_impact','wrk_build_impact',42,4242,'acme/api','main',FALSE,$1,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now.Add(time.Minute)); err != nil {
			t.Fatalf("seed manual GitHub source authority: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET source_type='github',repo='https://github.com/acme/api.git',ref='main',sha=$2 WHERE id=$1`, runtimeItem.DeploymentID, sourceSHA); err != nil {
		t.Fatalf("bind active release to canonical GitHub source: %v", err)
	}
	current, err := configs.Get(ctx, "prj_build_impact", "env_build_impact", "svc_build_impact")
	if err != nil || current.Version != 2 {
		t.Fatalf("runtime config version=%d err=%v", current.Version, err)
	}

	buildChanged := current
	buildChanged.RootDirectory = "apps/api"
	buildChanged.Builder = serviceconfig.BuilderRailpack
	buildChanged.BuildCommand = "go build ./cmd/api"
	buildSpec := environmentRuntimeSpecFromConfig(buildChanged)
	buildDraftBody := environmentRuntimeDraftBody(t, current.Version, buildSpec)
	buildDraft := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_build_impact/changeset", buildDraftBody, map[string]string{"Authorization": auth, "Idempotency-Key": "build-impact-source-draft", "If-Match": `"0"`})
	var buildView EnvironmentChangesetView
	if buildDraft.Code != http.StatusOK || json.Unmarshal(buildDraft.Body.Bytes(), &buildView) != nil || len(buildView.Validation.AffectedResources) != 1 || buildView.Validation.AffectedResources[0].DeploymentAction != environmentDeploymentActionRebuild {
		t.Fatalf("build draft status=%d impact=%#v body=%s", buildDraft.Code, buildView.Validation.AffectedResources, buildDraft.Body.String())
	}
	buildApply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_build_impact/changeset/apply-and-deploy", `{"expected_revision":1}`, map[string]string{"Authorization": auth, "Idempotency-Key": "build-impact-source-apply", "If-Match": `"1"`})
	var buildAccepted EnvironmentApplyAndDeployResponse
	if buildApply.Code != http.StatusAccepted || json.Unmarshal(buildApply.Body.Bytes(), &buildAccepted) != nil || len(buildAccepted.OperationGroup.Items) != 1 {
		t.Fatalf("build apply status=%d response=%#v body=%s", buildApply.Code, buildAccepted, buildApply.Body.String())
	}
	buildItem := buildAccepted.OperationGroup.Items[0]
	if buildItem.DeploymentAction != environmentDeploymentActionRebuild || buildItem.IntakeState != "queued" {
		t.Fatalf("build operation item=%#v", buildItem)
	}
	var buildAction, buildRepo, buildRef, buildSHA, buildRoot, buildCommand, buildImageDigest, outboxQueue string
	if err := pool.QueryRow(ctx, `SELECT action,repo,ref,sha,root_directory,COALESCE(image_digest,'') FROM deployments WHERE id=$1`, buildItem.DeploymentID).Scan(&buildAction, &buildRepo, &buildRef, &buildSHA, &buildRoot, &buildImageDigest); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT queue FROM deploy_intake_outbox WHERE deployment_id=$1`, buildItem.DeploymentID).Scan(&outboxQueue); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT effective_config->>'build_command' FROM deployment_config_snapshots WHERE deployment_id=$1`, buildItem.DeploymentID).Scan(&buildCommand); err != nil {
		t.Fatal(err)
	}
	if buildAction != "redeploy_rebuild_ref" || buildRepo != "https://github.com/acme/api.git" || buildRef != sourceSHA || buildSHA != sourceSHA || buildRoot != "apps/api" || buildCommand != "go build ./cmd/api" || buildImageDigest != "" || outboxQueue != "build" {
		t.Fatalf("build intake action=%q repo=%q ref=%q sha=%q root=%q command=%q image=%q queue=%q", buildAction, buildRepo, buildRef, buildSHA, buildRoot, buildCommand, buildImageDigest, outboxQueue)
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM deploy_intake_outbox WHERE deployment_id=$1`, buildItem.DeploymentID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var buildJob deployJobPayload
	if err := json.Unmarshal(payload, &buildJob); err != nil {
		t.Fatal(err)
	}
	if buildJob.Request.Source == nil || buildJob.Request.Source.Ref != sourceSHA || buildJob.Request.Source.RootDirectory != "apps/api" || buildJob.Request.Source.TrustProfile != DeployTrustProfileGitHubManual || buildJob.Request.Source.InstallationID != 42 || buildJob.Request.Source.LinkResourceVersion != 1 {
		t.Fatalf("build payload is not pinned to canonical source/config: %s", payload)
	}
}

func environmentRuntimeDraftBody(t *testing.T, expectedVersion int64, spec EnvironmentRuntimeSpec) string {
	t.Helper()
	input := EnvironmentChangesetInput{
		Kind: "runtime", Operation: "edit", ServiceID: "svc_build_impact",
		TargetID: "svc_build_impact", Name: "service configuration",
		ExpectedVersion: &expectedVersion, Runtime: &spec,
	}
	body, err := json.Marshal(map[string]any{"expected_revision": 0, "changes": []EnvironmentChangesetInput{input}})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
