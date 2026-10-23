package doctor

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestRunSnapshot(t *testing.T) {
	t.Parallel()

	snapshot, err := RunSnapshot(context.Background(), []Check{
		{ID: "DOC-ONE", Run: func(context.Context) Result { return Result{Status: StatusPass, Message: "ok"} }},
		{ID: "DOC-TWO", Run: func(context.Context) Result { return Result{Status: StatusWarn, Err: os.ErrPermission} }},
	})
	if err != nil {
		t.Fatalf("RunSnapshot() error = %v", err)
	}
	if snapshot.Status != StatusWarn {
		t.Fatalf("Status = %q, want %q", snapshot.Status, StatusWarn)
	}
	if snapshot.Severity != ReportSeverityWarn || snapshot.ExitCode != 1 {
		t.Fatalf("Severity/ExitCode = %q/%d, want warn/1", snapshot.Severity, snapshot.ExitCode)
	}
	if snapshot.Checks[0].Reason != "ok" || snapshot.Checks[1].Reason == "" {
		t.Fatalf("Checks = %+v, want message/error reasons", snapshot.Checks)
	}
	if _, err := MarshalSnapshotJSON(snapshot); err != nil {
		t.Fatalf("MarshalSnapshotJSON(snapshot) error = %v", err)
	}
	got, err := RoundTripSnapshotJSON(snapshot)
	if err != nil {
		t.Fatalf("RoundTripSnapshotJSON(snapshot) error = %v", err)
	}
	if got.Status != snapshot.Status || got.Severity != snapshot.Severity || got.ExitCode != snapshot.ExitCode {
		t.Fatalf("RoundTripSnapshotJSON(snapshot) = %+v, want status/severity/exit code from %+v", got, snapshot)
	}
	if _, err := json.Marshal(snapshot); err != nil {
		t.Fatalf("json.Marshal(snapshot) error = %v", err)
	}
}

func TestCheckCatalogJSON(t *testing.T) {
	t.Parallel()

	catalog, err := NewCheckCatalog(Checks())
	if err != nil {
		t.Fatalf("NewCheckCatalog() error = %v", err)
	}
	if len(catalog.Checks) != len(Checks()) {
		t.Fatalf("len(catalog.Checks) = %d, want %d", len(catalog.Checks), len(Checks()))
	}
	for _, check := range catalog.Checks {
		if check.DocsURL == "" {
			t.Fatalf("catalog check %s DocsURL is empty", check.ID)
		}
		if check.SafeFix.Available {
			t.Fatalf("catalog check %s SafeFix.Available = true, want false placeholder", check.ID)
		}
	}
	data, err := MarshalCheckCatalogJSON(catalog)
	if err != nil {
		t.Fatalf("MarshalCheckCatalogJSON() error = %v", err)
	}
	var decoded CheckCatalog
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(catalog) error = %v", err)
	}
	if len(decoded.Checks) != len(catalog.Checks) {
		t.Fatalf("decoded checks = %d, want %d", len(decoded.Checks), len(catalog.Checks))
	}
}

func TestNewCheckCatalogRejectsInvalidChecks(t *testing.T) {
	t.Parallel()

	_, err := NewCheckCatalog([]Check{{ID: "DOC-X"}, {ID: "DOC-X"}})
	if err == nil {
		t.Fatal("NewCheckCatalog() error = nil, want duplicate ID error")
	}
}

func TestRunSnapshotRejectsInvalidChecks(t *testing.T) {
	t.Parallel()

	_, err := RunSnapshot(context.Background(), []Check{{ID: "DOC-X"}, {ID: "DOC-X"}})
	if err == nil {
		t.Fatal("RunSnapshot() error = nil, want duplicate ID error")
	}
}

func TestNewConfigErrorSnapshot(t *testing.T) {
	t.Parallel()

	snapshot := NewConfigErrorSnapshot(os.ErrInvalid)
	if snapshot.Status != StatusConfigError {
		t.Fatalf("Status = %q, want %q", snapshot.Status, StatusConfigError)
	}
	if snapshot.Severity != ReportSeverityConfigError || snapshot.ExitCode != 3 {
		t.Fatalf("Severity/ExitCode = %q/%d, want config_error/3", snapshot.Severity, snapshot.ExitCode)
	}
}

func TestSnapshotSchemaVersionStamped(t *testing.T) {
	t.Parallel()

	snapshot := NewSnapshot([]Result{{ID: "DOC-OK", Status: StatusPass}})
	if snapshot.SchemaVersion != SnapshotSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", snapshot.SchemaVersion, SnapshotSchemaVersion)
	}
}

func TestMarshalUnmarshalSnapshotRoundTrip(t *testing.T) {
	t.Parallel()

	snapshot := NewSnapshot([]Result{
		{ID: "DOC-A", Status: StatusPass, Message: "ok"},
		{ID: "DOC-B", Status: StatusWarn, Message: "warn"},
	})

	data, err := MarshalSnapshot(snapshot)
	if err != nil {
		t.Fatalf("MarshalSnapshot() error = %v", err)
	}
	got, err := UnmarshalSnapshot(data)
	if err != nil {
		t.Fatalf("UnmarshalSnapshot() error = %v", err)
	}
	if got.SchemaVersion != SnapshotSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got.SchemaVersion, SnapshotSchemaVersion)
	}
	if got.Status != snapshot.Status || got.ExitCode != snapshot.ExitCode || len(got.Checks) != len(snapshot.Checks) {
		t.Fatalf("decoded snapshot drifted: %+v vs %+v", got, snapshot)
	}
}

func TestUnmarshalSnapshotRejectsBadSchema(t *testing.T) {
	t.Parallel()

	data := []byte(`{"schema_version":"wrong","status":"pass","severity":"healthy","exit_code":0,"checks":[]}`)
	if _, err := UnmarshalSnapshot(data); err == nil {
		t.Fatal("UnmarshalSnapshot(bad schema) error = nil, want schema error")
	}
}

func TestUnmarshalSnapshotRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	data := []byte(`{"schema_version":"midnight.dev/doctor-snapshot/v1alpha1","status":"pass","severity":"healthy","exit_code":0,"checks":[],"oops":"new"}`)
	if _, err := UnmarshalSnapshot(data); err == nil {
		t.Fatal("UnmarshalSnapshot(unknown field) error = nil, want strict-decode error")
	}
}

func TestDoctorExitCodeForStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status Status
		want   int
	}{
		{status: StatusPass, want: 0},
		{status: StatusSkip, want: 0},
		{status: StatusWarn, want: 1},
		{status: StatusFail, want: 2},
		{status: StatusConfigError, want: 3},
	}
	for _, tt := range tests {
		if got := ExitCodeForStatus(tt.status); got != tt.want {
			t.Fatalf("ExitCodeForStatus(%q) = %d, want %d", tt.status, got, tt.want)
		}
	}
}
