//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/backup"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/volume"
	migrations "github.com/midnight/midnight/migrations"
)

func TestPostgresVolumeBackupRestoreRestartAndStagedSwap(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		t.Fatalf("start Postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id,workspace_id,slug,display_name) VALUES ('prj_backup','wrk_default','backup','Backup');
		INSERT INTO environments (id,project_id,slug,name) VALUES ('env_backup','prj_backup','production','Production');
		INSERT INTO services (id,project_id,slug,display_name) VALUES ('svc_backup','prj_backup','database','Database')`,
	); err != nil {
		t.Fatal(err)
	}
	volumeStore := volume.NewPostgresStore(pool)
	source, err := volumeStore.Create(ctx, volume.VolumeCreateInput{
		ProjectID:     "prj_backup",
		EnvironmentID: "env_backup",
		ServiceID:     "svc_backup",
		Name:          "database",
		SizeBytes:     1 << 20,
		MountPath:     "/var/lib/database",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := volumeStore.MarkReady(ctx, source.ID); err != nil {
		t.Fatal(err)
	}

	runStore := backup.NewPostgresVolumeRunStore(pool)
	runStore.IDFactory = func(prefix string) string { return prefix + "_scheduled" }
	policy, err := runStore.CreatePolicy(ctx, backup.CreateVolumeBackupPolicy{
		VolumeID: source.ID, ScheduleKind: "daily", RetentionSeconds: 7 * 24 * 60 * 60,
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	dueAt := time.Now().UTC().Add(-time.Minute)
	if _, err := pool.Exec(ctx,
		`UPDATE volume_backup_policies SET next_run_at=$1 WHERE id=$2`,
		dueAt, policy.ID); err != nil {
		t.Fatal(err)
	}
	scheduled, err := runStore.EnqueueDuePolicies(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("EnqueueDuePolicies: %v", err)
	}
	if len(scheduled) != 1 || scheduled[0].Run.PolicyID != policy.ID ||
		scheduled[0].Run.TriggerKind != "daily" ||
		scheduled[0].Job.Queue != "cleanup" {
		t.Fatalf("scheduled work = %#v", scheduled)
	}
	replayed, err := runStore.EnqueueDuePolicies(ctx, time.Now().UTC(), 10)
	if err != nil || len(replayed) != 0 {
		t.Fatalf("replayed scheduled work = %#v, %v", replayed, err)
	}
	runStore.IDFactory = func(prefix string) string { return prefix + "_work" }
	work, err := runStore.CreateBackupWork(ctx, backup.CreateVolumeBackupRun{VolumeID: source.ID})
	if err != nil {
		t.Fatalf("CreateBackupWork: %v", err)
	}
	if work.Run.State != backup.VolumeBackupPending ||
		work.Job.Queue != "cleanup" ||
		work.Job.PartitionKey != source.ProjectID {
		t.Fatalf("backup work = %#v", work)
	}
	var queuedRunID string
	if err := pool.QueryRow(ctx, `SELECT payload->>'run_id' FROM jobs WHERE id=$1 AND state='pending'`, work.Job.ID).Scan(&queuedRunID); err != nil {
		t.Fatalf("backup job missing: %v", err)
	}
	if queuedRunID != work.Run.ID {
		t.Fatalf("queued run id = %q, want %q", queuedRunID, work.Run.ID)
	}

	runStore.IDFactory = func(prefix string) string { return prefix + "_integration" }
	backupRun, err := runStore.CreateBackup(ctx, backup.CreateVolumeBackupRun{
		VolumeID:  source.ID,
		ObjectKey: "volumes/" + source.ID + "/vbk_integration.mbkp",
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if _, err := runStore.StartBackup(ctx, backupRun.ID); err != nil {
		t.Fatalf("StartBackup: %v", err)
	}
	destination, err := backup.NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	passphrase := []byte("correct horse battery staple")
	archive, err := backup.PutEncryptedArchive(
		ctx,
		destination,
		backupRun.ObjectKey,
		bytes.NewReader(bytes.Repeat([]byte("database-record\n"), 10_000)),
		passphrase,
	)
	if err != nil {
		t.Fatal(err)
	}
	backupRun, err = runStore.CompleteBackup(ctx, backupRun.ID, archive)
	if err != nil {
		t.Fatalf("CompleteBackup: %v", err)
	}
	if backupRun.State != backup.VolumeBackupSucceeded || backupRun.ObjectSHA256 == "" {
		t.Fatalf("backup run = %#v", backupRun)
	}

	runStore.IDFactory = func(prefix string) string { return prefix + "_work_restore" }
	restoreWork, err := runStore.CreateRestoreWork(ctx, backup.CreateVolumeRestoreRun{BackupID: backupRun.ID})
	if err != nil {
		t.Fatalf("CreateRestoreWork: %v", err)
	}
	if restoreWork.Run.State != backup.VolumeRestorePending ||
		restoreWork.Job.Queue != "cleanup" ||
		restoreWork.Job.Priority != 10 {
		t.Fatalf("restore work = %#v", restoreWork)
	}
	runStore.IDFactory = func(prefix string) string { return prefix + "_integration" }
	restoreRun, err := runStore.CreateRestore(ctx, backup.CreateVolumeRestoreRun{
		ID:       "vrs_integration",
		BackupID: backupRun.ID,
	})
	if err != nil {
		t.Fatalf("CreateRestore: %v", err)
	}
	if _, err := runStore.StartRestore(ctx, restoreRun.ID); err != nil {
		t.Fatalf("StartRestore: %v", err)
	}
	replacementID := "vol_restore_integration"
	replacement := backup.ReplacementVolume{
		ID:            replacementID,
		RuntimeName:   volume.DockerVolumeName(replacementID),
		DisplayName:   "database 2026-07-27",
		ProjectID:     source.ProjectID,
		EnvironmentID: source.EnvironmentID,
		ServiceID:     source.ServiceID,
		MountPath:     source.MountPath,
		SizeBytes:     source.SizeBytes,
	}
	if err := runStore.StageReplacement(ctx, restoreRun.ID, volume.DockerVolumeName(source.ID), replacement); err != nil {
		t.Fatalf("StageReplacement: %v", err)
	}
	restoreRun, err = runStore.CompleteRestore(ctx, restoreRun.ID, backup.StreamRestoreResult{
		RestoredBytes:  archive.SourceBytes,
		RestoredSHA256: archive.SourceSHA256,
		Format:         archive.Format,
	})
	if err != nil {
		t.Fatalf("CompleteRestore: %v", err)
	}
	if restoreRun.State != backup.VolumeRestoreStaged || !restoreRun.SourceRetained || !restoreRun.AttachmentSwapStaged {
		t.Fatalf("staged restore = %#v", restoreRun)
	}
	activeBeforeApply, err := volumeStore.List(ctx, volume.ListFilter{
		ProjectID: source.ProjectID, EnvironmentID: source.EnvironmentID, ServiceID: source.ServiceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeBeforeApply) != 1 || activeBeforeApply[0].ID != source.ID {
		t.Fatalf("active volumes before apply = %#v", activeBeforeApply)
	}
	restoreRun, err = runStore.ApplyRestore(ctx, restoreRun.ID)
	if err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
	if restoreRun.State != backup.VolumeRestoreApplied {
		t.Fatalf("applied restore = %#v", restoreRun)
	}
	activeAfterApply, err := volumeStore.List(ctx, volume.ListFilter{
		ProjectID: source.ProjectID, EnvironmentID: source.EnvironmentID, ServiceID: source.ServiceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeAfterApply) != 1 || activeAfterApply[0].ID != replacementID {
		t.Fatalf("active volumes after apply = %#v", activeAfterApply)
	}
	retained, err := volumeStore.Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Status != volume.StatusRestoreRetained {
		t.Fatalf("source status = %q", retained.Status)
	}

	pool.Close()
	restarted, err := dbpg.Open(ctx, dbpg.PoolConfig{DSN: dsn, MaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	reloadedStore := backup.NewPostgresVolumeRunStore(restarted)
	reloaded, err := reloadedStore.GetRestore(ctx, restoreRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State != backup.VolumeRestoreApplied ||
		reloaded.ReplacementVolumeID != replacementID ||
		!reloaded.SourceRetained {
		t.Fatalf("reloaded restore = %#v", reloaded)
	}
}
