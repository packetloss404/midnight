//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package audit_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
	migrations "github.com/midnight/midnight/migrations"
)

// startPostgres spins up a real Postgres 16 container, returns the
// container handle + DSN. Skips when Docker is unavailable so
// developers running on hosts without Docker get a useful diagnostic
// instead of a panic.
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

func runMigrations(t *testing.T, ctx context.Context, pool *pg.Pool) {
	t.Helper()
	runner := &pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("migrations up: %v", err)
	}
}

// TestPostgresAuditChainPersistsAcrossRestart re-introduces the test
// dropped during the Lane 1 / Lane 4 merge (per
// agile/handoff/phase1-subagents/MERGE_RECONCILIATION.md "Lane 1's
// narrower draft cannot coexist with Lane 4's canonical design"). It
// asserts that:
//
//  1. Rows append into the Postgres-backed chain with monotonically
//     increasing Sequence + chain-correct prev_hash / row_hash.
//  2. After a process restart (closing + reopening the pool) the
//     chain still verifies cleanly through audit.Verify.
//  3. A direct UPDATE that mutates a row's payload is detected as
//     OutcomeChainBreak — proving tamper detection works against the
//     real Postgres seam.
func TestPostgresAuditChainPersistsAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)

	chain := audit.NewPostgresChain(pool)
	for i := 0; i < 5; i++ {
		projectID := "prj_a"
		if i == 2 {
			projectID = "prj_b"
		}
		_, err := chain.Append(ctx, audit.Row{
			ResourceKind: "deployment",
			Action:       "create",
			RequestID:    "req-pg",
			ProjectID:    projectID,
			Attrs:        map[string]any{"i": int64(i)},
		})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	// Verify is OK on the live chain.
	report, err := audit.Verify(ctx, audit.SourceFromChain{Chain: chain}, nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.Outcome != audit.OutcomeOK {
		t.Fatalf("verify outcome = %s, breaks = %+v", report.Outcome, report.Breaks)
	}
	if report.RowsChecked != 5 {
		t.Fatalf("rows checked = %d, want 5", report.RowsChecked)
	}

	// Sign + persist a checkpoint and verify it round-trips through
	// Postgres without a signature break. Postgres TIMESTAMPTZ is
	// microsecond-precision, so the checkpointer's clock must
	// truncate to microseconds before signing — otherwise the
	// signature is computed over nanosecond-precision timestamps
	// that the Postgres SELECT cannot return.
	key, err := audit.GenerateSigningKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	cpr := audit.NewCheckpointer(chain, key)
	cpr.SetClock(func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) })
	if _, err := cpr.Sign(ctx); err != nil {
		t.Fatalf("sign checkpoint: %v", err)
	}

	// Restart simulation: drop pool, reopen, build a new chain.
	pool.Close()
	pool2 := newPool(t, ctx, dsn)
	chain2 := audit.NewPostgresChain(pool2)

	report2, err := audit.Verify(ctx, audit.SourceFromChain{Chain: chain2}, []audit.VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil {
		t.Fatalf("verify after restart: %v", err)
	}
	if report2.Outcome != audit.OutcomeOK {
		t.Fatalf("verify after restart outcome = %s, breaks=%+v", report2.Outcome, report2.Breaks)
	}
	if report2.RowsChecked != 5 || report2.CheckpointsChecked != 1 {
		t.Fatalf("counts after restart = rows %d cps %d", report2.RowsChecked, report2.CheckpointsChecked)
	}
	projectRows, err := chain2.ReadProject(ctx, audit.ProjectQuery{ProjectID: "prj_a", Limit: 2})
	if err != nil {
		t.Fatalf("read project page 1: %v", err)
	}
	if len(projectRows) != 2 || projectRows[0].Sequence != 5 || projectRows[1].Sequence != 4 {
		t.Fatalf("project page 1 = %#v", projectRows)
	}
	projectRows, err = chain2.ReadProject(ctx, audit.ProjectQuery{ProjectID: "prj_a", BeforeID: 4, Limit: 2})
	if err != nil {
		t.Fatalf("read project page 2: %v", err)
	}
	if len(projectRows) != 2 || projectRows[0].Sequence != 2 || projectRows[1].Sequence != 1 {
		t.Fatalf("project page 2 = %#v", projectRows)
	}

	// Tamper test: edit a row in place. The verifier must report
	// OutcomeChainBreak.
	if _, err := pool2.Exec(ctx, `UPDATE audit_logs SET action = 'tampered' WHERE id = $1`, int64(3)); err != nil {
		t.Fatalf("tamper UPDATE: %v", err)
	}
	report3, err := audit.Verify(ctx, audit.SourceFromChain{Chain: chain2}, []audit.VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil {
		t.Fatalf("verify after tamper: %v", err)
	}
	if report3.Outcome != audit.OutcomeChainBreak {
		t.Fatalf("verify after tamper outcome = %s, want chain_break; breaks=%+v", report3.Outcome, report3.Breaks)
	}
	foundOffending := false
	for _, b := range report3.Breaks {
		if b.Sequence == 3 && b.Kind == "row_hash" {
			foundOffending = true
		}
	}
	if !foundOffending {
		t.Fatalf("expected row_hash break at row 3; got %+v", report3.Breaks)
	}
}

// TestPostgresAuditChainSerializeAppends asserts concurrent Append
// calls serialize through the panel-locked advisory lock with no lost
// rows or sequence gaps.
func TestPostgresAuditChainSerializeAppends(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	chain := audit.NewPostgresChain(pool)

	const writers = 8
	const perWriter = 4
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		go func(id int) {
			for j := 0; j < perWriter; j++ {
				_, err := chain.Append(ctx, audit.Row{
					ResourceKind: "deployment",
					Action:       "create",
					RequestID:    "req-concurrent",
					Attrs:        map[string]any{"writer": int64(id), "j": int64(j)},
				})
				if err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}(w)
	}
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}

	rows, err := chain.Read(ctx, audit.Query{})
	if err != nil {
		t.Fatalf("read after concurrent appends: %v", err)
	}
	if len(rows) != writers*perWriter {
		t.Fatalf("rows = %d, want %d", len(rows), writers*perWriter)
	}
	for i, r := range rows {
		if r.Sequence != int64(i+1) {
			t.Fatalf("row[%d].Sequence = %d, want %d", i, r.Sequence, i+1)
		}
	}
	report, err := audit.Verify(ctx, audit.SourceFromChain{Chain: chain}, nil)
	if err != nil || report.Outcome != audit.OutcomeOK {
		t.Fatalf("verify after concurrent appends: outcome=%s err=%v breaks=%+v", report.Outcome, err, report.Breaks)
	}
}
