package runtime

import (
	"errors"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

func TestUnsupportedOperationErrorCarriesTypedReasonCode(t *testing.T) {
	err := UnsupportedOperationError{
		Adapter:   "kubernetes",
		Operation: "AttachVolume",
		Reason:    "planning adapter does not attach volumes",
	}
	if !errors.Is(err, ErrUnsupportedOperation) {
		t.Fatalf("errors.Is(%v, ErrUnsupportedOperation) = false", err)
	}
	code, hint, ok := failurecodes.ReasonOf(err)
	if !ok || code != failurecodes.RuntimeUnsupportedOperation {
		t.Fatalf("ReasonOf() = (%q, %q, %t), want RUNTIME_UNSUPPORTED_OPERATION", code, hint, ok)
	}
	if hint == "" {
		t.Fatal("expected recovery hint")
	}
}
