# API Contracts

The selected architecture, delivery sequence, and stable-v1 blockers are in
[target-design.md](target-design.md).

MVP API contracts live under `/api/v1alpha` and are published from `api/openapi/v1alpha/midnight.yaml`. The OpenAPI file is embedded by the server and served at `GET /api/v1alpha/openapi.yaml`.

Errors are RFC 9457 `application/problem+json` documents. During v1alpha they
also include the former nested `error` object as a compatibility extension;
stable v1 removes that duplicate envelope. Authentication uses distinct
operator, human, project-automation, and runtime-agent credential classes.

Schema evidence is intentionally file- and memory-backed. It validates the ordered migration manifest, records migration SQL source-file metadata, scans the supported SQL subset, and reports ready/needs-review/blocked status without opening a database connection.

The migration plan is a dry-run plan only. It compares injected applied versions with manifest order, reports pending migrations and blocked gaps, and performs no DB calls, SQL execution, or file writes.

API/data readiness combines release evidence, the evidence index, embedded OpenAPI availability, and operation-store list health. The demo route is not a live database readiness probe; when the router is booted through `NewPostgresDataStack`, the same check exercises the Postgres-backed operation store.

`POST /api/v1alpha/_demo/long` is the async-operation envelope demo. The default test router stores operations and idempotency reservations in memory, while the Postgres data stack persists the operation row, operation events, and idempotency-key replay state. The route requires a syntactically valid `Idempotency-Key` and returns a queued operation for `/operations/{id}` and `/operations/{id}/events`.

The router exposes `GET /api/v1alpha/_demo/health-target` as a deterministic in-memory target-health demo for tests only. The pure review helper classifies injected target state as `healthy`, `down`, or `stale` with reason codes such as `TARGET_UP`, `TARGET_DOWN`, `SCRAPE_MISSING`, and `SCRAPE_STALE`; it performs no DB or network calls.

The router exposes `GET /api/v1alpha/_demo/schema-evidence` as deterministic in-memory schema evidence for tests only. The underlying helper loads the migration manifest, validates sequence/order and optional checksum placeholders, summarizes injected applied versus pending versions as `ready`, `needs-review`, or `blocked`, scans the current supported SQL subset for table/object names plus invalid or unsupported statement counts without a DB, and includes a deterministic migration SQL source-file manifest with filename, inferred `up`/`down` kind, byte count, and SHA-256.

The router exposes `GET /api/v1alpha/_demo/migration-plan` as a deterministic in-memory migration execution plan for tests only. The underlying helper derives ordered pending migrations and blocked gaps from the manifest/schema evidence plus injected applied versions, returning `ready`, `needs-review`, or `blocked` without DB calls or file writes.

The router exposes `GET /api/v1alpha/_demo/release-evidence` as a deterministic in-memory release evidence packet for tests only. The packet combines schema evidence and migration plan, rolls up status as `ready`, `needs-review`, or `blocked`, and includes issue codes such as `MIGRATION_PENDING`, `MIGRATION_GAP`, `SCHEMA_SQL_UNSUPPORTED`, `SCHEMA_SQL_INVALID`, and `MIGRATION_CHECKSUM_PLACEHOLDER`. The JSON file helper only accepts explicit `.json` paths and decodes with unknown-field and trailing-content rejection.

The router exposes `GET /api/v1alpha/_demo/evidence-index` as a deterministic in-memory API/schema evidence index for tests only. The helper references schema evidence, migration plan, release evidence, and `api/openapi/v1alpha/midnight.yaml`, validates relative-safe slash paths, rolls up status as `ready`, `needs-review`, or `blocked`, renders stable JSON, and reads/writes strict single-document `.json` files without DB calls.

The router exposes `GET /api/v1alpha/_demo/api-data-readiness` as a deterministic API/data release readiness summary for tests only. The helper combines release evidence, evidence index, embedded OpenAPI availability, and operation-store list health into `ready`, `needs-review`, or `blocked`, with top-level issue codes such as `RELEASE_EVIDENCE_NOT_READY`, `EVIDENCE_INDEX_NOT_READY`, `OPENAPI_UNAVAILABLE`, `OPENAPI_INVALID`, and `OPERATION_STORE_UNHEALTHY`. It only calls the operation-store seam supplied to the router.

`GET /api/v1alpha/operations/{id}/events` streams the operation event log as server-sent events in deterministic sequence order. Optional `after=<seq>` returns only events with a greater sequence number, and each stream ends with an `event: cursor` payload containing `next_sequence` for the next poll. Malformed `after` values return the stable `CURSOR_INVALID` envelope; unknown operations return `OPERATION_NOT_FOUND`.

`GET /api/v1alpha/operations/{id}/logs` is a truthful log surface for deploy tooling. It verifies the operation exists, reports the backed operation-event stream as `deploy_logs`, and returns build, runtime, HTTP, and platform log surfaces from the configured structured log store when available. Unbacked or failed surfaces carry `LOG_STORE_UNAVAILABLE` plus a recovery hint; empty backed surfaces mean no matching real entries were found. It does not synthesize app logs from operation events. Supported query parameters are `limit`, `q`, `surface`, and `cursor`. Resume cursors are encrypted, authenticated, and bound to the operation, surface, and text query; local filenames and offsets are not exposed. `surface` is required with cursor resume.

`GET /api/v1alpha/operations` returns a cursor-paginated operation list with optional `status` and `kind` filters plus `RateLimit-*` response headers. Cursor tokens are signed with the router cursor codec; production boot requires an injected secret.

`POST /api/v1alpha/services/{service_id}/deployments` accepts deploy intent, creates both a server-minted canonical deployment row and a queued operation, then enqueues one build or deploy job. `deployment_id`, `instance_id`, and `previous_active` values supplied by a caller are compatibility input only and are replaced with server-derived values before enqueue. The queue payload uses schema `midnight.dev/deploy-job/v1alpha1`, always carries `operation_id`, `service_id`, `project_id`, and `environment_id` for traceability, preserves the normalized request, and includes candidate schedule intent, optional public-route intent, and the server-resolved prior active deployment. If the target service is missing, the request returns a terminal idempotent `RESOURCE_NOT_FOUND` response without creating an operation, deployment, or job.

The core read model also includes `GET /api/v1alpha/environments`, `GET /api/v1alpha/environments/{id}`, and the batched `GET /api/v1alpha/environments/{environment_id}/resources`. The batched response prefers canonical deployment rows, labels legacy operation-derived evidence, and omits endpoint fields until durable publication evidence exists.

## Environment metadata update contract

`PATCH /api/v1alpha/environments/{environment_id}` accepts either the recovery
operator credential or a durable human session. The HumanSession body is one
strict non-null JSON object no larger than 16 KiB containing positive
`expected_version` and at least one explicitly present `slug`, `name`, or
`tier`. Unknown, null, project, protection, duplication, and copy properties
are rejected. Exactly one `Idempotency-Key` is required for the HumanSession
shape; the recovery operator retains its legacy wider body without that
header.

The PostgreSQL human path resolves the immutable
environment/project/workspace lineage, revalidates the current session,
`environment.write`, and restricted-environment overlay before replay, then
compare-and-swaps `resource_version`. Metadata, the exact `200` or terminal
version/slug-conflict response, idempotency, and audit evidence commit
atomically. Missing, cross-tenant, and restricted non-admin targets are
concealed as 404. The recovery operator retains its legacy wider PATCH body;
that compatibility does not widen human authority.

`midnight environment update` requires the active durable human session and
rejects recovery or external bearer credentials before HTTP. It sends only
explicitly selected metadata plus the expected version. If no retry key is
supplied, it derives a deterministic key from the exact environment ID and
canonical body. It verifies the returned target, requested values, and exactly
incremented version before reporting success. PATCH changes control-plane
metadata only; it does not change protection or restriction, delete, duplicate,
copy configuration, deploy, restart, or apply runtime state. Project
automation, GraphQL, and MCP are not admitted.

## Service/environment configuration contract

`GET /api/v1alpha/services/{service_id}/environments/{environment_id}/config`
accepts either the recovery operator credential or a durable human session. A
human read requires current `service.read` on the exact canonical project and
checks authority before returning a persisted row or the synthetic
`version: 0`, `explicit: false` defaults. Missing, cross-tenant, wrong-lineage,
and restricted environments are concealed from non-admin humans as 404.

`PUT` on the same route requires current `service.write`, an exact
`expected_version` compare-and-swap, exactly one `Idempotency-Key`, and one
strict non-null JSON object no larger than 32 KiB. Repeated/coalesced keys,
unknown properties, trailing JSON, and oversized bodies fail before product
state is accepted. The human PostgreSQL path commits current authorization,
configuration, the exact success or terminal version-conflict response,
idempotency state, and audit evidence atomically. Replay reauthorizes before
returning stored bytes. Saving config changes only desired input for a later
immutable deployment snapshot; it does not deploy, restart, or mutate a running
container. Project automation credentials are not admitted.

`midnight service config get|put` uses the same route and the active durable
human session by default. `put` accepts an explicit stable retry key and, when
omitted, derives a deterministic key from the exact service, environment, and
canonical request body so an ambiguous retry can recover the original response.

## Environment changeset Apply & Deploy

`POST /api/v1alpha/environments/{environment_id}/changeset/apply-and-deploy`
is the primary reviewed configuration transition. It accepts the exact positive
`expected_revision` in a strict JSON body and requires a matching `If-Match`,
one `Idempotency-Key`, and a current human session. The PostgreSQL coordinator
reauthorizes every resource kind, validates exact target versions, atomically
applies desired state, advances the environment configuration revision,
captures fresh immutable service snapshots, and records each affected
service's deployment intake.

The browser does not choose deployment behavior. Validation and the accepted
response report the server-derived `deployment_action`: `rebuild` for a real
normalized build-input change with pinnable source, `redeploy` for immutable
image replay under new runtime configuration, or `none` when no rollout is
required. A successful response is `202` and includes `applied_revision`, the
redaction-safe changeset and validation, `operation_group_id`, and the complete
initial operation group. `Location` points to the group resource and
`X-Midnight-Delivery-State` is `accepted` or `partial`.

`GET /api/v1alpha/environments/{environment_id}/operation-groups/{group_id}`
reauthorizes the tenant scope and derives aggregate `accepted`, `partial`,
`running`, `succeeded`, or `failed` state from canonical operation and
deployment rows. Each item identifies the service, resource kinds, required
action, intake result, and any durable operation/deployment outcome. A partial
response means configuration committed but at least one safe deployment intake
could not be created; the server does not conceal or roll back the applied
revision.

`POST .../changeset/apply` remains the explicit apply-only transition and does
not create deployment work. The generated Go client exposes Apply & Deploy and
operation-group reads. The CLI currently exposes changeset review, validate,
apply-only, and discard; direct wrappers for the combined transition and group
read are tracked as `MID-107`.

## Tenant variable contract

Durable human sessions list/create at
`/api/v1alpha/services/{service_id}/environments/{environment_id}/variables`
and get/update/delete an exact opaque variable ID beneath that collection.
These five routes do not accept recovery-operator or project-automation
credentials. The unchanged global `/api/v1alpha/variables*` family remains the
installation-wide operator-recovery compatibility surface.

Human reads require current `variables.read`; create, rotate, and tombstone
require current `variables.write`. The server derives project and workspace
ownership from the exact service/environment lineage and applies restricted
environment concealment. Human request schemas are strict and bounded to
64 KiB. Create accepts only a portable `[A-Za-z_][A-Za-z0-9_]*` name up to
255 bytes, `scope`, and write-only `value`; update accepts positive int64
`expected_version`, optional `scope`, and write-only `value`; delete accepts
only positive int64 `expected_version`. Metadata and item operations retain
compatibility with legacy recovery-created names. All mutations require
exactly one physical `Idempotency-Key`. Repeated/coalesced headers,
unknown or duplicate fields, trailing JSON, and null documents fail before
state is accepted.

The PostgreSQL coordinator revalidates the session and exact authority,
encrypts values before persistence, compare-and-swaps versions, and atomically
commits active state/history, the exact success or terminal conflict response,
idempotency, and audit evidence. Replay reauthorizes before returning stored
bytes. Responses use strict metadata-only schemas: plaintext, ciphertext,
wrapped keys, and tombstones are never returned. Create returns `Location` and
ETag; item GET and update return ETag; mutation replay reports
`Idempotent-Replayed: true`. Saving, rotating, or deleting desired variables
does not deploy, restart, or mutate a running workload. Human shared-variable
management remains a separate gap. Environment-scoped variables can also be
staged with runtime, generated networking, and volume intent through the
durable changeset API; all items revalidate their own capability and exact
resource version before one serializable apply.

## Tenant custom-hostname contract

The tenant-safe custom-host surface is nested beneath the exact service and
environment: `GET|POST /services/{service_id}/environments/{environment_id}/custom-domains`,
`GET|DELETE .../{domain_id}`, and `POST .../{domain_id}/verify`. A human session
with `domains.read` can inspect the redaction-safe projection; `domains.write`
is required for mutations. The create body contains `hostname` and an optional
`tls` request; Midnight normalizes DNS input, fixes `/` and the service target
port, and keeps route publication fail-closed until TLS evidence is ready. It
returns the exact server-generated TXT record name and value.
The response also includes `routing_dns`: subdomains receive a CNAME target
under the configured `MIDNIGHT_APPS_DOMAIN`, while apex names explicitly
require an operator-selected A/ALIAS/ANAME strategy. This is guidance only;
Midnight does not mutate or verify a DNS provider in this lane. The route
remains private until `verify` performs a bounded exact-TXT observation; a TLS
request may then be public intent while remaining blocked from publication
until certificate evidence is ready. Verification proves ownership evidence
only; it does not prove routing DNS, Traefik/public reachability, HTTPS, or a
certificate. Delete uses
the positive `expected_version` CAS and retired product identities prevent ABA
replays. The flat `/domains` family remains operator-recovery compatibility and
is never used as a tenant fallback. Wildcards, ports, paths, and hostnames
beneath the configured Midnight apps domain remain outside this slice. TLS
intent is opt-in; the worker-owned ACME loop and operator Traefik/DNS proof are
required before claiming HTTPS.

## Tenant generated-domain contract

Durable human sessions list/create at
`/api/v1alpha/services/{service_id}/environments/{environment_id}/domains`
and get/delete one exact opaque domain ID beneath that collection. These
nested routes accept only HumanSession credentials. The unchanged flat
`/api/v1alpha/domains*` family remains installation-recovery compatibility.

Reads require current `domains.read`; create/delete require current
`domains.write`. Canonical service/environment/project/workspace lineage and
the restricted-environment overlay are revalidated before replay. Create
accepts one strict empty JSON object and exactly one `Idempotency-Key`; the
server derives the generated hostname, root path, current immutable target
port (or server default), public HTTP intent, route group, and disabled TLS.
Delete accepts only positive int64 `expected_version` plus one
`Idempotency-Key`. The PostgreSQL coordinator atomically commits domain, route,
service/deployment bindings, exact response, idempotency, and audit evidence.
Create returns `Location`; replay reports `Idempotent-Replayed: true`. The body
`version` is desired-state compare-and-swap input for delete, not a strong
validator for a representation whose route-group evidence can change
independently.

The generated route binds exact current and future open deployments. A
successful create may report pending or blocked backend state; it is not
called reachable until current backend/controller/publication/Traefik/
trusted-probe evidence agrees. `route_apply_state` is environment route-group
state projected onto the domain, so a sibling route can block the group.
`midnight domain list|add|get|delete` uses this
nested path with the active human session and concrete service/environment
context. Independent create/delete invocations generate fresh secure
idempotency keys unless `--idempotency-key` is supplied for an ambiguous retry.
Custom hostname input, TLS, DNS mutation, and public-Internet reachability are
not part of this contract.

Built-in template catalog reads are available at `GET /api/v1alpha/templates`
and `GET /api/v1alpha/templates/{slug}`. The official PostgreSQL, Redis
durable, MySQL, and MongoDB single-node runtime path is
`POST /api/v1alpha/templates/{slug}/instances`; it atomically records service,
encrypted variables, volume, deployment, operation, and outbox truth.
`GET /api/v1alpha/templates/{slug}/instances/{run_id}` exposes metadata-only
connectivity, restart-persistence, and probe-cleanup evidence. Other catalog
templates are rejected with `422 RESOURCE_VALIDATION_FAILED`; only the
official runtime-backed catalog entries are accepted by this operation.

Persistent-volume control-plane CRUD is exposed at `/api/v1alpha/volumes` and `/api/v1alpha/volumes/{id}` when a volume store is configured. These records and mount intents do not alone prove host-side realization or attachment.

## GitHub source-link contract

The normal GitHub product workflow is human-session and tenant scoped. `GET /api/v1alpha/projects/{project_id}/github/repositories` lists only durable repository grants assigned to the project's workspace. `GET /api/v1alpha/projects/{project_id}/github/repositories/{repository_id}/branches` performs a live repository-scoped GitHub lookup after project authorization, then revalidates the session and exact grant. Neither route enumerates the GitHub App's global installation catalog.

`GET /api/v1alpha/services/{service_id}/github` reads the canonical relational source binding. `PUT` on the same route requires exactly one `Idempotency-Key` and a strict body containing only `installation_id`, `repository_id`, and `branch`. The server verifies the exact repository and branch, then atomically reauthorizes, revalidates the workspace grant, persists the binding, completes idempotency, and appends audit evidence. `DELETE` also requires exactly one `Idempotency-Key` and an empty body. Both mutations require current `service.write` and `deployment.trigger.git` capability. A returned `binding_state: "bound"` means the source link was saved; it does not mean a deployment was queued, built, or promoted. The production product route has no `POST` alias and does not read or write legacy `service.spec.github` data.

The global compatibility discovery routes, `GET /api/v1alpha/github/installations` and `GET /api/v1alpha/github/installations/{installation_id}/repos`, remain operator-only setup and diagnostic surfaces. They make live GitHub requests and are not accepted with a human session. Operator recovery uses `GET`, `PUT`, and `DELETE /api/v1alpha/github/installations/{installation_id}/workspace-grant`. `PUT` accepts a workspace plus an exact non-empty repository-ID set and persists only metadata observed by the server from GitHub. Replacement and revocation atomically remove omitted grants and their dependent service links with idempotency and audit evidence. These live verification routes return `503 CAPABILITY_UNAVAILABLE` when the GitHub App is not configured; the routes still exist, but the required adapter is unavailable on that server.

`GET /api/v1alpha/audit/events` returns a read-only audit event page with `prev_hash`, `row_hash`, and audit row hashes from the configured audit chain. A fresh local router returns an empty page rather than seeded demo rows. The dashboard feed shape accepts `limit`.

`GET /api/v1alpha/projects/{project_id}/audit/events` is the tenant-scoped product audit browser. It requires `project.read` for human sessions, conceals missing and cross-tenant projects as `404`, filters by `project_id` in storage before applying its latest-first limit, and returns a signed cursor that cannot be replayed into another project. Its response intentionally omits `before`, `after`, source IP, user agent, and other installation-level fields. The dashboard pages this route independently from the installation-wide recovery summary; failure of the scoped route never causes global audit rows to be substituted as project activity.

`GET /api/v1alpha/audit/export` emits matching audit rows as `application/x-ndjson` followed by a final record with `record_kind: "manifest"` containing row count, checkpoint count, genesis hash, canonical form version, and the SHA-256 of the row portion of the stream. This compliance evidence export does not write files.

`GET /api/v1alpha/audit/log-drains` returns configured SIEM/log-drain integrations when a real drain store is wired. The MVP router returns an empty list and does not perform network delivery.

`internal/audit.PlanDrainDelivery` is the SIEM streaming planning foundation. It computes the next deliverable audit-row batch after a drain cursor, applies a secret-shape payload gate, returns retry/dead-letter policy metadata, and reports the cursor that would be advanced after successful delivery. It does not perform HTTP/syslog delivery or mutate drain cursors.

`GET /api/v1alpha/audit/evidence` returns a compliance summary combining audit row count, first/last row hashes, and SIEM/log-drain readiness. Empty chains report `empty` status rather than demo-ready data.

`GET /api/v1alpha/observability/logs` queries the durable, pre-persistence-redacted build, runtime, HTTP, and platform record store. `collector_health` explicitly reports current, degraded, missing, and stale Docker/Traefik heartbeats without exposing collector IDs or raw driver errors. The SSE companion at `/api/v1alpha/observability/logs/stream` emits `collector-health` frames on connect and during the live tail; `log` frame IDs remain opaque durable replay cursors. Human-session requests are authorized against the requested project and cross-project misses are concealed as `404`. The dashboard deployment terminal reads `/operations/{operation_id}/logs`; operation lifecycle events drive its phase stepper but are never substituted for log records.

`GET /api/v1alpha/backup/restore/status` returns typed recovery availability with restore-execution and off-host destination reasons. `GET /api/v1alpha/backup/runs` lists legacy or experimental evidence manifests; these are not restore points and do not prove any application data was copied. `POST /api/v1alpha/backup/runs` fails closed with `NOT_IMPLEMENTED` by default. `MIDNIGHT_EXPERIMENTAL=1` exposes manifest creation for development evidence only. `GET /api/v1alpha/backup/restore/plans` and `POST /api/v1alpha/backup/restore/plans` list and create non-destructive offline restore plans in the same configured destination.

Normal-product local-dev dashboard endpoints are read-only and backed by router state, the configured audit chain, or product-readiness helpers: `GET /api/v1alpha/dashboard/summary`, `/dashboard/runtime-inventory`, `/dashboard/compatibility-matrix`, `/dashboard/audit-evidence`, and `/dashboard/template-marketplace`. They do not contact live notification providers, template registries, Kubernetes APIs, GitLab, IdPs, SAML/SCIM endpoints, SIEM drains, OCI registries, package mirrors, or runtime services. `/dashboard/notification-preview` is an experimental planner route and is not registered by default.

`POST /api/v1alpha/dashboard/template-deploy` fails closed with
`NOT_IMPLEMENTED`; it is the retired direct-queue shape and does not share the
canonical REST transaction. Feature flags do not expose it.

Contract-only notification preview, enterprise, billing, build-isolation, region-routing, HA, and disconnected-release planner routes are not registered in the normal product API. Set `MIDNIGHT_EXPERIMENTAL=1` to expose the routes marked `x-midnight-experimental: true` in OpenAPI.

OTLP collector production-readiness is modeled in `internal/observability.PlanCollectorPipeline`. The helper is default-off, requires an explicit redaction policy plus a redaction processor before any exporter, and rejects non-HTTPS exporter endpoints. It validates collector shape only; it does not start or contact a collector.

`POST /api/v1alpha/operations/{id}/cancel` requires an empty body and a
singleton `Idempotency-Key`. A human session with
`deployment.trigger.cli` may cancel only an operation joined through one
canonical deployment in its current project/environment authority. Pending
outbox or queue work is withdrawn and the operation/deployment become
`CANCELED` atomically. Leased build/deploy work receives a durable request that
is honored at a worker safe point before deploy handoff or traffic promotion;
an in-progress BuildKit solve is allowed to finish before that fence. Replay
reauthorizes first and returns the exact stored `202` response.

`POST /api/v1alpha/operations/{id}/fail` is a recovery-operator endpoint. It
transitions queued or running operations to `FAILED`, stores optional `code`
and `message` values on the operation failure metadata, appends a failure
event, and returns `202`. It is intentionally not a human-session product
action.

`POST /api/v1alpha/config/diff` and `POST /api/v1alpha/config/apply` accept the minimal `midnight/v1alpha` config-as-code JSON document for projects and services. Diff is read-only; apply requires `Idempotency-Key` and creates missing projects/services only. Updates are surfaced in the plan but are not applied by the current implementation.

## MVP Surface Agreement

`scripts/mvp-acceptance.sh surface-agreement` is the executable parity check for the current MVP lane. It confirms:

- API and CLI agree on project and service IDs through `/projects`, `/services`, `midnight project list --json`, and `midnight service list --json`.
- Dashboard runtime inventory agrees with API state for projects, services, registered hosts, observed routes, and deploy operations.
- API and CLI agree on deploy operation status/log availability through `/operations/{id}`, `/operations/{id}/logs`, `midnight deploy status --json`, and `midnight deploy logs --json`.
- API and dashboard agree on audit evidence through `/audit/events`, `/audit/evidence`, `/audit/export`, and `/dashboard/audit-evidence`.
- Recovery state is typed through `/backup/restore/status`; backup run and restore-plan creation are accepted by the `backup` runner mode when a destination is configured.
- Generated evidence is contract-backed through `_demo/schema-evidence`, `_demo/migration-plan`, `_demo/release-evidence`, `_demo/evidence-index`, and `_demo/api-data-readiness`.

The OpenAPI contract documents the pure planning endpoints for enterprise
identity/audit/controls, billing metering/limits/abuse, build isolation
evidence, region routing, Profile C HA readiness, and disconnected bundles.
The router registers them only when `MIDNIGHT_EXPERIMENTAL=1`; they are absent
from the normal product API. When enabled, the handlers validate request
bodies and build plans only; they do not call IdPs, SAML/SCIM endpoints, SIEM
drains, KMS providers, payment systems, BuildKit, registries, DNS/proxy
infrastructure, databases, or filesystems.
