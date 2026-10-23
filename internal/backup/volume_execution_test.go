// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type memoryVolumeArchiveDriver struct {
	volumes      map[string][]byte
	replacement  string
	discarded    []string
	stagedOld    string
	stagedNew    string
	verifyErr    error
	stageErr     error
	sourceOpened bool
}

func (d *memoryVolumeArchiveDriver) OpenVolumeArchive(_ context.Context, volumeName string) (io.ReadCloser, error) {
	value, ok := d.volumes[volumeName]
	if !ok {
		return nil, errors.New("volume not found")
	}
	d.sourceOpened = true
	return io.NopCloser(bytes.NewReader(value)), nil
}

func (d *memoryVolumeArchiveDriver) CreateReplacementVolume(_ context.Context, _ string, _ string, replacement ReplacementVolume) (io.WriteCloser, error) {
	d.replacement = replacement.RuntimeName
	return &memoryVolumeWriter{
		onClose: func(value []byte) {
			d.volumes[d.replacement] = value
		},
	}, nil
}

func (d *memoryVolumeArchiveDriver) VerifyReplacementVolume(_ context.Context, replacementVolumeName string) error {
	if len(d.volumes[replacementVolumeName]) == 0 {
		return errors.New("replacement is empty")
	}
	return d.verifyErr
}

func (d *memoryVolumeArchiveDriver) StageAttachmentSwap(_ context.Context, sourceVolumeName string, replacement ReplacementVolume) error {
	if d.stageErr != nil {
		return d.stageErr
	}
	d.stagedOld = sourceVolumeName
	d.stagedNew = replacement.RuntimeName
	return nil
}

func (d *memoryVolumeArchiveDriver) DiscardReplacementVolume(_ context.Context, replacementVolumeName string) error {
	d.discarded = append(d.discarded, replacementVolumeName)
	delete(d.volumes, replacementVolumeName)
	return nil
}

type memoryVolumeWriter struct {
	bytes.Buffer
	onClose func([]byte)
}

func (w *memoryVolumeWriter) Close() error {
	w.onClose(append([]byte(nil), w.Bytes()...))
	return nil
}

func TestVolumeRecoveryBacksUpAndStagesVerifiedReplacement(t *testing.T) {
	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte(strings.Repeat("persistent-record\n", 10_000))
	driver := &memoryVolumeArchiveDriver{
		volumes: map[string][]byte{"midnight-vol_source": source},
	}
	recovery := VolumeRecovery{Destination: destination, Driver: driver}
	passphrase := []byte("correct horse battery staple")
	backupResult, err := recovery.Backup(context.Background(), VolumeBackupRequest{
		VolumeName: "midnight-vol_source",
		ObjectKey:  "backups/vol_source.mbkp",
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if !driver.sourceOpened || backupResult.Archive.SourceBytes != int64(len(source)) {
		t.Fatalf("backup result = %#v", backupResult)
	}

	restoreResult, err := recovery.Restore(context.Background(), VolumeRestoreRequest{
		RestoreID:            "restore_1",
		SourceVolumeName:     "midnight-vol_source",
		ObjectKey:            backupResult.Archive.Object.Key,
		ExpectedObjectSHA256: backupResult.Archive.Object.SHA256,
		ExpectedSourceSHA256: backupResult.Archive.SourceSHA256,
		Passphrase:           passphrase,
		Replacement:          testReplacementVolume("midnight-vol_source-restore-restore_1"),
	})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !restoreResult.SourceRetained || !restoreResult.AttachmentSwapStaged {
		t.Fatalf("restore result = %#v", restoreResult)
	}
	if driver.stagedOld != "midnight-vol_source" || driver.stagedNew != restoreResult.ReplacementVolume {
		t.Fatalf("staged swap = %q -> %q", driver.stagedOld, driver.stagedNew)
	}
	if !bytes.Equal(driver.volumes["midnight-vol_source"], source) {
		t.Fatal("source volume was mutated")
	}
	if !bytes.Equal(driver.volumes[restoreResult.ReplacementVolume], source) {
		t.Fatal("replacement volume does not contain restored archive")
	}
	if len(driver.discarded) != 0 {
		t.Fatalf("successful replacement discarded: %v", driver.discarded)
	}
}

func TestVolumeRecoveryDiscardsFailedReplacementAndRetainsSource(t *testing.T) {
	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("old-volume-remains")
	driver := &memoryVolumeArchiveDriver{
		volumes:   map[string][]byte{"midnight-vol_source": source},
		verifyErr: errors.New("smoke probe failed"),
	}
	recovery := VolumeRecovery{Destination: destination, Driver: driver}
	passphrase := []byte("correct horse battery staple")
	backupResult, err := recovery.Backup(context.Background(), VolumeBackupRequest{
		VolumeName: "midnight-vol_source",
		ObjectKey:  "archive.mbkp",
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = recovery.Restore(context.Background(), VolumeRestoreRequest{
		RestoreID:            "restore_failed",
		SourceVolumeName:     "midnight-vol_source",
		ObjectKey:            backupResult.Archive.Object.Key,
		ExpectedObjectSHA256: backupResult.Archive.Object.SHA256,
		ExpectedSourceSHA256: backupResult.Archive.SourceSHA256,
		Passphrase:           passphrase,
		Replacement:          testReplacementVolume("midnight-vol_source-restore-restore_failed"),
	})
	if err == nil || !strings.Contains(err.Error(), "smoke probe failed") {
		t.Fatalf("Restore() error = %v", err)
	}
	if !bytes.Equal(driver.volumes["midnight-vol_source"], source) {
		t.Fatal("source volume was not retained after failure")
	}
	if len(driver.discarded) != 1 {
		t.Fatalf("discarded = %v, want one replacement", driver.discarded)
	}
	if driver.stagedOld != "" || driver.stagedNew != "" {
		t.Fatal("failed replacement staged an attachment swap")
	}
}

func testReplacementVolume(runtimeName string) ReplacementVolume {
	return ReplacementVolume{
		ID:            strings.TrimPrefix(runtimeName, "midnight-"),
		RuntimeName:   runtimeName,
		DisplayName:   "Restored volume",
		ProjectID:     "prj_test",
		EnvironmentID: "env_test",
		ServiceID:     "svc_test",
		MountPath:     "/data",
	}
}
