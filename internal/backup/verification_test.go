package backup

import (
	"errors"
	"testing"
)

func TestVerifyArtifactChecksumsAllMatch(t *testing.T) {
	report, err := VerifyArtifactChecksums([]ArtifactChecksum{
		{Artifact: "database-dump", ExpectedSHA256: "aaa", ObservedSHA256: "aaa"},
		{Artifact: "config-snapshot", ExpectedSHA256: "bbb", ObservedSHA256: "bbb"},
	})
	if err != nil {
		t.Fatalf("VerifyArtifactChecksums() error = %v", err)
	}
	if report.FailureCode != "" || report.Verified != 2 || report.Mismatched != 0 {
		t.Fatalf("VerifyArtifactChecksums() = %+v, want all-verified report", report)
	}
}

func TestVerifyArtifactChecksumsRecordsPerArtifactMismatches(t *testing.T) {
	report, err := VerifyArtifactChecksums([]ArtifactChecksum{
		{Artifact: "database-dump", ExpectedSHA256: "aaa", ObservedSHA256: "ZZZ"},
		{Artifact: "registry-metadata", ExpectedSHA256: "bbb", ObservedSHA256: ""},
		{Artifact: "config-snapshot", ExpectedSHA256: "ccc", ObservedSHA256: "ccc"},
	})
	if err != nil {
		t.Fatalf("VerifyArtifactChecksums() error = %v", err)
	}
	if report.FailureCode != FailureCodeBackupVerifyChecksumMismatch {
		t.Fatalf("VerifyArtifactChecksums() FailureCode = %q, want %q", report.FailureCode, FailureCodeBackupVerifyChecksumMismatch)
	}
	if report.Mismatched != 2 || report.Verified != 1 {
		t.Fatalf("VerifyArtifactChecksums() counts = %+v, want 2 mismatched 1 verified", report)
	}
	if report.Mismatches[0].Artifact != "database-dump" || report.Mismatches[0].Reason != "checksum-mismatch" {
		t.Fatalf("VerifyArtifactChecksums() mismatches[0] = %+v, want database-dump checksum-mismatch", report.Mismatches[0])
	}
	if report.Mismatches[1].Artifact != "registry-metadata" || report.Mismatches[1].Reason != "not-computed" {
		t.Fatalf("VerifyArtifactChecksums() mismatches[1] = %+v, want registry-metadata not-computed", report.Mismatches[1])
	}
}

func TestVerifyArtifactChecksumsRejectsMissingMetadata(t *testing.T) {
	cases := []ArtifactChecksum{
		{Artifact: "", ExpectedSHA256: "aaa", ObservedSHA256: "aaa"},
		{Artifact: "database-dump", ExpectedSHA256: "", ObservedSHA256: "aaa"},
	}
	for i, c := range cases {
		if _, err := VerifyArtifactChecksums([]ArtifactChecksum{c}); err == nil {
			t.Fatalf("VerifyArtifactChecksums(case %d) error = nil, want validation error", i)
		}
	}
}

func TestVerifyArtifactChecksumsStrictReturnsSentinelOnMismatch(t *testing.T) {
	_, err := VerifyArtifactChecksumsStrict([]ArtifactChecksum{
		{Artifact: "database-dump", ExpectedSHA256: "aaa", ObservedSHA256: "BBB"},
	})
	if err == nil {
		t.Fatal("VerifyArtifactChecksumsStrict() error = nil, want sentinel error")
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("VerifyArtifactChecksumsStrict() error = %v, want ErrChecksumMismatch", err)
	}
	var ce *ChecksumMismatchError
	if !errors.As(err, &ce) {
		t.Fatalf("VerifyArtifactChecksumsStrict() error = %v, want *ChecksumMismatchError", err)
	}
	if ce.Report.FailureCode != FailureCodeBackupVerifyChecksumMismatch {
		t.Fatalf("ChecksumMismatchError.Report.FailureCode = %q, want %q", ce.Report.FailureCode, FailureCodeBackupVerifyChecksumMismatch)
	}
}

func TestVerificationReportToChecksumSummaryFails(t *testing.T) {
	report, err := VerifyArtifactChecksums([]ArtifactChecksum{
		{Artifact: "database-dump", ExpectedSHA256: "aaa", ObservedSHA256: "ZZZ"},
	})
	if err != nil {
		t.Fatalf("VerifyArtifactChecksums() error = %v", err)
	}
	summary := report.ToChecksumVerificationSummary()
	if summary.OverallState != RestoreStepFailed || summary.Failed != 1 {
		t.Fatalf("ToChecksumVerificationSummary() = %+v, want failed summary", summary)
	}
	if summary.FailedReasons[FailureCodeBackupVerifyChecksumMismatch] != 1 {
		t.Fatalf("ToChecksumVerificationSummary() FailedReasons = %+v, want failure-code reason", summary.FailedReasons)
	}
}

func TestVerificationReportToChecksumSummarySucceeds(t *testing.T) {
	report, err := VerifyArtifactChecksums([]ArtifactChecksum{
		{Artifact: "database-dump", ExpectedSHA256: "aaa", ObservedSHA256: "aaa"},
	})
	if err != nil {
		t.Fatalf("VerifyArtifactChecksums() error = %v", err)
	}
	summary := report.ToChecksumVerificationSummary()
	if summary.OverallState != RestoreStepSucceeded || summary.Verified != 1 {
		t.Fatalf("ToChecksumVerificationSummary() = %+v, want succeeded summary", summary)
	}
}
