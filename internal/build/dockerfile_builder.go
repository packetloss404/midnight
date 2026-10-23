// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DockerfileBuilder hands a Dockerfile directly to BuildKit via the embedded
// BuildKitDriver. It is the simplest Builder strategy: it does nothing more
// than locate the Dockerfile in the checkout and forward to the driver.
type DockerfileBuilder struct {
	// ServiceID names the service the build belongs to; threaded into the
	// BuildKit cache namespace + provenance metadata.
	ServiceID string
	// Driver is the BuildKit driver doing the actual solve.
	Driver BuildKitDriver
	// DockerfileName overrides the default `Dockerfile` filename. Empty
	// means `Dockerfile`.
	DockerfileName string
}

// Plan inspects checkout.WorkDir for a Dockerfile and reports the resulting
// plan. It does not invoke BuildKit.
func (b DockerfileBuilder) Plan(_ context.Context, checkout SourceCheckout) (BuilderPlan, error) {
	if strings.TrimSpace(checkout.WorkDir) == "" {
		return BuilderPlan{}, errors.New("dockerfile builder: checkout work dir is required")
	}
	dockerfileName := b.DockerfileName
	if dockerfileName == "" {
		dockerfileName = "Dockerfile"
	}
	dockerfilePath := filepath.Join(checkout.WorkDir, dockerfileName)
	info, err := os.Stat(dockerfilePath)
	if err != nil {
		return BuilderPlan{}, fmt.Errorf("dockerfile builder: stat %s: %w", dockerfilePath, err)
	}
	if info.IsDir() {
		return BuilderPlan{}, fmt.Errorf("dockerfile builder: %s is a directory, expected a file", dockerfilePath)
	}
	return BuilderPlan{
		Kind:           PlanKindDockerfile,
		DockerfilePath: dockerfilePath,
		ContextDir:     checkout.WorkDir,
		Meta: map[string]string{
			"dockerfile": dockerfileName,
		},
	}, nil
}

// Build resolves the Dockerfile inside the checkout and hands it to the
// BuildKit driver. The returned ImageRef carries the digest + plan kind so the
// caller can stitch the `@sha256:` reference the deploy path expects.
func (b DockerfileBuilder) Build(ctx context.Context, checkout SourceCheckout, target BuildTarget) (ImageRef, error) {
	plan, err := b.Plan(ctx, checkout)
	if err != nil {
		return ImageRef{}, err
	}
	res, err := b.Driver.Solve(ctx, b.ServiceID, plan.ContextDir, plan.DockerfilePath, target)
	if err != nil {
		return ImageRef{}, err
	}
	return ImageRef{
		Image:      res.Image,
		Digest:     res.Digest,
		PlanKind:   PlanKindDockerfile,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
	}, nil
}
