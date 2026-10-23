package main

import (
	"context"
	"testing"

	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/db/pg"
)

func TestParseServeConfigDefaultsAndFlags(t *testing.T) {
	clearBuildkitEnv(t)

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	}
	if cfg.Buildkitd != "buildkitd" {
		t.Fatalf("Buildkitd = %q, want buildkitd", cfg.Buildkitd)
	}
	if cfg.Addr != "unix:///run/midnight/buildkit/buildkitd.sock" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
	if cfg.Root != "/var/lib/midnight/buildkit" {
		t.Fatalf("Root = %q", cfg.Root)
	}
	if cfg.Group != "" {
		t.Fatalf("Group = %q, want empty outside packaged configuration", cfg.Group)
	}

	cfg, err = parseServeConfig([]string{"--buildkitd", "/usr/local/bin/buildkitd", "--addr", "tcp://127.0.0.1:1234", "--root", "/tmp/buildkit", "--group", "midnight"})
	if err != nil {
		t.Fatalf("parseServeConfig(flags) error = %v", err)
	}
	if cfg.Buildkitd != "/usr/local/bin/buildkitd" || cfg.Addr != "tcp://127.0.0.1:1234" || cfg.Root != "/tmp/buildkit" || cfg.Group != "midnight" {
		t.Fatalf("config = %#v, want flag overrides", cfg)
	}
}

func TestParseServeConfigUsesEnvironment(t *testing.T) {
	clearBuildkitEnv(t)
	t.Setenv("MIDNIGHT_BUILDKITD_BIN", "/opt/buildkitd")
	t.Setenv("MIDNIGHT_BUILDKIT_ADDR", "unix:///tmp/buildkit.sock")
	t.Setenv("MIDNIGHT_BUILDKIT_ROOT", "/tmp/buildkit-root")
	t.Setenv("MIDNIGHT_BUILDKIT_GROUP", "build-users")

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	}
	if cfg.Buildkitd != "/opt/buildkitd" || cfg.Addr != "unix:///tmp/buildkit.sock" || cfg.Root != "/tmp/buildkit-root" || cfg.Group != "build-users" {
		t.Fatalf("config = %#v, want env overrides", cfg)
	}
}

func TestRunVerifyUsesBuildctlEnvironment(t *testing.T) {
	clearBuildkitEnv(t)
	t.Setenv("MIDNIGHT_BUILDCTL", "/opt/buildctl")

	cfg := envFirst("buildctl", "MIDNIGHT_BUILDCTL")
	if cfg != "/opt/buildctl" {
		t.Fatalf("buildctl path = %q, want env override", cfg)
	}
}

func TestParseWorkerConfigPropagatesBuildKitAndRegistryEnvironment(t *testing.T) {
	clearBuildkitEnv(t)
	t.Setenv("MIDNIGHT_BUILDKIT_ADDR", "unix:///run/midnight/buildkit/custom.sock")
	t.Setenv("MIDNIGHT_REGISTRY_ENDPOINT", "")
	t.Setenv("MIDNIGHT_REGISTRY_ADDR", "127.0.0.1:5500")

	cfg, err := parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig() error = %v", err)
	}
	if cfg.BuildkitAddr != "unix:///run/midnight/buildkit/custom.sock" {
		t.Fatalf("BuildkitAddr = %q", cfg.BuildkitAddr)
	}
	if cfg.Registry != "127.0.0.1:5500" {
		t.Fatalf("Registry = %q, want listener fallback", cfg.Registry)
	}

	t.Setenv("MIDNIGHT_REGISTRY_ENDPOINT", "registry.internal:5000")
	cfg, err = parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig(endpoint) error = %v", err)
	}
	if cfg.Registry != "registry.internal:5000" {
		t.Fatalf("Registry = %q, want explicit endpoint", cfg.Registry)
	}
}

func TestParseWorkerConfigUsesFileBackedPostgresDSN(t *testing.T) {
	clearBuildkitEnv(t)

	cfg, err := parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig() error = %v", err)
	}
	if cfg.PostgresDSN != "" {
		t.Fatalf("PostgresDSN = %q, want empty when no env DSN is set", cfg.PostgresDSN)
	}
	if cfg.PostgresDSNFile != pg.DefaultDSNFile {
		t.Fatalf("PostgresDSNFile = %q, want %q", cfg.PostgresDSNFile, pg.DefaultDSNFile)
	}

	t.Setenv(pg.EnvDSNFile, "/run/secrets/midnight.dsn")
	cfg, err = parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig(file override) error = %v", err)
	}
	if cfg.PostgresDSNFile != "/run/secrets/midnight.dsn" {
		t.Fatalf("PostgresDSNFile = %q, want file override", cfg.PostgresDSNFile)
	}
}

func TestParseWorkerConfigPropagatesNonSecretGitHubAppCoordinates(t *testing.T) {
	clearBuildkitEnv(t)
	t.Setenv("MIDNIGHT_GITHUB_APP_ID", "123")
	t.Setenv("MIDNIGHT_GITHUB_PRIVATE_KEY_PATH", "/run/secrets/github.pem")
	t.Setenv("MIDNIGHT_GITHUB_API_BASE_URL", "https://github.example/api/v3")
	t.Setenv("MIDNIGHT_GITHUB_CLONE_HOST", "github.example")

	cfg, err := parseWorkerConfig(nil)
	if err != nil {
		t.Fatalf("parseWorkerConfig() error = %v", err)
	}
	if cfg.GitHubAppID != "123" ||
		cfg.GitHubKeyPath != "/run/secrets/github.pem" ||
		cfg.GitHubAPIBaseURL != "https://github.example/api/v3" ||
		cfg.GitHubCloneHost != "github.example" {
		t.Fatalf("GitHub worker config = %#v", cfg)
	}
}

func TestVerifyBuildkitdVersionAcceptsPin(t *testing.T) {
	oldExec := execCommandOutput
	defer func() { execCommandOutput = oldExec }()
	execCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "/opt/buildkitd" {
			t.Fatalf("name = %q, want /opt/buildkitd", name)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("args = %#v, want --version", args)
		}
		return []byte("buildkitd github.com/moby/buildkit " + build.RequiredBuildKitVersion + " deadbeef\n"), nil
	}

	if err := verifyBuildkitdVersion(context.Background(), "/opt/buildkitd"); err != nil {
		t.Fatalf("verifyBuildkitdVersion() error = %v", err)
	}
}

func TestVerifyBuildkitdVersionRejectsMismatch(t *testing.T) {
	oldExec := execCommandOutput
	defer func() { execCommandOutput = oldExec }()
	execCommandOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("buildkitd github.com/moby/buildkit v0.99.0 deadbeef\n"), nil
	}

	if err := verifyBuildkitdVersion(context.Background(), "/opt/buildkitd"); err == nil {
		t.Fatal("verifyBuildkitdVersion() error = nil, want mismatch")
	}
}

func clearBuildkitEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"MIDNIGHT_BUILDKITD_BIN",
		"MIDNIGHT_BUILDCTL",
		"MIDNIGHT_BUILDKIT_ADDR",
		"MIDNIGHT_BUILDKIT_ROOT",
		"MIDNIGHT_BUILDKIT_GROUP",
		"MIDNIGHT_REGISTRY_ADDR",
		"MIDNIGHT_REGISTRY_ENDPOINT",
		"MIDNIGHT_GITHUB_APP_ID",
		"MIDNIGHT_GITHUB_PRIVATE_KEY_PATH",
		"MIDNIGHT_GITHUB_API_BASE_URL",
		"MIDNIGHT_GITHUB_CLONE_HOST",
	} {
		t.Setenv(name, "")
	}
}
