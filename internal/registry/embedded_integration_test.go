//go:build integration

package registry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestEmbeddedRegistryStartStop launches the pinned `registry` binary
// against the sample Lane 3 distribution config and terminates it cleanly.
// It is gated by `//go:build integration` so default test runs do not
// require the binary to be on PATH.
//
// To run locally:
//
//	# 1. Install distribution v3:
//	#       go install github.com/distribution/distribution/v3/cmd/registry@v3.0.0
//	# 2. Make sure registry.yml's rootdirectory is writable:
//	#       sudo install -d -o $USER /var/lib/midnight/registry
//	# 3. Run the integration suite:
//	#       MIDNIGHT_REGISTRY_BIN=$(which registry) \
//	#       MIDNIGHT_REGISTRY_CONFIG=$(pwd)/deploy/debian/registry.yml \
//	#       go test -tags=integration ./internal/registry/...
func TestEmbeddedRegistryStartStop(t *testing.T) {
	binary := os.Getenv("MIDNIGHT_REGISTRY_BIN")
	config := os.Getenv("MIDNIGHT_REGISTRY_CONFIG")
	if binary == "" || config == "" {
		t.Skip("set MIDNIGHT_REGISTRY_BIN and MIDNIGHT_REGISTRY_CONFIG to run this integration test")
	}
	if !filepath.IsAbs(config) {
		abs, err := filepath.Abs(config)
		if err != nil {
			t.Fatal(err)
		}
		config = abs
	}
	if _, err := exec.LookPath(binary); err != nil {
		t.Skipf("registry binary not on PATH: %v", err)
	}

	embedded := &EmbeddedRegistry{Binary: binary, ConfigPath: config, Logger: os.Stderr}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := embedded.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(2 * time.Second) // give the registry a moment to bind its port
	if err := embedded.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
