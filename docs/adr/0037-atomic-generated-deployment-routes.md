# ADR-0037: Human deployment intake owns one atomic generated route

- Status: accepted
- Date: 2026-07-29
- Amendment: ADR 0040 supersedes the one-deployment ownership and fixed
  initial-port assumptions for service-managed generated domains. This ADR
  remains authoritative for the private-by-default deployment opt-in and
  evidence-aware URL projection.

## Context

Loop 2 needed the smallest honest Railway-style path from a newly created
service to a public URL. Midnight already had durable domains, HTTP routes,
backends, Traefik publication, acknowledgement, and trusted ingress probes, but
the human deployment contract rejected all route input. Exposing the broader
operator `/domains` API would have admitted custom hosts, TLS intent, and
non-atomic side effects without the tenant coordinator required by the product
boundary.

Route state is scoped to a service/environment, while user intent belongs to
one exact deployment. Inferring intent from scope alone would make a later
private deployment appear public or cause a redeploy of a private revision to
inherit another revision's route.

## Decision

A human immutable-image deployment may omit `route` and remain private, or
submit exactly:

```json
{"route":{"public":true}}
```

All route identity is server-owned. The API derives one generated hostname
under `MIDNIGHT_APPS_DOMAIN`, path `/`, the current immutable service target
port (or the server default when no deployment snapshot exists), and TLS
disabled. Human sessions cannot supply a hostname, route name, controller
group, path, target port, custom domain, or TLS setting. Missing apps-domain
configuration returns an honest unavailable response before identities or
durable artifacts are minted.

Migration 0048 adds `deployment_route_intents`. The human deploy coordinator
creates or converges the generated domain and HTTP route, creates the canonical
deployment, binds that exact deployment to the route, commits outbox work and
the exact idempotent response, and appends audit evidence in one serializable
PostgreSQL transaction. Audit failure rolls the entire set back.

The original worker independently converged the same generated intent from the
durable payload. ADR 0040 and migration 0052 supersede that recovery rule:
generated payloads keep `host` empty, but an exact durable deployment binding
is now their only worker authority. Intake creates that binding atomically, and
a worker must not reconstruct a missing generated resource from an old payload
after its owner deletes it. Host-bearing custom recovery intents retain
deterministic convergence.

Canonical deployment reads batch by already-authorized deployment IDs.
`route_intent` is the requested hostname. `route_apply_state` reports durable
controller state. `route_reachable` becomes true, and `route` becomes an HTTP
URL, only when all of these are current:

1. the exact deployment has a ready route backend;
2. desired, observed, and applied group revisions agree;
3. the publication observation matches the current artifact hash and revision;
4. Traefik acknowledged the router; and
5. the trusted Host-header ingress probe succeeded.

The verifier probes every published route before a group can become ready.
This is trusted-ingress evidence, not proof that public DNS or an Internet
client can reach the hostname.

## Consequences

- First-deploy GUI and CLI flows can opt into a generated public URL while
  remaining private by default.
- Exact response replay, current-role revalidation, cross-tenant concealment,
  action preservation, and audit rollback share the existing tenant mutation
  guarantees.
- ADR 0040 keeps an existing generated hostname stable across apps-domain
  configuration changes until an explicit rotation workflow is designed.
- Custom domains, TLS/certificate issuance, path/port editing, and
  service-managed TCP proxies remain later capabilities.
- Installed DNS, firewall/NAT, HTTPS, and off-host reachability still require
  their external acceptance runners and must not be inferred from this gate.
