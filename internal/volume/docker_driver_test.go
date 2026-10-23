// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeDockerVolumeClient is a deterministic in-memory replacement for the
// Docker SDK volume client. It records create / inspect / remove calls so
// tests can assert idempotency and label propagation.
type fakeDockerVolumeClient struct {
	mu      sync.Mutex
	volumes map[string]DockerInfo
	creates int
	inspect int
	removes int
	// failCreate forces the next VolumeCreate to return an error.
	failCreate error
}

func newFakeDocker() *fakeDockerVolumeClient {
	return &fakeDockerVolumeClient{volumes: map[string]DockerInfo{}}
}

func (f *fakeDockerVolumeClient) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (DockerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.failCreate != nil {
		err := f.failCreate
		f.failCreate = nil
		return DockerInfo{}, err
	}
	if existing, ok := f.volumes[name]; ok {
		return existing, fmt.Errorf("docker: volume %q already exists", name)
	}
	info := DockerInfo{
		Name:       name,
		Driver:     driver,
		Labels:     copyLabels(labels),
		Mountpoint: "/var/lib/docker/volumes/" + name + "/_data",
	}
	f.volumes[name] = info
	return info, nil
}

func (f *fakeDockerVolumeClient) VolumeInspect(ctx context.Context, name string) (DockerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspect++
	v, ok := f.volumes[name]
	if !ok {
		return DockerInfo{}, fmt.Errorf("docker: no such volume %q", name)
	}
	return v, nil
}

func (f *fakeDockerVolumeClient) VolumeRemove(ctx context.Context, name string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	if _, ok := f.volumes[name]; !ok {
		return fmt.Errorf("docker: no such volume %q", name)
	}
	delete(f.volumes, name)
	return nil
}

func copyLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestDockerDriverEnsureIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := newFakeDocker()
	d := NewDockerDriver(fake)

	info, err := d.EnsureDockerVolume(ctx, "vol_abc", "svc_one", "env_prod", "prj_one", "data", 1024)
	if err != nil {
		t.Fatalf("EnsureDockerVolume: %v", err)
	}
	wantName := DockerVolumeNamePrefix + "vol_abc"
	if info.Name != wantName {
		t.Fatalf("Name = %s, want %s", info.Name, wantName)
	}
	if got := info.Labels[LabelManagedBy]; got != LabelManagedByV {
		t.Fatalf("LabelManagedBy = %q, want %q", got, LabelManagedByV)
	}
	if got := info.Labels[LabelVolumeID]; got != "vol_abc" {
		t.Fatalf("LabelVolumeID = %q, want vol_abc", got)
	}
	if got := info.Labels[LabelServiceID]; got != "svc_one" {
		t.Fatalf("LabelServiceID = %q, want svc_one", got)
	}

	// Second call should NOT create again; idempotent via inspect.
	if _, err := d.EnsureDockerVolume(ctx, "vol_abc", "svc_one", "env_prod", "prj_one", "data", 1024); err != nil {
		t.Fatalf("EnsureDockerVolume (second): %v", err)
	}
	if fake.creates != 1 {
		t.Fatalf("creates = %d, want 1 (idempotent)", fake.creates)
	}
}

func TestDockerDriverEnsureRequiresVolumeID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := newFakeDocker()
	d := NewDockerDriver(fake)
	if _, err := d.EnsureDockerVolume(ctx, "", "", "", "", "", 0); err == nil {
		t.Fatal("expected error for empty volume id")
	}
}

func TestDockerDriverEnsureRejectsUnmanagedNameCollision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := newFakeDocker()
	fake.volumes[DockerVolumeName("vol_collision")] = DockerInfo{
		Name: DockerVolumeName("vol_collision"),
		Labels: map[string]string{
			LabelManagedBy: "operator",
			LabelVolumeID:  "vol_collision",
		},
	}
	d := NewDockerDriver(fake)
	_, err := d.EnsureDockerVolume(ctx, "vol_collision", "svc", "env", "prj", "data", 0)
	if err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("EnsureDockerVolume() error = %v, want ownership conflict", err)
	}
	if fake.creates != 0 {
		t.Fatalf("creates = %d, want 0 for name collision", fake.creates)
	}
}

func TestDockerDriverRemoveIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := newFakeDocker()
	d := NewDockerDriver(fake)
	if _, err := d.EnsureDockerVolume(ctx, "vol_x", "svc", "env", "prj", "data", 0); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := d.Remove(ctx, "vol_x", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Second Remove should NOT error because the volume is gone.
	if err := d.Remove(ctx, "vol_x", true); err != nil {
		t.Fatalf("Remove (second): %v", err)
	}
}

func TestDockerDriverPropagatesUnexpectedInspectError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fake := &fakeDockerInspectFailer{}
	d := NewDockerDriver(fake)
	_, err := d.EnsureDockerVolume(ctx, "vol_z", "s", "e", "p", "d", 0)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("EnsureDockerVolume err = %v, want propagated inspect error", err)
	}
}

type fakeDockerInspectFailer struct{}

func (f *fakeDockerInspectFailer) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (DockerInfo, error) {
	return DockerInfo{}, errors.New("should not be called")
}
func (f *fakeDockerInspectFailer) VolumeInspect(ctx context.Context, name string) (DockerInfo, error) {
	return DockerInfo{}, errors.New("inspect: boom")
}
func (f *fakeDockerInspectFailer) VolumeRemove(ctx context.Context, name string, force bool) error {
	return errors.New("should not be called")
}

func TestDockerVolumeNameIsStableAcrossRedeploys(t *testing.T) {
	t.Parallel()
	id := "vol_abc123"
	got1 := DockerVolumeName(id)
	got2 := DockerVolumeName(id)
	if got1 != got2 {
		t.Fatalf("DockerVolumeName not stable: %q vs %q", got1, got2)
	}
	if !strings.HasPrefix(got1, DockerVolumeNamePrefix) {
		t.Fatalf("DockerVolumeName missing prefix: %s", got1)
	}
}
