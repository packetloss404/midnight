package backup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewLocalDestinationRequiresRoot(t *testing.T) {
	if _, err := NewLocalDestination(" "); err == nil {
		t.Fatal("NewLocalDestination() error = nil, want root validation error")
	}
}

func TestLocalDestinationPersistsObjects(t *testing.T) {
	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalDestination() error = %v", err)
	}

	object, err := destination.Put(context.Background(), "runs/backup-1/manifest.json", strings.NewReader("data"))
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if object.Key != "runs/backup-1/manifest.json" || object.Bytes != 4 || object.SHA256 == "" {
		t.Fatalf("Put() object = %#v, want key, byte count, and checksum", object)
	}

	reader, gotObject, err := destination.Get(context.Background(), "runs/backup-1/manifest.json")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if string(body) != "data" || gotObject.Bytes != 4 {
		t.Fatalf("Get() body=%q object=%#v, want persisted body", body, gotObject)
	}
	if _, err := destination.Put(context.Background(), "runs-extra/manifest.json", strings.NewReader("other")); err != nil {
		t.Fatalf("Put() second object error = %v", err)
	}

	objects, err := destination.List(context.Background(), "runs/")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(objects) != 1 || objects[0].Key != "runs/backup-1/manifest.json" {
		t.Fatalf("List() = %#v, want persisted object", objects)
	}
}

func TestLocalDestinationRejectsEscapingKeys(t *testing.T) {
	destination, err := NewLocalDestination(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalDestination() error = %v", err)
	}
	if _, err := destination.Put(context.Background(), "../escape", strings.NewReader("data")); err == nil {
		t.Fatal("Put() accepted escaping key")
	}
}

func TestLocalDestinationPutCreatesPrivateDirectories(t *testing.T) {
	root := t.TempDir()
	destination, err := NewLocalDestination(root)
	if err != nil {
		t.Fatalf("NewLocalDestination() error = %v", err)
	}
	if _, err := destination.Put(context.Background(), "runs/backup-2/manifest.json", strings.NewReader("data")); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "runs", "backup-2"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("backup run path is not a directory")
	}
}

func TestParseLocalURI(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "local host path", raw: "local://backups/nightly", want: filepath.Join("backups", "nightly")},
		{name: "local absolute path", raw: "local:///var/lib/midnight/backups", want: filepath.FromSlash("/var/lib/midnight/backups")},
		{name: "file path", raw: "file:///var/lib/midnight/backups", want: filepath.FromSlash("/var/lib/midnight/backups")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLocalURI(tt.raw)
			if err != nil {
				t.Fatalf("ParseLocalURI() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ParseLocalURI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLocalURIRejectsInvalidURI(t *testing.T) {
	tests := []string{"", "s3://bucket/path", "local://", "file://", "local://backups?token=secret"}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseLocalURI(raw); err == nil {
				t.Fatal("ParseLocalURI() error = nil, want validation error")
			}
		})
	}
}

func TestPlatformBackupArtifactsIncludesRequiredSources(t *testing.T) {
	artifacts := PlatformBackupArtifacts()
	want := map[string]struct{}{
		"postgres":          {},
		"midnight-config":   {},
		"secrets-key":       {},
		"registry-metadata": {},
		"acme-state":        {},
	}

	if len(artifacts) != len(want) {
		t.Fatalf("PlatformBackupArtifacts() returned %d artifacts, want %d", len(artifacts), len(want))
	}
	for _, artifact := range artifacts {
		if _, ok := want[artifact.Name]; !ok {
			t.Fatalf("PlatformBackupArtifacts() includes unexpected artifact %+v", artifact)
		}
		if artifact.Source == "" || artifact.Destination == "" || !artifact.Required {
			t.Fatalf("PlatformBackupArtifacts() artifact = %+v, want required source and destination", artifact)
		}
		delete(want, artifact.Name)
	}
	if len(want) != 0 {
		t.Fatalf("PlatformBackupArtifacts() missing artifacts: %+v", want)
	}
}

func TestBackupPlanValidateRequiresOperatorPassphraseForSecretsKey(t *testing.T) {
	plan := BackupPlan{Artifacts: PlatformBackupArtifacts()}
	if err := plan.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want passphrase validation error")
	}

	plan.OperatorPassphrase = "operator-confirmed"
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestBackupPlanValidateAllowsPlanWithoutSecretsKey(t *testing.T) {
	plan := BackupPlan{Artifacts: []PlanArtifact{{Name: "postgres", Source: "postgres", Destination: "postgres.dump", Required: true}}}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRestoreStepValidate(t *testing.T) {
	step := RestoreStep{Name: "verify-checksum", State: RestoreStepRunning}
	if err := step.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRestoreStepValidateRejectsInvalidState(t *testing.T) {
	step := RestoreStep{Name: "verify-checksum", State: RestoreStepState("done")}
	if err := step.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want invalid state error")
	}
}

func TestRestoreStepValidateRequiresFailedReasonCode(t *testing.T) {
	step := RestoreStep{Name: "verify-checksum", State: RestoreStepFailed}
	if err := step.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want failed reason code error")
	}
}

func TestRestoreSmokeProbeResultValidate(t *testing.T) {
	result := RestoreSmokeProbeResult{
		Name:      "restore-smoke-read",
		State:     RestoreStepSucceeded,
		CheckedAt: time.Now(),
		Object:    Object{Key: "postgres.dump", Bytes: 1024, SHA256: "abc123"},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRestoreSmokeProbeResultValidateRequiresCheckedAt(t *testing.T) {
	result := RestoreSmokeProbeResult{Name: "restore-smoke-read", State: RestoreStepSucceeded, Object: Object{Key: "postgres.dump"}}
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want checkedAt validation error")
	}
}

func TestRestoreSmokeProbeResultValidateRequiresSucceededObjectKey(t *testing.T) {
	result := RestoreSmokeProbeResult{Name: "restore-smoke-read", State: RestoreStepSucceeded, CheckedAt: time.Now()}
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want object key validation error")
	}
}

func TestSummarizeVerificationResults(t *testing.T) {
	checkedAt := time.Now()
	summary, err := SummarizeVerificationResults([]RestoreSmokeProbeResult{
		{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: checkedAt, Object: Object{Key: "postgres.dump", Bytes: 1024}},
		{Name: "read-config", State: RestoreStepFailed, CheckedAt: checkedAt, ReasonCode: "checksum-mismatch", Message: "sha256 mismatch"},
		{Name: "read-acme", State: RestoreStepSkipped, CheckedAt: checkedAt},
	})
	if err != nil {
		t.Fatalf("SummarizeVerificationResults() error = %v", err)
	}

	if summary.Total != 3 || summary.Succeeded != 1 || summary.Failed != 1 || summary.Skipped != 1 {
		t.Fatalf("SummarizeVerificationResults() = %+v, want counted result states", summary)
	}
	if summary.OverallState != RestoreStepFailed || summary.FailedReasons["checksum-mismatch"] != 1 {
		t.Fatalf("SummarizeVerificationResults() = %+v, want failed overall state and reason count", summary)
	}
}

func TestSummarizeVerificationResultsRejectsInvalidResult(t *testing.T) {
	_, err := SummarizeVerificationResults([]RestoreSmokeProbeResult{{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: time.Now()}})
	if err == nil {
		t.Fatal("SummarizeVerificationResults() error = nil, want invalid result error")
	}
}
