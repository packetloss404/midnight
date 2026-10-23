// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/deploy"
)

func TestMemoryDeploymentStoreCanonicalBuildLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)
	store := NewMemoryDeploymentStore(func() time.Time { return now })
	store.mintID = func() string { return "dep_canonical" }

	record, err := store.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     "prj_1",
		ServiceID:     "svc_1",
		EnvironmentID: "env_1",
		Action:        deploy.ActionDeploySource,
		OperationID:   "op_1",
		TriggerKey:    "trigger_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != "dep_canonical" || record.State != deploy.DeploymentQueued {
		t.Fatalf("created = %#v", record)
	}
	if err := store.MarkDeploymentBuilding(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordDeploymentBuild(
		context.Background(),
		record.ID,
		"0123456789abcdef",
		"railpack",
		"registry.example/app:rev",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentDeploying(context.Background(), record.ID, deploy.DeploymentBuilding); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(context.Background(), record.ID, ""); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetDeploymentByOperation(context.Background(), "op_1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active ||
		got.State != deploy.DeploymentActive ||
		got.SHA != "0123456789abcdef" ||
		got.Builder != "railpack" ||
		got.ImageDigest != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("deployment = %#v, want persisted build and active truth", got)
	}
}

func TestMemoryDeploymentStoreRejectsDuplicateTriggerAndActivePointerDrift(t *testing.T) {
	store := NewMemoryDeploymentStore(nil)
	first, err := store.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     "prj_1",
		ServiceID:     "svc_1",
		EnvironmentID: "env_1",
		Action:        deploy.ActionDeployImage,
		TriggerKey:    "trigger_1",
		ImageDigest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     "prj_1",
		ServiceID:     "svc_1",
		EnvironmentID: "env_1",
		Action:        deploy.ActionDeployImage,
		TriggerKey:    "trigger_1",
	}); err == nil {
		t.Fatal("duplicate trigger key accepted")
	}
	if err := store.MarkDeploymentDeploying(context.Background(), first.ID, deploy.DeploymentQueued); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(context.Background(), first.ID, ""); err != nil {
		t.Fatal(err)
	}

	second, err := store.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID:     "prj_1",
		ServiceID:     "svc_1",
		EnvironmentID: "env_1",
		Action:        deploy.ActionDeployImage,
		TriggerKey:    "trigger_2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentDeploying(context.Background(), second.ID, deploy.DeploymentQueued); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(context.Background(), second.ID, "dep_wrong"); !errors.Is(err, ErrPromotionConflict) {
		t.Fatalf("PromoteDeployment() error = %v, want promotion conflict", err)
	}
}

func TestMemoryDeploymentRollbackCarriesSourceConfigurationRevision(t *testing.T) {
	store := NewMemoryDeploymentStore(nil)
	source, err := store.CreateDeployment(context.Background(), DeploymentRecord{
		ProjectID: "prj_1", ServiceID: "svc_1", EnvironmentID: "env_1",
		Action: deploy.ActionDeployImage, ImageRef: "registry.example/app@sha256:" + strings.Repeat("a", 64),
		ImageDigest: "sha256:" + strings.Repeat("a", 64), ConfigurationRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := store.CreateRollbackDeployment(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rollback.ConfigurationRevision != 7 || rollback.RollbackSourceID != source.ID {
		t.Fatalf("rollback=%#v", rollback)
	}
}
