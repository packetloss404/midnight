package runtime

import (
	"errors"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// ErrUnsupportedOperation is returned when an adapter implements the v1
// boundary method but does not yet support the requested side effect.
var ErrUnsupportedOperation = errors.New("runtime adapter: unsupported operation")

// UnsupportedOperationError carries adapter-specific unsupported-operation
// detail while preserving errors.Is(err, ErrUnsupportedOperation).
type UnsupportedOperationError struct {
	Adapter   string
	Operation string
	Reason    string
}

func (e UnsupportedOperationError) Error() string {
	parts := []string{"runtime adapter"}
	if strings.TrimSpace(e.Adapter) != "" {
		parts = append(parts, strings.TrimSpace(e.Adapter))
	}
	if strings.TrimSpace(e.Operation) != "" {
		parts = append(parts, strings.TrimSpace(e.Operation))
	}
	message := strings.Join(parts, ": ") + ": unsupported operation"
	if strings.TrimSpace(e.Reason) != "" {
		message += ": " + strings.TrimSpace(e.Reason)
	}
	return message
}

func (e UnsupportedOperationError) Unwrap() error {
	return errors.Join(
		ErrUnsupportedOperation,
		failurecodes.New(failurecodes.RuntimeUnsupportedOperation, e.Error()),
	)
}
