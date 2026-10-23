package middleware

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersCoverConsoleAPIAndErrors(t *testing.T) {
	requests := []string{
		"/",
		"/login?redirect=%2F",
		"/api/v1alpha/compat",
		"/api/v1alpha/operations/op_1/events",
		"/.well-known/mcp",
	}
	for _, requestTarget := range requests {
		t.Run(requestTarget, func(t *testing.T) {
			handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "probe", http.StatusUnauthorized)
			}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestTarget, nil))

			assertSecurityHeaders(t, response.Header(), consoleContentSecurityPolicy)
		})
	}
}

func TestSecurityHeadersUseNarrowCompatibilityPolicies(t *testing.T) {
	tests := []struct {
		path       string
		wantPolicy string
	}{
		{path: "/graphiql", wantPolicy: graphiQLContentSecurityPolicy},
		{path: "/graphiql/", wantPolicy: graphiQLContentSecurityPolicy},
		{path: "/dashboard/landing.html", wantPolicy: legacyLandingContentSecurityPolicy},
		{path: "/dashboard/", wantPolicy: consoleContentSecurityPolicy},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			SecurityHeaders(http.NotFoundHandler()).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, test.path, nil),
			)
			assertSecurityHeaders(t, response.Header(), test.wantPolicy)
		})
	}

	if !strings.Contains(consoleContentSecurityPolicy, "script-src 'self';") ||
		strings.Contains(consoleContentSecurityPolicy, "script-src 'self' 'unsafe-inline'") ||
		strings.Contains(consoleContentSecurityPolicy, "https://unpkg.com") ||
		strings.Contains(consoleContentSecurityPolicy, "https://cdn.jsdelivr.net") {
		t.Fatal("production console CSP inherited an inline or external script exception")
	}
}

func TestSecurityHeadersPreserveStreamingAndUpgradeInterfaces(t *testing.T) {
	writer := &capabilityWriter{ResponseRecorder: httptest.NewRecorder()}
	handler := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("SecurityHeaders removed http.Flusher")
		}
		if _, ok := w.(http.Hijacker); !ok {
			t.Fatal("SecurityHeaders removed http.Hijacker")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": heartbeat\n\n"))
		w.(http.Flusher).Flush()
	}))

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/api/v1alpha/events", nil))

	assertSecurityHeaders(t, writer.Header(), consoleContentSecurityPolicy)
	if !writer.flushed {
		t.Fatal("stream was not flushed")
	}
	if got := writer.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
}

func TestSecurityHeadersNilHandlerFailsClosed(t *testing.T) {
	response := httptest.NewRecorder()
	SecurityHeaders(nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	assertSecurityHeaders(t, response.Header(), consoleContentSecurityPolicy)
}

func assertSecurityHeaders(t *testing.T, headers http.Header, wantPolicy string) {
	t.Helper()
	want := map[string]string{
		headerContentSecurityPolicy: wantPolicy,
		headerFrameOptions:          frameOptions,
		headerContentTypeOptions:    contentTypeOptions,
		headerReferrerPolicy:        referrerPolicy,
		headerPermissionsPolicy:     permissionsPolicy,
		headerStrictTransport:       strictTransportPolicy,
	}
	for name, wantValue := range want {
		if got := headers.Get(name); got != wantValue {
			t.Errorf("%s = %q, want %q", name, got, wantValue)
		}
	}
}

type capabilityWriter struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (w *capabilityWriter) Flush() {
	w.flushed = true
	w.ResponseRecorder.Flush()
}

func (w *capabilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, nil
}
