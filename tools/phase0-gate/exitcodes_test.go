package main

import "testing"

func TestAllExitCodesDocumented(t *testing.T) {
	want := []struct {
		code ExitCode
		i    int
		s    string
	}{
		{ExitOK, 0, "ok"},
		{ExitInputError, 2, "input-error"},
		{ExitManifestInvalid, 3, "manifest-invalid"},
		{ExitDrillFailure, 4, "drill-failure"},
		{ExitReleaseEvidenceFailure, 5, "release-evidence-failure"},
	}
	codes := AllExitCodes()
	if len(codes) != len(want) {
		t.Fatalf("AllExitCodes() = %v, want %d entries", codes, len(want))
	}
	for i, w := range want {
		if codes[i] != w.code {
			t.Fatalf("AllExitCodes()[%d] = %d, want %d", i, codes[i], w.code)
		}
		if codes[i].Int() != w.i {
			t.Fatalf("AllExitCodes()[%d].Int() = %d, want %d", i, codes[i].Int(), w.i)
		}
		if codes[i].String() != w.s {
			t.Fatalf("AllExitCodes()[%d].String() = %q, want %q", i, codes[i].String(), w.s)
		}
	}
}

func TestExitCodeStringUnknown(t *testing.T) {
	if got := ExitCode(99).String(); got != "unknown" {
		t.Fatalf("ExitCode(99).String() = %q, want unknown", got)
	}
}

func TestExitCodesAreStable(t *testing.T) {
	// These specific integer values are documented in the README and the
	// release-gate contract. They must not change without a versioned bump.
	if int(ExitOK) != 0 || int(ExitInputError) != 2 || int(ExitManifestInvalid) != 3 || int(ExitDrillFailure) != 4 || int(ExitReleaseEvidenceFailure) != 5 {
		t.Fatalf("exit code values drifted: %d %d %d %d %d", ExitOK, ExitInputError, ExitManifestInvalid, ExitDrillFailure, ExitReleaseEvidenceFailure)
	}
}
