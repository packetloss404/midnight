//go:build integration

package doctor

import (
	"context"
	"os"
	"testing"

	"github.com/midnight/midnight/internal/preflight"
)

// TestLiveDoctorOnHost runs the OR-CHK-DOC registry against the real
// host. It is gated behind both the `integration` build tag and the
// MIDNIGHT_DOCTOR_LIVE=1 environment variable so default `go test`
// runs and CI integration runs without the env var both skip.
func TestLiveDoctorOnHost(t *testing.T) {
	if os.Getenv("MIDNIGHT_DOCTOR_LIVE") != "1" {
		t.Skip("skipped: MIDNIGHT_DOCTOR_LIVE=1 not set")
	}
	src := preflight.HostSource{
		Reader: preflight.OSFileReader{},
		Paths: preflight.HostPaths{
			MountInfo:        "/proc/self/mountinfo",
			DockerDaemonJSON: "/etc/docker/daemon.json",
		},
	}
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: src,
		Live:   true,
	})
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	for _, r := range results {
		t.Logf("[%s] %s: %s — %s", r.Status, r.ID, r.Description, r.Message)
	}
}
