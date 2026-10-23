//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/serviceconfig"
)

// validDigest is a well-formed sha256 image digest for the deployments
// digest-format CHECK.
var validDigest = "sha256:" + strings.Repeat("a", 64)
var validDigest2 = "sha256:" + strings.Repeat("b", 64)

func seedDeploymentScope(t *testing.T, ctx context.Context, pool *pg.Pool) (project, service, env string) {
	t.Helper()
	project, service, env = "prj_dep_it", "svc_dep_it", "env_dep_it"
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, workspace_id, slug, display_name) VALUES ($1, 'wrk_default', 'dep-it', 'Deploy IT')`, project); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO services (id, project_id, slug, display_name) VALUES ($1, $2, 'web', 'Web')`, service, project); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO environments (id, project_id, slug, name) VALUES ($1, $2, 'production', 'Production')`, env, project); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	return project, service, env
}

func deploymentState(t *testing.T, ctx context.Context, pool *pg.Pool, id string) (state string, active bool) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT state, active FROM deployments WHERE id = $1`, id).Scan(&state, &active); err != nil {
		t.Fatalf("read deployment %s: %v", id, err)
	}
	return state, active
}

func activePointer(t *testing.T, ctx context.Context, pool *pg.Pool, service, env string) string {
	t.Helper()
	var ptr *string
	if err := pool.QueryRow(ctx,
		`SELECT active_deployment_id FROM service_environment_state WHERE service_id = $1 AND environment_id = $2`,
		service, env).Scan(&ptr); err != nil {
		t.Fatalf("read active pointer: %v", err)
	}
	if ptr == nil {
		return ""
	}
	return *ptr
}

func countActive(t *testing.T, ctx context.Context, pool *pg.Pool, service, env string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM deployments WHERE service_id = $1 AND environment_id = $2 AND active`,
		service, env).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

func TestDeploymentStorePromoteSupersedeAndOneActiveInvariant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, env := seedDeploymentScope(t, ctx, pool)

	store := api.NewPostgresDeploymentStore(pool)

	// A: create -> deploying -> promote (first active).
	a, err := store.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "img:a", ImageDigest: validDigest,
	})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if err := store.MarkDeploymentDeploying(ctx, a.ID, deploy.DeploymentQueued); err != nil {
		t.Fatalf("A queued->deploying: %v", err)
	}
	if err := store.PromoteDeployment(ctx, a.ID, ""); err != nil {
		t.Fatalf("promote A: %v", err)
	}
	if s, active := deploymentState(t, ctx, pool, a.ID); s != "active" || !active {
		t.Fatalf("A = (%s, active=%v), want active/true", s, active)
	}
	if got := activePointer(t, ctx, pool, service, env); got != a.ID {
		t.Fatalf("active pointer = %s, want A(%s)", got, a.ID)
	}

	// B: create -> deploying -> promote. A must be superseded; exactly one active.
	b, err := store.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "img:b", ImageDigest: validDigest2,
	})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	if err := store.MarkDeploymentDeploying(ctx, b.ID, deploy.DeploymentQueued); err != nil {
		t.Fatalf("B queued->deploying: %v", err)
	}
	if err := store.PromoteDeployment(ctx, b.ID, ""); err != nil {
		t.Fatalf("promote B: %v", err)
	}
	if s, _ := deploymentState(t, ctx, pool, a.ID); s != "superseded" {
		t.Fatalf("A after B promote = %s, want superseded", s)
	}
	if s, active := deploymentState(t, ctx, pool, b.ID); s != "active" || !active {
		t.Fatalf("B = (%s, active=%v), want active/true", s, active)
	}
	if n := countActive(t, ctx, pool, service, env); n != 1 {
		t.Fatalf("active count = %d, want exactly 1 (one-active invariant)", n)
	}
	if got := activePointer(t, ctx, pool, service, env); got != b.ID {
		t.Fatalf("active pointer = %s, want B(%s)", got, b.ID)
	}

	// Read-flip source: the service lists both deployments newest-first, with B
	// active and A superseded — no event-string parsing.
	list, err := store.ListDeploymentsByService(ctx, service)
	if err != nil {
		t.Fatalf("ListDeploymentsByService: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].ID != b.ID || !list[0].Active || list[0].State != deploy.DeploymentActive {
		t.Fatalf("newest = %#v, want B active", list[0])
	}
	if list[1].ID != a.ID || list[1].Active || list[1].State != deploy.DeploymentSuperseded {
		t.Fatalf("oldest = %#v, want A superseded/inactive", list[1])
	}
}

func TestDeploymentStoreRollbackAndCASConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, env := seedDeploymentScope(t, ctx, pool)
	store := api.NewPostgresDeploymentStore(pool)

	// Stand up an active deployment A.
	a, _ := store.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "img:a", ImageDigest: validDigest,
	})
	if err := store.MarkDeploymentDeploying(ctx, a.ID, deploy.DeploymentQueued); err != nil {
		t.Fatalf("A deploying: %v", err)
	}
	if err := store.PromoteDeployment(ctx, a.ID, ""); err != nil {
		t.Fatalf("promote A: %v", err)
	}

	// Rollback to A creates a NEW queued row carrying A's immutable digest and
	// action=rollback_digest, with expected-active = A.
	r, err := store.CreateRollbackDeployment(ctx, a.ID)
	if err != nil {
		t.Fatalf("rollback create: %v", err)
	}
	if r.Action != deploy.ActionRollbackDigest || r.ImageDigest != validDigest {
		t.Fatalf("rollback rec = (%s, %s), want rollback_digest/%s", r.Action, r.ImageDigest, validDigest)
	}
	if r.ExpectedActiveID != a.ID {
		t.Fatalf("rollback expected-active = %s, want A(%s)", r.ExpectedActiveID, a.ID)
	}

	// A CAS promote with a stale expected-active must be rejected.
	if err := store.MarkDeploymentDeploying(ctx, r.ID, deploy.DeploymentQueued); err != nil {
		t.Fatalf("R deploying: %v", err)
	}
	if err := store.PromoteDeployment(ctx, r.ID, "dep_stale_nonexistent"); !errors.Is(err, api.ErrPromotionConflict) {
		t.Fatalf("stale-CAS promote err = %v, want ErrPromotionConflict", err)
	}
	// R must still be deploying (promotion aborted, previous active intact).
	if s, _ := deploymentState(t, ctx, pool, r.ID); s != "deploying" {
		t.Fatalf("R after failed CAS = %s, want deploying", s)
	}
	if got := activePointer(t, ctx, pool, service, env); got != a.ID {
		t.Fatalf("active pointer after failed CAS = %s, want A(%s) unchanged", got, a.ID)
	}

	// With the correct expected-active it promotes and supersedes A.
	if err := store.PromoteDeployment(ctx, r.ID, a.ID); err != nil {
		t.Fatalf("promote R with correct CAS: %v", err)
	}
	if n := countActive(t, ctx, pool, service, env); n != 1 {
		t.Fatalf("active count after rollback = %d, want 1", n)
	}
	if got := activePointer(t, ctx, pool, service, env); got != r.ID {
		t.Fatalf("active pointer = %s, want R(%s)", got, r.ID)
	}
}

func TestDeploymentStoreCronActivationIsAtomicAndRetrySafe(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, env := seedDeploymentScope(t, ctx, pool)
	store := api.NewPostgresDeploymentStore(pool)

	rec, err := store.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "registry.example/job@" + validDigest, ImageDigest: validDigest,
	})
	if err != nil {
		t.Fatalf("create cron deployment: %v", err)
	}
	if err := store.MarkDeploymentDeploying(ctx, rec.ID, deploy.DeploymentQueued); err != nil {
		t.Fatalf("mark cron deployment deploying: %v", err)
	}

	cfg := serviceconfig.Default(project, service, env)
	cfg.RuntimeKind = serviceconfig.RuntimeKindCron
	cfg.CronSchedule = "*/5 * * * *"
	cfg.StartCommand = "bin/job"
	cfg.RestartPolicy = serviceconfig.RestartNo
	cfg.HealthKind = serviceconfig.HealthNone
	cfg.Replicas = 1
	snapshot := serviceconfig.Snapshot{
		DeploymentID: rec.ID, ProjectID: project, ServiceID: service, EnvironmentID: env,
		Config: cfg, SHA256: strings.Repeat("d", 64),
	}
	configJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,config_version,
			effective_config,variable_versions,snapshot_sha256
		) VALUES ($1,$2,$3,$4,0,$5,'[]'::jsonb,$6)`,
		rec.ID, project, service, env, configJSON, snapshot.SHA256,
	); err != nil {
		t.Fatalf("insert cron snapshot: %v", err)
	}

	invalid := deploy.CronActivation{
		Candidate: runtime.ScheduleRequest{
			DeploymentID: rec.ID, ProjectID: project, ServiceID: service, EnvironmentID: env,
			Image: rec.ImageRef,
		},
		Snapshot: snapshot,
	}
	invalid.Snapshot.SHA256 = "invalid"
	if err := store.PromoteCronDeployment(ctx, rec.ID, "", invalid); err == nil {
		t.Fatal("invalid PromoteCronDeployment() error = nil")
	}
	if state, active := deploymentState(t, ctx, pool, rec.ID); state != "deploying" || active {
		t.Fatalf("deployment after rolled-back activation = (%s, active=%v), want deploying/false", state, active)
	}
	var activePointerCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM service_environment_state
		WHERE service_id=$1 AND environment_id=$2 AND active_deployment_id IS NOT NULL`,
		service, env,
	).Scan(&activePointerCount); err != nil {
		t.Fatal(err)
	}
	if activePointerCount != 0 {
		t.Fatalf("active pointer count after rolled-back activation = %d, want 0", activePointerCount)
	}
	var cursorCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cron_schedule_cursors WHERE deployment_id=$1`, rec.ID).Scan(&cursorCount); err != nil {
		t.Fatal(err)
	}
	if cursorCount != 0 {
		t.Fatalf("cursor count after rolled-back activation = %d, want 0", cursorCount)
	}

	activation := invalid
	activation.Snapshot = snapshot
	if err := store.PromoteCronDeployment(ctx, rec.ID, "", activation); err != nil {
		t.Fatalf("PromoteCronDeployment: %v", err)
	}
	if state, active := deploymentState(t, ctx, pool, rec.ID); state != "active" || !active {
		t.Fatalf("cron deployment = (%s, active=%v), want active/true", state, active)
	}
	if got := activePointer(t, ctx, pool, service, env); got != rec.ID {
		t.Fatalf("active pointer = %q, want %q", got, rec.ID)
	}
	var enabled bool
	var cursorDeployment, cursorSnapshot string
	if err := pool.QueryRow(ctx, `
		SELECT deployment_id,snapshot_sha256,enabled
		FROM cron_schedule_cursors
		WHERE project_id=$1 AND service_id=$2 AND environment_id=$3`,
		project, service, env,
	).Scan(&cursorDeployment, &cursorSnapshot, &enabled); err != nil {
		t.Fatalf("read cron cursor: %v", err)
	}
	if cursorDeployment != rec.ID || cursorSnapshot != snapshot.SHA256 || !enabled {
		t.Fatalf("cron cursor = (%q,%q,enabled=%v)", cursorDeployment, cursorSnapshot, enabled)
	}

	var cursorVersion int64
	if err := pool.QueryRow(ctx, `SELECT resource_version FROM cron_schedule_cursors WHERE deployment_id=$1`, rec.ID).Scan(&cursorVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteCronDeployment(ctx, rec.ID, "", activation); err != nil {
		t.Fatalf("matching retry PromoteCronDeployment() error = %v", err)
	}
	var cursorVersionAfterRetry int64
	if err := pool.QueryRow(ctx, `SELECT resource_version FROM cron_schedule_cursors WHERE deployment_id=$1`, rec.ID).Scan(&cursorVersionAfterRetry); err != nil {
		t.Fatal(err)
	}
	if cursorVersionAfterRetry != cursorVersion {
		t.Fatalf("matching retry changed cursor version %d -> %d", cursorVersion, cursorVersionAfterRetry)
	}
	mismatchedRetry := activation
	mismatchedRetry.Candidate.Image = "registry.example/job@" + validDigest2
	if err := store.PromoteCronDeployment(ctx, rec.ID, "", mismatchedRetry); !errors.Is(err, api.ErrDeploymentTransitionConflict) {
		t.Fatalf("mismatched retry PromoteCronDeployment() error = %v, want transition conflict", err)
	}
	var occurrenceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cron_occurrence_runs WHERE deployment_id=$1`, rec.ID).Scan(&occurrenceCount); err != nil {
		t.Fatal(err)
	}
	if occurrenceCount != 0 {
		t.Fatalf("occurrence count before first due window = %d, want 0", occurrenceCount)
	}

	recoveryNow := time.Date(2026, 7, 27, 19, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		UPDATE cron_schedule_cursors
		SET last_evaluated_at=$2
		WHERE deployment_id=$1`,
		rec.ID, recoveryNow.Add(-time.Hour),
	); err != nil {
		t.Fatal(err)
	}
	cronStore := deploy.NewPostgresCronStore(pool)
	cronExecutor := &deploy.CronExecutor{
		Store: cronStore, Runner: cronRunnerUnexpected{}, Variables: cronVariablesNoop{},
		Now: func() time.Time { return recoveryNow },
	}
	if err := cronExecutor.RunDue(ctx); err != nil {
		t.Fatalf("RunDue after expired outage window: %v", err)
	}
	var recoveredCursor time.Time
	if err := pool.QueryRow(ctx, `
		SELECT last_evaluated_at
		FROM cron_schedule_cursors
		WHERE deployment_id=$1`,
		rec.ID,
	).Scan(&recoveredCursor); err != nil {
		t.Fatal(err)
	}
	if !recoveredCursor.Equal(recoveryNow) {
		t.Fatalf("recovered cursor = %s, want %s", recoveredCursor, recoveryNow)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cron_occurrence_runs WHERE deployment_id=$1`, rec.ID).Scan(&occurrenceCount); err != nil {
		t.Fatal(err)
	}
	if occurrenceCount != 0 {
		t.Fatalf("expired outage replayed %d occurrences, want skip/no catch-up", occurrenceCount)
	}
}

func TestDeploymentInstanceReconcileFailsClosedAndRecoversReadiness(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, env := seedDeploymentScope(t, ctx, pool)
	deployments := api.NewPostgresDeploymentStore(pool)
	rec, err := deployments.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "registry.example/app@" + validDigest, ImageDigest: validDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	instances := deploy.NewInstanceStore(pool)
	instances.Now = func() time.Time { return time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC) }
	if _, err := instances.Ensure(ctx, rec.ID, 1); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	instanceID := deploy.ReplicaInstanceID(rec.ID, 0)
	target := runtime.RoutingTarget{
		DeploymentID: rec.ID, InstanceID: instanceID, ContainerID: "ctr-1",
		ContainerIP: "10.42.0.8", Ready: true,
	}
	if err := instances.Observe(ctx, target); err != nil {
		t.Fatalf("Observe ready: %v", err)
	}

	resolver := &instanceResolverFake{err: errors.New("runtime target unavailable")}
	if err := instances.ReconcileRuntime(ctx, resolver); err != nil {
		t.Fatalf("ReconcileRuntime unavailable: %v", err)
	}
	rows, err := instances.List(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Ready || rows[0].ContainerIP != "" || rows[0].ObservedState != "unavailable" {
		t.Fatalf("instance after resolve error = %+v, want unavailable/not-ready/no IP", rows)
	}

	resolver.err = nil
	resolver.target = target
	if err := instances.ReconcileRuntime(ctx, resolver); err != nil {
		t.Fatalf("ReconcileRuntime recovery: %v", err)
	}
	rows, err = instances.List(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].Ready || rows[0].ContainerIP != target.ContainerIP || rows[0].ObservedState != "running" {
		t.Fatalf("instance after runtime recovery = %+v, want running/ready", rows)
	}

	if err := instances.SetDeploymentState(ctx, rec.ID, "running", "starting"); err != nil {
		t.Fatalf("SetDeploymentState restart: %v", err)
	}
	rows, err = instances.List(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Ready || rows[0].ContainerIP != "" || rows[0].ObservedState != "starting" {
		t.Fatalf("instance after restart = %+v, want starting/not-ready/no stale IP", rows)
	}
}

type instanceResolverFake struct {
	target runtime.RoutingTarget
	err    error
}

func (r *instanceResolverFake) ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error) {
	return r.target, r.err
}

type cronRunnerUnexpected struct{}

func (cronRunnerUnexpected) RunOneOff(context.Context, runtime.OneOffRequest) (runtime.OneOffResult, error) {
	return runtime.OneOffResult{}, errors.New("expired cron outage unexpectedly executed an occurrence")
}

type cronVariablesNoop struct{}

func (cronVariablesNoop) Resolve(context.Context, string, string, string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestDeploymentStoreTransitionConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, env := seedDeploymentScope(t, ctx, pool)
	store := api.NewPostgresDeploymentStore(pool)

	d, _ := store.CreateDeployment(ctx, api.DeploymentRecord{
		ProjectID: project, ServiceID: service, EnvironmentID: env,
		Action: deploy.ActionDeployImage, ImageRef: "img:a", ImageDigest: validDigest,
	})
	// queued -> active is illegal (must pass through deploying); ValidateTransition rejects.
	err := store.PromoteDeployment(ctx, d.ID, "")
	if !errors.Is(err, api.ErrDeploymentTransitionConflict) {
		t.Fatalf("promote from queued err = %v, want ErrDeploymentTransitionConflict", err)
	}
	// A CAS transition against the wrong prior state affects zero rows.
	if err := store.MarkDeploymentDeploying(ctx, d.ID, deploy.DeploymentBuilding); !errors.Is(err, api.ErrDeploymentTransitionConflict) {
		t.Fatalf("deploying-from-building err = %v, want ErrDeploymentTransitionConflict", err)
	}
}
