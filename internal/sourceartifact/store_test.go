// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
}

func TestFSStoreCommitSurvivesRestartAndMaterializesIsolatedCheckouts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	payload := makeArchive(t,
		archiveEntry{name: "Dockerfile", body: "FROM scratch\n", typeflag: tar.TypeReg},
		archiveEntry{name: "app/main.go", body: "package main\n", typeflag: tar.TypeReg},
	)
	store, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	sum := sha256.Sum256(payload)
	wantDigest := "sha256:" + hex.EncodeToString(sum[:])
	if staged.Digest != wantDigest {
		t.Fatalf("digest = %q, want %q", staged.Digest, wantDigest)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	wantKey, _ := StorageKey(wantDigest)
	wantURI, _ := URI(wantDigest)
	if artifact.StorageKey != wantKey || artifact.URI != wantURI ||
		artifact.EntryCount != 2 || artifact.UncompressedBytes != int64(len("FROM scratch\npackage main\n")) {
		t.Fatalf("artifact metadata = %#v", artifact)
	}
	blobPath := filepath.Join(root, filepath.FromSlash(wantKey))
	info, err := os.Stat(blobPath)
	if err != nil {
		t.Fatalf("stat committed blob: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o440 {
		t.Fatalf("blob mode = %o, want 440", got)
	}

	// A fresh store instance models an API/worker process restart.
	restarted, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := restarted.Materialize(ctx, wantDigest, "operation-a")
	if err != nil {
		t.Fatalf("first Materialize: %v", err)
	}
	second, err := restarted.Materialize(ctx, wantDigest, "operation-a")
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if first.Path == second.Path {
		t.Fatal("materializations reused a checkout path")
	}
	if data, err := os.ReadFile(filepath.Join(first.Path, "app", "main.go")); err != nil || string(data) != "package main\n" {
		t.Fatalf("first checkout content = %q, %v", data, err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("idempotent first Release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second.Path, "Dockerfile")); err != nil {
		t.Fatalf("releasing first checkout changed second: %v", err)
	}
	if _, err := os.Stat(blobPath); err != nil {
		t.Fatalf("releasing checkout changed immutable blob: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	duplicate, err := restarted.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatalf("duplicate Stage: %v", err)
	}
	if _, err := restarted.Commit(ctx, duplicate); err != nil {
		t.Fatalf("duplicate Commit: %v", err)
	}
	blobs, err := restarted.ListBlobs(ctx)
	if err != nil {
		t.Fatalf("ListBlobs: %v", err)
	}
	if len(blobs) != 1 || blobs[0].Digest != wantDigest {
		t.Fatalf("blobs = %#v, want one deduplicated blob", blobs)
	}
}

func TestFSStoreMaterializeExtractsTheVerifiedOpenBlobWhenPathIsSwapped(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := makeArchive(t, archiveEntry{
		name: "identity.txt", body: "verified\n", typeflag: tar.TypeReg,
	})
	staged, err := store.Stage(ctx, bytes.NewReader(original), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	blobPath, _, err := store.blobPath(artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	replacement := makeArchive(t, archiveEntry{
		name: "identity.txt", body: "swapped\n", typeflag: tar.TypeReg,
	})
	store.afterMaterializeVerify = func() {
		verifiedPath := blobPath + ".verified"
		if err := os.Rename(blobPath, verifiedPath); err != nil {
			t.Fatalf("swap verified blob: %v", err)
		}
		if err := os.WriteFile(blobPath, replacement, 0o440); err != nil {
			t.Fatalf("publish replacement blob: %v", err)
		}
	}
	checkout, err := store.Materialize(ctx, artifact.Digest, "stable-fd")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	defer checkout.Release()
	got, err := os.ReadFile(filepath.Join(checkout.Path, "identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "verified\n" {
		t.Fatalf("materialized swapped path bytes %q, want verified inode", got)
	}
}

func TestFSStoreRejectsOversizedCanonicalBlobBeforeHashing(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := makeArchive(t, archiveEntry{
		name: "source.txt", body: "bounded", typeflag: tar.TypeReg,
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
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, DefaultLimits.MaxCompressedBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Materialize(ctx, artifact.Digest, "oversized"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Materialize oversized blob error=%v, want ErrIntegrity", err)
	}
}

func TestFSStoreDuplicateCommitRepublishesWhenJanitorQuarantinesAfterVerification(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := makeArchive(t, archiveEntry{
		name: "source.txt", body: "same accepted bytes", typeflag: tar.TypeReg,
	})
	first, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := store.blobPath(artifact.Digest)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	var quarantined quarantineMove
	store.afterDuplicateVerify = func() {
		var hookErr error
		quarantined, hookErr = store.quarantinePath(path, "blob", artifact.Digest)
		if hookErr != nil {
			t.Fatalf("quarantine interleaving: %v", hookErr)
		}
	}
	if _, err := store.Commit(ctx, duplicate); err != nil {
		t.Fatalf("duplicate Commit did not republish after quarantine race: %v", err)
	}
	if err := verifyFileDigest(ctx, path, artifact.Digest, -1); err != nil {
		t.Fatalf("accepted duplicate has no canonical blob: %v", err)
	}
	if err := verifyFileDigest(ctx, quarantined.target, artifact.Digest, -1); err != nil {
		t.Fatalf("quarantine evidence changed unexpectedly: %v", err)
	}
}

func TestFSStoreRejectsUnsafeArchiveEntries(t *testing.T) {
	tests := map[string][]archiveEntry{
		"parent traversal": {
			{name: "../escape", body: "x", typeflag: tar.TypeReg},
		},
		"internal traversal": {
			{name: "app/../escape", body: "x", typeflag: tar.TypeReg},
		},
		"absolute": {
			{name: "/escape", body: "x", typeflag: tar.TypeReg},
		},
		"windows path": {
			{name: `C:\escape`, body: "x", typeflag: tar.TypeReg},
		},
		"symlink": {
			{name: "link", typeflag: tar.TypeSymlink, linkname: "target"},
		},
		"hardlink": {
			{name: "link", typeflag: tar.TypeLink, linkname: "target"},
		},
		"character device": {
			{name: "device", typeflag: tar.TypeChar},
		},
		"block device": {
			{name: "device", typeflag: tar.TypeBlock},
		},
		"fifo": {
			{name: "pipe", typeflag: tar.TypeFifo},
		},
		"duplicate normalized path": {
			{name: "app//main.go", body: "first", typeflag: tar.TypeReg},
			{name: "app/main.go", body: "second", typeflag: tar.TypeReg},
		},
		"file used as parent": {
			{name: "app", body: "file", typeflag: tar.TypeReg},
			{name: "app/main.go", body: "child", typeflag: tar.TypeReg},
		},
		"directory later used as file": {
			{name: "app/main.go", body: "child", typeflag: tar.TypeReg},
			{name: "app", body: "file", typeflag: tar.TypeReg},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Stage(context.Background(), bytes.NewReader(makeArchive(t, entries...)), DefaultLimits)
			if !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("Stage error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestFSStoreEnforcesEveryArchiveLimit(t *testing.T) {
	payload := makeArchive(t,
		archiveEntry{name: "one", body: "1234", typeflag: tar.TypeReg},
		archiveEntry{name: "two", body: "5678", typeflag: tar.TypeReg},
	)
	tests := []struct {
		name   string
		limits Limits
	}{
		{
			name: "compressed bytes",
			limits: Limits{
				MaxCompressedBytes:   int64(len(payload) - 1),
				MaxEntries:           10,
				MaxUncompressedBytes: 1 << 20,
			},
		},
		{
			name: "entry count",
			limits: Limits{
				MaxCompressedBytes:   int64(len(payload)),
				MaxEntries:           1,
				MaxUncompressedBytes: 1 << 20,
			},
		},
		{
			name: "expanded bytes",
			limits: Limits{
				MaxCompressedBytes:   int64(len(payload)),
				MaxEntries:           10,
				MaxUncompressedBytes: 7,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Stage(context.Background(), bytes.NewReader(payload), test.limits)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("Stage error = %v, want ErrLimitExceeded", err)
			}
		})
	}
}

func TestFSStoreStageAndMaterializeEnforceIdenticalNamespaceBounds(t *testing.T) {
	fanoutEntries := DefaultLimits.MaxEntries/(MaxArchivePathDepth-1) + 1
	fanout := make([]archiveEntry, 0, fanoutEntries)
	for index := 0; index < fanoutEntries; index++ {
		fanout = append(fanout, archiveEntry{
			name: fmt.Sprintf(
				"parent-%05d/%ssource",
				index,
				strings.Repeat("d/", MaxArchivePathDepth-2),
			),
			typeflag: tar.TypeReg,
		})
	}
	tests := []struct {
		name    string
		entries []archiveEntry
		message string
	}{
		{
			name: "path bytes",
			entries: []archiveEntry{{
				name:     boundedArchivePath(MaxArchivePathBytes + 1),
				typeflag: tar.TypeReg,
			}},
			message: "archive path exceeds 4096 bytes",
		},
		{
			name: "segment bytes",
			entries: []archiveEntry{{
				name:     strings.Repeat("s", MaxArchiveSegmentBytes+1),
				typeflag: tar.TypeReg,
			}},
			message: "archive path segment exceeds 255 bytes",
		},
		{
			name: "path depth",
			entries: []archiveEntry{{
				name:     strings.Repeat("d/", MaxArchivePathDepth) + "source",
				typeflag: tar.TypeReg,
			}},
			message: "archive path depth exceeds 64",
		},
		{
			name:    "implicit directory fanout",
			entries: fanout,
			message: "implicit directory count exceeds 10000",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewFSStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			payload := makeArchive(t, test.entries...)
			_, stageErr := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
			if !errors.Is(stageErr, ErrLimitExceeded) {
				t.Fatalf("Stage error = %v, want ErrLimitExceeded", stageErr)
			}
			digest := publishUncheckedArchive(t, store, payload)
			_, materializeErr := store.Materialize(ctx, digest, "namespace-bounds")
			if !errors.Is(materializeErr, ErrLimitExceeded) {
				t.Fatalf("Materialize error = %v, want ErrLimitExceeded", materializeErr)
			}
			if stageErr.Error() != materializeErr.Error() {
				t.Fatalf("Stage error = %q, Materialize error = %q", stageErr, materializeErr)
			}
			if !strings.Contains(stageErr.Error(), test.message) {
				t.Fatalf("error = %q, want message containing %q", stageErr, test.message)
			}
		})
	}
}

func TestFSStoreAbortOwnershipAndTamperDetection(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := makeArchive(t, archiveEntry{name: "Dockerfile", body: "FROM scratch\n", typeflag: tar.TypeReg})
	staged, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Abort(staged); !errors.Is(err, ErrOwnership) {
		t.Fatalf("cross-store Abort error = %v, want ErrOwnership", err)
	}
	if err := store.Abort(staged); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := os.Stat(staged.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging file survives Abort: %v", err)
	}

	staged, err = store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(store.root, filepath.FromSlash(artifact.StorageKey))
	if err := os.Chmod(blobPath, 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(blobPath, data, 0o440); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Materialize(ctx, artifact.Digest, "tampered"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Materialize tampered blob error = %v, want ErrIntegrity", err)
	}
}

func TestFSStoreRejectsSymlinkedDigestPrefix(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFSStore(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := makeArchive(t, archiveEntry{name: "Dockerfile", body: "FROM scratch\n", typeflag: tar.TypeReg})
	staged, err := store.Stage(ctx, bytes.NewReader(payload), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(root, filepath.FromSlash(artifact.StorageKey))
	prefixPath := filepath.Dir(blobPath)
	savedPath := prefixPath + "-saved"
	if err := os.Rename(prefixPath, savedPath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, prefixPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Materialize(ctx, artifact.Digest, "symlink-prefix"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Materialize error = %v, want ErrIntegrity", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%v err=%v", entries, err)
	}
}

func TestFSStorePruneIsAgeGatedReferenceAwareAndFailClosed(t *testing.T) {
	ctx := context.Background()
	store, err := NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	commit := func(name string) Artifact {
		t.Helper()
		staged, stageErr := store.Stage(ctx, bytes.NewReader(makeArchive(t,
			archiveEntry{name: name, body: name, typeflag: tar.TypeReg},
		)), DefaultLimits)
		if stageErr != nil {
			t.Fatal(stageErr)
		}
		artifact, commitErr := store.Commit(ctx, staged)
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		return artifact
	}
	referenced := commit("referenced")
	oldOrphan := commit("orphan")
	recentOrphan := commit("recent")
	oldTime := time.Now().Add(-2 * time.Hour)
	for _, artifact := range []Artifact{referenced, oldOrphan} {
		blobPath := filepath.Join(store.root, filepath.FromSlash(artifact.StorageKey))
		if err := os.Chtimes(blobPath, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Prune(ctx, map[string]struct{}{referenced.Digest: {}}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != oldOrphan.Digest {
		t.Fatalf("removed = %v, want only old orphan", result.Removed)
	}
	for _, artifact := range []Artifact{referenced, recentOrphan} {
		if _, err := os.Stat(filepath.Join(store.root, filepath.FromSlash(artifact.StorageKey))); err != nil {
			t.Fatalf("preserved blob %s missing: %v", artifact.Digest, err)
		}
	}

	corrupt := commit("corrupt")
	corruptPath := filepath.Join(store.root, filepath.FromSlash(corrupt.StorageKey))
	if err := os.Chmod(corruptPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte("not the artifact"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(corruptPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Prune(ctx, map[string]struct{}{referenced.Digest: {}}, time.Now().Add(-time.Hour)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Prune corrupt blob error = %v, want ErrIntegrity", err)
	}
	if _, err := os.Stat(corruptPath); err != nil {
		t.Fatalf("fail-closed prune removed corrupt evidence: %v", err)
	}
}

func TestArtifactIdentifiersAndMessagesAreCanonical(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	uri, err := URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, err := ParseURI(uri); err != nil || parsed != digest {
		t.Fatalf("ParseURI = %q, %v", parsed, err)
	}
	for _, invalid := range []string{
		"sha256:" + strings.Repeat("A", 64),
		"sha256:short",
		"md5:" + strings.Repeat("a", 64),
	} {
		if _, err := URI(invalid); !errors.Is(err, ErrInvalidDigest) {
			t.Fatalf("URI(%q) error = %v", invalid, err)
		}
	}
	if err := ValidateMessage(strings.Repeat("x", MaxMessageBytes)); err != nil {
		t.Fatalf("ValidateMessage max bytes: %v", err)
	}
	if err := ValidateMessage(strings.Repeat("x", MaxMessageBytes+1)); err == nil {
		t.Fatal("ValidateMessage accepted an oversized value")
	}
	for _, invalid := range []string{"line\nbreak", "tab\tvalue", string([]byte{0xff})} {
		if err := ValidateMessage(invalid); err == nil {
			t.Fatalf("ValidateMessage(%q) succeeded", invalid)
		}
	}
}

func makeArchive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(0)
		if typeflag == tar.TypeReg || typeflag == tar.TypeRegA {
			size = int64(len(entry.body))
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o640,
			Size:     size,
			Typeflag: typeflag,
			Linkname: entry.linkname,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write tar header %q: %v", entry.name, err)
		}
		if size > 0 {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatalf("write tar body %q: %v", entry.name, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func boundedArchivePath(size int) string {
	const segment = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var builder strings.Builder
	builder.Grow(size)
	for builder.Len() < size {
		if builder.Len() > 0 {
			builder.WriteByte('/')
		}
		remaining := size - builder.Len()
		if remaining > len(segment) {
			remaining = len(segment)
		}
		builder.WriteString(segment[:remaining])
	}
	return builder.String()
}

func publishUncheckedArchive(t *testing.T, store *FSStore, payload []byte) string {
	t.Helper()
	sum := sha256.Sum256(payload)
	digest := digestPrefix + hex.EncodeToString(sum[:])
	blobPath, _, err := store.blobPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobPath, payload, 0o440); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestExtractedCheckoutKeepsFilesReadableForDockerCopy(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	entries := []struct {
		name string
		mode int64
		body string
	}{
		{"entrypoint.sh", 0o755, "#!/bin/sh\nexec \"$@\"\n"},
		{"settings.py", 0o644, "DEBUG = False\n"},
	}
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg, Name: entry.name, Mode: entry.mode, Size: int64(len(entry.body)),
		}); err != nil {
			t.Fatalf("write header %s: %v", entry.name, err)
		}
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatalf("write body %s: %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	destination := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(destination, 0o750); err != nil {
		t.Fatalf("mkdir destination: %v", err)
	}
	if _, err := inspectArchiveReader(
		context.Background(), bytes.NewReader(buf.Bytes()), DefaultLimits, destination,
	); err != nil {
		t.Fatalf("extract: %v", err)
	}

	// Docker COPY carries these bits into the image, so a file that loses the
	// world-readable bit here becomes unreadable to a non-root USER at runtime.
	for _, want := range []struct {
		name string
		mode os.FileMode
	}{
		{"entrypoint.sh", 0o755},
		{"settings.py", 0o644},
	} {
		info, err := os.Stat(filepath.Join(destination, want.name))
		if err != nil {
			t.Fatalf("stat %s: %v", want.name, err)
		}
		if got := info.Mode().Perm(); got != want.mode {
			t.Fatalf("%s mode = %o, want %o", want.name, got, want.mode)
		}
	}
}
