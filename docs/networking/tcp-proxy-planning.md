# Executable single-host TCP proxy

Midnight implements the first executable TCP-publication slice behind the
`/api/v1alpha/tcp-proxies` REST resource, `midnight tcp-proxy`, and the service
settings GUI. The flow follows Railway's useful product shape: the caller
selects a service environment and supplies its internal port; Midnight
allocates an external port and continuously resolves the active deployment's
ready replicas. Railway's current behavior is documented in
[TCP Proxying](https://docs.railway.com/networking/tcp-proxy).

This is deliberately a single-host capability. It is not yet the same claim as
Railway's managed public edge.

## Product flow

1. The user presses **Expose TCP** or calls the REST/CLI create operation with
   the project, environment, service, and internal target port.
2. The API verifies that the scope currently has at least one ready instance
   from its active deployment. Callers cannot supply a backend hostname or IP.
3. PostgreSQL serializes allocation and reserves a generated descriptive
   hostname plus a port from the configured range. Concurrent requests cannot
   claim the same host, port, or service environment.
4. The Docker deploy worker joins only the labeled environment bridge for the
   desired endpoint, resolves every ready active replica, binds the listener,
   and probes the backends.
5. `active` is recorded only after the listener is bound and at least one
   backend accepted a TCP connection. New connections are distributed across
   the currently reachable replicas with failover.
6. Every reconciliation re-resolves the active deployment. A redeploy replaces
   stale container IPs without rebinding the public listener.
7. Withdrawal closes the listener, records `withdrawn`, releases the durable
   allocation, and detaches the worker from an environment bridge when no
   desired endpoint needs it.

Desired state and observed evidence survive process restarts. Listener bind
collisions, unreachable backends, missing active instances, and bridge
enrollment failures are recorded as bounded, non-sensitive error codes/details.

## Evidence boundaries

| Field | What it proves |
|---|---|
| `listener_bound` | The configured worker namespace owns the local TCP socket. |
| `backend_reachable` | At least one currently resolved ready replica accepted a TCP probe. |
| `apply_state=active` | Both local facts above were observed for current desired state. |
| `dns_provisioned=false` | Midnight did not mutate authoritative DNS. |
| `public_reachability_verified=false` | Midnight did not prove an internet path through operator firewall/NAT. |

The generated hostname is an allocation label until an operator publishes its
DNS record. A host port mapping, cloud security-group rule, router/NAT rule, or
equivalent is also operator-owned outside the local Compose profile.

## Observed-peer restrictions

An omitted or empty `allowed_source_cidrs` list creates an unrestricted public
listener, matching the expected TCP-proxy workflow. Optional CIDRs restrict the
peer address the listener actually observes.

This is not guaranteed to be the original internet client address. Docker
bridge port publishing commonly rewrites a host connection to the bridge
gateway or a Docker Desktop VM address. Real probes observed `172.17.0.1` on a
default bridge and `192.168.65.1` in the scoped-network integration flow,
rather than `127.0.0.1`; the address can also differ from the container's own
bridge gateway. Operators who enable a restriction must derive the CIDR from
their real deployment path. Midnight does not claim original-client source-IP
filtering without a future PROXY-protocol or host-network firewall design.

## Runtime topology and configuration

The API remains only on the control network. The privileged Docker deploy
worker owns the listeners because it already has the narrow authority required
to attach its explicitly named container to scoped workload bridges.

The local Compose profile configures:

- `MIDNIGHT_TCP_PROXY_BASE_DOMAIN=apps.localhost`
- API allocation range `MIDNIGHT_TCP_PROXY_PORT_START` through
  `MIDNIGHT_TCP_PROXY_PORT_END` (default `20000-20099`)
- worker `MIDNIGHT_TCP_PROXY_BIND_HOST=0.0.0.0`
- worker `MIDNIGHT_TCP_PROXY_CONTAINER=midnight-deploy-worker`
- a bounded loopback-only Docker host publication for the same port range

Production operators must publish the same bounded range on the worker host
and set DNS/firewall/NAT deliberately. A systemd worker is not enrolled into a
Docker bridge and therefore leaves this controller disabled unless the
operator supplies an equivalent supported topology.

## Current limitations

- Single Docker host only; no multi-host listener scheduling or failover.
- No authoritative DNS mutation or DNS ownership verification.
- No firewall, security-group, router, or NAT mutation.
- No internet reachability probe and no L4 TLS termination.
- Existing connections remain on their selected replica; only new connections
  use the freshly reconciled backend set.
- Optional CIDRs apply to the post-NAT observed peer, not necessarily the
  original client.

Database templates keep TCP exposure disabled by default. Choosing a template
never creates an endpoint; exposure remains an explicit user action.
