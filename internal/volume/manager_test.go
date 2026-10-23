// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
)

func newManagerHarness(t *testing.T) (*Manager, *MemoryStore, *fakeDockerVolumeClient) {
	t.Helper()
	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStoreWithClock(func() time.Time { return fixed })
	fake := newFakeDocker()
	driver := NewDockerDriver(fake)
	mgr := NewManager(store, driver)
	mgr.Now = func() time.Time { return fixed }
	return mgr, store, fake
}

func TestManagerEnsureForDeploymentMaterializesDockerVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, fake := newManagerHarness(t)

	vol, err := store.Create(ctx, VolumeCreateInput{
		ProjectID:     "prj_one",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_app",
		Name:          "data",
		SizeBytes:     1024,
		MountPath:     "/data",
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	out, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID:  "dep_1",
		InstanceID:    "inst_1",
		ProjectID:     "prj_one",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		Replicas:      1,
		Sequence:      1,
		Attachments: []AttachmentInput{
			{VolumeID: vol.ID, DeploymentID: "dep_1", InstanceID: "inst_1", MountPath: "/data", Sequence: 1},
		},
	})
	if err != nil {
		t.Fatalf("EnsureForDeployment: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].DockerVolumeName != "midnight-"+vol.ID {
		t.Fatalf("DockerVolumeName = %s, want midnight-%s", out[0].DockerVolumeName, vol.ID)
	}
	if out[0].MountPath != "/data" {
		t.Fatalf("MountPath = %s, want /data", out[0].MountPath)
	}
	if fake.creates != 1 {
		t.Fatalf("docker creates = %d, want 1", fake.creates)
	}
}

// TestManagerRedeployReusesSameDockerVolume is the canonical redeploy
// persistence test the brief calls for: a second deploy targeting the
// same Volume row resolves to the SAME Docker volume name so data persists
// across container replaces. This is the test that proves the "vol_* id
// → docker name" stability invariant.
func TestManagerRedeployReusesSameDockerVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, fake := newManagerHarness(t)

	vol, _ := store.Create(ctx, VolumeCreateInput{
		ProjectID:     "prj_one",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_app",
		Name:          "data",
		SizeBytes:     1024,
		MountPath:     "/data",
	})

	// First deploy.
	out1, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID:  "dep_v1",
		InstanceID:    "inst_v1",
		ProjectID:     "prj_one",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		Replicas:      1,
		Sequence:      1,
		Attachments: []AttachmentInput{
			{VolumeID: vol.ID, MountPath: "/data", Sequence: 1},
		},
	})
	if err != nil {
		t.Fatalf("EnsureForDeployment v1: %v", err)
	}

	// Simulate the container running, writing data — we model that by
	// stashing a sentinel label on the fake's volume.
	fake.mu.Lock()
	info := fake.volumes[out1[0].DockerVolumeName]
	info.Labels["test.data_written"] = "hello"
	fake.volumes[out1[0].DockerVolumeName] = info
	fake.mu.Unlock()

	// Stateful redeploy is serial: the old container must be stopped/removed
	// before its single-writer lease is released.
	firstAttachments, _ := store.ListAttachments(ctx, vol.ID)
	if err := store.MarkAttachmentDetached(ctx, firstAttachments[0].ID, time.Now().UTC()); err != nil {
		t.Fatalf("detach v1: %v", err)
	}

	// Second deploy (redeploy) — different deployment id, same volume id.
	out2, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID:  "dep_v2",
		InstanceID:    "inst_v2",
		ProjectID:     "prj_one",
		ServiceID:     "svc_app",
		EnvironmentID: "env_prod",
		Replicas:      1,
		Sequence:      2,
		Attachments: []AttachmentInput{
			{VolumeID: vol.ID, MountPath: "/data", Sequence: 2},
		},
	})
	if err != nil {
		t.Fatalf("EnsureForDeployment v2: %v", err)
	}

	if out1[0].DockerVolumeName != out2[0].DockerVolumeName {
		t.Fatalf("docker volume name diverged across redeploys: %s vs %s", out1[0].DockerVolumeName, out2[0].DockerVolumeName)
	}
	// Data semantically persists: the docker volume still has the
	// sentinel label we set during v1, proving the redeploy attached
	// the SAME volume rather than creating a fresh one.
	fake.mu.Lock()
	got := fake.volumes[out2[0].DockerVolumeName].Labels["test.data_written"]
	fake.mu.Unlock()
	if got != "hello" {
		t.Fatalf("redeploy reused a different volume: lost label was %q, want hello", got)
	}
	// And the Docker driver should not have been asked to CREATE a
	// second time — only inspected.
	if fake.creates != 1 {
		t.Fatalf("docker creates after redeploy = %d, want 1 (idempotent)", fake.creates)
	}

	// Both deploys recorded distinct attachment rows pointing at the
	// same volume_id.
	attaches, _ := store.ListAttachments(ctx, vol.ID)
	if len(attaches) != 2 {
		t.Fatalf("len(attachments) = %d, want 2 (v1 + v2)", len(attaches))
	}
}

func TestManagerEnsureRejectsReplicasWithVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, _ := newManagerHarness(t)
	vol, _ := store.Create(ctx, VolumeCreateInput{
		ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d", MountPath: "/data",
	})

	_, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1",
		ProjectID: "p", ServiceID: "s", EnvironmentID: "e",
		Replicas: 3,
		Attachments: []AttachmentInput{
			{VolumeID: vol.ID, MountPath: "/data"},
		},
	})
	var typed *ReplicasUnsupportedError
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want ReplicasUnsupportedError", err)
	}
	if typed.Code() != ReplicasUnsupportedReason {
		t.Fatalf("Code = %s, want %s", typed.Code(), ReplicasUnsupportedReason)
	}
}

func TestManagerEnsureRejectsSoftDeletedVolume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, _ := newManagerHarness(t)
	vol, _ := store.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d", MountPath: "/data"})
	if err := store.Delete(ctx, vol.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1",
		ProjectID: "p", ServiceID: "s", EnvironmentID: "e",
		Replicas:    1,
		Attachments: []AttachmentInput{{VolumeID: vol.ID, MountPath: "/data"}},
	})
	if err == nil {
		t.Fatal("expected error mounting soft-deleted volume")
	}
}

func TestManagerEnsureWithoutDriverFailsBeforeRecordingAttachment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	mgr := NewManager(store, nil) // no driver — dev/unit-test path
	vol, _ := store.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d", MountPath: "/data"})
	out, err := mgr.EnsureForDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1",
		ProjectID: "p", ServiceID: "s", EnvironmentID: "e",
		Replicas:    1,
		Attachments: []AttachmentInput{{VolumeID: vol.ID, MountPath: "/data"}},
	})
	if err == nil {
		t.Fatal("EnsureForDeployment() error = nil, want missing Docker driver failure")
	}
	if len(out) != 0 {
		t.Fatalf("out = %+v, want no synthesized attachment", out)
	}
	atts, _ := store.ListAttachments(ctx, vol.ID)
	if len(atts) != 0 {
		t.Fatalf("attachments = %d, want 0", len(atts))
	}
}

func TestManagerPrepareReservesPendingThenActivates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, fake := newManagerHarness(t)
	vol, err := store.Create(ctx, VolumeCreateInput{
		ProjectID: "p", EnvironmentID: "e", ServiceID: "s",
		Name: "data", MountPath: "/var/lib/data",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	prepared, err := mgr.PrepareServiceDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1", ProjectID: "p",
		ServiceID: "s", EnvironmentID: "e", Replicas: 1,
	})
	if err != nil {
		t.Fatalf("PrepareServiceDeployment: %v", err)
	}
	if fake.creates != 1 || len(prepared.Mounts) != 1 ||
		prepared.Mounts[0].VolumeID != vol.ID || prepared.Mounts[0].MountPath != "/var/lib/data" {
		t.Fatalf("prepared = %+v creates=%d", prepared, fake.creates)
	}
	attachments, _ := store.ListAttachments(ctx, vol.ID)
	if len(attachments) != 1 || attachments[0].Status != AttachmentPending {
		t.Fatalf("attachments before scheduler = %+v, want one pending", attachments)
	}
	if got, _ := store.Get(ctx, vol.ID); got.Status != StatusReady {
		t.Fatalf("volume before scheduler = %s, want ready (not synthetic attached)", got.Status)
	}
	if err := mgr.ConfirmForSchedule(ctx, prepared); err != nil {
		t.Fatalf("ConfirmForSchedule: %v", err)
	}
	if err := mgr.ActivatePrepared(ctx, prepared); err != nil {
		t.Fatalf("ActivatePrepared: %v", err)
	}
	attachments, _ = store.ListAttachments(ctx, vol.ID)
	if attachments[0].Status != AttachmentActive {
		t.Fatalf("attachment after scheduler = %s, want active", attachments[0].Status)
	}
}

func TestManagerScopeMismatchHasNoSideEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, fake := newManagerHarness(t)
	vol, _ := store.Create(ctx, VolumeCreateInput{
		ProjectID: "p1", EnvironmentID: "e", ServiceID: "s",
		Name: "data", MountPath: "/data",
	})
	_, err := mgr.PrepareForDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1", ProjectID: "p2",
		ServiceID: "s", EnvironmentID: "e", Replicas: 1,
		Attachments: []AttachmentInput{{VolumeID: vol.ID, MountPath: "/data"}},
	})
	if err == nil {
		t.Fatal("PrepareForDeployment() error = nil, want scope failure")
	}
	if fake.creates != 0 {
		t.Fatalf("Docker creates = %d, want 0 before scope validation", fake.creates)
	}
	attachments, _ := store.ListAttachments(ctx, vol.ID)
	if len(attachments) != 0 {
		t.Fatalf("attachments = %+v, want none", attachments)
	}
}

func TestManagerSingleWriterRejectsSecondDeploymentBeforeDocker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, fake := newManagerHarness(t)
	vol, _ := store.Create(ctx, VolumeCreateInput{
		ProjectID: "p", EnvironmentID: "e", ServiceID: "s",
		Name: "data", MountPath: "/data",
	})
	first, err := mgr.PrepareServiceDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_1", InstanceID: "inst_1", ProjectID: "p",
		ServiceID: "s", EnvironmentID: "e", Replicas: 1,
	})
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if err := mgr.ActivatePrepared(ctx, first); err != nil {
		t.Fatalf("activate first: %v", err)
	}
	_, err = mgr.PrepareServiceDeployment(ctx, DeploymentAttachments{
		DeploymentID: "dep_2", InstanceID: "inst_2", ProjectID: "p",
		ServiceID: "s", EnvironmentID: "e", Replicas: 1,
	})
	var lifecycle *LifecycleError
	if !errors.As(err, &lifecycle) || lifecycle.FailureCode != failurecodes.VolumeAttached {
		t.Fatalf("second prepare error = %v, want VOLUME_ATTACHED", err)
	}
	if fake.creates != 1 {
		t.Fatalf("Docker creates = %d, want no side effect from rejected writer", fake.creates)
	}
	attachments, _ := store.ListAttachments(ctx, vol.ID)
	if len(attachments) != 1 || attachments[0].DeploymentID != "dep_1" || attachments[0].Status != AttachmentActive {
		t.Fatalf("attachments = %+v, want first writer unchanged", attachments)
	}
}

func TestManagerReconcileObservedRepairsRestartTruth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mgr, store, _ := newManagerHarness(t)
	vol, _ := store.Create(ctx, VolumeCreateInput{
		ProjectID: "p", EnvironmentID: "e", ServiceID: "s",
		Name: "data", MountPath: "/data",
	})
	stale, err := store.RecordAttachment(ctx, Attachment{
		VolumeID: vol.ID, DeploymentID: "dep_stale", InstanceID: "inst_stale",
		ProjectID: "p", ServiceID: "s", EnvironmentID: "e",
		MountPath: "/data", Status: AttachmentPending,
	})
	if err != nil {
		t.Fatalf("record stale: %v", err)
	}
	if err := mgr.ReconcileObserved(ctx, []ObservedAttachment{{
		VolumeID: vol.ID, DeploymentID: "dep_live", InstanceID: "inst_live",
		ProjectID: "p", ServiceID: "s", EnvironmentID: "e",
		Source: DockerVolumeName(vol.ID), MountPath: "/data",
	}}); err != nil {
		t.Fatalf("ReconcileObserved: %v", err)
	}
	attachments, _ := store.ListAttachments(ctx, vol.ID)
	if len(attachments) != 2 {
		t.Fatalf("attachments = %+v, want stale and recovered", attachments)
	}
	statuses := map[string]AttachmentStatus{}
	for _, attachment := range attachments {
		statuses[attachment.ID] = attachment.Status
	}
	if statuses[stale.ID] != AttachmentDetached {
		t.Fatalf("stale status = %s, want detached", statuses[stale.ID])
	}
	foundLive := false
	for _, attachment := range attachments {
		if attachment.DeploymentID == "dep_live" && attachment.Status == AttachmentActive {
			foundLive = true
		}
	}
	if !foundLive {
		t.Fatalf("attachments = %+v, want observed live active", attachments)
	}
}
