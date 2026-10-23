# Durable HTTP routing

Migration 0022 introduced the single-host desired-state foundation for
generated/custom domains and Traefik file-provider routing. Migration 0052 and
ADR 0040 add the tenant-safe generated-HTTP product slice and exact
service-managed, multi-route deployment ownership.

Cloudflare is the first implemented delegated-DNS adapter. Its provider-neutral
controller remains opt-in and dry-run by default, while the ACME engine keeps
its separate DNS-01 provider boundary; see
[`cloudflare-dns.md`](cloudflare-dns.md).

## Durable truth

- `domains` owns a globally unique normalized hostname and carries composite
  project, environment, and service scope. `resource_version` covers
  tenant-visible domain and route changes.
- Generated domains must be exactly one DNS label beneath the configured apps
  domain so `*.apps.example.com` is never represented as covering
  `service.environment.apps.example.com`.
- `http_routes` is private by default. Public exposure requires
  `public_enabled = true`.
- `service_domain_bindings` marks an exact generated or custom domain/route
  managed by one service/environment. A generated singleton is enforced per
  scope; custom bindings remain private until exact ownership is verified.
- Migration 0052 refuses downgrade while any service-managed binding remains;
  schema 0051 cannot preserve its product identity or resource-version CAS
  epoch. Delete or convert tenant product domains before downgrading.
- `deployment_route_intents` uses `(deployment_id, route_id)` identity. Domain
  creation backfills exact open deployments, and later deploy/redeploy/
  rollback transactions inherit the service-managed binding.
- `route_backends` stores deployment/instance targets and their readiness
  observation with full composite scope.
- `route_controller_groups` owns desired, observed, and file-applied revisions,
  the artifact hash, apply attempts, and support-safe error state.

The reconciler renders every safe route in an environment group to one
validated file and replaces that file atomically. It observes the actual file
on every pass, including already-applied revisions, so startup and periodic
passes repair a deleted or corrupted artifact. Revision recording uses a
compare-and-swap: a render of revision N cannot mark revision N+1 applied. A
cross-process per-environment advisory lock in the publication directory spans
the desired-state load, file replacement, Traefik acknowledgement, probes, and
final CAS; a stale reconciler therefore cannot rewrite the artifact inside a
newer revision's verification window.

Worker startup also reconciles route state derived from authoritative
`service_domain_bindings` after runtime-instance recovery and before route or
queue work. It fills missing exact intents for open deployments and rebuilds
backend sets for the canonical active deployment from ready, running durable
instances. Each environment is repaired in one transaction: the route lock is
acquired first, followed by sorted service/environment deployment locks, then
the group, bindings, active pointers, routes, and ready instances are locked
and revalidated before replacement. Repeated startup is idempotent and never
adopts a loose custom row. A
custom binding is eligible only after its exact DNS ownership challenge is
durably verified. This lets an installation create a supported generated
domain for a deployment that predates the domain lifecycle and converge after
worker restart without hand-written SQL.

Private routes are excluded. A public route that requires TLS without a durable
ready certificate, or has no ready backend, is also excluded from the new
artifact so stale/insecure traffic fails closed.

An unchanged private route still gets its artifact observed on every pass, but
does not rewrite the controller row, refresh timestamps, or increment apply
attempts. A missing or corrupted artifact is a real write and records exactly
one repair attempt; publication errors, desired-revision changes, and public
state transitions also continue through the ordinary durable CAS path.

The HumanSession product path exposes nested list/create/get/delete only for
the server-derived generated HTTP singleton. It requires current
`domains.read`/`domains.write`, exact canonical lineage, the
restricted-environment overlay, positive-version delete CAS, singleton
idempotency, and atomic response/audit persistence. Custom host input, editable
path/port, and TLS intent are not admitted there. The flat domain routes remain
recovery-operator compatibility.

The human deploy path accepts only `route: {public:true}` and uses the atomic
server-managed generated-domain lifecycle. On the broader operator/automation
path, route fields are selectors, never hostname authority: a hostless public
selector must resolve to exactly one eligible generated service binding, while
`route.host` must resolve to the exact already-owned custom binding. Optional
name, group, path, and TLS fields further narrow that selection. Missing,
ambiguous, ownership-unverified, TLS-pending, or otherwise ineligible selectors
are rejected before accepted work. The worker revalidates and copies the exact
binding to the canonical deployment; it never creates a domain from payload.

## Namespace changes and custom-domain cutovers

Changing the installation apps domain changes the configured authority for
future generated hostnames. Existing generated domains retain their durable
hostname and apps-domain lineage; the settings change does not rewrite them.
Treat a move to a new generated namespace or a custom hostname as a cutover.

A custom hostname is eligible only after its exact ownership challenge is
verified and its service/environment binding exists. Domain, route, challenge,
binding, backend, and controller state are separate durable records, so a
failed compatibility operation can leave an unbound domain or route that is
safe to inspect and resume. Binding a verified hostname does not invent a
backend. Reconcile or redeploy the active deployment when the route has no
ready backend, then wait for current controller and probe evidence before
removing the previous public address.

Some service/environment scopes permit only one service-managed public binding
through the product path. If an existing generated binding blocks the verified
custom binding, record its identity, remove it through the supported domain
lifecycle, and retry the custom binding. Preserve the prior DNS record until
the replacement route is ready when the external topology permits that overlap.
The full operator sequence is in
[Custom-domain cutover](../runbooks/custom-domain-cutover.md).

Public TLS may terminate outside Midnight. With an operator-managed edge,
Midnight publishes and verifies an HTTP route with TLS intent disabled; the
edge owns certificate issuance, renewal, port 443, and external HTTPS evidence.
Forward the original Host and standard forwarding headers. Keep direct edits
out of generated edge configuration: update the controller or portal that owns
the persistent source instead.

Promotion and withdrawal reconcile every eligible exact route in a controller
group. Stage/commit compare-and-swap is strict; compensation may restore only
the ownership token captured by the failed attempt. A blocked or unverified
sibling fails the routing gate. Canonical deployment promotion independently
requires the candidate's exact route intent, ready backend, current group
revisions and publication observation, Traefik acknowledgement, and trusted
ingress probe before moving the active pointer.

## Evidence boundaries

`file_applied` proves only that Midnight atomically published a validated
Traefik dynamic configuration file. It does not prove that Traefik loaded the
revision, that DNS resolves, that an HTTP probe succeeded, or that HTTPS is
reachable.

The tenant-safe generated-domain slice intentionally admits HTTP with TLS
disabled. TLS-required routes remain dependent on durable certificate state
and cannot borrow this capability claim. The repository contains certificate
lifecycle and DNS-01 integration engines, but production construction,
live-zone DNS apply, public HTTPS, renewal, restore, firewall/NAT, and off-host
reachability still require their separate gates. Durable publications give
each router and service a revision-scoped identity. The verifier recomputes
the exact artifact hash, compares the active Traefik rule/service/TLS/
middleware/backend set and backend health, rejects an overlapping older
router with the same rule, and only then performs the trusted Host-header
probe. `file_applied`, Traefik
acknowledgement, and trusted Host-header probes are distinct observations; none
alone proves Internet reachability.
