// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/volume"
)

func TestVolumeJobPayloadNeverCarriesSecretMaterial(t *testing.T) {
	data, err := EncodeVolumeJobPayload(VolumeJobPayload{Kind: VolumeJobBackup, RunID: "vbk_1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"kind":"volume_backup","run_id":"vbk_1"}` {
		t.Fatalf("payload = %s", data)
	}
	got, err := DecodeVolumeJobPayload(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != VolumeJobBackup || got.RunID != "vbk_1" {
		t.Fatalf("decoded = %#v", got)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"kind":"volume_backup","run_id":"vbk_1","passphrase":"no"}`),
		[]byte(`{"kind":"unknown","run_id":"vbk_1"}`),
		[]byte(`{"kind":"volume_backup","run_id":""}`),
		[]byte(`{"kind":"volume_backup","run_id":"vbk_1"} {}`),
	} {
		if _, err := DecodeVolumeJobPayload(invalid); err == nil {
			t.Fatalf("DecodeVolumeJobPayload(%s) accepted invalid payload", invalid)
		}
	}
}

func TestFilePassphraseProviderRequiresPrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.key")
	if err := os.WriteFile(path, []byte("a sufficiently long passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := (FilePassphraseProvider{Path: path}).Passphrase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(value)
	if string(value) != "a sufficiently long passphrase" {
		t.Fatalf("passphrase = %q", value)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (FilePassphraseProvider{Path: path}).Passphrase(context.Background()); err == nil {
		t.Fatal("world-readable passphrase file was accepted")
	}
}

func TestReplacementForRestoreIsStableAndScoped(t *testing.T) {
	source := volume.Volume{
		ID: "vol_source", ProjectID: "prj_1", EnvironmentID: "env_1",
		ServiceID: "svc_1", Name: "data", MountPath: "/data", SizeBytes: 42,
	}
	first := ReplacementForRestore(source, "vrs_1")
	second := ReplacementForRestore(source, "vrs_1")
	if first != second {
		t.Fatalf("replacement is not deterministic: %#v != %#v", first, second)
	}
	if first.ID == source.ID || first.RuntimeName != volume.DockerVolumeName(first.ID) {
		t.Fatalf("replacement identity = %#v", first)
	}
	if first.ProjectID != source.ProjectID || first.EnvironmentID != source.EnvironmentID ||
		first.ServiceID != source.ServiceID || first.MountPath != source.MountPath {
		t.Fatalf("replacement scope = %#v", first)
	}
}

func TestVolumeJobExecutorTreatsTerminalRunAsRetryNoop(t *testing.T) {
	runs := &terminalVolumeRunStore{
		backup:  VolumeBackupRun{ID: "vbk_1", State: VolumeBackupSucceeded},
		restore: VolumeRestoreRun{ID: "vrs_1", State: VolumeRestoreStaged},
	}
	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executor := VolumeJobExecutor{
		Runs: runs, Volumes: volume.NewMemoryStore(),
		Destination: destination,
		Passphrases: PassphraseProviderFunc(func(context.Context) ([]byte, error) {
			return nil, errors.New("must not resolve key for terminal retry")
		}),
	}
	if err := executor.Execute(context.Background(), VolumeJobPayload{Kind: VolumeJobBackup, RunID: "vbk_1"}); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), VolumeJobPayload{Kind: VolumeJobRestore, RunID: "vrs_1"}); err != nil {
		t.Fatal(err)
	}
}

type terminalVolumeRunStore struct {
	backup  VolumeBackupRun
	restore VolumeRestoreRun
}

func (s *terminalVolumeRunStore) GetBackup(context.Context, string) (VolumeBackupRun, error) {
	return s.backup, nil
}
func (*terminalVolumeRunStore) StartBackup(context.Context, string) (VolumeBackupRun, error) {
	return VolumeBackupRun{}, errors.New("unexpected StartBackup")
}
func (*terminalVolumeRunStore) CompleteBackup(context.Context, string, StreamBackupResult) (VolumeBackupRun, error) {
	return VolumeBackupRun{}, errors.New("unexpected CompleteBackup")
}
func (*terminalVolumeRunStore) FailBackup(context.Context, string, string, string) (VolumeBackupRun, error) {
	return VolumeBackupRun{}, errors.New("unexpected FailBackup")
}
func (s *terminalVolumeRunStore) GetRestore(context.Context, string) (VolumeRestoreRun, error) {
	return s.restore, nil
}
func (*terminalVolumeRunStore) StartRestore(context.Context, string) (VolumeRestoreRun, error) {
	return VolumeRestoreRun{}, errors.New("unexpected StartRestore")
}
func (*terminalVolumeRunStore) CompleteRestore(context.Context, string, StreamRestoreResult) (VolumeRestoreRun, error) {
	return VolumeRestoreRun{}, errors.New("unexpected CompleteRestore")
}
func (*terminalVolumeRunStore) FailRestore(context.Context, string, string, string) (VolumeRestoreRun, error) {
	return VolumeRestoreRun{}, errors.New("unexpected FailRestore")
}
func (*terminalVolumeRunStore) StageReplacement(context.Context, string, string, ReplacementVolume) error {
	return errors.New("unexpected StageReplacement")
}
