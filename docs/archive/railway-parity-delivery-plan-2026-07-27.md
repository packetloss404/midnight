# Midnight Railway-Parity Delivery Plan

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

> Historical design record. The canonical current queue is
> [`BACKLOG.md`](../../BACKLOG.md); this dated plan must not be used to infer that
> an item remains open or that a completed item has production evidence.

- Date: 2026-07-27
- Status: archived design history; current state lives in `BACKLOG.md`
- Product target: a credible, self-hosted, single-VM Railway-like PaaS
- Inputs: six specialist review lanes, direct code inspection, local desktop
  and mobile GUI testing, and current Railway and Railpack documentation
- Companion audit:
  [railway-parity-review-2026-07-27.md](railway-parity-review-2026-07-27.md)
- GUI comparison and product direction:
  [midnight-railway-ui-comparison-2026-07-27.md](midnight-railway-ui-comparison-2026-07-27.md)
- Current reboot-safe implementation handoff:
  [railway-parity-reboot-handoff-2026-07-28.md](railway-parity-reboot-handoff-2026-07-28.md)

## Executive decision

Midnight should stop expanding breadth until one complete product loop is real:

```text
clean install
  -> authenticate
  -> create or link project/service/environment
  -> build source or accept a digest-pinned image
  -> start the workload with durable config, secrets, and storage
  -> pass readiness
  -> activate traffic safely
  -> inspect logs and metrics
  -> restart, redeploy, or roll back
  -> survive process and host restarts
```

The repository already contains useful contracts for nearly every box in that
flow. The main problem is that several product paths end in planners, fakes,
memory stores, UI placeholders, or worker-local files. The next release should
turn those contracts into one durable execution path before adding hosted-edge
claims, Kubernetes, billing, marketplace breadth, or enterprise controls.

The Railway parity target is behavioral, not visual or implementation cloning.
Midnight should preserve its own product identity and use open components and
public contracts.

## What “Railway-like” means for the first credible release

The P0 release is complete only when all of the following are true on a fresh
supported VM:

1. The packaged installation starts PostgreSQL, the API, build and deploy
   workers, BuildKit, a registry, Docker integration, Traefik, and the required
   controllers without undocumented manual wiring.
2. `midnight login`, `midnight up`, and the equivalent GUI flow use the same
   API, authentication, resource model, and operation state.
3. Dockerfile and current Railpack source deployments produce immutable images
   and one canonical deployment record.
4. Variables, service settings, and volume mounts are durable and reach the
   workload.
5. A service is private by default. An explicit domain action produces a
   working HTTPS URL and survives control-plane restarts.
6. A failed candidate never replaces healthy traffic.
7. Build, deploy, runtime, and HTTP logs are real, scoped, searchable, and
   streamable. CPU, memory, network, and disk graphs use real samples.
8. Restart, redeploy, rollback, cancel, stop, and remove have distinct,
   correctly tested semantics.
9. Private GitHub repositories and push autodeploy work without persisting
   short-lived clone credentials.
10. A real backup can destroy and restore platform data and a volume-backed
    application.

Railway currently keeps public exposure explicit: `railway up` deploys, and a
domain is generated separately unless one already exists. Midnight should
match that safe default. `midnight up --public` can be offered as an explicit
convenience, but a plain deploy must not silently expose a service.

## Scope boundaries

### P0: single-VM core parity

- Source and image deployments
- Persistent services and background workers
- Project, service, and isolated environment configuration
- Variables, references, sealed values, and deployment snapshots
- Single-writer volumes and real backup/restore
- Generated and custom HTTPS domains
- Single-host private networking and DNS aliases
- Health checks and safe traffic cutover
- Real logs and resource metrics
- CLI, REST API, and production GUI
- GitHub App source connections and autodeploy
- Restart, redeploy, rollback, cancel, stop, and remove

### P1: workflow parity

- Environment duplication and synchronization
- PR environments and focused monorepo deploys
- Staged changes and config-as-code
- Templates with real topology, volumes, and health aggregation
- Replicas on one host, background workers, and cron jobs
- TCP proxying
- Tenant-scoped product sessions, invitations, project tokens, and
  project/environment RBAC across REST, CLI, GUI, GraphQL, and MCP

### Later profiles

- Multi-host scheduling and WireGuard execution
- Multi-region traffic steering or anycast edge
- Kubernetes as a runtime adapter
- Billing and public multi-tenant hosting
- Advanced marketplace and enterprise breadth

## Evidence grading

Every capability must be labeled consistently:

| Grade | Meaning |
|---|---|
| Executed | A packaged or disposable live test exercises the real dependency and stores evidence. |
| Integrated | Production code is wired, but the full external dependency path is not yet in the loop. |
| Contract | Interfaces, schemas, renderers, or planners exist and have unit tests. |
| Missing | No usable product path exists. |

Unit tests over a fake command runner do not make a BuildKit or Railpack path
“executed.” A rendered Traefik file does not make a route active. A backup
manifest does not mean data was backed up.

## Critical findings (initial review snapshot)

This table preserves the evidence that motivated the plan. It is historical,
not the current capability matrix; current disposition is recorded immediately
after it and in the gate checkpoints below.

| Priority | Finding | Evidence at review start | Required outcome |
|---|---|---|---|
| P0 | Railpack integration targets obsolete commands | `internal/build/railpack.go` calls `plan --format json` and Dockerfile-emission flags; current Railpack uses `prepare` and a BuildKit gateway frontend | Pin and execute a supported Railpack CLI and frontend image using `prepare` plus `gateway.v0` |
| P0 | Default Compose cannot perform a source build | The build worker image lacks `railpack` and `buildctl`; Compose and the compatibility contract also disagree on the registry version | Ship one tested build-worker image and align all component pins |
| P0 | `midnight up` is absent from canonical deployment history | Upload intake creates an operation and build job without creating a canonical deployment row | Every trigger creates one deployment ID before work is queued |
| P0 | Source builds do not update canonical image/artifact truth | The built digest is only placed in the next queue payload | Persist source SHA/upload digest, builder, image digest, plan, SBOM, provenance, and logs before deploy |
| P0 | Traffic and database truth can diverge | Route publication occurs before canonical active state; previous-active identity is client/job supplied | Server-resolved active pointer, acknowledged route generation, transactional CAS, then drain |
| P0 | Public routing is not actually opt-in | The deploy executor promotes unconditionally when a worker has global route defaults | No stored public intent means no public route |
| P0 | Private-network creation fails open | Docker scheduling can fall back to the default bridge | Network convergence must fail closed with a typed retryable error |
| P0 | Host/path inputs cross an unsafe renderer boundary | Hostnames are not fully normalized or protected from Traefik rule metacharacters | IDNA/DNS/path validation before persistence and rendering |
| P0 | CLI authentication is split | Login uses `MIDNIGHT_TOKEN`/global config while many product commands only read `MIDNIGHT_API_TOKEN` or `--token` | One endpoint/token resolver for every command |
| P0 | CLI can report false success | SSE has a subscribe race and no replay IDs; premature EOF returns success | Durable sequenced events, resume support, terminal polling, failure-correct exit codes |
| P0 | Upload ignore handling is unsafe | The custom matcher knowingly accepts false negatives and lacks negation semantics | Use correct gitignore semantics and never upload ignored secrets |
| P0 | Private GitHub deploy is not production-wired | The server does not install the App client; installation identity is lost before checkout; no token reaches the worker | Durable non-secret auth descriptor and worker-minted, memory-only installation token |
| P0 | Duplicate pushes can duplicate builds | Queue insertion has no trigger uniqueness guarantee | Canonical trigger key and database uniqueness/outbox transaction |
| P0 | Monorepo root and watch paths are conflated | A matched watch prefix becomes the build root | Persist root directory, watch paths, and config path independently |
| P0 | Variable lifecycle is incomplete | Encrypted PostgreSQL persistence, optimistic update/delete, metadata tombstones, exact-version runtime deployment retries, and atomic config-bearing environment duplication with variable re-encryption are wired. Exposure selection, build-snapshot pinning, history-aware KEK retirement, and general multi-edit environment change sets remain | Complete the lifecycle without weakening the fail-closed KEK or metadata-only read boundaries |
| P0 | Volumes stop at control-plane records | Mounts are absent from the deploy job and live `ScheduleRequest` path | Provision, lock, attach, schedule, detach, recover, and back up a real Docker volume |
| P0 | Cross-project scope integrity is inconsistent | Service, project, environment, variable, deployment, and volume IDs are often checked independently; one global bearer grants broad access | Central scope resolver plus composite tenant constraints before any side effect |
| P0 | Domain/TLS state is not durable | Renderer and ACME libraries exist; domains, certs, and observed route revisions do not | Domain, certificate, route, backend, and controller state in PostgreSQL |
| P0 | Backup success does not mean data was copied | The endpoint uploads a JSON manifest from caller-supplied byte counts and records pending checksums; restore is an offline plan | Disable production success claims, then stream, hash, encrypt, destroy, and restore actual bytes |
| P0 | Official database templates can succeed without a database deployment | Closed for PostgreSQL by migration 0036 and broadened by migration 0037 + the canonical REST/worker saga | PostgreSQL, Redis durable, MySQL, and MongoDB single-node now require durable volume/credentials/private-network intent, engine-native authenticated write, real replacement, marker read, and probe cleanup. Managed HA and engine backup/restore remain separate. |
| P1 | Health configuration is not applied | Docker `HEALTHCHECK` is the only real signal; a container without one is immediately ready | Configurable HTTP 200 probe or TCP listener probe with a 300-second default timeout |
| P1 | Runtime configuration is mostly an echo surface | Start/build/root/replica GraphQL mutations do not persist or reach the runtime request | Durable per-service/environment config and immutable resolved snapshots |
| P1 | Actions have collapsed semantics | Restart, redeploy, and rollback converge on generic image deploy behavior | Separate commands, state transitions, and snapshot rules |
| P1 | Logs and metrics are mostly surfaces without collectors | Operation events and file-query contracts exist; Docker, build, and Traefik collection is absent | Real collection, retention, query, and live-tail paths |
| P0 | Current log filtering can act as a secret-presence oracle | Fixture `FileStore` filters unredacted content and redacts only returned rows; cursors can expose host filenames | Redact before persistence/indexing and use signed opaque cursors |
| P1 | Alerts and notifications are presentation contracts | Prometheus rules reference mostly un-emitted metrics; no Prometheus/Alertmanager ships; notification preview is hard-coded demo data | Built-in monitor evaluator and durable delivery outbox; Prometheus remains optional |
| P1 | Environments, previews, and templates are incomplete | Environments are effectively read-only; previews are planners; template volumes are synthetic | Transactional lifecycle with compensation and health aggregation |
| P1 | API contract drifts from the router | OpenAPI omits implemented routes and the live SSE contract | OpenAPI is authoritative and generates GUI/CLI clients |

### Current disposition

The implementation checkpoint uses only the four evidence grades above. A
closed initial finding can still be `Integrated` rather than `Executed` when
the remaining work is an external or installed-system proof.

| Initial finding group | Grade | Current disposition |
|---|---|---|
| Railpack, BuildKit, registry, and emitted applications | Executed | The production build-worker image and toolchain loop run current Railpack `prepare`, the pinned BuildKit gateway, Node/Python/Go/Dockerfile fixtures, registry digest comparison, application probes, and retained-evidence secret scans. Stock Compose trusts local-registry digests plus the exact digest-pinned official database image set; tag-only and other external images fail closed. |
| Canonical intake, artifact truth, outbox, and duplicate triggers | Integrated | REST, local upload, GitHub, image, and PostgreSQL-template intake use canonical deployments and durable staged-work/outbox paths. Migration 0045 and ADR 0035 give human local uploads immutable content-addressed blobs, DB-enforced exact operation/deployment tenant ownership, full-body replay fingerprints, isolated worker materialization, terminal-reference reconciliation, and automatic bounded build-worker cleanup for content-addressed plus retained legacy work. Migration 0046 adds replica-safe pre-body reservations, default workspace retained/inflight and installation inflight budgets, a workspace/digest charge ledger independent of product-row cascades, publication under quota→workspace→lifecycle locks, and proof-based charge release after successful CAS collection. Binding, release, reconciliation, and two-pass quarantine share the lifecycle lock. Live PostgreSQL/real-filesystem tests now execute accepted charge transfer, operation cascade survival, quarantine-before-proof, final charge and metadata convergence, same/cross-workspace digest charging, concurrent workspace/global count and byte boundaries, bounded reconciliation fairness, renewal/crash markers, rollback preservation, and both publication lock orderings. The policy is a human-path logical quota, not a random-staging, legacy-path, or kernel host-filesystem quota. Project-token and recovery-operator uploads retain the documented legacy extraction path pending a separate migration, and installed process-death evidence remains external. Activation uses a server-owned active-pointer CAS. Migration 0050 and ADR 0039 now commit verified webhook payloads before acknowledgement, plan immutable temporal-authority targets, retry with fenced leases, and provide metadata-only dead-letter recovery. The contract is at least once; installed process-death evidence remains external. |
| Private GitHub and monorepos | Integrated | Migration 0049 and ADR 0038 replace mutable service JSON with workspace-exclusive installation grants, exact repository-ID grants, and canonical service links. Recovery assignment observes metadata from GitHub; human discovery is project-scoped; linking live-verifies the exact branch and atomically reauthorizes, persists idempotency, and audits. Revocation and staged webhook intake share an installation lock and intake revalidates the link version. Migration 0050 and ADR 0039 add persist-before-ack bodies, receipt-time authority cutoffs, immutable per-service targets, bounded leased retries, dead-letter state, and atomic operator redrive. The worker mints repository-scoped installation tokens in memory from durable non-secret coordinates; clone targets and errors are constrained/redacted. Root directory, watch paths, and config path remain independent canonical fields but are not yet exposed by the strict human link mutation. A live private-repository delivery, installed process-fault run, and commit-status round trip remain outstanding. |
| Scope, idempotency, private-by-default networking, and renderer safety | Integrated | Scope constraints/resolvers, fail-closed environment networks, explicit public intent, and strict route input validation are production-wired. Migration 0039 and the shared stores fence immutable attempts by actor, tenant, method, route, client key, and generation. The PostgreSQL tenant coordinators admit REST project and service creation, a narrow digest-pinned image deployment, the exact human `POST /services/{service_id}/up` path, the five exact deployment-action routes, canonical deployment-operation cancellation, exact GitHub source-link reads/mutations, and the generated-domain nested lifecycle through serializable authorization/product/idempotency/audit/work transactions. Migration 0047 lets cancellation terminalize deploy and staged-work outboxes before dispatch; pending jobs are canceled and leased workers converge operation/deployment truth at safe points before traffic promotion. Source uploads authorize before body read and reauthorize after complete archive validation; replay never bypasses hashing the supplied body. Human canonical deployment reads plus exact operation status/event/log/domain reads are route-scoped and revalidate current tenant authority. REST, CLI, and GUI use tenant deployment/action/cancel/source-link/generated-domain routes without GraphQL, operator, App-global discovery, or manual-ID fallback. Live PostgreSQL coverage proves concurrent same-key replay, runtime/image lineage, current-role denial, cross-service concealment, runtime conflict fencing, cancellation/dispatcher races, source grant/link/revocation races, route/domain mutation ordering, leased-worker fencing, and audit rollback. Custom domains and TLS remain gated. Operation failure marking is recovery-only rather than a Railway user action. RFC 8785 request canonicalization is still missing. The security loop supplies the local Docker isolation proof. |
| Variables, service config, pre-deploy, and volumes | Executed | PostgreSQL variables/config snapshots and one-off pre-deploy are integrated; real Docker tests execute durable mounts, single-writer enforcement, replacement persistence, archive, and recovery. Installed VM-restart evidence remains external. |
| Routes, domains, certificates, and activation | Partial | Durable human sessions can manage one server-derived generated HTTP domain per service/environment through nested API/CLI/GUI. Migration 0052 and ADR 0040 add exact service-managed/current-and-future deployment bindings, multi-route group CAS, upgrade backfill, and a canonical promotion fence that requires candidate backend plus current controller/Traefik/trusted-probe evidence. The flat custom-domain surface remains recovery-only. Loop 3.6 now constructs the Lego DNS-01 issuer/controller only through explicit worker opt-in, defaults to staging, persists order/certificate references, and keeps TLS routes blocked until durable ready evidence. Traefik certificate installation, custom-host ownership, delegated DNS, public HTTPS, renewal, and off-host reachability on the clean VM remain separate proof. |
| Logs, metrics, runtime events, monitors, and delivery | Executed | Real PostgreSQL, Docker logs/stats, and Traefik access-log integrations exercise collection and rollups. Monitor incidents and durable notification delivery are integrated; installed real-origin and SMTP/webhook proof remains external. |
| Runtime actions and recovery | Executed | Distinct durable actions and reconciliation are integrated. Real Docker volume recovery and destructive PostgreSQL/platform-filesystem restore are executed. The platform index uses salted slow-KDF authentication and restore apply uses an fsync'd crash-resumable phase journal; the complete process-fault matrix and installed off-host restore remain external. |
| Environments and official database templates | Integrated | Environment lifecycle/transactional config-variable duplication is integrated. Real Docker execution proves official engine command syntax and same-volume replacement persistence; verifier contracts bind the exact official image and replacement identity, and require cleanup before success. PostgreSQL has the stronger end-to-end execution grade; the additional engines remain integrated until the installed data-plane runner passes. PR environments are not implemented. |
| Replicas and cron | Integrated | Durable single-host replicas, multi-backend routes, and five-field UTC cron with five-minute minimum/overlap skipping are wired. Multi-region placement remains missing, and packaged long-running cron/replica drills remain external evidence. |
| Single-host TCP proxy | Executed | Real Docker/PostgreSQL evidence proves published traffic, scoped network enrollment, backend replacement on redeploy, withdrawal, and detachment. Authoritative DNS/firewall, original-client IP preservation, and internet reachability are not claimed. |
| Multi-host/multi-region runtime | Missing | Planning/scaffolding is not a usable scheduler, distributed network, or global edge. Tenant identity/RBAC is tracked separately: the CLI and GUI use durable human sessions for scoped metadata, project/service creation, immutable-image/source deployment, deployment actions, and canonical GitHub source linking. GraphQL and MCP propagation remain gated. |

Generated-domain checkpoint (2026-07-29): migration 0052 and ADR 0040
supersede the one-deployment limitation of migration 0048/ADR 0037. Durable
human sessions can list/create/get/delete the one server-derived generated
HTTP domain for an exact service/environment through REST, CLI, and the
Networking panel. Current open deployments are bound atomically; future
deploy/redeploy/rollback intake inherits the exact service-managed route.
Multi-route group promotion fails on blocked or unverified siblings, and the
canonical active pointer requires candidate backend plus current controller,
Traefik, and trusted-probe evidence. Private-by-default omission remains.
Custom domains, TLS/certificates, editable route properties, and public
DNS/Internet evidence remain gated.

GitHub source-link checkpoint (2026-07-29): migration 0049 and ADR 0038 admit
workspace-exclusive installation grants, exact repository grants, canonical
service links, recovery assignment/revocation, and strict human API/CLI/GUI
flows. Signed push intake revalidates grant and link authority under the
revocation lock, and checkout tokens are repository-scoped.

Durable-webhook checkpoint (2026-07-29): migration 0050 and ADR 0039 supersede
reservation-before-synchronous-dispatch in PostgreSQL mode. Verified bodies
and acceptance audit evidence commit before `202`; exact immutable-envelope
replays return `200`, conflicts return `409`, and workers freeze target
discovery at database receipt before current-version intake revalidation.
GitHub acceptance and source mutation share the installation lock so that
receipt cutoffs follow commit order. Delivery and target leases are fenced and
renewed, retry budgets converge to dead letter, retained hashes are checked
before use, and recovery redrive commits inbox/idempotency/audit state
atomically. Terminal payload bytes purge after 30 days while hashes and
metadata remain. This is an at-least-once claim, not exactly once, and it does
not replace the installed private-repository/process-fault gates.

## Target architecture

```text
CLI / GUI / GitHub
        |
        v
API intake transaction
  operation + deployment + snapshot draft + trigger key + outbox
        |
        +--------------------------+
        |                          |
        v                          v
Build worker                  Deploy/controller workers
  Git checkout/upload           config + secret resolver
  Dockerfile or Railpack        volume manager
  BuildKit                      Docker scheduler
  registry push                 readiness controller
  artifact persistence          route/domain/cert controller
        |                          |
        +------------+-------------+
                     v
          PostgreSQL product truth
          OCI registry + artifact store
                     |
                     v
             Observer/reconciler
       Docker events/stats + logs + Traefik access
```

### Architectural rules

1. `deployment_id` is the authoritative work identity. Queue messages carry
   IDs and expected resource versions; workers load durable intent instead of
   trusting duplicated client-authored runtime state.
2. PostgreSQL owns desired and product state. Docker, BuildKit, the registry,
   Traefik, DNS, and ACME are observed systems reconciled toward that state.
3. Every asynchronous mutation commits its idempotency reservation, operation,
   deployment, initial event, and outbox row in one transaction.
4. Workers are idempotent and lease-backed. A crash after any external side
   effect must be recoverable by observing the side effect and continuing.
5. Secrets never enter durable queue payloads, operation messages, build
   arguments, image history, or general audit JSON.
6. Public exposure, TCP exposure, and cross-host networking are explicit
   capabilities. A failed security boundary never degrades silently.
7. One component owns certificate issuance. The recommended P0 choice is
   Midnight’s existing Lego-based controller, with Traefik consuming published
   certificate references; do not split issuance state between Lego and
   Traefik’s internal ACME store.
8. A central `ScopeResolver` validates that project, environment, service,
   deployment, variable, volume, domain, and backup resources belong to the
   same authorized scope before a transaction or external side effect begins.
   Composite foreign keys preserve that invariant in PostgreSQL.

## Durable resource model

Existing migrations for deployments, variables, volumes, projects,
environments, services, operations, and jobs should be extended rather than
replaced.

### Service and source configuration

Add `service_environment_configs`:

- service and environment IDs
- source connection and branch
- root directory, watch paths, and config path
- builder, Dockerfile path, build command, start command, pre-deploy command
- target port
- health path, port, timeout, and probe kind
- restart type and maximum retries
- desired replicas
- overlap and drain seconds
- cron schedule when applicable
- desired revision and resource version

Add `source_connections` with provider, installation ID, repository ID/name,
default branch, and non-secret policy. GitHub tokens are minted at checkout
time and never stored here.

### Deployments, builds, and instances

Extend the canonical deployment model with:

- resolved source SHA or content-addressed upload digest
- effective config revision
- previous active deployment resolved by the server
- immutable variable-version references
- image manifest digest
- build and release IDs

Add:

- `builds`: state, toolchain versions, cache key, timing, failure
- `build_artifacts`: Railpack plan/info, logs, SBOM, provenance, manifest
- `deployment_instances`: replica, host, container ID, state, readiness, exit
  code, restart count, observed timestamp
- `outbox`: typed work, aggregate ID/version, attempts, lease, delivery state

The deployment finite-state machine must include runtime terminal states needed
for persistent services and one-offs: queued, building, deploying, active,
superseded, failed, canceled, crashed, completed, removing, and removed.

### Networking

Add:

- `service_networking`
- `domains`
- `http_routes`
- `route_backends`
- `certificates`
- `tcp_endpoints`

Required constraints include unique normalized hostname ownership, unique
external TCP ports, unique hostname/path routes, and one active deployment per
service/environment.

Generated domains must use one label beneath the apps domain, for example:

```text
web--production--acme--a1b2c3.apps.example.com
```

A certificate for `*.apps.example.com` does not cover a multi-label prefix such
as `web.production.acme.apps.example.com`.

### Variables

Finish the existing encrypted `service_variables` design:

- PostgreSQL-backed envelope store
- KEK provider abstraction and rotation
- a distinct shared/project-variable store rather than ambiguous nullable
  service scope
- literal, reference, secret, and irreversibly sealed behavior
- metadata-only list APIs
- true optimistic-concurrency upsert, update/delete/rotate, and tombstones
- immutable version references in deployment snapshots
- BuildKit secret mounts for build-time values
- runtime resolution immediately before container creation
- durable environment-wide change sets so several variable/config edits deploy
  atomically

Changing variables creates staged desired state and a deployment; it must not
silently mutate an already running container.

### Volumes and backups

Finish the existing volume and attachment tables:

- persist mount path on an authoritative definition
- create the Docker volume before schedule
- enforce the single-writer lock before enqueue and again at attach
- use composite scope constraints and reject cross-project/environment
  attachment before any Docker call
- populate `ScheduleRequest.Mounts`
- record active and detached attachments
- recover attachment truth from Midnight labels after restart
- reject replicas greater than one for a volume-backed P0 service

Add real `backup_runs`, `backup_items`, and `restore_runs`. P0 can use a
filesystem artifact store with an S3-compatible adapter, but the backup must
contain actual PostgreSQL data, volume contents, registry metadata/data,
configuration, certificates, audit material, and the encrypted-secret recovery
boundary. Off-host backups must be encrypted with a backup key separate from
the live KEK.

Existing manifest-only backup rows must be migrated to an `evidence_only`
state and must never be offered as restore points. Existing synthetic
`vol_pending_*` template IDs and rows with lost mount paths require an explicit
operator repair state; Midnight must not guess where application data lives.

Platform restore must be an offline administrative workflow while the API and
workers are stopped. An online API may create or inspect a restore plan, but it
must not destructively replace the PostgreSQL database from which it is
currently serving.

### Logs and metrics

Avoid making Loki, ClickHouse, or a full observability distribution mandatory
for the single-VM profile.

Add a `midnight-observer` mode to the runtime agent or a narrow observer
process that:

- streams Docker stdout/stderr and lifecycle events through the Engine API
- ingests build logs as they are produced
- ingests structured Traefik access logs
- samples Docker CPU, memory, network, disk, and restart state
- normalizes and redacts records before persistence
- keeps an agent-side write-ahead spool and sends compressed,
  sequence-numbered batches that are acknowledged only after durable ingest

For P0, store compressed NDJSON segments in the artifact store with a
PostgreSQL segment catalog and bounded retention. Store bounded, partitioned
metric samples in PostgreSQL with downsampling and expiry. Keep log and metric
storage behind interfaces so larger installations can add Loki,
OpenTelemetry/Prometheus, or ClickHouse adapters later.

Add append-only typed `resource_events` instead of encoding deployment and
instance identity into log messages. Add `monitors`, `monitor_states`,
`notification_channels`, and `notification_deliveries`; evaluate core CPU,
memory, disk, network, deployment, collector, certificate, and backup alerts in
Midnight under a PostgreSQL lease. Prometheus exposition is an optional export,
not a mandatory P0 dependency.

Log records must be redacted before writing or indexing so searching cannot
reveal whether a guessed secret existed. Bound line and attribute sizes,
generate an explicit synthetic drop record under rate pressure, and expose
only signed opaque cursors—not local filesystem names.

## Canonical deployment lifecycle

1. **Intake**
   - Authenticate and authorize.
   - Resolve service, environment, config revision, current active deployment,
     source, and route policy server-side.
   - Compute a canonical trigger key.
   - Atomically create operation, deployment, snapshot draft, first event,
     idempotency response, and outbox work.

2. **Source resolution**
   - GitHub: mint a short-lived installation token in worker memory and resolve
     the exact commit SHA.
   - Local upload: store a content-addressed archive in the artifact store.
   - Enforce correct ignore semantics, archive size/count limits, path safety,
     and secret scanning.

3. **Build**
   - Explicit Dockerfile uses the Dockerfile frontend.
   - Otherwise run pinned `railpack prepare --plan-out --info-out`, then the
     pinned Railpack BuildKit gateway frontend.
   - Pass secrets with BuildKit secret mounts, not build args.
   - Push to the registry, verify the digest, persist artifacts, and update the
     canonical deployment before emitting deploy work.

4. **Pre-deploy**
   - Run the configured command as a one-off on the private environment
     network with resolved variables and no service volume.
   - Failure leaves current traffic unchanged.

5. **Schedule**
   - Materialize volumes and a complete runtime request: command, environment,
     resources, restart policy, mounts, labels, target port, and replica
     identity.
   - Fail closed if the environment network is unavailable.

6. **Readiness**
   - A configured HTTP health path must return exactly 200 until the timeout.
   - Without an HTTP path, require a successful TCP connection to the selected
     target port.
   - A merely running process is not ready.

7. **Activate**
   - Build a route generation from durable desired state and ready backends.
   - Publish it atomically.
   - Wait for Traefik to acknowledge the router/service generation and perform
     a synthetic request through ingress.
   - Commit the active-pointer CAS and deployment state.

8. **Overlap, drain, and remove**
   - Preserve old backends for the configured overlap.
   - Send SIGTERM, wait for the configured drain period, then force removal.
   - Record instance and deployment state at every step.

9. **Reconcile**
   - Docker events update crashes, exits, restarts, and removals.
   - Controllers repair missed outbox deliveries, leaked candidates, stale
     routes, volume attachments, and product/runtime divergence.

## Distinct deployment actions

| Action | Required behavior |
|---|---|
| Restart | Stop and start the selected active runtime process/instances; no source build and no new image |
| Redeploy | Create a new deployment from current resolved source and desired config; rebuild when source-based |
| Rollback | Create a new deployment from a prior immutable image and config/variable-version snapshot; never rebuild; active-pointer CAS required |
| Cancel | Cancel queued work immediately or honor defined worker safe points before activation |
| Stop | Remove active traffic and stop instances while retaining deployment history |
| Remove | Stop, remove runtime resources, and mark the deployment removed |

## Networking design

### Single-host private network

- One Docker bridge per project/environment
- Bare service alias plus `<service>.midnight.internal`
- Cross-project and cross-environment isolation tests
- The trusted Traefik proxy is attached only to networks for which it has
  active routes
- Failure to create or join a network fails the deployment

### Public HTTP

- Public routing is disabled unless stored networking policy enables it
- `web` redirects to `websecure`, excluding the reserved ACME challenge path
- TLS is the production default
- Generated domains use the operator’s apps domain and wildcard certificate
- Custom domains require normalized hostname ownership via TXT plus routing
  DNS before they serve
- Every domain has an explicit or safely detected target port
- Traefik rule values are rendered only after strict validation
- Forwarded headers are overwritten at the trusted edge

### Certificates

- Wildcard apps-domain certificate through DNS-01
- Custom-domain certificate only after ownership verification
- Durable order, failure, validity, and renewal state
- Key material stored outside general resource JSON with strict permissions
- Renewal and restore exercised in the loop

### TCP and hosted edge

TCP proxying needs a real L4 listener/allocator and cannot be represented as an
arbitrary dynamic Traefik HTTP route. It is P1.

The single-VM profile must not claim Railway’s global edge, anycast, or
multi-region failover behavior. Those require a separately tested deployment
profile.

Implementation checkpoint (2026-07-27): migration 0034 and
`internal/tcpproxy` now provide the single-host L4 listener/allocator,
PostgreSQL desired/observed state, ready-replica resolution, worker network
enrollment, load distribution/failover, and withdrawal. This follows
[Railway TCP proxying](https://docs.railway.com/networking/tcp-proxy) at the
product-flow level but deliberately does not claim authoritative DNS,
firewall/NAT mutation, original-client IP preservation, or internet
reachability. The integration loop now executes the complete local Docker and
PostgreSQL topology: published host port, scoped bridge enrollment, backend
traffic, redeploy target replacement, withdrawal, and bridge detachment.

## API and CLI design

### One control plane

- REST/OpenAPI is authoritative.
- Generate the TypeScript GUI client and CLI request types from it.
- Keep GraphQL read-only until it uses the same stores; remove or disable echo
  mutations.
- Move planner-only `/plan` and enterprise surfaces behind an explicit
  experimental flag so they cannot be mistaken for working product paths.
- Use a composite idempotency key: actor, canonical tenant, method, route
  template, client key, plus an immutable attempt-generation fence.
- Canonicalize JSON bodies and content-hash uploads.

### Durable operations and SSE

Operation events need a monotonic sequence, typed phase/state, timestamp,
deployment ID, and redaction-safe data. SSE must:

- replay durable events before following
- emit `id: <sequence>`
- support `Last-Event-ID`
- avoid the get-before-subscribe race
- tolerate slow consumers without silent loss
- make the CLI query terminal operation/deployment state after premature EOF

### CLI P0 surface

```text
login / logout / status
project create/list
service create/list
environment create/list
link / unlink
up / deploy image
logs
variables
domain
volume
restart / redeploy / rollback / cancel / down
doctor
```

`midnight up` should become a first-run on-ramp:

- authenticate interactively or with a device code on headless hosts
- offer create/link when no project link exists
- support `--yes`, `--new`, `--project`, `--service`, `--environment`,
  `--detach`, `--ci`, and `--json`
- return correct process exit status
- print dashboard and public URLs only when they truly exist

Use one precedence rule everywhere:

```text
explicit flag -> CI/project token env -> stored server credential -> error
```

Remove the fabricated `prod/default` environment fallback.

## GUI product design

Keep `web-next` as the production dashboard and retire the legacy dashboard
after equivalent acceptance coverage.

### Information architecture

- Global rail: projects, activity, fleet, registry, instance settings
- Project header: project selector, environment selector, environment state
- Project navigation: services, deployments, observability, shared variables,
  settings
- Service panel: overview, deployments, logs, metrics, configure
- Deployment detail: source, effective config, build, deploy, runtime,
  artifacts, logs, actions

Defer a persistent project rail until Grid and the inspector establish their
width requirements; allow it only on very wide layouts. The service inspector
should be a non-modal split pane so the resource workspace remains useful; on
mobile the same nested service route becomes a full-screen page. The inspector
and agent are mutually exclusive occupants of one right utility pane.

### Interaction rules

- Resolve omitted environment scope to the saved/default `production`
  environment.
- Make cross-environment Compare explicit and read-only. Every mutation must
  carry a concrete environment ID.
- Make `/new` the only project-creation flow and reuse one Source Picker from
  new project, add service, empty canvas, and command palette.
- Use a guided creation flow: source -> target -> detected config -> review and
  deploy.
- Keep a project-level Create action visible after the first service exists.
- Make Grid the deterministic default resource view. Keep the draggable
  topology as an optional Map mode with server-persisted environment layouts.
- Add one batched environment-resource summary read model for Grid cards;
  never issue one latest-deployment request per service or fabricate source,
  revision, replica, health, endpoint, or action state.
- Make create/link/deploy one idempotent operation or persist an explicit Draft
  service with a resumable setup step.
- Treat the current variables-only browser store as Local Draft, not a durable
  changeset. Never render plaintext secret values in diffs. Add a canonical
  cross-resource changeset API before presenting Deploy Changes.
- On narrow viewports, use a stacked service list; do not require drag gestures.
- Ensure every observability widget enforces the scope declared by its page.
- Make every capability state explicit: unavailable, needs configuration,
  queued, reconciling, ready, degraded, failed.
- Never show generated domains, saved settings, logs, metrics, backups, or
  successful deployment state unless the API reports durable evidence.
- Sanitize unexpected non-JSON API responses. Do not render an entire upstream
  HTML error body in the product surface; show status, bounded message,
  request ID, and recovery action.
- Preserve deep links for environment, service, tab, and deployment.
- Replace scattered preview/read-only prose with a server capability manifest
  and a reusable `CapabilityGate`.

### GUI release gates

- Playwright flows against a seeded real API, not only mocked component data
- Desktop and 390px visual coverage
- Keyboard-only create, deploy, inspect, and rollback
- Accessible names, focus containment/restoration, reduced motion, contrast,
  and live-region behavior
- No horizontal tab loss or obscured controls on mobile
- Empty, loading, permission, partial capability, failure, and reconnect states

## Dependency-ordered implementation plan

### Gate 0 — stop false claims and close safety holes

- Install the central scope resolver and repair cross-project rows before
  adding composite constraints.
- Replace the active environment-lock index with a partial unique constraint
  for unreleased locks.
- Make route publication explicitly opt-in.
- Strictly validate hostnames, paths, ports, wildcards, and Traefik rule input.
- Fail closed on private-network provisioning.
- Remove link’s fabricated environment fallback.
- Unify CLI credential and endpoint resolution.
- Fix `midnight up` premature-EOF success behavior.
- Replace the unsafe ignore matcher.
- Feature-gate database-template deploy and backup success until they execute
  real runtime/data work.
- Disable GraphQL echo mutations and hide planner-only product surfaces.
- Bring OpenAPI up to the actually supported core routes.

Exit: unsupported behavior fails with a typed, honest error and cannot publish
traffic, leak an upload, or report a false success.

Current checkpoint (2026-07-27) — **Integrated**: the listed scope,
idempotency, private-network, route-validation, authentication, SSE,
ignore-matcher, capability-gating, and OpenAPI safety boundaries are wired.
The clean installed-instance security proof remains part of the VM and live
gates.

### Gate 1 — canonical deployment and real toolchain

- Build a pinned worker image containing Git, CA roots, Railpack, and
  `buildctl`.
- Align Compose, OCI, Debian, `versions.yaml`, and doctor checks.
- Replace obsolete Railpack Dockerfile emission with `prepare` plus the pinned
  gateway frontend.
- Create the canonical deployment in every intake path, including `up`,
  GitHub, templates, and image deploy.
- Add outbox-driven transactional intake and trigger uniqueness.
- Persist built image and artifact truth before deploy.
- Resolve previous active state on the server.
- Make state transitions occur at real lifecycle boundaries.

Exit: Node, Python, Go, and Dockerfile samples deploy through real tools on a
fresh disposable stack and appear in one canonical timeline.

Current checkpoint (2026-07-27) — **Executed toolchain / Integrated full
deploy**: real BuildKit, registry, Railpack, Dockerfile, digest comparison, and
emitted-app probes are executed by `toolchain`. Canonical intake/artifact
state and activation are integrated. Two fresh complete `dataplane` runs and
the external process-fault matrix remain release evidence gates. Stock Compose
now grants exact-image trust to the digest-pinned official PostgreSQL image
without weakening the local-registry-only and immutable-digest policy.

### Gate 2 — configuration, secrets, and volumes

- Add durable per-service/environment configuration and resolved snapshots.
- Apply root, builder, Dockerfile, build/start/pre-deploy commands, target
  port, health, restart, and drain settings.
- Implement the PostgreSQL variable vault and worker resolver.
- Use BuildKit secret mounts and runtime injection.
- Complete volume provisioning, mounts, attachment arbitration, cleanup, and
  restart recovery.

Exit: config and secrets survive API/worker restart; a volume-backed app
retains data across deploy and host restart; no secret appears in evidence.

Current checkpoint (2026-07-27) — **Executed**: migrations 0023, 0024, 0027,
and 0030 cover scoped service config, the encrypted variable lifecycle, real
volume backup execution, and durable pre-deploy runs. Workers consume immutable
config/variable snapshots; Docker applies command, port, restart, health,
drain, mounts, and replica-safe volume policy. Real Docker tests prove
persistence across container replacement and staged restore. A clean VM
restart and installed off-host destination remain external evidence.

### Gate 3 — safe networking and HTTPS

- Add networking/domain/route/backend/certificate stores.
- Fix HTTP-to-HTTPS behavior and the Traefik-to-environment-network path.
- Implement domain, certificate, route, and activation reconcilers.
- Add HTTP/TCP readiness, route acknowledgement, ingress synthetic probes,
  active CAS, overlap, and drain.
- Implement generated domains, then custom-domain ownership and issuance.

Exit: private-by-default deploys stay private; an explicit domain action
returns a reachable HTTPS URL; failed candidates never change healthy traffic.

Current checkpoint (2026-07-27) — **Integrated**: migrations 0022 and 0026,
the durable route reconciler, trusted-ingress enrollment, acknowledgement and
synthetic-probe gates, active-pointer CAS, and ACME/domain controllers are
wired. Real local Traefik collection/routing dependencies are exercised, but
delegated DNS, certificate issuance/renewal, and public HTTPS on an installed
VM remain external evidence.

### Gate 4 — observability, runtime actions, and recovery

Current checkpoint (2026-07-27) — **Executed core / Integrated whole gate**:
migrations 0028, 0029, 0031, and 0033 wire real log/metric/event collection,
distinct runtime actions, monitors/incidents, and notification delivery.
Docker/PostgreSQL/Traefik collector tests and Docker volume recovery are
executed. `midnight-recovery` plus the built-in `restore` loop destructively
restore PostgreSQL and platform files with authenticated/encrypted evidence
and rollback. Its plaintext discovery index is authenticated through a salted,
310,000-round PBKDF2-HMAC-SHA256 derivation, and an fsync'd apply journal makes
each filesystem/database phase crash-resumable or explicitly rollbackable.
Installed real-origin delivery, process-death convergence, and off-host
whole-platform recovery remain external runner gates.

- Collect real build, deploy, Docker runtime, and Traefik HTTP logs.
- Add scoped query, retention, cursor, and live-tail APIs.
- Collect real resource metrics and runtime events.
- Add typed resource events, bounded retention/downsampling, deployment
  markers, and HTTP status/latency/byte rollups from Traefik access logs.
- Add built-in monitor evaluation and in-app, signed webhook, and SMTP delivery
  with deduplication, retry, and dead-letter state.
- Implement distinct restart, redeploy, rollback, stop, cancel, and remove.
- Reconcile crashes, restarts, leaked candidates, stale routes, and stale
  volume attachments.
- Add scheduled backup policies and jobs, stream actual bytes through
  compression and encryption, persist true SHA-256 checksums, restore into a
  replacement volume, verify it, then stage the attachment swap while
  retaining the old volume.
- Extend doctor with collector lag/spool, telemetry retention, queue lease,
  route reload, certificate renewal, monitor delivery, backup canary, and
  verified-restore-age checks. Wire the existing support-bundle library into a
  redacted, preview-before-export CLI flow.

Exit: a failed deployment can be diagnosed from real evidence, all actions
have correct semantics, and destructive restore testing recovers the app.

### Gate 5 — GitHub, CLI, and production GUI

- Wire GitHub App configuration into the real server.
- Mint installation tokens in worker memory.
- Add trigger deduplication, CI-wait handling, and commit status.
- Separate monorepo root, watch paths, and config path.
- Make `midnight up` a complete first-run flow.
- Generate clients from OpenAPI.
- Finish the GUI workflows and browser acceptance suite.

Exit: a user can deploy from a fresh local directory or a private GitHub
repository and reach the same deployment, logs, actions, and URL in CLI and GUI.

Current checkpoint (2026-07-29) — **Integrated**, with the GUI slice
**Executed** against a seeded real API: GitHub App wiring, canonical
workspace/repository/service authority, repository-scoped memory-only token
minting, exact push routing, independent monorepo fields, strict human CLI/GUI
link flows, capability gates, and desktop/mobile journeys are present. The
real service settings surface mounts the source panel plus runtime-kind,
replica, cron-occurrence, and TCP-proxy controls. Durable webhook
retry/dead-letter handling, production commit-status dispatch, a real private
repository, and an installed-instance CLI/GUI journey remain incomplete or
external evidence.

### Gate 6 — environment and template workflows

- Environment create/update/delete and transactional config+variable duplicate
  are wired. Full config-as-code sync remains unavailable because the current
  document cannot express scoped service config or encrypted variable versions;
  domains, volumes, deployments, and running instances are explicitly omitted
  from duplication rather than silently copied.
- Branch-specific source and config
- Staged changes
- PR environment lifecycle, focused service selection, comments, status, TTL
- Deployment config-as-code separate from stack topology config
- Template manifest v2 with real volumes, routes, variables, compensation, and
  health aggregation
- Keep the unified official-database saga convergent for PostgreSQL, Redis
  durable, MySQL, and MongoDB single-node; mark success only after
  private-network connectivity, persistence, health, and an engine-native smoke
  test pass.

Exit: environment and template success is based on active workloads, not
resource materialization.

Current checkpoint (2026-07-29) — **Executed PostgreSQL / Integrated additional
official databases and environment workflows**: operator environment lifecycle
and transactional config-variable duplication are wired. Loop 3.1 additionally
admits human creation of one ordinary unprotected environment through the
atomic PostgreSQL tenant coordinator and the REST/CLI/GUI contract. It does not
admit human item mutations at that checkpoint. Loop 3.2
separately admits exact service/environment config GET/PUT for human
sessions with `service.read`/`service.write`, the restricted-environment deny
overlay, exact-version CAS, and an atomic
authorization/config/idempotency/response/audit commit. Saving configuration
does not deploy or restart a service. Loop 3.3 separately admits strict
name/slug/tier metadata PATCH with `environment.write`, singleton idempotency,
resource-version CAS, exact replay, and atomic audit while keeping
protection/delete/duplicate/copy/runtime changes closed. Migration 0036 and the canonical
database saga uses exact digest-pinned official images for PostgreSQL 16, Redis
7 durable, MySQL 8.4, and MongoDB 7 single-node. Real Docker evidence proves
engine-native write, container replacement, and same-volume marker read.
Verifier/retry contracts bind reads to the exact replacement identity and
require metadata-only evidence plus probe cleanup before success; a complete
installed worker pass remains in the external data-plane gate. Computed template
variables now reference the exact created service slug returned after any
conflict disambiguation.

Loop 3.4 additionally admits the exact HumanSession-only nested
service/environment variable collection and item lifecycle. Reads are
metadata-only; create/rotate/tombstone use server-derived tenant ownership,
current `variables.read`/`variables.write`, strict 64-KiB write-only bodies,
portable 255-byte create names, positive-version CAS, singleton idempotency,
reauthorized replay, and one
atomic encrypted-state/history/response/audit boundary. Migration 0051
enforces canonical scope lineage. Immutable deployment snapshots pin exact
variable versions plus service aliases, and production worker decrypt fails
closed unless execution-attributed redacted hash-chained evidence is persisted
before injection.
The global compatibility API remains recovery-operator-only and project
automation is denied. Saving variable desired state does not deploy or restart
a service.

This follows Railway's
[database](https://docs.railway.com/databases),
[database reference](https://docs.railway.com/databases/reference), and
[template](https://docs.railway.com/templates) product baseline without
claiming managed HA. PR environments, staged changes, and full config-as-code
sync remain `Contract` or `Missing`.

### Gate 7 — replicas, workers, cron, and TCP

- One deployment instance row per replica
- Multi-backend routes and safe scale-down
- Background worker UX with no public route
- Five-field UTC cron, minimum interval, idempotent due-window invocation,
  overlap skipping, and one-off terminal states
- Real L4 TCP proxy allocation and reconciliation

Exit: scale, worker, cron, and TCP behavior is live-tested and volume safety
constraints are enforced.

Current checkpoint (2026-07-27) — **Executed TCP / Integrated replicas and
cron**: migrations 0032, 0034, and 0035 add cron definitions/occurrences, TCP
endpoints, and durable stateless deployment instances. Replica readiness,
multi-backend cutover, scale-down cleanup, runtime-action fan-out, cron
due-window idempotency/overlap skipping, and single-host TCP reconciliation are
wired. Real Docker/PostgreSQL evidence covers published TCP traffic, scoped
enrollment, redeploy, withdrawal, and detachment. The semantics track Railway's
[cron jobs](https://docs.railway.com/cron-jobs),
[replica optimization](https://docs.railway.com/deployments/optimize-performance),
[CLI scale](https://docs.railway.com/cli/scale), and
[TCP proxy](https://docs.railway.com/networking/tcp-proxy) documentation.
Multi-region placement and a managed public TCP edge remain `Missing`.

## Remaining autonomous implementation loops

The route/OpenAPI reconciliation gate completed on 2026-07-28. The remaining
repository work is dependency ordered below. A later loop must not be used to
bypass an earlier authority boundary.

| Loop | Scope | Exit |
|---|---|---|
| 1 — identity clients and automation credentials (executed 2026-07-28) | GUI and CLI durable human sessions, remote logout, tenant overview, recovery-token separation, encrypted sensitive-response idempotency, typed `ci-deploy-v1` admission, human-managed token APIs/CLI/GUI, audit, and scoped deployment use are implemented. Migration 0041 stores verifier-only project credentials. Migrations 0042–0043 and ADR 0034 add verifier-only, single-use invitation storage, replacement lineage, signed pagination, exact-scope listing, manual one-time delivery, and atomic create/reissue/revoke/accept workflows across REST, CLI, and GUI. Invitation and token raw values are returned only in encrypted one-time/replay responses and are never put in URLs, audit, list responses, or browser storage. | GUI and CLI login, identity, logout, revocation, tenant overview, token lifecycle, and invitation lifecycle pass without sending a human credential to an operator-only API. A managed project automation token can deploy only its own project and never receives installation-wide authority. Live PostgreSQL tests prove invitation lifecycle races, stale-session denial with zero artifacts, encrypted replay, membership creation, and audit-chain integrity. |
| 2 — atomic service and deployment access | Extend the PostgreSQL tenant coordinator and indexed tenant reads to service lifecycle, source/up/deploy intake, canonical deployment/operation/event reads, and the exact action paths needed for a first deployment. | A human session can create a service, deploy it, follow durable progress, inspect the result, and lose access immediately on revocation or grant removal, with no cross-tenant or partial-write window. |
| 3 — tenant-safe configuration and data controls | Admit environment lifecycle, service config, variables, domains, volumes/backups, templates, logs/metrics, and runtime actions one route at a time through explicit capabilities, canonical lineage, idempotency, and audit boundaries. | The same human principal sees and mutates only authorized project/environment state; every unsupported path remains visibly unavailable instead of falling back to operator authority. |
| 4 — Railway workflow semantics | Add canonical multi-resource staged changes, config-as-code synchronization, account recovery, optional invitation email delivery, and PR environment lifecycle with focused services, TTL, comments, and status. | A configuration workflow is resumable and atomic across resources, and ephemeral environments converge or clean up without synthetic success. |
| 5 — source and runtime hardening | Complete the installed private-GitHub journey, enforce build network/filesystem isolation, and close any source-token, trust-policy, queue/reconcile, or packaged-runtime gap exposed by Loops 1–4. | A private repository deploy succeeds through the packaged workers while secrets, trust policy, isolation, retries, and restart convergence remain fail closed. |
| 6 — local convergence | Run `fast`, `security`, `integration`, `ui`, `toolchain`, and the built-in destructive `restore` loop repeatedly from the same commit. | All repository-owned loops pass without retrying away a flake and their evidence records the tested commit. |
| 7 — installed-system proof | Run `dataplane`, `fault`, `observability`, clean-VM/DNS/ACME, live-instance, and installed off-host restore extensions using validated external runners. | Two fresh data-plane runs plus the fault, delivery, HTTPS, restart, and source-destruction matrices pass with retained evidence. Missing runner infrastructure remains an explicit external block, never a smaller substitute. |
| 8 — distributed profile (not single-VM parity) | Implement the multi-host scheduler, distributed private networking, listener failover, multi-region placement, and edge routing. | Only this loop permits multi-host, multi-region, global-edge, or equivalent availability claims. |

Loop 2 checkpoint (2026-07-29) — **the repository-owned human
service/deployment path, canonical source link, and durable webhook intake are
implemented; installed evidence remains**: human
`POST /services`
revalidates the session and grant and commits the idempotency reservation,
service, exact response, and audit entry atomically. The narrow human
`POST /services/{service_id}/deployments` accepts a lowercase SHA-256 image
digest and optional exact `route:{public:true}` generated-route opt-in. It then
commits its operation, deployment, deployment-route binding, durable
domain/route intent, events, exact response, outbox intent, and audit evidence
in one serializable transaction. REST, CLI, and GUI exercise that same route
with human authority and stable exact-intent replay keys; they do not fall back
to GraphQL or the operator credential. Human deployment list/latest/item reads
use canonical tenant-filtered rows and batch route evidence by those exact
deployment IDs. Exact operation status/events/logs use canonical deployment
lineage with live-stream revalidation. Cross-tenant and removed authority are
concealed.

The exact human `POST /services/{service_id}/up` path preflights current
authority and durably reserves its advertised digest and exact compressed
length before reading a bounded archive. It validates the complete gzip stream,
then revalidates authority and publishes the digest-addressed blob under the
quota-global, workspace, and artifact-lifecycle locks. The accepting
serializable transaction stores the operation, deployment, event, outbox,
exact response, audit row, artifact metadata, operation/deployment ownership,
and durable workspace/digest charge before consuming the reservation. The API
never extracts a human archive; the build worker verifies and materializes a
unique per-attempt checkout. `midnight up` supplies the exact digest and
length, a deterministic exact-intent key, refuses redirects, validates the
accepted response envelope, and follows only the tenant-scoped
operation-event route. Live PostgreSQL and real-filesystem tests execute the
quota ledger, concurrent admission, cascade survival, two-pass
quarantine/reconciliation, renewal/crash markers, bounded fairness, and paused
publication lock orderings. Project automation and recovery-operator uploads
retain the legacy extraction path until separately migrated. Loop 2 upload
authority is complete only for the human product path described here.

The exact empty-body human deployment-action route now admits redeploy,
rollback, restart, stop, and remove. A separate PostgreSQL coordinator
revalidates current session/RBAC authority, resolves canonical deployment
lineage, fences replay by actor/project/concrete route, commits durable action
and outbox state, stores the exact response, and appends audit evidence in one
serializable transaction. CLI and GUI action controls use that REST route;
project-automation credentials, GraphQL, and MCP do not inherit it. Live
PostgreSQL repetition proves concurrent same-key replay across sessions,
runtime and image action lineage, demoted-role replay denial, cross-service
concealment, pending-runtime conflict fencing, and audit rollback with no
partial state.

The exact empty-body human
`POST /operations/{operation_id}/cancel` route now requires
`deployment.trigger.cli` on one canonical deployment lineage. Migration 0047
adds terminal canceled outbox state. One serializable coordinator cancels
pending/delivering outboxes and pending build/deploy jobs, or records a
durable request when a worker already has a lease. Build and deploy workers
then atomically converge canonical operation/deployment state at named safe
points before deploy handoff or traffic promotion. CLI and GUI use the same
route; project automation does not. Disposable PostgreSQL coverage proves
concurrent replay, demoted replay denial, viewer/cross-tenant zero-artifact
denials, cancellation/dispatcher races, leased-worker fencing, canonical
worker convergence, and audit-failure rollback. An active BuildKit solve is
not interrupted; the request is honored after the solve and before handoff.

Migration 0048 and ADR 0037 first bind one exact deployment to its optional
generated route while omission remains private. Migration 0052 and ADR 0040
add the nested HumanSession list/create/get/delete lifecycle and the exact
service-managed binding. The hostname, route name, group, path, target port,
and TLS choice remain server-owned. Creation binds current open deployments;
future deploy/redeploy/rollback intake inherits the route. Multi-route
stage/commit is strict and preserves only captured prior backend ports during
zero-downtime target-port changes. Canonical reads publish an HTTP URL only
after ready backend, current controller revisions/hash, Traefik
acknowledgement, and a trusted Host-header ingress probe agree. Canonical
promotion rechecks that evidence before moving the active pointer. This is not
public DNS or Internet reachability evidence. Custom domains,
TLS/certificates, path/port editing, and broader ownership workflows remain
Loop 3.

Migration 0049/ADR 0038 admit current-authority human GitHub source linking.
Migration 0050/ADR 0039 commit verified deliveries before acknowledgement,
freeze service discovery at database receipt, persist immutable target
snapshots, revalidate current source versions at staged intake, and converge
bounded delivery/target retries to metadata-only terminal or dead-letter
state. Recovery-operator redrive is exact-empty-body, idempotent, audited, and
transactional. The production contract is at least once; canonical downstream
trigger keys suppress duplicate work after ambiguous completion. Memory mode
remains explicitly synchronous and is not durable evidence. GitHub acceptance
shares the installation mutation fence, terminal payload bytes purge after 30
days, and migration rollback refuses to erase acknowledged v50 deliveries.

The repository-owned Loop 2 authority and intake gate is complete. A live
private-repository deploy and process-death matrix remain Loop 5/7 evidence,
not a reason to relabel the local gate as `Executed`. The operation `fail`
endpoint remains recovery-only and is not treated as a missing Railway user
action.

Loop 3.1 checkpoint (2026-07-29) — **ordinary human environment creation
became integrated; the remaining environment lifecycle stayed gated at that
checkpoint**. The exact
`POST /environments` route accepts either an operator recovery bearer or a
durable human session. Human admission requires current `environment.write`
on the named project, a singleton `Idempotency-Key`, and
`is_protected` omitted or false. The PostgreSQL coordinator revalidates the
session and grant and commits the environment, exact replay response,
idempotency state, and audit evidence atomically; missing and cross-tenant
projects are concealed. `midnight environment create` sends a caller-provided
key or generates one with the project/service-create convention. At that
checkpoint the slice did not admit human item mutation, delete, duplicate,
protected creation, or configuration/variable copy.

Loop 3.2 checkpoint (2026-07-29) — **tenant-safe service/environment
configuration is integrated; broader data controls stay gated**. The exact
`GET` and `PUT /services/{service_id}/environments/{environment_id}/config`
route accepts either operator recovery or a durable human session. Human GET
requires current `service.read`; PUT requires current `service.write`, exactly
one `Idempotency-Key`, a strict non-null body bounded to 32 KiB, and an exact
`expected_version` compare-and-swap. Both operations resolve one canonical
service/environment/project lineage. Missing, cross-tenant, wrong-lineage, and
restricted environments are concealed from non-admin humans. The PostgreSQL
PUT coordinator commits current authority, config state, exact success or
terminal version-conflict response, idempotency state, and audit evidence
atomically; replay reauthorizes first. REST, CLI, and GUI use this path without
an operator fallback. CLI-generated keys are deterministic for the exact
service/environment/body intent. Project automation, GraphQL, MCP, human
environment delete/duplicate, variables, domains, volumes/backups, templates,
logs/metrics, and other Loop 3 mutations remain separately gated.
A successful PUT only changes desired state for a later immutable deployment
snapshot; it does not claim deployment, restart, runtime apply, or installed
evidence.

Loop 3.3 checkpoint (2026-07-29) — **ordinary environment metadata update is
integrated; protection and broader lifecycle stay gated**. The exact
`PATCH /environments/{environment_id}` route accepts either recovery-operator
compatibility or a durable human session. The human body is one strict
non-null object bounded to 16 KiB with positive `expected_version` and at least
one explicitly present `slug`, `name`, or `tier`; unknown, null, protection,
project, duplication, and copy fields are rejected. Current
`environment.write`, canonical project/workspace lineage, and the
restricted-environment overlay are revalidated before replay. The PostgreSQL
transaction compare-and-swaps `resource_version` and atomically commits the
metadata, exact `200` or terminal version/slug-conflict response, singleton
idempotency, and audit evidence. The CLI sends only explicit fields, derives a
deterministic key from the exact target/body when none is supplied, and
validates the returned target, metadata, and incremented version before
claiming success. A successful PATCH changes control-plane metadata only; it
does not change protection or restriction, delete, duplicate, copy
configuration, deploy, restart, or apply runtime state. Project automation,
GraphQL, and MCP remain denied.

Loop 3.4 checkpoint (2026-07-29) — **tenant-safe service variables are
integrated; shared variables and staged workflows stay gated**. The exact
HumanSession-only
`GET|POST /services/{service_id}/environments/{environment_id}/variables` and
`GET|PATCH|DELETE /services/{service_id}/environments/{environment_id}/variables/{variable_id}`
routes require current `variables.read` or `variables.write` after resolving
canonical workspace/project/service/environment lineage and applying the
restricted-environment overlay. Migration 0051 derives ownership, validates
scope integrity, preserves the shared sentinel for legacy storage, and leaves
encrypted payloads untouched. Human bodies are strict, bounded to 64 KiB, keep
`value` write-only, constrain new names to portable 255-byte identifiers while
retaining legacy metadata/item compatibility, require positive int64 CAS
versions for rotate/delete, and require exactly one physical
`Idempotency-Key` on every mutation. The
PostgreSQL coordinator atomically commits current authorization, encrypted
active state and immutable history, exact success or terminal conflict bytes,
idempotency, and audit evidence; replay reauthorizes first. REST, CLI, and GUI
use only the nested route. Deployment snapshots pin exact versions and service
aliases so retries do not consult mutable names. Production build/runtime
decrypt persists an execution-attributed redacted hash-chained record before
injection and fails closed on missing correlation or audit failure. The
unchanged global `/variables*` routes remain
recovery-operator-only, while project automation, GraphQL, MCP, and human
shared-variable mutation remain closed. Saving desired variables neither
deploys nor restarts a service. This deliberately implements the core flow
described by [Railway variables](https://docs.railway.com/variables) without
claiming Railway's staged multi-resource apply semantics.

Loop 3.5a checkpoint (2026-07-29) — **tenant-safe generated HTTP domains are
integrated; custom DNS ownership and TLS stay closed**. The exact
HumanSession-only
`GET|POST /services/{service_id}/environments/{environment_id}/domains` and
`GET|DELETE /services/{service_id}/environments/{environment_id}/domains/{domain_id}`
surface derives canonical lineage, the one-label generated host, path `/`,
current immutable target port (or server default), `public=true`, and
`tls=false`. It enforces current `domains.read`/`domains.write`, the
restricted-environment overlay, positive-version delete CAS, singleton
idempotency, reauthorized exact replay, and one serializable
domain/route/binding/response/audit transaction. Migration 0052 adds exact
multi-route deployment identity and service-managed bindings, conservatively
backfills compatible generated routes plus open deployments, and refuses
ambiguous legacy generated singletons with actionable scope diagnostics.
Product creates mint one opaque `dom_*` incarnation ID across transaction
retries while preserving the generated hostname and route identity. Deletes
retain the exact ID and scope in `retired_product_domain_ids`; later product
create/adoption rejects recovery reuse of that identity, and downgrade refuses
to discard nonempty retirement history.
Current and future open deployments bind the exact managed route. Strict
group CAS, late binding refresh, lock-order serialization, bounded
compensation, and the independent canonical publication fence prevent a
blocked or unverified sibling from advancing the active pointer.
Traefik acknowledgement is revision-specific and exact: artifact hash, active
rule/service/TLS/middleware shape, complete backend set and health must agree,
and an overlapping old rule is rejected before probing. A cross-process
environment publication lock spans snapshot load through publication,
Traefik verification/probe, and final observation CAS. An indeterminate
PostgreSQL canonical COMMIT is accepted only after a dedicated session
advisory lock is acquired before opening the read-only Repeatable Read exact
proof snapshot. The candidate, expected prior state, route publication, and
any template terminal evidence must all have committed together; otherwise
compensation remains fail-closed. Tenant and operator GUIs retain separate nested-product and
flat-recovery domain authority, and neither trusted-ingress state nor a route
URL is presented as public browser reachability.

The next repository-owned dependency is Loop 3.5b: tenant-safe custom HTTP
domain ownership desired state and verification while TLS remains disabled.
It must use a server-generated ownership challenge, exact hostname
normalization/single ownership, current authority on every verification
attempt, bounded resolver observations, versioned lifecycle, and no route
publication before ownership is proven. The existing flat `/domains*` API,
CLI, and GUI remain recovery-operator compatibility surfaces until that gate
lands. Generated wildcard TLS and custom TLS require production lifecycle
construction, issuance/renewal fencing, secure state, restore, and installed
public evidence. Honest public reachability and HTTPS proof still require real
delegated DNS, ingress/firewall, provider/ACME credentials, and a clean
installed host.

Current dependency edge:

```text
identity clients/tokens
        ↓
service + deploy authority
        ↓
config/data authority
        ↓
workflow semantics
        ↓
source/runtime hardening
        ↓
local convergence
        ↓
installed external proof
```

The distributed profile branches after the single-host control-plane
semantics are stable; it is not a prerequisite for an honest single-VM
Railway-like release.

## Streamlining work

1. Make `web-next` the only production GUI.
2. Make REST/OpenAPI the only writable public control plane until GraphQL is
   generated from or delegates to the same services.
3. Put contract-only enterprise, billing, HA, multi-region, Kubernetes, and AI
   planner routes behind `MIDNIGHT_EXPERIMENTAL=1`.
4. Keep build and runtime workers separate because their privilege and
   dependency profiles differ; consolidate queue, operation, event, and outbox
   libraries beneath them.
5. Use one deployment service for API, GitHub, CLI, GUI, template, and agent
   triggers.
6. Use one `ServiceEnvironmentConfig` and one resolved snapshot type instead
   of parallel REST, GraphQL, template, and worker configuration shapes.
7. Delete fakes only when their replacement integration tests exist; keep
   fakes for unit tests but never use them as release evidence.

## Convergent engineering loops

Extend `scripts/railway-parity-loop.sh` rather than creating unrelated test
entrypoints.

### `fast`

- formatting and repository lint
- targeted Go packages
- GUI typecheck and component tests
- schema/OpenAPI generated-client drift

Run on every change and repeat three times for state/order sensitivity.

### `toolchain`

- build the production build-worker image from the current tree
- run pinned Railpack `prepare` and its digest-pinned `gateway.v0` frontend
  for Node, Python, and Go, plus BuildKit's Dockerfile frontend
- push to a disposable real registry and compare registry/BuildKit digests
- run the emitted image and probe its application endpoints
- prove an env-backed BuildKit secret reaches a Railpack build while its
  deliberately echoed value is redacted before all durable evidence
- persist plan, info, and BuildKit metadata evidence under ignored local state

This is narrower than `dataplane`: it proves the external source-build
toolchain, not API intake, durable queueing, deployment scheduling, ingress, or
TLS.

### `dataplane`

- disposable PostgreSQL, registry, BuildKit, Traefik, API, workers, observer
- real pinned Railpack and BuildKit
- Node, Python, Go, and Dockerfile fixtures
- image digest verification and artifact assertions
- generated route through ingress

Recreate state for each run; no reuse of a developer’s local services.

### `fault`

Inject process death after:

- intake commit
- checkout
- image push
- deploy schedule
- readiness
- route publication
- route acknowledgement
- active-pointer CAS
- old-instance SIGTERM

Each case must converge to old-active or new-active truth without split state,
false success, or leaked public routes.

### `security`

- upload ignore and archive traversal corpus
- GitHub leakage scans; keep the built-in live BuildKit/Railpack secret
  injection and persisted-evidence scan green
- hostname/rule injection corpus
- cross-project/environment network isolation
- auth/RBAC and idempotency cross-actor tests
- TCP source policy when implemented
- log secret-search oracle and opaque-cursor tests

### `observability`

- real build/runtime/HTTP log origins
- agent WAL restart, sequence deduplication, retention, and pressure behavior
- CPU, memory, disk, network, restart, and HTTP rollup samples
- monitor `OK -> PENDING -> FIRING -> RESOLVED`
- signed webhook retry/deduplication and packaged runbook validation

### `ui`

- seeded real API
- desktop and mobile flows
- keyboard and accessibility checks
- screenshots for stable major states
- network loss, SSE resume, HTML/error sanitization, partial capability states

### `restore`

- create platform resources and volume data
- back up to a disposable off-host destination
- destroy the source state
- restore
- verify checksums, secrets, deployment history, registry digest, certificate
  state, volume contents, and reachable application

### `vm`

- clean supported Debian/Ubuntu VM
- package install and live doctor
- source deploy
- real delegated DNS and ACME staging/production as appropriate
- HTTPS and WebSocket probe
- restart API, workers, Docker, Traefik, and the VM
- uninstall/reinstall preservation check

### Convergence rule

A gate is complete only when:

1. its acceptance tests are committed,
2. the same commit passes `fast`,
3. two fresh `dataplane` runs pass,
4. relevant fault/security/UI/restore loops pass,
5. no ignored flake or manual database repair was needed, and
6. evidence is stored as CI artifacts with component versions and commit SHA.

“Run until green” must not mean retrying a nondeterministic failure away.

### Current runner evidence boundary

| Mode | Grade | Current boundary |
|---|---|---|
| `local` | Executed | Passed: repository checks, all Go tests/vet/builds, both dashboard workspaces and audits, and memory-mode acceptance. |
| `integration` | Executed | Passed: PostgreSQL/testcontainer suites plus real Docker TCP publication, redeploy, withdrawal, and network detachment. |
| `toolchain` | Executed | Passed: real Railpack/BuildKit/registry/emitted-application evidence is retained. |
| `security` | Executed | Passed: security contracts plus live Docker isolation and TCP policy proof. |
| `ui` | Executed | Passed: the production GUI is built and exercised in desktop/mobile Chromium against a freshly seeded real API without request interception. |
| `restore` | Executed | Passed: the built-in runner destroys and restores real PostgreSQL plus platform filesystem state and retains authenticated evidence. |
| `dataplane` | Integrated | Requires `MIDNIGHT_PARITY_DATAPLANE_RUNNER`; absence exits 78 rather than substituting a smaller test. |
| `fault` | Integrated | Requires `MIDNIGHT_PARITY_FAULT_RUNNER` for real process-death injection; absence exits 78. |
| `observability` | Integrated | Built-in collector proofs exist, but the installed real-origin/delivery run requires `MIDNIGHT_PARITY_OBSERVABILITY_RUNNER`; absence exits 78. |
| `vm` | Integrated | Requires `MIDNIGHT_PARITY_VM_RUNNER` on a clean supported Debian/Ubuntu VM; absence exits 78. |
| `live` | Integrated | Requires an installed instance through `MIDNIGHT_API_BASE` and `MIDNIGHT_API_TOKEN`; missing prerequisites exit 78. |
| installed off-host restore extension | Integrated | Optional `MIDNIGHT_PARITY_RESTORE_RUNNER` extends the built-in destructive proof to operator-controlled storage and the complete installed asset set. |

All external runner grades require the validated evidence protocol in
[`railway-parity-runner-evidence.md`](../railway-parity-runner-evidence.md);
runner exit status alone is not evidence.

## Release acceptance matrix

### Build and deploy

- Real Railpack Node, Python, and Go apps
- Explicit Dockerfile app
- Build cache hit and invalidation behavior
- Concurrent same-service builds
- Private GitHub monorepo with shared-package watch path
- Local upload with nested ignore/negation and secret files
- Build and pre-deploy failure preserve current traffic

### Runtime and routing

- App listening on injected target port
- HTTP readiness returns 503, then 200
- TCP fallback with no HTTP health path
- Traefik rejects a candidate generation
- Continuous requests observe no 5xx during healthy cutover
- WebSocket remains alive through configured drain
- Network creation failure does not use the default bridge
- Cross-environment and cross-project private access fails

### Data

- Variables survive API and worker restart
- References resolve deterministically; cycles fail safely
- Sealed values cannot be retrieved
- Secret changes affect the next deployment and never logs/history
- Volume data survives container, worker, Docker, and VM restart
- Volume-backed replicas greater than one are rejected
- Backup/restore destroys and recovers real state

### Operations

- SSE terminal-before-subscribe, reconnect, duplicate, gap, and slow-consumer
  cases
- Premature EOF never means success
- Duplicate webhook delivery and same-commit triggers create one deployment
- Restart does not build
- Redeploy builds current source/config
- Rollback uses the old digest and snapshot and loses a racing CAS safely
- Cancel before activation never moves traffic
- Crash restart counts and terminal states are correct

### GUI and CLI

- Login credentials work across every command
- Headless pairing login
- First-run `up`
- Domain generation and custom DNS instructions
- Real logs and metrics
- Deployment actions
- Honest unsupported/permission/error states
- Desktop, mobile, keyboard, and reduced-motion behavior

## Primary research references

- [Railway CLI](https://docs.railway.com/cli)
- [`railway up`](https://docs.railway.com/cli/up)
- [Railway deployments](https://docs.railway.com/deployments/reference)
- [Railway deployment actions](https://docs.railway.com/deployments/deployment-actions)
- [Railway health checks](https://docs.railway.com/deployments/healthchecks)
- [Railway restart policy](https://docs.railway.com/deployments/restart-policy)
- [Railway replicas and regions](https://docs.railway.com/deployments/optimize-performance)
- [Railway GitHub autodeploys](https://docs.railway.com/deployments/github-autodeploys)
- [Railway monorepos](https://docs.railway.com/deployments/monorepo)
- [Railway cron jobs](https://docs.railway.com/cron-jobs)
- [Railway environments](https://docs.railway.com/environments)
- [Railway config as code](https://docs.railway.com/config-as-code/reference)
- [Railway variables](https://docs.railway.com/variables)
- [Railway public networking](https://docs.railway.com/networking/public-networking)
- [Railway domains](https://docs.railway.com/networking/domains/working-with-domains)
- [Railway private networking](https://docs.railway.com/networking/private-networking)
- [Railway TCP proxy](https://docs.railway.com/networking/tcp-proxy)
- [Railway CLI scaling](https://docs.railway.com/cli/scale)
- [Railway databases](https://docs.railway.com/databases)
- [Railway database reference](https://docs.railway.com/databases/reference)
- [Railway templates](https://docs.railway.com/templates)
- [Railway volumes](https://docs.railway.com/volumes/reference)
- [Railway backups](https://docs.railway.com/volumes/backups)
- [Railway logs](https://docs.railway.com/observability/logs)
- [Railway metrics](https://docs.railway.com/observability/metrics)
- [Railway observability](https://docs.railway.com/observability)
- [Railpack CLI](https://railpack.com/reference/cli)
- [Railpack BuildKit frontend](https://railpack.com/reference/frontend/)
- [Railpack repository](https://github.com/railwayapp/railpack)
- [Railway dashboard layout update](https://railway.com/changelog/2026-03-20-new-dashboard-layout)
