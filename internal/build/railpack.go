// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const railpackSecretPlaceholder = "__MIDNIGHT_EPHEMERAL_BUILD_SECRET__" // not-a-failure-code

// RailpackBuilder shells out to the upstream Railpack CLI to prepare a build
// plan for a checkout that does not bring its own Dockerfile. BuildKit then
// consumes that plan through Railpack's gateway.v0 frontend.
//
// Two phases:
//
//   - `railpack prepare DIRECTORY --plan-out ... --info-out ...` persists the
//     build plan, detected-provider info, and pretty operator output.
//   - `buildctl build --frontend gateway.v0 --opt source=<pinned frontend>`
//     executes the saved plan without asking the CLI to build or emit a
//     Dockerfile.
//
// We deliberately do NOT reimplement any Railpack logic in Go. The whole
// point of integrating Railway's tool is leveraging upstream changes.
type RailpackBuilder struct {
	// ServiceID is threaded into the BuildKit cache + provenance metadata.
	ServiceID string
	// Binary is the path to the pinned `railpack` binary. Empty means
	// `railpack` on PATH.
	Binary string
	// Driver is the BuildKit driver doing the underlying solve.
	Driver BuildKitDriver
	// FrontendSource is the digest-pinned Railpack BuildKit frontend. Empty
	// uses RequiredRailpackFrontend.
	FrontendSource string
	// WorkRoot is where Railpack plan/info/pretty-output artifacts are
	// persisted. Empty defaults to <Driver.OutputDir>/railpack.
	WorkRoot string
	// Runner is the seam tests use to substitute a fake railpack CLI. nil
	// uses os/exec.
	Runner CommandRunner
	// Env is extra env passed through to the railpack invocations
	// (e.g. RAILPACK_LOG_LEVEL). Items are KEY=VALUE.
	Env []string
	// SecretIDs are the names Railpack records in its plan. Values are
	// intentionally unavailable to the prepare subprocess; BuildKit receives
	// them later through session secret mounts.
	SecretIDs []string
	// BuildCommand overrides Railpack's detected build command through the
	// upstream `prepare --build-cmd` contract. It is never interpreted by the
	// Midnight worker or passed through a host shell.
	BuildCommand string
	// prepared caches one job's prepare output so a caller may preview Plan
	// and then Build without running source analysis twice.
	prepared *railpackPreparation
}

// RailpackInfo is the stable platform-facing output produced by prepare.
// Unknown upstream fields are intentionally ignored and remain available in
// the persisted railpack-info.json artifact.
type RailpackInfo struct {
	RailpackVersion   string                     `json:"railpackVersion,omitempty"`
	ResolvedPackages  map[string]RailpackPackage `json:"resolvedPackages,omitempty"`
	Metadata          map[string]string          `json:"metadata,omitempty"`
	DetectedProviders []string                   `json:"detectedProviders,omitempty"`
	Success           bool                       `json:"success"`
}

type RailpackPackage struct {
	Name             string `json:"name,omitempty"`
	RequestedVersion string `json:"requestedVersion,omitempty"`
	ResolvedVersion  string `json:"resolvedVersion,omitempty"`
	Source           string `json:"source,omitempty"`
}

type railpackPreparation struct {
	CheckoutDir string
	PlanPath    string
	InfoPath    string
	LogPath     string
	Info        RailpackInfo
}

// Plan invokes Railpack's production prepare command and returns redaction-safe
// metadata plus paths to the persisted artifacts.
func (b *RailpackBuilder) Plan(ctx context.Context, checkout SourceCheckout) (BuilderPlan, error) {
	prepared, err := b.prepare(ctx, checkout)
	if err != nil {
		return BuilderPlan{}, err
	}
	meta := map[string]string{
		"railpack_version": prepared.Info.RailpackVersion,
		"providers":        strings.Join(prepared.Info.DetectedProviders, ","),
		"plan_path":        prepared.PlanPath,
		"info_path":        prepared.InfoPath,
		"prepare_log_path": prepared.LogPath,
	}
	for name, pkg := range prepared.Info.ResolvedPackages {
		meta["package:"+name] = pkg.ResolvedVersion
	}
	return BuilderPlan{
		Kind:       PlanKindRailpack,
		ContextDir: checkout.WorkDir,
		Meta:       prunedStringMap(meta),
	}, nil
}

func (b *RailpackBuilder) prepare(ctx context.Context, checkout SourceCheckout) (*railpackPreparation, error) {
	if strings.TrimSpace(checkout.WorkDir) == "" {
		return nil, errors.New("railpack builder: checkout work dir is required")
	}
	if b.prepared != nil && b.prepared.CheckoutDir == checkout.WorkDir {
		return b.prepared, nil
	}
	workRoot := strings.TrimSpace(b.WorkRoot)
	if workRoot == "" {
		if strings.TrimSpace(b.Driver.OutputDir) == "" {
			return nil, errors.New("railpack builder: work root or driver output dir is required")
		}
		workRoot = filepath.Join(b.Driver.OutputDir, "railpack")
	}
	dir := filepath.Join(workRoot, sanitizeServiceID(b.ServiceID))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("railpack builder: create artifact dir: %w", err)
	}
	planPath := filepath.Join(dir, "railpack-plan.json")
	infoPath := filepath.Join(dir, "railpack-info.json")
	logPath := filepath.Join(dir, "railpack-prepare.log")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	args := []string{
		"prepare", checkout.WorkDir,
		"--plan-out", planPath,
		"--info-out", infoPath,
	}
	if command := strings.TrimSpace(b.BuildCommand); command != "" {
		args = append(args, "--build-cmd", command)
	}
	if err := ValidateBuildSecretIDs(b.SecretIDs); err != nil {
		return nil, fmt.Errorf("railpack builder: %w", err)
	}
	secretIDs := append([]string(nil), b.SecretIDs...)
	sort.Strings(secretIDs)
	for _, id := range secretIDs {
		// Railpack v0.23 requires KEY=VALUE to retain the secret ID in the
		// generated plan. Give it a constant non-secret placeholder; the real
		// value remains unavailable to prepare and arrives only as a BuildKit
		// session secret during the later solve.
		args = append(args, "--env", id+"="+railpackSecretPlaceholder)
	}
	if err := b.run(ctx, args, stdout, stderr); err != nil {
		return nil, fmt.Errorf("railpack prepare failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	if err := requireRegularArtifact(planPath, "build plan"); err != nil {
		return nil, err
	}
	if err := requireRegularArtifact(infoPath, "build info"); err != nil {
		return nil, err
	}
	infoBytes, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("railpack builder: read build info: %w", err)
	}
	var info RailpackInfo
	if err := json.Unmarshal(infoBytes, &info); err != nil {
		return nil, fmt.Errorf("railpack builder: parse build info: %w", err)
	}
	if !info.Success {
		return nil, errors.New("railpack builder: prepare reported an unsuccessful build plan")
	}
	if err := writeFileAtomic(logPath, stdout.Bytes(), 0o640); err != nil {
		return nil, fmt.Errorf("railpack builder: persist prepare output: %w", err)
	}
	b.prepared = &railpackPreparation{
		CheckoutDir: checkout.WorkDir,
		PlanPath:    planPath,
		InfoPath:    infoPath,
		LogPath:     logPath,
		Info:        info,
	}
	return b.prepared, nil
}

// Build prepares once, then forwards the plan to BuildKit's gateway frontend.
func (b *RailpackBuilder) Build(ctx context.Context, checkout SourceCheckout, target BuildTarget) (ImageRef, error) {
	prepared, err := b.prepare(ctx, checkout)
	if err != nil {
		return ImageRef{}, err
	}
	frontendSource := strings.TrimSpace(b.FrontendSource)
	if frontendSource == "" {
		frontendSource = RequiredRailpackFrontend
	}
	driver := b.Driver
	driver.BuildArgs = copyStringMap(driver.BuildArgs)
	if driver.BuildArgs == nil {
		driver.BuildArgs = make(map[string]string)
	}
	if _, exists := driver.BuildArgs["cache-key"]; !exists {
		driver.BuildArgs["cache-key"] = sanitizeServiceID(b.ServiceID)
	}
	res, err := driver.SolveGateway(ctx, b.ServiceID, checkout.WorkDir, prepared.PlanPath, frontendSource, target)
	if err != nil {
		return ImageRef{}, err
	}
	return ImageRef{
		Image:      res.Image,
		Digest:     res.Digest,
		PlanKind:   PlanKindRailpack,
		StartedAt:  res.StartedAt,
		FinishedAt: res.FinishedAt,
	}, nil
}

func (b *RailpackBuilder) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	binary := b.Binary
	if strings.TrimSpace(binary) == "" {
		binary = "railpack"
	}
	if b.Runner != nil {
		return b.Runner.Run(ctx, binary, args, stdout, stderr)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	if len(b.Env) > 0 {
		cmd.Env = append(os.Environ(), b.Env...)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func requireRegularArtifact(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("railpack builder: prepare did not emit %s at %s: %w", label, path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("railpack builder: prepare emitted invalid %s at %s", label, path)
	}
	return nil
}

// copyFile is shared by source checkout staging paths.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func prunedStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(v) == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
