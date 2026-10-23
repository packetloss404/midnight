//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/timeline"
	migrations "github.com/midnight/midnight/migrations"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// startPostgres spins up a real Postgres 16 container, returns the
// container handle + DSN. The harness aborts cleanly when Docker is
// unavailable so developers running on hosts without Docker get a
// useful diagnostic instead of a panic.
func startPostgres(t *testing.T, ctx context.Context) (tc.Container, string) {
	t.Helper()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		if isDockerUnavailable(err) {
			t.Skipf("docker not available: %v", err)
		}
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	return container, dsn
}

func isDockerUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "docker") && (strings.Contains(msg, "not running") || strings.Contains(msg, "cannot connect") || strings.Contains(msg, "no such file") || strings.Contains(msg, "permission denied"))
}

func newPool(t *testing.T, ctx context.Context, dsn string) *pg.Pool {
	t.Helper()
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatalf("pg.Open: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

func runMigrations(t *testing.T, ctx context.Context, pool *pg.Pool) []pg.MigrationFile {
	t.Helper()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("migrations up: %v", err)
	}
	return applied
}

func TestMigrationRunnerComputesRealSHA256(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	applied := runMigrations(t, ctx, pool)
	if len(applied) == 0 {
		t.Fatalf("expected at least one migration applied")
	}
	for _, m := range applied {
		want := computeFileSHA256(t, m.Up)
		if !strings.HasPrefix(m.UpSHA256, "sha256:") {
			t.Fatalf("migration %d UpSHA256 = %q, want sha256: prefix", m.Version, m.UpSHA256)
		}
		got := strings.TrimPrefix(m.UpSHA256, "sha256:")
		if got != want {
			t.Fatalf("migration %d UpSHA256 = %s, want %s", m.Version, got, want)
		}
	}

	rows, err := pg.ListApplied(ctx, pool)
	if err != nil {
		t.Fatalf("list applied: %v", err)
	}
	if len(rows) != len(applied) {
		t.Fatalf("schema_migrations rows = %d, want %d", len(rows), len(applied))
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.UpSHA256, "sha256:") {
			t.Fatalf("schema_migrations row %d up_sha256 = %q, want sha256: prefix", row.Version, row.UpSHA256)
		}
	}

	// Re-run Up: should be a no-op.
	again, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx)
	if err != nil {
		t.Fatalf("idempotent up: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Up() applied %d, want 0", len(again))
	}
}

func computeFileSHA256(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(migrations.FS, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestPostgresOperationStoreRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	store := api.NewPostgresOperationStore(pool)
	op, err := store.Create(ctx, orailtypes.Operation{Kind: "deploy", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(string(op.ID), "op_") {
		t.Fatalf("Create returned id without op_ prefix: %s", op.ID)
	}

	if _, err := store.AppendEvent(ctx, api.OperationEvent{OperationID: op.ID, State: op.State, Message: "queued"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	moved, err := store.Transition(ctx, op.ID, orailtypes.OperationRunning)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if moved.State != orailtypes.OperationRunning {
		t.Fatalf("state = %q, want RUNNING", moved.State)
	}
	if _, err := store.AppendEvent(ctx, api.OperationEvent{OperationID: op.ID, State: moved.State, Message: "running"}); err != nil {
		t.Fatalf("AppendEvent running: %v", err)
	}
	const concurrentEvents = 16
	var appendWG sync.WaitGroup
	appendErrs := make(chan error, concurrentEvents)
	for i := 0; i < concurrentEvents; i++ {
		appendWG.Add(1)
		go func() {
			defer appendWG.Done()
			_, err := store.AppendEvent(ctx, api.OperationEvent{
				OperationID: op.ID,
				State:       moved.State,
				Message:     "concurrent running event",
			})
			appendErrs <- err
		}()
	}
	appendWG.Wait()
	close(appendErrs)
	for err := range appendErrs {
		if err != nil {
			t.Fatalf("concurrent AppendEvent: %v", err)
		}
	}
	finished, err := store.Transition(ctx, op.ID, orailtypes.OperationSucceeded)
	if err != nil {
		t.Fatalf("Transition succeed: %v", err)
	}
	if finished.State != orailtypes.OperationSucceeded {
		t.Fatalf("final state = %q, want SUCCEEDED", finished.State)
	}

	// Process restart simulation: open a new pool, fetch the op.
	pool.Close()
	pool2, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("reopen pool: %v", err)
	}
	defer pool2.Close()
	store2 := api.NewPostgresOperationStore(pool2)
	got, err := store2.Get(ctx, op.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.State != orailtypes.OperationSucceeded {
		t.Fatalf("after restart state = %q, want SUCCEEDED", got.State)
	}
	events, err := store2.ListEvents(ctx, op.ID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2+concurrentEvents {
		t.Fatalf("events = %d, want %d", len(events), 2+concurrentEvents)
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Fatalf("event[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}
}

func TestPostgresIdempotencyStoreReplay(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	opStore := api.NewPostgresOperationStore(pool)
	op, err := opStore.Create(ctx, orailtypes.Operation{Kind: "test", State: orailtypes.OperationQueued})
	if err != nil {
		t.Fatalf("seed op: %v", err)
	}

	store := idempotency.NewPostgresStore(pool)
	key := idempotency.Key("idem-test-1")
	fingerprint := idempotency.RequestFingerprint{Method: "POST", Path: "/api/v1alpha/projects", BodySHA256: idempotency.BodyHashSHA256([]byte(`{"slug":"x"}`))}

	res, err := store.Reserve(ctx, key, fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if res.State != idempotency.StateReserved {
		t.Fatalf("first state = %q, want RESERVED", res.State)
	}
	// In-flight duplicate -> ErrRequestInProgress
	if _, err := store.Reserve(ctx, key, fingerprint, time.Hour); !errors.Is(err, idempotency.ErrRequestInProgress) {
		t.Fatalf("Reserve in-flight err = %v, want ErrRequestInProgress", err)
	}
	if _, err := store.AttachOperation(ctx, res.Reference(), string(op.ID)); err != nil {
		t.Fatalf("AttachOperation: %v", err)
	}
	if _, err := store.CompleteSuccess(ctx, res.Reference(), idempotency.StoredResponse{StatusCode: 202, Body: []byte(`{"id":"` + string(op.ID) + `"}`)}); err != nil {
		t.Fatalf("CompleteSuccess: %v", err)
	}

	// Replay with same body -> verbatim
	replay, err := store.Reserve(ctx, key, fingerprint, time.Hour)
	if err != nil {
		t.Fatalf("Reserve replay: %v", err)
	}
	if replay.State != idempotency.StateSucceeded || replay.Response == nil || replay.Response.StatusCode != 202 {
		t.Fatalf("replay state = %v, response = %#v", replay.State, replay.Response)
	}

	// Different body -> ErrKeyReusedDifferentBody
	other := idempotency.RequestFingerprint{Method: "POST", Path: "/api/v1alpha/projects", BodySHA256: idempotency.BodyHashSHA256([]byte(`{"slug":"y"}`))}
	if _, err := store.Reserve(ctx, key, other, time.Hour); !errors.Is(err, idempotency.ErrKeyReusedDifferentBody) {
		t.Fatalf("Reserve different body err = %v, want ErrKeyReusedDifferentBody", err)
	}
}

// TestPostgresAuditStoreChainPersistsAcrossRestart exercised Lane 1's
// audit.PostgresStore against a real Postgres container. The Phase 1
// integration merge took Lane 4's panel-locked audit.AuditChain
// interface (per LANE4_STATUS.md §C and Open Question 5: "the
// AuditChain interface in internal/audit/chain.go is the seam") and
// Lane 1's diverging audit.Store / PostgresStore types were dropped
// from the package. A Postgres-backed AuditChain implementation is the
// natural follow-up; when it lands, this test should be reinstated
// against that interface.
//
// See docs/handoff/phase1-merge-notes.md (added in the merge commit)
// for the full reconciliation rationale.

func TestPostgresResourceStoreCRUD(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	store := api.NewPostgresResourceStore(pool)
	prj, err := store.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "demo", DisplayName: "Demo"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if !strings.HasPrefix(string(prj.ID), "prj_") {
		t.Fatalf("project id missing prefix: %s", prj.ID)
	}

	// Duplicate slug -> conflict
	if _, err := store.CreateProject(ctx, api.Project{WorkspaceID: api.DefaultWorkspaceID, Slug: "demo", DisplayName: "Demo2"}); !errors.Is(err, api.ErrResourceConflict) {
		t.Fatalf("duplicate slug err = %v, want ErrResourceConflict", err)
	}

	env, err := store.CreateEnvironment(ctx, api.Environment{ProjectID: prj.ID, Slug: "production", Name: "Production", Tier: "production"})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	svc, err := store.CreateService(ctx, api.Service{ProjectID: prj.ID, Slug: "api", DisplayName: "API"})
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}

	if got, err := store.GetProject(ctx, prj.ID); err != nil || got.Slug != "demo" {
		t.Fatalf("GetProject after create: got=%v err=%v", got, err)
	}
	if got, err := store.GetEnvironment(ctx, env.ID); err != nil || got.Tier != "production" {
		t.Fatalf("GetEnvironment after create: got=%v err=%v", got, err)
	}
	// Lifecycle metadata remains durable and a soft-deleted environment cannot
	// be selected after a later control-plane read.
	env.Name, env.Tier = "Preproduction", "staging"
	if _, err := store.UpdateEnvironment(ctx, env); err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if got, err := store.GetEnvironment(ctx, env.ID); err != nil || got.Name != "Preproduction" || got.Tier != "staging" {
		t.Fatalf("GetEnvironment after update: got=%v err=%v", got, err)
	}
	if got, err := store.GetService(ctx, svc.ID); err != nil || got.ProjectID != prj.ID {
		t.Fatalf("GetService after create: got=%v err=%v", got, err)
	}

	envs, err := store.ListEnvironments(ctx, api.ResourceListFilter{ParentID: prj.ID})
	if err != nil || len(envs) != 1 {
		t.Fatalf("ListEnvironments = %v err=%v", envs, err)
	}
	if err := store.DeleteEnvironment(ctx, env.ID); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if _, err := store.GetEnvironment(ctx, env.ID); !errors.Is(err, api.ErrResourceNotFound) {
		t.Fatalf("GetEnvironment after delete error=%v, want not found", err)
	}
	svcs, err := store.ListServices(ctx, api.ResourceListFilter{ParentID: prj.ID})
	if err != nil || len(svcs) != 1 {
		t.Fatalf("ListServices = %v err=%v", svcs, err)
	}
}

func TestPostgresTimelineStoreQueryAndRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	store := timeline.NewPostgresStore(pool)
	for i := 1; i <= 5; i++ {
		if err := store.Append(timeline.Event{
			DeploymentID: "dep_demo",
			EventSeq:     int64(i),
			Type:         timeline.EventIngressRouteRendered,
			TenantID:     "tenant_a",
			RouteName:    fmt.Sprintf("route-%d", i),
			Host:         "demo.example.test",
			At:           time.Now().UTC(),
			Message:      fmt.Sprintf("step %d", i),
		}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	res, err := store.Query(timeline.Filter{DeploymentID: "dep_demo"}, timeline.QueryCursor{}, 3)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Events) != 3 || !res.Truncated {
		t.Fatalf("page 1 = %d events truncated=%v, want 3 truncated", len(res.Events), res.Truncated)
	}
	res2, err := store.Query(timeline.Filter{DeploymentID: "dep_demo"}, res.NextCursor, 100)
	if err != nil {
		t.Fatalf("Query page 2: %v", err)
	}
	if len(res2.Events) != 2 {
		t.Fatalf("page 2 = %d events, want 2", len(res2.Events))
	}

	// Restart: events still there.
	pool.Close()
	pool2 := newPool(t, ctx, dsn)
	store2 := timeline.NewPostgresStore(pool2)
	all := store2.List("dep_demo")
	if len(all) != 5 {
		t.Fatalf("after restart all = %d, want 5", len(all))
	}
}

func TestPostgresQueueLeaseLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	store := queue.NewPostgresStore(pool)
	if _, err := store.Enqueue(ctx, queue.EnqueueRequest{Queue: queue.Build, PartitionKey: "ws_a", Payload: []byte(`{"hint":"first"}`)}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := store.Enqueue(ctx, queue.EnqueueRequest{Queue: queue.Build, PartitionKey: "ws_a", Priority: 10, Payload: []byte(`{"hint":"high"}`)}); err != nil {
		t.Fatalf("Enqueue high: %v", err)
	}

	first, err := store.Lease(ctx, queue.LeaseRequest{Queue: queue.Build, WorkerID: "worker-1", LeaseTime: 30 * time.Second, Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if !strings.Contains(string(first.Payload), "high") {
		t.Fatalf("priority order broken: payload = %s", first.Payload)
	}

	// FOR UPDATE SKIP LOCKED: a second worker leases the next row, not the first.
	second, err := store.Lease(ctx, queue.LeaseRequest{Queue: queue.Build, WorkerID: "worker-2", LeaseTime: 30 * time.Second, Now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("Lease 2: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("workers got the same job: %s", first.ID)
	}

	// Heartbeat extends lease.
	if err := store.Heartbeat(ctx, first.ID, first.LeaseID, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	// Foreign worker heartbeat -> ErrLeaseLost.
	if err := store.Heartbeat(ctx, first.ID, "worker-3", time.Now().UTC().Add(time.Minute)); !errors.Is(err, queue.ErrLeaseLost) {
		t.Fatalf("foreign heartbeat err = %v, want ErrLeaseLost", err)
	}
	if err := store.Complete(ctx, first.ID, first.LeaseID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	final, err := store.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	if final.State != queue.JobSucceeded {
		t.Fatalf("final state = %q, want SUCCEEDED", final.State)
	}
}

func TestDSNLoaderHonorsEnvOverride(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Setenv("MIDNIGHT_DB_DSN", "postgres://example/midnight")
	loaded, err := pg.LoadDSN()
	if err != nil {
		t.Fatalf("LoadDSN: %v", err)
	}
	if loaded.Source != pg.DSNSourceEnv || loaded.DSN != "postgres://example/midnight" {
		t.Fatalf("LoadDSN result = %#v", loaded)
	}
}

// TestEmbeddedMigrationsExist sanity-checks that the embed.FS is wired
// before we start a container; saves time when the wiring is wrong.
func TestEmbeddedMigrationsExist(t *testing.T) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("read embed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("no embedded migrations")
	}
	if _, err := fs.Stat(migrations.FS, "manifest.yaml"); err != nil {
		t.Fatalf("manifest.yaml missing from embed: %v", err)
	}
}

// silence unused import warnings on builds that prune the helper.
var _ = os.Stdout
