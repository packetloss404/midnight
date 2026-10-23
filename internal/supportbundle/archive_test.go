package supportbundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestWriteDeterministicArchiveProducesByteIdenticalOutput(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("api log line 1\napi log line 2\n")},
		{Path: "metrics/api.prom", Kind: "metric", Bytes: []byte("metric_one 42\n")},
	}

	var buf1, buf2 bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf1, "bundle-001", createdAt, Selector{Logs: true, Metrics: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	if _, err := WriteDeterministicArchive(&buf2, "bundle-001", createdAt, Selector{Logs: true, Metrics: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
		t.Fatalf("WriteDeterministicArchive() produced different bytes across runs")
	}
}

func TestWriteDeterministicArchiveSortsEntriesAndEmbedsManifest(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/zzz.log", Kind: "log", Bytes: []byte("z")},
		{Path: "logs/aaa.log", Kind: "log", Bytes: []byte("a")},
	}

	var buf bytes.Buffer
	manifest, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true}, entries)
	if err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	if len(manifest.Files) != 2 || manifest.Files[0].Path != "logs/aaa.log" || manifest.Files[1].Path != "logs/zzz.log" {
		t.Fatalf("WriteDeterministicArchive() manifest = %+v, want sorted entries", manifest.Files)
	}
	wantHashA := hex.EncodeToString(sha256Sum([]byte("a")))
	if manifest.Files[0].SHA256 != wantHashA {
		t.Fatalf("WriteDeterministicArchive() entry hash = %q, want %q", manifest.Files[0].SHA256, wantHashA)
	}

	got, err := ReadArchiveManifest(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadArchiveManifest() error = %v", err)
	}
	if got.ID != "bundle-001" || len(got.Files) != 2 {
		t.Fatalf("ReadArchiveManifest() = %+v, want round-trip manifest", got)
	}
}

func TestWriteDeterministicArchiveRejectsBlockedAndInvalidPaths(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		path string
	}{
		{name: "blocked secret", path: "logs/secret.log"},
		{name: "absolute path", path: "/etc/midnight/secrets.key"},
		{name: "traversal", path: "../escape.log"},
		{name: "manifest collision", path: ManifestArchivePath},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			_, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true}, []ArchiveEntry{{Path: tt.path, Kind: "log", Bytes: []byte("x")}})
			if err == nil {
				t.Fatalf("WriteDeterministicArchive() error = nil, want validation error for %q", tt.path)
			}
		})
	}
}

func TestVerifyArchiveContentsRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("hello")},
		{Path: "events/api.json", Kind: "event", Bytes: []byte(`{"id":"x"}`)},
	}
	var buf bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true, Events: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	manifest, mismatches, err := VerifyArchiveContents(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("VerifyArchiveContents() error = %v", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("VerifyArchiveContents() mismatches = %v, want none", mismatches)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("VerifyArchiveContents() manifest = %+v, want 2 files", manifest)
	}
}

func TestVerifyArchiveContentsDetectsTampering(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("hello")},
	}
	var buf bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	// Flip one byte of the payload region after the manifest.
	raw := buf.Bytes()
	idx := bytes.Index(raw, []byte("hello"))
	if idx < 0 {
		t.Fatal("could not locate payload byte to tamper with")
	}
	raw[idx] = 'H'
	_, mismatches, err := VerifyArchiveContents(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("VerifyArchiveContents() error = %v", err)
	}
	if len(mismatches) != 1 || mismatches[0] != "logs/api.log" {
		t.Fatalf("VerifyArchiveContents() mismatches = %v, want logs/api.log", mismatches)
	}
}

func TestReadArchiveManifestRequiresManifestEntry(t *testing.T) {
	// Empty archive: no manifest.
	if _, err := ReadArchiveManifest(strings.NewReader("")); err == nil {
		t.Fatal("ReadArchiveManifest() error = nil, want missing manifest error")
	}
}

func TestVerifyArchiveRoundTripAcceptsCleanArchive(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("api log")},
		{Path: "events/api.json", Kind: "event", Bytes: []byte(`{"id":"x"}`)},
	}
	var buf bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true, Events: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	if err := VerifyArchive(buf.Bytes()); err != nil {
		t.Fatalf("VerifyArchive() error = %v, want nil for clean archive", err)
	}
}

func TestVerifyArchiveDetectsTampering(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("hello")},
	}
	var buf bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	raw := buf.Bytes()
	idx := bytes.Index(raw, []byte("hello"))
	if idx < 0 {
		t.Fatal("could not locate payload byte to tamper with")
	}
	raw[idx] = 'H'
	err := VerifyArchive(raw)
	if err == nil {
		t.Fatal("VerifyArchive() error = nil, want SUPPORT_BUNDLE_VERIFY_FAILED")
	}
	if !strings.Contains(err.Error(), "logs/api.log") {
		t.Fatalf("VerifyArchive() error = %v, want mention of mismatched path", err)
	}
}

func TestVerifyArchiveRejectsEmptyInput(t *testing.T) {
	if err := VerifyArchive(nil); err == nil {
		t.Fatal("VerifyArchive(nil) error = nil, want non-nil error")
	}
	if err := VerifyArchive([]byte{}); err == nil {
		t.Fatal("VerifyArchive(empty) error = nil, want non-nil error")
	}
}

func TestVerifyArchiveStableFailureCode(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	entries := []ArchiveEntry{
		{Path: "logs/api.log", Kind: "log", Bytes: []byte("hello")},
	}
	var buf bytes.Buffer
	if _, err := WriteDeterministicArchive(&buf, "bundle-001", createdAt, Selector{Logs: true}, entries); err != nil {
		t.Fatalf("WriteDeterministicArchive() error = %v", err)
	}
	raw := buf.Bytes()
	idx := bytes.Index(raw, []byte("hello"))
	if idx < 0 {
		t.Fatal("could not locate payload byte to tamper with")
	}
	raw[idx] = 'H'
	err := VerifyArchive(raw)
	if err == nil {
		t.Fatal("VerifyArchive() error = nil, want failure")
	}
	// Stable failure: the structured error must carry SUPPORT_BUNDLE_VERIFY_FAILED.
	if got := err.Error(); !strings.Contains(got, "SUPPORT_BUNDLE_VERIFY_FAILED") && !strings.Contains(got, "support bundle archive") {
		t.Fatalf("VerifyArchive() error = %v, want stable verification failure code", err)
	}
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}
