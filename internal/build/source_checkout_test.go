// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/sourceartifact"
)

func TestSourceCheckoutPlannerSkipCloneCopiesLocalDir(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "app", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	planner := SourceCheckoutPlanner{Root: root, SkipClone: true}
	checkout, err := planner.Checkout(context.Background(), "svc-x", SourceRef{Repo: src, Ref: "main"})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer func() { _ = checkout.Discard() }()
	if !strings.HasPrefix(checkout.RepoDir, root) {
		t.Fatalf("checkout repo dir = %s, want under %s", checkout.RepoDir, root)
	}
	if _, err := os.Stat(filepath.Join(checkout.WorkDir, "Dockerfile")); err != nil {
		t.Fatalf("expected Dockerfile in checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout.WorkDir, "app", "main.go")); err != nil {
		t.Fatalf("expected nested file in checkout: %v", err)
	}
}

func TestSourceCheckoutPlannerSubpath(t *testing.T) {
	src := t.TempDir()
	apps := filepath.Join(src, "apps", "web")
	if err := os.MkdirAll(apps, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apps, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	planner := SourceCheckoutPlanner{Root: root, SkipClone: true}
	checkout, err := planner.Checkout(context.Background(), "svc-x", SourceRef{Repo: src, Ref: "main", Subpath: "apps/web"})
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer func() { _ = checkout.Discard() }()
	if filepath.Base(checkout.WorkDir) != "web" {
		t.Fatalf("checkout work dir = %s, want apps/web", checkout.WorkDir)
	}
	if _, err := os.Stat(filepath.Join(checkout.WorkDir, "Dockerfile")); err != nil {
		t.Fatalf("expected Dockerfile in subpath: %v", err)
	}
}

func TestSourceCheckoutPlannerSubpathMissing(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	planner := SourceCheckoutPlanner{Root: root, SkipClone: true}
	_, err := planner.Checkout(context.Background(), "svc-x", SourceRef{Repo: src, Ref: "main", Subpath: "missing"})
	if err == nil {
		t.Fatal("Checkout should fail when subpath does not exist")
	}
}

func TestSourceCheckoutPlannerFetchesProviderRefAndVerifiesExactCommit(t *testing.T) {
	work := t.TempDir()
	runGitForTest(t, work, "init")
	runGitForTest(t, work, "config", "user.name", "Midnight Test")
	runGitForTest(t, work, "config", "user.email", "midnight@example.test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("preview\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, work, "add", "README.md")
	runGitForTest(t, work, "commit", "-m", "preview head")
	sha := strings.TrimSpace(runGitForTest(t, work, "rev-parse", "HEAD"))
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, "", "init", "--bare", remote)
	runGitForTest(t, work, "push", remote, "HEAD:refs/pull/42/head")

	planner := SourceCheckoutPlanner{Root: t.TempDir()}
	checkout, err := planner.Checkout(context.Background(), "svc-preview", SourceRef{
		Repo: remote, Ref: sha, FetchRef: "refs/pull/42/head",
	})
	if err != nil {
		t.Fatalf("Checkout() error = %v", err)
	}
	defer func() { _ = checkout.Discard() }()
	if checkout.CommitID != sha {
		t.Fatalf("checkout commit = %q, want %q", checkout.CommitID, sha)
	}

	_, err = planner.Checkout(context.Background(), "svc-stale", SourceRef{
		Repo: remote, Ref: strings.Repeat("b", 40), FetchRef: "refs/pull/42/head",
	})
	if err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("stale Checkout() error = %v, want exact-pin rejection", err)
	}
}

func runGitForTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestSourceCheckoutPlannerMaterializesArtifactPerAttempt(t *testing.T) {
	ctx := context.Background()
	artifactRoot := t.TempDir()
	store, err := sourceartifact.NewFSStore(artifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	payload := buildArtifactArchive(t, map[string]string{
		"apps/web/Dockerfile": "FROM scratch\n",
		"README.md":           "midnight\n",
	})
	staged, err := store.Stage(ctx, bytes.NewReader(payload), sourceartifact.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.Commit(ctx, staged)
	if err != nil {
		t.Fatal(err)
	}
	planner := SourceCheckoutPlanner{
		Root:         t.TempDir(),
		ArtifactRoot: artifactRoot,
	}
	ref := SourceRef{Repo: artifact.URI, Subpath: "apps/web"}
	first, err := planner.Checkout(ctx, "svc-web", ref)
	if err != nil {
		t.Fatalf("first Checkout: %v", err)
	}
	second, err := planner.Checkout(ctx, "svc-web", ref)
	if err != nil {
		t.Fatalf("second Checkout: %v", err)
	}
	if first.RepoDir == second.RepoDir {
		t.Fatal("artifact attempts shared one mutable checkout")
	}
	if first.CommitID != strings.TrimPrefix(artifact.Digest, "sha256:") {
		t.Fatalf("commit id = %q, want artifact digest", first.CommitID)
	}
	if _, err := os.Stat(filepath.Join(first.WorkDir, "Dockerfile")); err != nil {
		t.Fatalf("first artifact workdir: %v", err)
	}
	if err := first.Discard(); err != nil {
		t.Fatalf("first Discard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second.WorkDir, "Dockerfile")); err != nil {
		t.Fatalf("first Discard changed concurrent checkout: %v", err)
	}
	if err := second.Discard(); err != nil {
		t.Fatalf("second Discard: %v", err)
	}
	blobs, err := store.ListBlobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].Digest != artifact.Digest {
		t.Fatalf("artifact blob changed by checkout cleanup: %#v", blobs)
	}
}

func TestSourceCheckoutPlannerArtifactRequiresSharedRoot(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	uri, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (SourceCheckoutPlanner{}).Checkout(context.Background(), "svc-web", SourceRef{Repo: uri})
	if err == nil || !strings.Contains(err.Error(), "artifact root is required") {
		t.Fatalf("Checkout error = %v, want missing artifact root", err)
	}
}

func TestSourceCheckoutPlannerArtifactRefMustMatchDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	uri, err := sourceartifact.URI(digest)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (SourceCheckoutPlanner{ArtifactRoot: t.TempDir()}).Checkout(
		context.Background(),
		"svc-web",
		SourceRef{Repo: uri, Ref: "sha256:" + strings.Repeat("b", 64)},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Checkout error = %v, want artifact identity mismatch", err)
	}
}

func buildArtifactArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, body := range files {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o640,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(body)); err != nil {
			t.Fatal(err)
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
