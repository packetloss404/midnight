# ADR-0020: Per-project private networking and built-in env vars

- Status: accepted
- Date: 2026-05-17
- Owners: platform / runtime

## Context

Services deployed to the same Midnight project + environment must be able
to address each other privately, without leaking through the public
Traefik ingress. Two surfaces drive the requirement:

1. **Developer ergonomics.** Railway's `{service}.railway.internal:PORT`
   hostname is the de-facto contract dev-portable templates expect. The
   reference-variables track (`${{Service.VAR}}`) assumes
   `MIDNIGHT_PRIVATE_DOMAIN` exists on every container.
2. **Operator scope.** The single-host MVP runs every service on one
   Docker daemon. Multi-host execution is on the roadmap (ADR-0018) but
   not implemented yet.

Today the runtime adapter launches every container on Docker's default
bridge. Containers on that bridge can reach each other by IP, but Docker's
embedded DNS resolver is **off** for the default bridge — so neither
`http://api:8080` nor `http://api.midnight.internal:8080` resolves. That
breaks both ergonomics goals above.

The longer-term answer is a WireGuard mesh per `(project, environment)`,
which gives service-to-service addressing across hosts without needing a
shared L2. That work is large and depends on the multi-host scheduler
agent path. We need the developer-visible behavior today.

## Decision

Introduce `internal/network` as the single-host private-networking surface
and wire the docker runtime adapter through it:

1. **Network naming.** Each `(project_id, environment_id)` pair maps to a
   deterministic Docker network name:

   ```
   midnight-proj-{project_id_safe}-env-{environment_id_safe}
   ```

   The name is RFC 1123-safe and capped at 63 bytes. Labels
   (`midnight.managed_by`, `midnight.project_id`,
   `midnight.environment_id`) let the reconciler rediscover networks after
   a worker restart without consulting the control plane.

2. **Provisioner interface.** `internal/network.Provisioner` exposes
   `EnsureNetwork(project, env)`, `RemoveNetwork(project, env)`, and
   `ListManagedNetworks(ctx)`. The production implementation
   (`DockerProvisioner`) calls the Docker SDK's `NetworkCreate` /
   `NetworkRemove` / `NetworkList` with a labels map for filter-based
   discovery.

3. **Container attachment.** The docker runtime adapter calls
   `EnsureNetwork` before every `ContainerCreate` and passes a
   `NetworkingConfig.EndpointsConfig` map with two DNS aliases:

   - the bare service slug (`api`)
   - the `{slug}.midnight.internal` FQDN (`api.midnight.internal`)

   Docker's embedded DNS honors both forms on user-defined bridges.

4. **Built-in env vars.** Every container receives the following
   auto-injected env vars BEFORE any user-supplied env entries:

   - `MIDNIGHT_SERVICE_NAME`
   - `MIDNIGHT_PROJECT_ID`, `MIDNIGHT_PROJECT_SLUG`
   - `MIDNIGHT_ENVIRONMENT_ID`, `MIDNIGHT_ENVIRONMENT_NAME`
   - `MIDNIGHT_PRIVATE_DOMAIN`
   - `MIDNIGHT_PUBLIC_DOMAIN` (empty when no public route promoted)
   - `MIDNIGHT_REPLICA_ID`
   - `MIDNIGHT_DEPLOYMENT_ID`

   The names mirror Railway's `RAILWAY_*` surface so templates carry
   over with a one-line `s/RAILWAY_/MIDNIGHT_/g`. Built-ins come FIRST
   in the env slice; Docker honors the last occurrence of any duplicate
   key, so user env vars override the built-ins. This matches Railway's
   precedence.

5. **Boot reconciliation.** The worker calls
   `RuntimeAdapter.Reconcile` on boot. The adapter seeds the provisioner
   cache via `ListManagedNetworks(ctx)` and then calls `EnsureNetwork`
   for every active record so an out-of-band network removal is
   self-healing.

6. **Feature flag.** Setting `MIDNIGHT_PRIVATE_NETWORK=off` on the worker
   disables network attachment but keeps the env-var injection in place.
   The flag exists so operators can diagnose default-bridge regressions
   without re-rolling images.

7. **Cleanup hook.** `RemoveNetwork` is exposed on the `Provisioner`
   interface for the future project/environment deletion flow. The
   current product surface never calls it — networks are low-cardinality
   and cheap to keep around — but the hook is wired so the deletion path
   has somewhere to land.

## Why per-`(project, environment)` scope?

- Scope-per-project alone is too wide: prod and staging would share DNS
  and a malformed staging service could resolve a prod private hostname.
- Scope-per-environment alone is too narrow: cross-service references
  inside the same project would break when one service is on `prod` and
  another on `prod-shared` (a use case the templates track relies on).
- The pair matches the existing tenancy boundary in the conformance
  suite, the route promoter, and the operations FSM.

## Why mirror Railway's `*.railway.internal` shape?

- Templates and Railway-portable apps assume the DNS suffix exists; the
  alias-with-suffix is one Docker network alias, not a separate code path.
- The reference-variables track expects `${{this.MIDNIGHT_PRIVATE_DOMAIN}}`
  to resolve to the same string the DNS resolver returns. Tying the env
  var and the alias to the same `ServiceAlias(slug)` builder keeps the
  two artifacts byte-identical.

## Why Docker bridge for now, WireGuard later?

- A user-defined Docker bridge gets per-network embedded DNS for free.
  No code in this track has to ship a DNS resolver.
- WireGuard requires kernel module presence, MTU probing, key
  distribution, and a control-plane registry of peer endpoints. Those
  pieces are scaffolded in `internal/networking` as planning contracts
  but not implemented end-to-end.
- The single-host bridge is byte-compatible with the multi-host
  contract: the `Provisioner` interface and `{slug}.midnight.internal`
  alias survive unchanged; only the implementation behind
  `EnsureNetwork` swaps from `NetworkCreate(bridge)` to "register peer
  in the project's mesh".

## Consequences

- A network create round-trip is added to the first schedule in a
  `(project, env)` scope. The cost is sub-millisecond and idempotent on
  retries.
- Containers never fall back to Docker's default bridge. Network-convergence
  failure returns `PRIVATE_NETWORK_UNAVAILABLE` before container creation.
- The `EngineClient` interface grows three methods (`NetworkCreate`,
  `NetworkRemove`, `NetworkList`) and an additive
  `ContainerCreateWithNetworking`. The existing `ContainerCreate` is
  preserved as a `netCfg=nil` shim so callers that do not need private
  networking compile unchanged.
- The `runtime.ScheduleRequest` value object gains `ServiceSlug`,
  `ProjectSlug`, `EnvironmentName`, `Env`, and `PublicDomain` fields.
  All are optional with explicit zero-value semantics so existing test
  fixtures keep compiling.

## Out of scope

- WireGuard mesh, peer discovery, conntrack flushing, and MTU probing.
  Planning contracts already exist in `internal/networking`; the
  execution path lands with the multi-host scheduler.
- Project / environment deletion. The cleanup hook is wired but unused.
- Egress controls and DNS-policy enforcement.
- Cross-project ingress (we intentionally do NOT allow services on
  different projects to address each other privately; the public Traefik
  ingress is the only cross-project surface).

## Links

- Related ADRs: ADR-0017 (GraphQL gateway), ADR-0018 (scheduler
  interface).
- Implementation: `internal/network/`, `internal/runtime/docker/adapter.go`.
- Operator doc: `docs/networking/private-networking.md`.
