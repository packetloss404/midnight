package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// SnapshotSchemaVersion is the stable schema version for doctor snapshots.
const SnapshotSchemaVersion = "midnight.dev/doctor-snapshot/v1alpha1"

// CheckCatalog is the stable JSON shape for the static doctor registry.
type CheckCatalog struct {
	Checks []CatalogCheck `json:"checks"`
}

// CatalogCheck exposes doctor check metadata without inspecting the live host.
type CatalogCheck struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Severity    string          `json:"severity"`
	DocsURL     string          `json:"docs_url"`
	SafeFix     SafeFixMetadata `json:"safe_fix"`
}

// ReportSeverity is the process-level severity derived from doctor results.
type ReportSeverity string

const (
	ReportSeverityHealthy     ReportSeverity = "healthy"
	ReportSeverityWarn        ReportSeverity = "warn"
	ReportSeverityFail        ReportSeverity = "fail"
	ReportSeverityConfigError ReportSeverity = "config_error"
)

// Snapshot is a JSON-serializable doctor runner output.
//
// SchemaVersion is set to SnapshotSchemaVersion by NewSnapshot. The remaining
// fields use stable, deterministic key ordering: status, severity, exit_code,
// checks. Each check entry uses id, status, reason, docs_url ordering.
type Snapshot struct {
	SchemaVersion string          `json:"schema_version"`
	Status        Status          `json:"status"`
	Severity      ReportSeverity  `json:"severity"`
	ExitCode      int             `json:"exit_code"`
	Checks        []SnapshotCheck `json:"checks"`
}

// SnapshotCheck is the stable public shape for one doctor check result.
type SnapshotCheck struct {
	ID      string `json:"id"`
	Status  Status `json:"status"`
	Reason  string `json:"reason,omitempty"`
	DocsURL string `json:"docs_url,omitempty"`
}

// NewCheckCatalog converts check metadata to a JSON-ready catalog.
func NewCheckCatalog(checks []Check) (CheckCatalog, error) {
	if err := ValidateChecks(checks); err != nil {
		return CheckCatalog{}, err
	}
	catalog := CheckCatalog{Checks: make([]CatalogCheck, 0, len(checks))}
	for _, check := range checks {
		catalog.Checks = append(catalog.Checks, CatalogCheck{
			ID:          check.ID,
			Description: check.Description,
			Severity:    check.Severity,
			DocsURL:     check.DocsURL,
			SafeFix:     check.SafeFix,
		})
	}
	return catalog, nil
}

// MarshalCheckCatalogJSON emits the stable JSON representation of doctor check metadata.
func MarshalCheckCatalogJSON(catalog CheckCatalog) ([]byte, error) {
	return json.Marshal(catalog)
}

// RunSnapshot executes doctor checks and converts their output to a JSON-ready snapshot.
func RunSnapshot(ctx context.Context, checks []Check) (Snapshot, error) {
	results, err := RunChecks(ctx, checks)
	if err != nil {
		return Snapshot{}, err
	}
	return NewSnapshot(results), nil
}

// NewSnapshot converts internal doctor results to a JSON-serializable aggregate shape.
func NewSnapshot(results []Result) Snapshot {
	snapshot := Snapshot{SchemaVersion: SnapshotSchemaVersion, Status: StatusPass, Checks: make([]SnapshotCheck, 0, len(results))}
	for _, result := range results {
		snapshot.Checks = append(snapshot.Checks, SnapshotCheck{ID: result.ID, Status: result.Status, Reason: resultReason(result), DocsURL: result.DocsURL})
		snapshot.Status = aggregateStatus(snapshot.Status, result.Status)
	}
	if len(results) == 0 {
		snapshot.Status = StatusSkip
	}
	snapshot.Severity = SeverityForStatus(snapshot.Status)
	snapshot.ExitCode = ExitCodeForStatus(snapshot.Status)
	return snapshot
}

// NewConfigErrorSnapshot builds a snapshot for invalid caller-supplied configuration.
func NewConfigErrorSnapshot(err error) Snapshot {
	return NewSnapshot([]Result{{ID: "DOC-CONFIG-001", Status: StatusConfigError, Err: err}})
}

// MarshalSnapshotJSON emits the stable JSON representation used by doctor callers.
// The schema version is stamped automatically when missing.
func MarshalSnapshotJSON(snapshot Snapshot) ([]byte, error) {
	if snapshot.SchemaVersion == "" {
		snapshot.SchemaVersion = SnapshotSchemaVersion
	}
	return json.Marshal(snapshot)
}

// UnmarshalSnapshotJSON reads the stable JSON representation used by doctor callers.
// It rejects unknown fields, trailing JSON, and snapshots whose schema_version is
// not SnapshotSchemaVersion.
func UnmarshalSnapshotJSON(data []byte) (Snapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("doctor snapshot JSON must contain one document")
		}
		return Snapshot{}, err
	}
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		return Snapshot{}, fmt.Errorf("doctor snapshot schema_version %q is invalid", snapshot.SchemaVersion)
	}
	return snapshot, nil
}

// RoundTripSnapshotJSON verifies that a snapshot survives the public JSON shape.
func RoundTripSnapshotJSON(snapshot Snapshot) (Snapshot, error) {
	if snapshot.SchemaVersion == "" {
		snapshot.SchemaVersion = SnapshotSchemaVersion
	}
	data, err := MarshalSnapshotJSON(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	return UnmarshalSnapshotJSON(data)
}

// MarshalSnapshot is the canonical Phase 1.5 snapshot serializer. Its bytes are
// identical to MarshalSnapshotJSON; the alias exists so callers can use the
// shorter name without keeping track of the JSON suffix.
func MarshalSnapshot(snapshot Snapshot) ([]byte, error) {
	return MarshalSnapshotJSON(snapshot)
}

// UnmarshalSnapshot is the canonical Phase 1.5 snapshot deserializer.
func UnmarshalSnapshot(data []byte) (Snapshot, error) {
	return UnmarshalSnapshotJSON(data)
}

// SeverityForStatus maps an aggregate status to process-level severity.
func SeverityForStatus(status Status) ReportSeverity {
	switch status {
	case StatusConfigError:
		return ReportSeverityConfigError
	case StatusFail:
		return ReportSeverityFail
	case StatusWarn:
		return ReportSeverityWarn
	default:
		return ReportSeverityHealthy
	}
}

// ExitCodeForStatus maps aggregate status to the installer/doctor contract.
func ExitCodeForStatus(status Status) int {
	switch status {
	case StatusConfigError:
		return 3
	case StatusFail:
		return 2
	case StatusWarn:
		return 1
	default:
		return 0
	}
}

func resultReason(result Result) string {
	if result.Message != "" {
		return result.Message
	}
	if result.Err != nil {
		return result.Err.Error()
	}
	return ""
}

func aggregateStatus(current, next Status) Status {
	if current == StatusConfigError || next == StatusConfigError {
		return StatusConfigError
	}
	if current == StatusFail || next == StatusFail {
		return StatusFail
	}
	if current == StatusWarn || next == StatusWarn {
		return StatusWarn
	}
	if current == StatusSkip || next == StatusSkip {
		return StatusSkip
	}
	return StatusPass
}
