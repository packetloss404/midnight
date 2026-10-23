package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestValidate(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal(manifest) error = %v", err)
	}
	if string(data) == "{}" {
		t.Fatal("Marshal(manifest) = {}, want stable fields")
	}
}

func TestManifestValidateRejectsInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "schema", mutate: func(m *Manifest) { m.SchemaVersion = 0 }},
		{name: "required", mutate: func(m *Manifest) { m.StateDir = " " }},
		{name: "services", mutate: func(m *Manifest) { m.Services = nil }},
		{name: "empty service", mutate: func(m *Manifest) { m.Services = []string{"midnight-server.service", " "} }},
		{name: "duplicate service", mutate: func(m *Manifest) { m.Services = []string{"midnight-server.service", "midnight-server.service"} }},
		{name: "mode path", mutate: func(m *Manifest) { m.Modes = []ModeMetadata{{Mode: "0644"}} }},
		{name: "mode value", mutate: func(m *Manifest) { m.Modes = []ModeMetadata{{Path: "/etc/midnight/config.yaml"}} }},
		{name: "empty package content", mutate: func(m *Manifest) { m.PackageContent = []string{"/opt/midnight/bin/midnight", " "} }},
		{name: "duplicate package content", mutate: func(m *Manifest) {
			m.PackageContent = []string{"/opt/midnight/bin/midnight", "/opt/midnight/bin/midnight"}
		}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manifest := validManifest()
			tt.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestVerifyPackageContent(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	manifest.PackageContent = []string{"/opt/midnight/bin/midnight", "/etc/midnight/midnight.env"}
	verification, err := VerifyPackageContent(manifest, []string{"/opt/midnight/bin/midnight"})
	if err != nil {
		t.Fatalf("VerifyPackageContent() error = %v", err)
	}
	if len(verification.Missing) != 1 || verification.Missing[0] != "/etc/midnight/midnight.env" {
		t.Fatalf("Missing = %+v, want env path", verification.Missing)
	}
}

func TestVerifyPackageContentDoesNotInspectHost(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	manifest.PackageContent = []string{"/definitely/not/on/host"}
	verification, err := VerifyPackageContent(manifest, []string{"/definitely/not/on/host"})
	if err != nil {
		t.Fatalf("VerifyPackageContent() error = %v", err)
	}
	if len(verification.Missing) != 0 {
		t.Fatalf("Missing = %+v, want none from explicit path list", verification.Missing)
	}
}

func TestManifestFileRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest := validManifest()
	manifest.Modes = []ModeMetadata{{Path: "/etc/midnight/midnight.env", Mode: "0640"}}

	if err := WriteManifestFile(path, manifest, 0o640); err != nil {
		t.Fatalf("WriteManifestFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o640 {
		t.Fatalf("manifest mode = %v, want 0640", got)
	}

	got, err := ReadManifestFile(path)
	if err != nil {
		t.Fatalf("ReadManifestFile() error = %v", err)
	}
	if len(got.Modes) != 1 || got.Modes[0] != manifest.Modes[0] {
		t.Fatalf("Modes = %+v, want %+v", got.Modes, manifest.Modes)
	}
}

func TestManifestFileRequiresExplicitPath(t *testing.T) {
	t.Parallel()

	if err := WriteManifestFile(" ", validManifest(), 0o600); err == nil {
		t.Fatal("WriteManifestFile() error = nil, want error")
	}
	if _, err := ReadManifestFile(" "); err == nil {
		t.Fatal("ReadManifestFile() error = nil, want error")
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:  1,
		PackageName:    "midnight",
		PackageVersion: "0.0.0",
		InstalledAt:    "2026-04-25T00:00:00Z",
		InstallRoot:    "/opt/midnight",
		ConfigDir:      "/etc/midnight",
		StateDir:       "/var/lib/midnight",
		LogDir:         "/var/log/midnight",
		User:           "midnight",
		Group:          "midnight",
		Services:       []string{"midnight-server.service", "midnight-worker.service"},
	}
}

func TestPackageContentDigestValidation(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	manifest.PackageContentDigests = []PackageContentEntry{
		{Path: "/opt/midnight/bin/midnight", SHA256: "abcd"},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for short sha256")
	}

	manifest.PackageContentDigests[0].SHA256 = "Z" + strings.Repeat("0", 63)
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error for non-hex sha256")
	}
}

func TestPackageContentDigestRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "midnight")
	contents := []byte("midnight-binary-stub")
	if err := os.WriteFile(binPath, contents, 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	digest, err := ComputeFileSHA256(binPath)
	if err != nil {
		t.Fatalf("ComputeFileSHA256() error = %v", err)
	}

	manifest := validManifest()
	manifest.PackageContentDigests = []PackageContentEntry{{Path: "midnight", SHA256: digest}}
	manifestPath := filepath.Join(dir, "install-manifest.json")
	if err := WriteManifestFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatalf("WriteManifestFile() error = %v", err)
	}
	got, err := ReadManifestFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifestFile() error = %v", err)
	}
	if len(got.PackageContentDigests) != 1 || got.PackageContentDigests[0].SHA256 != digest {
		t.Fatalf("PackageContentDigests = %+v, want one digest %q", got.PackageContentDigests, digest)
	}

	verification, err := VerifyPackageContentFiles(got, dir)
	if err != nil {
		t.Fatalf("VerifyPackageContentFiles() error = %v", err)
	}
	if len(verification.Missing) != 0 || len(verification.DigestMismatch) != 0 {
		t.Fatalf("verification = %+v, want clean", verification)
	}
}

func TestVerifyPackageContentFilesDetectsMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binPath := filepath.Join(dir, "midnight")
	if err := os.WriteFile(binPath, []byte("v1"), 0o644); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	manifest := validManifest()
	manifest.PackageContentDigests = []PackageContentEntry{
		{Path: "midnight", SHA256: strings.Repeat("0", 64)},
	}
	verification, err := VerifyPackageContentFiles(manifest, dir)
	if err != nil {
		t.Fatalf("VerifyPackageContentFiles() error = %v", err)
	}
	if len(verification.DigestMismatch) != 1 || verification.DigestMismatch[0].Path != "midnight" {
		t.Fatalf("DigestMismatch = %+v, want one mismatch", verification.DigestMismatch)
	}
}

func TestVerifyPackageContentFilesRequiresBaseDir(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	if _, err := VerifyPackageContentFiles(manifest, " "); err == nil {
		t.Fatal("VerifyPackageContentFiles() error = nil, want error")
	}
	if _, err := VerifyPackageContentFiles(manifest, filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("VerifyPackageContentFiles() error = nil, want error")
	}
}

func TestVerifyPackageContentFilesReportsMissing(t *testing.T) {
	t.Parallel()

	manifest := validManifest()
	manifest.PackageContentDigests = []PackageContentEntry{{Path: "missing", SHA256: ""}}
	verification, err := VerifyPackageContentFiles(manifest, t.TempDir())
	if err != nil {
		t.Fatalf("VerifyPackageContentFiles() error = %v", err)
	}
	if len(verification.Missing) != 1 || verification.Missing[0] != "missing" {
		t.Fatalf("Missing = %+v, want one missing", verification.Missing)
	}
}
