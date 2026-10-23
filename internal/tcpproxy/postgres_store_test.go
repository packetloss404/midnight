package tcpproxy

import (
	"errors"
	"strings"
	"testing"
)

func TestEndpointIDFromPropagatesEntropyFailure(t *testing.T) {
	_, err := endpointIDFrom(errorReader{})
	if err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("endpointIDFrom error = %v", err)
	}
}

func TestBoundedStringTrimsAndBoundsPersistedDiagnostics(t *testing.T) {
	value := boundedString("  "+strings.Repeat("x", 600)+"  ", 512)
	if len(value) != 512 || strings.Contains(value, " ") {
		t.Fatalf("bounded value length=%d", len(value))
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
