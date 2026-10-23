# Ingress Contract

The ingress contract now has two layers: deterministic control-plane rendering and the deploy-worker promotion path that publishes Traefik file-provider artifacts. DNS mutation and certificate issuance are still separately gated, but route promotion is no longer only a future contract.

- Tenant routes render from `internal/router.RouteInput` into `TraefikFile` and deterministic Traefik file-provider YAML text.
- Route groups can still be rendered as deterministic `.json` artifacts for legacy tests, but live publication writes flat `<route-group>[.<route>].traefik.dynamic.yaml` files directly in the Traefik dynamic file-provider directory.
- Route publication is split into a pure prewrite plan and an injected writer. `internal/router.LocalRouteArtifactPublicationWriter` is the local filesystem writer: callers must provide an absolute publication root, route group and route names are sanitized into a flat filename prefix, artifact names must be relative, and writes are atomic under that root. A pre-flat `<group>/<file>` artifact for the same route is removed on the next successful write.
- `internal/router.ApplyRouteArtifactPublication` refuses any prewrite plan with validation errors before calling the writer. Its apply evidence records only support-safe status, counts, bytes written, and diagnostic codes; it intentionally excludes route names, hostnames, service URLs, rendered config, and filesystem paths.
- `/.well-known/acme-challenge` remains reserved for ACME challenge handling and is pinned to the `acme-http@internal` service on the `web` entry point.
- Dynamic config does not describe or enable Traefik's Docker provider. Midnight publishes explicit file-provider services that point at runtime-resolved targets.
- Ingress defaults are owned by `internal/router`: read/write/idle timeouts and maximum request body bytes.
- Route target freshness is classified as `fresh`, `stale`, or `unknown` from the last target observation time.
- Route reconciliation returns a compact status summary and stable summary string with desired/rendered/active/pending route counts, stale or unknown observations, target mismatches, and the rendered artifact hash for UI/CLI display.
- ACME DNS integration is represented by `internal/acme.DNSProvider`, the manual provider, and a manual TXT record instruction generator. The worker can now opt into production Lego account/order execution with explicit provider, email, state-directory, and staging/production acknowledgement settings; the default remains disabled and network-free.
- Domain diagnostics are modeled as supplied observations for ownership, routing, challenge, CAA, last error, and certificate expiry. The in-memory store supports upsert/get/list support views; support views hash the domain and omit error text.
- CAA preflight results are caller-supplied models only and do not perform DNS lookups.
- Log query results can be encoded as redacted NDJSON records.
- `internal/acme.RoutePublicationEvidencePacket` combines the prewrite plan, support-safe publication summary, ingress diagnostics review, and deployment heartbeat review into deterministic JSON-friendly route publication evidence.
- `internal/acme.IngressRouteReleaseEvidenceIndex` is the support-safe manifest for referenced release evidence: route publication evidence, ACME diagnostics, ingress diagnostics review, deployment heartbeat review, logs, and timeline files. Refs must be normalized relative slash paths and may include optional lowercase SHA256 values.
- Evidence index validation is metadata-only and does not touch the filesystem. `internal/acme.VerifyIngressRouteReleaseEvidenceIndexFiles` is the explicit local verification step that checks each referenced file exists under the supplied root and validates hashes only for refs that provide SHA256 metadata.
- `internal/acme.IngressEvidenceBundle` keeps the route publication evidence packet, release evidence index, and route publication apply evidence together and rolls their statuses into one ingress decision record.
- Deploy step telemetry and operation events are part of the ingress evidence story: the dashboard timeline should show `readiness-routing-check` and `promote` succeeding before `ingress.route.active`, while route publication evidence records counts and diagnostic codes rather than rendered route content.
- No routine unit-test path performs certificate issuance, ACME network calls, DNS lookups, or DNS record writes. Enabling the worker lifecycle is an operator-owned external side-effect boundary; it does not by itself prove public DNS, Traefik certificate loading, or Internet HTTPS.

## Deploy Worker Promotion Contract

The deploy worker promotes a candidate only after the runtime and routing gate
has passed:

1. The deploy payload becomes a runtime `ScheduleRequest`.
2. The Docker/runtime adapter returns a `RoutingTarget` for the candidate instance.
3. The readiness/routing gate verifies candidate identity, host/target identity when known, target readiness, and route reconciliation status when supplied.
4. In PostgreSQL mode, exact deployment-route bindings are refreshed after
   scheduling and again immediately before promotion so a generated domain
   created during the build cannot be missed.
5. The durable promoter stages every eligible route in the group with the
   prior active backends plus the ready candidate, publishes one group
   artifact, and commits only through strict revision/ownership CAS. A blocked
   or unverified sibling fails the group.
6. A newly introduced candidate backend must match the current route target
   port. Only a prior backend captured from durable group state may retain its
   historical port during zero-downtime stage or compensation.
7. The publication writer writes
   `<publication-root>/<sanitized-route-group>[.<sanitized-route>].traefik.dynamic.yaml`
   by temp-file plus atomic rename. The artifact is flat rather than nested
   because Traefik's file provider loads subdirectory files only at startup
   while watching just the configured directory, so a nested artifact would be
   written and hash-verified but never observed.
8. Before the active pointer moves, the canonical PostgreSQL store rechecks
   exact route intent, ready candidate backend, matching desired/observed/
   applied revision, publication observation, Traefik acknowledgement, and
   trusted Host-header ingress probe.
9. Operation events record running, failure, or success without exposing
   rendered YAML or tenant secrets.

The service URL in the rendered route is derived from the runtime target
selected by the gate, normally
`http://<container-ip>:<internal-port>` for the single-VM Docker profile. The
canonical scheduled target port wins. When it is absent, the Docker adapter
checks `midnight.internal_port`, `midnight.target_port`, or `midnight.port`
labels, exposed ports, and `PORT`, then defaults to 8080. The worker promoter
must not publish if the target is missing, belongs to another
deployment/instance, is not ready, lacks a container IP, or lacks an internal
port.

Docker health readiness feeds the target readiness decision. The Docker adapter maps inspect status into runtime states: no healthcheck on a running container is currently ready, `healthy` is ready, `starting` is not ready yet, and `unhealthy` blocks promotion with a support-safe reason. Operators should treat that as the single-VM Docker readiness contract; it does not prove public ingress until the route artifact is published and Traefik observes it.

Local/noop fallback is explicit: `deploy.NoopRoutePromoter` treats promotion as successful without writing to the file provider, and `internal/runtime/local.Adapter` resolves synthetic loopback targets for deterministic tests. This fallback is useful for planner and queue tests only; it does not validate Docker labels, container networking, Traefik reloads, public DNS, ACME, or HTTPS.

The local gate has Docker-free route promotion drills for this contract. They
exercise late binding, multi-route group stage/commit/withdrawal, blocked
siblings, historical-port handoff, bounded compensation, and support-safe
telemetry with fake runtime targets and in-memory publication. Passing them
does not mean `/etc/midnight/traefik/dynamic` is writable, Traefik reloaded,
DNS resolves, or an Internet client reached the service.

Real durable PostgreSQL routing prerequisites:

- The worker is started with `--runtime-backend=docker` or `MIDNIGHT_WORKER_RUNTIME_BACKEND=docker`, not the default local adapter.
- The worker has route publication enabled with `--route-config-dir` / `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR`; an empty value wires `deploy.NoopRoutePromoter`.
- Postgres is configured with `MIDNIGHT_DB_DSN`, `MIDNIGHT_DATABASE_URL`, `MIDNIGHT_DB_DSN_FILE`, or the matching worker flags.
- The Traefik publication root is absolute, exists before startup, and is writable by the `midnight` user. The default is `/etc/midnight/traefik/dynamic`.
- The API server and worker use the same valid `MIDNIGHT_APPS_DOMAIN` so the server can derive a generated hostname and the worker can converge that durable intent.
- The worker has `MIDNIGHT_WORKER_TRAEFIK_API_URL`, `MIDNIGHT_WORKER_INGRESS_PROBE_URL`, and, for Docker, `MIDNIGHT_WORKER_INGRESS_CONTAINER` configured so strict acknowledgement, Host-header probe, and trusted-ingress enrollment can complete.
- Traefik static config has `providers.file.directory` pointed at the same root and the `web` / `websecure` entry points enabled.
- For opt-in worker-owned ACME, set `MIDNIGHT_WORKER_ACME_ENABLED=true`,
  `MIDNIGHT_WORKER_ACME_EMAIL`, `MIDNIGHT_WORKER_ACME_STATE_DIR`,
  `MIDNIGHT_WORKER_ACME_DNS_PROVIDER`, and provider-specific credentials.
  `MIDNIGHT_WORKER_ACME_ENVIRONMENT` defaults to `staging`; production also
  requires `MIDNIGHT_WORKER_ACME_PRODUCTION_ACK=true`. The worker uses the
  durable domain hostname as the initial certificate name and leaves route
  publication blocked until the certificate order is ready. When durable route
  publication is enabled, the cert resolver defaults to
  `letsencrypt-staging`/`letsencrypt-prod`; it must match the operator-owned
  Traefik static resolver name.

`--route-public-host`, `--route-public-path`, `--route-public-tls`,
`--route-cert-resolver`, `--route-group`, and `--route-target-port` (and their
`MIDNIGHT_WORKER_ROUTE_*` equivalents) configure the legacy worker-wide
compatibility promoter. They are not the source of truth and are not required
by the durable PostgreSQL path, which loads exact route/domain state from the
database and replaces that compatibility promoter after PostgreSQL startup.

## Single-VM MVP HTTPS Status

The current single-VM path is deliberately split between offline contracts and
live provider proof:

- `deploy/traefik/traefik.yaml` defines the operator-owned static Traefik
  config: `web`, `websecure`, the file-provider directory, and DNS-01 ACME
  resolvers for Let's Encrypt staging and production.
- `internal/router` renders support-safe, deterministic dynamic route YAML and
  writes it atomically under `/etc/midnight/traefik/dynamic/<group>/`.
- Generated service hostnames are expected to live under the platform
  apps-domain, for example
  `<service-token>-<scope-hash>.<apps-domain>`. The generated prefix is exactly
  one DNS label so `*.<apps-domain>` can cover it.
- Generated-domain HTTPS is positioned as DNS-01 wildcard reuse:
  `*.<apps-domain>` is issued once for the apps-domain and generated service
  hosts reuse it, avoiding per-service Let's Encrypt registered-domain budget
  pressure.
- `internal/acme` contains the live Lego issuer and built-in provider lookup
  for Cloudflare, Route53, and manual DNS. Its routine unit tests stay
  network-free; `internal/acme/lego_integration_test.go` is the gated live
  verification hook for Let's Encrypt staging.

Remaining live verification before this can be called proven on a real VM:

- Cloudflare and Route53 credentials can publish and clean up
  `_acme-challenge.<apps-domain>` records from the systemd environment used by
  Traefik or the issuer.
- Public recursive resolvers observe DNS-01 propagation before ACME
  validation.
- CAA permits `letsencrypt.org` for the apps-domain.
- The generated wildcard certificate is present in Traefik's ACME state with
  mode `0600` and survives byte-exact restore before Traefik startup.
- A generated hostname resolves to the VM, reaches `:443`, presents the
  wildcard certificate, and returns a routed application response.
