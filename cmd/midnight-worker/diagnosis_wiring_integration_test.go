//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	"github.com/midnight/midnight/internal/agent/tools"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// This drives the installed registration function, real HTTP provider adapter,
// observed operation failure and PostgreSQL conversation/event stores. The
// provider is local: no external model service or credentials are required.
func TestWorkerDiagnosisPersistsScopedReportAndOperationAttachment(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("midnight"), tcpostgres.WithUsername("midnight_test"), tcpostgres.WithPassword("midnight_test"), tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MIDNIGHT_TESTCONTAINERS_DIRECT_IP") == "1" { // not-a-failure-code
		ip, err := container.ContainerIP(ctx)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Host = net.JoinHostPort(ip, "5432")
		dsn = parsed.String()
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO workspaces (id,slug,display_name) VALUES ('wrk_diagnosis','diagnosis','Diagnosis');
INSERT INTO projects (id,workspace_id,slug,display_name) VALUES ('prj_diagnosis','wrk_diagnosis','diagnosis','Diagnosis');
INSERT INTO environments (id,project_id,slug,name) VALUES ('env_diagnosis','prj_diagnosis','qa','QA');
INSERT INTO services (id,project_id,slug,display_name,spec) VALUES ('svc_diagnosis','prj_diagnosis','web','Web','{"image":"qa-web"}');
INSERT INTO service_variables (id,workspace_id,project_id,environment_id,service_id,name,value_ciphertext)
VALUES ('var_diagnosis','wrk_diagnosis','prj_diagnosis','env_diagnosis','svc_diagnosis','PORT','ciphertext-value-never-read');`); err != nil {
		t.Fatal(err)
	}
	operations := api.NewPostgresOperationStore(pool)
	observed := api.NewObservedOperationStore(operations)
	op, err := operations.Create(ctx, orailtypes.Operation{ID: "op_diagnosis", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatal(err)
	}
	deployments := api.NewPostgresDeploymentStore(pool)
	if _, err := deployments.CreateDeployment(ctx, api.DeploymentRecord{ID: "dep_diagnosis", ProjectID: "prj_diagnosis", EnvironmentID: "env_diagnosis", ServiceID: "svc_diagnosis", OperationID: string(op.ID), Action: deploy.ActionDeployImage, State: deploy.DeploymentQueued}); err != nil {
		t.Fatal(err)
	}
	if err := deployments.FailDeployment(ctx, "dep_diagnosis", deploy.DeploymentQueued, "RECONCILE_STEP_FAILED", "HTTP readiness failed"); err != nil {
		t.Fatal(err)
	}
	// Deliberately seed legacy, unsanitized records. Outbound context must be
	// scrubbed again and remain limited to the exact canonical deployment.
	if _, err := pool.Exec(ctx, `
INSERT INTO observability_log_records (observed_at,project_id,environment_id,service_id,deployment_id,operation_id,surface,stream,level,component,message,source_key,source_sequence)
VALUES (CURRENT_TIMESTAMP,'prj_diagnosis','env_diagnosis','svc_diagnosis','dep_diagnosis','op_diagnosis','build','stdout','info','app.build','build-tail API_KEY=do-not-export-build','qa-diagnosis-build','1'),
       (CURRENT_TIMESTAMP,'prj_diagnosis','env_diagnosis','svc_diagnosis','dep_diagnosis','op_diagnosis','runtime','stderr','error','app.runtime','runtime-tail PASSWORD=do-not-export-runtime','qa-diagnosis-runtime','1');`); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	requests := make(chan string, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		select {
		case requests <- string(body):
		default:
		}
		var request struct {
			Model string            `json:"model"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &request); err != nil || request.Model != "qa-diagnosis-model" || len(request.Tools) != 0 || r.Header.Get("X-Api-Key") != "qa-local-provider" {
			t.Error("diagnosis provider request lost its model, credential or report-only tool policy")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"qa-report","type":"message","role":"assistant","model":"qa-diagnosis-model","content":[{"type":"text","text":"Set PORT to the port served by the application."}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":10}}`)
	}))
	defer provider.Close()
	t.Setenv(diagnosis.EnvEnabled, "true")
	t.Setenv(diagnosis.EnvModel, "qa-diagnosis-model")
	t.Setenv("MIDNIGHT_AGENT_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "qa-local-provider")
	t.Setenv("ANTHROPIC_BASE_URL", provider.URL) // not-a-failure-code
	registerWorkerLifecycleObservers(observed, pool)
	failed, err := observed.Fail(ctx, op.ID, "RECONCILE_STEP_FAILED", "HTTP readiness failed API_KEY=do-not-export-reason")
	if err != nil {
		t.Fatal(err)
	}
	var conversationID string
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for conversationID == "" {
		events, err := operations.ListEvents(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if strings.HasPrefix(event.Message, diagnosis.EventNameDiagnosisAttached+" diagnosis_id=") {
				conversationID = strings.TrimPrefix(event.Message, diagnosis.EventNameDiagnosisAttached+" diagnosis_id=")
			}
		}
		if conversationID != "" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("failed deployment never acquired a persisted diagnosis attachment")
		case <-tick.C:
		}
	}
	request := <-requests
	for _, expected := range []string{"op_diagnosis", "dep_diagnosis", "svc_diagnosis", "build-tail", "runtime-tail", "PORT", "service_spec_digest"} {
		if !strings.Contains(request, expected) {
			t.Errorf("outbound diagnosis missing %q", expected)
		}
	}
	for _, forbidden := range []string{"do-not-export", "ciphertext-value-never-read"} {
		if strings.Contains(request, forbidden) {
			t.Error("diagnosis context exposed credential material")
		}
	}
	store := conversation.NewPostgresStore(pool)
	conv, err := store.GetConversation(ctx, conversationID)
	if err != nil || conv.ProjectID != "prj_diagnosis" || conv.ServiceID != "svc_diagnosis" || conv.Model != "qa-diagnosis-model" {
		t.Fatalf("persisted diagnosis lost canonical scope/model: %#v err=%v", conv, err)
	}
	for key, expected := range map[string]string{"midnight.workspace_id": "wrk_diagnosis", "midnight.project_id": "prj_diagnosis", "midnight.environment_id": "env_diagnosis", "midnight.service_id": "svc_diagnosis", "midnight.deployment_id": "dep_diagnosis", "midnight.operation_id": "op_diagnosis"} {
		if conv.Metadata[key] != expected {
			t.Errorf("persisted lineage %s=%v, want %s", key, conv.Metadata[key], expected)
		}
	}
	// Exercise the canonical API's installation-operator read contract. Human
	// instance-admin versus ordinary-tenant access is tested at the router.
	server := httptest.NewServer(api.NewRouterWithDataStack(api.DataStack{APIBearerToken: "qa-operator-read-token", ConversationStore: store, OperationStore: operations}))
	defer server.Close()
	for _, path := range []string{api.AgentConversationsPrefix + "/" + conversationID, api.AgentConversationsPrefix + "/" + conversationID + "/messages", "/api/v1alpha/operations/op_diagnosis/events"} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer qa-operator-read-token")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("persisted report API status=%d", response.StatusCode)
		}
		if strings.HasSuffix(path, "/messages") && !strings.Contains(string(body), "Set PORT") {
			t.Fatal("attachment event became visible before the report message was persisted")
		}
		if strings.HasSuffix(path, "/events") && !strings.Contains(string(body), "diagnosis_id="+conversationID) {
			t.Fatal("canonical operation stream did not expose its diagnosis link")
		}
	}
	// A fresh runner receives the durable event snapshot after restart and
	// must not pay for or append another report on repeated failure delivery.
	events, err := operations.ListEvents(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller, model, err := newWorkerDiagnosisCaller(tools.New())
	if err != nil {
		t.Fatal(err)
	}
	opts, err := workerDiagnosisOptions(pool, operations, caller, model)
	if err != nil {
		t.Fatal(err)
	}
	opts.Enabled, opts.AsyncRun = true, func(fn func()) { fn() }
	replay := diagnosis.NewRunner(opts)
	replay.OnFailed(ctx, failed, events)
	if calls.Load() != 1 {
		t.Fatalf("provider called %d times for one operation", calls.Load())
	}
	// A failure without canonical lineage must not spend money or save an
	// unscoped conversation, even if free-form events suggest another tenant.
	replay.OnFailed(ctx, orailtypes.Operation{ID: "op_unlinked", Kind: "deploy", State: orailtypes.OperationFailed}, []api.OperationEvent{{Message: "deployment_id=dep_diagnosis"}})
	if calls.Load() != 1 {
		t.Fatal("unlinked failure reached the provider")
	}
}
