package backup

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReviewBackupReadinessReady(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest := mustBuildReadinessManifest(t, ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, DatabaseDumpBytes: 100, OperatorPassphrase: "confirmed"})

	summary := ReviewBackupReadiness(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	})

	if summary.Status != BackupReadinessStatusReady {
		t.Fatalf("ReviewBackupReadiness() status = %q, want %q; issues=%+v", summary.Status, BackupReadinessStatusReady, summary.Issues)
	}
	if !summary.ManifestValid || !summary.DestinationValid || summary.PassphraseStatus != PassphraseStatusProvided {
		t.Fatalf("ReviewBackupReadiness() = %+v, want valid manifest, destination, and passphrase", summary)
	}
	if summary.VerificationSummary.Total != 1 || summary.VerificationSummary.OverallState != RestoreStepSucceeded {
		t.Fatalf("ReviewBackupReadiness() verification = %+v, want succeeded summary", summary.VerificationSummary)
	}
}

func TestReviewBackupReadinessBlockedIssuesAreDeterministic(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest := mustBuildReadinessManifest(t, ExportManifestInput{ID: "backup-001", CreatedAt: createdAt})
	manifest.TotalBytes = 99

	summary := ReviewBackupReadiness(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "s3://bucket/backup",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepFailed, CheckedAt: createdAt, ReasonCode: "checksum-mismatch"},
		},
	})

	assertReadinessCodes(t, summary, []string{
		BackupReadinessIssueManifestInvalid,
		BackupReadinessIssueDestinationURIInvalid,
		BackupReadinessIssuePassphraseRequired,
		BackupReadinessIssueVerificationFailed,
	})
	if summary.Status != BackupReadinessStatusBlocked {
		t.Fatalf("ReviewBackupReadiness() status = %q, want %q", summary.Status, BackupReadinessStatusBlocked)
	}
}

func TestReviewBackupReadinessNeedsReviewForIncompleteVerification(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest := mustBuildReadinessManifest(t, ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})

	summary := ReviewBackupReadiness(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "file:///var/lib/midnight/backups",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepPending, CheckedAt: createdAt},
			{Name: "read-acme", State: RestoreStepSkipped, CheckedAt: createdAt},
		},
	})

	assertReadinessCodes(t, summary, []string{
		BackupReadinessIssueVerificationPending,
		BackupReadinessIssueVerificationSkipped,
	})
	if summary.Status != BackupReadinessStatusNeedsReview {
		t.Fatalf("ReviewBackupReadiness() status = %q, want %q", summary.Status, BackupReadinessStatusNeedsReview)
	}
}

func TestReviewBackupReadinessNeedsReviewForMissingVerification(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest := mustBuildReadinessManifest(t, ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})

	summary := ReviewBackupReadiness(BackupReadinessInput{Manifest: manifest, DestinationURI: "local://backups/nightly"})

	assertReadinessCodes(t, summary, []string{BackupReadinessIssueVerificationMissing})
	if summary.Status != BackupReadinessStatusNeedsReview {
		t.Fatalf("ReviewBackupReadiness() status = %q, want %q", summary.Status, BackupReadinessStatusNeedsReview)
	}
}

func TestReviewBackupReadinessReportsInvalidVerification(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest := mustBuildReadinessManifest(t, ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})

	summary := ReviewBackupReadiness(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt},
		},
	})

	assertReadinessCodes(t, summary, []string{BackupReadinessIssueVerificationInvalid})
	if summary.Status != BackupReadinessStatusBlocked {
		t.Fatalf("ReviewBackupReadiness() status = %q, want %q", summary.Status, BackupReadinessStatusBlocked)
	}
}

func TestMarshalBackupReadinessSummaryJSON(t *testing.T) {
	summary := BackupReadinessSummary{Status: BackupReadinessStatusNeedsReview, Issues: []BackupReadinessIssue{{Code: BackupReadinessIssueVerificationMissing, Status: BackupReadinessStatusNeedsReview}}}

	data, err := MarshalBackupReadinessSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalBackupReadinessSummaryJSON() error = %v", err)
	}

	var got BackupReadinessSummary
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("MarshalBackupReadinessSummaryJSON() returned invalid JSON: %v", err)
	}
	if got.Status != BackupReadinessStatusNeedsReview || len(got.Issues) != 1 || got.Issues[0].Code != BackupReadinessIssueVerificationMissing {
		t.Fatalf("MarshalBackupReadinessSummaryJSON() = %+v, want readiness summary", got)
	}
}

func mustBuildReadinessManifest(t *testing.T, input ExportManifestInput) ExportManifest {
	t.Helper()
	manifest, err := BuildExportManifest(input)
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	return manifest
}

func assertReadinessCodes(t *testing.T, summary BackupReadinessSummary, want []string) {
	t.Helper()
	if len(summary.Issues) != len(want) {
		t.Fatalf("ReviewBackupReadiness() issues = %+v, want codes %v", summary.Issues, want)
	}
	for i, code := range want {
		if summary.Issues[i].Code != code {
			t.Fatalf("ReviewBackupReadiness() issues[%d].Code = %q, want %q; issues=%+v", i, summary.Issues[i].Code, code, summary.Issues)
		}
	}
}
