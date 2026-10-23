# Phase 1 / Lane 2 (RUNTIME) — Status

- **Branch:** `phase1/lane2-runtime` (worktree at
  `D:/projects/midnight/.worktrees/phase1-lane2-runtime`).
- **Off:** `main` @ `2a64283`.
- **Status:** all in-scope work landed; ready for human review.

## What landed

### A. Dependencies
- `github.com/docker/docker v28.5.2+incompatible` (Engine API client).
- `github.com/docker/go-connections v0.7.0`.
- `golang.org/x/net v0.53.0` (HTTP/2 transport).
- Transitive deps pulled by `go mod tidy`: `containerd/errdefs`,
  `distribution/reference`, `Microsoft/go-winio`, `moby/sys/atomicwriter`,
  `opencontainers/{go-digest,image-spec}`, `docker/go-units`, OpenTelemetry
  SDK packages, etc.
- `go.mod` Go directive bumped to `go 1.25.0` (forced by Docker SDK);
  `tools/versions.txt` updated to match. Lint guard verifies parity.

### B. `internal/runtime/docker` package
- Implements the existing `RuntimeAdapter` interface against the Docker
  Engine API.
- Operations: `Schedule`, `Start`, `Stop`, `Remove`, `Drain`, `Reconcile`,
  `StreamInstanceStatus`, `ApplyNetworkPolicy`, `ResolveRoutingTarget`,
  `PullVerify`. Idempotent on `IdempotencyKey` retry; reconciles missing
  containers as `LOST`; Stop/Remove tolerate gone-already states.
- Engine API floor pinned to **v1.43** (ADR-004). Container hardening from
  ADR-004 + Sprint 4 / S4-06: `HostConfig.Init=true`, `no-new-privileges`,
  `CapDrop=[ALL]` + panel-locked `CapAdd`, default ulimits, `PidsLimit`,
  json-file logging with size caps.
- Deterministic naming: `or-{svc_id_short}-{dep_seq}-{inst_seq}` (Sprint 4
  S4-04). Required label set documented in
  `docs/runtime/labels.md` and asserted by the conformance suite.

### C. `daemon.json` baseline enforcement
- `internal/runtime/docker/baseline.go` — fetches `/info` + `/version`,
  validates `storage-driver=overlay2`, `live-restore=true`, json-file log
  driver, no-new-privileges, cgroup version `2`. Refuses with
  `DOCKER_DAEMON_BASELINE_CONFLICT` typed error on conflict.
- Init() sequence: Ping → API floor → /info baseline. Wedged daemon
  surfaces `AGENT_RUNTIME_UNAVAILABLE`. Engine < 1.43 surfaces
  `DOCKER_ENGINE_UNSUPPORTED`.
- Adapter does **not** auto-edit `daemon.json` (installer territory; out of
  scope per the prompt).

### D. cgroup v2 hard-fail
- `internal/runtime/docker/cgroup.go` probes `/sys/fs/cgroup` for the
  unified hierarchy. Refuses with **panel-locked** remediation string
  `add 'systemd.unified_cgroup_hierarchy=1' to the kernel command line and
  reboot` (`CGROUP_V2_REQUIRED`).

### E. Conformance suite
- Existing `internal/runtime/conformance` passes the Docker adapter (unit
  test in `internal/runtime/docker/adapter_test.go` calls
  `conformance.Run(...)` against a fake EngineClient).
- New `internal/runtime/conformance/integration_test.go` (build tag
  `integration`) exercises **schedule → start → reconcile → stop → remove**
  against a real Docker daemon. Image defaults to `busybox:stable`;
  override via `MIDNIGHT_RUNTIME_CONFORMANCE_IMAGE`.
- Default `go test ./...` does **not** require Docker; the tag opts in.
- Runs locally via:
  ```
  # Linux/macOS:
  go test -tags=integration ./internal/runtime/...
  # Windows: requires DOCKER_HOST pointing at WSL2 daemon
  ```

### F. Agent ↔ control-plane real network
- `pkg/agentapi` — new shared package carrying the wire DTOs +
  `HeartbeatVerifier` (Phase 0 HMAC helper preserved). Agent and server
  both depend on it; the agent's existing `internal/protocol` re-exports
  via type aliases so older call sites compile unchanged.
- `cmd/midnight-agent/internal/control` — Phase 1 client transport:
  - HTTPS over HTTP/2 (`golang.org/x/net/http2`).
  - mTLS-ready: `client-cert`/`client-key` flags or env vars; if both unset
    falls back to HTTPS+HMAC heartbeat (the panel-allowed scope-blow
    fallback).
  - System CA trust by default; `MIDNIGHT_AGENT_CA_BUNDLE` overrides.
- `control.Loop` runs **register → heartbeat → reconcile → status**:
  - Heartbeat HMAC produced via the locked helper at every tick.
  - On each tick the loop calls `Adapter.Reconcile(ctx)` and posts a
    `StatusReport` that includes the daemon.json + cgroup drift summary.
  - `VERSION_SKEW_REJECTED` is enforced server-side via
    `EnforceAgentVersion` and surfaced agent-side as a typed error;
    `cmd/midnight-agent/main.go` refuses to keep retrying on that code.
  - Heartbeat freshness review (`internal/.../supervisor.ReviewHeartbeat
    Freshness`) wired via `Loop.HeartbeatFreshness()`.
- `cmd/midnight-agent/main.go` rewritten to:
  cgroup-detect → Engine client → Adapter.boot() → HTTP client →
  `Loop.Register` → `Loop.Run`.

### G. Server-side handlers
- `internal/api/agent_handlers.go` — new file with **`RegisterAgentHandlers
  (mux, registry)`**. Lane 1 wires this in from a future file under
  `cmd/midnight-server/main.go`. Endpoints registered:
  - `POST /api/v1alpha/agents/register`
  - `POST /api/v1alpha/agents/{id}/heartbeat`
  - `GET  /api/v1alpha/agents/{id}/desired`
  - `POST /api/v1alpha/agents/{id}/status`
- The TODO marker is at the bottom of the file. Lane 2 does **not** modify
  `internal/api/router.go`.
- In-memory `AgentRegistry` is sufficient for the integration test; the
  durable schema lives in `migrations/0002_agents.up.sql` (new file) so
  Lane 1 can plug in repository-backed implementations without re-doing
  the schema.

### H. End-to-end test
- `cmd/midnight-agent/internal/control/loop_e2e_test.go` runs the agent's
  Loop against an `httptest.NewServer` that mounts the v1alpha agent
  handlers. Assertions: register accepted, heartbeat HMAC verified,
  reconcile pass produces a `StatusReport`. `TestAgentLoopRefusesVersion
  Skew` exercises the `VERSION_SKEW_REJECTED` path.

### I. Failure-code registry
- New typed codes added to `internal/failurecodes/codes.go` and
  `docs/error-codes.md`:
  - `AGENT_RUNTIME_UNAVAILABLE`
  - `CGROUP_V2_REQUIRED`
  - `CONTAINER_NAME_CONFLICT`
  - `DOCKER_DAEMON_BASELINE_CONFLICT`
  - `VERSION_SKEW_REJECTED`

### J. Documentation + manifest
- `docs/runtime/labels.md` — Phase 1 label/name schema (Sprint 4 S4-04).
- `versions.yaml` — added `docker_engine_api_min: "1.43"` and
  `docker_engine_min: "24.0"` (the panel-locked floor).
- `migrations/0002_agents.up.sql` + `0002_agents.down.sql` +
  `migrations/manifest.yaml` updated.

## What is deferred

- mTLS client-cert verification — config plumbing landed (`--client-cert`,
  `--client-key`, optional CA bundle), but the lane shipped HMAC as the
  authentication mechanism on the heartbeat path. Server-side mTLS
  enforcement is the one remaining stretch item.
- Image pull credential plumbing — Lane 4 owns secrets envelope
  encryption; Lane 2 leaves `PullVerify` returning `Verified=false` so the
  cosign step lands cleanly when Lane 4 plumbs the trust policy.
- Health probe runner per `FEATURE_DEPLOYMENT_STATE_MACHINE` — out of
  scope for the lane-2 slice; instance state mapping is in place
  (`mapDockerStateToInstanceState`) so the probe runner can attach in a
  follow-on slice.
- Bridge network creation in `ApplyNetworkPolicy` — Lane 2 records the
  policy and enforces tenancy, but does not yet call `NetworkCreate`. The
  follow-on slice closes Sprint 4 S4-07.
- Agent `internal/api` mTLS handshake (server-side) — currently uses the
  HTTPS+HMAC pair. Add `tls.Config{ClientAuth: RequireAndVerifyClientCert}`
  in Lane 1's wiring file once the client-cert provisioning story is in.

## Open questions for PM-A / PM-B

- **VERSION_SKEW_REJECTED window.** The current `EnforceAgentVersion`
  requires byte-equal version strings (we ship `0.0.0-dev` everywhere in
  Phase 1). Should the lane widen the window now using `versions.yaml`
  major.minor, or wait for Sprint 13's upgrade-tx work to define the
  matrix?
- **Agent bootstrap secret.** Lane 2 takes the HMAC secret via
  `--hmac-secret` / `MIDNIGHT_AGENT_HMAC_SECRET`. Lane 4's secrets layer
  needs to enroll this as a sealed value at install time; the schema in
  `migrations/0002_agents.up.sql` does **not** carry the secret material.
  Confirm the install flow's secret-bootstrapping owner.
- **Container naming.** Sprint 4 / S4-04 names containers with `dep_seq`
  and `inst_seq` integers. Lane 2 derives both deterministically from a
  64-bit FNV-1a hash of the Midnight ids so the name is stable on retry
  without depending on Lane 1's bigserial. Confirm this is acceptable for
  Phase 1; the cleanup path for Sprint 6+ (when the bigserial is
  authoritative) is to plumb `dep_seq` through `ScheduleRequest`.
- **Pre-existing test flake.** `TestRenderTraefikDynamicV2GoldenFiles` in
  `internal/router` fails on Windows due to a CRLF golden-file mismatch
  (the `--want` and `--got` byte sequences are identical when displayed).
  This was already failing on `main`; Lane 3 owns the file. Lane 2 has not
  modified anything in `internal/router`.

## Acceptance bar checklist

- [x] `go build ./...` passes.
- [x] `go test ./...` passes (only the pre-existing Lane 3 CRLF flake
      remains; not introduced by Lane 2).
- [x] `go test -tags=integration ./internal/runtime/...` builds (real
      execution requires Docker; CI on Ubuntu 24.04 will run the suite).
- [x] Agent registers, heartbeats, and reconciles a no-op desired state
      against a running midnight-server in
      `TestAgentLoopRegistersAndHeartbeatsAgainstServer`.
- [x] `daemon.json` baseline conflict produces typed
      `DOCKER_DAEMON_BASELINE_CONFLICT` error.
- [x] cgroup v1 produces typed `CGROUP_V2_REQUIRED` error with the
      panel-locked remediation string.
- [x] `go run ./tools/lint` passes.
- [x] `go run ./tools/phase0-gate` passes.
- [x] In-memory `internal/runtime/local` adapter still passes the
      conformance suite.

## Branch deliverables — quick reference

- Worktree: `D:/projects/midnight/.worktrees/phase1-lane2-runtime`
- Branch: `phase1/lane2-runtime` (off `main` at `2a64283`).
- Server registration function Lane 1 must wire in:
  **`api.RegisterAgentHandlers(mux *http.ServeMux, registry
  *api.AgentRegistry)`** in `internal/api/agent_handlers.go`. Lane 1 only
  needs to construct the `AgentRegistry` (the secret bootstrap question
  above) and call this function once after `NewRouter()` builds the v1alpha
  mux.
- Endpoints implemented (agent + server side):
  - `POST /api/v1alpha/agents/register`
  - `POST /api/v1alpha/agents/{id}/heartbeat`
  - `GET  /api/v1alpha/agents/{id}/desired`
  - `POST /api/v1alpha/agents/{id}/status`
