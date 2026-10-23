package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewReleaseEvidenceBundleRendersDeterministicJSON(t *testing.T) {
	bundle := releaseEvidenceBundleForTest(t)

	encoded, err := MarshalReleaseEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() error = %v", err)
	}
	second, err := MarshalReleaseEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() second error = %v", err)
	}
	if string(encoded) != string(second) {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() was not deterministic:\n%s\nthen\n%s", encoded, second)
	}

	wantPrefix := `{
  "schemaVersion": "midnight.dev/build-registry-release-evidence-bundle/v1alpha1",
  "status": "clean",
  "releaseEvidence": {`
	if !strings.HasPrefix(string(encoded), wantPrefix) {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() prefix =\n%s\nwant prefix\n%s", encoded, wantPrefix)
	}
	for _, want := range []string{`"evidenceIndex": {`, `"kind": "build-release-evidence"`, `"path": "evidence/build/release-evidence.json"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("MarshalReleaseEvidenceBundleJSON() missing %q in\n%s", want, encoded)
		}
	}
}

func TestReleaseEvidenceBundleValidateRejectsStatusMismatch(t *testing.T) {
	packet := cleanReleaseEvidencePacketForTest(t)
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
		{Kind: EvidenceIndexEntryGCManifest, Path: "evidence/registry/gc-manifest.json", Status: ReviewStatusNeedsReview},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}

	_, err = NewReleaseEvidenceBundle(ReleaseEvidenceBundleInput{ReleaseEvidence: packet, EvidenceIndex: index})
	if err == nil || !strings.Contains(err.Error(), "status mismatch") {
		t.Fatalf("NewReleaseEvidenceBundle() error = %v, want status mismatch", err)
	}
}

func TestWriteAndReadReleaseEvidenceBundleJSONFile(t *testing.T) {
	bundle := releaseEvidenceBundleForTest(t)
	path := filepath.Join(t.TempDir(), "release-evidence-bundle.json")

	if err := WriteReleaseEvidenceBundleJSONFile(path, bundle); err != nil {
		t.Fatalf("WriteReleaseEvidenceBundleJSONFile() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalReleaseEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() error = %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("written release evidence bundle =\n%s\nwant\n%s", written, want)
	}

	read, err := ReadReleaseEvidenceBundleJSONFile(path)
	if err != nil {
		t.Fatalf("ReadReleaseEvidenceBundleJSONFile() error = %v", err)
	}
	if err := read.Validate(); err != nil {
		t.Fatalf("read bundle Validate() error = %v", err)
	}
}

func TestReleaseEvidenceBundleJSONFileRejectsInvalidPaths(t *testing.T) {
	bundle := releaseEvidenceBundleForTest(t)
	tempDir := t.TempDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: " ", want: "must not be empty"},
		{name: "not json", path: filepath.Join(tempDir, "release-evidence-bundle.txt"), want: "explicit .json file path"},
		{name: "directory", path: tempDir, want: "explicit .json file path"},
		{name: "missing parent", path: filepath.Join(tempDir, "missing", "release-evidence-bundle.json"), want: "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteReleaseEvidenceBundleJSONFile(tt.path, bundle)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WriteReleaseEvidenceBundleJSONFile() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReadReleaseEvidenceBundleJSONFileStrictlyDecodesAndValidates(t *testing.T) {
	bundle := releaseEvidenceBundleForTest(t)
	encoded, err := MarshalReleaseEvidenceBundleJSON(bundle)
	if err != nil {
		t.Fatalf("MarshalReleaseEvidenceBundleJSON() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "release-evidence-bundle.json")

	t.Run("unknown field", func(t *testing.T) {
		withUnknownField := strings.Replace(string(encoded), `"releaseEvidence": {`, "\"unexpected\": true,\n  \"releaseEvidence\": {", 1)
		if err := os.WriteFile(path, []byte(withUnknownField), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := ReadReleaseEvidenceBundleJSONFile(path)
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("ReadReleaseEvidenceBundleJSONFile() error = %v, want unknown field error", err)
		}
	})

	t.Run("trailing data", func(t *testing.T) {
		if err := os.WriteFile(path, append(encoded, []byte("{}")...), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := ReadReleaseEvidenceBundleJSONFile(path)
		if err == nil || !strings.Contains(err.Error(), "single object") {
			t.Fatalf("ReadReleaseEvidenceBundleJSONFile() error = %v, want single object error", err)
		}
	})

	t.Run("status mismatch", func(t *testing.T) {
		mismatched := strings.Replace(string(encoded), `"status": "clean"`, `"status": "blocked"`, 1)
		if err := os.WriteFile(path, []byte(mismatched), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := ReadReleaseEvidenceBundleJSONFile(path)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("ReadReleaseEvidenceBundleJSONFile() error = %v, want validation error", err)
		}
	})
}

func releaseEvidenceBundleForTest(t *testing.T) ReleaseEvidenceBundle {
	t.Helper()
	packet := cleanReleaseEvidencePacketForTest(t)
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
		{Kind: EvidenceIndexEntryGCManifest, Path: "evidence/registry/gc-manifest.json", Status: ReviewStatusClean},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	bundle, err := NewReleaseEvidenceBundle(ReleaseEvidenceBundleInput{ReleaseEvidence: packet, EvidenceIndex: index})
	if err != nil {
		t.Fatalf("NewReleaseEvidenceBundle() error = %v", err)
	}
	return bundle
}
