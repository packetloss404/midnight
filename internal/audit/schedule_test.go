// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDailyCheckpointsUTCReplayCatchupAndConcurrentWorkers(t *testing.T) {
	chain := NewMemoryChain()
	key, _ := GenerateSigningKey()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	local := time.FixedZone("operator-local", -5*60*60)
	now := time.Date(2026, 10, 27, 18, 59, 59, 123456789, local)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cp := NewCheckpointer(chain, key)
			cp.SetClock(func() time.Time { return now })
			_, fresh, err := cp.SignDue(context.Background())
			if err != nil {
				t.Error(err)
			}
			if fresh {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("concurrent checkpoints=%d", created)
	}
	first, _ := chain.LatestCheckpoint(context.Background())
	if first.EndedAt.Location() != time.UTC || first.EndedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("noncanonical persisted time=%s", first.EndedAt)
	}
	_, _ = chain.Append(context.Background(), newTestRow(1))
	restarted := NewCheckpointer(chain, key)
	restarted.SetClock(func() time.Time { return now })
	if _, fresh, err := restarted.SignDue(context.Background()); err != nil || fresh {
		t.Fatalf("same-day replay fresh=%t err=%v", fresh, err)
	}
	now = now.Add(48 * time.Hour) // missed boundaries are caught up at actual time.
	second, fresh, err := restarted.SignDue(context.Background())
	if err != nil || !fresh || second.ID != 2 || second.FirstRowID != 2 || second.LastRowID != 2 || second.PrevCheckpointID != first.ID || !second.EndedAt.Equal(checkpointTime(now)) {
		t.Fatalf("catch-up checkpoint=%#v fresh=%t err=%v", second, fresh, err)
	}
	report, err := Verify(context.Background(), SourceFromChain{Chain: chain}, []VerifyingKey{{Public: key.Public, Fingerprint: key.Fingerprint}})
	if err != nil || !report.Valid || report.SignatureStatus != "verified" || report.SignedThroughSequence != 2 {
		t.Fatalf("signed chain=%#v err=%v", report, err)
	}
}

func TestCheckpointScheduleWaitsForUTCMidnightAndCancels(t *testing.T) {
	chain := NewMemoryChain()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	key, _ := GenerateSigningKey()
	now := time.Date(2026, 10, 27, 23, 59, 59, 0, time.UTC)
	cp := NewCheckpointer(chain, key)
	cp.SetClock(func() time.Time { return now })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passes := 0
	err := (CheckpointSchedule{Checkpointer: cp, Now: func() time.Time { return now }, Report: func(_ Checkpoint, fresh bool, err error) {
		passes++
		if err != nil || !fresh {
			t.Fatalf("scheduled pass fresh=%t err=%v", fresh, err)
		}
	}, Wait: func(ctx context.Context, delay time.Duration) error {
		if passes == 1 {
			if delay != time.Second {
				t.Fatalf("UTC midnight delay=%s", delay)
			}
			now = now.Add(delay)
			_, _ = chain.Append(ctx, newTestRow(1))
			return nil
		}
		cancel()
		return ctx.Err()
	}}).Run(ctx)
	if err != nil || passes != 2 {
		t.Fatalf("schedule passes=%d err=%v", passes, err)
	}
}

type blockedCheckpointChain struct{ *MemoryChain }

func (blockedCheckpointChain) SignAuditCheckpoint(ctx context.Context, _ SigningKey, _ func() time.Time, _ bool) (Checkpoint, bool, error) {
	<-ctx.Done()
	return Checkpoint{}, false, ctx.Err()
}

func TestCheckpointScheduleBoundsBlockedPassAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	key, _ := GenerateSigningKey()
	start := time.Now()
	reported := false
	err := (CheckpointSchedule{Checkpointer: NewCheckpointer(blockedCheckpointChain{NewMemoryChain()}, key), Timeout: 20 * time.Millisecond, Report: func(_ Checkpoint, _ bool, err error) {
		reported = true
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("bounded pass=%v", err)
		}
		cancel()
	}}).Run(ctx)
	if err != nil || !reported || time.Since(start) > time.Second {
		t.Fatalf("bounded cancellation reported=%t err=%v duration=%s", reported, err, time.Since(start))
	}
}

func TestCheckpointInsertRejectsStalePredecessorAndBackwardsClock(t *testing.T) {
	chain := NewMemoryChain()
	key, _ := GenerateSigningKey()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	now := time.Now().UTC()
	cp := NewCheckpointer(chain, key)
	cp.SetClock(func() time.Time { return now })
	first, err := cp.Sign(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = chain.Append(context.Background(), newTestRow(1))
	stale := first
	stale.LastRowID = 2
	if _, err := chain.InsertCheckpoint(context.Background(), stale); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("stale checkpoint insert=%v", err)
	}
	now = now.Add(-time.Hour)
	if _, err := cp.Sign(context.Background()); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("backwards signing interval=%v", err)
	}
}

func TestCheckpointScheduleDoesNotMissMidnightWhenPassStraddlesBoundary(t *testing.T) {
	chain := NewMemoryChain()
	_, _ = chain.Append(context.Background(), newTestRow(0))
	key, _ := GenerateSigningKey()
	before := time.Date(2026, 10, 27, 23, 59, 59, 0, time.UTC)
	cp := NewCheckpointer(chain, key)
	cp.SetClock(func() time.Time { return before })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (CheckpointSchedule{Checkpointer: cp, Now: func() time.Time { return before.Add(2 * time.Second) }, Wait: func(ctx context.Context, delay time.Duration) error {
		if delay != DefaultCheckpointRetry {
			t.Fatalf("straddled UTC boundary delayed checkpoint for %s", delay)
		}
		cancel()
		return ctx.Err()
	}}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
}
