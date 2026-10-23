package docker

import (
	"errors"
	"fmt"

	"github.com/midnight/midnight/internal/failurecodes"
)

// Error is the typed error every Docker-adapter validation surface returns.
// It carries one failurecodes.Code so callers (operations, deployment FSM,
// agent supervisor) can branch deterministically on the registered code
// without parsing strings.
type Error struct {
	Code    failurecodes.Code
	Message string
	Detail  string
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is enables errors.Is(err, anotherDockerErr) by code equality.
func (e *Error) Is(target error) bool {
	if target == nil || e == nil {
		return false
	}
	var other *Error
	if errors.As(target, &other) {
		return other.Code == e.Code
	}
	return false
}

func (e *Error) Reason() (failurecodes.Code, string) {
	if e == nil {
		return failurecodes.InternalError, ""
	}
	return e.Code, failurecodes.RecoveryHint(e.Code)
}

// CodeOf returns the failurecodes.Code embedded in err, or the zero value
// when err is not an *Error.
func CodeOf(err error) failurecodes.Code {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func newError(code failurecodes.Code, message, detail string) *Error {
	return &Error{Code: code, Message: message, Detail: detail}
}
