package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerHasProductionTimeouts(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 30*time.Second {
		t.Fatalf("ReadTimeout = %s", server.ReadTimeout)
	}
	if server.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %s", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("MaxHeaderBytes = %d", server.MaxHeaderBytes)
	}
	// WriteTimeout intentionally remains zero because operation/log streams
	// are long-lived responses.
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %s, want streaming-compatible zero", server.WriteTimeout)
	}
}

func TestHTTPServerAppliesBrowserSecurityHeaders(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, path := range []string{"/login", "/api/v1alpha/compat"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))

			if response.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
			}
			for name, want := range map[string]string{
				"Content-Security-Policy":   "frame-ancestors 'none'",
				"Permissions-Policy":        "camera=()",
				"Referrer-Policy":           "no-referrer",
				"Strict-Transport-Security": "max-age=31536000; includeSubDomains",
				"X-Content-Type-Options":    "nosniff",
				"X-Frame-Options":           "DENY",
			} {
				got := response.Header().Get(name)
				if name == "Content-Security-Policy" || name == "Permissions-Policy" {
					if !strings.Contains(got, want) {
						t.Errorf("%s = %q, want to contain %q", name, got, want)
					}
					continue
				}
				if got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
		})
	}
}

func TestServiceUpReadDeadlineExtendsOnlyExactUploadRoute(t *testing.T) {
	called := 0
	handler := serviceUpReadDeadlineMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		name         string
		method       string
		path         string
		wantExtended bool
	}{
		{name: "upload", method: http.MethodPost, path: "/api/v1alpha/services/svc_123/up", wantExtended: true},
		{name: "collection", method: http.MethodPost, path: "/api/v1alpha/services/svc_123", wantExtended: false},
		{name: "nested", method: http.MethodPost, path: "/api/v1alpha/services/svc_123/up/extra", wantExtended: false},
		{name: "read", method: http.MethodGet, path: "/api/v1alpha/services/svc_123/up", wantExtended: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &readDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			started := time.Now()
			handler.ServeHTTP(
				response,
				httptest.NewRequest(tc.method, tc.path, nil),
			)
			if response.Code != http.StatusNoContent {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.wantExtended != !response.readDeadline.IsZero() {
				t.Fatalf("read deadline=%s extended=%v", response.readDeadline, tc.wantExtended)
			}
			if tc.wantExtended && response.readDeadline.Before(started.Add(serverUpReadTimeout-time.Second)) {
				t.Fatalf("read deadline=%s does not provide the upload window", response.readDeadline)
			}
		})
	}
	if called != 4 {
		t.Fatalf("downstream calls=%d, want 4", called)
	}
}

type readDeadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadline time.Time
}

func (r *readDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadline = deadline
	return nil
}
