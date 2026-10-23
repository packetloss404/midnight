// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package volume

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *MemoryStore {
	t.Helper()
	fixed := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	return NewMemoryStoreWithClock(func() time.Time { return fixed })
}

func TestMemoryStoreCreateAndGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	in := VolumeCreateInput{
		ProjectID:     "prj_one",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_app",
		Name:          "data",
		SizeBytes:     1 << 30,
	}
	v, err := s.Create(ctx, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.ID == "" {
		t.Fatal("expected volume id allocation")
	}
	if v.Status != StatusProvisioning {
		t.Fatalf("Status = %s, want provisioning", v.Status)
	}
	if v.SizeBytes != in.SizeBytes {
		t.Fatalf("SizeBytes = %d, want %d", v.SizeBytes, in.SizeBytes)
	}

	got, err := s.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "data" {
		t.Fatalf("Name = %s, want data", got.Name)
	}
}

func TestMemoryStoreCreateRejectsDuplicateName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	in := VolumeCreateInput{
		ProjectID:     "prj_one",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_app",
		Name:          "data",
		SizeBytes:     1024,
	}
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatalf("Create1: %v", err)
	}
	if _, err := s.Create(ctx, in); !errors.Is(err, ErrVolumeConflict) {
		t.Fatalf("Create2 err = %v, want ErrVolumeConflict", err)
	}
}

func TestMemoryStoreCreateValidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	bad := []VolumeCreateInput{
		{ProjectID: "prj_one", EnvironmentID: "env_prod", Name: "data"},
		{ServiceID: "svc_app", EnvironmentID: "env_prod", Name: "data", ProjectID: "prj_one", SizeBytes: -1},
		{ServiceID: "svc_app", EnvironmentID: "env_prod", ProjectID: "prj_one"},
		{ServiceID: "svc_app", EnvironmentID: "env_prod", ProjectID: "prj_one", Name: "root", MountPath: "/"},
		{ServiceID: "svc_app", EnvironmentID: "env_prod", ProjectID: "prj_one", Name: "traversal", MountPath: "/data/../etc"},
	}
	for i, in := range bad {
		if _, err := s.Create(ctx, in); !errors.Is(err, ErrVolumeInvalid) {
			t.Fatalf("Create[%d] err = %v, want ErrVolumeInvalid", i, err)
		}
	}
}

func TestMemoryStoreListByServiceAndProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	for _, in := range []VolumeCreateInput{
		{ProjectID: "prj_a", EnvironmentID: "env_prod", ServiceID: "svc_one", Name: "data"},
		{ProjectID: "prj_a", EnvironmentID: "env_prod", ServiceID: "svc_two", Name: "data"},
		{ProjectID: "prj_b", EnvironmentID: "env_stage", ServiceID: "svc_three", Name: "data"},
	} {
		if _, err := s.Create(ctx, in); err != nil {
			t.Fatalf("Create %+v: %v", in, err)
		}
	}

	out, err := s.ListByService(ctx, "svc_one")
	if err != nil || len(out) != 1 || out[0].ServiceID != "svc_one" {
		t.Fatalf("ListByService = %+v, err=%v", out, err)
	}
	out, err = s.ListByProject(ctx, "prj_a")
	if err != nil || len(out) != 2 {
		t.Fatalf("ListByProject prj_a = %+v, err=%v", out, err)
	}
}

func TestMemoryStoreResizeUpOnlyDownRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v, _ := s.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d", SizeBytes: 100})

	up, err := s.Resize(ctx, v.ID, 200)
	if err != nil {
		t.Fatalf("Resize UP: %v", err)
	}
	if up.SizeBytes != 200 {
		t.Fatalf("Resize UP size = %d, want 200", up.SizeBytes)
	}

	if _, err := s.Resize(ctx, v.ID, 150); !errors.Is(err, ErrVolumeResizeDownUnsupported) {
		t.Fatalf("Resize DOWN err = %v, want ErrVolumeResizeDownUnsupported", err)
	}

	// resize unknown id
	if _, err := s.Resize(ctx, "vol_missing", 1024); !errors.Is(err, ErrVolumeNotFound) {
		t.Fatalf("Resize missing err = %v, want ErrVolumeNotFound", err)
	}
}

func TestMemoryStoreDeleteRejectsAttached(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v, _ := s.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d", SizeBytes: 100})
	if _, err := s.RecordAttachment(ctx, Attachment{
		VolumeID:      v.ID,
		DeploymentID:  "dep_1",
		InstanceID:    "inst_1",
		ServiceID:     "s",
		EnvironmentID: "e",
		MountPath:     "/data",
		Status:        AttachmentActive,
	}); err != nil {
		t.Fatalf("RecordAttachment: %v", err)
	}
	if err := s.Delete(ctx, v.ID); !errors.Is(err, ErrVolumeAttached) {
		t.Fatalf("Delete err = %v, want ErrVolumeAttached", err)
	}

	// Detach, then delete should succeed.
	atts, _ := s.ListAttachments(ctx, v.ID)
	if len(atts) != 1 {
		t.Fatalf("ListAttachments = %d, want 1", len(atts))
	}
	if err := s.MarkAttachmentDetached(ctx, atts[0].ID, time.Now().UTC()); err != nil {
		t.Fatalf("MarkAttachmentDetached: %v", err)
	}
	if err := s.Delete(ctx, v.ID); err != nil {
		t.Fatalf("Delete after detach: %v", err)
	}
	got, _ := s.Get(ctx, v.ID)
	if got.Status != StatusSoftDeleted {
		t.Fatalf("Status after delete = %s, want soft_deleted", got.Status)
	}
}

func TestMemoryStoreRecordAttachmentDeduplicates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v, _ := s.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d"})

	a1, err := s.RecordAttachment(ctx, Attachment{VolumeID: v.ID, DeploymentID: "dep_1", InstanceID: "inst_1", MountPath: "/data", Sequence: 1, ServiceID: "s", EnvironmentID: "e"})
	if err != nil {
		t.Fatalf("RecordAttachment1: %v", err)
	}
	a2, err := s.RecordAttachment(ctx, Attachment{VolumeID: v.ID, DeploymentID: "dep_1", InstanceID: "inst_1", MountPath: "/data2", Sequence: 1, ServiceID: "s", EnvironmentID: "e"})
	if err != nil {
		t.Fatalf("RecordAttachment2: %v", err)
	}
	if a1.ID != a2.ID {
		t.Fatalf("expected idempotent attachment, got %s vs %s", a1.ID, a2.ID)
	}
	all, _ := s.ListAttachments(ctx, v.ID)
	if len(all) != 1 {
		t.Fatalf("ListAttachments = %d, want 1 (idempotent)", len(all))
	}
	if all[0].MountPath != "/data2" {
		t.Fatalf("MountPath = %s, want /data2 (last write wins)", all[0].MountPath)
	}
}

func TestMemoryStoreListFiltersSoftDeleted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStore(t)

	v, _ := s.Create(ctx, VolumeCreateInput{ProjectID: "p", EnvironmentID: "e", ServiceID: "s", Name: "d"})
	if err := s.Delete(ctx, v.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	visible, _ := s.List(ctx, ListFilter{})
	if len(visible) != 0 {
		t.Fatalf("List (default) = %d, want 0 (soft-deleted hidden)", len(visible))
	}
	all, _ := s.List(ctx, ListFilter{IncludeSoft: true})
	if len(all) != 1 {
		t.Fatalf("List (IncludeSoft) = %d, want 1", len(all))
	}
}
