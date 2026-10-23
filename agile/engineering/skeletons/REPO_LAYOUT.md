# REPO_LAYOUT — Midnight Go Monorepo Structure

- **Skeleton ID:** SKEL-REPO-LAYOUT
- **Owner:** Senior Engineer 4 of 4 (skeleton specifications)
- **Status:** Locked by panel decisions; ready for engineering kickoff
- **Companion files:** `INTERFACES_CONTROL_PLANE.md`, `INTERFACES_AGENT.md`, `INTEGRATION_TESTING_HARNESS.md`, `MODULE_DEPENDENCY_MATRIX.md`

This file is the **source of truth for module structure**. Engineering implements
exactly this tree. Adding a top-level directory or moving a package across the
`internal/` ↔ `pkg/` line requires an ADR.

The layout encodes three panel-locked rules:

1. The control plane is the product (lines 420–422). Scheduler, reconciler, audit,
   and queue must be addressable as discrete packages, not collapsed into a
   monolithic "server" package.
2. The agent runs on hosts and crosses a strong trust boundary to tenant
   containers (lines 86–98, 124, 132). It must be a separate binary with its
   own package tree and shared **only** the public type packages with the server.
3. CLI/SDK/Terraform/MCP all derive from the **same** OpenAPI surface (lines
   170–177). The Go client lives in `pkg/midnightctl` so it can be vendored by
   external IaC consumers without pulling in `internal/`.

---

## Top-Level Tree

```text
midnight/                          # repo root, Go module github.com/midnight/midnight
├── cmd/                           # one subdir per producing binary
│   ├── midnight-server/           # control-plane API + reconciler host
│   ├── midnight-worker/           # build/deploy/reconcile/webhook/cleanup workers
│   ├── midnight-agent/            # host agent (Docker Engine API client + Traefik writer)
│   ├── midnight/                  # user-facing CLI
│   └── midnight-buildkit-supervisor/  # cgroup-capped BuildKit launcher (Alpha co-located)
├── internal/                      # Go-enforced no-import-from-outside boundary
│   ├── api/                       # HTTP handlers, OpenAPI server stubs
│   ├── api/middleware/            # auth, idempotency, rate-limit, request-id
│   ├── db/                        # sqlc-generated queries, migrations driver
│   ├── queue/                     # per-class Postgres queue (build/deploy/reconcile/webhook/cleanup)
│   ├── auth/                      # token verification, OIDC, session, CSRF
│   ├── secrets/                   # KEK/DEK envelope crypto, sealed variables
│   ├── audit/                     # per-row hash chain + Ed25519 daily checkpoint
│   ├── build/                     # BuildKit driver, Railpack/Nixpacks plan capture
│   ├── runtime/                   # Docker Engine API runtime adapter (server-side)
│   ├── scheduler/                 # placement, host-arch matching, multi-arch policy
│   ├── reconciler/                # Deployment reconciler (named promotion authority)
│   ├── router/                    # Traefik file-provider config writer (atomic temp+rename)
│   ├── acme/                      # ACME lifecycle, DnsProvider plugin host, CAA preflight
│   ├── registry/                  # embedded distribution registry, mirroring, GC, pin table
│   ├── backup/                    # restic driver, off-host destinations, verification
│   ├── upgrade/                   # transactional upgrade orchestrator + smoke probe
│   ├── observability/             # OTLP exporter, Prometheus rules, support-bundle
│   ├── policy/                    # built-in validators, approval gates, OPA shim
│   ├── redaction/                 # multi-pass secret redaction pipeline
│   ├── webhook/                   # signed outbound dispatch, replay-window key rotation
│   ├── idempotency/               # Idempotency-Key state machine + body-hash store
│   ├── ssrf/                      # safe outbound HTTP client (denylist + IP-pinning)
│   ├── cloudevents/               # CloudEvents envelope encoder/decoder
│   ├── failurecodes/              # generated registry from docs/error-codes.md
│   ├── version/                   # semver + schema_version + compat matrix
│   └── testutil/                  # shared test helpers (NOT exported)
├── pkg/                           # public Go API surface (semver-stable)
│   ├── midnightctl/               # Go API client used by CLI, IaC providers, MCP
│   └── orailtypes/                # resource types, IDs, enums shared across CLI/SDK/agent
├── api/                           # API artifacts (versioned)
│   ├── openapi/v1alpha/midnight.yaml
│   ├── openapi/v1/midnight.yaml   # promoted from v1alpha after v1-graduation rubric
│   └── asyncapi/v1/midnight.yaml  # generated from internal/cloudevents catalog
├── proto/                         # gRPC for agent ↔ server channel (control stream)
│   └── agent/v1/agent.proto
├── web/                           # React + TypeScript dashboard (separate Vite/pnpm workspace)
│   ├── app/
│   ├── components/
│   ├── lib/                       # generated TS client from openapi/v1alpha
│   └── package.json
├── migrations/                    # SQL migrations, tagged reversible|forward-only
│   ├── 0001_init.up.sql
│   ├── 0001_init.down.sql         # absent for forward-only
│   └── manifest.yaml              # tag, schema_version delta, hash
├── deploy/                        # packaging artifacts (.deb, systemd, journald, logrotate, ufw)
│   ├── debian/
│   ├── systemd/
│   ├── journald/
│   ├── logrotate/
│   ├── ufw/
│   ├── apparmor/midnight-default
│   └── prometheus/midnight.rules.yaml
├── tools/                         # build-only Go imports kept out of runtime modules
│   └── buildtools.go              # //go:build tools — pinned versions of mockgen, sqlc, etc.
├── schemas/                       # JSONSchemas for plans, events, telemetry
│   ├── plan/v1.json
│   ├── events/<type>/<version>.json
│   └── telemetry/v1.json
├── docs/                          # publishable docs and CI-gated artifacts
│   ├── error-codes.md             # SCREAMING_SNAKE_CASE registry; CI gates additive-only
│   ├── compat/docker.md
│   ├── compat/components.md
│   └── runbooks/                  # mirrored to /usr/share/midnight/runbooks/ in .deb
├── Taskfile.yml                   # Task runner (chosen over Make for cross-platform CI)
├── Makefile                       # thin shim that delegates to task; one-line targets
├── .golangci.yml                  # lint config (errcheck, gosec, sqlclosecheck, contextcheck, ireturn)
├── .editorconfig
├── go.mod
├── go.sum
├── go.work                        # only used to develop pkg/midnightctl alongside CLI
└── LICENSE                        # Apache-2.0
```

---

## Per-Binary Package Justifications

### `cmd/midnight-server`

The control-plane HTTP API host. Owns the listening socket, serves
`/api/v1alpha/*`, and houses the in-process **Deployment reconciler** worker
class (panel line 106: "Deployment reconciler is the only writer authorized to
advance `DEPLOYING → ACTIVE`"). Importing `internal/api`, `internal/reconciler`,
`internal/audit`, `internal/queue`, `internal/policy`. Does **not** import
`internal/runtime` or `internal/build` — those are worker concerns.

### `cmd/midnight-worker`

Hosts the `build`, `deploy`, `reconcile`, `webhook`, and `cleanup` worker pools
(panel line 102). Each pool has its own bounded concurrency and lease-aware
dequeue loop. Built as one binary so a single systemd unit dispatches by
`--queue=<class>` flag; this matches the panel rule that queues are "separate
per-class with independent worker pools and bounded concurrency."

### `cmd/midnight-agent`

The host agent. Static Go binary deployed via `.deb`. Owns the local Docker
Engine API client (panel line 90: refuses to start outside Engine v1.43+ with
`DOCKER_ENGINE_UNSUPPORTED`), the Traefik file-provider writer, the LOST
detector, and the heartbeat loop. Does **not** import the server. Communicates
via `proto/agent/v1` over a long-lived authenticated connection.

### `cmd/midnight`

User-facing CLI. Imports `pkg/midnightctl` for API calls and `pkg/orailtypes`
for resource shapes. Stores tokens in the OS keychain (panel line 175). Includes
`midnight audit verify` (panel line 121, ships in MVP), `midnight doctor` and
`midnight preflight` split (panel line 185), `midnight upgrade-plan` (panel
line 193), and the canonical command tree (panel lines 181–187).

### `cmd/midnight-buildkit-supervisor`

A small launcher binary that runs co-located BuildKit "as a dedicated
unprivileged user with a CPU/memory cgroup cap, dedicated tmpfs, and no read
access to `/var/lib/midnight/{db,secrets,registry/data,kms}`" (panel line 84).
Lives as its own binary because it must `setuid`/cgroup-attach before exec'ing
BuildKit; that responsibility does not belong in the agent or worker.

---

## Internal Package Boundary Justifications

| Package | Why a separate package |
|---|---|
| `internal/api` | HTTP layer should never reach into `internal/db` directly; goes through repository interfaces from `internal/db`. Keeps OpenAPI-generated handlers narrow. |
| `internal/api/middleware` | Cross-cutting (auth, idempotency, rate-limit, request-id). Composable, separately testable, no business logic. |
| `internal/db` | Owns sqlc-generated code and migrations driver. Defines `Repository` interfaces used by upper layers; concrete sqlc structs are package-private. |
| `internal/queue` | Per-class Postgres queue with `FOR UPDATE SKIP LOCKED` dequeue (panel line 102). Isolates the lease/heartbeat/reaper state machine from job handlers. |
| `internal/auth` | Token verification, OIDC PKCE, session-cookie machinery (panel line 122). Separate from `internal/api/middleware` so non-HTTP callers (gRPC agent stream, MCP) reuse it. |
| `internal/secrets` | Envelope encryption, KEK rotation, sealed-variable rules (panel line 118). Crypto must not leak into request handlers. |
| `internal/audit` | Per-row hash-chain, daily Ed25519 checkpoint (panel line 121). Read-only from `internal/api` — writes go through a single `Append` transaction. |
| `internal/build` | BuildKit driver, Railpack plan capture, untrusted-build classification (panel line 83). Separate from runtime so Phase-3 remote BuildKit can swap the driver. |
| `internal/runtime` | Server-side runtime adapter (mostly used by reconciler for image-digest mirroring). The host-side runtime lives in `cmd/midnight-agent/internal`. |
| `internal/scheduler` | Placement decisions, host-arch matching, multi-arch policy (panel line 81). Pure function over registry+host inputs; no IO. |
| `internal/reconciler` | The named **Deployment reconciler** worker class (panel line 106). Owning package because it is the only legal writer of `DEPLOYING → ACTIVE`. |
| `internal/router` | Atomic temp+rename writes of Traefik dynamic config (panel line 159). Reserves `/.well-known/acme-challenge/*` at max priority. |
| `internal/acme` | ACME lifecycle reconciler, DnsProvider plugin host, CAA preflight, cold-start budget (panel lines 153–158, 203). |
| `internal/registry` | Embedded distribution registry, mirror controller, pin-table, GC dry-run/apply (panel lines 144, 79, 112). |
| `internal/backup` | restic driver, per-project repo, off-host destinations, verification, KEK-isolated passphrase (panel lines 130, 137–143). |
| `internal/upgrade` | Transactional upgrade orchestrator (panel line 192). Snapshot → migrate → smoke → on-failure auto-rollback. Owns the `/var/lib/midnight/upgrade.lock` sentinel. |
| `internal/observability` | OTLP exporter, Prometheus rules registration, support-bundle collector (panel line 196). |
| `internal/policy` | Built-in validators and approval gates (panel line 405-ish). MUST NOT import `internal/db`; takes interfaces. |
| `internal/redaction` | Multi-pass secret redaction (panel line 126). Used by AI gateway, log pipeline, audit `before/after` payload sanitizer. |
| `internal/webhook` | Signed outbound dispatch (panel line 125: `Midnight-Signature: t=…,v1=…,kid=…`, two active keys, 30-day rotation). |
| `internal/idempotency` | `(token_id, method, path_template)` scope, 24h TTL, RFC-8785 body hash (panel line 172). Standalone so `internal/api/middleware` and gRPC agent stream both reuse it. |
| `internal/ssrf` | One shared safe outbound HTTP client (panel line 124). Resolved-once + IP-pinned. |
| `internal/cloudevents` | CloudEvents envelope encoder + event-type catalog (panel line 177). AsyncAPI is generated from this registry. |
| `internal/failurecodes` | Generated from `docs/error-codes.md` (panel line 174). Codes are additive-only; renames require v-bump. |
| `internal/version` | Semver, schema_version, `/api/v1alpha/compat` payload (panel line 193). |
| `internal/testutil` | Test fixtures and helpers; intentionally `internal/` so external packages cannot accidentally depend on test scaffolding. |

---

## Public Package Justifications

### `pkg/midnightctl`

The Go API client. **Semver-stable.** Used by:

- `cmd/midnight` (CLI)
- The Terraform / OpenTofu provider (separate repo, vendors this)
- Pulumi provider (separate repo, vendors this)
- MCP server (panel line 127)
- External integrations and customer scripts

Importing this package must not pull in `internal/db` or `internal/api`. The
panel's "two consecutive SDK releases generated without manual patches" rubric
(line 171, item e) requires that `pkg/midnightctl` be regeneratable from the
OpenAPI spec without hand-edits.

### `pkg/orailtypes`

Resource type definitions, IDs, enums. The **only** package that may be shared
between server and agent (panel line 124-trust-boundary corollary). Defines:

- Resource ID format `<prefix>_<26-char Crockford base32 ULID>` (panel line 169)
- Public state enums (`DeploymentState`, `JobState`, `OperationState`)
- `failurecodes.Code` re-export for clients
- CloudEvents type constants

`pkg/orailtypes` has zero non-stdlib dependencies so the agent stays a tiny
static binary.

---

## `web/` Dashboard

Separate pnpm workspace, not a Go package. Lives in the same repo so the
OpenAPI client TypeScript types regenerate against the same `api/openapi/`
spec, satisfying the panel's "Pact contracts for CLI, dashboard, Terraform
provider, MCP" rubric (line 171, item c). Build artifact is embedded into
`cmd/midnight-server` via `go:embed` only at release-build time; dev mode
proxies to a Vite dev server.

---

## Build Orchestration

**Taskfile.yml** is the primary entry point. Reasons:

- Cross-platform (developers on macOS / Linux / WSL).
- Declarative, with file-watch dependencies.
- CI uses the same task names as developers, eliminating "works locally" drift.

**Makefile** is a thin shim for environments without `task` installed. Each
target is one line that delegates: `task build` etc.

Required tasks (top-level):

```text
task build               # build all binaries + dashboard
task build:server
task build:agent
task build:cli
task build:dashboard
task lint                # golangci-lint + tsc + eslint
task test                # go test ./... -race -coverprofile -shuffle=on
task test:integration    # spins testcontainers (see INTEGRATION_TESTING_HARNESS.md)
task test:contract       # Schemathesis + Pact verifications
task generate            # sqlc, openapi, mock, asyncapi
task migrate:up
task migrate:plan        # offline migration plan inspector
task release             # only runs on protected branch + tag
task supplychain         # govulncheck, osv-scanner, npm audit --omit=dev
```

`tools/buildtools.go` (file-only, build-tagged):

```go
//go:build tools
// +build tools

package tools

import (
    _ "github.com/golangci/golangci-lint/cmd/golangci-lint"
    _ "github.com/sqlc-dev/sqlc/cmd/sqlc"
    _ "github.com/deepmap/oapi-codegen/v2/cmd/oapi-codegen"
    _ "go.uber.org/mock/mockgen"
    _ "github.com/pressly/goose/v3/cmd/goose"
    _ "golang.org/x/vuln/cmd/govulncheck"
)
```

This file pins build-time-only dependencies in `go.mod` without putting them on
the runtime path.

---

## Lint / Test Config Locations

| File | Purpose |
|---|---|
| `.golangci.yml` | Required linters: `errcheck`, `gosec`, `sqlclosecheck`, `rowserrcheck`, `contextcheck`, `ireturn` (interface-return discipline), `noctx`, `bodyclose`. Custom rule: forbid `time.Now()` outside `internal/clock`. |
| `.editorconfig` | Tabs for Go, 2-space for YAML/JSON, LF endings. |
| `web/.eslintrc.cjs` | TypeScript strict, no implicit any, react-hooks. |
| `web/tsconfig.json` | strict: true, exactOptionalPropertyTypes, noUncheckedIndexedAccess. |
| `.github/workflows/ci.yml` | Required checks: `task lint`, `task test`, `task test:integration`, `task test:contract`, `task supplychain`. Race detector required. Coverage gate ≥ 75% for control-plane packages, ≥ 60% for agent (panel-implied). |
| `.github/workflows/release.yml` | Runs only on protected branches with tag (panel line 133). Cosign-signs artifacts. |

---

## Module Boundary Enforcement

Two mechanisms enforce the layout:

1. **Go's `internal/` rule** — `internal/*` cannot be imported outside the
   parent module. Engineering may not work around this with `go.work` tricks.
2. **`depguard` lint rule in `.golangci.yml`** — explicit allow-lists per
   package matching the dependency matrix in `MODULE_DEPENDENCY_MATRIX.md`.
   For example: `internal/policy` lists `internal/db` as a forbidden import.

If a feature requires a new cross-package edge, the engineer adds an ADR and
updates both `.golangci.yml` and `MODULE_DEPENDENCY_MATRIX.md` in the same PR.
This is the panel's preferred way to keep the package graph acyclic and
auditable as the system grows past Alpha.
