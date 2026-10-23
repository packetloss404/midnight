// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package backup

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

type recordingVolumeStager struct {
	old string
	new string
}

func (s *recordingVolumeStager) StageReplacement(_ context.Context, oldVolume string, replacement ReplacementVolume) error {
	s.old, s.new = oldVolume, replacement.RuntimeName
	return nil
}

func TestDockerVolumeArchiveDriverRejectsUnsafeInputs(t *testing.T) {
	driver := DockerCLIVolumeArchiveDriver{HelperImage: "alpine:latest"}
	if _, err := driver.OpenVolumeArchive(context.Background(), "safe-volume"); err == nil ||
		!strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("unpinned helper error = %v", err)
	}
	driver.HelperImage = DefaultVolumeArchiveHelperImage
	if _, err := driver.OpenVolumeArchive(context.Background(), "bad:/source"); err == nil {
		t.Fatal("unsafe volume name accepted")
	}
	if err := driver.StageAttachmentSwap(context.Background(), "source", testReplacementVolume("midnight-replacement")); err == nil {
		t.Fatal("missing stager accepted")
	}
}

func TestDockerVolumeRecoveryLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live Docker volume recovery test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is unavailable: %v", err)
	}
	ctx := context.Background()
	source := "midnight-backup-test-source"
	restoreID := "live-proof"
	replacement := dockerRestoreVolumeName(source, restoreID)
	replacementSpec := ReplacementVolume{
		ID:            dockerRestoreVolumeID(source, restoreID),
		RuntimeName:   replacement,
		DisplayName:   "Live restored volume",
		ProjectID:     "prj_live",
		EnvironmentID: "env_live",
		ServiceID:     "svc_live",
		MountPath:     "/data",
	}
	cleanupDockerVolume(t, source)
	cleanupDockerVolume(t, replacement)
	t.Cleanup(func() {
		cleanupDockerVolume(t, source)
		cleanupDockerVolume(t, replacement)
	})
	if output, err := exec.Command("docker", "volume", "create", source).CombinedOutput(); err != nil {
		t.Fatalf("create source volume: %v: %s", err, output)
	}
	fixture := "midnight-live-volume-" + strings.Repeat("record-", 1000)
	if output, err := exec.Command(
		"docker", "run", "--rm", "--network", "none",
		"--mount", "type=volume,src="+source+",dst=/target",
		DefaultVolumeArchiveHelperImage,
		"sh", "-c", `printf "%s" "$1" > /target/proof.txt`, "fixture", fixture,
	).CombinedOutput(); err != nil {
		t.Fatalf("populate source volume: %v: %s", err, output)
	}

	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stager := &recordingVolumeStager{}
	driver := DockerCLIVolumeArchiveDriver{Stager: stager}
	recovery := VolumeRecovery{Destination: destination, Driver: driver}
	passphrase := []byte("correct horse battery staple")
	backupResult, err := recovery.Backup(ctx, VolumeBackupRequest{
		VolumeName: source,
		ObjectKey:  "docker/live-volume.mbkp",
		Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("live Backup() error = %v", err)
	}
	restoreResult, err := recovery.Restore(ctx, VolumeRestoreRequest{
		RestoreID:            restoreID,
		SourceVolumeName:     source,
		ObjectKey:            backupResult.Archive.Object.Key,
		ExpectedObjectSHA256: backupResult.Archive.Object.SHA256,
		ExpectedSourceSHA256: backupResult.Archive.SourceSHA256,
		Passphrase:           passphrase,
		Replacement:          replacementSpec,
	})
	if err != nil {
		t.Fatalf("live Restore() error = %v", err)
	}
	if restoreResult.ReplacementVolume != replacement || stager.old != source || stager.new != replacement {
		t.Fatalf("restore/stager = %#v, %q -> %q", restoreResult, stager.old, stager.new)
	}
	gotSource := readDockerVolumeProof(t, source)
	gotReplacement := readDockerVolumeProof(t, replacement)
	if !bytes.Equal([]byte(gotSource), []byte(fixture)) ||
		!bytes.Equal([]byte(gotReplacement), []byte(fixture)) {
		t.Fatalf("source/replacement proof differ: source=%d replacement=%d want=%d", len(gotSource), len(gotReplacement), len(fixture))
	}
}

func cleanupDockerVolume(t *testing.T, name string) {
	t.Helper()
	output, err := exec.Command("docker", "volume", "rm", "-f", name).CombinedOutput()
	if err != nil && !strings.Contains(string(output), "no such volume") {
		t.Logf("cleanup volume %s: %v: %s", name, err, output)
	}
}

func readDockerVolumeProof(t *testing.T, name string) string {
	t.Helper()
	output, err := exec.Command(
		"docker", "run", "--rm", "--network", "none", "--read-only",
		"--mount", "type=volume,src="+name+",dst=/target,readonly",
		DefaultVolumeArchiveHelperImage,
		"cat", "/target/proof.txt",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("read volume %s: %v: %s", name, err, output)
	}
	return string(output)
}

func TestDockerRestoreNameIsStableAndDistinct(t *testing.T) {
	first := dockerRestoreVolumeName("source", "restore")
	second := dockerRestoreVolumeName("source", "restore")
	if first != second || first == "source" {
		t.Fatalf("restore name = %q, %q", first, second)
	}
	if err := validateDockerVolumeName(first); err != nil {
		t.Fatal(err)
	}
}
