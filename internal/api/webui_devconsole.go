//go:build devconsole

package api

import (
	"io/fs"
	"runtime"
)

// This is the `-tags devconsole` build: nothing is embedded, and the console is
// resolved from disk (installed share directories, then this repository's
// web-next/dist). Use it while iterating on the frontend so a `vite build`
// shows up on the next request without recompiling Go.
//
// It is deliberately NOT the default: a source-tree path baked into a shipped
// binary resolves to nothing on a real host.

func embeddedConsole() (fs.FS, bool) { return nil, false }

// consoleSourceFile returns this file's compile-time path, from which
// resolveWebNextDistDirFrom derives <repo>/web-next/dist.
func consoleSourceFile() string {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return sourceFile
}

func consoleMissingReason() string {
	return "this midnight-server binary was built with -tags devconsole and no " +
		"console directory was found; run `make web-build` in this checkout, or " +
		"point " + webNextDistDirEnv + " at a built web-next/dist directory"
}
