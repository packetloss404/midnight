package failurecodes

import (
	"errors"
	"strings"
	"testing"
)

func TestWrapPopulatesCodeHintAndCause(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	failure := Wrap(ACMEHTTP01Unreachable, "host unreachable on :80", cause)

	if failure.Code != ACMEHTTP01Unreachable {
		t.Fatalf("Code = %q, want %q", failure.Code, ACMEHTTP01Unreachable)
	}
	if failure.RecoveryHint != RecoveryHint(ACMEHTTP01Unreachable) {
		t.Fatalf("RecoveryHint = %q, want %q", failure.RecoveryHint, RecoveryHint(ACMEHTTP01Unreachable))
	}
	if !errors.Is(failure, cause) {
		t.Fatalf("errors.Is should reach cause: got %v", failure)
	}

	got := failure.Error()
	for _, want := range []string{
		"code=ACME_HTTP01_UNREACHABLE",
		"message=\"host unreachable on :80\"",
		"recovery_hint=",
		"cause=dial tcp: refused",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestWrapCoercesUnregisteredCodeToInternalError(t *testing.T) {
	failure := Wrap(Code("UNKNOWN"), "boom", nil)
	if failure.Code != InternalError {
		t.Fatalf("Code = %q, want %q", failure.Code, InternalError)
	}
}

func TestReasonOfExtractsRegisteredCode(t *testing.T) {
	failure := New(ACMECAAForbidden, "blocked")
	wrapped := errors.Join(errors.New("context"), failure)

	code, hint, ok := ReasonOf(wrapped)
	if !ok {
		t.Fatal("ReasonOf should detect wrapped FailureError")
	}
	if code != ACMECAAForbidden {
		t.Fatalf("code = %q, want %q", code, ACMECAAForbidden)
	}
	if hint == "" {
		t.Fatal("expected non-empty recovery hint")
	}
}

func TestReasonOfReturnsInternalErrorForPlainErrors(t *testing.T) {
	code, hint, ok := ReasonOf(errors.New("plain"))
	if ok {
		t.Fatal("plain errors should not report a registered reason")
	}
	if code != InternalError {
		t.Fatalf("code = %q, want %q", code, InternalError)
	}
	if hint != "" {
		t.Fatalf("hint = %q, want empty", hint)
	}
	code, _, ok = ReasonOf(nil)
	if ok || code != InternalError {
		t.Fatalf("nil error should yield (InternalError, false), got (%q, %v)", code, ok)
	}
}

func TestFailureErrorReasonAccessor(t *testing.T) {
	failure := New(RouteReservedPath, "reserved")
	code, hint := failure.Reason()
	if code != RouteReservedPath || hint != RecoveryHint(RouteReservedPath) {
		t.Fatalf("Reason() = (%q, %q), want (%q, %q)", code, hint, RouteReservedPath, RecoveryHint(RouteReservedPath))
	}

	var nilFailure *FailureError
	code, hint = nilFailure.Reason()
	if code != InternalError || hint != "" {
		t.Fatalf("nil Reason() = (%q, %q), want (%q, empty)", code, hint, InternalError)
	}
	if nilFailure.Error() != "" {
		t.Fatalf("nil Error() = %q, want empty", nilFailure.Error())
	}
}
