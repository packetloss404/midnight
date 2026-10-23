//go:build integration
// +build integration

// Package conformance integration tests exercise the Docker RuntimeAdapter
// against a real Docker daemon. These tests are gated behind the
// `integration` build tag so default `go test ./...` does not require Docker.
//
// To run locally on Linux/macOS:
//
//	go test -tags=integration ./internal/runtime/...
//
// On Windows, point DOCKER_HOST at a Linux daemon (e.g. WSL) and run the same
// command. CI runs this suite on Ubuntu 24.04 against a system Docker
// daemon; see `/docs/runtime/conformance.md` for the matrix.
package conformance

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime/docker"
)

// TestDockerAdapterConformance runs the conformance suite against a real
// Docker daemon. It is the release gate referenced by ADR-015 §3.
//
// The test pulls a tiny stateless workload (busybox) and walks
// schedule → start → reconcile → stop → remove. The image reference is
// configurable so air-gapped CI can mirror it locally.
func TestDockerAdapterConformance(t *testing.T) {
	if os.Getenv("MIDNIGHT_RUNTIME_CONFORMANCE_DISABLED") != "" {
		t.Skip("conformance gated off via MIDNIGHT_RUNTIME_CONFORMANCE_DISABLED")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	engine, err := docker.NewEngineClient(os.Getenv("DOCKER_HOST"))
	if err != nil {
		t.Skipf("docker engine client unavailable: %v", err)
	}
	defer engine.Close()

	adapter, baseline, err := docker.New(ctx, docker.Config{
		AgentID: "conformance-agent",
		HostID:  "conformance-host",
		Engine:  engine,
	})
	if err != nil {
		t.Skipf("docker adapter init failed (likely missing daemon): %v", err)
	}
	t.Logf("baseline ok: api=%s engine=%s storage=%s log=%s",
		baseline.APIVersion, baseline.EngineVersion, baseline.StorageDriver, baseline.LoggingDriver)

	fixture := DefaultFixture()
	if v := os.Getenv("MIDNIGHT_RUNTIME_CONFORMANCE_IMAGE"); v != "" {
		fixture.Image = v
	} else {
		fixture.Image = "busybox:stable"
	}

	report := Run(ctx, adapter, fixture)
	for _, result := range report.Results {
		if result.Passed {
			t.Logf("PASS %s", result.Name)
			continue
		}
		t.Errorf("FAIL %s: %s", result.Name, result.Message)
	}
	if !report.Ok() {
		t.Fatalf("conformance suite failed: %d/%d passed", report.Passed, report.Total)
	}
}
