package desiredstate

import (
	"context"
	"testing"
)

func TestRESTPollingSourceFetchReturnsPlaceholderError(t *testing.T) {
	source := RESTPollingSource{BaseURL: "https://midnight.example"}

	_, err := source.Fetch(context.Background(), "agent-a")
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := err.(ErrNotImplemented); !ok {
		t.Fatalf("error = %T, want ErrNotImplemented", err)
	}
}

func TestRESTPollingSourceSatisfiesSource(t *testing.T) {
	var _ Source = RESTPollingSource{}
}
