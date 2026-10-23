package main

// ExitCode is the deterministic phase0-gate process exit code surface.
//
// Each ExitCode is the value passed to os.Exit. The stable mapping is:
//
//   - ExitOK (0) means the gate ran and every required check passed. CI may
//     proceed to the next stage.
//   - ExitInputError (2) means the CLI was invoked with invalid flags or
//     arguments. CI should treat this as a usage bug, not a release block.
//   - ExitManifestInvalid (3) means the drill manifest or alert ruleset failed
//     deterministic validation before any drill ran.
//   - ExitDrillFailure (4) means at least one executed drill failed or the
//     drill execution did not satisfy --require-executed-drills.
//   - ExitReleaseEvidenceFailure (5) means release evidence verification, or
//     the writing of release evidence/index/summary, failed.
//
// These values are intentionally small integers and never reused. Adding a new
// ExitCode requires a new constant; existing values must not shift.
type ExitCode int

const (
	// ExitOK indicates a successful gate run with all required checks passing.
	ExitOK ExitCode = 0
	// ExitInputError indicates malformed or contradictory CLI input.
	ExitInputError ExitCode = 2
	// ExitManifestInvalid indicates the drill manifest or alert ruleset is
	// invalid, structurally or referentially, before any drill executed.
	ExitManifestInvalid ExitCode = 3
	// ExitDrillFailure indicates one or more required drills failed, were
	// blocked, or were skipped under --require-executed-drills.
	ExitDrillFailure ExitCode = 4
	// ExitReleaseEvidenceFailure indicates release evidence emission or
	// verification failed.
	ExitReleaseEvidenceFailure ExitCode = 5
)

// Int returns the ExitCode as an int suitable for os.Exit.
func (c ExitCode) Int() int {
	return int(c)
}

// String returns a stable human-readable mnemonic for the exit code.
func (c ExitCode) String() string {
	switch c {
	case ExitOK:
		return "ok"
	case ExitInputError:
		return "input-error"
	case ExitManifestInvalid:
		return "manifest-invalid"
	case ExitDrillFailure:
		return "drill-failure"
	case ExitReleaseEvidenceFailure:
		return "release-evidence-failure"
	default:
		return "unknown"
	}
}

// AllExitCodes returns every ExitCode in stable numeric order. Tests use this
// to assert the documented surface stays exhaustive.
func AllExitCodes() []ExitCode {
	return []ExitCode{ExitOK, ExitInputError, ExitManifestInvalid, ExitDrillFailure, ExitReleaseEvidenceFailure}
}
