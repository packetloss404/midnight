package failurecodes

import (
	"errors"
	"fmt"
)

// FailureError wraps an error with a registered Code and an operator-facing
// recovery hint. It is the canonical shape that S06 packages return so callers
// can extract a (reason, recovery_hint) pair without hand-parsing strings.
type FailureError struct {
	// Code is the SCREAMING_SNAKE_CASE registry code; it must be registered.
	Code Code
	// RecoveryHint is the operator-facing remediation guidance.
	RecoveryHint string
	// Cause is the wrapped underlying error; it may be nil.
	Cause error
	// Message is an optional short, redaction-safe summary of the failure.
	Message string
}

// Error implements the error interface in the form
// `code=<CODE> message="..."` followed by the cause when present. The output is
// safe to surface in operator-facing logs and CLIs because it does not include
// tenant-controlled values that were not already in Message or Cause.
func (e *FailureError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"code=" + string(e.Code)}
	if e.Message != "" {
		parts = append(parts, fmt.Sprintf("message=%q", e.Message))
	}
	if e.RecoveryHint != "" {
		parts = append(parts, fmt.Sprintf("recovery_hint=%q", e.RecoveryHint))
	}
	if e.Cause != nil {
		parts = append(parts, "cause="+e.Cause.Error())
	}
	return joinSpaceSeparated(parts)
}

// Unwrap exposes the wrapped error so errors.Is/As traverse the chain.
func (e *FailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Reason returns the structured (code, recovery_hint) pair without losing the
// concrete *FailureError to callers that prefer interface access.
func (e *FailureError) Reason() (Code, string) {
	if e == nil {
		return InternalError, ""
	}
	return e.Code, e.RecoveryHint
}

// Wrap builds a FailureError that records a registered code, the registered
// recovery hint, an optional message, and an optional wrapped cause. Unknown
// codes are coerced to InternalError so callers cannot accidentally surface an
// unregistered reason.
func Wrap(code Code, message string, cause error) *FailureError {
	if !IsRegistered(code) {
		code = InternalError
	}
	return &FailureError{
		Code:         code,
		RecoveryHint: RecoveryHint(code),
		Message:      message,
		Cause:        cause,
	}
}

// New is shorthand for Wrap(code, message, nil) and is useful for emitting a
// plain registered failure without a wrapped cause.
func New(code Code, message string) *FailureError {
	return Wrap(code, message, nil)
}

// ReasonOf returns the registered Code and recovery hint extracted from err.
// When err does not carry a *FailureError, ok is false and the returned code
// is InternalError so callers always have a defined reason.
func ReasonOf(err error) (code Code, hint string, ok bool) {
	if err == nil {
		return InternalError, "", false
	}
	var failure *FailureError
	if errors.As(err, &failure) && failure != nil {
		return failure.Code, failure.RecoveryHint, true
	}
	return InternalError, "", false
}

func joinSpaceSeparated(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += " "
		}
		out += part
	}
	return out
}
