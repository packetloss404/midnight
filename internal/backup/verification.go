package backup

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// FailureCodeBackupVerifyChecksumMismatch is the top-level failure code surfaced when at least
// one artifact's expected SHA-256 differs from the value observed at verification time.
const FailureCodeBackupVerifyChecksumMismatch = "BACKUP_VERIFY_CHECKSUM_MISMATCH"

// ArtifactChecksum carries one artifact's expected/observed checksum pair for verification.
type ArtifactChecksum struct {
	// Artifact is the artifact name, matching the export-manifest entry name.
	Artifact string
	// ExpectedSHA256 is the value recorded in the source-of-truth manifest.
	ExpectedSHA256 string
	// ObservedSHA256 is the value computed at verification time. Empty signals "not computed yet".
	ObservedSHA256 string
}

// ArtifactChecksumMismatch is one artifact's verification failure record.
type ArtifactChecksumMismatch struct {
	Artifact       string `json:"artifact"`
	ExpectedSHA256 string `json:"expectedSha256"`
	ObservedSHA256 string `json:"observedSha256"`
	Reason         string `json:"reason"`
}

// VerificationReport is the deterministic structured report returned by VerifyArtifactChecksums.
type VerificationReport struct {
	// FailureCode is FailureCodeBackupVerifyChecksumMismatch when any artifact failed; empty otherwise.
	FailureCode string `json:"failureCode,omitempty"`
	// Total is the number of artifacts inspected.
	Total int `json:"total"`
	// Verified is the number of artifacts whose observed checksum matched the expected value.
	Verified int `json:"verified"`
	// Mismatched is the number of artifacts whose checksum did not match or was missing.
	Mismatched int `json:"mismatched"`
	// Mismatches is the per-artifact breakdown for review surfaces.
	Mismatches []ArtifactChecksumMismatch `json:"mismatches,omitempty"`
}

// VerifyArtifactChecksums compares expected/observed checksums and returns a deterministic report.
//
// Behavior:
//   - An empty checksums slice returns a zero report with no failure code.
//   - A non-empty observed value that does not match the expected value yields a "mismatch" record.
//   - An empty observed value is treated as "not-computed" and surfaces as a mismatch.
//   - Any non-zero mismatch count sets FailureCode to BACKUP_VERIFY_CHECKSUM_MISMATCH.
//   - Mismatches are sorted by artifact name for deterministic JSON output.
func VerifyArtifactChecksums(checksums []ArtifactChecksum) (VerificationReport, error) {
	report := VerificationReport{Total: len(checksums)}
	for i, c := range checksums {
		if strings.TrimSpace(c.Artifact) == "" {
			return VerificationReport{}, fmt.Errorf("checksums[%d]: artifact name is required", i)
		}
		if strings.TrimSpace(c.ExpectedSHA256) == "" {
			return VerificationReport{}, fmt.Errorf("checksums[%d]: expectedSha256 is required", i)
		}
		switch {
		case c.ObservedSHA256 == "":
			report.Mismatches = append(report.Mismatches, ArtifactChecksumMismatch{
				Artifact: c.Artifact, ExpectedSHA256: c.ExpectedSHA256, ObservedSHA256: "", Reason: "not-computed",
			})
		case c.ObservedSHA256 != c.ExpectedSHA256:
			report.Mismatches = append(report.Mismatches, ArtifactChecksumMismatch{
				Artifact: c.Artifact, ExpectedSHA256: c.ExpectedSHA256, ObservedSHA256: c.ObservedSHA256, Reason: "checksum-mismatch",
			})
		default:
			report.Verified++
		}
	}
	sort.SliceStable(report.Mismatches, func(i, j int) bool { return report.Mismatches[i].Artifact < report.Mismatches[j].Artifact })
	report.Mismatched = len(report.Mismatches)
	if report.Mismatched > 0 {
		report.FailureCode = FailureCodeBackupVerifyChecksumMismatch
	}
	return report, nil
}

// ToChecksumVerificationSummary maps a VerificationReport into the existing release-evidence
// shape so it can be embedded in restore readiness without recomputation.
func (r VerificationReport) ToChecksumVerificationSummary() ChecksumVerificationSummary {
	summary := ChecksumVerificationSummary{
		Total:    r.Total,
		Verified: r.Verified,
		Failed:   r.Mismatched,
	}
	if r.Mismatched > 0 {
		summary.OverallState = RestoreStepFailed
		summary.FailedReasons = map[string]int{r.FailureCode: r.Mismatched}
	} else if r.Total > 0 {
		summary.OverallState = RestoreStepSucceeded
	}
	return summary
}

// ErrChecksumMismatch is returned by VerifyArtifactChecksumsStrict when at least one mismatch
// is detected. It wraps the structured report.
var ErrChecksumMismatch = errors.New(FailureCodeBackupVerifyChecksumMismatch)

// ChecksumMismatchError carries the structured report alongside ErrChecksumMismatch.
type ChecksumMismatchError struct {
	Report VerificationReport
}

// Error renders a deterministic single-line summary of the checksum failure.
func (e *ChecksumMismatchError) Error() string {
	parts := make([]string, 0, len(e.Report.Mismatches))
	for _, m := range e.Report.Mismatches {
		parts = append(parts, fmt.Sprintf("%s=%s", m.Artifact, m.Reason))
	}
	return fmt.Sprintf("%s: %s", e.Report.FailureCode, strings.Join(parts, ", "))
}

// Is reports that ChecksumMismatchError matches the ErrChecksumMismatch sentinel.
func (e *ChecksumMismatchError) Is(target error) bool {
	return target == ErrChecksumMismatch //nolint:errorlint // sentinel comparison is intentional
}

// VerifyArtifactChecksumsStrict is like VerifyArtifactChecksums but returns ErrChecksumMismatch
// (with structured ChecksumMismatchError context) when any artifact fails verification.
func VerifyArtifactChecksumsStrict(checksums []ArtifactChecksum) (VerificationReport, error) {
	report, err := VerifyArtifactChecksums(checksums)
	if err != nil {
		return VerificationReport{}, err
	}
	if report.Mismatched > 0 {
		return report, &ChecksumMismatchError{Report: report}
	}
	return report, nil
}
