# ADR-001: Go for control plane, host agent, and CLI

- Status: Accepted (from panel review)
- Date: 2026-04-25
- Deciders: Eight-Specialist Panel
- Tags: [phase-0, foundations, language-choice, supply-chain, dx]

## Context

Midnight's promise is "deployment comprehension and control-plane correctness" on a single VM, with Postgres as source of truth, durable jobs, host agents that talk to the Docker Engine API, signed `.deb` packaging, and a CLI that powers automation, IaC, and CI integrations (`midnight-platform-plan.md` lines 9–26, 332–418). The platform must ship as **signed `.deb` packages and systemd units** with a **static binary host agent** (lines 34, 944–954), enforce a **Docker Engine API floor of v1.43** through the official Engine API client (line 90), and produce **CLI output that is machine-parseable** with stable check IDs and exit codes (lines 181–188). Webhook delivery, ACME, registry, and AI provider HTTP must share a single **SSRF-safe outbound HTTP client** (line 124). The build/supply-chain panel further requires `govulncheck` and `osv-scanner` as required CI checks (line 133).

These constraints converge on a single language for the control-plane server, the worker pool, the host agent, and the CLI: a language with first-class support for the Docker Engine API SDK, BuildKit's gRPC client, Postgres drivers with `FOR UPDATE SKIP LOCKED` lease support, static cross-compilation for Linux x86_64/arm64, ergonomic context-cancellable concurrency, an OpenAPI/JSONSchema toolchain, and a mature vulnerability-scanning ecosystem. The CLI must also be the same binary surface ops and CI use, so embedding the API client types in the CLI (line 396) is a hard requirement.

## Decision

Midnight's control-plane server, durable-job workers, host agent, and CLI are all written in **Go**.

> "Backend/control plane: Go. … CLI: Go, sharing API client types where possible. Host agent: Go static binary." (lines 394–397)

The dashboard remains TypeScript + React (line 395). Generated SDKs (line 171) and the Terraform provider follow the language-specific Go templates that v1 grad rubric requires.

This ADR scopes only the control-plane Go decision. Builder pinning is ADR-008-adjacent; runtime is ADR-004; queue is ADR-003.

## Alternatives Considered

- **Rust for the agent and CLI, Go for the server.** Rust offers smaller static binaries and stronger memory-safety guarantees at the trust boundary. Rejected because (a) the Docker Engine API and BuildKit ecosystems' first-class clients are Go; (b) doubling the language footprint doubles the supply-chain attack surface (`cargo audit` *and* `govulncheck` *and* `osv-scanner`) without proportional benefit at our threat model — rootful Docker is host-root-equivalent (line 64, line 894); (c) Nixpacks lives in a Rust codebase that is in maintenance mode (line 18) — we do not want to repeat that risk shape. The agent's privilege boundary is the Docker daemon, not the Go runtime.
- **TypeScript / Node.js end to end.** Rejected: no acceptable static-binary story for the host agent, GC pause behavior is incompatible with the agent's heartbeat contract (line 102, heartbeat = lease_ttl/3), and the Postgres `SKIP LOCKED` ecosystem is weaker. The dashboard remains TypeScript/React, but using Node.js for the control-plane server forces a second runtime in the appliance install image and a second supply-chain audit path.
- **Java / Kotlin (JVM).** Rejected: JVM startup and footprint are wrong for a single-VM appliance with a 15-minute install promise (line 191), and the agent must run on small VPS hosts where 256 MiB RSS is non-negotiable. JVM also adds a graceful-shutdown story that does not compose well with the agent's lease semantics.
- **Python.** Rejected: no static-binary path; concurrency model (asyncio + threads) fights the lease-and-heartbeat semantics of the queue. The Docker Engine API client (`docker-py`) lags the Go reference client in version coverage.
- **Go for control plane + a separate language for the CLI (e.g., Rust CLI for smaller binaries).** Rejected: panel-locked CLI normative behavior includes shared API client types (line 396) and a deterministic `tar.zst` upload format (line 183); duplicating those across two languages doubles the contract-test surface for zero shipping benefit. The CLI also embeds OpenAPI-generated types that the server emits — a single Go toolchain keeps that loop closed.

## Consequences

Positive:

- One language and one toolchain across server, worker, agent, CLI; one set of generated types feeding OpenAPI clients, MCP server, and CLI commands.
- Mature ecosystem for every component we need: `pgx` (Postgres `SKIP LOCKED`), `docker/docker/client` (Engine API), `moby/buildkit` (gRPC), `go-acme/lego` (ACME), `traefik/traefik` (we run Traefik out-of-process — see ADR-006), `spf13/cobra` (CLI), `go-chi/chi` or `gorilla/mux` (HTTP).
- Static cross-compilation gives us reproducible `.deb` artifacts (ADR signing flow can verify byte-for-byte builds).
- `govulncheck` and `osv-scanner` give panel-required vulnerability gating in CI (line 133).

Negative:

- Generic-collection ergonomics are weaker than Rust/TypeScript; we adopt code generators (sqlc, oapi-codegen) to compensate.
- Memory-safety bugs are still possible at the cgo boundary (libpq, libsodium for KEK rotation). We forbid cgo wrappers without a documented review (covered in security-review checklist).
- A single language increases blast radius of a Go-runtime CVE; we mitigate via `govulncheck` blocking CI and a documented Go version pinning policy.
- Hot-reload during development is weaker than Node/Python; we accept this tradeoff and provide `air`-based watcher configs for the inner dev loop.
- Long-running goroutines and context propagation discipline are required for graceful shutdown; reviewed in `CR-LANG-02` checklist.

Follow-up work:

- **ADR-protected:** Go toolchain version is pinned in `go.mod`; minor version upgrades require a CI green build and a release-notes entry.
- Code generators (`sqlc`, `oapi-codegen`, optional `mockery`) selected and pinned in `tools/tools.go` before Sprint 1 ends.
- CLI binaries and host-agent binary built as `CGO_ENABLED=0` static binaries in CI (covered by SPRINT_01).
- Lockfile policy: `go.sum` is committed and verified; `npm` lockfiles for the dashboard are pinned by integrity hash (panel: line 133).
- Code-style tooling pinned: `gofumpt`, `golangci-lint` with a curated linter set, `staticcheck`. Configuration shipped in the repo.
- A long-form Go style guide note in `TEAM_ONBOARDING.md` covers context propagation, error wrapping, and the `pgx` transactional outbox pattern.

## Compliance / Verification

Concrete gates a reviewer or auditor checks:

- **CI gate `gate.go-version-pinned`:** the `go` directive in `go.mod` matches the version baked into `.github/workflows/build.yml`; mismatch fails CI.
- **CI gate `gate.govulncheck`:** `govulncheck ./...` runs as a required check on every PR; non-zero exit blocks merge.
- **CI gate `gate.osv-scanner`:** `osv-scanner --lockfile go.mod` runs as a required check; non-zero exit blocks merge.
- **CI gate `gate.static-binary`:** the host agent and CLI binaries produced in CI link no dynamic libraries (`ldd ./midnight-agent` returns "not a dynamic executable"); regression fails the release job.
- **Code review checklist item `CR-LANG-01`:** any introduction of a non-Go runtime in the server, worker, agent, or CLI requires an ADR amendment and security sign-off.
- **Test name `TestSDKTypeShared`** in `pkg/api/clientgen` asserts the generated Go SDK and the CLI consume the same `oapi-codegen` output; a divergence fails the build.
- **Runbook check `OR-CHK-LANG-01`** in `midnight doctor`: reports the agent's Go runtime version and refuses to start if outside the supported window (panel: line 90 sets a precedent for `DOCKER_ENGINE_UNSUPPORTED`-style version gating).

## Build and Distribution Implications

The single-language decision constrains how artifacts are built and shipped:

- All Go binaries built with `CGO_ENABLED=0` to produce truly static ELFs that ship cleanly inside the panel-locked `.deb` packaging (line 34) and run on any Tier 1 distro without library version concerns.
- The `.deb` ships `midnight-server`, `midnight-worker`, `midnight-agent`, and the `midnight` CLI — four binaries, one toolchain.
- Cross-compilation for `linux/amd64` and `linux/arm64` is a single CI matrix; multi-arch image opt-in (panel: line 81) extends the same matrix without language complications.
- Reproducible builds are tractable: `-trimpath`, `-buildvcs=false`, fixed `BUILD_DATE` from the source commit, and a documented Go toolchain version produce bit-identical artifacts in CI.

## References

- spec: lines 34, 81, 90, 124, 133, 332–418, 394–397, 944–954, 181–188, 191
- features: `FEATURE_POSTGRES_JOB_QUEUE.md`, `FEATURE_DAEMON_JSON_BASELINE.md`
- sprints: `SPRINT_01_FOUNDATIONS.md`, `SPRINT_02_INSTALLER.md`, `SPRINT_03_CONTROL_PLANE_API.md`, `SPRINT_04_HOST_AGENT.md`
- handoff: `TEAM_ONBOARDING.md`, `QUALITY_AND_TEST_STRATEGY.md`
