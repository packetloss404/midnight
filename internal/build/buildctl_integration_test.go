//go:build integration

package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildctlIntegration exercises the live BuildKit + embedded registry
// path. It is gated by `//go:build integration` so default `go test ./...`
// runs do not require BuildKit, Docker, or a running registry.
//
// To run locally:
//
//	# 1. Start a buildkitd + registry pair (the deploy/registry/registry.yml
//	#    sample binds 127.0.0.1:5000):
//	#       buildkitd &
//	#       registry serve deploy/debian/registry.yml &
//	# 2. Export BUILDKIT_HOST so buildctl knows where to dial.
//	#       export BUILDKIT_HOST=unix:///run/buildkit/buildkitd.sock
//	# 3. Run the integration suite:
//	#       MIDNIGHT_BUILDCTL=$(which buildctl) \
//	#       MIDNIGHT_BUILDKIT_REGISTRY=127.0.0.1:5000 \
//	#       go test -tags=integration ./internal/build/...
//
// The test reproduces the digest across two consecutive solves of the same
// Dockerfile and asserts byte-equality of the produced SBOM and provenance.
func TestBuildctlIntegrationProducesDeterministicDigest(t *testing.T) {
	binary := os.Getenv("MIDNIGHT_BUILDCTL")
	registry := os.Getenv("MIDNIGHT_BUILDKIT_REGISTRY")
	if binary == "" || registry == "" {
		t.Skip("set MIDNIGHT_BUILDCTL and MIDNIGHT_BUILDKIT_REGISTRY to run this integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tmp := t.TempDir()
	contextDir := filepath.Join(tmp, "ctx")
	if err := os.MkdirAll(contextDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dockerfile := filepath.Join(contextDir, "Dockerfile")
	body := strings.Join([]string{
		"FROM scratch",
		"COPY hello /hello",
	}, "\n") + "\n"
	if err := os.WriteFile(dockerfile, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	hello := filepath.Join(contextDir, "hello")
	if err := os.WriteFile(hello, []byte("midnight-build-integration\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := BuildctlRunner{BuildctlPath: binary, Now: func() time.Time { return time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) }}
	if _, err := runner.VerifyVersion(ctx); err != nil {
		t.Fatalf("configured buildctl version check: %v", err)
	}

	out1 := filepath.Join(tmp, "out1")
	out2 := filepath.Join(tmp, "out2")
	inv := BuildctlInvocation{
		ServiceID:      "lane3-int",
		ContextDir:     contextDir,
		DockerfilePath: dockerfile,
		Image:          "midnight/lane3-int:integration",
		Registry:       registry,
		OutputDir:      out1,
		Insecure:       true,
		CacheRoot:      filepath.Join(tmp, "cache"),
	}
	res1, err := runner.Build(ctx, inv)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}

	inv.OutputDir = out2
	res2, err := runner.Build(ctx, inv)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if res1.Digest != res2.Digest {
		t.Fatalf("digests differ across runs: %s vs %s", res1.Digest, res2.Digest)
	}
	for name, paths := range map[string][2]string{
		"provenance": {res1.ProvenancePath, res2.ProvenancePath},
		"SBOM":       {res1.SBOMPath, res2.SBOMPath},
	} {
		first, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatalf("read first %s: %v", name, err)
		}
		second, err := os.ReadFile(paths[1])
		if err != nil {
			t.Fatalf("read second %s: %v", name, err)
		}
		if len(first) == 0 || string(first) != string(second) {
			t.Fatalf("%s is empty or differs across runs", name)
		}
	}
}
