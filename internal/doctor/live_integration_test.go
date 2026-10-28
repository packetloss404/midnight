//go:build integration

package doctor

import (
	"context"
	"os"
	"testing"

	"github.com/midnight/midnight/internal/preflight"
)

// TestLiveDoctorOnHost runs the cgroup/Docker read probes against the real
// host. It is gated behind both the `integration` build tag and the
// MIDNIGHT_DOCTOR_LIVE=1 environment variable so default `go test`
// runs and CI integration runs without the env var both skip.
func TestLiveDoctorOnHost(t *testing.T) {
	if os.Getenv("MIDNIGHT_DOCTOR_LIVE") != "1" {
		t.Skip("skipped: MIDNIGHT_DOCTOR_LIVE=1 not set")
	}
	src := NewLiveHostSource(preflight.HostSource{})
	results := RunLiveChecks(context.Background(), LiveOptions{
		Source: src,
		Live:   true,
		// These are actual non-mutating host probes. This canary does not
		// claim installed service health or execute the route write probe.
		FilterIDs: []string{"OR-CHK-DOC-001", "OR-CHK-DOC-003"},
	})
	if len(results) != 2 {
		t.Fatalf("expected both live host probes, got %d", len(results))
	}
	for _, r := range results {
		t.Logf("[%s] %s: %s — %s", r.Status, r.ID, r.Description, r.Message)
		if r.Status != StatusPass {
			t.Errorf("live host probe %s did not pass (skips are not evidence): status=%s err=%v", r.ID, r.Status, r.Err)
		}
	}
}
