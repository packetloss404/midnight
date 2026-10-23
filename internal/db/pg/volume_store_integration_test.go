//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/volume"
)

func TestPostgresVolumeMountAndSingleWriterLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx := context.Background()
	_, dsn := startPostgres(t, ctx)
	pool := newPool(t, ctx, dsn)
	runMigrations(t, ctx, pool)
	project, service, environment := seedDeploymentScope(t, ctx, pool)

	store := volume.NewPostgresStore(pool)
	created, err := store.Create(ctx, volume.VolumeCreateInput{
		ProjectID: project, ServiceID: service, EnvironmentID: environment,
		Name: "data", SizeBytes: 1024, MountPath: "/var/lib/data",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	reloaded, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.MountPath != "/var/lib/data" {
		t.Fatalf("MountPath = %q, want durable /var/lib/data", reloaded.MountPath)
	}

	first, err := store.RecordAttachment(ctx, volume.Attachment{
		VolumeID: created.ID, DeploymentID: "dep_volume_one", InstanceID: "inst_one",
		ProjectID: project, ServiceID: service, EnvironmentID: environment,
		MountPath: "/var/lib/data", Status: volume.AttachmentPending,
	})
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	_, err = store.RecordAttachment(ctx, volume.Attachment{
		VolumeID: created.ID, DeploymentID: "dep_volume_two", InstanceID: "inst_two",
		ProjectID: project, ServiceID: service, EnvironmentID: environment,
		MountPath: "/var/lib/data", Status: volume.AttachmentPending,
	})
	if !errors.Is(err, volume.ErrVolumeAttached) {
		t.Fatalf("reserve second error = %v, want ErrVolumeAttached", err)
	}
	if err := store.ActivateAttachment(ctx, first.ID); err != nil {
		t.Fatalf("activate first: %v", err)
	}
	if err := store.MarkAttachmentDetached(ctx, first.ID, store.Now()); err != nil {
		t.Fatalf("detach first: %v", err)
	}
	recovered, err := store.RecordAttachment(ctx, volume.Attachment{
		VolumeID: created.ID, DeploymentID: "dep_volume_one", InstanceID: "inst_one",
		ProjectID: project, ServiceID: service, EnvironmentID: environment,
		MountPath: "/var/lib/data", Status: volume.AttachmentPending,
	})
	if err != nil {
		t.Fatalf("recover first attachment: %v", err)
	}
	if recovered.ID != first.ID {
		t.Fatalf("recovered attachment id = %s, want stable %s", recovered.ID, first.ID)
	}
	if err := store.ActivateAttachment(ctx, recovered.ID); err != nil {
		t.Fatalf("activate recovered first: %v", err)
	}
	if err := store.MarkAttachmentDetached(ctx, recovered.ID, store.Now()); err != nil {
		t.Fatalf("detach recovered first: %v", err)
	}
	second, err := store.RecordAttachment(ctx, volume.Attachment{
		VolumeID: created.ID, DeploymentID: "dep_volume_two", InstanceID: "inst_two",
		ProjectID: project, ServiceID: service, EnvironmentID: environment,
		MountPath: "/var/lib/data", Status: volume.AttachmentPending,
	})
	if err != nil {
		t.Fatalf("reserve second after detach: %v", err)
	}
	if err := store.ActivateAttachment(ctx, second.ID); err != nil {
		t.Fatalf("activate second: %v", err)
	}
	attachments, err := store.ListAttachments(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListAttachments: %v", err)
	}
	statuses := map[string]volume.AttachmentStatus{}
	for _, attachment := range attachments {
		statuses[attachment.DeploymentID] = attachment.Status
	}
	if len(attachments) != 2 ||
		statuses["dep_volume_one"] != volume.AttachmentDetached ||
		statuses["dep_volume_two"] != volume.AttachmentActive {
		t.Fatalf("attachments = %+v, want first detached and second active", attachments)
	}
}
