package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewEvidenceIndexWritesDeterministicRollupJSON(t *testing.T) {
	_ = t.TempDir()
	provenanceRef := ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov", MediaType: "application/vnd.in-toto+json"}
	sbomRef := ArtifactRef{Name: "app.spdx.json", Digest: "sha256:sbom", MediaType: "application/spdx+json"}
	provenanceSHA256 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntrySBOM, Path: ` evidence\sbom/app.spdx.json `, Status: ReviewStatusClean, ArtifactRef: &sbomRef},
		{Kind: EvidenceIndexEntryGCManifest, Path: "evidence/registry/gc-manifest.json", Status: ReviewStatusNeedsReview},
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
		{Kind: EvidenceIndexEntryProvenance, Path: "evidence/provenance/app.intoto.jsonl", Status: ReviewStatusClean, SHA256: provenanceSHA256, ArtifactRef: &provenanceRef},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if index.Status != ReviewStatusNeedsReview {
		t.Fatalf("Status = %q, want %q", index.Status, ReviewStatusNeedsReview)
	}

	encoded, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}

	want := `{
  "schemaVersion": "midnight.dev/build-registry-evidence-index/v1alpha1",
  "status": "needs-review",
  "entries": [
    {
      "kind": "build-release-evidence",
      "path": "evidence/build/release-evidence.json",
      "status": "clean"
    },
    {
      "kind": "registry-gc-manifest",
      "path": "evidence/registry/gc-manifest.json",
      "status": "needs-review"
    },
    {
      "kind": "provenance",
      "path": "evidence/provenance/app.intoto.jsonl",
      "status": "clean",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "artifactRef": {
        "name": "app.intoto.jsonl",
        "digest": "sha256:prov",
        "mediaType": "application/vnd.in-toto+json"
      }
    },
    {
      "kind": "sbom",
      "path": "evidence/sbom/app.spdx.json",
      "status": "clean",
      "artifactRef": {
        "name": "app.spdx.json",
        "digest": "sha256:sbom",
        "mediaType": "application/spdx+json"
      }
    }
  ]
}`
	if string(encoded) != want {
		t.Fatalf("MarshalEvidenceIndexJSON() =\n%s\nwant\n%s", encoded, want)
	}
}

func TestComputeEvidenceFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := ComputeEvidenceFileSHA256(path)
	if err != nil {
		t.Fatalf("ComputeEvidenceFileSHA256() error = %v", err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("ComputeEvidenceFileSHA256() = %q, want %q", got, want)
	}

	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean, SHA256: got},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() with computed checksum error = %v", err)
	}
	if index.Entries[0].SHA256 != want {
		t.Fatalf("stored sha256 = %q, want %q", index.Entries[0].SHA256, want)
	}
}

func TestComputeEvidenceFileSHA256RejectsNonFiles(t *testing.T) {
	_, err := ComputeEvidenceFileSHA256(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("ComputeEvidenceFileSHA256() error = %v, want regular file validation", err)
	}
}

func TestVerifyEvidenceIndexFilesChecksExistenceAndHashes(t *testing.T) {
	baseDir := t.TempDir()
	releasePath := filepath.Join(baseDir, "evidence", "build", "release-evidence.json")
	sbomPath := filepath.Join(baseDir, "evidence", "sbom", "app.spdx.json")
	if err := os.MkdirAll(filepath.Dir(releasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sbomPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("abc"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(sbomPath, []byte("sbom"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	releaseSHA256, err := ComputeEvidenceFileSHA256(releasePath)
	if err != nil {
		t.Fatalf("ComputeEvidenceFileSHA256() error = %v", err)
	}
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean, SHA256: releaseSHA256},
		{Kind: EvidenceIndexEntrySBOM, Path: "evidence/sbom/app.spdx.json", Status: ReviewStatusClean},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}

	if err := VerifyEvidenceIndexFiles(baseDir, index); err != nil {
		t.Fatalf("VerifyEvidenceIndexFiles() error = %v", err)
	}
}

func TestVerifyEvidenceIndexFilesReportsMissingFiles(t *testing.T) {
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if err := index.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	err = VerifyEvidenceIndexFiles(t.TempDir(), index)
	if err == nil || !strings.Contains(err.Error(), "release-evidence.json") {
		t.Fatalf("VerifyEvidenceIndexFiles() error = %v, want missing referenced file", err)
	}
}

func TestVerifyEvidenceIndexFilesReportsSHA256Mismatch(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "evidence", "build", "release-evidence.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}

	err = VerifyEvidenceIndexFiles(baseDir, index)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("VerifyEvidenceIndexFiles() error = %v, want sha256 mismatch", err)
	}
}

func TestEvidenceIndexSHA256Validation(t *testing.T) {
	tests := []struct {
		name   string
		sha256 string
		want   string
	}{
		{name: "short", sha256: "abc", want: "64 hex"},
		{name: "digest prefix", sha256: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", want: "64 hex"},
		{name: "non hex", sha256: "ga7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", want: "lowercase hex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
				{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean, SHA256: tt.sha256},
			}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewEvidenceIndex() error = %v, want %q", err, tt.want)
			}
		})
	}

	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean, SHA256: " BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD "},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() uppercase checksum error = %v", err)
	}
	if index.Entries[0].SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("normalized sha256 = %q", index.Entries[0].SHA256)
	}
}

func TestNewEvidenceIndexRollsUpBlockedStatus(t *testing.T) {
	_ = t.TempDir()
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
		{Kind: EvidenceIndexEntryGCManifest, Path: "evidence/registry/gc-manifest.json", Status: ReviewStatusBlocked},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if index.Status != ReviewStatusBlocked {
		t.Fatalf("Status = %q, want %q", index.Status, ReviewStatusBlocked)
	}
}

func TestEvidenceIndexRejectsUnsafePaths(t *testing.T) {
	_ = t.TempDir()
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: " ", want: "must not be empty"},
		{name: "absolute slash", path: "/evidence/release.json", want: "must be relative"},
		{name: "parent escape", path: "evidence/../release.json", want: "must not escape"},
		{name: "explicit escape", path: "../release.json", want: "must not escape"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{{Kind: EvidenceIndexEntryReleaseEvidence, Path: tt.path, Status: ReviewStatusClean}}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewEvidenceIndex() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEvidenceIndexValidateRejectsInvalidStatusAndRefs(t *testing.T) {
	_ = t.TempDir()
	index := EvidenceIndex{
		SchemaVersion: EvidenceIndexSchemaVersion,
		Status:        ReviewStatusClean,
		Entries: []EvidenceIndexEntry{
			{Kind: EvidenceIndexEntryProvenance, Path: "evidence/provenance/app.intoto.jsonl", Status: ReviewStatusBlocked, ArtifactRef: &ArtifactRef{Name: "app.intoto.jsonl", Digest: "app:latest"}},
		},
	}
	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("Validate() error = %v, want artifact ref validation", err)
	}

	index.Entries[0].ArtifactRef = &ArtifactRef{Name: "app.intoto.jsonl", Digest: "sha256:prov"}
	if err := index.Validate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Validate() error = %v, want status rollup validation", err)
	}
}

func TestWriteAndReadEvidenceIndexJSONFile(t *testing.T) {
	index := evidenceIndexForTest(t)
	path := filepath.Join(t.TempDir(), "evidence-index.json")

	if err := WriteEvidenceIndexJSONFile(path, index); err != nil {
		t.Fatalf("WriteEvidenceIndexJSONFile() error = %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	if string(written) != string(want) {
		t.Fatalf("written evidence index =\n%s\nwant\n%s", written, want)
	}

	read, err := ReadEvidenceIndexJSONFile(path)
	if err != nil {
		t.Fatalf("ReadEvidenceIndexJSONFile() error = %v", err)
	}
	if err := read.Validate(); err != nil {
		t.Fatalf("read index Validate() error = %v", err)
	}
}

func TestEvidenceIndexJSONFileRejectsInvalidPaths(t *testing.T) {
	index := evidenceIndexForTest(t)
	tempDir := t.TempDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "empty", path: " ", want: "must not be empty"},
		{name: "not json", path: filepath.Join(tempDir, "evidence-index.txt"), want: "explicit .json file path"},
		{name: "directory", path: tempDir, want: "explicit .json file path"},
		{name: "missing parent", path: filepath.Join(tempDir, "missing", "evidence-index.json"), want: "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteEvidenceIndexJSONFile(tt.path, index)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("WriteEvidenceIndexJSONFile() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestReadEvidenceIndexJSONFileStrictlyDecodesAndValidates(t *testing.T) {
	index := evidenceIndexForTest(t)
	encoded, err := MarshalEvidenceIndexJSON(index)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence-index.json")

	t.Run("unknown field", func(t *testing.T) {
		withUnknownField := strings.Replace(string(encoded), `"entries": [`, "\"unexpected\": true,\n  \"entries\": [", 1)
		if err := os.WriteFile(path, []byte(withUnknownField), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := ReadEvidenceIndexJSONFile(path)
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("ReadEvidenceIndexJSONFile() error = %v, want unknown field error", err)
		}
	})

	t.Run("invalid rollup", func(t *testing.T) {
		invalidRollup := strings.Replace(string(encoded), `"status": "clean"`, `"status": "blocked"`, 1)
		if err := os.WriteFile(path, []byte(invalidRollup), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := ReadEvidenceIndexJSONFile(path)
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("ReadEvidenceIndexJSONFile() error = %v, want validation error", err)
		}
	})
}

func evidenceIndexForTest(t *testing.T) EvidenceIndex {
	t.Helper()
	index, err := NewEvidenceIndex(EvidenceIndexInput{Entries: []EvidenceIndexEntry{
		{Kind: EvidenceIndexEntryReleaseEvidence, Path: "evidence/build/release-evidence.json", Status: ReviewStatusClean},
		{Kind: EvidenceIndexEntryGCManifest, Path: "evidence/registry/gc-manifest.json", Status: ReviewStatusClean},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	return index
}
