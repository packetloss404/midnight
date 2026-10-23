// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	blobQuarantineIndexDirectory = "quarantine-index"
	maxQuarantineLocatorBytes    = 256
)

var (
	errBlobAlreadyQuarantined = errors.New("source artifact: blob already has recoverable quarantine")
	errIncompleteQuarantine   = errors.New("source artifact: quarantine creation is incomplete")
)

type filesystemJanitorResult struct {
	BlobsQuarantined     int
	StagingQuarantined   int
	CheckoutsQuarantined int
	QuarantineRestored   int
	QuarantineFinalized  int
}

type quarantineMove struct {
	kind      string
	digest    string
	original  string
	target    string
	container string
	locator   string
	finalize  bool
}

type preparedJanitorSweep struct {
	store                   *FSStore
	moves                   []quarantineMove
	eligibleMetadataDigests map[string]struct{}
	result                  filesystemJanitorResult
}

type janitorDiscovery struct {
	digests         map[string]struct{}
	quarantineNames []string
	stagingPaths    []string
	checkoutPaths   []string
}

// discoverJanitorCandidates performs a read-only, bounded filesystem
// pre-scan. Persistent directory handles advance between passes so protected
// or recent entries at the front cannot deterministically starve later work.
// Durable protection is checked again for exactly these candidates while the
// lifecycle lock is held; discovery alone never authorizes a mutation.
func (s *FSStore) discoverJanitorCandidates(ctx context.Context, maxItems int) (janitorDiscovery, error) {
	if err := s.ready(); err != nil {
		return janitorDiscovery{}, err
	}
	if maxItems <= 0 {
		return janitorDiscovery{}, ErrLimitExceeded
	}
	s.janitorScanMu.Lock()
	defer s.janitorScanMu.Unlock()
	if s.janitorScanHandles == nil {
		s.janitorScanHandles = map[string]*os.File{}
	}
	result := janitorDiscovery{digests: make(map[string]struct{}, maxItems)}
	emptyCategories := 0
	exhausted := [4]bool{}
	seenPaths := map[string]struct{}{}
	for examined := 0; examined < maxItems && emptyCategories < 4; {
		if err := ctx.Err(); err != nil {
			return janitorDiscovery{}, err
		}
		category := s.janitorScanCategory % 4
		s.janitorScanCategory = (s.janitorScanCategory + 1) % 4
		if exhausted[category] {
			emptyCategories++
			continue
		}
		var entry os.DirEntry
		var found bool
		var err error
		switch category {
		case 0:
			entry, found, err = s.nextJanitorDirectoryEntry("quarantine", s.quarantine)
		case 1:
			entry, found, err = s.nextJanitorBlobEntry()
		case 2:
			entry, found, err = s.nextJanitorDirectoryEntry("staging", s.staging)
		case 3:
			entry, found, err = s.nextJanitorDirectoryEntry("checkout", s.checkouts)
		}
		if err != nil {
			return janitorDiscovery{}, err
		}
		if !found {
			exhausted[category] = true
			emptyCategories++
			continue
		}
		emptyCategories = 0
		examined++
		switch category {
		case 0:
			if entry.Type()&os.ModeSymlink != 0 ||
				!entry.IsDir() ||
				!strings.HasPrefix(entry.Name(), quarantinePrefix) {
				return janitorDiscovery{}, fmt.Errorf("%w: unexpected quarantine entry", ErrIntegrity)
			}
			move, err := s.readQuarantineMove(entry.Name())
			if err != nil && !errors.Is(err, errIncompleteQuarantine) {
				return janitorDiscovery{}, err
			}
			if _, duplicate := seenPaths["quarantine:"+entry.Name()]; !duplicate {
				seenPaths["quarantine:"+entry.Name()] = struct{}{}
				result.quarantineNames = append(result.quarantineNames, entry.Name())
			}
			if move.kind == "blob" {
				result.digests[move.digest] = struct{}{}
			}
		case 1:
			digest, err := digestFromBlobEntry(entry)
			if err != nil {
				return janitorDiscovery{}, err
			}
			result.digests[digest] = struct{}{}
		case 2:
			path := filepath.Join(s.staging, entry.Name())
			if _, duplicate := seenPaths[path]; !duplicate {
				seenPaths[path] = struct{}{}
				result.stagingPaths = append(result.stagingPaths, path)
			}
		case 3:
			path := filepath.Join(s.checkouts, entry.Name())
			if _, duplicate := seenPaths[path]; !duplicate {
				seenPaths[path] = struct{}{}
				result.checkoutPaths = append(result.checkoutPaths, path)
			}
		}
	}
	return result, nil
}

func (s *FSStore) nextJanitorDirectoryEntry(key, directory string) (os.DirEntry, bool, error) {
	handle := s.janitorScanHandles[key]
	if handle == nil {
		var err error
		handle, err = os.Open(directory)
		if err != nil {
			return nil, false, fmt.Errorf("source artifact: open janitor scan directory: %w", err)
		}
		s.janitorScanHandles[key] = handle
	}
	entries, err := handle.ReadDir(1)
	if errors.Is(err, io.EOF) {
		_ = handle.Close()
		delete(s.janitorScanHandles, key)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("source artifact: advance janitor scan directory: %w", err)
	}
	return entries[0], true, nil
}

func (s *FSStore) nextJanitorBlobEntry() (os.DirEntry, bool, error) {
	prefixes, err := os.ReadDir(s.blobs)
	if err != nil {
		return nil, false, fmt.Errorf("source artifact: list blob prefixes: %w", err)
	}
	if len(prefixes) > 256 {
		return nil, false, fmt.Errorf("%w: too many blob prefix directories", ErrIntegrity)
	}
	for _, prefix := range prefixes {
		if prefix.Type()&os.ModeSymlink != 0 ||
			!prefix.IsDir() ||
			len(prefix.Name()) != 2 ||
			!hexRE.MatchString(prefix.Name()+strings.Repeat("0", 62)) {
			return nil, false, fmt.Errorf("%w: unexpected blob prefix", ErrIntegrity)
		}
	}
	if len(prefixes) == 0 {
		return nil, false, nil
	}
	for examined := 0; examined < len(prefixes); examined++ {
		if s.janitorBlobPrefix >= len(prefixes) {
			s.janitorBlobPrefix = 0
		}
		prefix := prefixes[s.janitorBlobPrefix]
		key := "blob:" + prefix.Name()
		entry, found, err := s.nextJanitorDirectoryEntry(key, filepath.Join(s.blobs, prefix.Name()))
		if err != nil {
			return nil, false, err
		}
		if found {
			hexDigest := strings.TrimSuffix(entry.Name(), ".tar.gz")
			if !strings.HasPrefix(hexDigest, prefix.Name()) {
				return nil, false, fmt.Errorf("%w: blob is in the wrong prefix directory", ErrIntegrity)
			}
			return entry, true, nil
		}
		s.janitorBlobPrefix = (s.janitorBlobPrefix + 1) % len(prefixes)
	}
	return nil, false, nil
}

func digestFromBlobEntry(entry os.DirEntry) (string, error) {
	if entry.Type()&os.ModeSymlink != 0 ||
		!entry.Type().IsRegular() ||
		!strings.HasSuffix(entry.Name(), ".tar.gz") {
		return "", fmt.Errorf("%w: unexpected blob entry", ErrIntegrity)
	}
	hexDigest := strings.TrimSuffix(entry.Name(), ".tar.gz")
	digest := digestPrefix + hexDigest
	if !digestRE.MatchString(digest) {
		return "", fmt.Errorf("%w: malformed blob path", ErrIntegrity)
	}
	return digest, nil
}

// RestoreRequiredBlob recovers the canonical blob from an owned quarantine
// after exact active database ownership has been verified. This closes the
// process-death window between an atomic quarantine rename and transaction
// rollback/commit recovery.
func (s *FSStore) RestoreRequiredBlob(ctx context.Context, digest string) error {
	if err := s.ready(); err != nil {
		return err
	}
	blobPath, _, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := verifyFileDigest(ctx, blobPath, digest, -1); err == nil {
		return nil
	} else if _, statErr := os.Lstat(blobPath); statErr == nil {
		return err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return err
	}
	move, err := s.readBlobQuarantineLocator(digest)
	if err != nil {
		return fmt.Errorf("%w: required blob is missing from canonical storage and quarantine: %v", ErrIntegrity, err)
	}
	if err := verifyFileDigest(ctx, move.target, digest, -1); err != nil {
		return err
	}
	if err := restoreQuarantineMove(ctx, move); err != nil {
		return err
	}
	if err := verifyFileDigest(ctx, blobPath, digest, -1); err != nil {
		return fmt.Errorf("%w: restored required blob failed canonical verification: %v", ErrIntegrity, err)
	}
	return nil
}

func (s *FSStore) prepareJanitorSweep(
	ctx context.Context,
	requiredDigests map[string]struct{},
	candidateDigests map[string]struct{},
	metadataCandidates map[string]struct{},
	discovery janitorDiscovery,
	blobCutoff time.Time,
	workCutoff time.Time,
	quarantineCutoff time.Time,
	maxItems int,
	maxVerifyBytes int64,
) (*preparedJanitorSweep, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	sweep := &preparedJanitorSweep{
		store:                   s,
		eligibleMetadataDigests: map[string]struct{}{},
	}
	actions := 0
	var verifiedBytes int64
	if err := sweep.convergeQuarantine(
		ctx,
		requiredDigests,
		candidateDigests,
		discovery.quarantineNames,
		blobCutoff,
		workCutoff,
		quarantineCutoff,
		&actions,
		&verifiedBytes,
		maxItems,
		maxVerifyBytes,
	); err != nil {
		_ = sweep.Restore()
		return nil, err
	}
	digests := make([]string, 0, len(candidateDigests))
	for digest := range candidateDigests {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	blobs := make([]Blob, 0, len(digests))
	for _, digest := range digests {
		if err := ctx.Err(); err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		path, storageKey, err := s.blobPath(digest)
		if err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if _, eligible := metadataCandidates[digest]; eligible {
				if _, required := requiredDigests[digest]; !required {
					sweep.eligibleMetadataDigests[digest] = struct{}{}
				}
			}
			continue
		}
		if err != nil ||
			info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Size() <= 0 ||
			info.Size() > DefaultLimits.MaxCompressedBytes {
			_ = sweep.Restore()
			return nil, fmt.Errorf("%w: malformed candidate blob", ErrIntegrity)
		}
		blobs = append(blobs, Blob{
			Digest:     digest,
			StorageKey: storageKey,
			Path:       path,
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC(),
		})
	}
	for _, blob := range blobs {
		if err := ctx.Err(); err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		if _, required := requiredDigests[blob.Digest]; required ||
			blobCutoff.IsZero() ||
			!blob.ModifiedAt.Before(blobCutoff) {
			continue
		}
		// Reserve one action for the quarantine rename and one for a
		// correctness-preserving restore if Commit refreshed the inode after
		// ListBlobs observed it.
		if actions+2 > maxItems {
			break
		}
		if blob.Size > maxVerifyBytes-verifiedBytes {
			continue
		}
		if err := verifyFileDigest(ctx, blob.Path, blob.Digest, blob.Size); err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		verifiedBytes += blob.Size
		move, err := s.quarantinePath(blob.Path, "blob", blob.Digest)
		if errors.Is(err, errBlobAlreadyQuarantined) {
			// Only one durable locator is admitted for a digest. An older
			// quarantine is converged first; until then, leave a republished
			// canonical copy in place.
			continue
		}
		if err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		actions++
		info, err := os.Lstat(move.target)
		if err != nil {
			_ = sweep.Restore()
			return nil, fmt.Errorf("source artifact: inspect quarantined blob: %w", err)
		}
		// Commit refreshes the inode before binding. Rechecking after the
		// atomic rename closes the list/stat race: a refreshed claim is
		// restored while the database lifecycle lock still blocks its bind.
		if !info.ModTime().UTC().Before(blobCutoff) {
			if err := restoreQuarantineMove(ctx, move); err != nil {
				_ = sweep.Restore()
				return nil, err
			}
			sweep.result.QuarantineRestored++
			actions++
			continue
		}
		sweep.moves = append(sweep.moves, move)
		sweep.eligibleMetadataDigests[blob.Digest] = struct{}{}
		sweep.result.BlobsQuarantined++
	}
	if err := sweep.quarantineStaleWork(
		ctx,
		s.staging,
		stagingPrefix,
		"staging",
		discovery.stagingPaths,
		workCutoff,
		&actions,
		maxItems,
	); err != nil {
		_ = sweep.Restore()
		return nil, err
	}
	if err := sweep.quarantineStaleWork(
		ctx,
		s.checkouts,
		checkoutPrefix,
		"checkout",
		discovery.checkoutPaths,
		workCutoff,
		&actions,
		maxItems,
	); err != nil {
		_ = sweep.Restore()
		return nil, err
	}
	return sweep, nil
}

func (sweep *preparedJanitorSweep) convergeQuarantine(
	ctx context.Context,
	requiredDigests map[string]struct{},
	candidateDigests map[string]struct{},
	quarantineNames []string,
	blobCutoff time.Time,
	workCutoff time.Time,
	quarantineCutoff time.Time,
	actions *int,
	verifiedBytes *int64,
	maxItems int,
	maxVerifyBytes int64,
) error {
	names := append([]string(nil), quarantineNames...)
	sort.SliceStable(names, func(i, j int) bool {
		left, leftRequired := quarantineNameRequired(sweep.store, names[i], requiredDigests)
		right, rightRequired := quarantineNameRequired(sweep.store, names[j], requiredDigests)
		if leftRequired != rightRequired {
			return leftRequired
		}
		return left < right
	})
	for _, name := range names {
		// Reserve one action for the quarantine rename and one for a
		// post-rename freshness restore.
		if *actions+2 > maxItems {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		containerPath := filepath.Join(sweep.store.quarantine, name)
		containerInfo, err := os.Lstat(containerPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil ||
			containerInfo.Mode()&os.ModeSymlink != 0 ||
			!containerInfo.IsDir() ||
			!strings.HasPrefix(name, quarantinePrefix) {
			return fmt.Errorf("%w: unexpected quarantine entry", ErrIntegrity)
		}
		move, err := sweep.store.readQuarantineMove(name)
		if errors.Is(err, errIncompleteQuarantine) {
			if quarantineCutoff.IsZero() ||
				!containerInfo.ModTime().UTC().Before(quarantineCutoff) {
				continue
			}
			if err := sweep.store.cleanupInterruptedQuarantine(
				ctx,
				move,
				quarantineCutoff,
			); err != nil {
				return err
			}
			sweep.result.QuarantineFinalized++
			*actions++
			continue
		}
		if err != nil {
			return err
		}
		info, err := os.Lstat(move.target)
		if err != nil {
			return fmt.Errorf("%w: inspect quarantine payload", ErrIntegrity)
		}
		switch move.kind {
		case "blob":
			if _, candidate := candidateDigests[move.digest]; !candidate {
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 ||
				!info.Mode().IsRegular() ||
				info.Size() <= 0 ||
				info.Size() > DefaultLimits.MaxCompressedBytes {
				return fmt.Errorf("%w: quarantined blob is not regular", ErrIntegrity)
			}
			_, required := requiredDigests[move.digest]
			restore := required || blobCutoff.IsZero() || !info.ModTime().UTC().Before(blobCutoff)
			finalize := false
			canonicalInfo, canonicalErr := os.Lstat(move.original)
			canonicalExists := canonicalErr == nil
			if canonicalErr != nil && !errors.Is(canonicalErr, os.ErrNotExist) {
				return fmt.Errorf("source artifact: inspect quarantined blob canonical path: %w", canonicalErr)
			}
			if canonicalExists &&
				(canonicalInfo.Mode()&os.ModeSymlink != 0 ||
					!canonicalInfo.Mode().IsRegular() ||
					canonicalInfo.Size() <= 0 ||
					canonicalInfo.Size() > DefaultLimits.MaxCompressedBytes) {
				return fmt.Errorf("%w: canonical duplicate is not a valid blob file", ErrIntegrity)
			}
			if !restore {
				finalize = !quarantineCutoff.IsZero() &&
					containerInfo.ModTime().UTC().Before(quarantineCutoff)
			}
			if restore && canonicalExists {
				// A same-digest upload may republish and bind while this older
				// quarantine is observing its deletion grace. Keep the
				// quarantine until grace, then verify both copies before
				// scheduling only the redundant copy for finalization.
				if quarantineCutoff.IsZero() ||
					!containerInfo.ModTime().UTC().Before(quarantineCutoff) {
					continue
				}
				if info.Size()+canonicalInfo.Size() > maxVerifyBytes-*verifiedBytes {
					continue
				}
				if err := verifyFileDigest(ctx, move.target, move.digest, info.Size()); err != nil {
					return err
				}
				if err := verifyFileDigest(ctx, move.original, move.digest, canonicalInfo.Size()); err != nil {
					return err
				}
				*verifiedBytes += info.Size() + canonicalInfo.Size()
				move.finalize = true
				sweep.moves = append(sweep.moves, move)
				sweep.result.BlobsQuarantined++
				*actions++
				continue
			}
			// Fresh quarantines require neither a hash nor a mutation in this
			// pass. This keeps the lifecycle lock byte budget small.
			if !restore && !finalize {
				continue
			}
			if info.Size() > maxVerifyBytes-*verifiedBytes {
				continue
			}
			if err := verifyFileDigest(ctx, move.target, move.digest, info.Size()); err != nil {
				return err
			}
			*verifiedBytes += info.Size()
			if restore {
				if err := restoreQuarantineMove(ctx, move); err != nil {
					return err
				}
				sweep.result.QuarantineRestored++
				*actions++
				continue
			}
			move.finalize = true
			sweep.moves = append(sweep.moves, move)
			if _, err := os.Lstat(move.original); errors.Is(err, os.ErrNotExist) {
				sweep.eligibleMetadataDigests[move.digest] = struct{}{}
			} else if err != nil {
				return fmt.Errorf("source artifact: inspect quarantined blob canonical path: %w", err)
			}
			sweep.result.BlobsQuarantined++
			*actions++
		case "staging":
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
				!strings.HasPrefix(filepath.Base(move.target), stagingPrefix) {
				return fmt.Errorf("%w: malformed quarantined staging file", ErrIntegrity)
			}
			if workCutoff.IsZero() || !info.ModTime().UTC().Before(workCutoff) {
				if err := restoreQuarantineMove(ctx, move); err != nil {
					return err
				}
				sweep.result.QuarantineRestored++
				*actions++
				continue
			}
			if quarantineCutoff.IsZero() ||
				!containerInfo.ModTime().UTC().Before(quarantineCutoff) {
				continue
			}
			move.finalize = true
			sweep.moves = append(sweep.moves, move)
			sweep.result.StagingQuarantined++
			*actions++
		case "checkout":
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
				!strings.HasPrefix(filepath.Base(move.target), checkoutPrefix) {
				return fmt.Errorf("%w: malformed quarantined checkout", ErrIntegrity)
			}
			if workCutoff.IsZero() || !info.ModTime().UTC().Before(workCutoff) {
				if err := restoreQuarantineMove(ctx, move); err != nil {
					return err
				}
				sweep.result.QuarantineRestored++
				*actions++
				continue
			}
			if quarantineCutoff.IsZero() ||
				!containerInfo.ModTime().UTC().Before(quarantineCutoff) {
				continue
			}
			move.finalize = true
			sweep.moves = append(sweep.moves, move)
			sweep.result.CheckoutsQuarantined++
			*actions++
		default:
			return fmt.Errorf("%w: unknown quarantine kind", ErrIntegrity)
		}
	}
	return nil
}

func (sweep *preparedJanitorSweep) quarantineStaleWork(
	ctx context.Context,
	directory string,
	ownedPrefix string,
	kind string,
	candidates []string,
	cutoff time.Time,
	actions *int,
	maxItems int,
) error {
	if cutoff.IsZero() {
		return nil
	}
	for _, candidate := range candidates {
		if *actions >= maxItems {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filepath.Dir(filepath.Clean(candidate)) != directory ||
			!strings.HasPrefix(filepath.Base(candidate), ownedPrefix) {
			return fmt.Errorf("%w: unexpected %s entry", ErrIntegrity, kind)
		}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("source artifact: inspect %s: %w", kind, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unexpected %s symlink", ErrIntegrity, kind)
		}
		if kind == "staging" && !info.Mode().IsRegular() {
			return fmt.Errorf("%w: staging entry is not regular", ErrIntegrity)
		}
		if kind == "checkout" && !info.IsDir() {
			return fmt.Errorf("%w: checkout entry is not a directory", ErrIntegrity)
		}
		if !info.ModTime().UTC().Before(cutoff) {
			continue
		}
		move, err := sweep.store.quarantinePath(
			candidate,
			kind,
			"",
		)
		if err != nil {
			return err
		}
		*actions++
		movedInfo, err := os.Lstat(move.target)
		if err != nil {
			return fmt.Errorf("source artifact: inspect quarantined %s: %w", kind, err)
		}
		if !movedInfo.ModTime().UTC().Before(cutoff) {
			if err := restoreQuarantineMove(ctx, move); err != nil {
				return err
			}
			sweep.result.QuarantineRestored++
			*actions++
			continue
		}
		sweep.moves = append(sweep.moves, move)
		if kind == "staging" {
			sweep.result.StagingQuarantined++
		} else {
			sweep.result.CheckoutsQuarantined++
		}
	}
	return nil
}

func quarantineNameRequired(
	store *FSStore,
	name string,
	requiredDigests map[string]struct{},
) (string, bool) {
	if store == nil {
		return name, false
	}
	move, err := store.readQuarantineMove(name)
	if (err != nil && !errors.Is(err, errIncompleteQuarantine)) ||
		move.kind != "blob" {
		return name, false
	}
	_, required := requiredDigests[move.digest]
	return name, required
}

func (s *FSStore) quarantinePath(original, kind, digest string) (quarantineMove, error) {
	if !ownedJanitorPath(s, original, kind) {
		return quarantineMove{}, ErrOwnership
	}
	tag := quarantinePrefix + kind + "-"
	if kind == "blob" {
		if !digestRE.MatchString(digest) {
			return quarantineMove{}, ErrInvalidDigest
		}
		tag += strings.TrimPrefix(digest, digestPrefix) + "-"
	}
	container, err := os.MkdirTemp(s.quarantine, tag)
	if err != nil {
		return quarantineMove{}, fmt.Errorf("source artifact: create quarantine: %w", err)
	}
	if err := syncDirectory(s.quarantine); err != nil {
		_ = os.Remove(container)
		return quarantineMove{}, err
	}
	move := quarantineMove{
		kind:      kind,
		digest:    digest,
		original:  original,
		target:    filepath.Join(container, filepath.Base(original)),
		container: container,
	}
	if kind == "blob" {
		move.locator, err = s.installBlobQuarantineLocator(move)
		if errors.Is(err, errBlobAlreadyQuarantined) {
			_ = os.Remove(container)
			return quarantineMove{}, err
		}
		if err != nil {
			_ = os.Remove(container)
			return quarantineMove{}, err
		}
	}
	if err := os.Rename(original, move.target); err != nil {
		_ = os.Remove(container)
		return quarantineMove{}, errors.Join(
			fmt.Errorf("source artifact: quarantine %s: %w", kind, err),
			removeBlobQuarantineLocator(move),
		)
	}
	if err := syncDirectory(filepath.Dir(original)); err != nil {
		return move, errors.Join(err, restoreQuarantineMove(context.Background(), move))
	}
	if err := syncDirectory(container); err != nil {
		return move, errors.Join(err, restoreQuarantineMove(context.Background(), move))
	}
	if err := syncDirectory(s.quarantine); err != nil {
		return move, errors.Join(err, restoreQuarantineMove(context.Background(), move))
	}
	return move, nil
}

func (s *FSStore) readQuarantineMove(containerName string) (quarantineMove, error) {
	container := filepath.Join(s.quarantine, containerName)
	if filepath.Dir(container) != s.quarantine ||
		!strings.HasPrefix(containerName, quarantinePrefix) {
		return quarantineMove{}, ErrOwnership
	}
	rest := strings.TrimPrefix(containerName, quarantinePrefix)
	kind := ""
	digest := ""
	switch {
	case strings.HasPrefix(rest, "blob-"):
		kind = "blob"
		rest = strings.TrimPrefix(rest, "blob-")
		if len(rest) < 65 || rest[64] != '-' {
			return quarantineMove{}, fmt.Errorf("%w: malformed blob quarantine name", ErrIntegrity)
		}
		digest = digestPrefix + rest[:64]
		if !digestRE.MatchString(digest) {
			return quarantineMove{}, fmt.Errorf("%w: malformed blob quarantine digest", ErrIntegrity)
		}
	case strings.HasPrefix(rest, "staging-"):
		kind = "staging"
	case strings.HasPrefix(rest, "checkout-"):
		kind = "checkout"
	default:
		return quarantineMove{}, fmt.Errorf("%w: malformed quarantine name", ErrIntegrity)
	}
	move := quarantineMove{
		kind:      kind,
		digest:    digest,
		container: container,
	}
	switch kind {
	case "blob":
		original, _, err := s.blobPath(digest)
		if err != nil {
			return quarantineMove{}, err
		}
		move.original = original
		move.locator, err = s.blobQuarantineLocatorPath(digest)
		if err != nil {
			return quarantineMove{}, err
		}
	}
	entries, err := os.ReadDir(container)
	if err != nil {
		return quarantineMove{}, fmt.Errorf("%w: inspect quarantine payload: %v", ErrIntegrity, err)
	}
	if len(entries) == 0 {
		return move, errIncompleteQuarantine
	}
	if len(entries) != 1 {
		return quarantineMove{}, fmt.Errorf("%w: quarantine must contain exactly one payload", ErrIntegrity)
	}
	move.target = filepath.Join(container, entries[0].Name())
	switch kind {
	case "staging":
		if !strings.HasPrefix(entries[0].Name(), stagingPrefix) {
			return quarantineMove{}, fmt.Errorf("%w: malformed staging quarantine payload", ErrIntegrity)
		}
		move.original = filepath.Join(s.staging, entries[0].Name())
	case "checkout":
		if !strings.HasPrefix(entries[0].Name(), checkoutPrefix) {
			return quarantineMove{}, fmt.Errorf("%w: malformed checkout quarantine payload", ErrIntegrity)
		}
		move.original = filepath.Join(s.checkouts, entries[0].Name())
	}
	if !ownedJanitorPath(s, move.original, kind) {
		return quarantineMove{}, ErrOwnership
	}
	return move, nil
}

func (s *FSStore) cleanupInterruptedQuarantine(
	ctx context.Context,
	move quarantineMove,
	cutoff time.Time,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !isDirectOwnedQuarantine(s, move.container) {
		return ErrOwnership
	}
	info, err := os.Lstat(move.container)
	if err != nil {
		return fmt.Errorf("source artifact: inspect incomplete quarantine: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		cutoff.IsZero() ||
		!info.ModTime().UTC().Before(cutoff) {
		return fmt.Errorf("%w: incomplete quarantine is not an old owned directory", ErrOwnership)
	}
	entries, err := os.ReadDir(move.container)
	if err != nil || len(entries) != 0 {
		return fmt.Errorf("%w: incomplete quarantine is no longer empty", ErrIntegrity)
	}
	if move.kind == "blob" {
		if err := verifyFileDigest(ctx, move.original, move.digest, -1); err != nil {
			return fmt.Errorf("%w: incomplete blob quarantine has no verified canonical copy: %v", ErrIntegrity, err)
		}
		if err := removeInterruptedBlobQuarantineLocator(move, cutoff); err != nil {
			return err
		}
	}
	if err := os.Remove(move.container); err != nil {
		return fmt.Errorf("source artifact: remove incomplete quarantine: %w", err)
	}
	return syncDirectory(s.quarantine)
}

func (sweep *preparedJanitorSweep) Restore() error {
	if sweep == nil {
		return nil
	}
	var result error
	for index := len(sweep.moves) - 1; index >= 0; index-- {
		if sweep.moves[index].finalize {
			continue
		}
		if err := restoreQuarantineMove(context.Background(), sweep.moves[index]); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (sweep *preparedJanitorSweep) Finalize() error {
	if sweep == nil {
		return nil
	}
	var result error
	for _, move := range sweep.moves {
		if !move.finalize {
			continue
		}
		if !isDirectOwnedQuarantine(sweep.store, move.container) {
			result = errors.Join(result, ErrOwnership)
			continue
		}
		if err := os.RemoveAll(move.container); err != nil {
			result = errors.Join(result, fmt.Errorf("source artifact: finalize quarantine: %w", err))
			continue
		}
		if err := removeBlobQuarantineLocator(move); err != nil {
			result = errors.Join(result, err)
			continue
		}
		sweep.result.QuarantineFinalized++
	}
	if err := syncDirectory(sweep.store.quarantine); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func restoreQuarantineMove(ctx context.Context, move quarantineMove) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Lstat(move.target); errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(move.container)
		return removeBlobQuarantineLocator(move)
	} else if err != nil {
		return fmt.Errorf("source artifact: inspect quarantine restore target: %w", err)
	}
	if _, err := os.Lstat(move.original); err == nil {
		if move.kind == "blob" {
			if err := verifyFileDigest(ctx, move.original, move.digest, -1); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(move.container); err != nil {
			return fmt.Errorf("source artifact: remove duplicate quarantine: %w", err)
		}
		return errors.Join(
			syncDirectory(filepath.Dir(move.container)),
			removeBlobQuarantineLocator(move),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: inspect quarantine restore destination: %w", err)
	}
	if err := os.Rename(move.target, move.original); err != nil {
		return fmt.Errorf("source artifact: restore quarantined %s: %w", move.kind, err)
	}
	if err := syncDirectory(filepath.Dir(move.original)); err != nil {
		return err
	}
	if err := os.Remove(move.container); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: remove restored quarantine directory: %w", err)
	}
	return errors.Join(
		syncDirectory(filepath.Dir(move.container)),
		removeBlobQuarantineLocator(move),
	)
}

func (s *FSStore) blobQuarantineLocatorPath(digest string) (string, error) {
	if s == nil || !digestRE.MatchString(digest) {
		return "", ErrInvalidDigest
	}
	hexDigest := strings.TrimPrefix(digest, digestPrefix)
	return filepath.Join(s.root, blobQuarantineIndexDirectory, hexDigest[:2], hexDigest), nil
}

func (s *FSStore) installBlobQuarantineLocator(move quarantineMove) (string, error) {
	locator, err := s.blobQuarantineLocatorPath(move.digest)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(locator)
	indexRoot := filepath.Dir(directory)
	if err := ensureDirectory(indexRoot, 0o750); err != nil {
		return "", fmt.Errorf("source artifact: initialize quarantine index: %w", err)
	}
	if err := syncDirectory(s.root); err != nil {
		return "", err
	}
	if err := ensureDirectory(directory, 0o750); err != nil {
		return "", fmt.Errorf("source artifact: initialize quarantine index shard: %w", err)
	}
	if err := syncDirectory(indexRoot); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(locator, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			_, readErr := s.readBlobQuarantineLocator(move.digest)
			if readErr == nil {
				return "", errBlobAlreadyQuarantined
			}
			if errors.Is(readErr, errIncompleteQuarantine) {
				// Another lifecycle pass may be between durable locator
				// publication and the canonical rename. Grace-aged recovery
				// owns cleanup; this path must not race the active rename.
				return "", errBlobAlreadyQuarantined
			}
			// An interrupted locator write or a crash after quarantine
			// cleanup is safely repairable only while the verified canonical
			// blob still exists.
			if verifyErr := verifyFileDigest(context.Background(), move.original, move.digest, -1); verifyErr != nil {
				return "", errors.Join(
					fmt.Errorf("%w: invalid blob quarantine locator", ErrIntegrity),
					readErr,
					verifyErr,
				)
			}
			if removeErr := os.Remove(locator); removeErr != nil {
				return "", fmt.Errorf("source artifact: remove stale quarantine locator: %w", removeErr)
			}
			if syncErr := syncDirectory(directory); syncErr != nil {
				return "", syncErr
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("source artifact: create quarantine locator: %w", err)
		}
		_, writeErr := io.WriteString(file, filepath.Base(move.container))
		syncErr := file.Sync()
		closeErr := file.Close()
		if joined := errors.Join(writeErr, syncErr, closeErr); joined != nil {
			_ = os.Remove(locator)
			return "", fmt.Errorf("source artifact: persist quarantine locator: %w", joined)
		}
		if err := syncDirectory(directory); err != nil {
			_ = os.Remove(locator)
			return "", err
		}
		return locator, nil
	}
	return "", fmt.Errorf("%w: could not repair blob quarantine locator", ErrIntegrity)
}

func (s *FSStore) readBlobQuarantineLocator(digest string) (quarantineMove, error) {
	locator, err := s.blobQuarantineLocatorPath(digest)
	if err != nil {
		return quarantineMove{}, err
	}
	if err := validateBlobQuarantineLocatorParents(locator, digest); err != nil {
		return quarantineMove{}, err
	}
	pathInfo, err := os.Lstat(locator)
	if err != nil {
		return quarantineMove{}, fmt.Errorf("source artifact: inspect quarantine locator: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		pathInfo.Size() <= 0 ||
		pathInfo.Size() > maxQuarantineLocatorBytes {
		return quarantineMove{}, fmt.Errorf("%w: malformed blob quarantine locator", ErrIntegrity)
	}
	file, err := os.Open(locator)
	if err != nil {
		return quarantineMove{}, fmt.Errorf("source artifact: open quarantine locator: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil ||
		!os.SameFile(pathInfo, info) ||
		!info.Mode().IsRegular() ||
		info.Size() <= 0 ||
		info.Size() > maxQuarantineLocatorBytes {
		_ = file.Close()
		return quarantineMove{}, fmt.Errorf("%w: malformed blob quarantine locator", ErrIntegrity)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxQuarantineLocatorBytes+1))
	closeErr := file.Close()
	if joined := errors.Join(readErr, closeErr); joined != nil {
		return quarantineMove{}, fmt.Errorf("source artifact: read quarantine locator: %w", joined)
	}
	if int64(len(payload)) > maxQuarantineLocatorBytes {
		return quarantineMove{}, fmt.Errorf("%w: oversized blob quarantine locator", ErrIntegrity)
	}
	containerName := string(payload)
	if filepath.Base(containerName) != containerName ||
		!strings.HasPrefix(
			containerName,
			quarantinePrefix+"blob-"+strings.TrimPrefix(digest, digestPrefix)+"-",
		) {
		return quarantineMove{}, fmt.Errorf("%w: malformed blob quarantine locator target", ErrIntegrity)
	}
	move, err := s.readQuarantineMove(containerName)
	if err != nil {
		return quarantineMove{}, err
	}
	if move.digest != digest || move.locator != locator {
		return quarantineMove{}, fmt.Errorf("%w: blob quarantine locator digest mismatch", ErrIntegrity)
	}
	return move, nil
}

func removeBlobQuarantineLocator(move quarantineMove) error {
	if move.kind != "blob" || move.locator == "" {
		return nil
	}
	pathInfo, err := os.Lstat(move.locator)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("source artifact: inspect quarantine locator for cleanup: %w", err)
	}
	if err := validateBlobQuarantineLocatorParents(move.locator, move.digest); err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		pathInfo.Size() <= 0 ||
		pathInfo.Size() > maxQuarantineLocatorBytes {
		return fmt.Errorf("%w: malformed blob quarantine locator", ErrIntegrity)
	}
	file, err := os.Open(move.locator)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("source artifact: open quarantine locator for cleanup: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil || !os.SameFile(pathInfo, info) {
		_ = file.Close()
		return fmt.Errorf("%w: blob quarantine locator changed during cleanup", ErrIntegrity)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxQuarantineLocatorBytes+1))
	closeErr := file.Close()
	if joined := errors.Join(readErr, closeErr); joined != nil {
		return fmt.Errorf("source artifact: read quarantine locator for cleanup: %w", joined)
	}
	if string(payload) != filepath.Base(move.container) {
		// A newer locator must never be removed by cleanup of an older move.
		return nil
	}
	if err := os.Remove(move.locator); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: remove quarantine locator: %w", err)
	}
	return syncDirectory(filepath.Dir(move.locator))
}

func removeInterruptedBlobQuarantineLocator(move quarantineMove, cutoff time.Time) error {
	if move.kind != "blob" || move.locator == "" {
		return nil
	}
	pathInfo, err := os.Lstat(move.locator)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("source artifact: inspect interrupted quarantine locator: %w", err)
	}
	if err := validateBlobQuarantineLocatorParents(move.locator, move.digest); err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		cutoff.IsZero() ||
		!pathInfo.ModTime().UTC().Before(cutoff) ||
		pathInfo.Size() > maxQuarantineLocatorBytes {
		return fmt.Errorf("%w: interrupted quarantine locator is not an old owned file", ErrOwnership)
	}
	file, err := os.Open(move.locator)
	if err != nil {
		return fmt.Errorf("source artifact: open interrupted quarantine locator: %w", err)
	}
	info, statErr := file.Stat()
	if statErr != nil || !os.SameFile(pathInfo, info) {
		_ = file.Close()
		return fmt.Errorf("%w: interrupted quarantine locator changed during cleanup", ErrIntegrity)
	}
	payload, readErr := io.ReadAll(io.LimitReader(file, maxQuarantineLocatorBytes+1))
	closeErr := file.Close()
	if joined := errors.Join(readErr, closeErr); joined != nil {
		return fmt.Errorf("source artifact: read interrupted quarantine locator: %w", joined)
	}
	containerName := string(payload)
	if containerName != filepath.Base(move.container) {
		if filepath.Base(containerName) == containerName &&
			strings.HasPrefix(
				containerName,
				quarantinePrefix+"blob-"+strings.TrimPrefix(move.digest, digestPrefix)+"-",
			) {
			// A valid locator for another same-digest container is not stale
			// evidence owned by this interrupted creation.
			return nil
		}
		// Empty/partial locator bytes can only be discarded after both the
		// locator and matching empty container have observed grace.
	}
	if err := os.Remove(move.locator); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: remove interrupted quarantine locator: %w", err)
	}
	return syncDirectory(filepath.Dir(move.locator))
}

func validateBlobQuarantineLocatorParents(locator, digest string) error {
	if !digestRE.MatchString(digest) {
		return ErrInvalidDigest
	}
	hexDigest := strings.TrimPrefix(digest, digestPrefix)
	shard := filepath.Dir(locator)
	indexRoot := filepath.Dir(shard)
	if filepath.Base(locator) != hexDigest ||
		filepath.Base(shard) != hexDigest[:2] ||
		filepath.Base(indexRoot) != blobQuarantineIndexDirectory {
		return ErrOwnership
	}
	for _, directory := range []string{indexRoot, shard} {
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("source artifact: inspect quarantine index ownership: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: quarantine index path is not an owned directory", ErrOwnership)
		}
	}
	return nil
}

func ownedJanitorPath(store *FSStore, candidate, kind string) bool {
	if store == nil {
		return false
	}
	candidate = filepath.Clean(candidate)
	switch kind {
	case "blob":
		return isBelowRoot(store.blobs, candidate) &&
			strings.HasSuffix(filepath.Base(candidate), ".tar.gz")
	case "staging":
		return filepath.Dir(candidate) == store.staging &&
			strings.HasPrefix(filepath.Base(candidate), stagingPrefix)
	case "checkout":
		return filepath.Dir(candidate) == store.checkouts &&
			strings.HasPrefix(filepath.Base(candidate), checkoutPrefix)
	default:
		return false
	}
}

func isDirectOwnedQuarantine(store *FSStore, candidate string) bool {
	return store != nil &&
		filepath.Dir(filepath.Clean(candidate)) == store.quarantine &&
		strings.HasPrefix(filepath.Base(candidate), quarantinePrefix)
}
