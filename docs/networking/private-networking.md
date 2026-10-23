# Private Networking Foundations

Private networking starts with pure planning contracts for WireGuard and the internal DNS resolver. These contracts are intentionally offline: they do not create interfaces, write WireGuard configuration, run DNS servers, flush conntrack, or inspect host state.

## WireGuard

- `internal/networking.WireGuardInterfacePlan` models one private interface, its CIDR, listen port, MTU, and peers.
- `NormalizeWireGuardInterfacePlan` validates addresses and peers and applies the PMTU-safe default MTU of `1380`.
- Non-default MTUs are rejected in this slice so callers cannot accidentally regress PMTU behavior while host integration is still pending.
- `PlanPMTUProbe` turns already-collected PMTU observations into a selected MTU, defaulting to `1380` when no observation exists and marking degraded per-link override plans below the default.
- Peer allowed IPs are validated as CIDR prefixes, deduplicated by peer ID, and sorted for deterministic evidence.

## Key Rotation

- `internal/networking.KeyRotationPolicy` requires a positive interval and a positive overlap shorter than the interval.
- `PlanKeyRotation` returns the next rotation time, whether rotation is due, and `OverlapUntil` for the period where old and new peer keys should both be accepted.
- `PlanPeerKeyRotation` plans current/new public-key overlap and old-key retirement timing; callers still provide the next public key.
- The planner only schedules rotation. It does not generate keys or distribute configuration.

## Peer State

- Peer lifecycle states are `absent`, `pending-add`, `active`, `pending-remove`, and `removed`.
- Add flow: `absent` or `removed` plus `request-add` plans `write-peer-config` and `await-peer-handshake`, then `pending-add` plus `add-applied` marks the peer `active`.
- Remove flow: `active` or `pending-add` plus `request-remove` plans `remove-peer-config` and `flush-peer-conntrack`, then `pending-remove` plus `remove-applied` marks the peer `removed`.
- Invalid transitions return errors and produce no mutation plan.

## Conntrack Flush

- Peer removal plans include `ConntrackFlushPlan` with the peer ID and allowed IPs.
- `PlanConntrackFlush` emits intended `conntrack -D -d <cidr>` and `conntrack -D -s <cidr>` commands for a removed peer.
- The flush plan exists because stale NAT/session state can survive WireGuard peer removal.
- Execution is deliberately left to a future host integration that can apply platform-specific safety controls.

## Internal DNS Resolver

- `internal/networking.DNSResolverPlan` models resolver listen address, port, zone, search domains, upstreams, and A/AAAA records for private peers and services.
- `PlanInternalDNSResolver` defaults the resolver port to `53`, normalizes DNS names to lowercase without trailing dots, deduplicates search/upstream lists, validates record IP addresses, and sorts records deterministically.
- `CheckInternalDNSResolverReadiness` validates the normalized resolver plan has records and any caller-required internal record names before a host integration starts or advertises the resolver.
- The resolver planner does not perform DNS lookups and does not start a DNS process.

## Single-host Service-to-service Networking (active)

Per ADR-0020, the single-host scheduler provisions a dedicated user-defined Docker bridge per `(project_id, environment_id)` pair so services within the same project + environment can address each other over Docker's embedded DNS resolver. The WireGuard mesh described above is the multi-host replacement and is intentionally deferred.

### Network naming

The deterministic Docker network name is:

```
midnight-proj-{project_id_safe}-env-{environment_id_safe}
```

`{*_safe}` is the lowercased ULID body (the `prj_`/`env_` prefix is stripped) with any non-alphanumeric characters removed. The full name is capped at 63 bytes (RFC 1123); when the inputs exceed the cap the environment token is trimmed first because the project_id is more semantically meaningful to operators.

Every Midnight-managed network carries three labels:

| Label | Value |
| --- | --- |
| `midnight.managed_by` | `midnight-network-provisioner` |
| `midnight.project_id` | full project id |
| `midnight.environment_id` | full environment id |

The reconciler filters by `midnight.managed_by` to rediscover networks after a worker restart, then matches on the project + environment labels.

### DNS aliases

Every container joins its project network with two aliases:

1. The bare service slug (`api`).
2. The `{slug}.midnight.internal` FQDN (`api.midnight.internal`).

Docker's embedded DNS resolver honors both forms, so peer services can connect using either `http://api:8080` or `http://api.midnight.internal:8080`. The FQDN form is preferred — it matches the auto-injected `MIDNIGHT_PRIVATE_DOMAIN` env var and is portable to the multi-host mesh.

### Auto-injected env vars (`MIDNIGHT_*`)

The runtime injects the following env vars on every container BEFORE any user-supplied env entries. Docker honors the last occurrence of a duplicate key, so user-supplied env vars override the built-ins.

| Variable | Source |
| --- | --- |
| `MIDNIGHT_SERVICE_NAME` | service slug |
| `MIDNIGHT_PROJECT_ID` / `MIDNIGHT_PROJECT_SLUG` | project id / slug |
| `MIDNIGHT_ENVIRONMENT_ID` / `MIDNIGHT_ENVIRONMENT_NAME` | environment id / name |
| `MIDNIGHT_PRIVATE_DOMAIN` | `{slug}.midnight.internal` |
| `MIDNIGHT_PUBLIC_DOMAIN` | assigned public route hostname, empty when no route promoted yet |
| `MIDNIGHT_REPLICA_ID` | container hostname / instance id |
| `MIDNIGHT_DEPLOYMENT_ID` | current deployment id |

These mirror Railway's `RAILWAY_*` surface so dev-portable templates (and the reference-variables track) can resolve `${{this.MIDNIGHT_PRIVATE_DOMAIN}}` directly.

### Testing service-to-service connectivity

From any running container in the same project + environment:

```bash
# By bare slug:
curl http://api:8080/healthz

# By FQDN (preferred):
curl http://api.midnight.internal:8080/healthz

# Inspect the embedded DNS:
nslookup api.midnight.internal
```

Containers on the **default** Docker bridge cannot resolve these names — Docker's embedded DNS only fires on user-defined networks.

### Boot-time reconciliation

On worker startup the runtime adapter calls `Provisioner.ListManagedNetworks(ctx)` to seed its in-memory cache from existing labelled networks. The first `Reconcile` pass then calls `EnsureNetwork(project, env)` for every active deployment record, restoring any network that was removed out-of-band. The reconciler never deletes networks — they are low-cardinality and cheap to keep around.

### Failure behavior

Private-network convergence is a required isolation boundary. If Midnight
cannot create or discover the project/environment bridge, scheduling returns
`PRIVATE_NETWORK_UNAVAILABLE` before container creation. There is no
default-bridge fallback. The historical `MIDNIGHT_PRIVATE_NETWORK=off` setting
is ignored and retained only so older configuration inventories can identify
and remove it.

### Multi-host extension note

The single-host bridge is a temporary implementation of the same API surface. The multi-host scheduler (future track) replaces the Docker bridge with a WireGuard mesh per project + environment; the `Provisioner` interface, `{slug}.midnight.internal` aliases, and `MIDNIGHT_*` env vars stay byte-identical so services do not need to be re-deployed when the topology changes.

## Out Of Scope

This slice does not include multi-host scheduling, TCP proxying, registry caching, volume attach/detach, Kubernetes adapters, Pulumi providers, workload identity, or CI adapters. Multi-host WireGuard mesh execution remains a future track; the planning contracts in this file are the substrate it will reuse.
