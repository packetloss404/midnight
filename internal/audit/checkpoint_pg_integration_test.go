//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit_test

import (
	"bytes"
	"context"
	"errors"
	tc "github.com/testcontainers/testcontainers-go"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/db/pg"
)

func startCheckpointPostgres(t *testing.T, ctx context.Context) (tc.Container, string) {
	container, dsn := startPostgres(t, ctx)
	// Containerized local QA can bypass Docker Desktop's host port forwarding.
	// CI's normal host-based path is unchanged and both paths use real Postgres.
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
	return container, dsn
}

func TestPostgresScheduledAuditCheckpointsSurviveRestartAndConcurrentWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	container, dsn := startCheckpointPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	chain := audit.NewPostgresChain(pool)
	appendRows := func(chain *audit.PostgresChain, count int) {
		t.Helper()
		for range count {
			if _, err := chain.Append(ctx, audit.Row{ActorType: "system", ResourceKind: "audit", Action: "verification.fixture"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	appendRows(chain, 5)
	path := filepath.Join(t.TempDir(), "private", "signing-key.pem")
	key, err := audit.LoadOrCreateSigningKey(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	publicPath, err := audit.PublishVerifyingKey(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := audit.LoadVerifyingKeyFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 23, 59, 59, 123456789, time.UTC)
	first := audit.NewCheckpointer(chain, key)
	first.SetClock(func() time.Time { return now })
	runCtx, stop := context.WithCancel(ctx)
	created := false
	if err := (audit.CheckpointSchedule{Checkpointer: first, Now: func() time.Time { return now }, Report: func(cp audit.Checkpoint, fresh bool, err error) {
		if err != nil {
			t.Errorf("scheduled checkpoint: %v", err)
		}
		created = fresh
	}, Wait: func(ctx context.Context, _ time.Duration) error { stop(); return ctx.Err() }}).Run(runCtx); err != nil || !created {
		t.Fatalf("installed schedule created=%t err=%v", created, err)
	}
	pool.Close()
	pool = newPool(t, ctx, dsn)
	chain = audit.NewPostgresChain(pool)
	reloaded, err := audit.LoadOrCreateSigningKey(ctx, path, false)
	if err != nil || reloaded.Fingerprint != key.Fingerprint {
		t.Fatalf("restart key continuity err=%v", err)
	}
	appendRows(chain, 3)
	restarted := audit.NewCheckpointer(chain, reloaded)
	restarted.SetClock(func() time.Time { return now })
	if _, fresh, err := restarted.SignDue(ctx); err != nil || fresh {
		t.Fatalf("same UTC day restart fresh=%t err=%v", fresh, err)
	}
	now = now.Add(72 * time.Hour)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := audit.NewCheckpointer(audit.NewPostgresChain(pool), reloaded)
			worker.SetClock(func() time.Time { return now })
			_, fresh, err := worker.SignDue(ctx)
			if err != nil {
				t.Error(err)
			}
			if fresh {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent daily checkpoints=%d", successes.Load())
	}
	checkpoints, err := chain.ListCheckpoints(ctx)
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("checkpoint count=%d err=%v", len(checkpoints), err)
	}
	if checkpoints[0].FirstRowID != 1 || checkpoints[0].LastRowID != 5 || checkpoints[1].FirstRowID != 6 || checkpoints[1].LastRowID != 8 || checkpoints[1].PrevCheckpointID != checkpoints[0].ID {
		t.Fatalf("overlapping/noncontiguous coverage=%#v", checkpoints)
	}
	stale := checkpoints[1]
	stale.PrevCheckpointID = 0
	if _, err := chain.InsertCheckpoint(ctx, stale); !errors.Is(err, audit.ErrCheckpointConflict) {
		t.Fatalf("stale predecessor accepted: %v", err)
	}
	for _, cp := range checkpoints {
		if cp.EndedAt.Nanosecond()%1000 != 0 {
			t.Fatal("signature signed timestamp precision lost by Postgres")
		}
		if err := audit.VerifyCheckpoint(cp, trusted); err != nil {
			t.Fatal(err)
		}
	}
	dst := audit.NewMemoryDestination("scheduled-export")
	manifest, err := audit.NewExporter(chain).Export(ctx, dst)
	if err != nil || manifest.CheckpointCount != 2 {
		t.Fatalf("export checkpoints=%d err=%v", manifest.CheckpointCount, err)
	}
	if bytes.Contains(dst.Bytes(), []byte("PRIVATE KEY")) {
		t.Fatal("export exposed private key")
	}
	src, err := audit.NewJSONLSourceFromReader(bytes.NewReader(dst.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	report, err := audit.Verify(ctx, src, []audit.VerifyingKey{trusted})
	if err != nil || !report.Valid || report.SignatureStatus != "verified" || report.SignedThroughSequence != 8 {
		t.Fatalf("signed export report=%#v err=%v", report, err)
	}
	wrong, _ := audit.GenerateSigningKey()
	report, err = audit.Verify(ctx, src, []audit.VerifyingKey{{Public: wrong.Public, Fingerprint: wrong.Fingerprint}})
	if err != nil || report.Valid || report.Outcome != audit.OutcomeSignatureBreak {
		t.Fatalf("wrong trust accepted=%#v err=%v", report, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.LoadOrCreateSigningKey(ctx, path, false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lost history key regenerated: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_checkpoints SET signature=set_byte(signature,0,get_byte(signature,0)#1) WHERE id=$1`, checkpoints[1].ID); err != nil {
		t.Fatal(err)
	}
	report, err = audit.Verify(ctx, audit.SourceFromChain{Chain: chain}, []audit.VerifyingKey{trusted})
	if err != nil || report.Valid || report.Outcome != audit.OutcomeSignatureBreak {
		t.Fatalf("tampered checkpoint accepted=%#v err=%v", report, err)
	}
}

func TestPostgresAuditCheckpointLockWaitIsBounded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, dsn := startCheckpointPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	chain := audit.NewPostgresChain(pool)
	head, err := chain.Append(ctx, audit.Row{ActorType: "system", ResourceKind: "audit", Action: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := audit.GenerateSigningKey()
	locked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	finished := make(chan error, 1)
	go func() {
		finished <- pool.WithTx(ctx, func(tx *pg.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(4_911_374_188_220_140_519)); err != nil {
				return err
			}
			close(locked)
			<-release
			now := time.Now().UTC().Truncate(time.Microsecond)
			cp, err := audit.SignCheckpoint(audit.Checkpoint{ID: 1, PartitionLabel: now.Format("2006-01"), StartedAt: now, EndedAt: now, FirstRowID: head.Sequence, LastRowID: head.Sequence, LastRowHash: head.RowHash}, key)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO audit_checkpoints(id,partition_label,started_at,ended_at,first_row_id,last_row_id,last_row_hash,signature_alg,signing_key_fingerprint,signature) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, cp.ID, cp.PartitionLabel, cp.StartedAt, cp.EndedAt, cp.FirstRowID, cp.LastRowID, cp.LastRowHash, cp.SignatureAlg, cp.SigningKeyFingerprint, cp.Signature)
			return err
		})
	}()
	select {
	case <-locked:
	case err := <-finished:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	bounded, stop := context.WithTimeout(ctx, 75*time.Millisecond)
	defer stop()
	var clockCalls atomic.Int32
	checkpointer := audit.NewCheckpointer(chain, key)
	checkpointer.SetClock(func() time.Time { clockCalls.Add(1); return time.Now().UTC() })
	start := time.Now()
	_, _, err = checkpointer.SignDue(bounded)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("lock cancellation=%v duration=%s", err, time.Since(start))
	}
	missingPath := filepath.Join(t.TempDir(), "private", "signing-key.pem")
	initCtx, cancelInit := context.WithTimeout(ctx, 75*time.Millisecond)
	_, initErr := chain.InitializeSigningKey(initCtx, missingPath)
	cancelInit()
	if !errors.Is(initErr, context.DeadlineExceeded) {
		t.Fatalf("key initialization bypassed checkpoint lock: %v", initErr)
	}
	if _, err := os.Stat(missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("key was published before locked history proof: %v", err)
	}
	unblock()
	if lockErr := <-finished; lockErr != nil {
		t.Fatal(lockErr)
	}
	if clockCalls.Load() != 0 {
		t.Fatal("signing timestamp was captured before acquiring chain lock")
	}
	checkpoints, err := chain.ListCheckpoints(ctx)
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("unexpected checkpoint count=%d err=%v", len(checkpoints), err)
	}
	if _, err := chain.InitializeSigningKey(ctx, missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history committed while waiting allowed key regeneration: %v", err)
	}
}

func TestPostgresAuditExportSnapshotDuringConcurrentCheckpointing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, dsn := startCheckpointPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	chain := audit.NewPostgresChain(pool)
	key, _ := audit.GenerateSigningKey()
	if _, err := chain.Append(ctx, audit.Row{ActorType: "system", ResourceKind: "audit", Action: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.NewCheckpointer(chain, key).Sign(ctx); err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	go func() {
		for range 20 {
			if _, err := chain.Append(ctx, audit.Row{ActorType: "system", ResourceKind: "audit", Action: "fixture"}); err != nil {
				writer <- err
				return
			}
			if _, err := audit.NewCheckpointer(chain, key).Sign(ctx); err != nil {
				writer <- err
				return
			}
		}
		writer <- nil
	}()
	for range 20 {
		dst := audit.NewMemoryDestination("concurrent-snapshot")
		if _, err := audit.NewExporter(chain).Export(ctx, dst); err != nil {
			t.Fatal(err)
		}
		src, err := audit.NewJSONLSourceFromReader(bytes.NewReader(dst.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		report, err := audit.Verify(ctx, src, []audit.VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
		if err != nil || !report.Valid {
			t.Fatalf("inconsistent export=%#v err=%v", report, err)
		}
	}
	if err := <-writer; err != nil {
		t.Fatal(err)
	}
}
