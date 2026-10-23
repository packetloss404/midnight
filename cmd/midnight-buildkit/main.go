// Command midnight-buildkit is the Phase 1 Lane 3 BuildKit launcher. It
// wraps the pinned `buildctl` binary, performs a Dockerfile solve for one
// service, pushes the produced image to the embedded registry, and writes
// SBOM + provenance artifacts to a deterministic output directory.
//
// Subcommands:
//
//	midnight-buildkit version
//	midnight-buildkit serve [--buildkitd <path>] [--addr <addr>] [--root <path>]
//	midnight-buildkit verify
//	midnight-buildkit build [flags]
//	midnight-buildkit worker [flags]   # drain `build` queue jobs
//
// `verify` calls `buildctl --version` and asserts the pinned version. `build`
// runs the solve. All cache, output, and registry destinations are passed
// explicitly; the binary refuses to write anywhere not specified by a flag.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "midnight-buildkit: subcommand required (version|serve|verify|build|worker)")
		os.Exit(2)
	}
	args := os.Args[2:]
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)
	case "serve":
		if err := runServe(ctx, args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-buildkit serve:", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(ctx, args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-buildkit verify:", err)
			os.Exit(1)
		}
	case "build":
		if err := runBuild(ctx, args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-buildkit build:", err)
			os.Exit(1)
		}
	case "worker":
		if err := runWorker(ctx, args, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "midnight-buildkit worker:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "midnight-buildkit: unknown subcommand", os.Args[1])
		os.Exit(2)
	}
}

type serveConfig struct {
	Buildkitd string
	Addr      string
	Root      string
	Group     string
}

func runServe(ctx context.Context, args []string, stdout, stderr *os.File) error {
	cfg, err := parseServeConfig(args)
	if err != nil {
		return err
	}
	if err := verifyBuildkitdVersion(ctx, cfg.Buildkitd); err != nil {
		return err
	}
	buildkitdArgs := []string{"--addr", cfg.Addr, "--root", cfg.Root}
	if cfg.Group != "" {
		buildkitdArgs = append(buildkitdArgs, "--group", cfg.Group)
	}
	cmd := exec.CommandContext(ctx, cfg.Buildkitd, buildkitdArgs...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func parseServeConfig(args []string) (serveConfig, error) {
	cfg := serveConfig{
		Buildkitd: envFirst("buildkitd", "MIDNIGHT_BUILDKITD_BIN"),
		Addr:      envFirst("unix:///run/midnight/buildkit/buildkitd.sock", "MIDNIGHT_BUILDKIT_ADDR"),
		Root:      envFirst("/var/lib/midnight/buildkit", "MIDNIGHT_BUILDKIT_ROOT"),
		Group:     envFirst("", "MIDNIGHT_BUILDKIT_GROUP"),
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.StringVar(&cfg.Buildkitd, "buildkitd", cfg.Buildkitd, "path to pinned buildkitd binary")
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "buildkitd listen address")
	fs.StringVar(&cfg.Root, "root", cfg.Root, "buildkitd state root")
	fs.StringVar(&cfg.Group, "group", cfg.Group, "group that owns the BuildKit Unix socket")
	if err := fs.Parse(args); err != nil {
		return serveConfig{}, err
	}
	if strings.TrimSpace(cfg.Buildkitd) == "" {
		return serveConfig{}, fmt.Errorf("buildkitd path is required")
	}
	if strings.TrimSpace(cfg.Addr) == "" {
		return serveConfig{}, fmt.Errorf("buildkitd address is required")
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return serveConfig{}, fmt.Errorf("buildkitd root is required")
	}
	return cfg, nil
}

func runVerify(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	binary := fs.String("buildctl", envFirst("buildctl", "MIDNIGHT_BUILDCTL"), "path to pinned buildctl binary (default: $MIDNIGHT_BUILDCTL or buildctl on PATH)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	runner := build.BuildctlRunner{BuildctlPath: *binary}
	got, err := runner.VerifyVersion(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "verify failed:", err)
		return err
	}
	fmt.Fprintf(stdout, "buildctl %s (matches pinned %s)\n", got, build.RequiredBuildKitVersion)
	return nil
}

func runBuild(ctx context.Context, args []string, stdout, stderr *os.File) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	service := fs.String("service", "", "service id (used for cache namespace + provenance metadata)")
	contextDir := fs.String("context", "", "absolute path to build context")
	dockerfile := fs.String("dockerfile", "", "absolute path to Dockerfile (must live inside --context)")
	image := fs.String("image", "", "destination image reference (e.g. midnight/demo:phase1)")
	registry := fs.String("registry", "", "destination registry endpoint (e.g. localhost:5000)")
	cacheRoot := fs.String("cache-root", "", "parent directory for per-service local BuildKit cache")
	output := fs.String("out", "", "output directory for sbom.json, provenance.json, digest.txt")
	platforms := fs.String("platforms", "", "comma-separated list of platforms (multi-arch is opt-in)")
	insecure := fs.Bool("insecure-registry", false, "allow pushing to a registry without TLS (use only for the embedded localhost registry)")
	binary := fs.String("buildctl", envFirst("buildctl", "MIDNIGHT_BUILDCTL"), "path to pinned buildctl binary")
	address := fs.String("addr", envFirst("", "MIDNIGHT_BUILDKIT_ADDR"), "BuildKit daemon address (empty uses buildctl/BUILDKIT_HOST discovery)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contextDir != "" && !filepath.IsAbs(*contextDir) {
		abs, err := filepath.Abs(*contextDir)
		if err == nil {
			*contextDir = abs
		}
	}
	if *dockerfile != "" && !filepath.IsAbs(*dockerfile) {
		abs, err := filepath.Abs(*dockerfile)
		if err == nil {
			*dockerfile = abs
		}
	}
	if *output != "" && !filepath.IsAbs(*output) {
		abs, err := filepath.Abs(*output)
		if err == nil {
			*output = abs
		}
	}

	runner := build.BuildctlRunner{BuildctlPath: *binary, Address: *address}
	if _, err := runner.VerifyVersion(ctx); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	inv := build.BuildctlInvocation{
		ServiceID:      *service,
		ContextDir:     *contextDir,
		DockerfilePath: *dockerfile,
		Image:          *image,
		Registry:       *registry,
		CacheRoot:      *cacheRoot,
		OutputDir:      *output,
		Insecure:       *insecure,
		Platforms:      splitNonEmpty(*platforms, ","),
	}
	res, err := runner.Build(ctx, inv)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "image: %s\n", res.Image)
	fmt.Fprintf(stdout, "digest: %s\n", res.Digest)
	fmt.Fprintf(stdout, "provenance: %s\n", res.ProvenancePath)
	fmt.Fprintf(stdout, "sbom: %s\n", res.SBOMPath)
	return nil
}

func splitNonEmpty(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func verifyBuildkitdVersion(ctx context.Context, binary string) error {
	out, err := execCommandOutput(ctx, binary, "--version")
	if err != nil {
		return fmt.Errorf("buildkitd --version failed: %w", err)
	}
	actual := build.ParsePinnedBinaryVersion(string(out))
	if actual == "" {
		return fmt.Errorf("buildkitd --version output unparsable: %q", string(out))
	}
	if err := build.ValidateBuildKitVersion(actual); err != nil {
		return err
	}
	return nil
}

var execCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func envFirst(fallback string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return fallback
}
