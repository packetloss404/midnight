# Midnight Railway-Parity Review

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

- Date: 2026-07-27
- Scope: control plane, build/deploy workers, runtime, security, packaging,
  CI, CLI, legacy dashboard, and `web-next`
- Method: six specialist review/implementation lanes followed by repeated
  fast, local, integration, and supply-chain verification
- Delivery plan:
  [railway-parity-delivery-plan-2026-07-27.md](railway-parity-delivery-plan-2026-07-27.md)
- GUI comparison and product direction:
  [midnight-railway-ui-comparison-2026-07-27.md](midnight-railway-ui-comparison-2026-07-27.md)

## Verdict

Midnight now has an integrated single-host Railway-like product path rather
than only a control-plane foundation. Canonical deploy intake, real
Railpack/BuildKit builds, immutable image execution, private networks, durable
volumes, readiness-gated routing, real logs and metrics, runtime actions,
monitors, cron, replicas, TCP proxying, a PostgreSQL template, and destructive
platform restore all have production wiring.

That is not the same as a turnkey Railway replacement. The remaining release
boundary is primarily installed-system evidence: the repository still needs a
fresh supported-VM run that exercises package installation, a private GitHub
repository, delegated DNS/ACME, public HTTPS, process-fault convergence, and
off-host recovery as one system. Multi-host/multi-region execution, human
source linking and product-route lifecycle beyond the admitted local-source,
deployment-action, and cancellation journeys, PR environments, and managed
database breadth remain real product gaps. Project-scoped automation credentials are
implemented but still share the installed-system evidence boundary.
The matrix below deliberately separates those missing capabilities from
implemented code that is awaiting external proof.

## Railway Product Baseline

The comparison used Railway's current public documentation:

- [Services and deployment sources](https://docs.railway.com/services)
- [GitHub autodeploys](https://docs.railway.com/deployments/github-autodeploys)
- [Default production and isolated environments](https://docs.railway.com/environments)
- [Build-time and runtime variables](https://docs.railway.com/variables)
- [Private networking](https://docs.railway.com/networking/private-networking)
- [Public networking and automatic SSL](https://docs.railway.com/networking/public-networking)
- [Generated and custom domains](https://docs.railway.com/networking/domains/working-with-domains)
- [Volumes](https://docs.railway.com/volumes/reference)
- [Volume backups](https://docs.railway.com/volumes/backups)
- [Logs](https://docs.railway.com/observability/logs)
- [Metrics](https://docs.railway.com/observability/metrics)
- [Deployment actions](https://docs.railway.com/deployments/deployment-actions)
- [Deployment lifecycle and state](https://docs.railway.com/deployments/reference)
- [Restart policy](https://docs.railway.com/deployments/restart-policy)
- [Cron jobs](https://docs.railway.com/cron-jobs)
- [Database services](https://docs.railway.com/databases)
- [Database reference](https://docs.railway.com/databases/reference)
- [Templates](https://docs.railway.com/templates)
- [TCP proxying](https://docs.railway.com/networking/tcp-proxy)
- [Replicas and regions](https://docs.railway.com/deployments/optimize-performance)
- [CLI scaling](https://docs.railway.com/cli/scale)
- [Current dashboard layout](https://railway.com/changelog/2026-03-20-new-dashboard-layout)

## Implemented in This Pass

### Build and deploy correctness

- Source builds retain one operation through build-to-deploy handoff; only the
  deploy worker owns the terminal success transition.
- GitHub push delivery queues a structured build intent with an immutable SHA
  instead of a malformed deploy job.
- New projects receive a real default `production` environment, and GitHub
  builds target it.
- Deploy intake and worker payload decoding now require an environment before
  runtime side effects. `midnight up` sends the linked environment ID.
- Dashboard template deployments create or reuse the project's production
  environment.
- PostgreSQL wiring includes the canonical deployment store.
- Docker instances use `unless-stopped`.
- Build worker leases heartbeat during long work and enforce a bounded
  execution timeout.
- Successful local-upload checkouts are removed only from the configured
  owned staging root; failed work remains retryable.

### Runtime and package integration

- API and build worker share a durable upload root in Compose and Debian.
- BuildKit addresses and registry endpoint aliases propagate consistently.
- A local-registry verifier accepts only configured-registry references pinned
  to lowercase SHA-256 digests; external and mutable references remain closed.
- Stock Compose adds the exact digest-pinned official PostgreSQL, Redis, MySQL,
  and MongoDB images to the deploy worker's trust set. Tag-only images and
  every other external image still fail closed.
- Debian includes the build worker unit and modern dashboard assets.
- Package installation generates distinct random API bearer and agent HMAC
  secrets plus a raw 32-byte KEK with restrictive ownership and modes.
- Server UI resolution supports an explicit distribution directory, installed
  paths, executable-adjacent assets, and source-tree development.

### Security

- Agent registration, heartbeat, desired-state, and status routes are bearer
  protected.
- Heartbeat HMACs bind the full security-relevant request, including sequence,
  runtime state, timestamp, and maintenance/drain state.
- Production without a configured API bearer fails closed for protected reads
  and mutations.
- The CLI OAuth shim no longer transports operator tokens in URLs or cookies
  and only permits exact HTTP loopback callbacks with explicit ports.
- HTTP request/header/idle limits and graceful shutdown are configured without
  breaking streaming responses.
- Browser tokens are runtime-only `sessionStorage` values; production bundles
  contain no baked token fallback.

### GUI honesty and usability

- Removed fabricated settings saves, generated domains, DNS tokens, live
  states, variable persistence, and fake redeploy behavior.
- Image deploy requires an environment and immutable digest.
- GitHub UI says it connects a repository; it does not claim a deploy has
  occurred.
- Variable reads remain metadata-only; production wiring now persists
  encrypted envelopes and injects values only at build/runtime boundaries.
- Service settings mount real environment-scoped runtime-kind, replica,
  cron-occurrence, and TCP-proxy controls instead of preview-only cards.
- Added project navigation, responsive mobile rails/dock, skip navigation,
  reduced-motion behavior, correct ref forwarding, and route-level bundle
  splitting.

### Engineering loop and CI

- CI runs on pull requests and `main`, covers `web-next`, and preserves manual
  release controls.
- Go, Node, and frontend dependencies were upgraded; direct `x/net` and
  `x/text` vulnerability findings were removed.
- The repository lint guard is green, including package docs, ADR index, error
  registry, workflow policy, and classified non-error constants.
- The acceptance harness selects an isolated loopback port and works with the
  macOS system Bash under `set -u`.
- `scripts/railway-parity-loop.sh` provides repeatable `fast`, `toolchain`,
  `dataplane`, `fault`, `security`, `observability`, `ui`, `restore`, `vm`,
  `local`, `integration`, `all`, and `live` modes. The local, integration,
  toolchain, security, UI, and restore modes passed this convergence run.
  External dataplane, fault, observability, VM, and live prerequisites remain
  fail-closed rather than being replaced with smaller tests.

## Capability Matrix

Grades use the delivery plan's evidence definitions. `Executed` means a real
dependency was exercised locally and evidence was retained; it does not imply
that an installed public instance was tested. `Integrated` means the
production path is wired but still needs that broader external proof.

| Capability | Grade | Evidence or remaining problem |
|---|---|---|
| Projects, services, and environments | Integrated | PostgreSQL stores and REST/CLI/GUI creation paths are wired. Project creation creates protected `production`; Loop 3.1 admits ordinary unprotected human creation and Loop 3.3 admits strict name/slug/tier metadata PATCH with current `environment.write`, positive resource-version CAS, singleton idempotency, exact response replay, and atomic audit. Human protected-state changes, delete, duplicate, restriction changes, and configuration copy remain gated. Deploy intake requires explicit environment scope. Final proof is the clean-VM installed-instance flow. |
| Canonical digest-image deploy | Integrated | Intake, outbox, queue, readiness, canonical activation CAS, rollback-safe route publication, replica instances, and drain are wired. The disposable full-data-plane and process-fault runners remain external evidence gates. |
| Railpack and Dockerfile source build | Executed | The toolchain loop builds Node, Python, Go, and Dockerfile fixtures through real BuildKit/registry dependencies, probes emitted apps, compares digests, and scans retained evidence for a test secret. It does not by itself prove installed API-to-HTTPS deployment. |
| Private GitHub checkout and autodeploy | Integrated | Migration 0049 makes workspace-exclusive installations, exact repository grants, and canonical links authoritative. Migration 0050 commits verified payloads before acknowledgement, serializes GitHub receipt against installation mutation so temporal discovery follows commit order, persists immutable grant/link snapshots, and uses renewed fenced leases with bounded retry, dead letter, and atomic metadata-only redrive. A daily bounded pass purges terminal payload bytes after 30 days while retaining hashes and metadata. Signed staged intake revalidates exact current grant/link versions under the revocation lock; generic callers cannot submit App coordinates. The build worker rechecks current authority before token mint, before variable resolution, and before build solve, requests a repository-scoped `contents:read` token, constrains the clone target, and redacts transport failures. The contract is at least once. A live private-repository run, installed process-death proof, and commit-status round trip remain outstanding. |
| Monorepo configuration | Integrated | Root directory, watch paths, and config path are independent durable inputs. PR-environment lifecycle and config-as-code synchronization remain missing. |
| Variables and resolved config | Integrated | Encrypted PostgreSQL envelopes, fail-closed KEK wiring, references, metadata-only reads, exact-version and service-alias-pinned snapshots, BuildKit secret mounts, runtime injection, optimistic lifecycle, tombstones, and re-encrypted environment duplication are wired. Production build/runtime decrypt fails closed unless an execution-attributed redacted hash-chained audit record is persisted before injection; names, templates, plaintext, and infrastructure causes are excluded from product evidence. Loop 3.2 admits human service/environment config GET/PUT through current `service.read`/`service.write`, the restricted-environment overlay, exact-version CAS, singleton idempotency, exact replay, and an atomic config/response/audit commit. Loop 3.4 adds HumanSession-only nested service/environment variable list/create/get/rotate/tombstone across REST/CLI/GUI, with `variables.read`/`variables.write`, server-derived ownership, migration 0051 scope integrity, strict write-only bodies and portable create names, legacy metadata/item compatibility, positive-version CAS, atomic encrypted state/history/idempotency/response/audit, and reauthorized replay. The global `/variables*` surface remains recovery-operator-only, project automation stays denied, and no read returns a value. Saving config or variables does not deploy, restart, or prove runtime apply. Human shared-variable lifecycle and general staged multi-resource change sets remain gated. |
| Pre-deploy command | Integrated | A retained one-off execution, durable run identity/evidence, timeout, cleanup, and worker-restart idempotency gate activation. Installed failure/retry proof remains part of the external fault loop. |
| Single-host private networking | Executed | Environment-scoped Docker bridges, aliases, fail-closed scheduling, trusted-ingress enrollment, and Docker isolation tests exist. This is not multi-host service discovery. |
| Public HTTP domains and TLS | Partial | Migration 0052 and ADR 0040 add HumanSession-only generated-HTTP list/create/get/delete across the nested API, CLI, and service Networking panel with current `domains.read`/`domains.write`, exact service/environment lineage, restricted-environment concealment, resource-version delete CAS, atomic idempotency/audit, and one server-owned generated singleton. Compatible legacy routes and exact open deployments are backfilled; future deploy/redeploy/rollback intake inherits the service-managed route. Multi-route group promotion is strict. Revision-scoped Traefik resources must match the exact artifact, rule, service, backend set, health, and trusted probe before the canonical pointer moves; overlapping stale rules fail closed. Indeterminate canonical COMMIT is recovered only from exact durable active/prior/route/template proof. The flat domain lifecycle remains recovery-operator compatibility. Custom-host ownership and TLS stay independently closed. Loop 3.6 wires the existing Lego DNS-01 issuer and lifecycle controller into an opt-in, staging-first worker path with durable order references and fail-closed TLS admission; certificate installation into Traefik, delegated DNS, public HTTPS, renewal, and off-host reachability on an installed VM remain external evidence. |
| Volumes | Executed | Durable lifecycle, single-writer and replica safety, Docker mounts, restart/redeploy reuse, attachment recovery, and real-Docker persistence/replacement tests are present. VM-restart proof remains external. |
| Volume backup and staged restore | Executed | Real Docker volume bytes are archived, checksummed, restored into a verified replacement, and staged for an atomic attachment swap while retaining the source. Installed off-host-object-store proof remains external. |
| Platform backup and restore | Executed | `midnight-recovery` performs authenticated, encrypted platform backup plus destructive PostgreSQL/filesystem restore with checksum and rollback checks. The plaintext index is authenticated with salted, 310,000-round PBKDF2-HMAC-SHA256 key derivation, and apply uses an fsync'd phase journal to resume or roll back safely after a crash. The built-in restore loop retained real Docker/PostgreSQL evidence; registry/certificate/installed off-host recovery still needs the external extension. |
| Logs and metrics | Executed | PostgreSQL-backed scoped logs, opaque cursors, live tail, Docker logs/stats, runtime events, Traefik access logs, rollups, and real Docker/PostgreSQL/Traefik integration proofs are present. The external observability runner still has to prove installed build/runtime/HTTP origins together. |
| Monitors and notifications | Integrated | Built-in evaluation, incidents, in-app delivery, signed webhooks, SMTP, retry/deduplication, and dead-letter status are durable. Real installed webhook/SMTP delivery remains external evidence. |
| Runtime actions | Integrated | Restart, redeploy, rollback, cancel, stop, and remove use distinct durable action records and immutable snapshot rules; replica fan-out is wired. Human REST/CLI/GUI admit redeploy, rollback, restart, stop, remove, and canonical deployment-operation cancellation through current-authority serializable coordinators. Cancellation withdraws pre-lease work or converges at build/deploy safe points; it does not interrupt an active BuildKit solve. The complete process-death matrix remains external. |
| PostgreSQL template | Executed | The canonical saga atomically creates service/config/encrypted variables/volume/deployment/operation/outbox state for the exact digest-pinned official image. Real Docker evidence covers TCP readiness, private `psql` write, container replacement, marker read from the same volume, and probe cleanup. Computed variable references use the exact created service slug, including conflict disambiguation. This does not claim managed HA or off-host backup. |
| Redis, MySQL, and MongoDB templates | Integrated | The same atomic intake and exact-image-bound worker verifier now covers Redis durable, MySQL 8.4, and MongoDB 7 single-node. Real Docker tests prove each engine's native commands and volume persistence across container replacement; unit/retry contracts prove verifier sequencing, replacement identity binding, and cleanup-before-success. A complete installed worker pass remains part of the external data-plane gate. This does not prove managed HA, replica sets, sharding, or off-host backup. |
| Cron jobs | Integrated | Durable five-field UTC schedules enforce Railway's documented five-minute minimum; due-window idempotency and overlap skipping create one-off runs rather than long-lived services. A packaged long-running overlap drill remains external proof. |
| Stateless replicas | Integrated | Durable `r0..rN` instance identities, all-replica readiness, multi-backend routes, action fan-out, and reverse-order cleanup are wired, with a single-host ceiling of 50. Midnight does not claim Railway's multi-region placement or edge load balancing. |
| TCP proxy | Executed | Real Docker/PostgreSQL integration proves host-port publication, scoped worker network enrollment, an active backend, redeploy to a new backend, withdrawal, and final network detachment; contract tests cover ready-replica distribution/failover. The generated hostname is only an allocation label: authoritative DNS, firewall/NAT, original-client IP preservation, and internet reachability are not claimed. |
| Environment duplication | Integrated | Service config and active variables/immutable versions are copied transactionally and re-encrypted under fresh IDs. Domains, volumes, deployments, and instances are explicitly not cloned. |
| Authentication and tenant RBAC | Integrated | Migration 0038 adds explicit project workspace ownership, claim-time MFA, hashed/expiring/revocable human sessions, canonical workspace/project roles, atomic membership invariants, rate/session bounds, and durable cross-tenant membership concealment proofs. Human sessions can read only their authorized project, service, and environment metadata through indexed PostgreSQL predicates and inspect RBAC permissions; product reads fail closed unless session and grant stores share one verified authority. Migration 0039 and the atomic tenant mutation coordinator admit REST project creation, including its protected production environment, owner grant, idempotent response, and audit row in one serializable transaction. The same coordinator admits service creation with session/grant revalidation and atomic idempotency, product, response, and audit state. Dedicated coordinators admit digest-pinned image deployment, the exact human local-source upload route, redeploy/rollback/restart/stop/remove, canonical deployment-operation cancellation, and canonical GitHub source linking while atomically storing current authorization, actor/project/route replay state, canonical product/work state, exact responses, and audit evidence. Migration 0047 and ADR 0036 fence deploy/staged outboxes, pending jobs, and leased-worker safe-point requests. Live PostgreSQL coverage executes concurrent same-key replay across sessions, runtime/image lineage, demoted-role denial before replay, cross-service concealment, runtime conflict fencing, cancellation/dispatcher races, GitHub grant/link/revocation races, leased-worker convergence, and audit rollback. Migration 0045 and ADR 0035 provide immutable content-addressed archive storage, isolated worker materialization, terminal reference release/reconciliation, and automatic bounded cleanup with lifecycle locking, two-pass quarantine, and rollback restoration. Migration 0046 adds durable pre-body reservations, logical workspace retained and concurrent-upload limits, installation-wide upload backpressure, a cascade-independent workspace/digest charge ledger, locked publication, and proof-only release after successful CAS collection. Live PostgreSQL/real-filesystem coverage executes accepted transfer, cascade survival, workspace/global byte and count races, same/cross-workspace digest accounting, bounded fairness, renewal/crash markers, quarantine proof, rollback preservation, and publication lock ordering. A replay still validates and hashes the complete supplied upload body. REST, CLI, and GUI use tenant deployment/action/cancel/source-link routes without GraphQL or operator fallback, and `midnight up` uses the human source route with exact digest/length headers, deterministic exact-intent keys, and tenant-scoped event follow. Canonical deployment list/latest/item and exact operation status/event/log reads are tenant scoped; live event streams periodically revalidate authority. Other product routes, GraphQL, and MCP remain quarantined behind route-specific gates; operation failure marking remains recovery-only. Project-token and recovery-operator `up` retain the legacy extraction path until separately migrated. The human-path logical quota does not claim hard control of random staging, legacy uploads, or host filesystem capacity; installed process-fault evidence remains an explicit external gap. The dashboard and CLI create, inspect, revoke, deploy, act, cancel, and link GitHub sources with human sessions while preserving the installation-wide operator recovery credential as a separate authority. Migrations 0040–0041 and `internal/automationtoken` provide human-managed, HMAC-verifier-only project credentials whose REST/CLI/GUI lifecycle and typed `ci-deploy-v1` use are project scoped, auditable, and never inherit operator authority. Migrations 0042–0043 and ADR 0034 provide exact-scope invitation list/create/reissue/revoke/accept REST, CLI, and GUI flows with verifier-only storage, replacement lineage, signed pagination, encrypted one-time replay, and atomic membership/audit commits. |
| Multi-host and multi-region runtime | Missing | Agent/reconciliation contracts and planning types do not constitute a scheduler, distributed network, or global edge. Tenant identity/RBAC is tracked separately: the CLI and GUI use durable human sessions for scoped metadata, project/service creation, immutable-image/source deployment, deployment actions, and canonical GitHub source linking. GraphQL and MCP propagation remain gated. |

Generated-domain checkpoint (2026-07-29): migration 0052 and ADR 0040
supersede the one-deployment-only limitation of migration 0048/ADR 0037.
Durable human sessions can list, create, inspect, and delete the exact
service/environment generated HTTP singleton through the API, CLI, and
Networking panel without providing custom route properties. The transaction
binds current open deployments; future intake and image actions inherit the
exact service-managed route. Multi-route promotion fails on blocked or
unverified siblings, and the canonical active pointer moves only after exact
candidate backend plus current controller/Traefik/trusted-probe evidence.
Private-by-default omission and evidence-aware URL projection remain. Custom
domains, TLS/certificate issuance, editable route properties, public
DNS/Internet proof, installed webhook process-death proof, GraphQL, and MCP
remain gated.

Environment-create checkpoint (2026-07-29): the production
`POST /environments` contract now admits operator recovery and durable human
sessions. The human path requires current `environment.write` on the exact
project, a required singleton idempotency key, and an ordinary unprotected
request. The CLI accepts an explicit retry key or generates one with the same
convention as project/service create. Atomic PostgreSQL tests cover replay,
authorization loss, cross-tenant concealment, conflicts, and audit rollback.
This was the `Integrated` create-only slice at Loop 3.1; Loop 3.3 below
supersedes only the ordinary metadata-update gap. Delete, duplicate, protected
creation/state changes, restriction changes, and configuration copy are not
claimed.

Environment-update checkpoint (2026-07-29): the production
`PATCH /environments/{environment_id}` contract now admits recovery-operator
compatibility plus a narrow durable-human path. Human input is one strict
16-KiB object containing positive `expected_version` and at least one explicit
`slug`, `name`, or `tier`. The transaction revalidates current session,
canonical project authority, `environment.write`, and restricted-environment
policy before replay, then commits metadata, exact success or terminal
version/slug conflict, idempotency, and audit atomically. The CLI derives a
stable exact-target/body key when omitted and rejects mismatched success
responses. The resource version is exposed on environment reads. This remains
an `Integrated` metadata-only slice: it does not change protection/restriction,
delete, duplicate, copy data, deploy, restart, or prove installed runtime
application. Automation, GraphQL, and MCP remain closed.

Tenant-variable checkpoint (2026-07-29): Loop 3.4 now admits the exact
HumanSession-only service/environment collection and item routes for metadata
list/get plus create/rotate/tombstone. Migration 0051 derives canonical
workspace/project/service ownership and retains encrypted immutable history.
Current `variables.read`/`variables.write`, the restricted-environment overlay,
strict 64-KiB write-only bodies, positive-version CAS, singleton idempotency,
reauthorized exact replay, and atomic audit keep the surface tenant scoped.
REST, CLI, and the service drawer use the same nested routes without falling
back to the unchanged global recovery-operator API. Project automation remains
denied, values never appear in reads, and a save changes only desired state for
a later deployment snapshot. Those snapshots pin exact variable versions and
service aliases; production build/runtime decrypt persists redacted
hash-chained evidence before injection and fails closed if it cannot. This
implements the core service-variable flow
informed by [Railway's variable model](https://docs.railway.com/variables);
shared-variable human lifecycle, staged multi-resource changes, and automatic
redeploy remain separate gaps.

Tenant generated-domain checkpoint (2026-07-29): Loop 3.5a admits the exact
HumanSession-only nested service/environment list/create/get/delete lifecycle
for one server-derived generated HTTP domain. Migration 0052 and ADR 0040 add
exact service-managed/current-and-future deployment bindings, conservative
upgrade backfill, resource-version CAS, and multi-route group reconciliation.
REST, CLI, and the Networking panel use the same current-authority path.
Each product incarnation has a fresh opaque ID; transactional retired-ID
history prevents stale delete ABA and rejects recovery-mediated identity reuse
without changing the stable generated hostname or route identity.
Canonical activation independently requires exact candidate backend plus
current revision-scoped controller/publication/Traefik backend and
trusted-probe evidence. A cross-process publication lock spans load through
Traefik proof and final CAS. Indeterminate canonical COMMIT acknowledgement is
accepted only from a Repeatable Read snapshot opened after the dedicated
session advisory lock and exact durable active/prior/route/template proof. The capability
is still `Partial`: custom host ownership, TLS, delegated DNS, public-Internet
reachability, and installed HTTPS proof remain separate gates.

Tenant custom-hostname checkpoint (2026-07-29, Loops 3.5b–3.7): the repository now
admits exact service/environment custom hostname claims through a sibling
nested API, CLI, and expandable Networking panel. Creation returns a fresh TXT
challenge and leaves the route private. An explicit `tls=true` request opts into
the worker-owned ACME lifecycle; without ready certificate evidence the route
remains fail-closed. Verification performs a bounded
resolver observation and, only for the current domain/version/challenge, marks
ownership verified, enables the route, binds open deployments, and refreshes
ready backends. Recovery-created custom rows are not adopted into this product
surface. The UI distinguishes HTTP-only versus HTTPS-requested, ownership
evidence, trusted ingress, and public reachability separately. The current
follow-on projection adds explicit
routing-DNS guidance: subdomains receive the configured apps-domain CNAME
target, while apex names require operator-selected A/ALIAS/ANAME handling.
Midnight still does not mutate or verify provider DNS. Railway’s routing
DNS/CNAME/ALIAS setup, wildcard ownership, DNS provider automation, and
off-host reachability remain separate follow-on loops; ACME issuance is now
wired only through explicit worker configuration and remains unproven on a
clean public VM.

## Remaining Release Gates

### 1. Prove the default installation on a clean VM

Run the VM harness against a fresh supported Debian/Ubuntu machine, install the
package, run doctor, deploy through the packaged API/build/deploy workers, and
survive API, worker, Docker, Traefik, and VM restarts. This is an external
evidence requirement, not a claim that the corresponding repository code is
absent.

### 2. Prove the full public data plane

Supply `MIDNIGHT_PARITY_DATAPLANE_RUNNER` and exercise one fresh
PostgreSQL/registry/BuildKit/Traefik/API/worker stack twice. The run must cover
source intake through generated HTTPS, private GitHub checkout, canonical
history, variables, pre-deploy, volumes, replicas, and rollback without
reusing developer state.

### 3. Prove fault convergence

Supply `MIDNIGHT_PARITY_FAULT_RUNNER` and inject process death at every
documented activation boundary. Canonical deployment state, durable desired
routes, observed Traefik state, and running containers must converge without a
false success, split active pointer, or leaked candidate.

### 4. Prove installed observability and delivery

Supply `MIDNIGHT_PARITY_OBSERVABILITY_RUNNER` for real installed build,
runtime, and HTTP origins plus webhook/SMTP delivery. The built-in collector
tests remain valid executed evidence, but they do not replace this
whole-system run.

### 5. Prove public DNS, ACME, and recovery

Use delegated DNS and the ACME staging-to-production gate to reach a real HTTPS
application, then run the installed off-host restore extension. Verify
database truth, encrypted configuration, volumes, registry digests,
certificate state, and application reachability after source destruction.

## Remaining Product Gaps

- Continue extending the actor-, tenant-, route-, and generation-scoped
  idempotency plus atomic authorization boundary beyond admitted project,
  service, immutable-image deployment creation, content-addressed local
  source upload, deployment actions, operation cancellation, and canonical
  GitHub source linking. Other product routes, variables, domains, volumes,
  templates, GraphQL, and MCP remain gated; operation failure marking remains
  recovery-only.
  Canonical
  deployment and exact
  operation progress reads are admitted only through current tenant
  authority; they do not imply the remaining mutation paths.
- Implement PR environments, staged changes, configuration synchronization,
  and a canonical multi-resource changeset API.
- Add managed database operations only after engine-specific backup, restore,
  upgrade, replica/failover, and off-host recovery gates; the current official
  templates remain explicitly self-managed and single-host.
- Build a real multi-host scheduler, distributed private network, listener
  failover, multi-region placement, and edge routing before making those
  claims.
- Enforce build network/filesystem isolation rather than only validating a
  plan.
- Add authoritative DNS/firewall automation or an explicit operator
  integration for TCP endpoints; preserve original client addresses before
  claiming source-IP enforcement.

## Repeatable Loops

```bash
# High-risk Go packages and modern UI; repeat to catch state/order sensitivity.
MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast

# Production build-worker, Node/Python/Go Railpack prepares, explicit
# Dockerfile, pinned BuildKit gateway, real registry digest comparison, and
# emitted-app health/root probes.
scripts/railway-parity-loop.sh toolchain

# Browser contracts plus a mobile Grid/Map/inspector journey against a
# freshly seeded real memory-mode API.
scripts/railway-parity-loop.sh ui

# Formatting, repository lint, all Go tests/vet/build, both dashboards,
# production dependency audits, and memory-mode API/CLI acceptance.
scripts/railway-parity-loop.sh local

# Docker-backed PostgreSQL stores and API boot/rebuild tests.
scripts/railway-parity-loop.sh integration

# Local plus integration.
scripts/railway-parity-loop.sh all

# Destructive PostgreSQL/filesystem platform recovery with retained evidence.
scripts/railway-parity-loop.sh restore

# Built-in contracts plus live Docker network/TCP policy proof.
scripts/railway-parity-loop.sh security

# Installed-environment checks. The operator must provide a real instance.
MIDNIGHT_API_BASE=https://midnight.example \
MIDNIGHT_API_TOKEN=... \
scripts/railway-parity-loop.sh live
```

The `dataplane`, `fault`, `observability`, and `vm` modes intentionally exit
with prerequisite status 78 unless their named external runner is supplied.
They must not silently substitute unit tests or memory-mode fixtures.

## Verification and Limits

Green in this pass:

- `local`, including all Go tests/vet/builds, both dashboard workspaces, audits,
  and memory-mode acceptance.
- `integration`, including real PostgreSQL stores and real Docker/PostgreSQL
  TCP publication, redeploy, withdrawal, and network-detachment evidence.
- `toolchain`, including real Railpack/BuildKit/registry source builds and
  emitted-app probes.
- `security`, including the live Docker network/TCP policy proof.
- `ui`, including the production build and seeded real-API desktop/mobile
  Chromium journey without request interception.
- `restore`, including destructive PostgreSQL/platform-filesystem recovery and
  retained evidence under `.midnight/parity-evidence/restore/`.
- Real Docker volume persistence, archive, replacement, and recovery tests.
- Real PostgreSQL, Docker log/stats, and Traefik access-log collector tests.
- Real official PostgreSQL template write/container-replacement/read
  persistence proof.

Still external or installation-limited:

- `MIDNIGHT_PARITY_DATAPLANE_RUNNER`: fresh complete data plane and public
  HTTPS.
- `MIDNIGHT_PARITY_FAULT_RUNNER`: real process-death convergence matrix.
- `MIDNIGHT_PARITY_OBSERVABILITY_RUNNER`: installed real-origin collection and
  notification delivery.
- `MIDNIGHT_PARITY_VM_RUNNER`: clean package install, restart, and reinstall.
- `MIDNIGHT_API_BASE` plus `MIDNIGHT_API_TOKEN`: installed-instance live
  acceptance.
- Optional `MIDNIGHT_PARITY_RESTORE_RUNNER`: installed off-host recovery beyond
  the built-in destructive PostgreSQL/filesystem proof.
- Delegated public DNS, ACME issuance/renewal, firewall/NAT, and internet TCP
  reachability require operator-controlled external infrastructure.

External runners must follow the clean-source, nonce, assertion, artifact,
hash, redaction, cleanup, and retention contract in
[`railway-parity-runner-evidence.md`](../railway-parity-runner-evidence.md).

The four external runner modes and `live` return prerequisite status 78 when
their runner or installed-instance credentials are absent. That is expected
fail-closed gating, not a failed built-in verification.
