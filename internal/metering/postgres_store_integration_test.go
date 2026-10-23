//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package metering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresUsageReconciliationReplayAndTenantBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := startMeteringPostgres(t, ctx)
	defer pool.Close()
	now := time.Date(2026, 8, 13, 12, 0, 30, 0, time.UTC)
	currentNow := now
	seedMeteringScope(t, ctx, pool, now)
	store := NewPostgresStore(pool)
	store.Now = func() time.Time { return currentNow }
	query := SummaryQuery{WorkspaceID: "wrk_usage", ActorUserID: "usr_usage",
		WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Minute)}
	scheduled, err := store.ReconcileScheduled(ctx, ScheduledReconcileRequest{
		Since: now.Add(-time.Hour), Until: now.Add(time.Minute), MaxWorkspaces: 10, StageTimeout: time.Second,
	})
	if err != nil || scheduled.WorkspacesExamined < 1 {
		t.Fatalf("scheduled reconciliation=%+v err=%v", scheduled, err)
	}
	var scheduledCheckpoints int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_source_cursors
		WHERE workspace_id='wrk_usage' AND source_instance=$1`, scheduledReconcileSourceInstance).Scan(&scheduledCheckpoints); err != nil || scheduledCheckpoints != 3 {
		t.Fatalf("scheduled checkpoints=%d err=%v", scheduledCheckpoints, err)
	}
	var openBuildGaps int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_evidence_gaps WHERE workspace_id='wrk_usage' AND source='build_lifecycle' AND source_instance='dep_usage_legacy' AND resolved_at IS NULL`).Scan(&openBuildGaps); err != nil || openBuildGaps != 1 {
		t.Fatalf("open legacy build gaps=%d err=%v", openBuildGaps, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE deployments SET build_started_at=$2,build_completed_at=$3 WHERE id=$1`, "dep_usage_legacy", now.Add(-45*time.Second), now.Add(-25*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileScheduled(ctx, ScheduledReconcileRequest{Since: now.Add(-time.Hour), Until: now.Add(time.Minute), MaxWorkspaces: 10, StageTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_evidence_gaps WHERE workspace_id='wrk_usage' AND source='build_lifecycle' AND source_instance='dep_usage_legacy' AND resolved_at IS NULL`).Scan(&openBuildGaps); err != nil || openBuildGaps != 0 {
		t.Fatalf("unresolved repaired build gaps=%d err=%v", openBuildGaps, err)
	}

	firstStarted := time.Now()
	first, err := store.Summary(ctx, query)
	firstElapsed := time.Since(firstStarted)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bounded cold summary completed in %v", firstElapsed)
	if firstElapsed >= 2*time.Second {
		t.Fatalf("bounded cold summary took %v, want <2s", firstElapsed)
	}
	assertIntegrationUsage(t, first, ResourceProjectCount, 1, CoverageComplete)
	assertIntegrationUsage(t, first, ResourceVolumeCount, 1, CoverageComplete)
	assertIntegrationUsage(t, first, ResourceBuildSeconds, 50, CoveragePartial)
	assertIntegrationUsage(t, first, ResourcePublicHTTPRequests, 1, CoveragePartial)
	assertIntegrationUsage(t, first, ResourcePublicHTTPResponseBytes, 40, CoveragePartial)
	assertIntegrationUsage(t, first, ResourceCPUSeconds, 1, CoveragePartial)
	assertIntegrationUsage(t, first, ResourceMemoryByteSeconds, 10000, CoveragePartial)
	var summaryCheckpoints int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_source_cursors
		WHERE workspace_id='wrk_usage' AND source_instance=$1`, summaryReconcileSourceInstance).Scan(&summaryCheckpoints); err != nil || summaryCheckpoints != 3 {
		t.Fatalf("summary checkpoints=%d err=%v", summaryCheckpoints, err)
	}
	warmStarted := time.Now()
	warm, err := store.Summary(ctx, query)
	warmElapsed := time.Since(warmStarted)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("checkpointed warm summary completed in %v", warmElapsed)
	if warmElapsed >= 2*time.Second || warm.ReplayAfter != first.ReplayAfter {
		t.Fatalf("warm summary elapsed=%v replay=%d/%d", warmElapsed, warm.ReplayAfter, first.ReplayAfter)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at)
        VALUES ('prj_usage_two','wrk_usage','two','Two',$1,$1)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Summary(ctx, query); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET deleted_at=$1 WHERE id='prj_usage_two'`, now); err != nil {
		t.Fatal(err)
	}
	third, err := store.Summary(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	assertIntegrationUsage(t, third, ResourceProjectCount, 1, CoverageComplete)
	rows, err := pool.Query(ctx, `SELECT quantity FROM usage_events WHERE workspace_id='wrk_usage'
        AND dimension='project_count' ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := []int64{}
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		got = append(got, value)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 1 {
		t.Fatalf("gauge transitions=%v", got)
	}
	if _, err := store.Summary(ctx, SummaryQuery{WorkspaceID: "wrk_usage", ActorUserID: "usr_other",
		WindowStart: query.WindowStart, WindowEnd: query.WindowEnd}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("cross-tenant error=%v", err)
	}
	producerEvent := LedgerEvent{EventID: "producer-event-one", Source: "resource_reconciler",
		SourceInstance: "wrk_usage", SourceEventID: "manual-transition:1", WorkspaceID: "wrk_usage",
		Resource: ResourceProjectCount, Quantity: 1, Kind: ValueGauge, ObservedAt: now}
	if _, err := store.Append(ctx, producerEvent); err != nil {
		t.Fatal(err)
	}
	producerEvent.Quantity = 2
	if _, err := store.Append(ctx, producerEvent); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting producer replay err=%v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE usage_policy_limits SET limit_value=1,enforceable=TRUE
		WHERE profile_key='self_hosted' AND dimension='project_count'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET deleted_at=NULL WHERE id='prj_usage_two'`); err == nil || !strings.Contains(err.Error(), "MIDNIGHT_USAGE_LIMIT_EXCEEDED") { // not-a-failure-code: internal PostgreSQL trigger sentinel.
		t.Fatalf("soft-delete restore bypassed count policy: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE usage_policy_limits SET limit_value=3,enforceable=TRUE
		WHERE profile_key='self_hosted' AND dimension='service_count'`); err != nil {
		t.Fatal(err)
	}
	reservationRequest := ReservationRequest{WorkspaceID: "wrk_usage", ActorUserID: "usr_usage",
		Resource: ResourceServiceCount, Quantity: 1, IdempotencyKey: "reserve-service-one",
		IntentHash: strings.Repeat("a", 64)}
	firstReservation, err := store.Reserve(ctx, reservationRequest)
	if err != nil || firstReservation.IdempotentReplay {
		t.Fatalf("first reservation=%+v err=%v", firstReservation, err)
	}
	replayReservation, err := store.Reserve(ctx, reservationRequest)
	if err != nil || !replayReservation.IdempotentReplay || replayReservation.ID != firstReservation.ID {
		t.Fatalf("replay reservation=%+v err=%v", replayReservation, err)
	}
	var decisions int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_admission_decisions WHERE reservation_id=$1`, firstReservation.ID).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("replay decision amplification count=%d err=%v", decisions, err)
	}
	reservationRequest.Quantity = 2
	if _, err := store.Reserve(ctx, reservationRequest); !errors.Is(err, ErrReservationConflict) {
		t.Fatalf("mismatched replay err=%v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE usage_policy_limits SET limit_value=2,enforceable=TRUE
		WHERE profile_key='self_hosted' AND dimension='volume_count'`); err != nil {
		t.Fatal(err)
	}
	var volumeReservation Reservation
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		var enforced bool
		var err error
		volumeReservation, enforced, err = store.ReserveIfEnforcedInTx(ctx, tx, ReservationRequest{
			WorkspaceID: "wrk_usage", ActorUserID: "usr_usage", Resource: ResourceVolumeCount,
			Quantity: 1, IdempotencyKey: "volume-two", IntentHash: strings.Repeat("b", 64),
		})
		if err != nil || !enforced {
			return fmt.Errorf("reserve volume: enforced=%t err=%v", enforced, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO volumes (id,project_id,environment_id,service_id,name,status)
			VALUES ('vol_usage_two','prj_usage','env_usage','svc_usage','cache','ready')`); err != nil {
			return err
		}
		return store.CommitReservationInTx(ctx, tx, volumeReservation, now)
	}); err != nil {
		t.Fatal(err)
	}
	var reservationState string
	if err := pool.QueryRow(ctx, `SELECT state FROM usage_reservations WHERE id=$1`, volumeReservation.ID).Scan(&reservationState); err != nil || reservationState != "committed" {
		t.Fatalf("volume reservation state=%q err=%v", reservationState, err)
	}
	if err := pool.WithTx(ctx, func(tx *pg.Tx) error {
		_, enforced, err := store.ReserveIfEnforcedInTx(ctx, tx, ReservationRequest{
			WorkspaceID: "wrk_usage", ActorUserID: "usr_usage", Resource: ResourceVolumeCount,
			Quantity: 1, IdempotencyKey: "volume-three", IntentHash: strings.Repeat("c", 64),
		})
		var limitErr *LimitError
		if !enforced || !errors.As(err, &limitErr) {
			return fmt.Errorf("volume denial: enforced=%t err=%v", enforced, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// An unavailable producer table must not turn an interactive summary into
	// an unbounded request. The HTTP stage times out independently; durable
	// gauges, existing ledger evidence, and runtime reconciliation still return
	// with explicit incomplete coverage.
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blockTx, err := blocker.Begin(ctx)
	if err != nil {
		blocker.Release()
		t.Fatal(err)
	}
	if _, err := blockTx.Exec(ctx, `LOCK TABLE observability_log_records IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = blockTx.Rollback(ctx)
		blocker.Release()
		t.Fatal(err)
	}
	store.SummaryReconcileBudget = 75 * time.Millisecond
	currentNow = now.Add(2 * time.Minute)
	blockedQuery := query
	blockedQuery.WindowEnd = currentNow
	started := time.Now()
	bounded, summaryErr := store.Summary(ctx, blockedQuery)
	elapsed := time.Since(started)
	if err := blockTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	blocker.Release()
	if summaryErr != nil {
		t.Fatalf("bounded summary: %v", summaryErr)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("bounded summary took %v, want <2s", elapsed)
	}
	t.Logf("lock-blocked HTTP reconciliation returned an honest partial summary in %v", elapsed)
	var httpCoverage Coverage
	for _, line := range bounded.Usage {
		if line.Resource == ResourcePublicHTTPRequests {
			httpCoverage = line.Coverage
			break
		}
	}
	if httpCoverage.MissingIntervals < 1 || !strings.Contains(httpCoverage.Note, "bounded time budget") {
		t.Fatalf("bounded HTTP coverage=%+v elapsed=%v", httpCoverage, elapsed)
	}
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	down, err := runner.Down(ctx)
	if err != nil || down == nil || down.Version != 69 {
		t.Fatalf("down migration 69=%#v err=%v", down, err)
	}
	up, err := runner.Up(ctx)
	if err != nil || len(up) != 1 || up[0].Version != 69 {
		t.Fatalf("reapply migration 69=%#v err=%v", up, err)
	}
}

func assertIntegrationUsage(t *testing.T, summary Summary, resource Resource, used int64, coverage CoverageStatus) {
	t.Helper()
	for _, line := range summary.Usage {
		if line.Resource == resource {
			if line.Used != used || line.Coverage.Status != coverage {
				t.Fatalf("%s=%+v", resource, line)
			}
			return
		}
	}
	t.Fatalf("missing %s", resource)
}

func startMeteringPostgres(t *testing.T, ctx context.Context) *pg.Pool {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"), tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "docker") {
			t.Skipf("docker unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pg.Open(ctx, pg.PoolConfig{DSN: dsn, MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func seedMeteringScope(t *testing.T, ctx context.Context, pool *pg.Pool, now time.Time) {
	t.Helper()
	statements := []string{
		`INSERT INTO users (id,email,display_name,session_generation,created_at,updated_at) VALUES
            ('usr_usage','usage@example.com','Usage',1,$1,$1),('usr_other','other@example.com','Other',1,$1,$1)`,
		`INSERT INTO workspaces (id,slug,display_name,created_at,updated_at) VALUES ('wrk_usage','usage','Usage',$1,$1)`,
		`INSERT INTO workspace_members (workspace_id,user_id,role,invited_at,accepted_at,created_at,updated_at)
            VALUES ('wrk_usage','usr_usage','member',$1,$1,$1,$1)`,
		`INSERT INTO projects (id,workspace_id,slug,display_name,created_at,updated_at)
            VALUES ('prj_usage','wrk_usage','usage','Usage',$1,$1)`,
		`INSERT INTO services (id,project_id,slug,display_name,created_at,updated_at)
            VALUES ('svc_usage','prj_usage','web','Web',$1,$1)`,
		`INSERT INTO environments (id,project_id,slug,name,tier,created_at,updated_at)
            VALUES ('env_usage','prj_usage','production','Production','production',$1,$1)`,
		`INSERT INTO volumes (id,project_id,environment_id,service_id,name,status,created_at,updated_at)
			VALUES ('vol_usage','prj_usage','env_usage','svc_usage','data','ready',$1,$1)`,
		`INSERT INTO deployments (id,project_id,service_id,environment_id,action,source_type,state,created_at,updated_at)
			VALUES ('dep_usage','prj_usage','svc_usage','env_usage','deploy_source','git','queued',$1::timestamptz-INTERVAL '1 minute',$1::timestamptz-INTERVAL '1 minute')`,
		`UPDATE deployments SET state='building',started_at=$1::timestamptz-INTERVAL '50 seconds',
			build_started_at=$1::timestamptz-INTERVAL '50 seconds',updated_at=$1::timestamptz-INTERVAL '50 seconds'
			WHERE id='dep_usage'`,
		`UPDATE deployments SET state='deploying',sha=repeat('a',40),builder='railpack',image_ref='registry.test/usage',
			image_digest='sha256:'||repeat('b',64),build_completed_at=$1::timestamptz-INTERVAL '20 seconds',
			updated_at=$1::timestamptz-INTERVAL '20 seconds' WHERE id='dep_usage'`,
		`INSERT INTO deployments (id,project_id,service_id,environment_id,action,source_type,state,created_at,updated_at)
			VALUES ('dep_usage_legacy','prj_usage','svc_usage','env_usage','deploy_source','git','queued',$1::timestamptz-INTERVAL '1 minute',$1::timestamptz-INTERVAL '1 minute')`,
		`UPDATE deployments SET state='building',started_at=$1::timestamptz-INTERVAL '45 seconds',updated_at=$1::timestamptz-INTERVAL '45 seconds'
			WHERE id='dep_usage_legacy'`,
		`UPDATE deployments SET state='deploying',sha=repeat('c',40),builder='railpack',image_ref='registry.test/legacy',
			image_digest='sha256:'||repeat('d',64),updated_at=$1::timestamptz-INTERVAL '25 seconds' WHERE id='dep_usage_legacy'`,
		`INSERT INTO observability_log_records (observed_at,project_id,environment_id,service_id,surface,stream,level,component,message,attributes,source_key,source_sequence)
            VALUES ($1::timestamptz-INTERVAL '20 seconds','prj_usage','env_usage','svc_usage','http','access','info','traefik','GET / 200','{"rx_bytes":60,"tx_bytes":40}','traefik-access:test','one')`,
		`INSERT INTO observability_metric_samples (observed_at,project_id,environment_id,service_id,replica_id,source,sample_resolution,cpu_percent,memory_bytes)
			VALUES ($1::timestamptz-INTERVAL '20 seconds','prj_usage','env_usage','svc_usage','replica','docker','raw',10,1000),
			       ($1::timestamptz-INTERVAL '10 seconds','prj_usage','env_usage','svc_usage','replica','docker','raw',10,1000)`,
		`INSERT INTO observability_ingestion_windows
			(source_key,window_started_at,project_id,environment_id,service_id,accepted_count,dropped_count)
			VALUES ('traefik-access:test',date_trunc('minute',$1::timestamptz),'prj_usage','env_usage','svc_usage',1,1)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement, now); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}
}
