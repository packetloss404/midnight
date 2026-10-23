package backup

import (
	"testing"
	"time"
)

func TestReviewBackupReadinessExtendedReady(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, err := BuildExportManifest(ExportManifestInput{
		ID: "backup-001", CreatedAt: createdAt, DatabaseDumpBytes: 100, OperatorPassphrase: "confirmed",
	})
	if err != nil {
		t.Fatalf("BuildExportManifest() error = %v", err)
	}
	envFor := func(name string) (string, bool) {
		switch name {
		case "AWS_ACCESS_KEY_ID":
			return "AKIAEXAMPLEEXAMPLE12", true
		case "AWS_SECRET_ACCESS_KEY":
			return "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", true
		}
		return "", false
	}
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	}, BackupReadinessExtension{
		AllowedSchemes:        []string{"local", "file"},
		RequiredCredentialEnv: []CredentialEnvSpec{{Name: "AWS_ACCESS_KEY_ID", Format: "prefix:AKIA"}, {Name: "AWS_SECRET_ACCESS_KEY", Format: "minlength:40"}},
		KEKID:                 "kek-platform-001",
		DEKID:                 "dek-backup-001",
		EnforceFreeSpace:      true,
		FreeSpaceBytes:        10_000,
		LookupEnv:             envFor,
	})

	if summary.Status != BackupReadinessStatusReady {
		t.Fatalf("ReviewBackupReadinessExtended() status = %q, want %q; issues=%+v", summary.Status, BackupReadinessStatusReady, summary.Issues)
	}
	if !summary.Extension.SchemeValid || summary.Extension.Scheme != "local" {
		t.Fatalf("ReviewBackupReadinessExtended() extension scheme = %+v, want valid local scheme", summary.Extension)
	}
	if !summary.Extension.KEKDEKSeparationOK {
		t.Fatalf("ReviewBackupReadinessExtended() KEK/DEK separation = %+v, want ok", summary.Extension)
	}
	if !summary.Extension.FreeSpaceSufficient {
		t.Fatalf("ReviewBackupReadinessExtended() free space = %+v, want sufficient", summary.Extension)
	}
}

func TestReviewBackupReadinessExtendedBlocksUnsupportedScheme(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, _ := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "ftp://attacker.example/backups",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	}, BackupReadinessExtension{
		AllowedSchemes: []string{"local", "file", "s3", "sftp"},
		KEKID:          "kek-001",
		DEKID:          "dek-001",
	})
	if summary.Status != BackupReadinessStatusBlocked {
		t.Fatalf("ReviewBackupReadinessExtended() status = %q, want blocked", summary.Status)
	}
	if !hasIssueCode(summary.Issues, BackupReadinessIssueDestinationSchemeUnsupported) {
		t.Fatalf("ReviewBackupReadinessExtended() issues = %+v, want destination-scheme-unsupported", summary.Issues)
	}
}

func TestReviewBackupReadinessExtendedBlocksMissingCredentials(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, _ := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})
	envFor := func(name string) (string, bool) { return "", false }
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	}, BackupReadinessExtension{
		RequiredCredentialEnv: []CredentialEnvSpec{{Name: "S3_BUCKET", Format: "any"}},
		KEKID:                 "kek-001",
		DEKID:                 "dek-001",
		LookupEnv:             envFor,
	})
	if !hasIssueCode(summary.Issues, BackupReadinessIssueCredentialMissing) {
		t.Fatalf("ReviewBackupReadinessExtended() issues = %+v, want credential-missing", summary.Issues)
	}
}

func TestReviewBackupReadinessExtendedRejectsKEKDEKCollision(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, _ := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	}, BackupReadinessExtension{KEKID: "same-id", DEKID: "same-id"})
	if !hasIssueCode(summary.Issues, BackupReadinessIssueKEKDEKCollision) {
		t.Fatalf("ReviewBackupReadinessExtended() issues = %+v, want kek-dek-collision", summary.Issues)
	}
}

func TestReviewBackupReadinessExtendedRejectsInsufficientFreeSpace(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, _ := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, DatabaseDumpBytes: 10_000, OperatorPassphrase: "confirmed"})
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 10_000}},
		},
	}, BackupReadinessExtension{
		KEKID:            "kek-001",
		DEKID:            "dek-001",
		EnforceFreeSpace: true,
		FreeSpaceBytes:   1_000,
	})
	if !hasIssueCode(summary.Issues, BackupReadinessIssueFreeSpaceInsufficient) {
		t.Fatalf("ReviewBackupReadinessExtended() issues = %+v, want free-space-insufficient", summary.Issues)
	}
	if summary.Status != BackupReadinessStatusBlocked {
		t.Fatalf("ReviewBackupReadinessExtended() status = %q, want blocked", summary.Status)
	}
}

func TestReviewBackupReadinessExtendedRequiresKEKMetadata(t *testing.T) {
	createdAt := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	manifest, _ := BuildExportManifest(ExportManifestInput{ID: "backup-001", CreatedAt: createdAt, OperatorPassphrase: "confirmed"})
	summary := ReviewBackupReadinessExtended(BackupReadinessInput{
		Manifest:       manifest,
		DestinationURI: "local://backups/nightly",
		VerificationResults: []RestoreSmokeProbeResult{
			{Name: "read-postgres", State: RestoreStepSucceeded, CheckedAt: createdAt, Object: Object{Key: "postgres.dump", Bytes: 100}},
		},
	}, BackupReadinessExtension{})
	if !hasIssueCode(summary.Issues, BackupReadinessIssueKEKMetadataMissing) {
		t.Fatalf("ReviewBackupReadinessExtended() issues = %+v, want kek-metadata-missing", summary.Issues)
	}
}

func hasIssueCode(issues []BackupReadinessIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
