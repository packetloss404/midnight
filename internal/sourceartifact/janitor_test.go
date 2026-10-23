// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJanitorDiscoveryCursorEventuallyReachesLateStaleWork(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < 8; index++ {
		path := filepath.Join(store.staging, stagingPrefix+"fresh-"+string(rune('a'+index)))
		if err := os.WriteFile(path, []byte("fresh"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := filepath.Join(store.staging, stagingPrefix+"late-stale")
	if err := os.WriteFile(stale, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	reached := false
	for pass := 0; pass < 20; pass++ {
		discovery, err := store.discoverJanitorCandidates(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		sweep, err := store.prepareJanitorSweep(
			ctx,
			map[string]struct{}{},
			discovery.digests,
			map[string]struct{}{},
			discovery,
			now.Add(-24*time.Hour),
			now.Add(-24*time.Hour),
			now.Add(-time.Hour),
			2,
			DefaultJanitorConfig.MaxVerifyBytes,
		)
		if err != nil {
			t.Fatal(err)
		}
		if sweep.result.StagingQuarantined == 1 {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatal("advancing discovery cursor never reached late stale staging work")
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late stale stage remains live: %v", err)
	}
}

func TestJanitorDiscoveryDoesNotReopenExhaustedCategoriesInOnePass(t *testing.T) {
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(store.staging, stagingPrefix+"only")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(store.checkouts, checkoutPrefix+"only")
	if err := os.Mkdir(checkout, 0o750); err != nil {
		t.Fatal(err)
	}
	artifact := commitJanitorArtifact(t, store, "only")
	path, _, _ := store.blobPath(artifact.Digest)
	move, err := store.quarantinePath(path, "blob", artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := store.discoverJanitorCandidates(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.quarantineNames) != 1 ||
		len(discovery.stagingPaths) != 1 ||
		len(discovery.checkoutPaths) != 1 {
		t.Fatalf(
			"discovery duplicated exhausted categories: quarantine=%v staging=%v checkout=%v",
			discovery.quarantineNames,
			discovery.stagingPaths,
			discovery.checkoutPaths,
		)
	}
	if discovery.quarantineNames[0] != filepath.Base(move.container) {
		t.Fatalf("quarantine candidate=%q, want %q", discovery.quarantineNames[0], filepath.Base(move.container))
	}
}

func TestJanitorDiscoveryEventuallyRestoresRequiredQuarantinePastFreshHead(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	var requiredDigest string
	for index := 0; index < 6; index++ {
		artifact := commitJanitorArtifact(t, store, "source-"+string(rune('a'+index)))
		path, _, err := store.blobPath(artifact.Digest)
		if err != nil {
			t.Fatal(err)
		}
		move, err := store.quarantinePath(path, "blob", artifact.Digest)
		if err != nil {
			t.Fatal(err)
		}
		if index == 5 {
			requiredDigest = artifact.Digest
		}
		if err := os.Chtimes(move.target, old, old); err != nil {
			t.Fatal(err)
		}
		// Keep every earlier quarantine fresh and ineligible. The advancing
		// directory cursor must still reach the required entry behind them.
		if err := os.Chtimes(move.container, now, now); err != nil {
			t.Fatal(err)
		}
	}

	restored := false
	for pass := 0; pass < 20; pass++ {
		discovery, err := store.discoverJanitorCandidates(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		required := map[string]struct{}{}
		if _, candidate := discovery.digests[requiredDigest]; candidate {
			required[requiredDigest] = struct{}{}
		}
		sweep, err := store.prepareJanitorSweep(
			ctx,
			required,
			discovery.digests,
			map[string]struct{}{},
			discovery,
			now.Add(-24*time.Hour),
			now.Add(-24*time.Hour),
			now.Add(-time.Hour),
			2,
			DefaultJanitorConfig.MaxVerifyBytes,
		)
		if err != nil {
			t.Fatal(err)
		}
		if sweep.result.QuarantineRestored == 1 {
			restored = true
			break
		}
	}
	if !restored {
		t.Fatal("advancing quarantine cursor never restored late required blob")
	}
	path, _, _ := store.blobPath(requiredDigest)
	if err := verifyFileDigest(ctx, path, requiredDigest, -1); err != nil {
		t.Fatalf("required canonical blob was not restored: %v", err)
	}
}

func TestRestoreRequiredBlobUsesExactDigestLocator(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := commitJanitorArtifact(t, store, "exact locator")
	path, _, err := store.blobPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	move, err := store.quarantinePath(path, "blob", artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.blobQuarantineLocatorPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(locator)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(payload), filepath.Base(move.container); got != want {
		t.Fatalf("locator=%q, want exact quarantine container %q", got, want)
	}

	// This deliberately malformed matching-prefix entry sorts before the real
	// random container. A global quarantine scan would inspect it and fail;
	// exact digest lookup must never touch it.
	poison := filepath.Join(
		store.quarantine,
		quarantinePrefix+"blob-"+strings.TrimPrefix(artifact.Digest, digestPrefix)+"-!poison",
	)
	if err := os.WriteFile(poison, []byte("not a quarantine container"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreRequiredBlob(ctx, artifact.Digest); err != nil {
		t.Fatalf("exact digest recovery was affected by unrelated quarantine entries: %v", err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("required blob was not restored: %v", err)
	}
	if _, err := os.Lstat(locator); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored locator remains: %v", err)
	}
	if _, err := os.Lstat(poison); err != nil {
		t.Fatalf("exact digest recovery mutated unrelated quarantine entry: %v", err)
	}
}

func TestBlobQuarantineLocatorPreventsUnboundedSameDigestCopies(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := makeArchive(t, archiveEntry{
		name: "source.txt", body: "one digest", typeflag: tar.TypeReg,
	})
	staged, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := store.blobPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.quarantinePath(path, "blob", artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}

	republished, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, republished); err != nil {
		t.Fatal(err)
	}
	if _, err := store.quarantinePath(path, "blob", artifact.Digest); !errors.Is(err, errBlobAlreadyQuarantined) {
		t.Fatalf("second same-digest quarantine error=%v, want bounded locator conflict", err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("locator conflict removed canonical blob: %v", err)
	}
	if err := verifyFileDigest(ctx, first.target, artifact.Digest, -1); err != nil {
		t.Fatalf("locator conflict damaged existing quarantine: %v", err)
	}

	if err := restoreQuarantineMove(ctx, first); err != nil {
		t.Fatal(err)
	}
	locator, err := store.blobQuarantineLocatorPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(locator); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("duplicate convergence left locator behind: %v", err)
	}
}

func TestBlobQuarantineRepairsInterruptedLocatorBeforeRename(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := commitJanitorArtifact(t, store, "interrupted locator")
	path, _, err := store.blobPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.blobQuarantineLocatorPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(locator), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locator, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	move, err := store.quarantinePath(path, "blob", artifact.Digest)
	if err != nil {
		t.Fatalf("verified canonical blob did not repair interrupted locator: %v", err)
	}
	if err := store.RestoreRequiredBlob(ctx, artifact.Digest); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("repaired locator did not restore canonical blob: %v", err)
	}
	if _, err := os.Lstat(move.container); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restored quarantine container remains: %v", err)
	}
}

func TestJanitorConvergesGraceAgedInterruptedBlobQuarantineCreation(t *testing.T) {
	tests := map[string]func(*testing.T, string, string){
		"container before locator creation": func(t *testing.T, _, _ string) {},
		"container and zero-byte locator": func(t *testing.T, locator, _ string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(locator), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(locator, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"durable locator before canonical rename": func(t *testing.T, locator, container string) {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(locator), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(locator, []byte(filepath.Base(container)), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, createLocator := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			artifact := commitJanitorArtifact(t, store, name)
			path, _, err := store.blobPath(artifact.Digest)
			if err != nil {
				t.Fatal(err)
			}
			container, err := os.MkdirTemp(
				store.quarantine,
				quarantinePrefix+"blob-"+strings.TrimPrefix(artifact.Digest, digestPrefix)+"-",
			)
			if err != nil {
				t.Fatal(err)
			}
			locator, err := store.blobQuarantineLocatorPath(artifact.Digest)
			if err != nil {
				t.Fatal(err)
			}
			createLocator(t, locator, container)

			now := time.Now().UTC()
			old := now.Add(-2 * time.Hour)
			if err := os.Chtimes(container, old, old); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(locator); err == nil {
				if err := os.Chtimes(locator, old, old); err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}

			discovery, err := store.discoverJanitorCandidates(ctx, 4)
			if err != nil {
				t.Fatalf("interrupted container poisoned bounded discovery: %v", err)
			}
			required := map[string]struct{}{artifact.Digest: {}}
			sweep, err := store.prepareJanitorSweep(
				ctx,
				required,
				discovery.digests,
				map[string]struct{}{},
				discovery,
				now.Add(-24*time.Hour),
				now.Add(-24*time.Hour),
				now.Add(-time.Hour),
				4,
				DefaultJanitorConfig.MaxVerifyBytes,
			)
			if err != nil {
				t.Fatalf("interrupted container did not converge after grace: %v", err)
			}
			if sweep.result.QuarantineFinalized != 1 {
				t.Fatalf("finalized=%d, want one incomplete container", sweep.result.QuarantineFinalized)
			}
			if _, err := os.Lstat(container); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete quarantine remains: %v", err)
			}
			if _, err := os.Lstat(locator); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("interrupted locator remains: %v", err)
			}
			if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
				t.Fatalf("cleanup damaged canonical blob: %v", err)
			}
		})
	}
}

func TestJanitorIgnoresFreshIncompleteBlobQuarantine(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := commitJanitorArtifact(t, store, "active quarantine creation")
	container, err := os.MkdirTemp(
		store.quarantine,
		quarantinePrefix+"blob-"+strings.TrimPrefix(artifact.Digest, digestPrefix)+"-",
	)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := store.blobQuarantineLocatorPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(locator), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(locator, []byte(filepath.Base(container)), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	discovery, err := store.discoverJanitorCandidates(ctx, 4)
	if err != nil {
		t.Fatalf("fresh incomplete creation poisoned discovery: %v", err)
	}
	sweep, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{artifact.Digest: {}},
		discovery.digests,
		map[string]struct{}{},
		discovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sweep.result.QuarantineFinalized != 0 {
		t.Fatalf("fresh incomplete creation was finalized: %#v", sweep.result)
	}
	if _, err := os.Lstat(container); err != nil {
		t.Fatalf("fresh incomplete container was removed: %v", err)
	}
	if _, err := os.Lstat(locator); err != nil {
		t.Fatalf("fresh incomplete locator was removed: %v", err)
	}
}

func TestJanitorMakesAbsentOldMetadataEligibleAndBoundsVerifiedBytes(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	absent := "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))
	first := commitJanitorArtifact(t, store, "first")
	second := commitJanitorArtifact(t, store, "second")
	for _, artifact := range []Artifact{first, second} {
		path, _, _ := store.blobPath(artifact.Digest)
		old := now.Add(-48 * time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	firstPath, _, _ := store.blobPath(first.Digest)
	firstInfo, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[string]struct{}{
		absent:        {},
		first.Digest:  {},
		second.Digest: {},
	}
	sweep, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{},
		candidates,
		candidates,
		janitorDiscovery{digests: candidates},
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		8,
		firstInfo.Size(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, eligible := sweep.eligibleMetadataDigests[absent]; !eligible {
		t.Fatal("old unreferenced metadata with absent canonical blob was not eligible")
	}
	if sweep.result.BlobsQuarantined != 1 {
		t.Fatalf("blobs quarantined=%d, want byte budget to permit exactly one", sweep.result.BlobsQuarantined)
	}
}

func TestJanitorConfigAlwaysBudgetsTwoMaximumSizedCopies(t *testing.T) {
	config := DefaultJanitorConfig
	config.MaxVerifyBytes = 2*DefaultLimits.MaxCompressedBytes - 1
	if err := config.Validate(); err == nil {
		t.Fatal("janitor accepted a budget that cannot converge a maximum-sized republished duplicate")
	}
	config.MaxVerifyBytes = 2 * DefaultLimits.MaxCompressedBytes
	if err := config.Validate(); err != nil {
		t.Fatalf("exact two-copy verification budget rejected: %v", err)
	}
}

func TestFreshQuarantineDoesNotHashCorruptPayloadBeforeGrace(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	artifact := commitJanitorArtifact(t, store, "corrupt-later")
	path, _, _ := store.blobPath(artifact.Digest)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	move, err := store.quarantinePath(path, "blob", artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(move.target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(move.target, []byte("corrupt"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(move.target, old, old); err != nil {
		t.Fatal(err)
	}
	discovery := janitorDiscovery{
		digests:         map[string]struct{}{artifact.Digest: {}},
		quarantineNames: []string{filepath.Base(move.container)},
	}
	if _, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{},
		discovery.digests,
		map[string]struct{}{},
		discovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		2,
		DefaultJanitorConfig.MaxVerifyBytes,
	); err != nil {
		t.Fatalf("fresh quarantine performed an unnecessary integrity hash: %v", err)
	}
}

func TestNewQuarantineCannotBeFinalizedUntilLaterGraceAgedPass(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	artifact := commitJanitorArtifact(t, store, "two-pass")
	path, _, _ := store.blobPath(artifact.Digest)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	discovery := janitorDiscovery{digests: map[string]struct{}{artifact.Digest: {}}}
	first, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{},
		discovery.digests,
		discovery.digests,
		discovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("new quarantine finalized in the same pass: entries=%d", len(entries))
	}
	container := filepath.Join(store.quarantine, entries[0].Name())
	if err := os.Chtimes(container, old, old); err != nil {
		t.Fatal(err)
	}
	secondDiscovery := janitorDiscovery{
		digests:         map[string]struct{}{artifact.Digest: {}},
		quarantineNames: []string{entries[0].Name()},
	}
	second, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{},
		secondDiscovery.digests,
		secondDiscovery.digests,
		secondDiscovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(store.quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || second.result.QuarantineFinalized != 1 {
		t.Fatalf("grace-aged quarantine not finalized: entries=%d result=%#v", len(entries), second.result)
	}
}

func TestRebindDuringQuarantineGracePreservesCanonicalAndFinalizesRedundantCopy(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload := makeArchive(t, archiveEntry{
		name: "source.txt", body: "same digest", typeflag: tar.TypeReg,
	})
	staged, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := store.blobPath(artifact.Digest)
	old := now.Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	firstDiscovery := janitorDiscovery{digests: map[string]struct{}{artifact.Digest: {}}}
	first, err := store.prepareJanitorSweep(
		ctx,
		map[string]struct{}{},
		firstDiscovery.digests,
		firstDiscovery.digests,
		firstDiscovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(store.quarantine)
	if err != nil || len(entries) != 1 {
		t.Fatalf("initial quarantine entries=%v err=%v", entries, err)
	}

	// A new accepted upload of the same bytes republishes the canonical blob
	// before its ownership bind waits on the lifecycle lock.
	rebindStage, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Commit(ctx, rebindStage); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("rebind did not republish canonical blob: %v", err)
	}

	required := map[string]struct{}{artifact.Digest: {}}
	secondDiscovery := janitorDiscovery{
		digests:         required,
		quarantineNames: []string{entries[0].Name()},
	}
	second, err := store.prepareJanitorSweep(
		ctx,
		required,
		required,
		map[string]struct{}{},
		secondDiscovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(store.quarantine)
	if err != nil || len(entries) != 1 {
		t.Fatalf("required redundant quarantine bypassed grace: entries=%v err=%v", entries, err)
	}

	container := filepath.Join(store.quarantine, entries[0].Name())
	if err := os.Chtimes(container, old, old); err != nil {
		t.Fatal(err)
	}
	third, err := store.prepareJanitorSweep(
		ctx,
		required,
		required,
		map[string]struct{}{},
		secondDiscovery,
		now.Add(-24*time.Hour),
		now.Add(-24*time.Hour),
		now.Add(-time.Hour),
		4,
		DefaultJanitorConfig.MaxVerifyBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := third.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("finalizing redundant quarantine damaged canonical blob: %v", err)
	}
	entries, err = os.ReadDir(store.quarantine)
	if err != nil || len(entries) != 0 {
		t.Fatalf("grace-aged redundant quarantine remains: entries=%v err=%v", entries, err)
	}
}

func commitJanitorArtifact(t *testing.T, store *FSStore, body string) Artifact {
	t.Helper()
	payload := makeArchive(t, archiveEntry{
		name: "source.txt", body: body, typeflag: tar.TypeReg,
	})
	staged, err := store.Stage(context.Background(), bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(context.Background(), staged)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
