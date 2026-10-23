//go:build integration

// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tc "github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/midnight/midnight/internal/backup"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/migrations"
)

func TestVolumeBackupWorkerQueueRoundTripLive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("midnight"),
		tcpostgres.WithUsername("midnight_test"),
		tcpostgres.WithPassword("midnight_test"),
		tc.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)),
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
	defer pool.Close()
	if _, err := (&dbpg.MigrationRunner{Pool: pool, FS: migrations.FS, ManifestPath: "manifest.yaml"}).Up(ctx); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO projects (id,workspace_id,slug,display_name) VALUES ('prj_worker_backup','wrk_default','worker-backup','Worker Backup');
		INSERT INTO environments (id,project_id,slug,name) VALUES ('env_worker_backup','prj_worker_backup','production','Production');
		INSERT INTO services (id,project_id,slug,display_name) VALUES ('svc_worker_backup','prj_worker_backup','app','App')`,
	); err != nil {
		t.Fatal(err)
	}
	volumeStore := volume.NewPostgresStore(pool)
	source, err := volumeStore.Create(ctx, volume.VolumeCreateInput{
		ProjectID: "prj_worker_backup", EnvironmentID: "env_worker_backup",
		ServiceID: "svc_worker_backup", Name: "data", SizeBytes: 1 << 20, MountPath: "/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := volumeStore.MarkReady(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	sourceName := volume.DockerVolumeName(source.ID)
	dockerCommand(t, "volume", "create", sourceName)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", sourceName).Run() })
	dockerCommand(t,
		"run", "--rm", "--network", "none",
		"--mount", "type=volume,src="+sourceName+",dst=/data",
		backup.DefaultVolumeArchiveHelperImage,
		"sh", "-c", "printf 'midnight-live-backup-proof' > /data/proof.txt",
	)

	keyPath := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(keyPath, []byte("integration archive key material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinationRoot := t.TempDir()
	t.Setenv(backup.EnvBackupPassphraseFile, keyPath)
	t.Setenv(backup.EnvBackupDestinationURI, (&url.URL{Scheme: "file", Path: destinationRoot}).String())
	worker, err := buildVolumeBackupWorker(ctx, workerConfig{RuntimeBackend: "docker"}, pool, volumeStore)
	if err != nil {
		t.Fatalf("buildVolumeBackupWorker: %v", err)
	}
	runStore := backup.NewPostgresVolumeRunStore(pool)
	backupWork, err := runStore.CreateBackupWork(ctx, backup.CreateVolumeBackupRun{VolumeID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := processOneVolumeBackupJob(
		ctx, queue.NewPostgresStore(pool), "worker-live", time.Minute, time.Now().UTC(), worker,
	)
	if err != nil || !processed {
		t.Fatalf("process backup = %t, %v", processed, err)
	}
	backupRun, err := runStore.GetBackup(ctx, backupWork.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if backupRun.State != backup.VolumeBackupSucceeded ||
		len(backupRun.ObjectSHA256) != 64 || len(backupRun.SourceSHA256) != 64 {
		t.Fatalf("backup run = %#v", backupRun)
	}

	restoreWork, err := runStore.CreateRestoreWork(ctx, backup.CreateVolumeRestoreRun{BackupID: backupRun.ID})
	if err != nil {
		t.Fatal(err)
	}
	processed, err = processOneVolumeBackupJob(
		ctx, queue.NewPostgresStore(pool), "worker-live", time.Minute, time.Now().UTC(), worker,
	)
	if err != nil || !processed {
		t.Fatalf("process restore = %t, %v", processed, err)
	}
	restoreRun, err := runStore.GetRestore(ctx, restoreWork.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoreRun.State != backup.VolumeRestoreStaged ||
		!restoreRun.SourceRetained || !restoreRun.AttachmentSwapStaged {
		t.Fatalf("restore run = %#v", restoreRun)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "volume", "rm", "-f", restoreRun.ReplacementVolumeName).Run()
	})
	proof := dockerCommand(t,
		"run", "--rm", "--network", "none", "--read-only",
		"--mount", "type=volume,src="+restoreRun.ReplacementVolumeName+",dst=/data,readonly",
		backup.DefaultVolumeArchiveHelperImage,
		"cat", "/data/proof.txt",
	)
	if strings.TrimSpace(proof) != "midnight-live-backup-proof" {
		t.Fatalf("restored proof = %q", proof)
	}
}

func dockerCommand(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, output)
	}
	return string(output)
}
