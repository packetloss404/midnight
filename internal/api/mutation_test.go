package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/failurecodes"
)

// TestMutationSeamRejectsMissingIdempotencyKey proves the seam rejects a
// mutating request that omits Idempotency-Key with the same error shape MAIN
// uses today (IDEMPOTENCY_KEY_REQUIRED, 400) and does not invoke the handler.
func TestMutationSeamRejectsMissingIdempotencyKey(t *testing.T) {
	called := false
	handler := mutation("test:write", func(http.ResponseWriter, *http.Request) {
		called = true
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/anything", strings.NewReader(`{}`))
	// The seam depends on writeRequestError pulling the id from context; the
	// RequestID middleware would normally populate it, but its absence here is
	// fine — the envelope still carries an (empty) request_id field.
	handler(rr, req)

	if called {
		t.Fatal("handler should not run when Idempotency-Key is missing")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyRequired {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyRequired)
	}
}

// TestMutationSeamRejectsMalformedIdempotencyKey proves format enforcement
// matches the handlers' IDEMPOTENCY_KEY_INVALID shape.
func TestMutationSeamRejectsMalformedIdempotencyKey(t *testing.T) {
	handler := mutation("test:write", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/anything", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "bad key with spaces\x01")
	handler(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyInvalid {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyInvalid)
	}
}

func TestMutationSeamRejectsAmbiguousIdempotencyKey(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		wantCode failurecodes.Code
	}{
		{
			name:     "repeated header fields",
			values:   []string{"first-key", "second-key"},
			wantCode: failurecodes.IdempotencyKeyRequired,
		},
		{
			name:     "proxy coalesced fields",
			values:   []string{"first-key,second-key"},
			wantCode: failurecodes.IdempotencyKeyInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			handler := mutation("test:write", func(http.ResponseWriter, *http.Request) {
				called = true
			})
			request := httptest.NewRequest(http.MethodPost, "/api/v1alpha/anything", strings.NewReader(`{}`))
			for _, value := range tt.values {
				request.Header.Add("Idempotency-Key", value)
			}
			response := httptest.NewRecorder()

			handler(response, request)

			if called {
				t.Fatal("handler should not run for an ambiguous Idempotency-Key")
			}
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", response.Code, response.Body.String())
			}
			var got ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if got.Error.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", got.Error.Code, tt.wantCode)
			}
		})
	}
}

// TestMutationSeamPassesThroughReadsAndValidMutations proves non-mutating
// methods are never gated and that a valid key reaches the handler with the
// declared scope recorded in context.
func TestMutationSeamPassesThroughReadsAndValidMutations(t *testing.T) {
	var sawScope string
	var ran bool
	handler := mutation("widgets:write", func(w http.ResponseWriter, r *http.Request) {
		ran = true
		sawScope = MutationScopeFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	// GET passes through untouched (no key required).
	rrGet := httptest.NewRecorder()
	handler(rrGet, httptest.NewRequest(http.MethodGet, "/api/v1alpha/widgets", nil))
	if !ran || rrGet.Code != http.StatusOK {
		t.Fatalf("GET should pass through: ran=%v code=%d", ran, rrGet.Code)
	}
	if sawScope != "widgets:write" {
		t.Fatalf("scope = %q, want %q", sawScope, "widgets:write")
	}

	// POST with a valid key reaches the handler.
	ran = false
	rrPost := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1alpha/widgets", strings.NewReader(`{}`))
	postReq.Header.Set("Idempotency-Key", "valid-key-1")
	handler(rrPost, postReq)
	if !ran || rrPost.Code != http.StatusOK {
		t.Fatalf("valid POST should reach handler: ran=%v code=%d body=%s", ran, rrPost.Code, rrPost.Body.String())
	}
}

func TestTenantGeneratedDomainMethodScope(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{method: http.MethodGet, want: "domains:read"},
		{method: http.MethodPost, want: "domains:write"},
		{method: http.MethodDelete, want: "domains:write"},
		{method: http.MethodPut, want: ""},
	}
	for _, test := range tests {
		t.Run(test.method, func(t *testing.T) {
			var got string
			handler := tenantGeneratedDomainMethodScope(
				func(w http.ResponseWriter, r *http.Request) {
					got = MutationScopeFromContext(r.Context())
					w.WriteHeader(http.StatusNoContent)
				},
			)
			response := httptest.NewRecorder()
			handler(
				response,
				httptest.NewRequest(
					test.method,
					"/api/v1alpha/services/svc_test/environments/env_test/domains",
					nil,
				),
			)
			if response.Code != http.StatusNoContent || got != test.want {
				t.Fatalf(
					"status=%d scope=%q, want status=%d scope=%q",
					response.Code,
					got,
					http.StatusNoContent,
					test.want,
				)
			}
		})
	}
}

// TestMutationSeamEnforcedOnWrappedVolumesRoute proves the seam is actually
// wired on a router mutation route (volumes) and enforces the key end to end.
func TestMutationSeamEnforcedOnWrappedVolumesRoute(t *testing.T) {
	router := NewRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1alpha/volumes", strings.NewReader(`{"name":"data"}`))
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if got.Error.Code != failurecodes.IdempotencyKeyRequired {
		t.Fatalf("code = %q, want %q", got.Error.Code, failurecodes.IdempotencyKeyRequired)
	}
}
