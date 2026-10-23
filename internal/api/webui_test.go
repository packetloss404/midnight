package api

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	root := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
		"favicon.ico":   {Data: []byte("icon")},
	}
	handler, ok := spaHandler(root)
	if !ok {
		t.Fatal("spaHandler ok = false, want true when index.html present")
	}

	cases := []struct {
		path     string
		wantBody string
	}{
		{"/", "<!doctype html><div id=root></div>"},             // root → index
		{"/assets/app.js", "console.log(1)"},                    // real asset served
		{"/projects/abc", "<!doctype html><div id=root></div>"}, // deep link → SPA fallback
		{"/nope/deep/link", "<!doctype html><div id=root></div>"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.path, rec.Code)
		}
		if rec.Body.String() != tc.wantBody {
			t.Fatalf("%s: body = %q, want %q", tc.path, rec.Body.String(), tc.wantBody)
		}
	}
}

type fixedInstanceDomainSettingsStore struct {
	value InstanceDomainSettings
	err   error
}

func (s fixedInstanceDomainSettingsStore) Get(_ context.Context) (InstanceDomainSettings, error) {
	return s.value, s.err
}

func (s fixedInstanceDomainSettingsStore) Put(_ context.Context, value InstanceDomainSettings, _ int64) (InstanceDomainSettings, error) {
	return value, nil
}

func TestConfiguredConsoleHostnameServesPublicLandingAtRoot(t *testing.T) {
	spa, ok := spaHandler(fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><div id=root></div>")},
	})
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	handler := configuredDomainLandingHandler(spa, fixedInstanceDomainSettingsStore{
		value: InstanceDomainSettings{ConsoleHostname: "midnight.example.com"},
	})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(method, "https://midnight.example.com/", nil)
			request.Host = "MIDNIGHT.EXAMPLE.COM:443"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
				t.Fatalf("Content-Type = %q, want text/html", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("Cache-Control = %q, want no-cache", got)
			}
			if method == http.MethodHead {
				if response.Body.Len() != 0 {
					t.Fatalf("HEAD body length = %d, want 0", response.Body.Len())
				}
				return
			}
			body := response.Body.String()
			for _, want := range []string{
				"This domain runs on Midnight.",
				"https://deployatmidnight.com",
				"deployatmidnight.com",
				"<svg",
			} {
				if !strings.Contains(body, want) {
					t.Errorf("landing body does not contain %q", want)
				}
			}
		})
	}
}

func TestConfiguredConsoleHostnamePreservesConsoleRoutesAndOtherHosts(t *testing.T) {
	const index = "<!doctype html><div id=root></div>"
	spa, ok := spaHandler(fstest.MapFS{"index.html": {Data: []byte(index)}})
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	handler := configuredDomainLandingHandler(spa, fixedInstanceDomainSettingsStore{
		value: InstanceDomainSettings{ConsoleHostname: "midnight.example.com"},
	})

	for _, test := range []struct {
		name   string
		host   string
		path   string
		method string
	}{
		{name: "console login", host: "midnight.example.com", path: "/login", method: http.MethodGet},
		{name: "console settings", host: "midnight.example.com", path: "/settings", method: http.MethodGet},
		{name: "other hostname", host: "admin.example.com", path: "/", method: http.MethodGet},
		{name: "non-read root", host: "midnight.example.com", path: "/", method: http.MethodPost},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "https://"+test.host+test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Body.String() != index {
				t.Fatalf("body = %q, want SPA index", response.Body.String())
			}
		})
	}
}

func TestConfiguredDomainLandingFailsOpenToConsoleWhenSettingsUnavailable(t *testing.T) {
	const index = "<!doctype html><div id=root></div>"
	spa, ok := spaHandler(fstest.MapFS{"index.html": {Data: []byte(index)}})
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	handler := configuredDomainLandingHandler(spa, fixedInstanceDomainSettingsStore{
		err: ErrInstanceDomainsNotConfigured,
	})
	request := httptest.NewRequest(http.MethodGet, "https://midnight.example.com/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Body.String() != index {
		t.Fatalf("body = %q, want SPA index", response.Body.String())
	}
}

func TestSPAHandlerDoesNotSwallowBackendPaths(t *testing.T) {
	root := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	handler, ok := spaHandler(root)
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	// Unmatched backend paths must 404 (not serve the SPA index), so the API
	// mux's own 404 semantics are preserved when the SPA catch-all is mounted.
	for _, p := range []string{"/api/v1alpha/auth/bootstrap", "/api/v1alpha/nope", "/graphql/v2", "/graphiql", "/mcp/v1", "/dashboard/x", "/auth/oauth/token"} {
		req := httptest.NewRequest(http.MethodPost, p, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (backend path must not serve SPA)", p, rec.Code)
		}
	}
}

func TestSPAHandlerDeclinesWithoutIndex(t *testing.T) {
	if _, ok := spaHandler(fstest.MapFS{"assets/app.js": {Data: []byte("x")}}); ok {
		t.Fatal("spaHandler ok = true, want false when index.html absent")
	}
}

func TestResolveWebNextDistDirUsesExplicitAbsolutePath(t *testing.T) {
	explicit := writeWebNextDistFixture(t, filepath.Join(t.TempDir(), "custom-ui"))
	installed := writeWebNextDistFixture(t, filepath.Join(t.TempDir(), "share", "midnight", "web-next"))
	executable := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(installed))), "bin", "midnight-server")

	if got := resolveWebNextDistDirFrom(explicit, executable, installed, ""); got != explicit {
		t.Fatalf("resolveWebNextDistDirFrom() = %q, want explicit %q", got, explicit)
	}
}

func TestResolveWebNextDistDirRejectsInvalidExplicitPath(t *testing.T) {
	installed := writeWebNextDistFixture(t, filepath.Join(t.TempDir(), "share", "midnight", "web-next"))
	executable := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(installed))), "bin", "midnight-server")

	if got := resolveWebNextDistDirFrom("relative/web-next", executable, installed, ""); got != "" {
		t.Fatalf("relative explicit path resolved to %q, want empty", got)
	}
	if got := resolveWebNextDistDirFrom(filepath.Join(t.TempDir(), "missing"), executable, installed, ""); got != "" {
		t.Fatalf("missing explicit path resolved to %q, want empty", got)
	}
}

func TestResolveWebNextDistDirFindsInstalledShareNextToExecutable(t *testing.T) {
	prefix := t.TempDir()
	installed := writeWebNextDistFixture(t, filepath.Join(prefix, "share", "midnight", "web-next"))
	executable := filepath.Join(prefix, "bin", "midnight-server")
	canonical := writeWebNextDistFixture(t, filepath.Join(t.TempDir(), "usr", "share", "midnight", "web-next"))

	if got := resolveWebNextDistDirFrom("", executable, canonical, ""); got != installed {
		t.Fatalf("resolveWebNextDistDirFrom() = %q, want installed %q", got, installed)
	}
}

func TestResolveWebNextDistDirPrefersCanonicalInstalledCandidate(t *testing.T) {
	root := t.TempDir()
	installed := writeWebNextDistFixture(t, filepath.Join(root, "usr", "share", "midnight", "web-next"))
	sourceFile := filepath.Join(root, "source", "internal", "api", "webui.go")
	writeWebNextDistFixture(t, filepath.Join(root, "source", "web-next", "dist"))

	if got := resolveWebNextDistDirFrom("", "", installed, sourceFile); got != installed {
		t.Fatalf("resolveWebNextDistDirFrom() = %q, want canonical installed %q", got, installed)
	}
}

func TestResolveWebNextDistDirFallsBackToSourceTree(t *testing.T) {
	root := t.TempDir()
	sourceFile := filepath.Join(root, "internal", "api", "webui.go")
	dist := writeWebNextDistFixture(t, filepath.Join(root, "web-next", "dist"))
	missingInstalled := filepath.Join(root, "usr", "share", "midnight", "web-next")

	if got := resolveWebNextDistDirFrom("", "", missingInstalled, sourceFile); got != dist {
		t.Fatalf("resolveWebNextDistDirFrom() = %q, want source dist %q", got, dist)
	}
}

func writeWebNextDistFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir dist fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatalf("write index fixture: %v", err)
	}
	return filepath.Clean(dir)
}

func TestSPAHandlerSetsCacheControl(t *testing.T) {
	root := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
		"favicon.ico":   {Data: []byte("icon")},
	}
	handler, ok := spaHandler(root)
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	cases := map[string]string{
		// Vite hashes filenames under assets/, so they are immutable forever.
		"/assets/app.js": "public, max-age=31536000, immutable",
		"/favicon.ico":   "no-cache",
		// The index carries the asset hashes; it must always revalidate.
		"/":             "no-cache",
		"/projects/abc": "no-cache",
	}
	for p, want := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control = %q, want %q", p, got, want)
		}
	}
}

func TestSPAHandlerServesCorrectContentTypes(t *testing.T) {
	root := fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"assets/app.css": {Data: []byte("body{}")},
	}
	handler, ok := spaHandler(root)
	if !ok {
		t.Fatal("spaHandler ok = false, want true")
	}
	cases := map[string]string{
		"/":               "text/html",
		"/assets/app.js":  "text/javascript",
		"/assets/app.css": "text/css",
	}
	for p, wantPrefix := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("%s: Content-Type = %q, want prefix %q", p, got, wantPrefix)
		}
	}
}

// A binary without a console must fail loudly at "/" rather than 404ing like a
// missing route, while backend prefixes keep their own 404 semantics.
func TestConsoleUnavailableHandler(t *testing.T) {
	handler := consoleUnavailableHandler("MIDNIGHT_WEB_DIST_DIR=\"/nope\" is bogus")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("root status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "is bogus") {
		t.Fatalf("root body = %q, want the operator-facing reason", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("root Content-Type = %q, want text/html", got)
	}

	for _, p := range []string{"/api/v1alpha/nope", "/graphql", "/mcp/v1", "/.well-known/x"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (backend paths keep their own 404)", p, rec.Code)
		}
	}
}

func TestResolveWebConsoleHonorsExplicitOverride(t *testing.T) {
	dist := writeWebNextDistFixture(t, filepath.Join(t.TempDir(), "custom-ui"))
	t.Setenv(webNextDistDirEnv, dist)

	console := resolveWebConsole()
	if console.Problem != "" {
		t.Fatalf("resolveWebConsole() problem = %q, want none", console.Problem)
	}
	if console.FS == nil {
		t.Fatal("resolveWebConsole() FS = nil, want the override directory")
	}
	if !strings.Contains(console.Origin, dist) {
		t.Fatalf("resolveWebConsole() origin = %q, want it to name %q", console.Origin, dist)
	}
	if got := WebConsoleStatus(); !strings.Contains(got, dist) {
		t.Fatalf("WebConsoleStatus() = %q, want it to name %q", got, dist)
	}
}

// An explicit override is authoritative: a bogus one must NOT silently fall
// back to the embedded bundle, because the operator asked for a specific one.
func TestResolveWebConsoleRefusesBogusOverride(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "does-not-exist")
	t.Setenv(webNextDistDirEnv, bogus)

	console := resolveWebConsole()
	if console.FS != nil {
		t.Fatal("resolveWebConsole() served a console despite a bogus override")
	}
	if !strings.Contains(console.Problem, webNextDistDirEnv) {
		t.Fatalf("resolveWebConsole() problem = %q, want it to name %s", console.Problem, webNextDistDirEnv)
	}
	if got := WebConsoleStatus(); !strings.HasPrefix(got, "unavailable") {
		t.Fatalf("WebConsoleStatus() = %q, want unavailable", got)
	}
}

// The default build embeds the console. CI compiles without running
// `make web-embed`, so this asserts the bundle's shape only when one is
// actually present, and asserts the honest failure report when it is not.
func TestEmbeddedConsoleIsSelfContainedWhenBuilt(t *testing.T) {
	t.Setenv(webNextDistDirEnv, "")
	embedded, ok := embeddedConsole()
	if !ok {
		if got := consoleMissingReason(); got == "" {
			t.Fatal("consoleMissingReason() is empty; a console-less binary must explain itself")
		}
		t.Skip("no console embedded in this build (run `make web-embed` first)")
	}
	index, err := fs.ReadFile(embedded, "index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(index)), "<!doctype html") {
		t.Fatalf("embedded index.html does not look like a document: %q", string(index))
	}
	if _, ok := spaHandler(embedded); !ok {
		t.Fatal("spaHandler declined the embedded console")
	}
	console := resolveWebConsole()
	if console.Origin != "embedded" {
		t.Fatalf("resolveWebConsole() origin = %q (problem=%q), want embedded", console.Origin, console.Problem)
	}
}
