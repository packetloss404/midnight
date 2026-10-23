# MODULE_DEPENDENCY_MATRIX — Internal Package Dependency Graph

- **Skeleton ID:** SKEL-DEP-MATRIX
- **Owner:** Senior Engineer 4 of 4 (skeleton specifications)
- **Status:** Locked by panel decisions; ready for engineering kickoff

This file is the source of truth for **which package is allowed to import
which**. The matrix is enforced by `depguard` rules in `.golangci.yml`;
violations fail CI.

The shape of this matrix is driven by four panel constraints:

1. **The control plane is the product** (line 422). The reconciler, audit,
   queue, and policy packages must be discrete and not collapse into the
   API package.
2. **The agent does not import the server** (line 124 trust-boundary
   corollary, line 132 role separation). Only `pkg/orailtypes` and the
   gRPC `proto/agent/v1` cross the boundary.
3. **Audit is read-only from the API path** (line 132 role separation:
   `midnight_app` is INSERT-only; `midnight_audit_ro` SELECT-only).
4. **Policy is independent of persistence** (line 405 control-plane
   decomposition; pure functional policy enables OPA-compatibility later).

---

## 1. Package Index

```text
Server-side (cmd/midnight-server, cmd/midnight-worker import these):
  internal/api                  (HTTP)
  internal/api/middleware
  internal/db                   (sqlc + migrations + Tx + Repository ifaces)
  internal/queue                (Postgres job queue, lease/heartbeat)
  internal/auth
  internal/secrets              (KEK / DEK envelope crypto)
  internal/audit                (hash chain + Ed25519 checkpoints)
  internal/build
  internal/runtime              (server-side runtime adapter)
  internal/scheduler
  internal/reconciler           (Deployment reconciler)
  internal/router
  internal/acme
  internal/registry
  internal/backup
  internal/upgrade
  internal/observability
  internal/policy
  internal/redaction
  internal/webhook
  internal/idempotency
  internal/ssrf
  internal/cloudevents
  internal/failurecodes
  internal/version
  internal/clock                (time abstraction; only place time.Now is allowed)
  internal/testutil             (test helpers)

Agent-side (cmd/midnight-agent imports these):
  cmd/midnight-agent/internal/agent
  cmd/midnight-agent/internal/runtime
  cmd/midnight-agent/internal/route
  cmd/midnight-agent/internal/log
  cmd/midnight-agent/internal/drift
  cmd/midnight-agent/internal/asciicast

Public (everyone may import):
  pkg/midnightctl
  pkg/orailtypes
  proto/agent/v1
  api/openapi/v1alpha (generated stubs)
```

---

## 2. Dependency Matrix (server-side)

`✓` = allowed import. `✗` = forbidden. `→` = allowed but only via the
named interface (no concrete dependency).

```text
Importer ↓                       Importee →

                        api  mw   db   queue auth sec  aud  bld  rt   sch  rec  rou  acm  reg  bak  upg  obs  pol  red  whk  idp  ssr  cev  fcd  ver  clk
internal/api             —   ✓    ✓    ✓     ✓    ✓    →    ✓    →    ✓    →    →    ✓    ✓    ✓    ✓    ✓    →    ✓    ✓    ✓    ✓    ✓    ✓    ✓    ✓
internal/api/middleware  ✗   —    ✗    ✗     ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✓    ✓    ✗    ✓    ✓    ✓
internal/db              ✗   ✗    —    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/queue           ✗   ✗    ✓    —     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/auth            ✗   ✗    ✓    ✗     —    ✓    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/secrets         ✗   ✗    ✓    ✗     ✗    —    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✓    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/audit           ✗   ✗    ✓    ✗     ✗    ✓    —    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✓    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/build           ✗   ✗    ✓    ✓     ✗    ✓    ✓    —    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✓    ✗    ✓    ✗    ✗    ✓    ✓    ✓    ✓    ✓
internal/runtime         ✗   ✗    ✓    ✗     ✗    ✗    ✗    ✗    —    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/scheduler       ✗   ✗    ✓    ✗     ✗    ✗    ✗    ✗    →    —    ✗    ✗    ✗    ✓    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/reconciler      ✗   ✗    ✓    ✓     ✗    ✓    ✓    ✗    →    ✓    —    →    ✗    ✓    ✗    ✗    ✓    ✗    ✗    ✓    ✗    ✗    ✓    ✓    ✓    ✓
internal/router          ✗   ✗    ✓    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    —    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/acme            ✗   ✗    ✓    ✓     ✗    ✓    ✓    ✗    ✗    ✗    ✗    ✗    —    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✓    ✓    ✓    ✓    ✓
internal/registry        ✗   ✗    ✓    ✗     ✗    ✓    ✓    ✗    ✗    ✗    ✗    ✗    ✗    —    ✗    ✗    ✓    ✗    ✓    ✗    ✗    ✓    ✗    ✓    ✓    ✓
internal/backup          ✗   ✗    ✓    ✓     ✗    ✓    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✗    —    ✗    ✓    ✗    ✓    ✗    ✗    ✓    ✓    ✓    ✓    ✓
internal/upgrade         ✗   ✗    ✓    ✓     ✗    ✓    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    —    ✓    ✗    ✓    ✗    ✗    ✗    ✓    ✓    ✓    ✓
internal/observability   ✗   ✗    ✓    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    —    ✗    ✓    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/policy          ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    —    ✗    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/redaction       ✗   ✗    ✗    ✗     ✗    →    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    —    ✗    ✗    ✗    ✗    ✓    ✓    ✓
internal/webhook         ✗   ✗    ✓    ✓     ✗    ✓    ✓    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✓    —    ✗    ✓    ✓    ✓    ✓    ✓
internal/idempotency     ✗   ✗    ✓    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    —    ✗    ✗    ✓    ✓    ✓
internal/ssrf            ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    —    ✗    ✓    ✓    ✓
internal/cloudevents     ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✓    ✗    ✗    ✗    ✗    ✗    —    ✓    ✓    ✓
internal/failurecodes    ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    —    ✓    ✓
internal/version         ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    —    ✓
internal/clock           ✗   ✗    ✗    ✗     ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    ✗    —
```

Legend: `mw=middleware`, `sec=secrets`, `aud=audit`, `bld=build`,
`rt=runtime`, `sch=scheduler`, `rec=reconciler`, `rou=router`, `acm=acme`,
`reg=registry`, `bak=backup`, `upg=upgrade`, `obs=observability`,
`pol=policy`, `red=redaction`, `whk=webhook`, `idp=idempotency`,
`ssr=ssrf`, `cev=cloudevents`, `fcd=failurecodes`, `ver=version`,
`clk=clock`.

`→` (interface seam): the importer **defines** an interface in its own
package and has the importee implement it via dependency injection. No
direct compile-time import edge exists. Examples:

- `internal/api → internal/audit`: the API does not import `internal/audit`;
  the API package defines its own `Auditor` interface and the server's
  composition root injects an `internal/audit` implementation. This is the
  panel's "Audit is read-only from `internal/api`" rule (line 132).
- `internal/api → internal/runtime`: API never imports runtime; only the
  reconciler does.
- `internal/api → internal/reconciler`: API hands work to the queue, never
  calls the reconciler directly. The "Deployment reconciler is the only
  writer" rule (line 106) is enforced by import discipline.
- `internal/redaction → internal/secrets`: redaction receives a
  `SecretRegistrar` interface; it never imports secrets directly. This
  prevents the redaction pipeline from accidentally seeing decrypted
  plaintext (only the secret bytes for the bloom-filter pass).
- `internal/scheduler → internal/runtime`: scheduler defines a
  `CapabilityProvider` interface; runtime implements it.
- `internal/reconciler → internal/router`: reconciler enqueues route
  changes via the queue, not by importing router directly. Router is the
  worker-side consumer.
- `internal/reconciler → internal/runtime`: reconciler enqueues `deploy`
  jobs; the deploy worker imports runtime.

---

## 3. Cyclic-Dependency Rules

**No cycles.** Period. The matrix above is a DAG.

If two packages need to talk both ways, the consumer defines an interface
in its own package and dependency-injects the implementation. Concretely:

- A package P that "wants to call back into" package Q defines a
  Q-callback interface inside P. Q does not depend on P; the composition
  root in `cmd/midnight-server/main.go` wires P's interface to a Q-shaped
  adapter.
- This is exactly how `internal/audit` is invoked from `internal/api`
  without `internal/api` importing `internal/audit`.

A custom golangci-lint rule (`gocyclo` + `depguard`) plus a CI job that
runs `go mod graph | tsort` flags any cycle. Cycles are a hard CI gate,
not a warning.

---

## 4. Crossing Rules (panel-locked)

### Rule A: `internal/policy` cannot import `internal/db`

Panel implication: policy must be testable as a pure function over a
typed input struct so that an OPA/Rego adapter can be added later without
restructuring callers (panel "policy: built-in validators and approval
gates first; OPA/Rego-compatible policy later", line ~405). The policy
package receives all inputs through its own value types; persistence
shapes never leak in.

Enforcement: `depguard` rule

```yaml
- internal/policy:
    deny:
      - github.com/midnight/midnight/internal/db
```

### Rule B: `internal/audit` is read-only from `internal/api`

Panel decision (line 132): role separation — `midnight_app` is INSERT-only;
`midnight_audit_ro` is SELECT-only. The Go layer mirrors:

- `internal/api` may **read** audit via the `Auditor` read methods through
  an injected interface.
- `internal/api` MUST NOT call `audit.Append`. Writes are routed through
  the queue and the workers, never through HTTP handlers directly.

Enforcement: `depguard` plus a code-review rule that any PR adding
`audit.Append` from an HTTP handler is rejected.

### Rule C: The agent does not import the server

Panel implication (lines 124, 132 trust-boundary corollary). Agent
binaries link only:

- `pkg/orailtypes`
- `pkg/midnightctl` (only for the optional `--register-via-server-api`
  bootstrap path; the runtime path uses the gRPC stream)
- `proto/agent/v1`
- `internal/failurecodes` re-exported through `pkg/orailtypes`
- standard library + a small allowlist of third-party libs

Enforcement: a separate `go.mod` is **not** used (single-module repo for
release ergonomics), but a CI job builds `cmd/midnight-agent` with a
`-tags agentonly` build constraint and a custom linker rule that fails if
any `internal/api`, `internal/db`, `internal/queue`, etc. symbol is
referenced.

### Rule D: `internal/redaction` does not import `internal/secrets`

The redaction pipeline must be importable into the AI gateway and the log
collector; importing `internal/secrets` would couple it to KEK/KMS state
and to a Postgres handle. Instead, `internal/redaction` defines a
`SecretRegistrar` interface that `internal/secrets` implements at the
composition root.

### Rule E: Workers do not import the API

`cmd/midnight-worker` and `cmd/midnight-buildkit-supervisor` import the
business packages (`internal/build`, `internal/reconciler`, etc.) but do
NOT import `internal/api`. Workers are not HTTP servers; mixing
responsibilities would make the queue tests have to spin a full HTTP
server.

### Rule F: The platform/tenant boundary

This is the most important crossing rule. **What may flow into a tenant
container:**

| Direction | Allowed | Forbidden |
|---|---|---|
| Host → tenant: env vars | `publicClientVars`, `buildVars`, resolved variables (deep-copied per panel line 112) | KEK material, raw cosign keys, agent-bootstrap tokens |
| Host → tenant: mounts | `/etc/midnight/tenant/<svc>/...` (config), explicit volume directories | `/etc/midnight/secrets.key`, `/var/lib/midnight/db`, `/var/lib/midnight/secrets`, `/var/lib/midnight/registry/data`, `/var/lib/midnight/kms` (panel line 84) |
| Host → tenant: Docker socket | NEVER (no `/var/run/docker.sock` mount into any tenant container) | All cases |
| Host → tenant: namespaces | network bridge per `(project, environment)`; volume mount; nothing else | host PID, host network, host IPC, host UTS (panel line 91) |
| Tenant → host | egress through Docker NAT only; SMTP 25/465/587 blocked by default (panel line 162); cloud metadata blocked by SSRF rules (panel line 124 — applies to user code via egress proxy in `egress: stable-ip` mode) | Direct file system access; raw socket; capability `SYS_ADMIN` |
| Tenant → control plane API | only if the workspace explicitly grants it (service accounts) and the request goes through Traefik ingress, not a back-door socket | All cases without explicit grant |

These rules are the single most-likely-leak boundary in the system; see
the "Most Likely To Leak" finding at the end of this file.

---

## 5. Build-Time vs Runtime Dependencies

### Build-time only

`tools/buildtools.go` carries build-time imports (mockgen, sqlc,
oapi-codegen, govulncheck) under a `//go:build tools` constraint. These
DO NOT appear in any `cmd/*` binary.

### Runtime only

- `cmd/midnight-server` runtime imports: every `internal/*` package
  except `internal/build` and `internal/runtime`-host-only.
- `cmd/midnight-worker` runtime imports: depends on `--queue=<class>`
  flag. Build worker imports `internal/build` + `internal/registry`;
  deploy worker imports `internal/runtime` + `internal/registry`;
  reconcile worker imports `internal/reconciler` + `internal/router`;
  webhook worker imports `internal/webhook` + `internal/ssrf`; cleanup
  worker imports `internal/backup` + `internal/registry`.
- `cmd/midnight-agent` runtime imports: only `cmd/midnight-agent/internal/*`
  + `pkg/orailtypes` + `proto/agent/v1` + stdlib.
- `cmd/midnight` (CLI) runtime imports: `pkg/midnightctl` +
  `pkg/orailtypes` + a small set of CLI helpers in
  `cmd/midnight/internal/`. Crucially the CLI does NOT link the server
  binary; `task release` produces a small static binary.

### Build vs runtime separation invariant

The agent binary at MVP must be ≤ 25 MiB stripped. This is a hard CI
gate: `du -B1 cmd/midnight-agent` after `task build:agent && strip`. The
gate enforces "the agent does not accidentally import the server" by
making accidental coupling visible in binary size growth.

---

## 6. Public Package Stability (`pkg/*`)

`pkg/midnightctl` and `pkg/orailtypes` follow semver `MAJOR.MINOR.PATCH`
(panel line 202). Removing or renaming any exported symbol is a major
bump. Adding fields to non-finalized resource types is a minor bump.

Engineering MUST NOT add a transitive dependency from `pkg/*` into
`internal/*`. A CI job runs `go list -m -deps ./pkg/...` and fails if any
internal path appears.

---

## 7. Visualizing the Graph

A `task graph` target produces `out/depgraph.svg` from the depguard
config. CI publishes this artifact on every PR so reviewers can see the
graph diff visually.

The graph is intentionally narrow at the top (HTTP and queue have many
fan-outs), wide in the middle (business packages), and narrow at the
bottom (`failurecodes`, `version`, `clock`, `cloudevents`,
`orailtypes`). Anything that pushes the bottom layer wider is suspect.

---

## 8. Most Likely Architectural Leak (engineering finding)

**The platform / tenant boundary in `cmd/midnight-agent/internal/runtime`
+ `internal/secrets` is the most likely architectural leak.**

Why:

- The agent assembles the `ContainerSpec.Env` from resolved variables
  before submitting to Docker. That assembly path crosses three different
  trust zones (KEK-decrypted secrets in `internal/secrets`, the redaction
  pipeline, and the runtime spec the agent hands to the daemon).
- The panel adds significant pressure on this seam: `publicClientVars`
  and `buildVars` are the only classes that may populate `process.env`
  during build (panel line 129); sealed variables must never round-trip
  to plaintext via API (panel line 120); deep-copy of resolved variables
  into the immutable snapshot is required (panel line 112); the
  `BUILD_OUTPUT_SECRET_LEAK` static scan must run pre-deploy (panel line
  129); session asciicasts must be redacted by the same pipeline (panel
  line 131).
- Each of these is correct individually, but they all share the same
  small handful of types in `internal/secrets` and `internal/redaction`.
  A future "convenience" helper that flattens a `[]Variable` into
  `map[string]string` for an unrelated reason (e.g. a debug endpoint) is
  the obvious mechanism by which sealed plaintext leaks.

Mitigations the matrix already encodes:

1. `internal/redaction` cannot import `internal/secrets` directly; it
   uses the `SecretRegistrar` interface (Rule D).
2. `internal/api/middleware` cannot import `internal/secrets` (matrix
   row).
3. `internal/policy` cannot import `internal/db` (Rule A), so a policy
   helper cannot accidentally read a sealed variable from the DB.
4. The agent binary cannot import any `internal/*` server packages (Rule
   C), so a host-side helper cannot reach back through the API for a
   plaintext sealed value.
5. The `Sealed` type wraps the byte slice and disallows access from
   non-deploy contexts via a deploy-context token; this is encoded in
   `internal/secrets` and exposed only on the `SecretStore.Decrypt`
   return for sealed scope.

What additional mitigations engineering should add as a follow-up ADR:

- A linter rule forbidding any function whose return type is
  `map[string]string` and whose name begins with `Resolved` or
  `EnvVarsFor`. The shape map[string]string is the obvious leak vector;
  the codebase should use a typed `EnvVarSet` that distinguishes sealed
  from plain.
- A property test that fuzzes the agent's spec assembly path with random
  combinations of public, build, runtime, and sealed variables, and
  asserts that every resulting `ContainerSpec.Env` line passes the
  `RedactionPipeline.Redact` round-trip with `secret_leakage_rate == 0`.
- A documented quarterly chaos drill that injects a sealed variable
  whose plaintext is a unique sentinel string and grep'd against every
  asciicast, log line, audit row, support bundle, and webhook body
  produced by the system over a 24-hour window.

This boundary is the one engineering must pay the most attention to. The
matrix narrows the surface; the mitigations close the door.
