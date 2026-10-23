// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package sourceartifact stores tenant source uploads as immutable,
// content-addressed gzip/tar blobs and materializes isolated build checkouts.
//
// PostgresStore.RunJanitor provides the serialized lifecycle boundary used by
// the production build worker. Direct FSStore.Prune calls are reserved for
// offline/operator maintenance.
package sourceartifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// DefaultRoot is shared by the packaged API server and build worker.
	DefaultRoot = "/var/lib/midnight/uploads"

	// MaxArchivePathBytes and the component bounds limit archive namespace
	// allocation before any path reaches the host filesystem.
	MaxArchivePathBytes    = 4_096
	MaxArchivePathDepth    = 64
	MaxArchiveSegmentBytes = 255

	uriPrefix        = "sourceartifact://sha256:"
	digestPrefix     = "sha256:"
	stagingPrefix    = "sourceartifact-stage-"
	checkoutPrefix   = "sourceartifact-checkout-"
	quarantinePrefix = "sourceartifact-quarantine-"
)

var (
	// DefaultLimits bounds both upload validation and worker extraction.
	DefaultLimits = Limits{
		MaxCompressedBytes:   50 << 20,
		MaxEntries:           10_000,
		MaxUncompressedBytes: 512 << 20,
	}

	ErrInvalidDigest  = errors.New("source artifact: invalid digest")
	ErrInvalidArchive = errors.New("source artifact: invalid archive")
	ErrLimitExceeded  = errors.New("source artifact: archive limit exceeded")
	ErrIntegrity      = errors.New("source artifact: integrity verification failed")
	ErrOwnership      = errors.New("source artifact: staged object is not owned by this store")

	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	hexRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Limits bounds compressed input, archive entries, and the complete expanded
// tar stream (headers, metadata, padding, and file bytes).
type Limits struct {
	MaxCompressedBytes   int64
	MaxEntries           int
	MaxUncompressedBytes int64
}

// Artifact is immutable metadata derived from the exact compressed bytes.
type Artifact struct {
	Digest            string
	CompressedBytes   int64
	UncompressedBytes int64
	EntryCount        int
	StorageKey        string
	URI               string
}

// Staged is a validated but unpublished upload owned by one FSStore.
type Staged struct {
	Digest            string
	CompressedBytes   int64
	UncompressedBytes int64
	EntryCount        int

	root string
	path string
}

// Checkout is an isolated, per-materialization extraction. Release removes
// only this checkout and is safe to call repeatedly.
type Checkout struct {
	Path    string
	Digest  string
	Release func() error
}

// Blob describes one canonical filesystem blob.
type Blob struct {
	Digest     string
	StorageKey string
	Path       string
	Size       int64
	ModifiedAt time.Time
}

// PruneResult reports only content-addressed blobs removed by Prune.
type PruneResult struct {
	Examined int
	Removed  []string
}

// FSStore confines every path below one absolute, non-symlink root.
type FSStore struct {
	root       string
	staging    string
	blobRoot   string
	blobs      string
	checkouts  string
	quarantine string

	// afterMaterializeVerify is a deterministic race-test seam. Production
	// stores leave it nil.
	afterMaterializeVerify func()
	afterDuplicateVerify   func()

	janitorScanMu             sync.Mutex
	janitorScanCategory       int
	janitorScanHandles        map[string]*os.File
	janitorBlobPrefix         int
	janitorLegacyScanCategory int
}

// NewFSStore validates and creates the shared source-artifact layout.
func NewFSStore(root string) (*FSStore, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("source artifact: root must be an absolute path")
	}
	if err := ensureDirectory(root, 0o750); err != nil {
		return nil, fmt.Errorf("source artifact: root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("source artifact: resolve root: %w", err)
	}
	root = resolved
	store := &FSStore{
		root:       root,
		staging:    filepath.Join(root, "staging"),
		blobRoot:   filepath.Join(root, "blobs"),
		blobs:      filepath.Join(root, "blobs", "sha256"),
		checkouts:  filepath.Join(root, "checkouts"),
		quarantine: filepath.Join(root, "quarantine"),
	}
	for _, directory := range []string{store.staging, store.blobRoot, store.blobs, store.checkouts, store.quarantine} {
		if err := ensureDirectory(directory, 0o750); err != nil {
			return nil, fmt.Errorf("source artifact: initialize layout: %w", err)
		}
	}
	return store, nil
}

// Root returns the canonical shared root used by this store.
func (s *FSStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Stage streams one archive into private staging, hashes the exact compressed
// bytes, fsyncs them, and validates the full gzip/tar before returning.
func (s *FSStore) Stage(ctx context.Context, reader io.Reader, limits Limits) (Staged, error) {
	if err := s.ready(); err != nil {
		return Staged{}, err
	}
	if reader == nil {
		return Staged{}, errors.New("source artifact: reader is required")
	}
	if err := limits.validate(); err != nil {
		return Staged{}, err
	}
	file, err := os.CreateTemp(s.staging, stagingPrefix)
	if err != nil {
		return Staged{}, fmt.Errorf("source artifact: create staging file: %w", err)
	}
	stagedPath := file.Name()
	owned := true
	defer func() {
		_ = file.Close()
		if owned {
			_ = os.Remove(stagedPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return Staged{}, fmt.Errorf("source artifact: secure staging file: %w", err)
	}
	hash := sha256.New()
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: reader}, N: limits.MaxCompressedBytes + 1}
	written, err := io.Copy(io.MultiWriter(file, hash), limited)
	if err != nil {
		return Staged{}, fmt.Errorf("source artifact: stage compressed bytes: %w", err)
	}
	if written == 0 {
		return Staged{}, fmt.Errorf("%w: empty compressed archive", ErrInvalidArchive)
	}
	if written > limits.MaxCompressedBytes {
		return Staged{}, fmt.Errorf("%w: compressed bytes exceed %d", ErrLimitExceeded, limits.MaxCompressedBytes)
	}
	if err := file.Sync(); err != nil {
		return Staged{}, fmt.Errorf("source artifact: fsync staging file: %w", err)
	}
	if err := file.Close(); err != nil {
		return Staged{}, fmt.Errorf("source artifact: close staging file: %w", err)
	}
	stats, err := inspectArchive(ctx, stagedPath, limits, "")
	if err != nil {
		return Staged{}, err
	}
	digest := digestPrefix + hex.EncodeToString(hash.Sum(nil))
	owned = false
	return Staged{
		Digest:            digest,
		CompressedBytes:   written,
		UncompressedBytes: stats.uncompressedBytes,
		EntryCount:        stats.entries,
		root:              s.root,
		path:              stagedPath,
	}, nil
}

// Abort removes only an unpublished staging file owned by this store.
func (s *FSStore) Abort(staged Staged) error {
	if err := s.validateStaged(staged); err != nil {
		return err
	}
	err := os.Remove(staged.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("source artifact: abort staging file: %w", err)
	}
	return syncDirectory(s.staging)
}

// Commit publishes a validated stage at its digest-derived immutable path.
// Existing content is accepted only after a complete digest verification.
func (s *FSStore) Commit(ctx context.Context, staged Staged) (Artifact, error) {
	if err := s.validateStaged(staged); err != nil {
		return Artifact{}, err
	}
	if err := verifyFileDigest(ctx, staged.path, staged.Digest, staged.CompressedBytes); err != nil {
		return Artifact{}, err
	}
	destination, storageKey, err := s.blobPath(staged.Digest)
	if err != nil {
		return Artifact{}, err
	}
	destinationDir := filepath.Dir(destination)
	if err := ensureDirectory(destinationDir, 0o750); err != nil {
		return Artifact{}, fmt.Errorf("source artifact: create digest directory: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		if err := verifyFileDigest(ctx, destination, staged.Digest, staged.CompressedBytes); err != nil {
			return Artifact{}, err
		}
		if s.afterDuplicateVerify != nil {
			s.afterDuplicateVerify()
		}
		now := time.Now().UTC()
		if err := os.Chtimes(destination, now, now); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return Artifact{}, fmt.Errorf("source artifact: refresh duplicate blob claim: %w", err)
			}
			if err := os.Chmod(staged.path, 0o440); err != nil {
				return Artifact{}, fmt.Errorf("source artifact: make replacement blob immutable: %w", err)
			}
			if err := os.Rename(staged.path, destination); err != nil {
				return Artifact{}, fmt.Errorf("source artifact: republish reclaimed blob: %w", err)
			}
		}
		if err := os.Remove(staged.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Artifact{}, fmt.Errorf("source artifact: remove duplicate stage: %w", err)
		}
		if err := syncDirectory(destinationDir); err != nil {
			return Artifact{}, err
		}
		if err := syncDirectory(s.staging); err != nil {
			return Artifact{}, err
		}
		return artifactFromStage(staged, storageKey), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, fmt.Errorf("source artifact: inspect destination: %w", err)
	}
	if err := os.Chmod(staged.path, 0o440); err != nil {
		return Artifact{}, fmt.Errorf("source artifact: make blob immutable: %w", err)
	}
	if err := os.Rename(staged.path, destination); err != nil {
		// A concurrent commit of the same digest is harmless only when the
		// winner's exact compressed content verifies.
		if verifyErr := verifyFileDigest(ctx, destination, staged.Digest, staged.CompressedBytes); verifyErr != nil {
			return Artifact{}, fmt.Errorf("source artifact: publish blob: %w", err)
		}
		_ = os.Remove(staged.path)
	}
	if err := syncDirectory(destinationDir); err != nil {
		return Artifact{}, err
	}
	if err := syncDirectory(s.staging); err != nil {
		return Artifact{}, err
	}
	if err := syncDirectory(filepath.Dir(destinationDir)); err != nil {
		return Artifact{}, err
	}
	return artifactFromStage(staged, storageKey), nil
}

// VerifyArtifact proves that the canonical digest-derived blob is present and
// byte-identical to the accepted artifact metadata. Tenant intake calls this
// while holding the database lifecycle advisory lock immediately before it
// binds durable ownership, closing the publication-to-transaction GC window.
func (s *FSStore) VerifyArtifact(ctx context.Context, artifact Artifact) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := validateArtifact(artifact); err != nil {
		return err
	}
	blobPath, _, err := s.blobPath(artifact.Digest)
	if err != nil {
		return err
	}
	if err := verifyFileDigest(
		ctx,
		blobPath,
		artifact.Digest,
		artifact.CompressedBytes,
	); err != nil {
		return fmt.Errorf("source artifact: verify canonical publication: %w", err)
	}
	return nil
}

// committedArtifactBytesAbsent proves that neither the canonical digest path
// nor its exact durable quarantine locator exists. Callers must hold the
// lifecycle lock so publication, restore, and janitor moves cannot race this
// negative evidence.
func (s *FSStore) committedArtifactBytesAbsent(digest string) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	blobPath, _, err := s.blobPath(digest)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(blobPath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("source artifact quota: inspect canonical blob: %w", err)
	}
	locator, err := s.blobQuarantineLocatorPath(digest)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(locator); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("source artifact quota: inspect quarantine locator: %w", err)
	}
	return true, nil
}

// Materialize re-verifies the compressed blob and extracts it into a unique
// per-purpose checkout. No shared blob is removed by Checkout.Release.
func (s *FSStore) Materialize(ctx context.Context, digest, purpose string) (Checkout, error) {
	if err := s.ready(); err != nil {
		return Checkout{}, err
	}
	blobPath, _, err := s.blobPath(digest)
	if err != nil {
		return Checkout{}, err
	}
	if err := ensureDirectory(filepath.Dir(blobPath), 0o750); err != nil {
		return Checkout{}, fmt.Errorf("%w: invalid blob prefix directory", ErrIntegrity)
	}
	blob, err := openVerifiedBlob(ctx, blobPath, digest, -1)
	if err != nil {
		return Checkout{}, err
	}
	defer blob.Close()
	if s.afterMaterializeVerify != nil {
		s.afterMaterializeVerify()
	}
	purpose = sanitizePurpose(purpose)
	if purpose == "" {
		return Checkout{}, errors.New("source artifact: checkout purpose is required")
	}
	checkoutPath, err := os.MkdirTemp(s.checkouts, checkoutPrefix+purpose+"-")
	if err != nil {
		return Checkout{}, fmt.Errorf("source artifact: create checkout: %w", err)
	}
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(checkoutPath)
		}
	}()
	if _, err := inspectArchiveReader(ctx, blob, DefaultLimits, checkoutPath); err != nil {
		return Checkout{}, err
	}
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			if filepath.Dir(checkoutPath) != s.checkouts ||
				!strings.HasPrefix(filepath.Base(checkoutPath), checkoutPrefix) {
				releaseErr = ErrOwnership
				return
			}
			releaseErr = os.RemoveAll(checkoutPath)
		})
		return releaseErr
	}
	owned = false
	return Checkout{Path: checkoutPath, Digest: digest, Release: release}, nil
}

// ListBlobs enumerates only canonical regular blob paths. Unexpected entries
// fail closed instead of being treated as garbage.
func (s *FSStore) ListBlobs(ctx context.Context) ([]Blob, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	var blobs []Blob
	prefixEntries, err := os.ReadDir(s.blobs)
	if err != nil {
		return nil, fmt.Errorf("source artifact: list blob prefixes: %w", err)
	}
	for _, prefixEntry := range prefixEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if prefixEntry.Type()&os.ModeSymlink != 0 || !prefixEntry.IsDir() || len(prefixEntry.Name()) != 2 {
			return nil, fmt.Errorf("%w: unexpected blob prefix entry", ErrIntegrity)
		}
		prefixPath := filepath.Join(s.blobs, prefixEntry.Name())
		entries, err := os.ReadDir(prefixPath)
		if err != nil {
			return nil, fmt.Errorf("source artifact: list digest directory: %w", err)
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
				return nil, fmt.Errorf("%w: unexpected blob entry", ErrIntegrity)
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".tar.gz") {
				return nil, fmt.Errorf("%w: unexpected blob filename", ErrIntegrity)
			}
			hexDigest := strings.TrimSuffix(name, ".tar.gz")
			if !hexRE.MatchString(hexDigest) || !strings.HasPrefix(hexDigest, prefixEntry.Name()) {
				return nil, fmt.Errorf("%w: malformed blob filename", ErrIntegrity)
			}
			info, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("source artifact: inspect blob: %w", err)
			}
			digest := digestPrefix + hexDigest
			storageKey, _ := StorageKey(digest)
			blobs = append(blobs, Blob{
				Digest:     digest,
				StorageKey: storageKey,
				Path:       filepath.Join(prefixPath, name),
				Size:       info.Size(),
				ModifiedAt: info.ModTime().UTC(),
			})
		}
	}
	return blobs, nil
}

// Prune removes only old, unreferenced, fully verified canonical blobs. It
// deliberately ignores staging and checkouts. Callers should pass a current
// active-reference snapshot and a conservative cutoff.
func (s *FSStore) Prune(ctx context.Context, referencedDigests map[string]struct{}, cutoff time.Time) (PruneResult, error) {
	blobs, err := s.ListBlobs(ctx)
	if err != nil {
		return PruneResult{}, err
	}
	result := PruneResult{Examined: len(blobs)}
	for _, blob := range blobs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, referenced := referencedDigests[blob.Digest]; referenced {
			continue
		}
		if cutoff.IsZero() || !blob.ModifiedAt.Before(cutoff) {
			continue
		}
		if err := verifyFileDigest(ctx, blob.Path, blob.Digest, blob.Size); err != nil {
			return result, err
		}
		if err := os.Remove(blob.Path); err != nil {
			return result, fmt.Errorf("source artifact: prune blob: %w", err)
		}
		if err := syncDirectory(filepath.Dir(blob.Path)); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, blob.Digest)
	}
	return result, nil
}

// RemoveBlob is the low-level GC-only deletion seam. Request rollback must
// never call it because a content-addressed blob can have another owner.
func (s *FSStore) RemoveBlob(ctx context.Context, digest string) error {
	if err := s.ready(); err != nil {
		return err
	}
	blobPath, _, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := ensureDirectory(filepath.Dir(blobPath), 0o750); err != nil {
		return fmt.Errorf("%w: invalid blob prefix directory", ErrIntegrity)
	}
	if err := verifyFileDigest(ctx, blobPath, digest, -1); err != nil {
		return err
	}
	if err := os.Remove(blobPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: remove blob: %w", err)
	}
	return syncDirectory(filepath.Dir(blobPath))
}

// URI returns the canonical queue-safe source artifact URI.
func URI(digest string) (string, error) {
	if !digestRE.MatchString(digest) {
		return "", ErrInvalidDigest
	}
	return uriPrefix + strings.TrimPrefix(digest, digestPrefix), nil
}

// ParseURI returns the canonical digest represented by a source artifact URI.
func ParseURI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, uriPrefix) {
		return "", ErrInvalidDigest
	}
	digest := digestPrefix + strings.TrimPrefix(value, uriPrefix)
	if !digestRE.MatchString(digest) {
		return "", ErrInvalidDigest
	}
	return digest, nil
}

// StorageKey returns the relative, digest-derived blob location.
func StorageKey(digest string) (string, error) {
	if !digestRE.MatchString(digest) {
		return "", ErrInvalidDigest
	}
	hexDigest := strings.TrimPrefix(digest, digestPrefix)
	return path.Join("blobs", "sha256", hexDigest[:2], hexDigest+".tar.gz"), nil
}

func (s *FSStore) ready() error {
	if s == nil || s.root == "" {
		return errors.New("source artifact: store is not initialized")
	}
	for _, directory := range []string{s.root, s.staging, s.blobRoot, s.blobs, s.checkouts, s.quarantine} {
		if err := ensureDirectory(directory, 0o750); err != nil {
			return err
		}
	}
	return nil
}

func (s *FSStore) validateStaged(staged Staged) error {
	if err := s.ready(); err != nil {
		return err
	}
	if staged.root != s.root || filepath.Dir(staged.path) != s.staging ||
		!strings.HasPrefix(filepath.Base(staged.path), stagingPrefix) ||
		!digestRE.MatchString(staged.Digest) {
		return ErrOwnership
	}
	info, err := os.Lstat(staged.path)
	if err != nil {
		return fmt.Errorf("source artifact: inspect staged file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrOwnership
	}
	return nil
}

func (s *FSStore) blobPath(digest string) (string, string, error) {
	storageKey, err := StorageKey(digest)
	if err != nil {
		return "", "", err
	}
	blobPath := filepath.Join(s.root, filepath.FromSlash(storageKey))
	if !isBelowRoot(s.root, blobPath) {
		return "", "", ErrOwnership
	}
	return blobPath, storageKey, nil
}

func artifactFromStage(staged Staged, storageKey string) Artifact {
	uri, _ := URI(staged.Digest)
	return Artifact{
		Digest:            staged.Digest,
		CompressedBytes:   staged.CompressedBytes,
		UncompressedBytes: staged.UncompressedBytes,
		EntryCount:        staged.EntryCount,
		StorageKey:        storageKey,
		URI:               uri,
	}
}

type archiveStats struct {
	entries           int
	uncompressedBytes int64
}

func inspectArchive(ctx context.Context, archivePath string, limits Limits, destination string) (archiveStats, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return archiveStats{}, fmt.Errorf("source artifact: open archive: %w", err)
	}
	defer file.Close()
	return inspectArchiveReader(ctx, file, limits, destination)
}

func inspectArchiveReader(ctx context.Context, reader io.Reader, limits Limits, destination string) (archiveStats, error) {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return archiveStats{}, fmt.Errorf("%w: gzip header: %v", ErrInvalidArchive, err)
	}
	defer gzipReader.Close()
	expanded := &archiveBoundReader{
		reader: gzipReader,
		limit:  limits.MaxUncompressedBytes,
	}
	tarReader := tar.NewReader(expanded)
	seen := map[string]struct{}{}
	requiredDirectories := map[string]struct{}{}
	regularPaths := map[string]struct{}{}
	stats := archiveStats{}
	for {
		if err := ctx.Err(); err != nil {
			return archiveStats{}, err
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, ErrLimitExceeded) {
			return archiveStats{}, err
		}
		if err != nil {
			return archiveStats{}, fmt.Errorf("%w: tar stream: %v", ErrInvalidArchive, err)
		}
		stats.entries++
		if stats.entries > limits.MaxEntries {
			return archiveStats{}, fmt.Errorf("%w: entry count exceeds %d", ErrLimitExceeded, limits.MaxEntries)
		}
		name, err := safeArchivePath(header.Name)
		if err != nil {
			return archiveStats{}, err
		}
		if _, duplicate := seen[name]; duplicate {
			return archiveStats{}, fmt.Errorf("%w: duplicate path %q", ErrInvalidArchive, name)
		}
		seen[name] = struct{}{}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, fileConflict := regularPaths[parent]; fileConflict {
				return archiveStats{}, fmt.Errorf("%w: file %q is an archive parent", ErrInvalidArchive, parent)
			}
			if _, required := requiredDirectories[parent]; required {
				continue
			}
			if len(requiredDirectories) >= limits.MaxEntries {
				return archiveStats{}, fmt.Errorf(
					"%w: implicit directory count exceeds %d",
					ErrLimitExceeded,
					limits.MaxEntries,
				)
			}
			requiredDirectories[parent] = struct{}{}
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return archiveStats{}, fmt.Errorf("%w: directory %q has file content", ErrInvalidArchive, name)
			}
			if destination != "" {
				if err := os.MkdirAll(filepath.Join(destination, filepath.FromSlash(name)), directoryMode(header.Mode)); err != nil {
					return archiveStats{}, fmt.Errorf("source artifact: create checkout directory: %w", err)
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if _, requiredDirectory := requiredDirectories[name]; requiredDirectory {
				return archiveStats{}, fmt.Errorf("%w: path %q is both a file and directory", ErrInvalidArchive, name)
			}
			regularPaths[name] = struct{}{}
			if header.Size < 0 || header.Size > limits.MaxUncompressedBytes-stats.uncompressedBytes {
				return archiveStats{}, fmt.Errorf("%w: expanded bytes exceed %d", ErrLimitExceeded, limits.MaxUncompressedBytes)
			}
			stats.uncompressedBytes += header.Size
			if destination == "" {
				if _, err := io.CopyN(io.Discard, tarReader, header.Size); err != nil {
					if errors.Is(err, ErrLimitExceeded) {
						return archiveStats{}, err
					}
					return archiveStats{}, fmt.Errorf("%w: truncated file %q", ErrInvalidArchive, name)
				}
				continue
			}
			target := filepath.Join(destination, filepath.FromSlash(name))
			if !isBelowRoot(destination, target) {
				return archiveStats{}, ErrOwnership
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return archiveStats{}, fmt.Errorf("source artifact: create checkout parent: %w", err)
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode(header.Mode))
			if err != nil {
				return archiveStats{}, fmt.Errorf("source artifact: create checkout file: %w", err)
			}
			_, copyErr := io.CopyN(out, tarReader, header.Size)
			syncErr := out.Sync()
			closeErr := out.Close()
			if copyErr != nil {
				if errors.Is(copyErr, ErrLimitExceeded) {
					return archiveStats{}, copyErr
				}
				return archiveStats{}, fmt.Errorf("%w: truncated file %q", ErrInvalidArchive, name)
			}
			if syncErr != nil {
				return archiveStats{}, fmt.Errorf("source artifact: fsync checkout file: %w", syncErr)
			}
			if closeErr != nil {
				return archiveStats{}, fmt.Errorf("source artifact: close checkout file: %w", closeErr)
			}
		default:
			return archiveStats{}, fmt.Errorf("%w: path %q uses forbidden tar type %d", ErrInvalidArchive, name, header.Typeflag)
		}
	}
	if stats.entries == 0 {
		return archiveStats{}, fmt.Errorf("%w: archive has no entries", ErrInvalidArchive)
	}
	trailingBytes, err := io.Copy(io.Discard, expanded)
	if errors.Is(err, ErrLimitExceeded) {
		return archiveStats{}, err
	}
	if err != nil {
		return archiveStats{}, fmt.Errorf("%w: gzip trailer: %v", ErrInvalidArchive, err)
	}
	if trailingBytes != 0 {
		return archiveStats{}, fmt.Errorf("%w: trailing decompressed bytes", ErrInvalidArchive)
	}
	return stats, nil
}

// ValidateArchivePath applies the exact namespace rules used by both upload
// staging and worker materialization. Archive producers can call it before
// transmitting bytes so an invalid local path fails without a network upload.
func ValidateArchivePath(name string) error {
	_, err := safeArchivePath(name)
	return err
}

func safeArchivePath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) ||
		strings.HasPrefix(name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("%w: unsafe archive path", ErrInvalidArchive)
	}
	if len(name) > MaxArchivePathBytes {
		return "", fmt.Errorf("%w: archive path exceeds %d bytes", ErrLimitExceeded, MaxArchivePathBytes)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%w: unsafe archive path %q", ErrInvalidArchive, name)
		}
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: unsafe archive path %q", ErrInvalidArchive, name)
	}
	segments := strings.Split(cleaned, "/")
	if len(segments) > MaxArchivePathDepth {
		return "", fmt.Errorf("%w: archive path depth exceeds %d", ErrLimitExceeded, MaxArchivePathDepth)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: unsafe archive path %q", ErrInvalidArchive, name)
		}
		if len(segment) > MaxArchiveSegmentBytes {
			return "", fmt.Errorf("%w: archive path segment exceeds %d bytes", ErrLimitExceeded, MaxArchiveSegmentBytes)
		}
	}
	return cleaned, nil
}

func verifyFileDigest(ctx context.Context, filePath, expectedDigest string, expectedSize int64) error {
	file, err := openVerifiedBlob(ctx, filePath, expectedDigest, expectedSize)
	if err != nil {
		return err
	}
	return file.Close()
}

func openVerifiedBlob(ctx context.Context, filePath, expectedDigest string, expectedSize int64) (*os.File, error) {
	if !digestRE.MatchString(expectedDigest) {
		return nil, ErrInvalidDigest
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect blob: %v", ErrIntegrity, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: blob is not a regular file", ErrIntegrity)
	}
	if info.Size() <= 0 || info.Size() > DefaultLimits.MaxCompressedBytes {
		return nil, fmt.Errorf("%w: blob size is outside the compressed limit", ErrIntegrity)
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		return nil, fmt.Errorf("%w: blob size mismatch", ErrIntegrity)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: open blob: %v", ErrIntegrity, err)
	}
	owned := true
	defer func() {
		if owned {
			_ = file.Close()
		}
	}()
	openedInfo, err := file.Stat()
	if err != nil ||
		openedInfo.Mode()&os.ModeSymlink != 0 ||
		!openedInfo.Mode().IsRegular() ||
		!os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%w: blob identity changed while opening", ErrIntegrity)
	}
	if openedInfo.Size() <= 0 || openedInfo.Size() > DefaultLimits.MaxCompressedBytes {
		return nil, fmt.Errorf("%w: blob size is outside the compressed limit", ErrIntegrity)
	}
	if expectedSize >= 0 && openedInfo.Size() != expectedSize {
		return nil, fmt.Errorf("%w: blob size mismatch", ErrIntegrity)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: file}); err != nil {
		return nil, fmt.Errorf("%w: hash blob: %v", ErrIntegrity, err)
	}
	got := digestPrefix + hex.EncodeToString(hash.Sum(nil))
	if got != expectedDigest {
		return nil, fmt.Errorf("%w: digest mismatch", ErrIntegrity)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("%w: rewind verified blob: %v", ErrIntegrity, err)
	}
	owned = false
	return file, nil
}

func ensureDirectory(directory string, mode fs.FileMode) error {
	if err := os.MkdirAll(directory, mode); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("directory path must not be a symlink or non-directory")
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("source artifact: open directory for fsync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("source artifact: fsync directory: %w", err)
	}
	return nil
}

func isBelowRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func sanitizePurpose(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('-')
		}
		if builder.Len() >= 80 {
			break
		}
	}
	return strings.Trim(builder.String(), "-.")
}

// fileMode normalises an archive entry's permission bits for the extracted
// checkout. A checkout is a Docker build context, not a secret store: Docker
// COPY carries these bits straight into the image, so withholding the
// world-readable bit here silently breaks any image that reads a copied file
// as a non-root user. An executable script extracted 0750 arrives in the image
// as 0751 after a `chmod +x` (the mode bits `+x` cannot take away), and a
// container running `USER app` then gets execute without read — the shell
// reports `cannot open ...: Permission denied` and the container restart-loops.
//
// Host confidentiality is enforced by the checkout root instead, which is
// created 0750 and owned by the service user, so a permissive file mode inside
// it is not reachable by other host users.
func fileMode(mode int64) fs.FileMode {
	value := fs.FileMode(mode).Perm()
	if value&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func directoryMode(_ int64) fs.FileMode {
	return 0o755
}

func (limits Limits) validate() error {
	if limits.MaxCompressedBytes <= 0 || limits.MaxEntries <= 0 || limits.MaxUncompressedBytes <= 0 {
		return errors.New("source artifact: all archive limits must be positive")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

type archiveBoundReader struct {
	reader   io.Reader
	limit    int64
	consumed int64
}

func (reader *archiveBoundReader) Read(buffer []byte) (int, error) {
	remaining := reader.limit - reader.consumed
	if remaining <= 0 {
		var probe [1]byte
		n, err := reader.reader.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("%w: expanded archive exceeds %d", ErrLimitExceeded, reader.limit)
		}
		return 0, err
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := reader.reader.Read(buffer)
	reader.consumed += int64(n)
	return n, err
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
