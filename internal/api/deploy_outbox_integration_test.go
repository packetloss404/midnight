//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestPostgresDeployIntakeCommitSurvivesCrashBeforeQueueDelivery(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()

	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stack, err := NewPostgresDataStack(PostgresDataStackConfig{
		Pool:                  pool,
		MigrationsFS:          migrations.FS,
		ManifestPath:          "manifest.yaml",
		CursorSecret:          []byte("0123456789abcdef0123456789abcdef"),
		AgentHMACSecret:       []byte("outbox-integration-secret"),
		AutomationTokenMACKey: []byte("deploy-outbox-automation-token-key"),
		InvitationMACKey:      []byte("deploy-outbox-invitation-mac-key"),
		VariableIntentMACKey:  []byte("deploy-outbox-variable-intent-key"),
		VariableKeyProvider:   testVariableKeyProvider(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// This test isolates durable deploy intake. Production stacks fail closed
	// by default, so opt into the unauthenticated test harness explicitly.
	stack.ProductionAuthRequired = false
	durable := NewPostgresDeployOutbox(pool)
	durable.WorkerID = "dispatcher-before-crash"
	stack.DeployIntakeOutbox = durable
	router := NewRouterWithDataStack(stack)

	project := doRouterJSON[Project](t, router, http.MethodPost, "/api/v1alpha/projects",
		`{"workspace_id":"wrk_default","slug":"outbox","display_name":"Outbox"}`,
		map[string]string{"Idempotency-Key": "create-outbox-project"}, http.StatusCreated)
	service := doRouterJSON[Service](t, router, http.MethodPost, "/api/v1alpha/services",
		`{"project_id":"`+string(project.ID)+`","slug":"web","display_name":"Web"}`,
		map[string]string{"Idempotency-Key": "create-outbox-service"}, http.StatusCreated)
	environments, err := stack.EnvironmentStore.ListEnvironments(ctx, ResourceListFilter{ParentID: project.ID})
	if err != nil || len(environments) != 1 {
		t.Fatalf("production environments = %#v, %v", environments, err)
	}

	requestBody := `{"environment_id":"` + string(environments[0].ID) + `","image":"registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	req := doRouterRequest(router, http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", requestBody, map[string]string{
		"Idempotency-Key": "postgres-outbox-crash-1",
	})
	if req.Code != http.StatusAccepted {
		t.Fatalf("deploy status = %d body=%s, want 202", req.Code, req.Body.String())
	}
	if got := req.Header().Get("X-Midnight-Delivery-State"); got != "pending" {
		t.Fatalf("delivery state = %q, want pending", got)
	}
	var accepted orailtypes.Operation
	if err := json.Unmarshal(req.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.CreatedAt.IsZero() || !accepted.CreatedAt.Equal(accepted.UpdatedAt) {
		t.Fatalf("accepted operation timestamps = %s/%s, want one durable commit instant", accepted.CreatedAt, accepted.UpdatedAt)
	}
	replay := doRouterRequest(router, http.MethodPost, "/api/v1alpha/services/"+string(service.ID)+"/deployments", requestBody, map[string]string{
		"Idempotency-Key": "postgres-outbox-crash-1",
	})
	if replay.Code != req.Code ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		replay.Header().Get("Content-Type") != req.Header().Get("Content-Type") ||
		replay.Header().Get("Location") != req.Header().Get("Location") ||
		replay.Header().Get("X-Midnight-Delivery-State") != req.Header().Get("X-Midnight-Delivery-State") ||
		replay.Body.String() != req.Body.String() {
		t.Fatalf("replay = %d/%q/%q/%q/%q/%s, want exact stored response %d/%q/%q/%q/%s", replay.Code, replay.Header().Get("Idempotent-Replayed"), replay.Header().Get("Content-Type"), replay.Header().Get("Location"), replay.Header().Get("X-Midnight-Delivery-State"), replay.Body.String(), req.Code, req.Header().Get("Content-Type"), req.Header().Get("Location"), req.Header().Get("X-Midnight-Delivery-State"), req.Body.String())
	}

	assertTableCount(t, ctx, pool, "operations", 1)
	assertTableCount(t, ctx, pool, "deployments", 1)
	assertTableCount(t, ctx, pool, "deploy_intake_outbox", 1)
	assertTableCount(t, ctx, pool, "jobs", 0)
	assertTableCount(t, ctx, pool, "operation_events", 1)
	assertTableCount(t, ctx, pool, "deployment_events", 1)
	var idemState, operationID string
	if err := pool.QueryRow(ctx,
		`SELECT state, COALESCE(operation_id, '') FROM idempotency_keys WHERE key = $1`,
		"postgres-outbox-crash-1",
	).Scan(&idemState, &operationID); err != nil {
		t.Fatal(err)
	}
	if idemState != "SUCCEEDED" || operationID == "" {
		t.Fatalf("idempotency state=%q operation=%q, want committed success linkage", idemState, operationID)
	}

	// A replacement process creates a fresh dispatcher against the same DB.
	afterRestart := NewPostgresDeployOutbox(pool)
	afterRestart.WorkerID = "dispatcher-after-restart"
	dispatchNow := time.Now().UTC().Add(time.Second)
	afterRestart.Now = func() time.Time { return dispatchNow }

	// Inject a delivery-side schema fault after the durable commit. The claim
	// is released back to pending with visible attempt/error evidence.
	if _, err := pool.Exec(ctx, `DROP INDEX jobs_outbox_id_uq`); err != nil {
		t.Fatal(err)
	}
	if delivered, err := afterRestart.DispatchDeployIntake(ctx, 8); err == nil || delivered != 0 {
		t.Fatalf("faulted dispatch = %d, %v; want visible delivery error", delivered, err)
	}
	var retryState, retryError string
	var retryAttempt int
	if err := pool.QueryRow(ctx,
		`SELECT state, attempt, last_error FROM deploy_intake_outbox WHERE operation_id = $1`,
		operationID,
	).Scan(&retryState, &retryAttempt, &retryError); err != nil {
		t.Fatal(err)
	}
	if retryState != "pending" || retryAttempt != 1 || retryError == "" {
		t.Fatalf("retry state=%q attempt=%d error=%q, want visible pending retry", retryState, retryAttempt, retryError)
	}
	if _, err := pool.Exec(ctx,
		`CREATE UNIQUE INDEX jobs_outbox_id_uq ON jobs (outbox_id) WHERE outbox_id IS NOT NULL`,
	); err != nil {
		t.Fatal(err)
	}
	dispatchNow = dispatchNow.Add(2 * time.Second)
	delivered, err := afterRestart.DispatchDeployIntake(ctx, 8)
	if err != nil || delivered != 1 {
		t.Fatalf("dispatch after restart = %d, %v; want 1", delivered, err)
	}
	assertTableCount(t, ctx, pool, "jobs", 1)
	var state, jobID string
	if err := pool.QueryRow(ctx,
		`SELECT state, COALESCE(job_id, '') FROM deploy_intake_outbox WHERE operation_id = $1`,
		operationID,
	).Scan(&state, &jobID); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || jobID == "" {
		t.Fatalf("outbox state=%q job=%q, want delivered linkage", state, jobID)
	}
	var jobOutboxID string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(outbox_id, '') FROM jobs WHERE id = $1`, jobID).Scan(&jobOutboxID); err != nil {
		t.Fatal(err)
	}
	if jobOutboxID == "" {
		t.Fatal("job outbox linkage is empty")
	}
	assertTableCount(t, ctx, pool, "operation_events", 3)
	if again, err := afterRestart.DispatchDeployIntake(ctx, 8); err != nil || again != 0 {
		t.Fatalf("duplicate dispatch = %d, %v; want no-op", again, err)
	}
	assertTableCount(t, ctx, pool, "jobs", 1)
}

func doRouterRequest(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func assertTableCount(t *testing.T, ctx context.Context, pool *pg.Pool, table string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}
