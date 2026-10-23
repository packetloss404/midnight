package backup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildExportManifestUsesDeterministicEntriesAndTotals(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildExportManifest(ExportManifestInput{
		ID:                    "backup-001",
		CreatedAt:             createdAt,
		DatabaseDumpBytes:     100,
		AppDataBytes:          7,
		RegistryMetadataBytes: 20,
		ConfigSnapshotBytes:   30,
		SupportReferenceBytes: 4,
		SupportReferenceCount: 2,
		OperatorPassphrase:    "operator-confirmed",
	})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}

	wantEntries := []ExportEntry{
		{Name: "database-dump", Path: "database/postgres.dump", Kind: "database", DataClass: "database", Bytes: 100, Checksum: ChecksumPending},
		{Name: "app-data", Path: "app/data.tar", Kind: "app", DataClass: "app", Bytes: 7, Checksum: ChecksumPending},
		{Name: "registry-metadata", Path: "platform/registry/metadata.json", Kind: "registry", DataClass: "platform", Bytes: 20, Checksum: ChecksumPending},
		{Name: "config-snapshot", Path: "platform/config/midnight-config.tar", Kind: "config", DataClass: "platform", Bytes: 30, Checksum: ChecksumPending},
		{Name: "support-references", Path: "platform/support/references.json", Kind: "support", DataClass: "platform", Bytes: 4, Checksum: ChecksumPending},
	}
	if len(manifest.Entries) != len(wantEntries) {
		t.Fatalf("BuildExportManifest() entries = %+v, want %+v", manifest.Entries, wantEntries)
	}
	for i := range wantEntries {
		if manifest.Entries[i] != wantEntries[i] {
			t.Fatalf("BuildExportManifest() entries[%d] = %+v, want %+v", i, manifest.Entries[i], wantEntries[i])
		}
	}
	if manifest.TotalBytes != 161 {
		t.Fatalf("BuildExportManifest() totalBytes = %d, want 161", manifest.TotalBytes)
	}
	if !manifest.PassphraseRequired || manifest.PassphraseStatus != PassphraseStatusProvided {
		t.Fatalf("BuildExportManifest() passphrase = required:%t status:%q, want provided", manifest.PassphraseRequired, manifest.PassphraseStatus)
	}
	if manifest.SupportReferenceCount != 2 {
		t.Fatalf("BuildExportManifest() supportReferenceCount = %d, want 2", manifest.SupportReferenceCount)
	}
}

func TestBuildExportManifestRecordsMissingPassphraseStatus(t *testing.T) {
	manifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	if manifest.PassphraseStatus != PassphraseStatusMissing {
		t.Fatalf("BuildExportManifest() passphraseStatus = %q, want %q", manifest.PassphraseStatus, PassphraseStatusMissing)
	}
}

func TestBuildExportManifestRejectsNegativeSizes(t *testing.T) {
	_, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: time.Now(), ConfigSnapshotBytes: -1})
	if err == nil {
		t.Fatal("BuildExportManifest() error = nil, want negative size validation error")
	}
}

func TestExportManifestValidateRejectsChecksumWithoutPendingPlaceholder(t *testing.T) {
	manifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	manifest.Entries[0].Checksum = "abc123"

	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want checksum placeholder validation error")
	}
}

func TestExportManifestValidateRejectsTotalMismatch(t *testing.T) {
	manifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: time.Now(), DatabaseDumpBytes: 10})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	manifest.TotalBytes = 9

	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want total byte mismatch error")
	}
}

func TestExportManifestValidateRejectsEntryOrderChange(t *testing.T) {
	manifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	manifest.Entries[0], manifest.Entries[1] = manifest.Entries[1], manifest.Entries[0]

	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want deterministic order validation error")
	}
}

func TestMarshalExportManifestJSONRendersManifest(t *testing.T) {
	createdAt := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	manifest, err := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, DatabaseDumpBytes: 1, OperatorPassphrase: "confirmed"})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}

	data, err := MarshalExportManifestJSON(manifest)
	if err != nil {
		t.Fatalf("MarshalExportManifestJSON() error = %v", err)
	}

	var got ExportManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalExportManifestJSON() returned invalid JSON: %v", err)
	}
	if got.ID != "backup-001" || got.TotalBytes != 1 || len(got.Entries) != 5 || got.Entries[0].Checksum != ChecksumPending {
		t.Fatalf("MarshalExportManifestJSON() manifest = %+v, want rendered export manifest", got)
	}
}
