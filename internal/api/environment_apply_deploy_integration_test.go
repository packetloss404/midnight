//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/serviceconfig"
	"github.com/midnight/midnight/migrations"
)

func TestEnvironmentApplyAndDeployRevisionReplaySnapshotAndPromotion(t *testing.T) {
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
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	password := "apply-deploy-integration-password"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation,created_at,updated_at) VALUES ('usr_apply_deploy','apply-deploy@example.com','Apply Deploy',$1,1,$2,$2)`, hash, now); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES ('wrk_apply_deploy','apply-deploy','Apply Deploy',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_apply_deploy','wrk_apply_deploy','app','App',$1,$1)`,
		`INSERT INTO environments(id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at) VALUES ('env_apply_deploy','prj_apply_deploy','production','Production','production',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_apply_deploy','prj_apply_deploy','api','API',$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_apply_deploy_empty','prj_apply_deploy','worker','Worker',$1,$1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES ('wrk_apply_deploy','usr_apply_deploy','member',$1,$1,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed identity/resource: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,state,actor,expires_at,created_at,updated_at) VALUES ('op_apply_deploy_source','deploy','SUCCEEDED','system',$2,$1,$1)`, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deployments(id,project_id,service_id,environment_id,action,image_ref,image_digest,state,active,operation_id,trigger_key,created_at,updated_at) VALUES ('dep_apply_deploy_source','prj_apply_deploy','svc_apply_deploy','env_apply_deploy','deploy_image','registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','queued',FALSE,'op_apply_deploy_source','source-trigger',$1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"deploying", "active"} {
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id='dep_apply_deploy_source'`, state, state == "active", now); err != nil {
			t.Fatalf("promote source %s: %v", state, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_environment_state(service_id,environment_id,active_deployment_id,last_deployment_seq,version,updated_at) VALUES ('svc_apply_deploy','env_apply_deploy','dep_apply_deploy_source',1,1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	configs := serviceconfig.NewStore(pool)
	configs.Now = func() time.Time { return now }
	if _, err := configs.ResolveDeployment(ctx, "dep_apply_deploy_source", "prj_apply_deploy", "env_apply_deploy", "svc_apply_deploy"); err != nil {
		t.Fatalf("seed immutable source snapshot: %v", err)
	}

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "apply-deploy@example.com", Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	putBody := `{"expected_revision":0,"changes":[{"kind":"variable","operation":"add","service_id":"svc_apply_deploy","name":"RELEASE_TOKEN","scope":"secret","value":"never-return-this"}]}` // not-a-failure-code
	put := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_apply_deploy/changeset", putBody, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "apply-deploy-draft", "If-Match": `"0"`})
	if put.Code != http.StatusOK || strings.Contains(put.Body.String(), "never-return-this") {
		t.Fatalf("draft status=%d body=%s", put.Code, put.Body.String())
	}
	headers := map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "apply-deploy-transition", "If-Match": `"1"`}
	apply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_apply_deploy/changeset/apply-and-deploy", `{"expected_revision":1}`, headers)
	if apply.Code != http.StatusAccepted || strings.Contains(apply.Body.String(), "never-return-this") {
		t.Fatalf("apply-and-deploy status=%d body=%s", apply.Code, apply.Body.String())
	}
	var accepted EnvironmentApplyAndDeployResponse
	if err := json.Unmarshal(apply.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.AppliedRevision != 1 || accepted.OperationGroup.State != "accepted" || len(accepted.OperationGroup.Items) != 1 || accepted.OperationGroup.Items[0].IntakeState != "queued" {
		t.Fatalf("accepted response=%#v", accepted)
	}
	item := accepted.OperationGroup.Items[0]
	groupPath := v1alphaPrefix + "/environments/env_apply_deploy/operation-groups/" + accepted.OperationGroupID
	groupRunning := doRouterRequest(handler, http.MethodGet, groupPath, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	var running EnvironmentOperationGroup
	if groupRunning.Code != http.StatusOK || json.Unmarshal(groupRunning.Body.Bytes(), &running) != nil || running.State != "running" || len(running.Items) != 1 || running.Items[0].DeploymentState != "queued" {
		t.Fatalf("running group status=%d response=%#v body=%s", groupRunning.Code, running, groupRunning.Body.String())
	}
	var appliedRevision, deployedRevision, deploymentRevision int64
	var pendingDeployment string
	if err := pool.QueryRow(ctx, `SELECT applied_revision,deployed_revision,COALESCE(pending_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`).Scan(&appliedRevision, &deployedRevision, &pendingDeployment); err != nil {
		t.Fatal(err)
	}
	if appliedRevision != 1 || deployedRevision != 0 || pendingDeployment != item.DeploymentID {
		t.Fatalf("revision state applied=%d deployed=%d pending=%q", appliedRevision, deployedRevision, pendingDeployment)
	}
	if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM deployments WHERE id=$1`, item.DeploymentID).Scan(&deploymentRevision); err != nil || deploymentRevision != 1 {
		t.Fatalf("deployment revision=%d err=%v", deploymentRevision, err)
	}
	var variableVersions []byte
	// not-a-failure-code -- fixture variable name recorded in snapshot metadata.
	if err := pool.QueryRow(ctx, `SELECT variable_versions FROM deployment_config_snapshots WHERE deployment_id=$1`, item.DeploymentID).Scan(&variableVersions); err != nil || !strings.Contains(string(variableVersions), "RELEASE_TOKEN") {
		t.Fatalf("fresh snapshot variables=%s err=%v", variableVersions, err)
	}
	replay := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_apply_deploy/changeset/apply-and-deploy", `{"expected_revision":1}`, headers)
	if replay.Code != apply.Code || replay.Body.String() != apply.Body.String() || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d replay=%q body=%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Body.String())
	}
	for _, state := range []string{"deploying", "active"} {
		if state == "active" {
			if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, "dep_apply_deploy_source", now.Add(time.Minute)); err != nil {
				t.Fatalf("supersede source: %v", err)
			}
		}
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id=$4`, state, state == "active", now.Add(time.Minute), item.DeploymentID); err != nil {
			t.Fatalf("promote grouped deployment %s: %v", state, err)
		}
	}
	for _, state := range []string{"RUNNING", "SUCCEEDED"} {
		if _, err := pool.Exec(ctx, `UPDATE operations SET state=$1,updated_at=$2 WHERE id=$3`, state, now.Add(time.Minute), item.OperationID); err != nil {
			t.Fatalf("complete grouped operation %s: %v", state, err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT deployed_revision,COALESCE(deployed_deployment_id,''),COALESCE(pending_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`).Scan(&deployedRevision, &pendingDeployment, &putBody); err != nil {
		t.Fatal(err)
	}
	if deployedRevision != 1 || pendingDeployment != item.DeploymentID || putBody != "" {
		t.Fatalf("promoted revision=%d deployment=%q pending=%q", deployedRevision, pendingDeployment, putBody)
	}
	groupSucceeded := doRouterRequest(handler, http.MethodGet, groupPath, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	var succeeded EnvironmentOperationGroup
	if groupSucceeded.Code != http.StatusOK || json.Unmarshal(groupSucceeded.Body.Bytes(), &succeeded) != nil || succeeded.State != "succeeded" || len(succeeded.Items) != 1 || succeeded.Items[0].DeploymentState != "active" {
		t.Fatalf("succeeded group status=%d response=%#v body=%s", groupSucceeded.Code, succeeded, groupSucceeded.Body.String())
	}

	partialDraftBody := `{"expected_revision":0,"changes":[{"kind":"variable","operation":"add","service_id":"svc_apply_deploy","name":"NEXT_TOKEN","scope":"secret","value":"queued-secret"},{"kind":"variable","operation":"add","service_id":"svc_apply_deploy_empty","name":"WORKER_TOKEN","scope":"secret","value":"partial-secret"}]}`
	partialDraft := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_apply_deploy/changeset", partialDraftBody, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "apply-deploy-partial-draft", "If-Match": `"0"`})
	if partialDraft.Code != http.StatusOK {
		t.Fatalf("partial draft status=%d body=%s", partialDraft.Code, partialDraft.Body.String())
	}
	partial := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_apply_deploy/changeset/apply-and-deploy", `{"expected_revision":1}`, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": "apply-deploy-partial", "If-Match": `"1"`})
	if partial.Code != http.StatusAccepted || strings.Contains(partial.Body.String(), "partial-secret") {
		t.Fatalf("partial apply status=%d body=%s", partial.Code, partial.Body.String())
	}
	var partialAccepted EnvironmentApplyAndDeployResponse
	if err := json.Unmarshal(partial.Body.Bytes(), &partialAccepted); err != nil {
		t.Fatal(err)
	}
	if partialAccepted.AppliedRevision != 2 || partialAccepted.OperationGroup.State != "running" || len(partialAccepted.OperationGroup.Items) != 2 {
		t.Fatalf("partial response=%#v", partialAccepted)
	}
	var queuedItem, failedItem EnvironmentOperationGroupItem
	for _, candidate := range partialAccepted.OperationGroup.Items {
		switch candidate.IntakeState {
		case "queued":
			queuedItem = candidate
		case "failed":
			failedItem = candidate
		}
	}
	if queuedItem.ServiceID != "svc_apply_deploy" || failedItem.ServiceID != "svc_apply_deploy_empty" {
		t.Fatalf("partial intake items=%#v", partialAccepted.OperationGroup.Items)
	}
	partialPath := v1alphaPrefix + "/environments/env_apply_deploy/operation-groups/" + partialAccepted.OperationGroupID
	partialRunningResponse := doRouterRequest(handler, http.MethodGet, partialPath, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	var partialRunning EnvironmentOperationGroup
	if partialRunningResponse.Code != http.StatusOK || json.Unmarshal(partialRunningResponse.Body.Bytes(), &partialRunning) != nil || partialRunning.State != "running" {
		t.Fatalf("mixed intake must remain running status=%d response=%#v body=%s", partialRunningResponse.Code, partialRunning, partialRunningResponse.Body.String())
	}
	var appliedVariables int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM service_variables WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy_empty' AND name='WORKER_TOKEN' AND deleted_at IS NULL`).Scan(&appliedVariables); err != nil || appliedVariables != 1 {
		t.Fatalf("partial applied variable count=%d err=%v", appliedVariables, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, item.DeploymentID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("supersede first grouped deployment: %v", err)
	}
	for _, state := range []string{"deploying", "active"} {
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id=$4`, state, state == "active", now.Add(2*time.Minute), queuedItem.DeploymentID); err != nil {
			t.Fatalf("promote mixed-intake sibling %s: %v", state, err)
		}
	}
	for _, state := range []string{"RUNNING", "SUCCEEDED"} {
		if _, err := pool.Exec(ctx, `UPDATE operations SET state=$1,updated_at=$2 WHERE id=$3`, state, now.Add(2*time.Minute), queuedItem.OperationID); err != nil {
			t.Fatalf("complete mixed-intake sibling operation %s: %v", state, err)
		}
	}
	partialTerminalResponse := doRouterRequest(handler, http.MethodGet, partialPath, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	var partialTerminal EnvironmentOperationGroup
	if partialTerminalResponse.Code != http.StatusOK || json.Unmarshal(partialTerminalResponse.Body.Bytes(), &partialTerminal) != nil || partialTerminal.State != "partial" {
		t.Fatalf("mixed intake terminal status=%d response=%#v body=%s", partialTerminalResponse.Code, partialTerminal, partialTerminalResponse.Body.String())
	}

	// A rollback can truthfully move the currently deployed configuration
	// backwards, including to a legacy deployment whose revision is zero.
	if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,state,actor,expires_at,created_at,updated_at) VALUES ('op_apply_deploy_rollback','deploy','QUEUED','system',$2,$1,$1)`, now.Add(3*time.Minute), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO deployments(id,project_id,service_id,environment_id,action,image_ref,image_digest,state,active,operation_id,trigger_key,configuration_revision,created_at,updated_at) VALUES ('dep_apply_deploy_rollback','prj_apply_deploy','svc_apply_deploy','env_apply_deploy','deploy_image','registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','queued',FALSE,'op_apply_deploy_rollback','rollback-zero-trigger',0,$1,$1)`, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, queuedItem.DeploymentID, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("supersede revision-two deployment: %v", err)
	}
	for _, state := range []string{"deploying", "active"} {
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id='dep_apply_deploy_rollback'`, state, state == "active", now.Add(3*time.Minute)); err != nil {
			t.Fatalf("activate revision-zero rollback %s: %v", state, err)
		}
	}
	var deployedID string
	if err := pool.QueryRow(ctx, `SELECT applied_revision,deployed_revision,COALESCE(deployed_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`).Scan(&appliedRevision, &deployedRevision, &deployedID); err != nil {
		t.Fatal(err)
	}
	if appliedRevision != 2 || deployedRevision != 0 || deployedID != "dep_apply_deploy_rollback" {
		t.Fatalf("rollback truth applied=%d deployed=%d deployment=%q", appliedRevision, deployedRevision, deployedID)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, "dep_apply_deploy_rollback", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("remove final active deployment: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT deployed_revision,COALESCE(deployed_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`).Scan(&deployedRevision, &deployedID); err != nil {
		t.Fatal(err)
	}
	if deployedRevision != 0 || deployedID != "" {
		t.Fatalf("no-active truth deployed=%d deployment=%q", deployedRevision, deployedID)
	}

	for _, terminal := range []string{"failed", "canceled"} {
		deploymentID := "dep_apply_deploy_" + terminal
		operationID := "op_apply_deploy_" + terminal
		if _, err := pool.Exec(ctx, `INSERT INTO operations(id,kind,state,actor,expires_at,created_at,updated_at) VALUES ($1,'deploy','QUEUED','system',$3,$2,$2)`, operationID, now.Add(5*time.Minute), now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO deployments(id,project_id,service_id,environment_id,action,image_ref,image_digest,state,active,operation_id,trigger_key,configuration_revision,created_at,updated_at) VALUES ($1,'prj_apply_deploy','svc_apply_deploy','env_apply_deploy','deploy_image','registry.example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','queued',FALSE,$2,$3,2,$4,$4)`, deploymentID, operationID, terminal+"-pending-trigger", now.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_configuration_revisions SET pending_deployment_id=$1 WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`, deploymentID); err != nil {
			t.Fatal(err)
		}
		failureCode := ""
		if terminal == "failed" {
			failureCode = "WORKER_ERROR"
		}
		if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,failure_code=$2,updated_at=$3 WHERE id=$4`, terminal, failureCode, now.Add(6*time.Minute), deploymentID); err != nil {
			t.Fatalf("terminal candidate %s: %v", terminal, err)
		}
		if err := pool.QueryRow(ctx, `SELECT COALESCE(pending_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_apply_deploy' AND service_id='svc_apply_deploy'`).Scan(&pendingDeployment); err != nil || pendingDeployment != "" {
			t.Fatalf("terminal %s pending=%q err=%v", terminal, pendingDeployment, err)
		}
	}

	// Service visibility alone does not reveal resource-specific group facts.
	// A viewer can read configuration revision state but not a variable group,
	// because the current role has service.read without variables.read.
	if _, err := pool.Exec(ctx, `UPDATE workspace_members SET role=$1,updated_at=$2 WHERE workspace_id='wrk_apply_deploy' AND user_id='usr_apply_deploy'`, "viewer", now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	configurationResponse := doRouterRequest(handler, http.MethodGet, v1alphaPrefix+"/environments/env_apply_deploy/changeset", "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	if configurationResponse.Code != http.StatusOK {
		t.Fatalf("viewer configuration state status=%d body=%s", configurationResponse.Code, configurationResponse.Body.String())
	}
	variableGroupResponse := doRouterRequest(handler, http.MethodGet, groupPath, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
	if variableGroupResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer variable group status=%d body=%s", variableGroupResponse.Code, variableGroupResponse.Body.String())
	}

	// Current authority is re-read for every request. A further downgrade to
	// deployer cannot use project.read alone to inspect configuration or group
	// deployment identifiers.
	if _, err := pool.Exec(ctx, `UPDATE workspace_members SET role=$1,updated_at=$2 WHERE workspace_id='wrk_apply_deploy' AND user_id='usr_apply_deploy'`, "deployer", now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"configuration state": v1alphaPrefix + "/environments/env_apply_deploy/changeset",
		"operation group":     groupPath,
	} {
		response := doRouterRequest(handler, http.MethodGet, path, "", map[string]string{"Authorization": "Bearer " + login.AccessToken})
		if response.Code != http.StatusForbidden {
			t.Fatalf("downgraded deployer %s status=%d body=%s", name, response.Code, response.Body.String())
		}
	}
}

func TestDeploymentSnapshotRevisionFollowsApplyFreshReplayAndRollback(t *testing.T) {
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
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	password := "revision-binding-integration-password"
	hash, err := bootstrap.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,session_generation,created_at,updated_at) VALUES ('usr_revision_binding','revision-binding@example.com','Revision Binding',$1,1,$2,$2)`, hash, now); err != nil {
		t.Fatalf("seed revision binding user: %v", err)
	}
	for _, statement := range []string{
		`INSERT INTO workspaces(id,slug,display_name,created_at,updated_at) VALUES ('wrk_revision_binding','revision-binding','Revision Binding',$1,$1)`,
		`INSERT INTO projects(id,workspace_id,slug,display_name,created_at,updated_at) VALUES ('prj_revision_binding','wrk_revision_binding','app','App',$1,$1)`,
		`INSERT INTO environments(id,project_id,slug,name,tier,is_protected,access_restricted,created_at,updated_at) VALUES ('env_revision_binding','prj_revision_binding','production','Production','production',FALSE,FALSE,$1,$1)`,
		`INSERT INTO services(id,project_id,slug,display_name,created_at,updated_at) VALUES ('svc_revision_binding','prj_revision_binding','api','API',$1,$1)`,
		`INSERT INTO workspace_members(workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at) VALUES ('wrk_revision_binding','usr_revision_binding','member',$1,$1,$1,$1)`,
	} {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed revision binding: %v", err)
		}
	}
	deployments := NewPostgresDeploymentStore(pool)
	deployments.Now = func() time.Time { return now }
	configs := serviceconfig.NewStore(pool)
	configs.Now = func() time.Time { return now }
	createFresh := func(id, trigger string) {
		t.Helper()
		_, err := deployments.CreateDeployment(ctx, DeploymentRecord{
			ID: id, ProjectID: "prj_revision_binding", ServiceID: "svc_revision_binding",
			EnvironmentID: "env_revision_binding", Action: deploy.ActionDeployImage,
			ImageRef:    "registry.example/api@sha256:" + strings.Repeat("a", 64),
			ImageDigest: "sha256:" + strings.Repeat("a", 64), TriggerKey: trigger,
		})
		if err != nil {
			t.Fatalf("create fresh %s: %v", id, err)
		}
		if _, err := configs.ResolveDeployment(ctx, id, "prj_revision_binding", "env_revision_binding", "svc_revision_binding"); err != nil {
			t.Fatalf("snapshot fresh %s: %v", id, err)
		}
	}
	setActive := func(previous, candidate string, at time.Time) {
		t.Helper()
		if previous != "" {
			if _, err := pool.Exec(ctx, `UPDATE deployments SET state='superseded',active=FALSE,updated_at=$2 WHERE id=$1`, previous, at); err != nil {
				t.Fatalf("supersede %s: %v", previous, err)
			}
		}
		for _, state := range []string{"deploying", "active"} {
			if _, err := pool.Exec(ctx, `UPDATE deployments SET state=$1,active=$2,updated_at=$3 WHERE id=$4`, state, state == "active", at, candidate); err != nil {
				t.Fatalf("promote %s to %s: %v", candidate, state, err)
			}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO service_environment_state(service_id,environment_id,active_deployment_id,last_deployment_seq,version,updated_at)
			VALUES ('svc_revision_binding','env_revision_binding',$1,1,1,$2)
			ON CONFLICT (service_id,environment_id) DO UPDATE SET active_deployment_id=EXCLUDED.active_deployment_id,updated_at=EXCLUDED.updated_at`, candidate, at); err != nil {
			t.Fatalf("point active to %s: %v", candidate, err)
		}
	}
	assertRevision := func(deploymentID string, want int64) {
		t.Helper()
		var deploymentRevision, snapshotRevision int64
		if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM deployments WHERE id=$1`, deploymentID).Scan(&deploymentRevision); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT configuration_revision FROM deployment_config_snapshots WHERE deployment_id=$1`, deploymentID).Scan(&snapshotRevision); err != nil {
			t.Fatal(err)
		}
		if deploymentRevision != want || snapshotRevision != want {
			t.Fatalf("revision %s deployment=%d snapshot=%d want=%d", deploymentID, deploymentRevision, snapshotRevision, want)
		}
	}
	createFresh("dep_revision_zero", "revision-zero")
	assertRevision("dep_revision_zero", 0)
	setActive("", "dep_revision_zero", now)

	stack := postgresIdentityIntegrationStack(t, pool, now)
	handler := NewRouterWithDataStack(stack)
	login, err := stack.IdentitySessions.Login(ctx, identity.LoginRequest{Email: "revision-binding@example.com", Password: password, ClientKind: "api"})
	if err != nil {
		t.Fatal(err)
	}
	applyOnly := func(name, key string, wantRevision int64) {
		t.Helper()
		body := fmt.Sprintf(`{"expected_revision":0,"changes":[{"kind":"variable","operation":"add","service_id":"svc_revision_binding","name":%q,"scope":"secret","value":"sealed-value"}]}`, name)
		put := doRouterRequest(handler, http.MethodPut, v1alphaPrefix+"/environments/env_revision_binding/changeset", body, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": key + "-draft", "If-Match": `"0"`})
		if put.Code != http.StatusOK {
			t.Fatalf("apply-only draft status=%d body=%s", put.Code, put.Body.String())
		}
		apply := doRouterRequest(handler, http.MethodPost, v1alphaPrefix+"/environments/env_revision_binding/changeset/apply", `{"expected_revision":1}`, map[string]string{"Authorization": "Bearer " + login.AccessToken, "Idempotency-Key": key + "-apply", "If-Match": `"1"`})
		if apply.Code != http.StatusOK {
			t.Fatalf("apply-only status=%d body=%s", apply.Code, apply.Body.String())
		}
		var applied EnvironmentChangesetView
		if err := json.Unmarshal(apply.Body.Bytes(), &applied); err != nil || applied.ConfigurationState.AppliedRevision != wantRevision {
			t.Fatalf("apply-only revision response=%#v err=%v", applied, err)
		}
	}
	applyOnly("TOKEN_ONE", "revision-one", 1) // not-a-failure-code
	createFresh("dep_revision_one", "revision-one-fresh")
	assertRevision("dep_revision_one", 1)
	setActive("dep_revision_zero", "dep_revision_one", now.Add(time.Minute))

	actions := NewPostgresDeploymentActionStore(pool)
	actions.Now = func() time.Time { return now.Add(2 * time.Minute) }
	redeploy, err := actions.CommitImageReplay(ctx, ImageReplayRequest{
		ServiceID: "svc_revision_binding", SourceID: "dep_revision_one",
		Action: deploy.ActionRedeployImage, TriggerKey: "revision-one-redeploy", Now: now.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("revisioned redeploy: %v", err)
	}
	assertRevision(redeploy.Deployment.ID, 1)
	setActive("dep_revision_one", redeploy.Deployment.ID, now.Add(2*time.Minute))
	var appliedRevision, deployedRevision int64
	var deployedID string
	if err := pool.QueryRow(ctx, `SELECT applied_revision,deployed_revision,COALESCE(deployed_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_revision_binding' AND service_id='svc_revision_binding'`).Scan(&appliedRevision, &deployedRevision, &deployedID); err != nil {
		t.Fatal(err)
	}
	if appliedRevision != 1 || deployedRevision != 1 || deployedID != redeploy.Deployment.ID {
		t.Fatalf("redeploy truth applied=%d deployed=%d id=%q", appliedRevision, deployedRevision, deployedID)
	}

	applyOnly("TOKEN_TWO", "revision-two", 2) // not-a-failure-code
	createFresh("dep_revision_two", "revision-two-fresh")
	assertRevision("dep_revision_two", 2)
	setActive(redeploy.Deployment.ID, "dep_revision_two", now.Add(3*time.Minute))
	rollback, err := actions.CommitImageReplay(ctx, ImageReplayRequest{
		ServiceID: "svc_revision_binding", SourceID: "dep_revision_one",
		Action: deploy.ActionRollbackDigest, TriggerKey: "revision-one-rollback", Now: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatalf("revisioned rollback: %v", err)
	}
	assertRevision(rollback.Deployment.ID, 1)
	setActive("dep_revision_two", rollback.Deployment.ID, now.Add(4*time.Minute))
	if err := pool.QueryRow(ctx, `SELECT applied_revision,deployed_revision,COALESCE(deployed_deployment_id,'') FROM service_configuration_revisions WHERE environment_id='env_revision_binding' AND service_id='svc_revision_binding'`).Scan(&appliedRevision, &deployedRevision, &deployedID); err != nil {
		t.Fatal(err)
	}
	if appliedRevision != 2 || deployedRevision != 1 || deployedID != rollback.Deployment.ID {
		t.Fatalf("rollback truth applied=%d deployed=%d id=%q", appliedRevision, deployedRevision, deployedID)
	}
}
