# The web console and the server binary

`midnight-server` is one binary that serves its own console. The web-next
single-page app is compiled into the executable with `go:embed`; a released
server needs no co-located `dist/`, no `/usr/share` payload and no particular
working directory to render the dashboard at `/`.

## Building a self-contained binary

```sh
make web-embed      # npm ci && npm run build, then stage web-next/dist -> internal/api/webdist
make build-server   # refuses to compile if the console was not staged
```

`task web:embed` and `task build:server` are the same two steps.

What the steps do:

1. `web-next/dist` is built by Vite.
2. Its contents are copied verbatim to `internal/api/webdist/`, the directory
   `internal/api/webui_embed.go` embeds with `//go:embed all:webdist`.
3. `go build ./cmd/midnight-server` compiles that bundle into the binary.

`internal/api/webdist/` is a build output, not source. Only its own
`.gitignore` is tracked, which keeps the directory present so a fresh checkout
still compiles — CI lint and unit-test jobs do not need Node installed.

A binary compiled without step 2 is **not** silently empty:

- `make build-server` / `task build:server` fail before invoking the compiler,
  naming `internal/api/webdist/index.html` as missing.
- If one is produced anyway (a bare `go build ./cmd/midnight-server`), the
  server starts, logs `console=unavailable (...)` on its startup line, and
  answers every console route with an explicit HTTP 503 page saying the console
  was not bundled and how to rebuild. API, GraphQL, MCP and auth routes are
  unaffected.

The same staging step runs inside the release paths, so shipped artifacts
always carry a console:

| Artifact | Where the console is built |
| --- | --- |
| `deploy/oci/server/Dockerfile` | `node:22` stage, copied into `internal/api/webdist` before `go build` |
| `deploy/debian/rules` (`make package-deb`) | `npm ci && npm run build`, staged before the Go builds |
| `.github/workflows/build.yml` | the `web-next` job's artifact, downloaded into `internal/api/webdist`; the built binary is then grepped for a hashed asset name |

## Overriding the bundle: `MIDNIGHT_WEB_DIST_DIR`

Set `MIDNIGHT_WEB_DIST_DIR` to an absolute directory containing `index.html`
to serve that bundle instead of the embedded one. Use it to preview a console
build against a running server, or to ship a patched console without rebuilding
Go.

The override is **authoritative**. If it is set but the path is relative, is
missing, or has no `index.html`, the server does *not* quietly fall back to the
embedded console — it serves the 503 "console unavailable" page naming
`MIDNIGHT_WEB_DIST_DIR` and the value it was given. Falling back would mean an
operator who asked for a specific bundle silently gets a different one.

Resolution order:

1. `$MIDNIGHT_WEB_DIST_DIR` (authoritative when set)
2. the console embedded in the binary
3. `<dir of executable>/../share/midnight/web-next`
4. `/usr/share/midnight/web-next`

The Debian package still installs its console at `/usr/share/midnight/web-next`
and leaves `MIDNIGHT_WEB_DIST_DIR` commented out in
`/etc/midnight/midnight.env`: the embedded console is used by default, and the
on-disk copy is there for operators who want to point at it (or at their own)
explicitly.

The local compose stack (`docker-compose.local.yml`) sets
`MIDNIGHT_WEB_DIST_DIR=/workspace/web-next/dist` for exactly this reason: the
repository is bind-mounted, so `npm --prefix web-next run build` is visible on
the next request without recompiling Go. Run `make web-build` at least once, or
`/` answers 503 with the diagnostic page.

## Frontend iteration: `-tags devconsole`

```sh
go build -tags devconsole ./cmd/midnight-server
```

The `devconsole` build embeds nothing and resolves the console from disk: the
installed share directories, then this repository's `web-next/dist`. It exists
so a frontend change needs only `npm run build`, not a Go rebuild.

It is not the default, and must not be used for anything shipped: the
source-tree path it resolves is the path of the machine that compiled it, which
does not exist on a real host. That baked-in source path was the original defect
this design replaces.

## Caching and routing

- `/assets/*` (Vite writes content-hashed filenames there) is served
  `Cache-Control: public, max-age=31536000, immutable`.
- `index.html` and every SPA deep-link fallback is served `no-cache`, so a new
  deploy's asset hashes are picked up on the next navigation.
- Unknown paths fall back to `index.html` so client-side router deep links such
  as `/projects/{id}` resolve.
- Backend prefixes are never swallowed by that fallback: `/api/`, `/graphql`,
  `/graphiql`, `/auth/`, `/mcp`, `/.well-known` and `/dashboard` keep their own
  404 semantics for unmatched paths.

## Checking what a binary is serving

The startup line reports the console source:

```
midnight-server serving 0.1.0-alpha on 127.0.0.1:8080 (storage=postgres, console=embedded)
```

`console=` is one of `embedded`, `directory <path> (MIDNIGHT_WEB_DIST_DIR)`,
`directory <path>`, or `unavailable (<reason>)`.
