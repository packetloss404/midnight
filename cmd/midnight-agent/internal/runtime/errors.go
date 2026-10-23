// Package runtime contains local runtime state and errors.
package runtime

import (
	"errors"
	"fmt"
)

// ErrUnavailable reports that the local runtime cannot satisfy an operation yet.
type ErrUnavailable struct {
	Runtime string
	Reason  string
}

func (e ErrUnavailable) Error() string {
	if e.Runtime == "" && e.Reason == "" {
		return "runtime unavailable"
	}
	if e.Runtime == "" {
		return fmt.Sprintf("runtime unavailable: %s", e.Reason)
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s runtime unavailable", e.Runtime)
	}
	return fmt.Sprintf("%s runtime unavailable: %s", e.Runtime, e.Reason)
}

// IsRuntimeUnavailable reports whether err is an ErrUnavailable value or pointer.
func IsRuntimeUnavailable(err error) bool {
	var unavailable ErrUnavailable
	if errors.As(err, &unavailable) {
		return true
	}
	var unavailablePtr *ErrUnavailable
	return errors.As(err, &unavailablePtr)
}
