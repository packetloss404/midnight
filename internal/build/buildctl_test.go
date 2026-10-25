package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	versionOutput string
	versionErr    error
	solveOutput   string
	solveErr      error
	captured      []capturedExec
}

func TestBuildctlPushTargetHonorsRegistryAndNamespaces(t *testing.T) {
	for _, test := range []struct{ image, registry, want string }{
		{"web:latest", "registry.test:5000", "registry.test:5000/web:latest"},
		{"midnight/web:latest", "registry.test:5000", "registry.test:5000/midnight/web:latest"},
		{"other.test/team/web:latest", "registry.test:5000", "other.test/team/web:latest"},
		{"localhost/team/web:latest", "registry.test:5000", "localhost/team/web:latest"},
		{"registry/team/web:latest", "registry", "registry/team/web:latest"},
		{"midnight/web:latest", "", "midnight/web:latest"},
	} {
		t.Run(test.image+"_via_"+test.registry, func(t *testing.T) {
			invocation := BuildctlInvocation{Image: test.image, Registry: test.registry}
			if got := buildctlImageRef(invocation); got != test.want {
				t.Fatalf("push target = %q, want %q", got, test.want)
			}
		})
	}
}

type capturedExec struct {
	name string
	args []string
}

type secretCaptureRunner struct {
	args   []string
	env    []string
	stdout string
	stderr string
	err    error
}

func (r *secretCaptureRunner) Run(context.Context, string, []string, io.Writer, io.Writer) error {
	return errors.New("secret solve unexpectedly used environment-blind runner")
}

func (r *secretCaptureRunner) RunEnv(_ context.Context, _ string, args, env []string, stdout, stderr io.Writer) error {
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), env...)
	_, _ = io.WriteString(stdout, r.stdout)
	_, _ = io.WriteString(stderr, r.stderr)
	return r.err
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args []string, stdout, _ io.Writer) error {
	f.captured = append(f.captured, capturedExec{name: name, args: append([]string(nil), args...)})
	if len(args) > 0 && args[0] == "--version" {
		_, _ = io.WriteString(stdout, f.versionOutput)
		return f.versionErr
	}
	_, _ = io.WriteString(stdout, f.solveOutput)
	return f.solveErr
}

func TestVerifyVersionAcceptsPinnedRelease(t *testing.T) {
	runner := BuildctlRunner{Runner: &fakeCommandRunner{
		versionOutput: "buildctl github.com/moby/buildkit " + RequiredBuildKitVersion + " 1234567\n",
	}}
	got, err := runner.VerifyVersion(context.Background())
	if err != nil {
		t.Fatalf("VerifyVersion error = %v", err)
	}
	if got != RequiredBuildKitVersion {
		t.Fatalf("VerifyVersion = %q, want %q", got, RequiredBuildKitVersion)
	}
}

func TestVerifyVersionRejectsMismatch(t *testing.T) {
	runner := BuildctlRunner{Runner: &fakeCommandRunner{
		versionOutput: "buildctl github.com/moby/buildkit v0.99.0 cafe\n",
	}}
	_, err := runner.VerifyVersion(context.Background())
	if err == nil {
		t.Fatal("VerifyVersion accepted mismatched version")
	}
	var mismatch BuildKitVersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected BuildKitVersionMismatchError, got %T: %v", err, err)
	}
}

func TestBuildEmitsProvenanceSBOMAndDigestDeterministically(t *testing.T) {
	tmp := t.TempDir()
	contextDir := filepath.Join(tmp, "ctx")
	if err := os.MkdirAll(contextDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(contextDir, "Dockerfile")
	dockerContent := "" +
		"FROM ghcr.io/example/base:1.0@sha256:" + strings.Repeat("a", 64) + " AS base\n" +
		"COPY . /app\n"
	if err := os.WriteFile(dockerfile, []byte(dockerContent), 0o640); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(tmp, "out")
	digest := "sha256:" + strings.Repeat("b", 64)
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	runner := BuildctlRunner{
		Runner: &fakeCommandRunner{
			solveOutput: "exporting to image\nexporting layers done\npushed digest " + digest + "\n",
		},
		Now: func() time.Time { return now },
	}

	inv := BuildctlInvocation{
		ServiceID:      "demo",
		ContextDir:     contextDir,
		DockerfilePath: dockerfile,
		Image:          "midnight/demo:phase1",
		Registry:       "registry.midnight.local:5000",
		OutputDir:      output,
		Insecure:       true,
		Platforms:      []string{"linux/amd64"},
		BuildArgs:      map[string]string{"GO_VERSION": "1.23"},
	}
	res, err := runner.Build(context.Background(), inv)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if res.Digest != digest {
		t.Fatalf("digest = %q, want %q", res.Digest, digest)
	}
	if res.Image != "registry.midnight.local:5000/midnight/demo:phase1" {
		t.Fatalf("returned image = %q, want the actual registry push target", res.Image)
	}
	if res.BuildKitVer != RequiredBuildKitVersion {
		t.Fatalf("BuildKitVer = %q, want %q", res.BuildKitVer, RequiredBuildKitVersion)
	}

	provenance1, err := os.ReadFile(res.ProvenancePath)
	if err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	stmt, err := UnmarshalProvenanceStatementJSON(provenance1)
	if err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if stmt.Predicate.RunDetails.Builder.ID != "midnight-buildkit/"+RequiredBuildKitVersion {
		t.Fatalf("builder id = %q", stmt.Predicate.RunDetails.Builder.ID)
	}
	if len(stmt.Subject) != 1 || stmt.Subject[0].Name != res.Image {
		t.Fatalf("provenance subject = %#v, want returned push target %q", stmt.Subject, res.Image)
	}
	if len(stmt.Predicate.BuildDefinition.ResolvedDependencies) != 1 {
		t.Fatalf("resolved deps = %d, want 1", len(stmt.Predicate.BuildDefinition.ResolvedDependencies))
	}

	sbom1, err := os.ReadFile(res.SBOMPath)
	if err != nil {
		t.Fatalf("read sbom: %v", err)
	}
	if !strings.Contains(string(sbom1), `"specVersion": "`+CycloneDXSpecVersion+`"`) {
		t.Fatalf("sbom missing pinned spec version: %s", sbom1)
	}

	digestFile, err := os.ReadFile(filepath.Join(output, "digest.txt"))
	if err != nil {
		t.Fatalf("read digest.txt: %v", err)
	}
	if strings.TrimSpace(string(digestFile)) != digest {
		t.Fatalf("digest.txt = %q, want %q", string(digestFile), digest)
	}

	// Reproducibility: second run with identical inputs must produce
	// byte-identical provenance + SBOM (per ADR-008 the only differences
	// allowed across runs are the timestamps, which we hold constant via
	// the injected clock).
	output2 := filepath.Join(tmp, "out2")
	inv2 := inv
	inv2.OutputDir = output2
	res2, err := runner.Build(context.Background(), inv2)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	provenance2, err := os.ReadFile(res2.ProvenancePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(provenance1, provenance2) {
		t.Fatalf("provenance not byte-identical across runs:\n--- run1 ---\n%s\n--- run2 ---\n%s", provenance1, provenance2)
	}
	sbom2, err := os.ReadFile(res2.SBOMPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sbom1, sbom2) {
		t.Fatalf("sbom not byte-identical across runs")
	}
}

func TestBuildRejectsMissingFields(t *testing.T) {
	cases := []BuildctlInvocation{
		{},
		{ServiceID: "x"},
		{ServiceID: "x", ContextDir: "/abs", DockerfilePath: "/elsewhere/Dockerfile", Image: "img", OutputDir: "/abs/out"},
	}
	runner := BuildctlRunner{Runner: &fakeCommandRunner{}}
	for i, inv := range cases {
		if _, err := runner.Build(context.Background(), inv); err == nil {
			t.Fatalf("case %d: expected error for invocation %#v", i, inv)
		}
	}
}

func TestBuildctlSecretsUseEnvironmentNotArgvAndRedactEveryArtifact(t *testing.T) {
	tmp := t.TempDir()
	contextDir := filepath.Join(tmp, "context")
	if err := os.MkdirAll(contextDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(contextDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	const secret = "midnight-build-secret-plaintext"
	digest := "sha256:" + strings.Repeat("e", 64)
	command := &secretCaptureRunner{
		stdout: "prefix " + secret[:13] + secret[13:] + "\npushed digest " + digest + "\n",
		stderr: "adversarial stderr=" + secret,
	}
	outputDir := filepath.Join(tmp, "output")
	inv := BuildctlInvocation{
		ServiceID:      "svc",
		ContextDir:     contextDir,
		DockerfilePath: dockerfile,
		Image:          "app:latest",
		OutputDir:      outputDir,
		BuildArgs: map[string]string{
			RailpackSecretsHashBuildArg: BuildSecretsHash([]BuildSecret{{ID: "API_TOKEN", Value: []byte(secret)}}),
		},
		Secrets: []BuildSecret{{ID: "API_TOKEN", Value: []byte(secret)}},
	}
	if _, err := (BuildctlRunner{Runner: command}).Build(context.Background(), inv); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if strings.Contains(strings.Join(command.args, "\n"), secret) {
		t.Fatalf("buildctl argv leaked secret: %#v", command.args)
	}
	if !strings.Contains(strings.Join(command.args, "\n"), "--secret\nid=API_TOKEN,env="+buildSecretEnvPrefix+"0") {
		t.Fatalf("buildctl argv missing env-backed secret mount: %#v", command.args)
	}
	if !strings.Contains(strings.Join(command.env, "\n"), buildSecretEnvPrefix+"0="+secret) {
		t.Fatalf("buildctl child environment missing secret source: %#v", command.env)
	}
	if err := filepath.Walk(outputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(content, []byte(secret)) {
			t.Fatalf("artifact %s leaked build secret", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	provenance, err := os.ReadFile(filepath.Join(outputDir, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(provenance, []byte(`"secrets-hash": "<redacted>"`)) {
		t.Fatalf("provenance did not redact secret hash: %s", provenance)
	}
}

func TestBuildctlSecretFailureRedactsErrorAndLogs(t *testing.T) {
	tmp := t.TempDir()
	contextDir := filepath.Join(tmp, "context")
	if err := os.MkdirAll(contextDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(contextDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	const secret = "failure-path-private-value"
	command := &secretCaptureRunner{
		stdout: "split " + secret,
		stderr: "stderr " + secret,
		err:    errors.New("runner repeated " + secret),
	}
	outputDir := filepath.Join(tmp, "output")
	_, err := (BuildctlRunner{Runner: command}).Build(context.Background(), BuildctlInvocation{
		ServiceID:      "svc",
		ContextDir:     contextDir,
		DockerfilePath: dockerfile,
		Image:          "app:latest",
		OutputDir:      outputDir,
		Secrets:        []BuildSecret{{ID: "API_TOKEN", Value: []byte(secret)}},
	})
	if err == nil {
		t.Fatal("Build() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Build() error leaked secret: %v", err)
	}
	for _, name := range []string{"buildctl.stdout.log", "buildctl.stderr.log"} {
		content, readErr := os.ReadFile(filepath.Join(outputDir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if bytes.Contains(content, []byte(secret)) {
			t.Fatalf("%s leaked secret: %q", name, content)
		}
	}
}

func TestAssembleSolveArgsPropagatesConfiguredBuildKitAddress(t *testing.T) {
	runner := BuildctlRunner{Address: "unix:///run/midnight/buildkit/buildkitd.sock"}
	args := runner.assembleSolveArgs(BuildctlInvocation{
		ContextDir:     "/tmp/context",
		DockerfilePath: "/tmp/context/Dockerfile",
		Image:          "registry.test/app:latest",
		OutputDir:      "/tmp/output",
	})
	if len(args) < 3 || args[0] != "--addr" || args[1] != runner.Address || args[2] != "build" {
		t.Fatalf("solve args prefix = %#v, want --addr %s build", args, runner.Address)
	}
}

func TestAssembleSolveArgsLeavesDaemonDiscoveryToBuildctlWhenAddressEmpty(t *testing.T) {
	args := (BuildctlRunner{}).assembleSolveArgs(BuildctlInvocation{
		ContextDir:     "/tmp/context",
		DockerfilePath: "/tmp/context/Dockerfile",
		Image:          "registry.test/app:latest",
		OutputDir:      "/tmp/output",
	})
	if len(args) == 0 || args[0] != "build" {
		t.Fatalf("solve args prefix = %#v, want build with no global --addr", args)
	}
}

func TestAssembleSolveArgsUsesPinnedGatewayFrontendAndPlanDirectory(t *testing.T) {
	args := (BuildctlRunner{Address: "tcp://buildkit:1234"}).assembleSolveArgs(BuildctlInvocation{
		ServiceID:      "svc_api",
		ContextDir:     "/tmp/context",
		Frontend:       "gateway.v0",
		FrontendSource: RequiredRailpackFrontend,
		PlanPath:       "/tmp/artifacts/railpack-plan.json",
		Image:          "registry.test/app:latest",
		OutputDir:      "/tmp/output",
		BuildArgs:      map[string]string{"cache-key": "svc_api"},
	})
	want := []string{
		"--addr", "tcp://buildkit:1234",
		"build",
		"--frontend", "gateway.v0",
		"--local", "context=/tmp/context",
		"--local", "dockerfile=/tmp/artifacts",
		"--opt", "source=" + RequiredRailpackFrontend,
		"--opt", "build-arg:cache-key=svc_api",
		"--output", "type=image,name=registry.test/app:latest",
		"--metadata-file", "/tmp/output/buildctl.metadata.json",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("gateway args = %#v, want exact command %#v", args, want)
	}
}

func TestBuildctlInvocationRejectsMutableGatewayFrontend(t *testing.T) {
	err := (BuildctlInvocation{
		ServiceID:      "svc",
		ContextDir:     "/tmp/context",
		Frontend:       "gateway.v0",
		FrontendSource: "ghcr.io/railwayapp/railpack-frontend:v0.23.0",
		PlanPath:       "/tmp/artifacts/railpack-plan.json",
		Image:          "app:latest",
		OutputDir:      "/tmp/output",
	}).Validate()
	if err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("Validate() error = %v, want digest-pinned rejection", err)
	}
}

func TestReadDigestFromBuildctlOutput(t *testing.T) {
	stdout := "loading dockerfile\nexporting to image\npushed digest sha256:" + strings.Repeat("c", 64) + "\n"
	digest, err := readDigestFromBuildctlOutput(strings.NewReader(stdout))
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("digest = %q", digest)
	}

	if _, err := readDigestFromBuildctlOutput(strings.NewReader("nothing")); err == nil {
		t.Fatal("expected error when digest is missing")
	}
}

func TestSanitizeServiceIDProducesPathSafeNames(t *testing.T) {
	cases := map[string]string{
		"my-service":    "my-service",
		"My Service!":   "my-service",
		"  ":            "service",
		"tenant/foo":    "tenant-foo",
		"weird/../path": "weird----path",
	}
	for in, want := range cases {
		if got := sanitizeServiceID(in); got != want {
			t.Fatalf("sanitizeServiceID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadLogTailBoundsLinesAndBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stderr.log")
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "line-%03d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tail := readLogTail(path, 5, 4096)
	lines := strings.Split(tail, "\n")
	if len(lines) != 5 || lines[0] != "line-095" || lines[4] != "line-099" {
		t.Fatalf("tail = %q, want the last five lines", tail)
	}
	if got := readLogTail(path, 100, 30); strings.Contains(got, "line-000") {
		t.Fatalf("byte cap not applied: %q", got)
	}
	if got := readLogTail(filepath.Join(dir, "missing.log"), 5, 4096); got != "" {
		t.Fatalf("missing file must yield empty tail, got %q", got)
	}
	if got := readLogTail(path, 0, 4096); got != "" {
		t.Fatalf("zero line budget must yield empty tail, got %q", got)
	}
}

// stderrFailRunner writes realistic BuildKit progress to stderr and then fails,
// mirroring a Dockerfile step that dies (here, the OOM-killed Angular build
// this was found with on a 2 GB host).
type stderrFailRunner struct{}

func (stderrFailRunner) Run(_ context.Context, _ string, _ []string, _, stderr io.Writer) error {
	fmt.Fprintln(stderr, "#8 [frontend 4/5] RUN npx ng build --configuration production")
	fmt.Fprintln(stderr, `ERROR: process "/bin/sh -c npx ng build" did not complete successfully: exit code: 137`)
	return errors.New("exit status 1")
}

func TestSolveFailureIncludesStderrTail(t *testing.T) {
	tmp := t.TempDir()
	contextDir := filepath.Join(tmp, "context")
	if err := os.MkdirAll(contextDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(contextDir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runner := BuildctlRunner{Runner: stderrFailRunner{}}
	_, err := runner.Build(context.Background(), BuildctlInvocation{
		ServiceID:      "svc_x",
		ContextDir:     contextDir,
		DockerfilePath: dockerfile,
		OutputDir:      filepath.Join(tmp, "out"),
		Image:          "registry.example/app:latest",
	})
	if err == nil {
		t.Fatal("expected solve failure")
	}
	if !strings.Contains(err.Error(), "exit code: 137") || !strings.Contains(err.Error(), "buildctl stderr") {
		t.Fatalf("error must carry the daemon's own output, got: %v", err)
	}
}
