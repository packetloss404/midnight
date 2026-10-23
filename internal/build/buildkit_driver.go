// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BuildKitDriver wraps a BuildctlRunner so any Builder implementation can
// trigger a Dockerfile solve + push without re-deriving the args. It is the
// integration seam between the Builder interface and the existing pinned
// buildctl runner.
//
// Progress streaming: BuildKitDriver tails the buildctl stdout log while the
// solve runs and forwards every line to ProgressSink (best-effort). This keeps
// observability close to BuildKit's native output without depending on the
// Go client SDK.
type BuildKitDriver struct {
	// Runner is the underlying buildctl runner. Zero value uses the default
	// ExecCommandRunner + "buildctl" on PATH.
	Runner BuildctlRunner
	// CacheRoot, when set, is passed to the underlying BuildctlInvocation so
	// builds for the same service reuse local cache.
	CacheRoot string
	// OutputDir is where per-build artifacts (logs, sbom, provenance, digest)
	// land. Required.
	OutputDir string
	// Platforms is the optional explicit list of platforms to build for.
	Platforms []string
	// BuildArgs is forwarded to the underlying solve. Build callers may pass
	// resolved values (e.g. railpack env hooks) through here.
	BuildArgs map[string]string
	// Secrets are forwarded as BuildKit session secret mounts. Values travel
	// only in the buildctl child environment and are redacted before solve
	// output reaches the durable artifact log.
	Secrets []BuildSecret
	// ProgressSink, when non-nil, receives one call per stdout/stderr line
	// emitted by the buildctl subprocess. It is intentionally line-oriented
	// rather than a structured progress event because buildctl's solve writes
	// human-readable progress to stdout when no `--progress=auto` is given.
	ProgressSink func(line string)
}

// Solve performs a Dockerfile build with the embedded BuildctlRunner. It
// returns the BuildctlResult so callers can derive the digest and SBOM /
// provenance file paths.
func (d BuildKitDriver) Solve(ctx context.Context, serviceID, contextDir, dockerfilePath string, target BuildTarget) (BuildctlResult, error) {
	return d.solve(ctx, serviceID, contextDir, dockerfilePath, "", "", "dockerfile.v0", target)
}

// SolveGateway performs a gateway.v0 build using an immutable custom
// frontend and its plan file. Railpack plans use this path.
func (d BuildKitDriver) SolveGateway(ctx context.Context, serviceID, contextDir, planPath, frontendSource string, target BuildTarget) (BuildctlResult, error) {
	return d.solve(ctx, serviceID, contextDir, "", planPath, frontendSource, "gateway.v0", target)
}

func (d BuildKitDriver) solve(ctx context.Context, serviceID, contextDir, dockerfilePath, planPath, frontendSource, frontend string, target BuildTarget) (BuildctlResult, error) {
	if strings.TrimSpace(serviceID) == "" {
		return BuildctlResult{}, errors.New("buildkit driver: service id is required")
	}
	if strings.TrimSpace(d.OutputDir) == "" {
		return BuildctlResult{}, errors.New("buildkit driver: output dir is required")
	}
	if err := target.Validate(); err != nil {
		return BuildctlResult{}, err
	}
	contextAbs, err := filepath.Abs(contextDir)
	if err != nil {
		return BuildctlResult{}, fmt.Errorf("buildkit driver: resolve context dir: %w", err)
	}
	dockerAbs := ""
	planAbs := ""
	if frontend == "gateway.v0" {
		planAbs, err = filepath.Abs(planPath)
		if err != nil {
			return BuildctlResult{}, fmt.Errorf("buildkit driver: resolve gateway plan: %w", err)
		}
	} else {
		dockerAbs, err = filepath.Abs(dockerfilePath)
		if err != nil {
			return BuildctlResult{}, fmt.Errorf("buildkit driver: resolve dockerfile: %w", err)
		}
	}
	outAbs, err := filepath.Abs(d.OutputDir)
	if err != nil {
		return BuildctlResult{}, fmt.Errorf("buildkit driver: resolve output dir: %w", err)
	}
	if err := os.MkdirAll(outAbs, 0o750); err != nil {
		return BuildctlResult{}, fmt.Errorf("buildkit driver: create output dir: %w", err)
	}

	inv := BuildctlInvocation{
		ServiceID:      serviceID,
		ContextDir:     contextAbs,
		DockerfilePath: dockerAbs,
		Frontend:       frontend,
		FrontendSource: frontendSource,
		PlanPath:       planAbs,
		Image:          target.Image,
		Registry:       target.Registry,
		CacheRoot:      d.CacheRoot,
		OutputDir:      outAbs,
		Insecure:       target.Insecure,
		Platforms:      append([]string(nil), d.Platforms...),
		BuildArgs:      copyStringMap(d.BuildArgs),
		Secrets:        CopyBuildSecrets(d.Secrets),
	}
	defer ZeroBuildSecrets(inv.Secrets)

	// Kick off a background tail of the stdout log so callers see progress
	// while the solve runs. The log file is created lazily by Build; we wait
	// for it to appear before streaming, then stop once Build returns.
	stop := d.streamProgress(ctx, outAbs)
	defer stop()

	res, err := d.Runner.Build(ctx, inv)
	if err != nil {
		return BuildctlResult{}, err
	}
	return res, nil
}

func (d BuildKitDriver) streamProgress(ctx context.Context, outDir string) func() {
	if d.ProgressSink == nil {
		return func() {}
	}
	doneCh := make(chan struct{})
	stopCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		path := filepath.Join(outDir, "buildctl.stdout.log")
		// Wait until the solve creates the log; bail out if context cancels.
		var file *os.File
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			default:
			}
			f, err := os.Open(path)
			if err == nil {
				file = f
				break
			}
			if !os.IsNotExist(err) {
				return
			}
			// Brief wait by yielding through the scheduler; avoid time.Sleep
			// loops so tests run deterministically.
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				// Solve may have written and closed without us catching up;
				// fall through and try a final open below.
				if f, err := os.Open(path); err == nil {
					file = f
				}
				if file == nil {
					return
				}
			}
			if file != nil {
				break
			}
		}
		defer func() { _ = file.Close() }()
		reader := bufio.NewReader(file)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			line, err := reader.ReadString('\n')
			if line != "" {
				d.ProgressSink(strings.TrimRight(line, "\r\n"))
			}
			if err == io.EOF {
				select {
				case <-stopCh:
					// Drain remaining bytes once the solve finished.
					rest, _ := io.ReadAll(reader)
					for _, l := range strings.Split(string(rest), "\n") {
						if l != "" {
							d.ProgressSink(strings.TrimRight(l, "\r"))
						}
					}
					return
				case <-ctx.Done():
					return
				default:
					// No new line yet — let the scheduler advance and retry.
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return func() {
		close(stopCh)
		<-doneCh
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
