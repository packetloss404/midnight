// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package docker

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

// TestScheduleAppliesVolumeMounts proves the Schedule path folds the
// runtime.ScheduleMount slice into HostConfig.Mounts. The volume Manager
// produces these entries from volume_attachments rows; this test pins the
// container-create field the scheduler depends on.
func TestScheduleAppliesVolumeMounts(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)

	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_mount",
		InstanceID:     "inst_mount",
		ProjectID:      "prj_one",
		EnvironmentID:  "env_prod",
		ServiceID:      "svc_app",
		Image:          "registry.example.com/foo:bar",
		IdempotencyKey: "abc",
		Mounts: []runtime.ScheduleMount{
			{VolumeID: "vol_abc", Source: "midnight-vol_abc", Target: "/data", IsVolume: true},
		},
		Replicas: 1,
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", fake.createCalls)
	}
	// Inspect the recorded HostConfig.Mounts on the fake.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var found bool
	for _, c := range fake.containers {
		if c.hc == nil {
			continue
		}
		for _, m := range c.hc.Mounts {
			if m.Type != mount.TypeVolume {
				continue
			}
			if m.Source == "midnight-vol_abc" && m.Target == "/data" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected HostConfig.Mounts to contain (midnight-vol_abc -> /data)")
	}
}

// TestScheduleWithoutMountsLeavesHostConfigEmpty proves the hardening defaults
// do not gain an empty mounts slice when no mounts are requested, so the
// merge with existing fields stays clean.
func TestScheduleWithoutMountsLeavesHostConfigEmpty(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_x",
		InstanceID:     "inst_x",
		ProjectID:      "prj_one",
		EnvironmentID:  "env_prod",
		ServiceID:      "svc_app",
		Image:          "registry.example.com/foo:bar",
		IdempotencyKey: "abc",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.containers {
		if c.hc == nil {
			continue
		}
		if len(c.hc.Mounts) != 0 {
			t.Fatalf("HostConfig.Mounts = %+v, want empty", c.hc.Mounts)
		}
	}
}

func TestScheduleRejectsVolumeWithMultipleReplicasBeforeContainerCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	adapter, engine := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID: "dep_multi", InstanceID: "inst_multi",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image:          "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey: "multi", Replicas: 2,
		Mounts: []runtime.ScheduleMount{{
			VolumeID: "vol_data", Source: "midnight-vol_data",
			Target: "/data", IsVolume: true,
		}},
	}
	_, err := adapter.Schedule(ctx, req)
	if got := CodeOf(err); got != failurecodes.VolumeReplicasUnsupported {
		t.Fatalf("Schedule() error = %v code=%s, want VOLUME_REPLICAS_UNSUPPORTED", err, got)
	}
	if engine.createCalls != 0 {
		t.Fatalf("container create calls = %d, want 0", engine.createCalls)
	}
}

func TestScheduleRejectsMountSourceNotDerivedFromVolumeID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	adapter, engine := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID: "dep_badmount", InstanceID: "inst_badmount",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image:          "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey: "badmount", Replicas: 1,
		Mounts: []runtime.ScheduleMount{{
			VolumeID: "vol_data", Source: "operator-volume",
			Target: "/data", IsVolume: true,
		}},
	}
	_, err := adapter.Schedule(ctx, req)
	if got := CodeOf(err); got != failurecodes.ContainerSpecInvalid {
		t.Fatalf("Schedule() error = %v code=%s, want CONTAINER_SPEC_INVALID", err, got)
	}
	if engine.createCalls != 0 {
		t.Fatalf("container create calls = %d, want 0", engine.createCalls)
	}
}

func TestObserveVolumeAttachmentsReturnsOnlyRunningManagedMounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	adapter, _ := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID: "dep_observed", InstanceID: "inst_observed",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image:          "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey: "observed", Replicas: 1,
		Mounts: []runtime.ScheduleMount{{
			VolumeID: "vol_data", Source: "midnight-vol_data",
			Target: "/data", IsVolume: true,
		}},
	}
	if _, err := adapter.Schedule(ctx, req); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if got, err := adapter.ObserveVolumeAttachments(ctx); err != nil || len(got) != 0 {
		t.Fatalf("observed before start = %+v err=%v, want none", got, err)
	}
	if err := adapter.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := adapter.ObserveVolumeAttachments(ctx)
	if err != nil {
		t.Fatalf("ObserveVolumeAttachments: %v", err)
	}
	if len(got) != 1 || got[0].VolumeID != "vol_data" ||
		got[0].DeploymentID != req.DeploymentID || got[0].InstanceID != req.InstanceID ||
		got[0].ProjectID != "prj" || got[0].EnvironmentID != "env" ||
		got[0].ServiceID != "svc" || got[0].MountPath != "/data" {
		t.Fatalf("observed = %+v, want running durable mount", got)
	}
	if err := adapter.Stop(ctx, req.DeploymentID, req.InstanceID, time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got, err := adapter.ObserveVolumeAttachments(ctx); err != nil || len(got) != 0 {
		t.Fatalf("observed after stop = %+v err=%v, want none", got, err)
	}
}

func TestIdempotentScheduleRejectsChangedVolumeIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	adapter, engine := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID: "dep_idem", InstanceID: "inst_idem",
		ProjectID: "prj", EnvironmentID: "env", ServiceID: "svc",
		Image:          "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey: "idem", Replicas: 1,
	}
	if _, err := adapter.Schedule(ctx, req); err != nil {
		t.Fatalf("first Schedule: %v", err)
	}
	req.Mounts = []runtime.ScheduleMount{{
		VolumeID: "vol_data", Source: "midnight-vol_data",
		Target: "/data", IsVolume: true,
	}}
	_, err := adapter.Schedule(ctx, req)
	if got := CodeOf(err); got != failurecodes.ContainerSpecInvalid {
		t.Fatalf("retry error = %v code=%s, want CONTAINER_SPEC_INVALID", err, got)
	}
	if engine.createCalls != 1 {
		t.Fatalf("container create calls = %d, want 1", engine.createCalls)
	}
}
