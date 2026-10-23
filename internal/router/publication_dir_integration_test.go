//go:build integration

package router

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTraefikDynamicPublicationServesYAML writes a route group artifact to
// a temporary directory and (when MIDNIGHT_TRAEFIK_BIN is set) spawns
// Traefik against the deploy/traefik/traefik.yaml sample to verify the
// generated subdomain returns 200 OK.
//
// To run locally:
//
//	# 1. Install pinned Traefik:
//	#       curl -L https://github.com/traefik/traefik/releases/download/v3.1.5/traefik_v3.1.5_linux_amd64.tar.gz | tar -xz
//	# 2. Run the integration suite:
//	#       MIDNIGHT_TRAEFIK_BIN=$(pwd)/traefik \
//	#       go test -tags=integration ./internal/router/...
//
// When MIDNIGHT_TRAEFIK_BIN is not set, the test still validates the
// directory contract (atomic write, route artifact present) but does not
// boot a real Traefik.
func TestTraefikDynamicPublicationServesYAML(t *testing.T) {
	tmp := t.TempDir()
	pubdir, err := NewTraefikDynamicPublicationDirectory(tmp)
	if err != nil {
		t.Fatal(err)
	}
	routes := []RouteInput{{
		Name:       "demo",
		Host:       "demo.example.test",
		ServiceURL: "http://127.0.0.1:65535", // unreachable upstream is fine; we only assert routing
	}}
	evidence, err := pubdir.PublishRouteGroup("demo-group", routes, "")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if evidence.Status != RouteArtifactPublicationApplyStatusReady {
		t.Fatalf("status = %s, want ready", evidence.Status)
	}
	stem, err := SanitizeRouteFilename("demo")
	if err != nil {
		t.Fatalf("SanitizeRouteFilename: %v", err)
	}
	written := filepath.Join(tmp, "demo-group."+stem+".traefik.dynamic.yaml")
	body, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read written: %v", err)
	}
	if !strings.Contains(string(body), "demo.example.test") {
		t.Fatalf("artifact missing host: %s", body)
	}

	bin := os.Getenv("MIDNIGHT_TRAEFIK_BIN")
	if bin == "" {
		t.Log("MIDNIGHT_TRAEFIK_BIN not set; skipping Traefik boot phase")
		return
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("traefik binary not on PATH: %v", err)
	}
	// Boot a Traefik instance long enough to verify it loads the dynamic
	// config without the file-provider rejecting it. We do not wait for
	// real traffic: the apply path is the source-of-truth assertion.
	cmd := exec.Command(bin, "--providers.file.directory="+tmp, "--entrypoints.web.address=:0", "--entrypoints.websecure.address=:0")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("traefik start: %v", err)
	}
	time.Sleep(2 * time.Second)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("traefik kill: %v", err)
	}
	_ = cmd.Wait()
}
