// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/backup"
	dbpg "github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/volume"
)

type volumeBackupWorker struct {
	executor backup.VolumeJobExecutor
	policies *backup.PostgresVolumeRunStore
}

// buildVolumeBackupWorker fails closed on partially configured recovery. An
// entirely absent configuration leaves the cleanup queue untouched and keeps
// stateless/local development workers working as before.
func buildVolumeBackupWorker(ctx context.Context, cfg workerConfig, pool *dbpg.Pool, volumes volume.Store) (*volumeBackupWorker, error) {
	destinationURI := strings.TrimSpace(os.Getenv(backup.EnvBackupDestinationURI))
	passphrasePath := strings.TrimSpace(os.Getenv(backup.EnvBackupPassphraseFile))
	if destinationURI == "" && passphrasePath == "" {
		return nil, nil
	}
	if destinationURI == "" || passphrasePath == "" {
		return nil, fmt.Errorf(
			"volume backup worker requires both %s and %s",
			backup.EnvBackupDestinationURI,
			backup.EnvBackupPassphraseFile,
		)
	}
	if cfg.RuntimeBackend != "docker" {
		return nil, errors.New("volume backup worker requires the docker runtime backend")
	}
	readinessKey, err := (backup.FilePassphraseProvider{Path: passphrasePath}).Passphrase(ctx)
	if err != nil {
		return nil, fmt.Errorf("volume backup key readiness: %w", err)
	}
	clear(readinessKey)
	destination, err := backup.OpenDestination(ctx, destinationURI)
	if err != nil {
		return nil, fmt.Errorf("volume backup destination readiness: %w", err)
	}
	runs := backup.NewPostgresVolumeRunStore(pool)
	return &volumeBackupWorker{executor: backup.VolumeJobExecutor{
		Runs:        runs,
		Volumes:     volumes,
		Destination: destination,
		Driver: backup.DockerCLIVolumeArchiveDriver{
			Host: cfg.DockerHost,
		},
		Passphrases: backup.FilePassphraseProvider{Path: passphrasePath},
	}, policies: runs}, nil
}

func (worker *volumeBackupWorker) enqueueDuePolicies(ctx context.Context, now time.Time) (int, error) {
	if worker == nil || worker.policies == nil {
		return 0, nil
	}
	works, err := worker.policies.EnqueueDuePolicies(ctx, now.UTC(), 25)
	return len(works), err
}

func processOneVolumeBackupJob(
	ctx context.Context,
	store queue.Store,
	workerID string,
	leaseTTL time.Duration,
	now time.Time,
	worker *volumeBackupWorker,
) (bool, error) {
	if worker == nil {
		return false, nil
	}
	job, err := store.Lease(ctx, queue.LeaseRequest{
		Queue:     queue.Cleanup,
		WorkerID:  workerID + "-volume-recovery",
		LeaseTime: leaseTTL,
		Now:       now,
	})
	if errors.Is(err, queue.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	execCtx, stopHeartbeat, err := startDeployLeaseHeartbeat(ctx, store, job, leaseTTL)
	if err != nil {
		return true, err
	}
	payload, execErr := backup.DecodeVolumeJobPayload(job.Payload)
	if execErr == nil {
		execErr = worker.executor.Execute(execCtx, payload)
	}
	heartbeatErr := stopHeartbeat()
	if execErr != nil {
		failErr := store.Fail(ctx, job.ID, job.LeaseID, "volume_recovery_failed")
		return true, errors.Join(execErr, heartbeatErr, failErr)
	}
	return true, errors.Join(store.Complete(ctx, job.ID, job.LeaseID), heartbeatErr)
}
