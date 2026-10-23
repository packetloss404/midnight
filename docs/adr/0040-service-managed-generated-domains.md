# ADR-0040: Generated HTTP domains are service-managed tenant resources

- Status: accepted
- Date: 2026-07-29

## Context

ADR 0037 admitted one generated route only as an optional property of one
human image deployment. That was enough for a private-by-default first-deploy
flow, but not for Railway-style domain management: a user could not list,
create, inspect, or delete the service endpoint independently, and a later
deployment inherited only an exact source deployment's route intent.

Midnight already stores route publication as a route-group state machine. A
domain lifecycle therefore has to preserve exact deployment ownership while
also making the service/environment's current endpoint durable across future
deployments. It must not move the canonical active pointer merely because a
route was requested.

## Decision

Migration 0052 makes the generated HTTP endpoint a service-managed resource:

- `service_domain_bindings` identifies the generated domain and route owned by
  one exact project/service/environment;
- `deployment_route_intents` uses `(deployment_id, route_id)` identity so one
  deployment can own several exact routes;
- at most one generated domain exists for a service/environment; and
- domain resource versions cover tenant-owned desired-state domain and route
  mutations.

Each product-created domain incarnation receives a fresh opaque `dom_*`
identity while the generated hostname and `rte_*` route identity remain stable.
Creation may instead adopt the actual ID of an already-compatible,
never-retired recovery-generated singleton, but it never rewrites incompatible
hostname, apps-domain, custom, or TLS intent. IDs are minted once outside
SERIALIZABLE retry loops.

`retired_product_domain_ids` durably records the exact identity and scope
before a human product delete cascades the live domain and route state. Product
creation rejects a proposed or recovery-adopted ID found in that ledger. A
recovery operator may reconstruct the old row for diagnosis, but the nested
product lifecycle cannot adopt it and a stale nested delete cannot target it.

The HumanSession product surface is limited to:

```text
GET|POST /services/{service_id}/environments/{environment_id}/domains
GET|DELETE /services/{service_id}/environments/{environment_id}/domains/{domain_id}
```

The server owns the generated hostname, root path, target port, public HTTP
intent, controller group, and disabled-TLS state. Reads require current
`domains.read`; writes require current `domains.write`. Restricted
environments are concealed from non-admin humans. Mutations use bounded strict
JSON, singleton idempotency, exact-target replay with current reauthorization,
positive resource-version delete CAS, atomic audit, and serializable retries.
The flat domain API remains an installation-recovery surface.

Creation binds the service-managed route to every exact open deployment in
`queued`, `building`, `deploying`, or `active` state. Deploy intake and
redeploy/rollback inheritance bind future open deployments as part of their
canonical transaction. Existing eligible generated routes and open
deployments are conservatively backfilled during upgrade; ambiguous legacy
generated singletons fail migration with an actionable diagnostic.

Worker startup performs an additional derived-state reconciliation after
runtime-instance recovery and before route-controller or queue work. It treats
`service_domain_bindings` as the only authority, inserts missing exact intents
for open deployments, and reconstructs each eligible route's backend set from
the canonical active deployment's ready, running instances. The normal
per-environment mutation lock is followed by sorted service/environment deploy
locks, and group, route, active-pointer, and ready-instance reads plus backend
replacement occur in the same transaction. This makes concurrent promotion
and repeated startup repair safe and idempotent. It is a runtime backfill, not
a schema migration. It intentionally excludes loose
custom rows and custom bindings without a verified ownership challenge.

Deploy payloads cannot establish that authority. The HumanSession path retains
the generated-domain product lifecycle. Broad operator/automation route fields
are only selectors: hostless selects exactly one eligible generated binding;
host-bearing selects an exact ownership-verified custom binding. Both intake
and the worker fail closed on missing, ambiguous, ownership-unverified, or
TLS-pending state, and the worker copies the exact binding instead of creating
hostname authority from the payload.

Migration 0052 refuses downgrade while any service-managed domain binding or
retired product-domain identity exists. Schema 0051 cannot preserve product
identity, its resource-version CAS epoch, or the retirement fence. Dropping and
later recreating those records could admit a stale pre-downgrade mutation.
Retirement history therefore requires staying on schema 0052 or restoring from
a pre-product-domain backup rather than erasing the fence.

Route promotion updates all eligible routes as one group-CAS operation. A
blocked or unverified sibling fails the deployment's routing gate.
Service/environment deploy state and route mutation use shared advisory-lock
orders before row locks. Canonical promotion rechecks exact route intent,
candidate backend readiness, current desired/observed/applied revisions,
matching publication observation, Traefik acknowledgement, and trusted ingress
probe evidence before moving the active pointer.

Durable Traefik router and service identities include an opaque token for the
exact route-group revision. Publication verification recomputes the rendered
artifact hash and requires Traefik's active rule, service reference,
entrypoints, middleware/TLS shape, complete backend URL set, and backend health
to match. An overlapping older file-provider router with the same rule fails
the gate before the trusted Host-header probe. A cross-process,
project/environment-scoped advisory file lock covers the desired-state load,
artifact replacement, acknowledgement, probes, and final revision CAS so a
stale reconciler cannot overwrite the external artifact inside that evidence
window.

PostgreSQL COMMIT acknowledgement can be indeterminate after the server has
applied a canonical promotion. Normal and template worker committers
compensate only after a definite failure or after exact post-error proof fails.
Recovery proof binds the operation and deployment to canonical active state,
the expected prior deployment to superseded state, every public route to
candidate-only ready backends at the observed revision, and the matching
Traefik/probe observation. Template promotion additionally requires terminal
template verification evidence.

When a target port changes, the newly introduced candidate backend must match
the new route port. A backend captured from the prior active revision may keep
its historical port during zero-downtime stage or compensation; arbitrary new
historical-port backends are rejected.

## Consequences

- A human can add or remove one stable generated HTTP endpoint independently
  of deployment creation through the API, CLI, and Networking panel.
- Current and future deployments carry exact route identities; the system does
  not infer publication from loose service/environment coincidence.
- Domain creation may make a route pending, but never claims it reachable
  before current controller, Traefik, and trusted-ingress evidence exists.
- `version` is desired-state delete CAS input, not a strong representation
  validator; route-group evidence may change without a domain mutation.
- Delete/recreate returns a new domain ID. A delete targeting the older
  incarnation is not found even when the new row's `version` is again `1`.
  Compatible recovery identity is adopted only when it has never been retired
  from the product lifecycle.
- Workers use exact deployment-route bindings for generated product routes and
  cannot resurrect a deleted domain from a stale host-empty job payload.
- A pre-domain-lifecycle active deployment can acquire a generated endpoint
  through the supported domain API and converge on worker startup without
  manual database repair. Re-running startup repair leaves equivalent intents,
  backends, revisions, and observations unchanged.
- Unchanged private route passes retain their durable controller timestamps and
  apply-attempt count while still observing and repairing the external file.
- Human and recovery GUI authority remains explicit: tenant sessions use the
  nested generated-only lifecycle, while operator recovery retains the flat
  generated/custom/TLS compatibility surface.
- Generated hosts remain stable across apps-domain configuration rotation
  until an explicit future rotation design exists.
- Custom host ownership, editable route properties, TLS issuance/renewal,
  delegated DNS, public-Internet reachability, and installed HTTPS proof remain
  separate gates.
