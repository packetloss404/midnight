package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewEvidenceIndexSorted(t *testing.T) {
	t.Parallel()

	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceRemediationPlan, Path: "remediation.json", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceInstallManifest, Path: "manifest.json", Status: EvidenceIndexStatusReady},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if index.References[0].Kind != EvidenceReferenceInstallManifest {
		t.Fatalf("References[0] = %+v, want manifest first", index.References[0])
	}
	if index.Status != EvidenceIndexStatusReady {
		t.Fatalf("Status = %q, want %q", index.Status, EvidenceIndexStatusReady)
	}
}

func TestNewEvidenceIndexRollupBlocked(t *testing.T) {
	t.Parallel()

	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "manifest.json", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceRemediationPlan, Path: "remediation.json", Status: EvidenceIndexStatusBlocked},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if index.Status != EvidenceIndexStatusBlocked {
		t.Fatalf("Status = %q, want blocked rollup", index.Status)
	}
}

func TestNewEvidenceIndexRequiresReferences(t *testing.T) {
	t.Parallel()

	if _, err := NewEvidenceIndex(EvidenceIndexInput{}); err == nil {
		t.Fatal("NewEvidenceIndex() error = nil, want error for empty input")
	}
}

func TestNewEvidenceIndexRejectsDuplicates(t *testing.T) {
	t.Parallel()

	_, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "manifest.json", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceInstallManifest, Path: "manifest.json", Status: EvidenceIndexStatusReady},
	}})
	if err == nil {
		t.Fatal("NewEvidenceIndex() error = nil, want duplicate error")
	}
}

func TestEvidenceIndexJSONFileRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	indexPath := filepath.Join(dir, "evidence-index.json")

	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "install-manifest.json", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceInstallReadinessSummary, Path: "readiness.json", Status: EvidenceIndexStatusReady},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if err := WriteEvidenceIndexJSONFile(indexPath, index); err != nil {
		t.Fatalf("WriteEvidenceIndexJSONFile() error = %v", err)
	}
	got, err := ReadEvidenceIndexJSONFile(indexPath)
	if err != nil {
		t.Fatalf("ReadEvidenceIndexJSONFile() error = %v", err)
	}
	if len(got.References) != 2 || got.References[0].Kind != EvidenceReferenceInstallManifest {
		t.Fatalf("References = %+v, want round-trip", got.References)
	}
}

func TestVerifyEvidenceIndexFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install-manifest.json")
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	digest, err := ComputeFileSHA256(manifestPath)
	if err != nil {
		t.Fatalf("ComputeFileSHA256() error = %v", err)
	}
	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "install-manifest.json", Status: EvidenceIndexStatusReady, SHA256: digest},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if err := VerifyEvidenceIndexFiles(dir, index); err != nil {
		t.Fatalf("VerifyEvidenceIndexFiles() error = %v", err)
	}

	// Mutate the file and ensure verification fails.
	if err := os.WriteFile(manifestPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	err = VerifyEvidenceIndexFiles(dir, index)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("VerifyEvidenceIndexFiles() error = %v, want sha256 mismatch", err)
	}
}

func TestVerifyEvidenceIndexFilesReportsMissingFile(t *testing.T) {
	t.Parallel()

	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "missing.json", Status: EvidenceIndexStatusReady},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if err := VerifyEvidenceIndexFiles(t.TempDir(), index); err == nil {
		t.Fatal("VerifyEvidenceIndexFiles() error = nil, want missing reference error")
	}
}

func TestVerifyAndStampEvidenceIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install-manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"hello":"world"}`), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	digest, err := ComputeFileSHA256(manifestPath)
	if err != nil {
		t.Fatalf("ComputeFileSHA256() error = %v", err)
	}
	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "install-manifest.json", Status: EvidenceIndexStatusReady, SHA256: digest},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}

	at := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	stamped, err := VerifyAndStampEvidenceIndex(dir, index, at)
	if err != nil {
		t.Fatalf("VerifyAndStampEvidenceIndex() error = %v", err)
	}
	if stamped.References[0].VerifiedAt == "" {
		t.Fatalf("VerifiedAt is empty after stamp: %+v", stamped.References[0])
	}
	parsed, err := time.Parse(time.RFC3339Nano, stamped.References[0].VerifiedAt)
	if err != nil {
		t.Fatalf("VerifiedAt %q is not RFC3339Nano: %v", stamped.References[0].VerifiedAt, err)
	}
	if !parsed.Equal(at) {
		t.Fatalf("VerifiedAt = %v, want %v", parsed, at)
	}

	// Round-trip JSON keeps the verified_at field.
	data, err := MarshalEvidenceIndexJSON(stamped)
	if err != nil {
		t.Fatalf("MarshalEvidenceIndexJSON() error = %v", err)
	}
	if !strings.Contains(string(data), `"verified_at"`) {
		t.Fatalf("evidence index JSON missing verified_at: %s", data)
	}

	// Corrupting the file makes verification fail and leaves the original index untouched.
	if err := os.WriteFile(manifestPath, []byte("changed"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := VerifyAndStampEvidenceIndex(dir, index, at); err == nil {
		t.Fatal("VerifyAndStampEvidenceIndex(corrupted) error = nil, want sha256 mismatch")
	}
}

func TestVerifyAndStampEvidenceIndexRejectsZeroTime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "install-manifest.json")
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	index, err := NewEvidenceIndex(EvidenceIndexInput{References: []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "install-manifest.json", Status: EvidenceIndexStatusReady},
	}})
	if err != nil {
		t.Fatalf("NewEvidenceIndex() error = %v", err)
	}
	if _, err := VerifyAndStampEvidenceIndex(dir, index, time.Time{}); err == nil {
		t.Fatal("VerifyAndStampEvidenceIndex(zero time) error = nil, want non-zero requirement")
	}
}

func TestEvidenceIndexReferenceVerifiedAtRoundTrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 4, 26, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	ref := EvidenceIndexReference{
		Kind:       EvidenceReferenceInstallManifest,
		Path:       "install-manifest.json",
		Status:     EvidenceIndexStatusReady,
		SHA256:     strings.Repeat("a", 64),
		VerifiedAt: at,
	}
	if err := ref.Validate(); err != nil {
		t.Fatalf("ref.Validate() error = %v", err)
	}

	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("json.Marshal(ref) error = %v", err)
	}
	var got EvidenceIndexReference
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal(ref) error = %v", err)
	}
	if got.VerifiedAt != at {
		t.Fatalf("VerifiedAt = %q, want %q", got.VerifiedAt, at)
	}
}

func TestEvidenceIndexReferenceRejectsInvalidVerifiedAt(t *testing.T) {
	t.Parallel()

	ref := EvidenceIndexReference{
		Kind:       EvidenceReferenceInstallManifest,
		Path:       "install-manifest.json",
		Status:     EvidenceIndexStatusReady,
		VerifiedAt: "not-a-date",
	}
	if err := ref.Validate(); err == nil {
		t.Fatal("ref.Validate() error = nil, want bad verified_at error")
	}
}

func TestEvidenceIndexReferencePathRules(t *testing.T) {
	t.Parallel()

	bad := []EvidenceIndexReference{
		{Kind: EvidenceReferenceInstallManifest, Path: "/abs/path", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceInstallManifest, Path: "../escape", Status: EvidenceIndexStatusReady},
		{Kind: EvidenceReferenceInstallManifest, Path: "windows\\style", Status: EvidenceIndexStatusReady},
	}
	for _, ref := range bad {
		ref := ref
		t.Run(ref.Path, func(t *testing.T) {
			t.Parallel()
			if err := ref.Validate(); err == nil {
				t.Fatalf("Validate() error = nil for %+v", ref)
			}
		})
	}
}
