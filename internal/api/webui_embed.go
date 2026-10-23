//go:build !devconsole

package api

import (
	"embed"
	"io/fs"
)

// This is the DEFAULT build: the operator console ships inside the binary.
//
// webdist/ is a staging copy of web-next/dist produced by `make web-embed`
// (or `task web:embed`), which is also what deploy/oci/server/Dockerfile,
// deploy/debian/rules and .github/workflows/build.yml run before compiling.
// Only webdist/.gitignore is tracked, so a checkout that has never built the
// console still compiles — `all:` is required for the embed pattern to match
// that dotfile-only directory. A binary built that way is not silently empty:
// embeddedConsole reports "not present", the root serves an explicit 503
// diagnostic, and `make build-server` fails before the compiler runs.
//
// Build with `-tags devconsole` to resolve the console from disk instead (see
// webui_devconsole.go) when iterating against a live Vite build.
//
//go:embed all:webdist
var embeddedConsoleFS embed.FS

// embeddedConsole returns the compiled-in console bundle, if this binary
// actually carries one.
func embeddedConsole() (fs.FS, bool) {
	sub, err := fs.Sub(embeddedConsoleFS, "webdist")
	if err != nil {
		return nil, false
	}
	info, err := fs.Stat(sub, "index.html")
	if err != nil || info.IsDir() {
		return nil, false
	}
	return sub, true
}

// consoleSourceFile is empty in the default build: a shipped binary must never
// resolve its console from the source tree it happened to be compiled in.
func consoleSourceFile() string { return "" }

func consoleMissingReason() string {
	return "this midnight-server binary was built without a console bundle " +
		"(internal/api/webdist was empty at compile time); rebuild with " +
		"`make web-embed && make build-server`, or point " +
		webNextDistDirEnv + " at a built web-next/dist directory"
}
