package api

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// registerWebUI serves the web-next single-page console at the site root ("/"),
// with an index.html fallback so client-side router deep links (e.g.
// /projects/{id}) resolve.
//
// The default build compiles the console INTO the binary (see webui_embed.go),
// so `midnight-server` is a single self-contained artifact: no co-located
// dist/, no source tree, no working-directory assumptions. The `devconsole`
// build tag swaps the embedded bundle for on-disk resolution (see
// webui_devconsole.go) for iterating against a live Vite build.
//
// The root is ALWAYS registered. When no console can be resolved the root
// serves an explicit "console not bundled" diagnostic (503) instead of leaving
// the route unmounted, so a mis-built binary or a bad MIDNIGHT_WEB_DIST_DIR
// override fails loudly rather than looking like a routing bug.
//
// Static assets are public (they carry no secrets); API calls the SPA makes
// are authorized separately by their own bearer token, so the root is mounted
// without the API bearer gate to avoid a chicken-and-egg where the UI needed a
// token to load before the user could supply one.
func registerWebUI(mux *http.ServeMux, instanceDomains InstanceDomainSettingsStore) {
	console := resolveWebConsole()
	var handler http.Handler
	if console.FS != nil {
		if spa, ok := spaHandler(console.FS); ok {
			handler = spa
		}
	}
	if handler == nil {
		handler = consoleUnavailableHandler(console.Problem)
	}
	mux.Handle("/", configuredDomainLandingHandler(handler, instanceDomains))
}

// configuredDomainLandingHandler gives the configured public console hostname
// a useful root page without moving or weakening the authenticated console.
// Only an exact GET/HEAD for "/" is intercepted. Console deep links, static
// assets, and every backend route continue to the normal handler.
func configuredDomainLandingHandler(next http.Handler, store InstanceDomainSettingsStore) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store != nil &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.URL.Path == "/" {
			settings, err := store.Get(r.Context())
			if err == nil && requestHostname(r.Host) == strings.ToLower(settings.ConsoleHostname) {
				serveConfiguredDomainLanding(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func requestHostname(hostport string) string {
	hostport = strings.TrimSpace(hostport)
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = host
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(hostport, "[]")), ".")
}

const configuredDomainLandingHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#090d18">
  <title>This domain runs on Midnight</title>
  <style>
    :root { color-scheme: dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 32px 20px; color: #f5f7ff; background: radial-gradient(circle at 50% 12%, #172748 0, #0d1527 30%, #090d18 68%); }
    main { width: min(100%, 620px); text-align: center; }
    .mark { width: 112px; height: 112px; margin: 0 auto 30px; color: #f5f7ff; filter: drop-shadow(0 20px 36px rgba(57, 112, 214, .28)); }
    .eyebrow { margin: 0 0 12px; color: #8bb7ff; font: 700 12px/1.4 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; letter-spacing: .18em; text-transform: uppercase; }
    h1 { margin: 0; font-size: clamp(34px, 7vw, 58px); line-height: 1.04; letter-spacing: -.045em; }
    .copy { max-width: 500px; margin: 20px auto 0; color: #aebbd1; font-size: 17px; line-height: 1.65; }
    a { display: inline-flex; align-items: center; gap: 10px; margin-top: 32px; border: 1px solid #31548a; border-radius: 10px; padding: 12px 17px; color: #f5f7ff; background: rgba(26, 48, 83, .7); font-weight: 700; text-decoration: none; transition: border-color .16s ease, background .16s ease, transform .16s ease; }
    a:hover { border-color: #8bb7ff; background: rgba(40, 71, 119, .82); transform: translateY(-1px); }
    a:focus-visible { outline: 3px solid rgba(139, 183, 255, .5); outline-offset: 4px; }
    .url { color: #bcd3fa; font: 600 13px/1.4 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    @media (prefers-reduced-motion: reduce) { a { transition: none; } }
  </style>
</head>
<body>
  <main>
    <svg class="mark" viewBox="0 0 256 256" fill="none" role="img" aria-labelledby="midnight-mark-title">
      <title id="midnight-mark-title">Midnight</title>
      <mask id="midnight-crescent" maskUnits="userSpaceOnUse" x="0" y="0" width="256" height="256">
        <rect width="256" height="256" fill="white"/>
        <circle cx="148" cy="98" r="58" fill="black"/>
      </mask>
      <circle cx="122" cy="112" r="58" fill="currentColor" mask="url(#midnight-crescent)"/>
      <path d="M64 184h128" stroke="#8bb7ff" stroke-width="12" stroke-linecap="round"/>
    </svg>
    <p class="eyebrow">Midnight</p>
    <h1>This domain runs on Midnight.</h1>
    <p class="copy">Midnight is the open-source platform for deploying, operating, and understanding your applications on infrastructure you control.</p>
    <a href="https://deployatmidnight.com">
      <span>Visit Midnight</span>
      <span class="url">deployatmidnight.com →</span>
    </a>
  </main>
</body>
</html>
`

func serveConfiguredDomainLanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(configuredDomainLandingHTML))
	}
}

// WebConsoleStatus reports, in one short line, how this process resolved the
// operator console. Servers print it at startup so an operator can tell an
// embedded console from an overridden directory from a missing bundle without
// reading the source.
func WebConsoleStatus() string {
	console := resolveWebConsole()
	if console.Problem != "" {
		return "unavailable (" + console.Problem + ")"
	}
	return console.Origin
}

// webConsole is a resolved console bundle: either FS+Origin, or Problem.
type webConsole struct {
	FS     fs.FS
	Origin string
	// Problem explains, in operator terms, why no console is available.
	Problem string
}

// resolveWebConsole picks the console bundle this process serves.
//
// Order:
//  1. $MIDNIGHT_WEB_DIST_DIR — an operator override, and AUTHORITATIVE: when it
//     is set but unusable we refuse to serve anything rather than silently
//     falling back to a different (possibly stale, possibly older) bundle.
//  2. The console embedded in this binary (default builds).
//  3. On-disk locations: a share directory next to the installed executable,
//     the canonical package location, and — only under the `devconsole` build
//     tag — the repository's web-next/dist.
func resolveWebConsole() webConsole {
	explicit := strings.TrimSpace(os.Getenv(webNextDistDirEnv))
	executable, _ := os.Executable()
	resolved := resolveWebNextDistDirFrom(
		explicit,
		executable,
		filepath.FromSlash(installedWebNextDistDir),
		consoleSourceFile(),
	)
	if explicit != "" {
		if resolved == "" {
			return webConsole{Problem: fmt.Sprintf(
				"%s=%q is not an absolute directory containing index.html; "+
					"unset it to use the console built into this binary",
				webNextDistDirEnv, explicit,
			)}
		}
		return webConsole{FS: os.DirFS(resolved), Origin: "directory " + resolved + " (" + webNextDistDirEnv + ")"}
	}
	if embedded, ok := embeddedConsole(); ok {
		return webConsole{FS: embedded, Origin: "embedded"}
	}
	if resolved != "" {
		return webConsole{FS: os.DirFS(resolved), Origin: "directory " + resolved}
	}
	return webConsole{Problem: consoleMissingReason()}
}

// spaHandler builds a single-page-app handler over root: real files are served
// as-is, and any other path falls back to index.html so a client-side router
// can resolve deep links. It returns ok=false when root has no index.html
// (i.e. the SPA has not been built), so the caller can decline to register it.
func spaHandler(root fs.FS) (http.Handler, bool) {
	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		return nil, false
	}
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SPA is mounted at "/" as a catch-all. It must NOT swallow
		// unmatched backend paths — those should keep returning a proper 404
		// (JSON from the API mux, or a bare 404) rather than the HTML index.
		// Registered backend routes are more specific and win in ServeMux; this
		// guards the UNregistered tail under a backend prefix.
		if isBackendPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		rel := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if rel == "" || rel == "." {
			serveSPAIndex(w, index)
			return
		}
		// Serve a real static asset when it exists; otherwise fall back to the
		// SPA index so the client router can handle the route.
		if f, openErr := root.Open(rel); openErr == nil {
			info, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !info.IsDir() {
				setStaticAssetCacheControl(w, rel)
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		serveSPAIndex(w, index)
	}), true
}

// hashedAssetDir is the Vite output directory whose filenames carry a content
// hash, so its entries are safe to cache permanently. Everything else (the
// index, favicons, manifests) is served revalidated.
const hashedAssetDir = "assets/"

func setStaticAssetCacheControl(w http.ResponseWriter, rel string) {
	if strings.HasPrefix(rel, hashedAssetDir) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
}

// backendPathPrefixes are request-path prefixes owned by non-SPA surfaces. The
// SPA catch-all returns 404 for their unmatched tail instead of serving index.
var backendPathPrefixes = []string{
	"/api/",
	"/graphql",
	"/graphiql",
	"/auth/",
	"/mcp",
	"/.well-known",
	"/dashboard",
}

func isBackendPath(p string) bool {
	for _, prefix := range backendPathPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func serveSPAIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}

// consoleUnavailableHandler answers console routes with an explicit 503 that
// names the problem and the fix. Backend prefixes keep their own 404 semantics.
func consoleUnavailableHandler(problem string) http.Handler {
	if strings.TrimSpace(problem) == "" {
		problem = consoleMissingReason()
	}
	body := []byte("<!doctype html>\n<meta charset=\"utf-8\">\n" +
		"<title>Midnight console unavailable</title>\n" +
		"<h1>Midnight console unavailable</h1>\n<p>" + htmlEscape(problem) + "</p>\n" +
		"<p>The API on this listener is unaffected; only the web console is missing.</p>\n")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBackendPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write(body)
	})
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return replacer.Replace(s)
}

const (
	webNextDistDirEnv       = "MIDNIGHT_WEB_DIST_DIR"
	installedWebNextDistDir = "/usr/share/midnight/web-next"
)

// resolveWebNextDistDirFrom resolves an on-disk console bundle from the
// candidate inputs. It is pure so the ordering can be tested directly.
//
// An explicit path is authoritative: if it is invalid or lacks index.html we
// return "" instead of silently serving a stale package/source-tree bundle.
func resolveWebNextDistDirFrom(
	explicit,
	executable,
	installedCandidate,
	sourceFile string,
) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		if !filepath.IsAbs(explicit) {
			return ""
		}
		return usableWebNextDistDir(explicit)
	}

	candidates := make([]string, 0, 3)
	if executable = strings.TrimSpace(executable); executable != "" {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(executable), "..", "share", "midnight", "web-next"),
		)
	}
	if installedCandidate = strings.TrimSpace(installedCandidate); installedCandidate != "" {
		candidates = append(candidates, installedCandidate)
	}
	if sourceFile = strings.TrimSpace(sourceFile); sourceFile != "" {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(sourceFile), "..", "..", "web-next", "dist"),
		)
	}
	for _, candidate := range candidates {
		if resolved := usableWebNextDistDir(candidate); resolved != "" {
			return resolved
		}
	}
	return ""
}

func usableWebNextDistDir(candidate string) string {
	candidate = filepath.Clean(candidate)
	info, err := os.Stat(filepath.Join(candidate, "index.html"))
	if err != nil || info.IsDir() {
		return ""
	}
	return candidate
}
