package runtime

import (
	"fmt"
	"testing"
)

func TestErrUnavailableMessage(t *testing.T) {
	err := ErrUnavailable{Runtime: "docker", Reason: "daemon not reachable"}

	if got := err.Error(); got != "docker runtime unavailable: daemon not reachable" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestIsRuntimeUnavailable(t *testing.T) {
	if !IsRuntimeUnavailable(ErrUnavailable{}) {
		t.Fatal("value ErrUnavailable not detected")
	}
	if !IsRuntimeUnavailable(&ErrUnavailable{}) {
		t.Fatal("pointer ErrUnavailable not detected")
	}
	if !IsRuntimeUnavailable(fmt.Errorf("probe failed: %w", ErrUnavailable{})) {
		t.Fatal("wrapped ErrUnavailable not detected")
	}
}
