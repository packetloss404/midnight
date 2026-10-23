package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/api/middleware"
)

// TestErrorEnvelopeCarriesRequestIDMatchingHeader proves that JSON error
// bodies carry a request_id that clients/support can correlate with the
// X-Request-Id response header, including on the generated-id path (no inbound
// X-Request-Id header).
func TestErrorEnvelopeCarriesRequestIDMatchingHeader(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()

	// POST without Idempotency-Key deterministically produces an error envelope.
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"slug":"x"}`))
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}

	headerID := rr.Header().Get(middleware.HeaderRequestID)
	if headerID == "" {
		t.Fatal("expected X-Request-Id response header to be set")
	}

	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got.Error.RequestID == "" {
		t.Fatal("expected error body request_id to be populated")
	}
	if got.Error.RequestID != headerID {
		t.Fatalf("request_id mismatch: body=%q header=%q", got.Error.RequestID, headerID)
	}
}

// TestErrorEnvelopeRequestIDEchoesInboundHeader proves the correlation id
// honours a caller-supplied X-Request-Id.
func TestErrorEnvelopeRequestIDEchoesInboundHeader(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()

	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/projects", strings.NewReader(`{"slug":"x"}`))
	req.Header.Set(middleware.HeaderRequestID, "req-correlate-me")
	router.ServeHTTP(rr, req)

	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got.Error.RequestID != "req-correlate-me" {
		t.Fatalf("request_id = %q, want %q", got.Error.RequestID, "req-correlate-me")
	}
	if hdr := rr.Header().Get(middleware.HeaderRequestID); hdr != "req-correlate-me" {
		t.Fatalf("X-Request-Id header = %q, want %q", hdr, "req-correlate-me")
	}
}
