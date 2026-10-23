// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	dockerclient "github.com/moby/moby/client"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/internal/volume"
)

// TestSchedulerVolumeIntegrationRoundTrip wires the volume Manager to the
// docker.Adapter and proves three things in one fixture:
//
//  1. EnsureForDeployment materializes a Docker volume named
//     "midnight-<vol_id>".
//  2. Schedule receives that name as a ScheduleMount and folds it into
//     HostConfig.Mounts (the exact container-create field).
//  3. A redeploy targeting the same volume id reuses the same Docker
//     volume name, so data semantically persists.
func TestSchedulerVolumeIntegrationRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	store := volume.NewMemoryStoreWithClock(func() time.Time { return fixed })
	bridge := newSchedulerFakeBridge()
	driver := volume.NewDockerDriver(bridge)
	mgr := volume.NewManager(store, driver)
	mgr.Now = func() time.Time { return fixed }

	vol, err := store.Create(ctx, volume.VolumeCreateInput{
		ProjectID:     "prj_one",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_app",
		Name:          "data",
		SizeBytes:     1 << 30,
		MountPath:     "/var/lib/app",
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	// Step 1: scheduler asks the manager to materialize attachments for
	// the first deploy (v1).
	attached, err := mgr.EnsureForDeployment(ctx, volume.DeploymentAttachments{
		DeploymentID:  "dep_v1",
		InstanceID:    "inst_v1",
		ProjectID:     "prj_one",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		Replicas:      1,
		Sequence:      1,
		Attachments: []volume.AttachmentInput{
			{VolumeID: vol.ID, MountPath: "/var/lib/app", Sequence: 1},
		},
	})
	if err != nil {
		t.Fatalf("EnsureForDeployment v1: %v", err)
	}
	if len(attached) != 1 {
		t.Fatalf("len(attached) = %d, want 1", len(attached))
	}
	wantName := volume.DockerVolumeName(vol.ID)
	if attached[0].DockerVolumeName != wantName {
		t.Fatalf("DockerVolumeName = %s, want %s", attached[0].DockerVolumeName, wantName)
	}

	// Step 2: feed the AttachedVolume slice into Docker Schedule via
	// ScheduleMount.
	adapter, eng := newSchedulerAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_v1",
		InstanceID:     "inst_v1",
		ProjectID:      "prj_one",
		EnvironmentID:  "env_prod",
		ServiceID:      "svc_app",
		Image:          "registry.example.com/app:v1",
		IdempotencyKey: "k1",
		Mounts:         attachedToScheduleMounts(attached),
	}
	if _, err := adapter.Schedule(ctx, req); err != nil {
		t.Fatalf("Schedule v1: %v", err)
	}
	if !eng.hasMount(wantName, "/var/lib/app") {
		t.Fatalf("container created without expected mount; mounts=%+v", eng.snapshotMounts())
	}

	// Mark the docker volume with a sentinel so the redeploy can prove
	// data persisted.
	bridge.tag(wantName, "test.persist", "yes")

	firstAttachments, _ := store.ListAttachments(ctx, vol.ID)
	if err := store.MarkAttachmentDetached(ctx, firstAttachments[0].ID, fixed.Add(time.Minute)); err != nil {
		t.Fatalf("detach v1 attachment: %v", err)
	}

	// Step 3: redeploy (v2) — different deployment id, same volume row.
	attached2, err := mgr.EnsureForDeployment(ctx, volume.DeploymentAttachments{
		DeploymentID:  "dep_v2",
		InstanceID:    "inst_v2",
		ProjectID:     "prj_one",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		Replicas:      1,
		Sequence:      2,
		Attachments: []volume.AttachmentInput{
			{VolumeID: vol.ID, MountPath: "/var/lib/app", Sequence: 2},
		},
	})
	if err != nil {
		t.Fatalf("EnsureForDeployment v2: %v", err)
	}
	if attached2[0].DockerVolumeName != attached[0].DockerVolumeName {
		t.Fatalf("docker volume name diverged across redeploys: %s vs %s", attached2[0].DockerVolumeName, attached[0].DockerVolumeName)
	}
	if !bridge.hasTag(wantName, "test.persist", "yes") {
		t.Fatalf("redeploy did not preserve the sentinel tag — data would have been lost")
	}
	if bridge.creates() != 1 {
		t.Fatalf("docker volume creates = %d, want 1 (idempotent across redeploy)", bridge.creates())
	}

	// Schedule the v2 container; the mount slice should still resolve to
	// the same docker volume name.
	req2 := runtime.ScheduleRequest{
		DeploymentID:   "dep_v2",
		InstanceID:     "inst_v2",
		ProjectID:      "prj_one",
		EnvironmentID:  "env_prod",
		ServiceID:      "svc_app",
		Image:          "registry.example.com/app:v2",
		IdempotencyKey: "k2",
		Mounts:         attachedToScheduleMounts(attached2),
	}
	if _, err := adapter.Schedule(ctx, req2); err != nil {
		t.Fatalf("Schedule v2: %v", err)
	}
	if !eng.hasMount(wantName, "/var/lib/app") {
		t.Fatalf("v2 container created without expected mount; mounts=%+v", eng.snapshotMounts())
	}
}

func attachedToScheduleMounts(in []volume.AttachedVolume) []runtime.ScheduleMount {
	out := make([]runtime.ScheduleMount, 0, len(in))
	for _, a := range in {
		out = append(out, runtime.ScheduleMount{
			VolumeID: a.VolumeID,
			Source:   a.DockerVolumeName,
			Target:   a.MountPath,
			IsVolume: true,
			ReadOnly: a.ReadOnly,
		})
	}
	return out
}

// schedulerFakeBridge is a volume.DockerVolumeClient that also lets the
// test peek at the underlying volume label set (proxy for "data on disk").
type schedulerFakeBridge struct {
	mu           sync.Mutex
	volumes      map[string]volume.DockerInfo
	createsCount int
}

func newSchedulerFakeBridge() *schedulerFakeBridge {
	return &schedulerFakeBridge{volumes: map[string]volume.DockerInfo{}}
}

func (b *schedulerFakeBridge) VolumeCreate(ctx context.Context, name, driver string, labels, opts map[string]string) (volume.DockerInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.createsCount++
	if _, ok := b.volumes[name]; ok {
		return volume.DockerInfo{}, fmt.Errorf("docker: volume %q already exists", name)
	}
	cp := make(map[string]string, len(labels))
	for k, v := range labels {
		cp[k] = v
	}
	info := volume.DockerInfo{Name: name, Driver: driver, Labels: cp}
	b.volumes[name] = info
	return info, nil
}

func (b *schedulerFakeBridge) VolumeInspect(ctx context.Context, name string) (volume.DockerInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.volumes[name]
	if !ok {
		return volume.DockerInfo{}, fmt.Errorf("docker: no such volume %q", name)
	}
	return v, nil
}

func (b *schedulerFakeBridge) VolumeRemove(ctx context.Context, name string, force bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.volumes, name)
	return nil
}

func (b *schedulerFakeBridge) tag(volumeName, key, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.volumes[volumeName]
	if v.Labels == nil {
		v.Labels = map[string]string{}
	}
	v.Labels[key] = value
	b.volumes[volumeName] = v
}

func (b *schedulerFakeBridge) hasTag(volumeName, key, value string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.volumes[volumeName]
	if !ok {
		return false
	}
	return v.Labels[key] == value
}

func (b *schedulerFakeBridge) creates() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.createsCount
}

// schedulerFakeEngine is a docker.EngineClient minimal enough to drive
// adapter.Schedule and let the integration test inspect HostConfig.Mounts.
type schedulerFakeEngine struct {
	mu             sync.Mutex
	containers     map[string]*schedulerContainerState
	containersByID map[string]*schedulerContainerState
	createCalls    int
}

type schedulerContainerState struct {
	id   string
	name string
	cfg  *container.Config
	hc   *container.HostConfig
}

func newSchedulerFakeEngine() *schedulerFakeEngine {
	return &schedulerFakeEngine{
		containers:     map[string]*schedulerContainerState{},
		containersByID: map[string]*schedulerContainerState{},
	}
}

func (f *schedulerFakeEngine) NegotiatedAPIVersion() string { return "1.43" }

func (f *schedulerFakeEngine) Info(ctx context.Context) (system.Info, error) {
	return system.Info{
		ServerVersion:      "24.0.0",
		Driver:             "overlay2",
		LiveRestoreEnabled: true,
		LoggingDriver:      "json-file",
		SecurityOptions:    []string{"name=no-new-privileges", "name=apparmor"},
		CgroupVersion:      "2",
	}, nil
}

func (f *schedulerFakeEngine) Ping(ctx context.Context) error { return nil }

func (f *schedulerFakeEngine) ContainerCreate(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if _, exists := f.containers[name]; exists {
		return "", fmt.Errorf("conflict: container name %q is already in use", name)
	}
	id := fmt.Sprintf("id-%s-%d", name, f.createCalls)
	st := &schedulerContainerState{id: id, name: name, cfg: cfg, hc: hc}
	f.containers[name] = st
	f.containersByID[id] = st
	return id, nil
}

func (f *schedulerFakeEngine) ContainerCreateWithNetworking(ctx context.Context, name string, cfg *container.Config, hc *container.HostConfig, _ *dockernetwork.NetworkingConfig) (string, error) {
	return f.ContainerCreate(ctx, name, cfg, hc)
}
func (f *schedulerFakeEngine) NetworkCreate(ctx context.Context, name string, _ dockerclient.NetworkCreateOptions) (string, error) {
	return "net-" + name, nil
}
func (f *schedulerFakeEngine) NetworkRemove(ctx context.Context, _ string) error { return nil }
func (f *schedulerFakeEngine) NetworkList(ctx context.Context, _ map[string]string) ([]dockernetwork.Summary, error) {
	return nil, nil
}
func (f *schedulerFakeEngine) ContainerStart(ctx context.Context, id string) error { return nil }
func (f *schedulerFakeEngine) ContainerStop(ctx context.Context, id string, grace time.Duration) error {
	return nil
}
func (f *schedulerFakeEngine) ContainerRemove(ctx context.Context, id string, force bool) error {
	return nil
}
func (f *schedulerFakeEngine) ContainerInspect(ctx context.Context, id string) (container.InspectResponse, error) {
	return container.InspectResponse{}, errors.New("not used")
}
func (f *schedulerFakeEngine) ContainerList(ctx context.Context, labelFilter map[string]string) ([]container.Summary, error) {
	return nil, nil
}
func (f *schedulerFakeEngine) ImagePull(ctx context.Context, ref string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("ok")), nil
}

func (f *schedulerFakeEngine) ImageExposedPorts(context.Context, string) ([]int, error) {
	return nil, nil
}

func (f *schedulerFakeEngine) NetworkConnect(context.Context, string, string) error { return nil }
func (f *schedulerFakeEngine) Close() error                                         { return nil }

func (f *schedulerFakeEngine) hasMount(source, target string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.containers {
		if c.hc == nil {
			continue
		}
		for _, m := range c.hc.Mounts {
			if m.Type == mount.TypeVolume && m.Source == source && m.Target == target {
				return true
			}
		}
	}
	return false
}

func (f *schedulerFakeEngine) snapshotMounts() []mount.Mount {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []mount.Mount{}
	for _, c := range f.containers {
		if c.hc == nil {
			continue
		}
		out = append(out, c.hc.Mounts...)
	}
	return out
}

func newSchedulerAdapter(t *testing.T) (*docker.Adapter, *schedulerFakeEngine) {
	t.Helper()
	fake := newSchedulerFakeEngine()
	cfg := docker.Config{
		AgentID:    "agent-test",
		HostID:     "host-test",
		Engine:     fake,
		CGroupMode: docker.CGroupModeV2,
		Now:        func() time.Time { return time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC) },
		StopGrace:  5 * time.Second,
	}
	adapter, _, err := docker.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("docker.New: %v", err)
	}
	return adapter, fake
}
