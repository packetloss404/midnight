// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestLegacySweepQuarantinesOnlyOldUnprotectedOwnedPaths(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)

	stage := filepath.Join(store.root, legacyStagePrefix+"abc"+legacyStageSuffix)
	if err := os.WriteFile(stage, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(store.root, legacyCheckoutPrefix+"garbage")
	protectedCheckout := filepath.Join(store.root, legacyCheckoutPrefix+"active")
	for _, directory := range []string{checkout, protectedCheckout} {
		if err := os.Mkdir(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{stage, checkout, protectedCheckout} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := store.discoverLegacyWork(ctx, 32)
	if err != nil {
		t.Fatal(err)
	}
	protected := map[string]struct{}{legacyLocalURIPrefix + protectedCheckout: {}}
	sweep, err := store.prepareLegacySweep(
		ctx,
		candidates,
		protected,
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		32,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sweep.result.StagesQuarantined != 1 ||
		sweep.result.CheckoutsQuarantined != 1 {
		t.Fatalf("result=%#v, want one stage and checkout quarantined", sweep.result)
	}
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage still live: %v", err)
	}
	if _, err := os.Lstat(checkout); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout still live: %v", err)
	}
	if info, err := os.Lstat(protectedCheckout); err != nil || !info.IsDir() {
		t.Fatalf("protected checkout missing: info=%v err=%v", info, err)
	}

	quarantineRoot := filepath.Join(store.root, legacyQuarantineDirectory)
	entries, err := os.ReadDir(quarantineRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("quarantine entries=%d, want 2", len(entries))
	}
	for _, entry := range entries {
		container := filepath.Join(quarantineRoot, entry.Name())
		if err := os.Chtimes(container, old, old); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err = store.discoverLegacyWork(ctx, 32)
	if err != nil {
		t.Fatal(err)
	}
	finalize, err := store.prepareLegacySweep(
		ctx,
		candidates,
		protected,
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		32,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := finalize.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(quarantineRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("quarantine entries=%d, want finalized empty quarantine", len(entries))
	}
}

func TestLegacySweepRestoresPreparedAndProtectedQuarantine(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	checkout := filepath.Join(store.root, legacyCheckoutPrefix+"restore")
	if err := os.Mkdir(checkout, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(checkout, old, old); err != nil {
		t.Fatal(err)
	}

	candidates, err := store.discoverLegacyWork(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	sweep, err := store.prepareLegacySweep(
		ctx,
		candidates,
		nil,
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := sweep.Restore(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(checkout); err != nil || !info.IsDir() {
		t.Fatalf("rollback did not restore checkout: info=%v err=%v", info, err)
	}

	candidates, err = store.discoverLegacyWork(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	sweep, err = store.prepareLegacySweep(
		ctx,
		candidates,
		nil,
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		8,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err = store.discoverLegacyWork(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	protected := map[string]struct{}{legacyLocalURIPrefix + checkout: {}}
	restore, err := store.prepareLegacySweep(
		ctx,
		candidates,
		protected,
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if restore.result.QuarantineRestored != 1 {
		t.Fatalf("result=%#v, want protected restore", restore.result)
	}
	if info, err := os.Lstat(checkout); err != nil || !info.IsDir() {
		t.Fatalf("protected quarantine did not restore: info=%v err=%v", info, err)
	}
}

func TestLegacyDiscoveryFailsClosedOnOwnedNameSymlink(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	link := filepath.Join(store.root, legacyCheckoutPrefix+"symlink")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.discoverLegacyWork(context.Background(), 8); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("discover error=%v, want ErrIntegrity", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("symlink target changed: info=%v err=%v", info, err)
	}
}

func TestLegacyDiscoveryCursorEventuallyVisitsEveryOwnedCandidate(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const perKind = 12
	want := map[string]struct{}{}
	for index := 0; index < perKind; index++ {
		stage := filepath.Join(
			store.root,
			legacyStagePrefix+fmt.Sprintf("%03d", index)+legacyStageSuffix,
		)
		if err := os.WriteFile(stage, []byte("staged"), 0o600); err != nil {
			t.Fatal(err)
		}
		want[stage] = struct{}{}

		checkout := filepath.Join(
			store.root,
			legacyCheckoutPrefix+fmt.Sprintf("%03d", index),
		)
		if err := os.Mkdir(checkout, 0o750); err != nil {
			t.Fatal(err)
		}
		want[checkout] = struct{}{}
	}

	seen := map[string]struct{}{}
	for pass := 0; pass < 64 && len(seen) < len(want); pass++ {
		candidates, err := store.discoverLegacyWork(context.Background(), 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) > 3 {
			t.Fatalf("pass %d returned %d candidates, want at most 3", pass, len(candidates))
		}
		for _, candidate := range candidates {
			seen[candidate.original] = struct{}{}
		}
	}
	if len(seen) != len(want) {
		var missing []string
		for candidate := range want {
			if _, ok := seen[candidate]; !ok {
				missing = append(missing, filepath.Base(candidate))
			}
		}
		sort.Strings(missing)
		t.Fatalf("visited %d/%d candidates; missing=%v", len(seen), len(want), missing)
	}
}
