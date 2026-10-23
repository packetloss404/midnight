// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package build

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceRefValidate(t *testing.T) {
	cases := []struct {
		name    string
		ref     SourceRef
		wantErr bool
	}{
		{"ok", SourceRef{Repo: "https://example.com/r.git", Ref: "main"}, false},
		{"ok fetch ref", SourceRef{Repo: "https://example.com/r.git", Ref: strings.Repeat("a", 40), FetchRef: "refs/pull/7/head"}, false},
		{"ok subpath", SourceRef{Repo: "r", Ref: "main", Subpath: "apps/web"}, false},
		{"missing repo", SourceRef{Ref: "main"}, true},
		{"missing ref", SourceRef{Repo: "r"}, true},
		{"absolute subpath", SourceRef{Repo: "r", Ref: "main", Subpath: "/abs"}, true},
		{"escaping subpath", SourceRef{Repo: "r", Ref: "main", Subpath: "../escape"}, true},
		{"hidden escape", SourceRef{Repo: "r", Ref: "main", Subpath: "ok/../escape"}, true},
		{"invalid fetch ref", SourceRef{Repo: "r", Ref: "main", FetchRef: "refs/pull/7/head:evil"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ref.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestImageRefDigestRef(t *testing.T) {
	ref := ImageRef{Image: "registry.midnight.local:5000/midnight/web:rev_abc", Digest: "sha256:" + strings.Repeat("a", 64)}
	got := ref.DigestRef()
	want := "registry.midnight.local:5000/midnight/web:rev_abc@sha256:" + strings.Repeat("a", 64)
	if got != want {
		t.Fatalf("DigestRef = %q, want %q", got, want)
	}
}

func TestDockerfilePresentAndSelectBuilder(t *testing.T) {
	dir := t.TempDir()
	// No Dockerfile: should pick railpack.
	checkout := SourceCheckout{WorkDir: dir}
	if DockerfilePresent(checkout) {
		t.Fatal("DockerfilePresent should be false when no Dockerfile exists")
	}
	railpack := stubBuilder{kind: PlanKindRailpack}
	dockerfile := stubBuilder{kind: PlanKindDockerfile}
	got := SelectBuilder(checkout, dockerfile, railpack)
	if got != Builder(railpack) {
		t.Fatalf("SelectBuilder picked %v, want railpack", got)
	}
	// Add a Dockerfile.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DockerfilePresent(checkout) {
		t.Fatal("DockerfilePresent should be true after writing Dockerfile")
	}
	got = SelectBuilder(checkout, dockerfile, railpack)
	if got != Builder(dockerfile) {
		t.Fatalf("SelectBuilder picked %v, want dockerfile", got)
	}
}

func TestDockerfileBuilderPlanRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	b := DockerfileBuilder{ServiceID: "svc"}
	_, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir})
	if err == nil {
		t.Fatal("Plan should fail when Dockerfile is missing")
	}
}

func TestDockerfileBuilderPlanReportsDockerfilePath(t *testing.T) {
	dir := t.TempDir()
	dockerfilePath := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfilePath, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := DockerfileBuilder{ServiceID: "svc"}
	plan, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != PlanKindDockerfile || plan.DockerfilePath != dockerfilePath || plan.ContextDir != dir {
		t.Fatalf("plan = %#v, want dockerfile plan for %s", plan, dockerfilePath)
	}
}

func TestRailpackBuilderPlanParsesJSON(t *testing.T) {
	dir := t.TempDir()
	workRoot := t.TempDir()
	runner := &railpackPrepareRunner{
		info: `{"railpackVersion":"0.23.0","resolvedPackages":{"node":{"name":"node","resolvedVersion":"20.20.2"}},"detectedProviders":["node"],"success":true}`,
	}
	b := RailpackBuilder{ServiceID: "svc", WorkRoot: workRoot, Runner: runner}
	plan, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Kind != PlanKindRailpack {
		t.Fatalf("plan.Kind = %s, want railpack", plan.Kind)
	}
	if plan.Meta["providers"] != "node" || plan.Meta["railpack_version"] != "0.23.0" {
		t.Fatalf("plan.Meta = %#v, want Railpack node metadata", plan.Meta)
	}
	if plan.Meta["package:node"] != "20.20.2" {
		t.Fatalf("plan.Meta packages = %#v, want node 20.20.2", plan.Meta)
	}
	wantArgs := []string{
		"prepare", dir,
		"--plan-out", filepath.Join(workRoot, "svc", "railpack-plan.json"),
		"--info-out", filepath.Join(workRoot, "svc", "railpack-info.json"),
	}
	if strings.Join(runner.args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("railpack args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestRailpackBuilderPlanCachesPreparedArtifacts(t *testing.T) {
	dir := t.TempDir()
	runner := &railpackPrepareRunner{info: `{"railpackVersion":"0.23.0","success":true}`}
	b := RailpackBuilder{ServiceID: "svc", WorkRoot: t.TempDir(), Runner: runner}
	first, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	second, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir})
	if err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("prepare calls = %d, want 1", runner.calls)
	}
	if first.Meta["plan_path"] != second.Meta["plan_path"] {
		t.Fatalf("cached plan paths differ: %#v / %#v", first.Meta, second.Meta)
	}
}

func TestRailpackBuilderForwardsCustomBuildCommandAsOneArgument(t *testing.T) {
	dir := t.TempDir()
	runner := &railpackPrepareRunner{info: `{"railpackVersion":"0.23.0","success":true}`}
	b := RailpackBuilder{
		ServiceID:    "svc",
		WorkRoot:     t.TempDir(),
		Runner:       runner,
		BuildCommand: `npm run build && printf '%s' "$HOME"`,
	}
	if _, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	var got string
	for index, arg := range runner.args {
		if arg == "--build-cmd" && index+1 < len(runner.args) {
			got = runner.args[index+1]
		}
	}
	if got != b.BuildCommand {
		t.Fatalf("build command = %q, want exact single argv value %q; args=%#v", got, b.BuildCommand, runner.args)
	}
}

func TestRailpackBuilderPrepareReceivesOnlySortedSecretIDs(t *testing.T) {
	dir := t.TempDir()
	const rawSecret = "must-not-reach-railpack"
	runner := &railpackPrepareRunner{info: `{"railpackVersion":"0.23.0","success":true}`}
	b := RailpackBuilder{
		ServiceID: "svc",
		WorkRoot:  t.TempDir(),
		Runner:    runner,
		SecretIDs: []string{"z_token", "a_token"},
	}
	if _, err := b.Plan(context.Background(), SourceCheckout{WorkDir: dir}); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	joined := strings.Join(runner.args, "\n")
	if strings.Contains(joined, rawSecret) || strings.Contains(strings.Join(b.Env, "\n"), rawSecret) {
		t.Fatalf("railpack prepare received plaintext: args=%#v env=%#v", runner.args, b.Env)
	}
	wantSuffix := "--env\na_token=" + railpackSecretPlaceholder + "\n--env\nz_token=" + railpackSecretPlaceholder
	if !strings.HasSuffix(joined, wantSuffix) {
		t.Fatalf("railpack args = %#v, want sorted name-only secret declarations", runner.args)
	}
}

func TestRailpackBuilderBuildUsesPreparedPlanWithGateway(t *testing.T) {
	checkoutDir := t.TempDir()
	outputDir := t.TempDir()
	prepareRunner := &railpackPrepareRunner{info: `{"railpackVersion":"0.23.0","detectedProviders":["node"],"success":true}`}
	digest := "sha256:" + strings.Repeat("d", 64)
	buildctlRunner := &fakeCommandRunner{solveOutput: "pushed digest " + digest + "\n"}
	b := RailpackBuilder{
		ServiceID: "svc_api",
		WorkRoot:  filepath.Join(outputDir, "railpack"),
		Runner:    prepareRunner,
		Driver: BuildKitDriver{
			Runner:    BuildctlRunner{Runner: buildctlRunner},
			OutputDir: outputDir,
			BuildArgs: map[string]string{"cache-key": "svc_api"},
		},
	}
	checkout := SourceCheckout{WorkDir: checkoutDir}
	if _, err := b.Plan(context.Background(), checkout); err != nil {
		t.Fatalf("Plan: %v", err)
	}
	image, err := b.Build(context.Background(), checkout, BuildTarget{Image: "app:latest"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if image.Digest != digest || image.PlanKind != PlanKindRailpack {
		t.Fatalf("image = %#v", image)
	}
	if prepareRunner.calls != 1 {
		t.Fatalf("prepare calls = %d, want 1", prepareRunner.calls)
	}
	if len(buildctlRunner.captured) != 1 {
		t.Fatalf("buildctl calls = %#v, want one solve", buildctlRunner.captured)
	}
	joined := strings.Join(buildctlRunner.captured[0].args, "\n")
	if !strings.Contains(joined, "--frontend\ngateway.v0") ||
		!strings.Contains(joined, "--opt\nsource="+RequiredRailpackFrontend) {
		t.Fatalf("buildctl gateway contract missing:\n%s", joined)
	}
	for _, artifact := range []string{"railpack-plan.json", "railpack-info.json", "railpack-prepare.log"} {
		if _, err := os.Stat(filepath.Join(outputDir, "railpack", "svc_api", artifact)); err != nil {
			t.Fatalf("%s not persisted: %v", artifact, err)
		}
	}
}

type stubBuilder struct{ kind PlanKind }

func (s stubBuilder) Plan(_ context.Context, _ SourceCheckout) (BuilderPlan, error) {
	return BuilderPlan{Kind: s.kind}, nil
}

func (s stubBuilder) Build(_ context.Context, _ SourceCheckout, _ BuildTarget) (ImageRef, error) {
	return ImageRef{PlanKind: s.kind}, nil
}

type railpackPrepareRunner struct {
	info  string
	args  []string
	calls int
}

func (r *railpackPrepareRunner) Run(_ context.Context, _ string, args []string, stdout, _ io.Writer) error {
	r.calls++
	r.args = append([]string(nil), args...)
	var planPath, infoPath string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--plan-out":
			planPath = args[i+1]
		case "--info-out":
			infoPath = args[i+1]
		}
	}
	if planPath == "" || infoPath == "" {
		return errors.New("prepare output paths missing")
	}
	if err := os.WriteFile(planPath, []byte(`{"steps":{}}`), 0o640); err != nil {
		return err
	}
	if err := os.WriteFile(infoPath, []byte(r.info), 0o640); err != nil {
		return err
	}
	_, _ = io.WriteString(stdout, "Railpack 0.23.0\nDetected Node\n")
	return nil
}
