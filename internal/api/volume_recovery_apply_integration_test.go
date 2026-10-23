//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/backup"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/migrations"
)

func TestPostgresVolumeRestoreApplyAndRedeployIsAtomicAndReplaySafe(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, dsn := startPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	pool := newBootPool(t, ctx, dsn)
	defer pool.Close()
	if _, err := (&pg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id,workspace_id,slug,display_name)
		VALUES ('prj_recovery','wrk_default','recovery','Recovery');
		INSERT INTO environments (id,project_id,slug,name)
		VALUES ('env_recovery','prj_recovery','production','Production');
		INSERT INTO services (id,project_id,slug,display_name)
		VALUES ('svc_recovery','prj_recovery','app','App');
		INSERT INTO deployments (
			id,project_id,service_id,environment_id,action,source_type,
			image_ref,image_digest,state,active,created_at,updated_at
		) VALUES (
			'dep_recovery_active','prj_recovery','svc_recovery','env_recovery',
			'deploy_image','image','registry.example/app',
			'sha256:`+strings.Repeat("a", 64)+`','queued',FALSE,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP
		);
		UPDATE deployments
		   SET state='deploying',updated_at=CURRENT_TIMESTAMP
		 WHERE id='dep_recovery_active' AND state='queued';
		UPDATE deployments
		   SET state='active',active=TRUE,updated_at=CURRENT_TIMESTAMP
		 WHERE id='dep_recovery_active' AND state='deploying';
		INSERT INTO service_environment_state (
			service_id,environment_id,active_deployment_id,last_deployment_seq
		) VALUES ('svc_recovery','env_recovery','dep_recovery_active',1)`,
	); err != nil {
		t.Fatal(err)
	}

	volumeStore := volume.NewPostgresStore(pool)
	source, err := volumeStore.Create(ctx, volume.VolumeCreateInput{
		ProjectID: "prj_recovery", EnvironmentID: "env_recovery",
		ServiceID: "svc_recovery", Name: "data",
		SizeBytes: 1 << 20, MountPath: "/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := volumeStore.MarkReady(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	restoreStore := backup.NewPostgresVolumeRunStore(pool)
	backupRun, err := restoreStore.CreateBackup(ctx, backup.CreateVolumeBackupRun{
		VolumeID: source.ID, ObjectKey: "volumes/" + source.ID + "/atomic.mnbk",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreStore.StartBackup(ctx, backupRun.ID); err != nil {
		t.Fatal(err)
	}
	backupRun, err = restoreStore.CompleteBackup(ctx, backupRun.ID, backup.StreamBackupResult{
		Object:       backup.Object{SHA256: strings.Repeat("b", 64), Bytes: 4096},
		SourceSHA256: strings.Repeat("c", 64),
		SourceBytes:  2048,
		Format:       "midnight-backup-aes256gcm-gzip-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	restoreRun, err := restoreStore.CreateRestore(ctx, backup.CreateVolumeRestoreRun{BackupID: backupRun.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restoreStore.StartRestore(ctx, restoreRun.ID); err != nil {
		t.Fatal(err)
	}
	replacementID := "vol_recovery_replacement"
	if err := restoreStore.StageReplacement(ctx, restoreRun.ID, volume.DockerVolumeName(source.ID), backup.ReplacementVolume{
		ID: replacementID, RuntimeName: volume.DockerVolumeName(replacementID),
		DisplayName: "data restored", ProjectID: source.ProjectID,
		EnvironmentID: source.EnvironmentID, ServiceID: source.ServiceID,
		MountPath: source.MountPath, SizeBytes: source.SizeBytes,
	}); err != nil {
		t.Fatal(err)
	}
	restoreRun, err = restoreStore.CompleteRestore(ctx, restoreRun.ID, backup.StreamRestoreResult{
		RestoredSHA256: strings.Repeat("c", 64), RestoredBytes: 2048,
		Format: "midnight-backup-aes256gcm-gzip-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	actions := NewPostgresDeploymentActionStore(pool)
	coordinator := NewPostgresVolumeRecoveryApplyCoordinator(pool, restoreStore, actions)
	triggerKey := "integration:restore-apply:" + restoreRun.ID

	// Missing immutable config evidence makes the entire transaction roll back:
	// no attachment swap and no partial deployment/outbox.
	if _, err := coordinator.ApplyRestoreAndRedeploy(ctx, restoreRun.ID, triggerKey); !errors.Is(err, ErrDeploymentSnapshotAbsent) {
		t.Fatalf("missing-snapshot error = %v, want ErrDeploymentSnapshotAbsent", err)
	}
	assertRestoreAndVolumes(t, ctx, pool, restoreRun.ID, source.ID, replacementID,
		string(backup.VolumeRestoreStaged), "ready", "restore_staged")
	assertTableCount(t, ctx, pool, "staged_work_outbox", 0)

	if _, err := pool.Exec(ctx, `
		INSERT INTO deployment_config_snapshots (
			deployment_id,project_id,service_id,environment_id,config_version,
			effective_config,variable_versions,snapshot_sha256
		) VALUES (
			'dep_recovery_active','prj_recovery','svc_recovery','env_recovery',1,
			'{}'::jsonb,'[]'::jsonb,$1
		)`, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ApplyRestoreAndRedeploy(ctx, restoreRun.ID, triggerKey)
	if err != nil {
		t.Fatalf("apply and redeploy: %v", err)
	}
	if !result.Created || result.Restore.State != backup.VolumeRestoreApplied ||
		result.DeploymentAction.Deployment.ImageDigest != "sha256:"+strings.Repeat("a", 64) ||
		result.DeploymentAction.OutboxID == "" {
		t.Fatalf("result = %#v", result)
	}
	assertRestoreAndVolumes(t, ctx, pool, restoreRun.ID, source.ID, replacementID,
		string(backup.VolumeRestoreApplied), "restore_retained", "ready")
	assertTableCount(t, ctx, pool, "staged_work_outbox", 1)
	assertTableCount(t, ctx, pool, "deployment_config_snapshots", 2)

	replayed, err := coordinator.ApplyRestoreAndRedeploy(ctx, restoreRun.ID, triggerKey)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Created || replayed.DeploymentAction.Created ||
		replayed.DeploymentAction.OutboxID != result.DeploymentAction.OutboxID ||
		replayed.DeploymentAction.Deployment.ID != result.DeploymentAction.Deployment.ID {
		t.Fatalf("replay = %#v, initial = %#v", replayed, result)
	}
	assertTableCount(t, ctx, pool, "staged_work_outbox", 1)
	assertTableCount(t, ctx, pool, "deployments", 2)
}

func assertRestoreAndVolumes(
	t *testing.T,
	ctx context.Context,
	pool *pg.Pool,
	restoreID, sourceID, replacementID, wantRestore, wantSource, wantReplacement string,
) {
	t.Helper()
	var restoreState, sourceState, replacementState string
	if err := pool.QueryRow(ctx, `SELECT state FROM volume_restore_runs WHERE id=$1`, restoreID).Scan(&restoreState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM volumes WHERE id=$1`, sourceID).Scan(&sourceState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM volumes WHERE id=$1`, replacementID).Scan(&replacementState); err != nil {
		t.Fatal(err)
	}
	if restoreState != wantRestore || sourceState != wantSource || replacementState != wantReplacement {
		t.Fatalf("states restore/source/replacement = %q/%q/%q, want %q/%q/%q",
			restoreState, sourceState, replacementState, wantRestore, wantSource, wantReplacement)
	}
}
