// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/midnight/midnight/internal/volume"
)

const (
	VolumeJobBackup  = "volume_backup"
	VolumeJobRestore = "volume_restore"

	EnvBackupPassphraseFile = "MIDNIGHT_BACKUP_PASSPHRASE_FILE"
)

// VolumeJobPayload is deliberately reference-only. In particular, encryption
// passphrases and destination credentials must never enter the durable queue.
type VolumeJobPayload struct {
	Kind  string `json:"kind"`
	RunID string `json:"run_id"`
}

func EncodeVolumeJobPayload(payload VolumeJobPayload) ([]byte, error) {
	payload.Kind = strings.TrimSpace(payload.Kind)
	payload.RunID = strings.TrimSpace(payload.RunID)
	switch payload.Kind {
	case VolumeJobBackup, VolumeJobRestore:
	default:
		return nil, errors.New("volume job kind is invalid")
	}
	if payload.RunID == "" {
		return nil, errors.New("volume job run_id is required")
	}
	return json.Marshal(payload)
}

func DecodeVolumeJobPayload(data []byte) (VolumeJobPayload, error) {
	var payload VolumeJobPayload
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return VolumeJobPayload{}, fmt.Errorf("decode volume job payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return VolumeJobPayload{}, errors.New("decode volume job payload: trailing JSON")
		}
		return VolumeJobPayload{}, fmt.Errorf("decode volume job payload: %w", err)
	}
	encoded, err := EncodeVolumeJobPayload(payload)
	if err != nil {
		return VolumeJobPayload{}, err
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return VolumeJobPayload{}, err
	}
	return payload, nil
}

// PassphraseProvider resolves the archive key only inside the worker process.
// Implementations must return a fresh byte slice because the executor clears it
// immediately after use.
type PassphraseProvider interface {
	Passphrase(context.Context) ([]byte, error)
}

type PassphraseProviderFunc func(context.Context) ([]byte, error)

func (f PassphraseProviderFunc) Passphrase(ctx context.Context) ([]byte, error) {
	return f(ctx)
}

// FilePassphraseProvider is the production key boundary. The file is expected
// to be a root/container-secret mounted file, not a path supplied by an API
// request or queue message.
type FilePassphraseProvider struct {
	Path string
}

func (p FilePassphraseProvider) Passphrase(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(p.Path)
	if path == "" {
		return nil, fmt.Errorf("%s is not configured", EnvBackupPassphraseFile)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat backup passphrase file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("backup passphrase path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("backup passphrase file must not be group/world accessible")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backup passphrase file: %w", err)
	}
	value = []byte(strings.TrimSpace(string(value)))
	if len(value) < 16 {
		clear(value)
		return nil, errors.New("backup passphrase must contain at least 16 bytes")
	}
	return value, nil
}

// VolumeRunStore is the worker-facing subset of durable run persistence.
type VolumeRunStore interface {
	GetBackup(context.Context, string) (VolumeBackupRun, error)
	StartBackup(context.Context, string) (VolumeBackupRun, error)
	CompleteBackup(context.Context, string, StreamBackupResult) (VolumeBackupRun, error)
	FailBackup(context.Context, string, string, string) (VolumeBackupRun, error)
	GetRestore(context.Context, string) (VolumeRestoreRun, error)
	StartRestore(context.Context, string) (VolumeRestoreRun, error)
	CompleteRestore(context.Context, string, StreamRestoreResult) (VolumeRestoreRun, error)
	FailRestore(context.Context, string, string, string) (VolumeRestoreRun, error)
	StageReplacement(context.Context, string, string, ReplacementVolume) error
}

// VolumeJobExecutor performs one crash-retry-safe backup or restore. Durable
// run state is authoritative; already-terminal retries are successful no-ops.
type VolumeJobExecutor struct {
	Runs        VolumeRunStore
	Volumes     volume.Store
	Destination Destination
	Driver      DockerCLIVolumeArchiveDriver
	Passphrases PassphraseProvider
}

func (e VolumeJobExecutor) Execute(ctx context.Context, payload VolumeJobPayload) error {
	if e.Runs == nil || e.Volumes == nil || e.Destination == nil || e.Passphrases == nil {
		return errors.New("volume job executor is not fully configured")
	}
	switch payload.Kind {
	case VolumeJobBackup:
		return e.executeBackup(ctx, payload.RunID)
	case VolumeJobRestore:
		return e.executeRestore(ctx, payload.RunID)
	default:
		return errors.New("volume job kind is invalid")
	}
}

func (e VolumeJobExecutor) executeBackup(ctx context.Context, id string) error {
	run, err := e.Runs.GetBackup(ctx, id)
	if err != nil {
		return err
	}
	if run.State == VolumeBackupSucceeded {
		return nil
	}
	if run.State != VolumeBackupPending {
		return fmt.Errorf("%w: backup is %s", ErrVolumeRunConflict, run.State)
	}
	run, err = e.Runs.StartBackup(ctx, id)
	if err != nil {
		return err
	}
	passphrase, err := e.Passphrases.Passphrase(ctx)
	if err != nil {
		e.failBackup(ctx, id, "backup_key_unavailable", err)
		return err
	}
	defer clear(passphrase)
	recovery := VolumeRecovery{Destination: e.Destination, Driver: e.Driver}
	result, err := recovery.Backup(ctx, VolumeBackupRequest{
		VolumeName: volume.DockerVolumeName(run.VolumeID),
		ObjectKey:  run.ObjectKey,
		Passphrase: passphrase,
	})
	if err != nil {
		e.failBackup(ctx, id, "backup_execution_failed", err)
		return err
	}
	_, err = e.Runs.CompleteBackup(ctx, id, result.Archive)
	return err
}

func (e VolumeJobExecutor) executeRestore(ctx context.Context, id string) error {
	restore, err := e.Runs.GetRestore(ctx, id)
	if err != nil {
		return err
	}
	if restore.State == VolumeRestoreStaged || restore.State == VolumeRestoreApplied {
		return nil
	}
	if restore.State != VolumeRestorePending {
		return fmt.Errorf("%w: restore is %s", ErrVolumeRunConflict, restore.State)
	}
	backupRun, err := e.Runs.GetBackup(ctx, restore.BackupID)
	if err != nil {
		return err
	}
	if backupRun.State != VolumeBackupSucceeded {
		return fmt.Errorf("%w: source backup is %s", ErrVolumeRunConflict, backupRun.State)
	}
	source, err := e.Volumes.Get(ctx, restore.SourceVolumeID)
	if err != nil {
		return err
	}
	restore, err = e.Runs.StartRestore(ctx, id)
	if err != nil {
		return err
	}
	passphrase, err := e.Passphrases.Passphrase(ctx)
	if err != nil {
		e.failRestore(ctx, id, "backup_key_unavailable", err)
		return err
	}
	defer clear(passphrase)
	replacement := ReplacementForRestore(source, id)
	driver := e.Driver
	driver.Stager = volumeRestoreStager{runs: e.Runs, restoreID: id}
	recovery := VolumeRecovery{Destination: e.Destination, Driver: driver}
	result, err := recovery.Restore(ctx, VolumeRestoreRequest{
		RestoreID:            id,
		SourceVolumeName:     volume.DockerVolumeName(source.ID),
		ObjectKey:            backupRun.ObjectKey,
		ExpectedObjectSHA256: backupRun.ObjectSHA256,
		ExpectedSourceSHA256: backupRun.SourceSHA256,
		Passphrase:           passphrase,
		Replacement:          replacement,
	})
	if err != nil {
		e.failRestore(ctx, id, "restore_execution_failed", err)
		return err
	}
	_, err = e.Runs.CompleteRestore(ctx, id, result.Archive)
	return err
}

func ReplacementForRestore(source volume.Volume, restoreID string) ReplacementVolume {
	sourceName := volume.DockerVolumeName(source.ID)
	id := dockerRestoreVolumeID(sourceName, strings.TrimSpace(restoreID))
	return ReplacementVolume{
		ID:            id,
		RuntimeName:   volume.DockerVolumeName(id),
		DisplayName:   source.Name + " restored",
		ProjectID:     source.ProjectID,
		EnvironmentID: source.EnvironmentID,
		ServiceID:     source.ServiceID,
		MountPath:     source.MountPath,
		SizeBytes:     source.SizeBytes,
	}
}

func (e VolumeJobExecutor) failBackup(ctx context.Context, id, code string, cause error) {
	_, _ = e.Runs.FailBackup(context.WithoutCancel(ctx), id, code, boundedJobError(cause))
}

func (e VolumeJobExecutor) failRestore(ctx context.Context, id, code string, cause error) {
	_, _ = e.Runs.FailRestore(context.WithoutCancel(ctx), id, code, boundedJobError(cause))
}

func boundedJobError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}

type volumeRestoreStager struct {
	runs      VolumeRunStore
	restoreID string
}

func (s volumeRestoreStager) StageReplacement(ctx context.Context, sourceVolumeName string, replacement ReplacementVolume) error {
	return s.runs.StageReplacement(ctx, s.restoreID, sourceVolumeName, replacement)
}
