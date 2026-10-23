// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/sourceartifact"
)

// SourceRef describes the repository checkout that feeds a builder. Repo + Ref
// are required; Subpath is optional and, when provided, narrows the build
// context to a subdirectory of the checkout.
type SourceRef struct {
	// Repo is the git URL (or local checkout path) that holds the source.
	Repo string
	// Ref is the git ref (branch, tag, or full commit sha) to check out.
	Ref string
	// FetchRef optionally names the remote ref that must resolve to Ref. It is
	// used for provider-owned refs such as GitHub's read-only pull refs.
	FetchRef string
	// Subpath, when non-empty, names a directory within the checkout that
	// should be used as the build context root. Empty means the repo root.
	Subpath string
}

// Validate enforces the minimum fields required to feed any builder.
func (s SourceRef) Validate() error {
	if strings.TrimSpace(s.Repo) == "" {
		return errors.New("source ref: repo is required")
	}
	// Local-tarball compatibility sources and durable source artifacts do not
	// require (or use) a git ref.
	_, artifactErr := sourceartifact.ParseURI(s.Repo)
	if !IsLocalSourceURI(s.Repo) && artifactErr != nil && strings.TrimSpace(s.Ref) == "" {
		return errors.New("source ref: ref is required")
	}
	if fetchRef := strings.TrimSpace(s.FetchRef); fetchRef != "" {
		if !strings.HasPrefix(fetchRef, "refs/") || strings.ContainsAny(fetchRef, " ~^:?*[\\\t\r\n") ||
			strings.Contains(fetchRef, "..") || strings.Contains(fetchRef, "@{") || strings.HasSuffix(fetchRef, "/") {
			return errors.New("source ref: fetch ref is invalid")
		}
	}
	if strings.HasPrefix(s.Subpath, "/") || strings.HasPrefix(s.Subpath, "..") {
		return errors.New("source ref: subpath must be a relative path inside the checkout")
	}
	if strings.Contains(s.Subpath, "..") {
		return errors.New("source ref: subpath must not escape the checkout")
	}
	return nil
}

// SourceCheckout is a materialized SourceRef on disk. WorkDir is the build
// context root (already adjusted for Subpath). RepoDir is the bare clone root
// so the worker can probe ancillary files (e.g. midnight.toml) at the repo
// root.
type SourceCheckout struct {
	Ref      SourceRef
	RepoDir  string
	WorkDir  string
	CommitID string
	release  func() error
}

// BuildTarget names the image reference + registry the builder should push
// the produced layers to.
type BuildTarget struct {
	// Image is the human-readable image reference (repository[:tag]) the
	// builder should tag the resulting manifest with before pushing.
	Image string
	// Registry is the destination registry endpoint (e.g.
	// `registry.midnight.local:5000`). Empty means the builder MUST not push;
	// callers that only want a local image rely on Image alone.
	Registry string
	// Insecure permits pushing to a non-TLS registry. The embedded
	// `registry.midnight.local:5000` ships without TLS, so the build job sets
	// this true for that endpoint.
	Insecure bool
}

// Validate rejects targets that are missing required fields.
func (t BuildTarget) Validate() error {
	if strings.TrimSpace(t.Image) == "" {
		return errors.New("build target: image is required")
	}
	return nil
}

// PlanKind names the build strategy a planner picked for a given checkout.
type PlanKind string

const (
	// PlanKindDockerfile means the checkout has a Dockerfile and BuildKit will
	// build it directly.
	PlanKindDockerfile PlanKind = "dockerfile"
	// PlanKindRailpack means Railpack will prepare a JSON plan and BuildKit
	// will execute it through the pinned gateway.v0 frontend.
	PlanKindRailpack PlanKind = "railpack"
)

// BuilderPlan summarises a builder's decision for a given checkout. The
// concrete builder fills in additional plan-kind specific fields under Meta.
type BuilderPlan struct {
	Kind           PlanKind
	DockerfilePath string
	ContextDir     string
	Meta           map[string]string
}

// ImageRef is the digest-pinned identity of a successfully built image.
type ImageRef struct {
	// Image is the registry-qualified human-readable reference, e.g.
	// `registry.midnight.local:5000/midnight/web:rev_abc`.
	Image string
	// Digest is the immutable `sha256:<hex>` digest. Callers stitch this with
	// Image to form the `<image>@sha256:<digest>` reference that the deploy
	// path requires.
	Digest string
	// PlanKind records which builder produced the image.
	PlanKind PlanKind
	// StartedAt / FinishedAt time the build for observability.
	StartedAt  time.Time
	FinishedAt time.Time
}

// DigestRef returns the canonical `image@sha256:digest` form. Empty if the
// digest is missing.
func (r ImageRef) DigestRef() string {
	if strings.TrimSpace(r.Digest) == "" {
		return ""
	}
	if strings.Contains(r.Image, "@") {
		return r.Image
	}
	if !strings.HasPrefix(r.Digest, "sha256:") {
		return r.Image + "@sha256:" + r.Digest
	}
	return r.Image + "@" + r.Digest
}

// Builder is the contract every build strategy implements. Plan inspects the
// checkout and returns the decision. Build performs the actual image build
// and registry push.
type Builder interface {
	// Plan inspects checkout.WorkDir and returns the plan that Build would
	// execute. A planner may persist redaction-safe analysis artifacts (for
	// example Railpack's plan/info files), but must not execute the image build.
	Plan(ctx context.Context, checkout SourceCheckout) (BuilderPlan, error)
	// Build performs the image build described by Plan and pushes to target.
	// Returns the digest-pinned ImageRef on success.
	Build(ctx context.Context, checkout SourceCheckout, target BuildTarget) (ImageRef, error)
}

// DockerfilePresent reports whether the checkout's WorkDir contains a
// Dockerfile (the default builder-selection signal).
func DockerfilePresent(checkout SourceCheckout) bool {
	if strings.TrimSpace(checkout.WorkDir) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(checkout.WorkDir, "Dockerfile"))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// SelectBuilder picks the appropriate Builder for the given checkout. A
// Dockerfile in the checkout's WorkDir wins; otherwise the configured Railpack
// builder takes over. If railpack is nil the function returns the
// dockerfileBuilder regardless, which fails fast in Build with a useful error.
func SelectBuilder(checkout SourceCheckout, dockerfileBuilder, railpackBuilder Builder) Builder {
	if DockerfilePresent(checkout) && dockerfileBuilder != nil {
		return dockerfileBuilder
	}
	if railpackBuilder != nil {
		return railpackBuilder
	}
	return dockerfileBuilder
}
