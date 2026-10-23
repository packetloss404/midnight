package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// ChecksumPending records that Phase 0 planned an artifact but did not read it.
	ChecksumPending = "sha256:pending"

	PassphraseStatusProvided = "provided"
	PassphraseStatusMissing  = "missing"

	exportEntryDatabaseDump     = "database-dump"
	exportEntryAppData          = "app-data"
	exportEntryRegistryMetadata = "registry-metadata"
	exportEntryConfigSnapshot   = "config-snapshot"
	exportEntrySupportRefs      = "support-references"
)

// ExportManifest records the planned contents of a backup export without doing I/O.
type ExportManifest struct {
	ID                    string        `json:"id"`
	CreatedAt             time.Time     `json:"createdAt"`
	Entries               []ExportEntry `json:"entries"`
	TotalBytes            int64         `json:"totalBytes"`
	PassphraseRequired    bool          `json:"passphraseRequired"`
	PassphraseStatus      string        `json:"passphraseStatus"`
	SupportReferenceCount int           `json:"supportReferenceCount"`
}

// ExportEntry describes one planned backup export artifact.
type ExportEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	DataClass string `json:"dataClass,omitempty"`
	Bytes     int64  `json:"bytes"`
	Checksum  string `json:"checksum"`
}

// ExportManifestInput is the operator-provided dry-run metadata for an export manifest.
type ExportManifestInput struct {
	ID                    string
	CreatedAt             time.Time
	DatabaseDumpBytes     int64
	AppDataBytes          int64
	RegistryMetadataBytes int64
	ConfigSnapshotBytes   int64
	SupportReferenceBytes int64
	SupportReferenceCount int
	OperatorPassphrase    string
}

// BuildExportManifest returns the deterministic Phase 0 backup export manifest.
func BuildExportManifest(input ExportManifestInput) (ExportManifest, error) {
	manifest := ExportManifest{
		ID:                    input.ID,
		CreatedAt:             input.CreatedAt,
		PassphraseRequired:    true,
		PassphraseStatus:      passphraseStatus(input.OperatorPassphrase),
		SupportReferenceCount: input.SupportReferenceCount,
		Entries: []ExportEntry{
			{Name: exportEntryDatabaseDump, Path: "database/postgres.dump", Kind: "database", DataClass: "database", Bytes: input.DatabaseDumpBytes, Checksum: ChecksumPending},
			{Name: exportEntryAppData, Path: "app/data.tar", Kind: "app", DataClass: "app", Bytes: input.AppDataBytes, Checksum: ChecksumPending},
			{Name: exportEntryRegistryMetadata, Path: "platform/registry/metadata.json", Kind: "registry", DataClass: "platform", Bytes: input.RegistryMetadataBytes, Checksum: ChecksumPending},
			{Name: exportEntryConfigSnapshot, Path: "platform/config/midnight-config.tar", Kind: "config", DataClass: "platform", Bytes: input.ConfigSnapshotBytes, Checksum: ChecksumPending},
			{Name: exportEntrySupportRefs, Path: "platform/support/references.json", Kind: "support", DataClass: "platform", Bytes: input.SupportReferenceBytes, Checksum: ChecksumPending},
		},
	}
	for _, entry := range manifest.Entries {
		manifest.TotalBytes += entry.Bytes
	}
	if err := manifest.Validate(); err != nil {
		return ExportManifest{}, err
	}
	return manifest, nil
}

// MarshalExportManifestJSON validates and renders stable, human-readable manifest JSON.
func MarshalExportManifestJSON(manifest ExportManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(manifest, "", "  ")
}

// Validate checks that the export manifest is complete and internally consistent.
func (m ExportManifest) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("backup export manifest id is required")
	}
	if m.CreatedAt.IsZero() {
		return errors.New("backup export manifest createdAt is required")
	}
	if !m.PassphraseRequired {
		return errors.New("backup export manifest passphrase requirement must be recorded")
	}
	if m.PassphraseStatus != PassphraseStatusProvided && m.PassphraseStatus != PassphraseStatusMissing {
		return errors.New("backup export manifest passphrase status is invalid")
	}
	if m.SupportReferenceCount < 0 {
		return errors.New("backup export manifest support reference count must be non-negative")
	}
	if len(m.Entries) != 4 && len(m.Entries) != 5 {
		return errors.New("backup export manifest must include deterministic database, app, and platform entries")
	}
	wantNames := expectedExportEntryNames(len(m.Entries))
	wantTotal := int64(0)
	for i, entry := range m.Entries {
		if entry.Name != wantNames[i] {
			return fmt.Errorf("entries[%d]: unexpected backup export entry %q", i, entry.Name)
		}
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("entries[%d]: %w", i, err)
		}
		wantTotal += entry.Bytes
	}
	if m.TotalBytes != wantTotal {
		return errors.New("backup export manifest total bytes must equal entry bytes")
	}
	return nil
}

func expectedExportEntryNames(count int) []string {
	if count == 4 {
		return []string{exportEntryDatabaseDump, exportEntryRegistryMetadata, exportEntryConfigSnapshot, exportEntrySupportRefs}
	}
	return []string{exportEntryDatabaseDump, exportEntryAppData, exportEntryRegistryMetadata, exportEntryConfigSnapshot, exportEntrySupportRefs}
}

// Validate checks that an export entry is deterministic and safe to render.
func (e ExportEntry) Validate() error {
	if strings.TrimSpace(e.Name) == "" {
		return errors.New("entry name is required")
	}
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("entry path is required")
	}
	if strings.HasPrefix(e.Path, "/") || strings.Contains(e.Path, "..") || strings.Contains(e.Path, "\\") {
		return errors.New("entry path must be a safe relative slash path")
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("entry kind is required")
	}
	if e.DataClass != "" {
		switch e.DataClass {
		case "app", "database", "platform":
		default:
			return errors.New("entry dataClass is invalid")
		}
	}
	if e.Bytes < 0 {
		return errors.New("entry bytes must be non-negative")
	}
	if e.Checksum != ChecksumPending {
		return errors.New("entry checksum must use the Phase 0 pending placeholder")
	}
	return nil
}

func passphraseStatus(passphrase string) string {
	if strings.TrimSpace(passphrase) == "" {
		return PassphraseStatusMissing
	}
	return PassphraseStatusProvided
}
