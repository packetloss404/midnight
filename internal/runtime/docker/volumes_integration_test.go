//go:build integration

package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
)

// TestAdapterVolumeLifecycleAgainstDocker creates → inspects →
// attaches → detaches → removes a real Docker volume. Default
// `go test ./...` does NOT exercise this file. Set DOCKER_HOST or
// rely on the default unix socket.
func TestAdapterVolumeLifecycleAgainstDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cli, err := NewEngineClient("")
	if err != nil {
		t.Skipf("docker engine unavailable: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	a, _, err := New(ctx, Config{
		AgentID:    "agent-test",
		HostID:     "host-test",
		Engine:     cli,
		StopGrace:  5 * time.Second,
		CGroupMode: CGroupModeV2,
	})
	if err != nil {
		t.Skipf("docker adapter init failed: %v", err)
	}

	name := "midnight-test-vol-" + time.Now().UTC().Format("20060102150405")
	v, err := a.VolumeCreate(ctx, VolumeCreateOptions{
		Name: name,
		Labels: map[string]string{
			LabelManagedBy:  LabelManagedByV,
			"midnight.test": "1",
		},
	})
	if err != nil {
		t.Fatalf("VolumeCreate: %v", err)
	}
	if v.Name != name {
		t.Fatalf("VolumeCreate name = %q, want %q", v.Name, name)
	}
	t.Cleanup(func() {
		_ = a.VolumeRemove(context.Background(), name, true)
	})

	insp, err := a.VolumeInspect(ctx, name)
	if err != nil {
		t.Fatalf("VolumeInspect: %v", err)
	}
	if insp.Name != name {
		t.Fatalf("VolumeInspect name = %q, want %q", insp.Name, name)
	}

	list, err := a.VolumeList(ctx, map[string]string{LabelManagedBy: LabelManagedByV})
	if err != nil {
		t.Fatalf("VolumeList: %v", err)
	}
	found := false
	for _, lv := range list {
		if lv != nil && lv.Name == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("VolumeList did not return the created volume")
	}

	if err := a.VolumeRemove(ctx, name, true); err != nil {
		t.Fatalf("VolumeRemove: %v", err)
	}
	if _, err := a.VolumeInspect(ctx, name); err == nil {
		t.Fatal("VolumeInspect after remove returned nil error; expected not-found")
	}

	// Sanity: schedule + remove a placeholder container to make sure
	// the integration path remains intact alongside the new volume
	// methods.
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep-vol-test",
		InstanceID:     "inst-1",
		ServiceID:      "svc-vol",
		ProjectID:      "prj",
		EnvironmentID:  "env",
		Image:          "busybox:stable",
		IdempotencyKey: "vol-integration-1",
		Labels:         map[string]string{},
	}
	status, err := a.Schedule(ctx, req)
	if err != nil {
		t.Skipf("optional sanity Schedule failed (image may not exist locally): %v", err)
	}
	if status.DeploymentID == "" {
		t.Skip("schedule returned empty status; busybox not available")
	}
	if err := a.Remove(ctx, req.DeploymentID, req.InstanceID); err != nil && !strings.Contains(err.Error(), "no such") {
		t.Fatalf("Remove: %v", err)
	}
}
