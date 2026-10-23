package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDUsesInboundHeader(t *testing.T) {
	var got string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "req-user")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if got != "req-user" || rr.Header().Get(HeaderRequestID) != "req-user" {
		t.Fatalf("request id context/header = %q/%q", got, rr.Header().Get(HeaderRequestID))
	}
}

func TestRequestIDGeneratesWhenMissing(t *testing.T) {
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if FromContext(r.Context()) == "" {
			t.Fatal("request id missing from context")
		}
	}))
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.HasPrefix(rr.Header().Get(HeaderRequestID), "req-") {
		t.Fatalf("generated request id = %q", rr.Header().Get(HeaderRequestID))
	}
}

func TestRequestIDReplacesUnsafeInboundHeader(t *testing.T) {
	for _, value := range []string{
		"request id with spaces",
		"request-id\nforged",
		strings.Repeat("x", maxExternalRequestIDLength+1),
	} {
		t.Run(value[:min(len(value), 16)], func(t *testing.T) {
			var got string
			h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = FromContext(r.Context())
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header[HeaderRequestID] = []string{value}
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, req)

			if got == value || !strings.HasPrefix(got, "req-") || rr.Header().Get(HeaderRequestID) != got {
				t.Fatalf("unsafe request id was not replaced: input=%q got=%q", value, got)
			}
		})
	}
}
