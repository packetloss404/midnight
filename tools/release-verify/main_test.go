package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/midnight/midnight/internal/build"
)

func TestRunPassesWithUnsignedAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	writeSupplychainArtifacts(t, dir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha", "--allow-unsigned-deb"}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.OverallStatus != "warn" {
		t.Fatalf("OverallStatus = %q, want warn (deb unsigned)", rep.OverallStatus)
	}
	if len(rep.DebFiles) != 1 {
		t.Fatalf("DebFiles = %v", rep.DebFiles)
	}
}

func TestRunFailsWithoutAllowUnsigned(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected run to fail when an unsigned .deb is present and --allow-unsigned-deb is unset")
	}
}

func TestRunPassesFullySignedRelease(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}
	writeSupplychainArtifacts(t, dir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha"}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.OverallStatus != "pass" {
		t.Fatalf("OverallStatus = %q, want pass", rep.OverallStatus)
	}
	if rep.PinnedCosignVersion != build.RequiredCosignVersion {
		t.Fatalf("PinnedCosignVersion = %q, want %q", rep.PinnedCosignVersion, build.RequiredCosignVersion)
	}
}

func TestRunReadsCosignPinFromVersionsFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}
	writeSupplychainArtifacts(t, dir)
	versionsPath := filepath.Join(dir, "versions.yaml")
	versionsBody := "buildkit: " + build.RequiredBuildKitVersion + "\ndistribution: v3.0.0\ntraefik: v3.1.5\nlego: v4.17.4\ncosign: " + build.RequiredCosignVersion + "\ncosign_linux_amd64_digest: \"" + build.RequiredCosignLinuxAMD64Digest + "\"\ncosign_linux_arm64_digest: \"" + build.RequiredCosignLinuxARM64Digest + "\"\n"
	if err := os.WriteFile(versionsPath, []byte(versionsBody), 0o644); err != nil {
		t.Fatalf("WriteFile versions: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha", "--versions-file", versionsPath}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.PinnedCosignVersion != build.RequiredCosignVersion {
		t.Fatalf("PinnedCosignVersion = %q, want %q", rep.PinnedCosignVersion, build.RequiredCosignVersion)
	}
	if rep.PinnedCosignLinuxAMD64Digest != build.RequiredCosignLinuxAMD64Digest {
		t.Fatalf("PinnedCosignLinuxAMD64Digest = %q, want %q", rep.PinnedCosignLinuxAMD64Digest, build.RequiredCosignLinuxAMD64Digest)
	}
	hasPinFinding := false
	for _, f := range rep.Findings {
		if f.Category == "versions-pin" && f.Status == "pass" {
			hasPinFinding = true
		}
	}
	if !hasPinFinding {
		t.Fatalf("expected a passing versions-pin finding; findings=%+v", rep.Findings)
	}
}

func TestRunRejectsCosignPinDrift(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}
	writeSupplychainArtifacts(t, dir)
	versionsPath := filepath.Join(dir, "versions.yaml")
	versionsBody := "buildkit: " + build.RequiredBuildKitVersion + "\ndistribution: v3.0.0\ntraefik: v3.1.5\nlego: v4.17.4\ncosign: v0.0.0-drift\n"
	if err := os.WriteFile(versionsPath, []byte(versionsBody), 0o644); err != nil {
		t.Fatalf("WriteFile versions: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha", "--versions-file", versionsPath, "--allow-unsigned-deb"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected run to fail when cosign pin drift detected")
	}
}

func TestRunFailsWhenSupplychainEvidenceMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected run to fail when cosign, SBOM, and provenance evidence are missing")
	}
}

func TestRunFailsWhenDebPackageMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeSupplychainArtifacts(t, dir)

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected run to fail when the .deb package is missing")
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.OverallStatus != "fail" {
		t.Fatalf("OverallStatus = %q, want fail", rep.OverallStatus)
	}
}

func TestRunFailsWhenRequiredOCIImageEvidenceMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cosign"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cosign", "server.verify.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "image-digests.txt"), []byte("server=ghcr.io/example/midnight-server:v0.1.0-alpha@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), 0o644); err != nil {
		t.Fatalf("WriteFile image digests: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight-server.sbom.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile sbom: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight-server.provenance.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile prov: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha"}, stdout, stderr)
	if err == nil {
		t.Fatal("expected run to fail when required OCI image evidence is incomplete")
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.OverallStatus != "fail" {
		t.Fatalf("OverallStatus = %q, want fail", rep.OverallStatus)
	}
}

func TestRunWarnsWhenRequiredOCIImageEvidenceMissingButAllowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb"), []byte{0x1, 0x2, 0x3}, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight_0.1.0_amd64.deb.asc"), []byte("-----BEGIN PGP SIGNATURE-----\n"), 0o644); err != nil {
		t.Fatalf("WriteFile asc: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := run([]string{"--artifacts-dir", dir, "--tag", "v0.1.0-alpha", "--allow-missing-supplychain"}, stdout, stderr)
	if err != nil {
		t.Fatalf("run: %v; stderr=%q", err, stderr.String())
	}
	var rep Report
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if rep.OverallStatus != "warn" {
		t.Fatalf("OverallStatus = %q, want warn", rep.OverallStatus)
	}
}

func writeSupplychainArtifacts(t *testing.T, dir string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(dir, "cosign"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	digests := ""
	for _, component := range requiredOCIComponents {
		if err := os.WriteFile(filepath.Join(dir, "cosign", component+".verify.json"), []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile verify %s: %v", component, err)
		}
		digests += component + "=ghcr.io/example/midnight-" + component + ":v0.1.0-alpha@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "image-digests.txt"), []byte(digests), 0o644); err != nil {
		t.Fatalf("WriteFile image digests: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight-server.sbom.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile sbom: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "midnight-server.provenance.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile prov: %v", err)
	}
}
