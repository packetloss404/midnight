// Package build keeps the deterministic build planner that fronts the
// BuildKit + registry + provenance pipeline. The buildctl glue in this file
// is the Phase 1 Lane 3 live path: it shells out to a pinned `buildctl`
// binary, validates its version against versions.yaml, performs a Dockerfile
// solve, pushes the result to the embedded registry, and writes SBOM and
// provenance artifacts to a known directory.
//
// The runner deliberately stays at the os/exec level rather than importing
// github.com/moby/buildkit/client. The Go client transitively pulls
// containerd, runc, and the docker module, which is a several-MB
// supply-chain expansion the project is not yet ready to absorb. The CLI
// surface is also what we record in deployment snapshots per ADR-008, so
// keeping the boundary at the binary edge is faithful to the contract.
package build

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BuildctlInvocation describes a single buildctl solve invocation. It is the
// input to RunBuildctl. Every field is required unless explicitly noted.
type BuildctlInvocation struct {
	// ServiceID is the per-service identifier used to namespace the local
	// BuildKit cache directory. It is also recorded in provenance metadata.
	ServiceID string
	// ContextDir is the absolute path to the build context (the directory
	// that contains the source tree).
	ContextDir string
	// DockerfilePath is the absolute path to the Dockerfile inside ContextDir.
	// It is required for dockerfile.v0 and unused for gateway.v0.
	DockerfilePath string
	// Frontend selects the BuildKit frontend. Empty defaults to
	// dockerfile.v0 for backwards compatibility.
	Frontend string
	// FrontendSource is the immutable gateway frontend image reference.
	// Required for gateway.v0 and rejected unless digest-pinned.
	FrontendSource string
	// PlanPath is the absolute path to the frontend plan. Railpack's
	// gateway.v0 frontend expects railpack-plan.json in this file's directory.
	PlanPath string
	// Image is the destination image reference (repository[:tag] form).
	// The runner pushes to <Registry>/<Image> after building.
	Image string
	// Registry is the destination registry endpoint, e.g.
	// "registry.midnight.local:5000". An empty value means "use the
	// existing tag verbatim, do not push".
	Registry string
	// CacheRoot is the parent directory under which a per-service cache is
	// created (CacheRoot/<ServiceID>). Empty means "no cache".
	CacheRoot string
	// Platforms is the optional explicit multi-arch list. When empty the
	// runner builds for the host platform only.
	Platforms []string
	// BuildArgs are key=value build args. They MUST already pass the
	// default-profile whitelist enforced by BuildPlan.Validate.
	BuildArgs map[string]string
	// Secrets are BuildKit session secrets. Values are supplied to the
	// buildctl client through per-invocation environment variables and never
	// serialized into argv, provenance, SBOMs, or metadata. Secret IDs remain
	// visible because Dockerfiles and gateway frontends address mounts by ID.
	Secrets []BuildSecret
	// OutputDir is where the runner writes provenance.json, sbom.json,
	// digest.txt, and stdout/stderr logs. Created if missing.
	OutputDir string
	// Insecure controls whether buildctl is invoked with
	// --output type=image,push=true,registry.insecure=true. Used for the
	// embedded localhost registry.
	Insecure bool
}

// Validate rejects invocations whose required fields are missing or whose
// paths escape the build context.
func (inv BuildctlInvocation) Validate() error {
	if strings.TrimSpace(inv.ServiceID) == "" {
		return errors.New("buildctl invocation: service id is required")
	}
	if !filepath.IsAbs(inv.ContextDir) {
		return errors.New("buildctl invocation: context dir must be absolute")
	}
	switch inv.frontend() {
	case "dockerfile.v0":
		if !filepath.IsAbs(inv.DockerfilePath) {
			return errors.New("buildctl invocation: dockerfile path must be absolute")
		}
		rel, err := filepath.Rel(inv.ContextDir, inv.DockerfilePath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("buildctl invocation: dockerfile must live inside context dir")
		}
	case "gateway.v0":
		if !filepath.IsAbs(inv.PlanPath) {
			return errors.New("buildctl invocation: gateway plan path must be absolute")
		}
		if filepath.Base(inv.PlanPath) != "railpack-plan.json" {
			return errors.New("buildctl invocation: Railpack gateway plan must be named railpack-plan.json")
		}
		if !isDigestPinnedImage(inv.FrontendSource) {
			return errors.New("buildctl invocation: gateway frontend source must be digest-pinned")
		}
	default:
		return fmt.Errorf("buildctl invocation: unsupported frontend %q", inv.Frontend)
	}
	if strings.TrimSpace(inv.Image) == "" {
		return errors.New("buildctl invocation: image reference is required")
	}
	if strings.TrimSpace(inv.OutputDir) == "" {
		return errors.New("buildctl invocation: output dir is required")
	}
	if !filepath.IsAbs(inv.OutputDir) {
		return errors.New("buildctl invocation: output dir must be absolute")
	}
	for _, platform := range inv.Platforms {
		if strings.TrimSpace(platform) == "" {
			return errors.New("buildctl invocation: platform entries must not be empty")
		}
	}
	if err := ValidateBuildSecrets(inv.Secrets); err != nil {
		return fmt.Errorf("buildctl invocation: %w", err)
	}
	return nil
}

// BuildctlResult records the digest, SBOM ref, and provenance ref produced by
// a successful buildctl solve.
type BuildctlResult struct {
	Image          string
	Digest         string
	ProvenancePath string
	SBOMPath       string
	StdoutPath     string
	StderrPath     string
	StartedAt      time.Time
	FinishedAt     time.Time
	BuildKitVer    string
	Frontend       string
}

// CommandRunner is the seam tests use to substitute fake buildctl binaries.
// Production code uses ExecCommandRunner. The contract is intentionally
// narrow — exec.Cmd.Run() with stdout/stderr captured to buffers.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error
}

// EnvironmentCommandRunner is the capability required for BuildKit session
// secrets. The environment entries are scoped to one child process and are
// not inherited by the worker itself. A custom runner that does not implement
// this interface fails closed when an invocation carries secrets.
type EnvironmentCommandRunner interface {
	RunEnv(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error
}

// ExecCommandRunner uses os/exec to launch the supplied command.
type ExecCommandRunner struct{}

// Run launches name with args and pipes stdout/stderr to the provided
// writers. ctx cancellation kills the child process.
func (ExecCommandRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	return (ExecCommandRunner{}).RunEnv(ctx, name, args, nil, stdout, stderr)
}

// RunEnv launches name with a process-local environment overlay. Existing
// entries with the same key are removed so an operator environment cannot
// shadow Midnight's generated BuildKit secret source names.
func (ExecCommandRunner) RunEnv(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = environmentWithOverrides(os.Environ(), env)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// BuildctlRunner wires a CommandRunner to a pinned buildctl binary path. The
// zero value uses ExecCommandRunner and "buildctl" on PATH.
type BuildctlRunner struct {
	// BuildctlPath is the path to the pinned buildctl binary. Empty means
	// "buildctl on PATH". The alpha package installs the Midnight wrapper;
	// operators provide the pinned upstream binary path through package env.
	// Callers that mock it out in tests inject a CommandRunner instead.
	BuildctlPath string
	// Address is the BuildKit daemon address passed to buildctl's global
	// --addr option for solve invocations. Empty leaves daemon discovery to
	// buildctl (including its standard BUILDKIT_HOST environment variable).
	Address string
	// Runner is the command runner used for both --version and solve
	// invocations.
	Runner CommandRunner
	// Now is the clock seam used for provenance metadata. nil = time.Now().UTC().
	Now func() time.Time
}

// VerifyVersion runs `buildctl --version` and validates the reported version
// matches RequiredBuildKitVersion. It returns the actual version string for
// audit trails.
func (r BuildctlRunner) VerifyVersion(ctx context.Context) (string, error) {
	runner := r.runner()
	var stdout bytes.Buffer
	if err := runner.Run(ctx, r.binary(), []string{"--version"}, &stdout, io.Discard); err != nil {
		return "", fmt.Errorf("buildctl --version failed: %w", err)
	}
	actual := ParsePinnedBinaryVersion(stdout.String())
	if actual == "" {
		return "", fmt.Errorf("buildctl --version output unparsable: %q", stdout.String())
	}
	if err := ValidateBuildKitVersion(actual); err != nil {
		return actual, err
	}
	return actual, nil
}

// Build performs a buildctl solve and post-processing. It returns the
// produced image digest plus the paths to SBOM, provenance, and log files.
//
// Reproducibility: a SLSA v1 provenance statement is written to
// <OutputDir>/provenance.json with the Dockerfile hash, builder ID, build
// args, and resolved dependencies derived from FROM-line digests. A minimal
// CycloneDX SBOM listing the resolved base images is written to
// <OutputDir>/sbom.json. Both files use canonical JSON encoders so two runs
// over the same inputs produce byte-identical artifacts (modulo timestamps,
// which the planner normalizes to the supplied Now()).
func (r BuildctlRunner) Build(ctx context.Context, inv BuildctlInvocation) (BuildctlResult, error) {
	if err := inv.Validate(); err != nil {
		return BuildctlResult{}, err
	}
	startedAt := r.now()
	if err := os.MkdirAll(inv.OutputDir, 0o750); err != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: create output dir: %w", err)
	}

	args := r.assembleSolveArgs(inv)
	secretEnv := buildSecretEnvironment(inv.Secrets)
	secretValues := buildSecretValues(inv.Secrets)
	stdoutPath := filepath.Join(inv.OutputDir, "buildctl.stdout.log")
	stderrPath := filepath.Join(inv.OutputDir, "buildctl.stderr.log")
	stdout, err := os.OpenFile(stdoutPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: open stdout log: %w", err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(stderrPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: open stderr log: %w", err)
	}
	defer func() { _ = stderr.Close() }()

	stdoutWriter := NewSecretRedactingWriter(stdout, secretValues)
	stderrWriter := NewSecretRedactingWriter(stderr, secretValues)
	runner := r.runner()
	var runErr error
	if len(secretEnv) > 0 {
		envRunner, ok := runner.(EnvironmentCommandRunner)
		if !ok {
			runErr = errors.New("buildctl secret injection requires an environment-capable command runner")
		} else {
			runErr = envRunner.RunEnv(ctx, r.binary(), args, secretEnv, stdoutWriter, stderrWriter)
		}
	} else {
		runErr = runner.Run(ctx, r.binary(), args, stdoutWriter, stderrWriter)
	}
	flushErr := errors.Join(stdoutWriter.Close(), stderrWriter.Close())
	if runErr != nil {
		redactor := NewRedactor(secretValues)
		redacted := redactor.RedactText(runErr.Error())
		// Without the daemon's own output the operator sees only "exit
		// status 1" and has nothing to act on: the compiler error, the
		// failing RUN step and the unreachable-daemon case all look
		// identical. The log was already written through a redacting
		// writer; the tail is redacted again so a value that only appeared
		// in the exec error cannot slip through.
		if tail := redactor.RedactText(readLogTail(stderrPath, solveFailureTailLines, solveFailureTailBytes)); tail != "" {
			redacted = fmt.Sprintf("%s\n--- buildctl stderr (last %d lines) ---\n%s", redacted, solveFailureTailLines, tail)
		}
		return BuildctlResult{}, errors.Join(fmt.Errorf("buildctl solve failed: %s", redacted), flushErr)
	}
	if flushErr != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: redact solve output: %w", flushErr)
	}

	if _, err := stdout.Seek(0, io.SeekStart); err != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: rewind stdout log: %w", err)
	}
	metadataPath := filepath.Join(inv.OutputDir, "buildctl.metadata.json")
	digest, err := resolveImageDigest(metadataPath, stdout)
	if err != nil {
		return BuildctlResult{}, err
	}

	sbomPath := filepath.Join(inv.OutputDir, "sbom.json")
	provenancePath := filepath.Join(inv.OutputDir, "provenance.json")
	definitionPath := inv.DockerfilePath
	var froms []DockerfileFromDigest
	if inv.frontend() == "gateway.v0" {
		definitionPath = inv.PlanPath
	}
	definitionBytes, err := os.ReadFile(definitionPath)
	if err != nil {
		return BuildctlResult{}, fmt.Errorf("buildctl: read build definition: %w", err)
	}
	if inv.frontend() == "dockerfile.v0" {
		froms = ParseDockerfileFromDigests(string(definitionBytes))
	}
	definitionHash := sha256Hex(definitionBytes)
	finishedAt := r.now()
	provenance, err := r.buildProvenance(inv, digest, definitionHash, froms, startedAt, finishedAt)
	if err != nil {
		return BuildctlResult{}, err
	}
	provenanceBytes, err := MarshalProvenanceStatementJSON(provenance)
	if err != nil {
		return BuildctlResult{}, err
	}
	if err := writeFileAtomic(provenancePath, provenanceBytes, 0o640); err != nil {
		return BuildctlResult{}, err
	}
	sbomBytes, err := EmitCycloneDXSBOM(inv.ServiceID, inv.Image, digest, froms)
	if err != nil {
		return BuildctlResult{}, err
	}
	if err := writeFileAtomic(sbomPath, sbomBytes, 0o640); err != nil {
		return BuildctlResult{}, err
	}
	if err := writeFileAtomic(filepath.Join(inv.OutputDir, "digest.txt"), []byte(digest+"\n"), 0o640); err != nil {
		return BuildctlResult{}, err
	}

	return BuildctlResult{
		Image:          inv.Image,
		Digest:         digest,
		ProvenancePath: provenancePath,
		SBOMPath:       sbomPath,
		StdoutPath:     stdoutPath,
		StderrPath:     stderrPath,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		BuildKitVer:    RequiredBuildKitVersion,
		Frontend:       inv.frontend(),
	}, nil
}

func (r BuildctlRunner) buildProvenance(inv BuildctlInvocation, digest, definitionHash string, froms []DockerfileFromDigest, started, finished time.Time) (ProvenanceStatement, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return ProvenanceStatement{}, fmt.Errorf("buildctl: image digest %q must be a sha256 reference", digest)
	}
	subjectName := inv.Image
	statement := NewProvenanceStatement(subjectName, digest, "midnight-buildkit/"+RequiredBuildKitVersion, "https://midnight.dev/build/v1")

	external := map[string]interface{}{
		"serviceId": inv.ServiceID,
		"image":     inv.Image,
		"context":   filepath.Base(inv.ContextDir),
		"frontend":  inv.frontend(),
	}
	internal := map[string]interface{}{
		"buildDefinitionSha256": definitionHash,
		"buildkitVersion":       RequiredBuildKitVersion,
	}
	if inv.frontend() == "gateway.v0" {
		external["buildPlan"] = filepath.Base(inv.PlanPath)
		external["frontendSource"] = inv.FrontendSource
		internal["railpackVersion"] = RequiredRailpackVersion
	} else {
		external["dockerfile"] = filepath.Base(inv.DockerfilePath)
		internal["dockerfileSha256"] = definitionHash
	}
	if len(inv.Platforms) > 0 {
		external["platforms"] = append([]string(nil), inv.Platforms...)
	}
	if len(inv.BuildArgs) > 0 {
		args := make(map[string]string, len(inv.BuildArgs))
		for k, v := range inv.BuildArgs {
			if k == RailpackSecretsHashBuildArg {
				args[k] = redactedValue
				continue
			}
			args[k] = v
		}
		external["buildArgs"] = args
	}
	if ids := BuildSecretIDs(inv.Secrets); len(ids) > 0 {
		external["buildSecretIds"] = ids
	}
	statement.Predicate.BuildDefinition.ExternalParameters = external
	statement.Predicate.BuildDefinition.InternalParameters = internal
	deps := make([]SLSAResourceRef, 0, len(froms))
	for _, from := range froms {
		if from.Digest == "" {
			continue
		}
		deps = append(deps, SLSAResourceRef{
			URI:    "container://" + from.Image,
			Digest: map[string]string{"sha256": strings.TrimPrefix(from.Digest, "sha256:")},
		})
	}
	statement = statement.WithResolvedDependencies(deps)
	statement = statement.WithMetadata(SLSABuildMetadata{
		InvocationID: inv.ServiceID + "@" + finished.UTC().Format(time.RFC3339Nano),
		StartedOn:    started.UTC(),
		FinishedOn:   finished.UTC(),
	})
	if err := statement.Validate(); err != nil {
		return ProvenanceStatement{}, err
	}
	return statement, nil
}

func (r BuildctlRunner) assembleSolveArgs(inv BuildctlInvocation) []string {
	args := make([]string, 0, 24)
	if address := strings.TrimSpace(r.Address); address != "" {
		args = append(args, "--addr", address)
	}
	args = append(args, "build", "--frontend", inv.frontend(), "--local", "context="+inv.ContextDir)
	if inv.frontend() == "gateway.v0" {
		args = append(args,
			"--local", "dockerfile="+filepath.Dir(inv.PlanPath),
			"--opt", "source="+inv.FrontendSource,
		)
	} else {
		args = append(args,
			"--local", "dockerfile="+filepath.Dir(inv.DockerfilePath),
			"--opt", "filename="+filepath.Base(inv.DockerfilePath),
		)
	}
	if len(inv.Platforms) > 0 {
		args = append(args, "--opt", "platform="+strings.Join(inv.Platforms, ","))
	}
	keys := make([]string, 0, len(inv.BuildArgs))
	for k := range inv.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--opt", "build-arg:"+k+"="+inv.BuildArgs[k])
	}
	for index, secret := range sortedBuildSecrets(inv.Secrets) {
		args = append(args, "--secret", fmt.Sprintf(
			"id=%s,env=%s%d",
			secret.ID,
			buildSecretEnvPrefix,
			index,
		))
	}
	if inv.CacheRoot != "" {
		cacheDir := filepath.Join(inv.CacheRoot, sanitizeServiceID(inv.ServiceID))
		args = append(args,
			"--export-cache", "type=local,mode=max,dest="+cacheDir,
			"--import-cache", "type=local,src="+cacheDir,
		)
	}
	output := "type=image,name=" + buildctlImageRef(inv)
	if inv.Registry != "" {
		output += ",push=true"
		if inv.Insecure {
			output += ",registry.insecure=true"
		}
	}
	args = append(args, "--output", output)
	args = append(args, "--metadata-file", filepath.Join(inv.OutputDir, "buildctl.metadata.json"))
	return args
}

func (inv BuildctlInvocation) frontend() string {
	if strings.TrimSpace(inv.Frontend) == "" {
		return "dockerfile.v0"
	}
	return strings.TrimSpace(inv.Frontend)
}

func isDigestPinnedImage(ref string) bool {
	before, digest, ok := strings.Cut(strings.TrimSpace(ref), "@sha256:")
	if !ok || before == "" || len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (r BuildctlRunner) binary() string {
	if strings.TrimSpace(r.BuildctlPath) != "" {
		return r.BuildctlPath
	}
	return "buildctl"
}

func (r BuildctlRunner) runner() CommandRunner {
	if r.Runner != nil {
		return r.Runner
	}
	return ExecCommandRunner{}
}

func (r BuildctlRunner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func buildctlImageRef(inv BuildctlInvocation) string {
	if inv.Registry == "" {
		return inv.Image
	}
	if strings.Contains(inv.Image, "/") {
		// Operator-supplied a full reference; respect it.
		return inv.Image
	}
	return strings.TrimSuffix(inv.Registry, "/") + "/" + inv.Image
}

// resolveImageDigest determines the pushed image digest, preferring buildctl's
// --metadata-file JSON (the canonical, unambiguous source) and falling back to
// a scan of the solve stdout log only when the metadata file is absent or does
// not carry a digest. A malformed digest in the metadata file is a hard error
// — we never silently drop to the noisier stdout scan when the authoritative
// source is present but corrupt.
func resolveImageDigest(metadataPath string, stdout io.Reader) (string, error) {
	if data, readErr := os.ReadFile(metadataPath); readErr == nil {
		digest, parseErr := ParseMetadataDigest(data)
		switch {
		case parseErr == nil:
			return digest, nil
		case errors.Is(parseErr, ErrMetadataDigestMissing):
			// Metadata file present but no digest key: fall through to the
			// stdout scan below.
		case errors.Is(parseErr, ErrInvalidImageDigest):
			return "", fmt.Errorf("buildctl: metadata digest invalid: %w", parseErr)
		default:
			return "", fmt.Errorf("buildctl: parse metadata file: %w", parseErr)
		}
	}
	return readDigestFromBuildctlOutput(stdout)
}

// readDigestFromBuildctlOutput scans a buildctl stdout/stderr log for image
// digests. It delegates to ParseImageDigest, which collects every distinct
// sha256 token and rejects ambiguity rather than blindly returning the first
// match (which could be a base-image or cache-layer digest).
func readDigestFromBuildctlOutput(r io.Reader) (string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("buildctl: read stdout: %w", err)
	}
	return ParseImageDigest(string(data))
}

func readDockerfileFroms(path string) ([]DockerfileFromDigest, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("buildctl: read dockerfile: %w", err)
	}
	return ParseDockerfileFromDigests(string(data)), data, nil
}

func sanitizeServiceID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(id)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "service"
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("buildctl: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("buildctl: write temp: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("buildctl: chmod temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("buildctl: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("buildctl: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("buildctl: rename temp: %w", err)
	}
	return nil
}

// Bounds for the diagnostic tail attached to a failed solve. The tail travels
// into the operation's durable build log, so it stays small enough to read at
// a glance and to store as one record.
const (
	solveFailureTailLines = 30
	solveFailureTailBytes = 4096
)

// readLogTail returns at most maxLines trailing lines of path, capped at
// maxBytes counted from the end. It is best-effort: a missing or unreadable
// log yields an empty string, never an error, because the caller is already
// reporting a failure and must not mask it with a logging problem.
func readLogTail(path string, maxLines, maxBytes int) string {
	if path == "" || maxLines <= 0 || maxBytes <= 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
		// Drop the partial first line produced by the byte cut.
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 && idx+1 < len(data) {
			data = data[idx+1:]
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	return out
}
