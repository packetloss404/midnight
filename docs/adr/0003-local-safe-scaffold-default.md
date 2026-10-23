# ADR-0003: Local-Safe Scaffold Seams Are The Phase 0 Default; Live Adapters Stay Behind Opt-In

- Status: superseded in practice (2026-09-04); retained as Phase 0 history
- Date: 2026-04-26
- Owners: PM-A Delivery Coordination, S01 Foundations

> **Superseded note.** The shipped binaries no longer follow the opt-in rule
> below. `midnight-worker` talks to the Docker Engine, `midnight-buildkit`
> drives a real BuildKit daemon and pushes to an OCI registry, and the worker
> publishes Traefik routes, all enabled by default (see
> `docker-compose.local.yml`, `deploy/`, and `docs/dev-local-stack.md`). The
> remaining opt-in gates are narrower and documented where they live: DNS
> mutation (`MIDNIGHT_WORKER_DNS_APPLY`), ACME production issuance, and the
> agent mutation flag. Memory mode (`--storage=memory`) still exists for
> smoke checks but is not the default. No replacement ADR was written; this
> note records the drift so the index stays honest.

## Context

Phase 0 produced runnable seams for the API, agent, build, registry, ingress,
and support lanes by routing every external dependency (Docker, BuildKit,
ACME, KMS, registries, real DBs, route writers) through an injected interface
that the production binaries fulfill with an in-memory or file-backed
implementation. The Phase 0 worker outputs in `STATUS.md` confirm that PM-A
and PM-B accept these local-safe adapters as the gate-passing default.

PM-A's "Post-Phase 0 PM-A Actions" anticipate promoting those adapters to
real Docker, BuildKit, registry, ACME, and route execution, but does not
specify *when* a live adapter is allowed to ship.

The risk of skipping this decision is silent regression: a feature lane could
swap a local seam for a live network call, the gate would still pass on a CI
runner that happens to have the dependency installed, and the alpha install
on a design-partner host would behave differently than the gate predicted.

## Decision

The local-safe scaffold seams stay the default for every Phase 0 binary. A
lane that wants to enable a live adapter must:

1. Land an ADR that names the live dependency, the failure mode it
   introduces, and the rollback strategy.
2. Gate the live behavior behind an explicit `--enable-live-<adapter>`
   process flag (or an equivalent named feature flag in the agent config).
3. Default the flag to `false` in every shipped binary so a fresh install,
   `make release-check`, and `go run ./tools/phase0-gate --execute-drills`
   never invoke the live path.
4. Wire a drill in `test/drills/manifest.yaml` that exercises both modes,
   with the live mode guarded by the same flag so CI can opt out cleanly.

This decision applies to Docker daemon access, BuildKit invocation, OCI
registry pushes, ACME order execution, KMS calls for release signing, and any
network-bound observability backend.

## Consequences

- A local clone, a CI runner, and the `release-check` workflow all behave
  identically because no implicit live path can fire.
- Feature lanes pay a small cost for each live promotion (an ADR plus a flag
  plumbing change), which the gate documents up-front rather than discovering
  during an incident.
- The `LINT_SOURCE_GUARD_FAILED` and `LINT_REGISTRY_GUARD_FAILED` reason
  codes inherit responsibility for catching accidental live calls when paired
  with the `forbid panic()` and `forbid os.Exit` guards in `tools/lint`: a
  live adapter that swallows an error with `panic()` outside `cmd/*/main.go`
  fails the gate even before runtime testing.
- The post-Phase 0 PM-A action "promote S02/S04/S05/S06 adapters from
  local-safe seams to live Docker, BuildKit, registry, ACME, and route
  execution behind explicit opt-in gates" is the first consumer of this ADR.

## Links

- Related panel decisions: Midnight supply-chain hygiene (lockfiles required;
  `govulncheck`, `osv-scanner`, `npm audit --omit=dev`); SLSA v1.0 in-toto
  provenance with no claimed Build Level until audited.
- Related sprint stories: MIDNIGHT-S01-001..011 (Phase 0 scaffold);
  MIDNIGHT-S02-* (preflight host probes); MIDNIGHT-S04-* (agent loop);
  MIDNIGHT-S05-* (build/registry).
- Related implementation PRs: Phase 0 scaffold commit `de15c68`.
