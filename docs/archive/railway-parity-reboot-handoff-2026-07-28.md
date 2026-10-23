# Railway parity reboot handoff

> **Archive — historical record.** Implementation and status statements below
> describe the recorded checkpoint. Use [the current backlog](../../BACKLOG.md)
> and [deployment readiness](../deployment-readiness.md) for current work and
> evidence boundaries; this report is not a live queue.

- Paused: 2026-07-28
- Resumed: 2026-07-29; source quota, deployment-action,
  operation-cancel, generated-route, canonical GitHub source-link, durable
  webhook, ordinary human environment-create, and tenant-safe
  service/environment configuration, ordinary environment-metadata update,
  tenant-safe service-variable, and tenant-safe generated-domain gates below
  are complete
- Repository: `/path/to/midnight`
- Branch: `main`
- Baseline before the durable-webhook checkpoint:
  `068a671 feat(github): admit canonical tenant source links`
- Checkpoint identity: the `main` commit containing this file; after checkout,
  resolve it with `git log -1 --oneline --decorate`
- Working-tree expectation after handoff: clean and equal to `origin/main`

## Pause boundary

The repository was paused at a coherent Loop 2 source-upload checkpoint. The
resumed passes completed the live PostgreSQL/race proof, admitted deployment
actions, safe-point operation cancellation, one generated first-deploy route,
canonical tenant-safe GitHub source linking for human sessions, and a durable
verified-webhook inbox. Loop 3.1 adds ordinary human environment creation,
Loop 3.2 adds exact service/environment config GET/PUT, and Loop 3.3 adds
ordinary name/slug/tier environment update with resource-version CAS. Loop 3.4
adds exact service/environment variable metadata and create/rotate/tombstone
for durable human sessions. Loop 3.5a adds the exact generated HTTP domain
singleton lifecycle plus current/future deployment binding. Custom domains,
TLS, protected/delete/duplicate environment lifecycle, shared-variable
management, and the broader tenant-safe data lifecycle remain Loop 3. Do not
bypass that authority boundary by widening into workflow or
distributed-runtime work.

Loop 1 is complete: durable human sessions, project automation credentials,
and invitation lifecycle are committed in the four commits preceding this
checkpoint. Loop 2 has working atomic service creation, digest-pinned image
deployment, tenant-scoped deployment/operation reads, and now the
human-session local-source, deployment-action, and operation-cancellation
slices plus one optional generated route on an immutable-image deployment.
Loop 2's repository-owned human authority and intake path is complete.
PostgreSQL webhook delivery is at least once, not exactly once; the live
private-repository and installed process-death matrices remain external
evidence. Operation failure marking stays recovery-only.

Loop 3.1 admits ordinary unprotected human environment creation.
`POST /environments` requires current `environment.write` on the exact project
and a singleton idempotency key, then atomically commits the environment,
exact replay response, and audit evidence. Missing and cross-tenant projects
are concealed. Loop 3.1 itself did not admit item mutations.

Loop 3.2 admits human service/environment configuration without widening the
rest of the data plane. GET requires current `service.read`; PUT requires
current `service.write`, singleton idempotency, strict bounded JSON, and
exact-version CAS. Both enforce canonical lineage and conceal restricted
environments from non-admin humans. The human PUT commits configuration, the
exact response, idempotency, and audit atomically. It does not deploy or restart
the service.

Loop 3.3 admits only ordinary environment metadata PATCH for durable human
sessions. The strict 16-KiB request requires positive `expected_version`, a
singleton idempotency key, and at least one explicit `slug`, `name`, or `tier`.
The coordinator revalidates canonical lineage, current `environment.write`,
and restricted-environment policy before replay, compare-and-swaps
`resource_version`, and atomically commits metadata, the exact success or
terminal conflict response, idempotency, and audit. The recovery operator
retains its legacy wider PATCH compatibility. Human protected-state or
restriction changes, delete, duplicate, copy, deployment, restart, GraphQL,
MCP, and project automation remain closed.

Loop 3.4 admits only the exact nested service/environment variable collection
and item lifecycle to durable human sessions. Reads require current
`variables.read`; create/rotate/tombstone require current `variables.write`,
singleton idempotency, strict 64-KiB write-only bodies, portable 255-byte
create names with legacy metadata/item compatibility, and positive-version CAS
where applicable. Canonical lineage and restricted-environment policy are
revalidated before replay. The coordinator atomically commits encrypted active
state/history, the exact success or terminal conflict response, idempotency,
and audit evidence. The global `/variables*` compatibility surface remains
recovery-operator-only, and project automation remains denied. Saving desired
variables does not deploy or restart the service.

Loop 3.5a admits only the exact nested generated HTTP domain singleton
lifecycle to durable human sessions. Reads require current `domains.read`;
create/delete require current `domains.write`, singleton idempotency, and
positive-version delete CAS. The server derives hostname, root path, current
immutable target port (or default), public HTTP intent, controller group, and
disabled TLS. Migration 0052 adds service-managed generated-domain identity and
exact multi-route deployment bindings, backfills compatible legacy routes plus
open deployments, and rejects ambiguous generated singletons. Existing and
future open deployments inherit the exact route. Strict group CAS, late
binding refresh, bounded ownership-aware compensation, consistent advisory
lock order, and a canonical evidence fence keep blocked or unverified routes
from advancing the active pointer. Custom host ownership, TLS, delegated DNS,
and public-Internet proof remain closed.

## What this checkpoint implements

The exact human
`POST /api/v1alpha/services/{service_id}/up` path now:

1. requires singleton human authority, idempotency/environment headers, a
   supported gzip media type, an exact positive `Content-Length`, and a
   canonical digest of the exact compressed bytes;
2. revalidates current tenant authority and durably reserves logical bytes
   before reading the request body;
3. enforces 50 MiB compressed, 10,000-entry, and 512 MiB expanded limits while
   rejecting unsafe tar entries;
4. validates and hashes the entire supplied body even for an idempotent replay;
5. publishes an immutable content-addressed blob under the quota-global,
   workspace, and artifact-lifecycle lock order;
6. atomically records canonical operation/deployment/event/outbox/audit/
   idempotency state, exact artifact lineage, and the workspace/digest charge;
   and
7. lets the build worker verify and materialize a unique checkout, then release
   ownership only after durable terminal queue truth.

Migration 0045 owns exact operation/deployment/artifact lineage. Migration
0046 owns replica-safe upload reservations and a cascade-independent
workspace/digest charge ledger. The default policy is:

| Boundary | Default |
|---|---:|
| Retained logical bytes per workspace | 5 GiB |
| Concurrent bytes per workspace | 200 MiB |
| Concurrent uploads per workspace | 4 |
| Concurrent bytes installation-wide | 1 GiB |
| Concurrent uploads installation-wide | 32 |
| Upload lease | 10 minutes |
| Conservative orphan charge | 26 hours |

The build worker runs bounded content-addressed and legacy janitors. A
successful CAS pass must prove both canonical and quarantine locators absent
before proof reconciliation can release an accounting record. Ledger charges
survive operation/deployment cascades. Quota, workspace, lifecycle, and
sorted-workspace lock order is consistent across admission, publication,
collection, and reconciliation.

The CLI builds a deterministic archive, advertises its exact length and digest,
derives an exact-intent idempotency key, refuses redirects, bounds response and
event streams, and displays `Retry-After`. Compose and the packaged
installation share an upload volume and explicit quota defaults between the
API and build worker.

## Honest capability boundary

- A `202` means the build is durably queued. It does not mean the workload is
  healthy, public, or deployed.
- A plain source deployment stays private and does not create a domain.
- The quota covers retained, committed human-session CAS archives and
  concurrent human uploads. It is not a kernel-enforced physical-disk quota or
  a global host-storage watermark.
- Crash-orphaned random staging files are age-cleaned but are not linked to
  reservation-absence proof. Stage and stage-quarantine bytes are therefore
  outside the hard accounting claim.
- Project automation and recovery-operator `up` still use the legacy
  staged-extraction path. They are not covered by the human CAS/quota claim.
- Restrictive quota foreign keys can intentionally delay hard deletion of a
  session, project, service, environment, or workspace until conservative
  accounting is reconciled.
- The human-session quota/ledger slice has executed disposable PostgreSQL and
  real-filesystem evidence. The broader canonical intake group remains
  **Integrated** because legacy upload authorities, full route lifecycle,
  live private-repository delivery, and installed process-death evidence are
  still incomplete.
- Human environment authority covers ordinary unprotected create plus strict
  name/slug/tier metadata update. `is_protected=true` creation and all
  protected-state changes remain recovery-operator-only; delete, duplicate,
  restriction changes, configuration copy, and runtime application are not
  part of the human-session claim.
- Human domain authority covers only the server-derived generated HTTP
  singleton. It does not admit a custom hostname, editable path/port, TLS,
  DNS mutation, or an Internet-reachability claim.

## Verification at pause

The settled tree passed:

```sh
CGO_ENABLED=0 go test ./...

CGO_ENABLED=0 go test \
  ./internal/sourceartifact \
  ./internal/api \
  ./cmd/midnight \
  ./cmd/midnight-server \
  ./cmd/midnight-buildkit \
  ./migrations \
  -count=1

CGO_ENABLED=0 go test ./internal/api ./internal/sourceartifact -count=1
git diff --check
docker compose -f docker-compose.local.yml config --quiet
```

`gofmt -l` over every touched Go path was empty. A host-default
integration-tag attempt crashed during the third-party
`github.com/shoenig/go-m1cpu` CGO initializer before repository test output.
That is not recorded as a passing integration run. Use `CGO_ENABLED=0` after
reboot and retain the exact output.

The broader `fast`, `security`, `integration`, `ui`, `toolchain`, `restore`,
and external installed-system loops were not rerun after this source gate.
They remain required; focused green tests are not a substitute.

## Verification after resume

The completed source-quota, deployment-action, operation-cancellation, and
generated-route gates passed:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration -count=1 -timeout=15m \
  ./internal/sourceartifact \
  ./internal/api \
  ./internal/db/pg \
  ./cmd/midnight-buildkit

CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration -count=5 -timeout=10m \
  ./internal/sourceartifact \
  -run '^TestQuotaConcurrentAdmissionFairnessAndPublicationLockOrder$'

CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration -count=5 -timeout=10m \
  ./internal/api \
  -run '^TestTenantOperationCancelAtomicAuthorizationAndWorkerFence$'

MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh integration
scripts/railway-parity-loop.sh security
scripts/railway-parity-loop.sh ui
scripts/railway-parity-loop.sh toolchain
scripts/railway-parity-loop.sh restore
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go vet ./...
git diff --check
```

All declared steps passed. The security loop included its Docker-backed
same-scope/cross-project network acceptance, and the integration loop included
the cancellation and generated-route transactions, source-artifact,
migration, route, and build-worker packages. The UI loop passed typecheck, all
190 unit/component tests, a
production build, 16 seeded-browser cases with one intentional skip, and the
real memory-mode API browser flow. It also corrected two stale browser
expectations left over from the pre-admission deployment-action boundary.
`gofmt -l` over every touched or untracked Go file was empty. The live
`toolchain` loop built and ran Node, Python, Go, secret-mount, and Dockerfile
samples through pinned Railpack, BuildKit, and a disposable registry. The
`restore` loop destroyed and restored PostgreSQL and the platform filesystem.
Evidence is under
`.midnight/parity-evidence/toolchain/20260729T070557Z-71206` and
`.midnight/parity-evidence/restore/20260729T070750Z-71687`. External
dataplane, fault, observability, clean-VM, and installed-system loops remain
separate gates; these local proofs do not claim them.

## Completed resume gate

Live PostgreSQL and real-filesystem coverage now proves:

1. accept → charge ledger → operation/deployment cascade while accounting
   remains;
2. canonical blob → quarantine → final deletion → last workspace charge
   reconciliation;
3. one digest shared within and across workspaces, quota de-duplication, and
   bounded retry fairness;
4. concurrent workspace/global admission at byte and count boundaries;
5. publication paused against GC/reconciliation in both lock orderings; and
6. reservation renewal, process-crash windows, and rollback restoration.

Integration minimal schemas and migration-lineage expectations now include
schema version 49. The autonomous `integration` loop runs the source-artifact
and build-worker end-to-end packages, and the `security` loop runs the quota
race/proof cases. A future physical staging quota still needs an explicit
work-retention decision; the current logical CAS quota must not be relabeled as
that stronger feature.

## Completed deployment-action gate

The exact empty-body human route now admits redeploy, rollback, restart, stop,
and remove through a separate PostgreSQL coordinator. REST, CLI, and GUI use
the same route without operator or GraphQL fallback. Live PostgreSQL coverage,
repeated five times, proves concurrent same-key replay across sessions,
runtime and image action lineage, demoted-role denial before replay,
cross-service concealment, queued/running runtime conflict fencing, and audit
rollback with zero partial state. The repository `security` loop owns this
transaction proof.

The completed action slice passed:

```sh
CGO_ENABLED=0 go test -tags=integration -count=5 -timeout=10m \
  ./internal/api \
  -run '^TestTenantImageDeployIntakeAtomicAuthorizationAndReplay$'

MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh integration
scripts/railway-parity-loop.sh security
```

The CLI regression proof additionally asserts that action requests have no
body or JSON content type, matching the exact human REST contract. Action audit
proof distinguishes the `service.write` runtime capability from
`deployment.trigger.cli` immutable replay authority.

## Completed operation-cancellation gate

The exact empty-body human `POST /operations/{operation_id}/cancel` route now
uses a separate PostgreSQL coordinator. Migration 0047 adds terminal canceled
outbox state. The accepting transaction revalidates
`deployment.trigger.cli`, scopes replay to actor/project/concrete route,
cancels pending or delivering outboxes and pending jobs, records a durable
request for leased jobs, stores the exact response, and appends audit evidence.
Build and deploy workers converge the canonical deployment and operation at
named safe points before deploy handoff or traffic promotion.

Disposable PostgreSQL evidence proves concurrent same-key replay, viewer and
cross-tenant zero-artifact denial, demoted replay denial, cancellation racing
the outbox dispatcher, leased-worker request fencing, canonical worker
convergence, and complete rollback when the audit append fails. The CLI and
GUI use the same route; project automation remains denied. An active BuildKit
solve is not interrupted and is fenced after solve before handoff. The
recovery-only operation `fail` route remains operator-only.

## Completed generated-route gate

Human immutable-image deployment remains private by default and now accepts
only the optional nested opt-in `{"route":{"public":true}}`. Migration 0048
binds the exact canonical deployment to its server-derived generated domain
and HTTP route. Domain/route intent, the binding, canonical deployment,
idempotent response, outbox work, and audit evidence share the accepting
serializable transaction. Missing `MIDNIGHT_APPS_DOMAIN` fails before durable
identities are minted. CLI and GUI expose a single opt-in and cannot send a
custom host, name, group, path, port, or TLS setting.

Deployment reads batch route evidence by the already-authorized deployment
IDs. They expose a requested hostname and apply state without calling it live;
the HTTP URL appears only when the exact deployment has a ready backend and
the current revision/hash has Traefik acknowledgement plus a successful trusted
Host-header ingress probe. The verifier now probes every published route.
Redeploy/rollback clones only the source deployment's explicit binding and
keeps generated payload hosts empty so the worker cannot reclassify them as
custom.

Focused Go, CLI, GUI, migration-contract, disposable PostgreSQL, and durable
route integration tests passed while developing this gate. The settled tree
also passed the three-repeat `fast` loop and the `integration`, `security`,
`ui`, `toolchain`, and `restore` loops, plus repository-wide Go test and vet.
This historical gate does not claim public DNS, Internet reachability,
TLS/certificate issuance, custom domains, service-managed lifecycle, or
path/port editing. Loop 3.5a and ADR 0040 later supersede only the
service-managed generated-domain and exact multi-route binding gaps.

## Canonical GitHub source-link checkpoint

Migration 0049 and ADR 0038 now:

1. assign each GitHub App installation exclusively to one workspace;
2. grant exact server-verified numeric repository IDs;
3. quarantine and deactivate legacy `service.spec.github` links;
4. provide recovery-operator grant replacement/revocation with idempotency and
   audit evidence;
5. expose only project-workspace repositories and branches to human sessions;
6. require current membership plus `service.write` and
   `deployment.trigger.git` for strict service link mutation;
7. live-verify the exact repository and branch before a serializable
   reauthorization/link/idempotency/audit commit;
8. route signed pushes by exact installation/repository/name/branch/SHA and
   skip untrusted pushes;
9. revalidate grant, link, and link version under the same installation lock
   used by revocation before staged work becomes durable; and
10. mint a repository-scoped installation token in worker memory immediately
    before checkout.

The CLI and real service drawer source panel use these human routes without an
operator fallback or App-global installation browser. Linking does not deploy.
CI-wait configuration remains rejected until its production gate exists.

The settled source-link tree passed:

```sh
MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh integration
scripts/railway-parity-loop.sh security
scripts/railway-parity-loop.sh ui
scripts/railway-parity-loop.sh toolchain
scripts/railway-parity-loop.sh restore
```

The UI gate includes 203 component tests, 16 admitted browser flows, and the
real-API browser flow. The toolchain gate built and executed Node, Python, Go,
Railpack secret-mount, and Dockerfile samples through the pinned production
worker, BuildKit, and registry. The restore gate destroyed and recovered its
disposable PostgreSQL database and platform filesystem. These runs do not
substitute for the still-external installed-system/private-repository proof.

## Durable verified-webhook checkpoint

Migration 0050 and ADR 0039 now replace PostgreSQL reservation-before-dispatch
with:

1. bounded signature verification followed by payload and acceptance-audit
   commit before HTTP `202`;
2. exact `(provider, endpoint, provider delivery)` replay with immutable
   envelope conflict detection;
3. database-receipt temporal source lookup, serialized against installation
   mutation commit order, so a later link cannot join an old delivery;
4. immutable per-service targets containing the exact grant/link versions and
   authority timestamps;
5. renewed delivery and target leases with token/expiry fencing, bounded
   backoff, and explicit dead-letter convergence;
6. retained delivery/target SHA-256 checks before planning, dispatch, or
   redrive;
7. canonical staged-intake idempotency after ambiguous downstream completion;
   and
8. recovery-operator metadata list/detail plus exact-empty-body redrive whose
   inbox, idempotency, and audit state share one serializable transaction.
9. daily bounded terminal payload purge after a 30-day recovery window while
   retaining identity, hashes, outcomes, and target/result metadata; and
10. guarded migration rollback that refuses to erase acknowledged v50 work.

The capability claim is at-least-once delivery. Lease expiry may repeat a
downstream attempt; canonical trigger and staged-intake keys suppress duplicate
work. Operator projections never return signed bodies, target dispatch
payloads, signatures, key identifiers, or lease tokens. Memory mode remains
synchronous and returns an honest `501` for durable recovery. An aged-out dead
letter remains inspectable but is no longer redrivable.

Disposable PostgreSQL coverage exercises admission/replay/envelope conflict,
signature-key rotation, receipt-time authority cutoffs, grant/link version
revalidation, delivery/target lease fencing and renewal, payload corruption,
retry/dead-letter/redrive, idempotent replay, audit rollback, and migration
up/down/re-up. The final regression matrix additionally proves:

1. a real signed HTTP receiver cannot acknowledge or expose its inbox row
   before the payload and acceptance audit commit, and audit failure rolls the
   whole admission back;
2. concurrent identical and conflicting admissions select one immutable
   winner without corrupting replay/conflict counters;
3. receipt waits behind the installation authority lock, then resolves source
   links from the database commit order;
4. duplicate target plans roll back without a partial routing timestamp;
5. an ambiguous staged-intake commit is reclaimed after lease expiry with the
   exact operation, deployment, and outbox identities and no duplicate rows;
6. terminal payload retention removes only aged bodies while preserving
   metadata and keeping active work dispatchable; and
7. migration rollback refuses to drop a populated v50 inbox.

The settled durable-inbox tree passed:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration -count=1 -timeout=10m \
  ./internal/api ./internal/webhook \
  -run 'TestWebhookWorkerRecoversAmbiguousGitHubIntakeCommit|TestPostgresWebhookAcceptanceSerializesAfterGitHubAuthorityCommit|TestPostgresDeliveryStoreLeaseFencingAndSchemaAlignment'

CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration -count=1 \
  ./internal/db/pg -run '^TestPostgresWebhookDurableInbox'

MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh integration
scripts/railway-parity-loop.sh security
scripts/railway-parity-loop.sh ui
scripts/railway-parity-loop.sh toolchain
scripts/railway-parity-loop.sh restore
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go vet ./...
go run ./tools/lint
git diff --check
```

All declared steps passed. The UI gate passed 203 unit/component tests, 16
browser cases with one intentional skip, and the real memory-mode API browser
flow. The production toolchain gate built and ran Node, Python, Go,
secret-mount, and Dockerfile samples. The restore gate destroyed and restored
PostgreSQL and the platform filesystem. Exact local evidence is under
`.midnight/parity-evidence/ui/20260729T093514Z-16875`,
`.midnight/parity-evidence/toolchain/20260729T093527Z-17014`, and
`.midnight/parity-evidence/restore/20260729T093707Z-17638`.

## Ordinary human environment-create checkpoint

Loop 3.1 adds `environment.write` for workspace admins/members and direct
project owners/editors. The exact collection POST is the only newly admitted
human route. It reads a bounded body, revalidates current session and project
authority under the membership locks, and atomically stores idempotency,
environment state, the exact response, and audit evidence. Protected creation
is denied before durable state; item PATCH/DELETE and duplicate remained behind
operator recovery at that checkpoint. Loop 3.3 later supersedes only the
ordinary metadata-PATCH gap.

The focused gate passed:

```sh
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test -tags=integration ./internal/api \
  -run '^TestTenantEnvironmentMutationAuthorizationReplayRollbackAndRetryIdentity$' \
  -count=5

CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 \
  go test ./internal/api ./internal/identity ./cmd/midnight \
  ./tools/contract-tests -count=1

npm --prefix web-next run typecheck
npm --prefix web-next test
go run ./tools/contract-tests

MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh integration
scripts/railway-parity-loop.sh security
scripts/railway-parity-loop.sh ui
scripts/railway-parity-loop.sh toolchain
scripts/railway-parity-loop.sh restore
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go test ./...
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go vet ./...
CGO_ENABLED=0 GOTOOLCHAIN=go1.25.12 go run ./tools/lint
git diff --check
```

The PostgreSQL proof covers exact cross-session replay, viewer and cross-tenant
denial, protected-create zero artifacts, revoked session and removed grant
denial before replay, direct editor admission, terminal slug-conflict replay,
audit rollback, and one pre-minted identity across a compatibility-writer
serialization retry. The GUI waits for an exact project-access response with
`environment.write`, reuses one key for an unchanged retry intent, and exposes
no duplicate or protected control. The CLI accepts an explicit key or
generates one. Shared mutation admission and the environment handler both
reject repeated or proxy-coalesced `Idempotency-Key` fields before a replay
identity can be selected.

All declared local parity steps passed after the gate was settled. The UI loop
passed typecheck, all 210 unit/component tests, a production build, 16 seeded
browser cases with one intentional skip, and the real memory-mode API browser
flow. The production toolchain loop built and ran Node, Python, Go,
Railpack-secret, and explicit-Dockerfile samples. The restore loop destroyed
and restored PostgreSQL and the platform filesystem. `gofmt -l` over every
touched or untracked Go file was empty. Exact local evidence is under
`.midnight/parity-evidence/ui/20260729T100750Z-32052`,
`.midnight/parity-evidence/toolchain/20260729T100759Z-32153`, and
`.midnight/parity-evidence/restore/20260729T100937Z-32524`.

## Tenant-safe service/environment configuration checkpoint

Loop 3.2 admits the exact
`GET` and `PUT /services/{service_id}/environments/{environment_id}/config`
route to durable human sessions while retaining recovery-operator
compatibility. Human GET requires current `service.read` before explicit config
or synthetic defaults are returned. Human PUT requires current `service.write`,
exactly one non-blank `Idempotency-Key`, one strict non-null JSON object bounded
to 32 KiB, and an exact `expected_version` compare-and-swap.

Both methods bind the service and environment to one canonical project.
Missing, cross-tenant, wrong-lineage, and restricted environments are concealed
from non-admin humans as 404; an authenticated exact-scope role denial is 403.
The PostgreSQL mutation transaction revalidates current session and grant before
replay and commits the config row, exact success or terminal version-conflict
response, idempotency state, and audit evidence atomically. Project automation,
GraphQL, and MCP remain denied.

The CLI and GUI use the same REST route with the active human session and no
operator fallback. `midnight service config put` accepts an explicit retry key;
when omitted it derives a deterministic versioned key from the exact service,
environment, and canonical request body. A successful response means desired
configuration was saved for a later immutable deployment snapshot. It is not
evidence of a deployment, restart, runtime apply, or installed-system proof.

The settled PostgreSQL matrix passed three consecutive times and covers
synthetic-default reads, viewer reads and write denial, editor writes,
restricted-member concealment, restricted-admin access, cross-lineage denial,
same-user cross-session exact replay, changed-body rejection and
concrete-target isolation, revocation before replay, concurrent same-key
admission, immutable
deployment snapshots, terminal create-CAS conflict replay, and audit-trigger
rollback with zero config/idempotency/audit artifacts. The full Go test, vet,
repository lint, OpenAPI/CLI contract, and frontend suites passed. The repeated
fast loop passed three times. Integration, security, UI, production toolchain,
and destructive restore loops also passed; the UI gate contains 220
unit/component tests plus 16 browser cases with one intentional skip and the
real memory-mode API flow. Exact local evidence is under
`.midnight/parity-evidence/ui/20260729T103047Z-43044`,
`.midnight/parity-evidence/toolchain/20260729T103103Z-43152`, and
`.midnight/parity-evidence/restore/20260729T103325Z-43564`.

Loop 3.3 now implements tenant-safe ordinary environment metadata update. The
human route is limited to name/slug/tier behind current `environment.write`,
canonical lineage, singleton idempotency, positive resource-version CAS,
serialized terminal conflict replay, and atomic audit. The CLI derives a
deterministic exact-target/body key and validates the returned identity,
metadata, and next version before reporting success. Protected-state and
restriction changes, delete, duplicate, configuration copy, and runtime
application remain closed. Variables, domains, volumes/backups, templates,
logs/metrics, and the remaining lifecycle mutations stay independently gated.
Installed private-repository/process-fault evidence remains Loop 5/7 and must
not be replaced by unit or disposable-database proof.

The settled Loop 3.3 PostgreSQL matrix passed three consecutive times. It
covers member/editor/viewer authority, cross-tenant and restricted-environment
concealment, admin access, immutable project/protection state, exact replay,
changed-body rejection, concrete-target key isolation, revoked sessions,
removed grants, terminal version and slug conflicts, same-key concurrency,
distinct-key CAS, a compatibility-writer unique race, audit rollback, and
direct stale-CAS transaction usability. The full Go test, vet, repository lint,
CLI/OpenAPI contract, and frontend suites passed. The repeated fast loop passed
three times, and the integration, security, UI, production-toolchain, and
destructive-restore loops passed. The UI gate contains 230 unit/component tests
plus 16 passing browser cases, one intentional skip, and the real memory-mode
API browser flow. Exact Loop 3.3 evidence is under
`.midnight/parity-evidence/ui/20260729T111142Z-62619`,
`.midnight/parity-evidence/toolchain/20260729T111222Z-62794`, and
`.midnight/parity-evidence/restore/20260729T111359Z-63161`.

Loop 3.4 now implements tenant-safe service variables across the canonical
nested REST contract, active-session CLI, and service-drawer editor. Migration
0051 derives and validates canonical ownership without rewriting ciphertext.
Human list/get responses are strict metadata only. Create/rotate/tombstone
share current-authority, exact-scope, encryption, immutable history, CAS,
idempotency, terminal replay, and audit transaction boundaries. Unknown,
cross-tenant, wrong-lineage, restricted, and stale targets fail closed.
Deployment snapshots additionally pin the exact service slug/display aliases
used by cross-service references; legacy aliasless snapshots fail closed
instead of consulting mutable aliases. Production build/runtime decrypt must
append an execution-attributed redacted hash-chained audit record before
injection and fails closed when it cannot.
Shared-variable human lifecycle, staged multi-resource changes, automatic
redeploy, GraphQL/MCP admission, and project-automation access remain closed.

The Loop 3.4 contract/documentation checkpoint passed the canonical contract
runner, all Go unit packages, Go vet, all integration-tag package compilation,
focused race suites, repository lint, `git diff --check`, Docker Compose
parsing, and the full frontend suite (250 tests, typecheck, production build).
Live disposable PostgreSQL proof covers the tenant lifecycle, variable
transaction primitives, immutable snapshots including retired-service
isolation, and the exact 0050→0051 ownership repair/down/up path while proving
ciphertext and wrapped-DEK byte identity. The parsed contract asserts all five
nested operations, HumanSession-only security, exact status matrices including
413/503, strict schemas, positive int64 versions, write-only input, redacted
responses, mutation headers, unchanged legacy operation IDs, and denial of
project automation. This is repository proof; it does not replace the
installed-system Loop 7 evidence.

The closeout audit additionally made infrastructure failure reasons constant
at the durable product boundary while retaining wrapped causes for trusted
inspection, excluded soft-deleted-service variables from unrelated new
snapshots, required exact `value_kind` in CLI/GUI response validation, and
attributed each decrypt audit to an exact leased deploy/build or cron attempt.

Loop 3.5a now implements tenant-safe generated HTTP domains across the exact
nested REST contract, active-session CLI, and service Networking panel.
Migration 0052 adds `resource_version`, one generated singleton per
service/environment, exact service-managed domain bindings, and
`(deployment_id, route_id)` deployment intent. The upgrade conservatively
backfills compatible generated routes and exact open deployments while
preserving deployment-local routes; ambiguous legacy generated singletons fail
with actionable scope diagnostics. Human reads and mutations revalidate
current `domains.read`/`domains.write`, canonical lineage, and the
restricted-environment overlay. Create/delete atomically commit product state,
exact replay response, singleton idempotency, and audit evidence; delete uses
positive-version CAS.

Each delete/recreate incarnation has a fresh opaque `dom_*` ID while the
generated hostname and route ID remain stable. Create mints the proposal once
across SERIALIZABLE retries and may adopt a compatible, never-retired
operator-recovery singleton by its actual ID. Human delete records the exact ID
and scope in `retired_product_domain_ids` before cascading live state; product
creation rejects any proposed or recovery-reused retired ID. Migration 0052
down refuses nonempty retirement history so downgrade/re-upgrade cannot erase
the ABA fence. Generated workers use exact deployment bindings as their
authority and cannot reconstruct a domain that its owner deleted from an old
host-empty payload.

The runtime refreshes bindings after scheduling and immediately before route
promotion. Group stage/commit CAS is strict, blocked or unverified siblings
fail, compensation is bounded and ownership-aware, and zero-downtime
target-port changes admit only captured prior backends at historical ports.
Service/environment and route mutations use consistent advisory-lock ordering.
The canonical deployment transaction independently requires exact candidate
intent/backend and current controller/publication/Traefik/trusted-probe
evidence before moving the active pointer. This proves repository and
disposable-PostgreSQL behavior, not public DNS or Internet reachability.

The closeout adversarial pass fixed two additional P1s. Durable Traefik
router/service names are revision-scoped; the verifier recomputes the artifact
hash, requires exact active rule/service/TLS/middleware/backend and health
state, and rejects a conflicting old rule before the trusted ingress probe. A
cross-process environment publication lock now spans group load, publication,
Traefik acknowledgement/probe, and final observation CAS, so a second
reconciler cannot publish from a stale snapshot.
An indeterminate PostgreSQL canonical COMMIT no longer triggers unsafe route
compensation or candidate removal when the write actually committed. Normal
and template workers acquire a dedicated session advisory lock before opening
the read-only Repeatable Read proof snapshot, then require the exact operation,
candidate active pointer, expected prior supersession, candidate-only route
publication, and template terminal evidence. Definite failures still fail.

The same pass restored the flat operator-recovery domain GUI beside the
human-only nested panel, removed strong ETags from the dynamic route-evidence
projection while retaining body `version` for delete CAS, and removed browser
links and public-reachability labels based only on trusted local ingress
evidence. Environment route groups remain strict: an idle, TLS-blocked, or
otherwise unverified sibling can block another service in the same
project/environment. This is an honest availability limitation and a future
route-group partitioning dependency, not public parity.

Final exact-worktree verification on 2026-07-29 passed `go generate`, Compose
parsing, repository lint, the 156-operation OpenAPI contract, all Go unit
packages, Go vet, focused API/router/deploy/database/CLI/worker race tests, the
full integration-tag Docker/PostgreSQL loop, the live security isolation loop,
and three consecutive `fast` loops. The GUI gate passed typecheck, production
build, 273 unit/component tests, 17 Playwright scenarios with one intentional
skip, and the real memory-mode API browser flow. The real toolchain gate built,
pushed by digest, ran, and probed Node, Python, Go, secret-mount, and explicit
Dockerfile samples. The destructive restore gate destroyed and recovered
PostgreSQL plus platform filesystem state. Evidence is retained at
`.midnight/parity-evidence/ui/20260729T144115Z-42765`,
`.midnight/parity-evidence/toolchain/20260729T145728Z-48066`, and
`.midnight/parity-evidence/restore/20260729T145918Z-48468`.

The next repository-owned dependency is Loop 3.5b: custom HTTP hostname
ownership desired state and bounded TXT verification with TLS still disabled.
The existing flat domain routes remain recovery-only. Generated wildcard TLS,
custom TLS, renewal, and installed HTTPS remain separate gates; public proof
requires real DNS/ingress/provider state.

## Resume commands

```sh
cd /path/to/midnight
git status --short --branch
git fetch origin
git log -1 --oneline --decorate
git diff --exit-code origin/main...HEAD

CGO_ENABLED=0 go test \
  ./internal/sourceartifact \
  ./internal/api \
  ./cmd/midnight \
  ./cmd/midnight-server \
  ./cmd/midnight-buildkit \
  ./internal/router \
  ./migrations \
  -count=1

CGO_ENABLED=0 go test -tags=integration -count=1 -timeout=15m \
  ./internal/sourceartifact \
  ./internal/api \
  ./internal/db/pg \
  ./internal/router \
  ./cmd/midnight-buildkit

npm --prefix web-next test -- --run
npm --prefix web-next run typecheck
npm --prefix web-next run build
npm --prefix web-next run test:e2e

MIDNIGHT_PARITY_REPEAT=3 scripts/railway-parity-loop.sh fast
scripts/railway-parity-loop.sh --list
```

Then run repository-owned loops in dependency order:

```text
security → integration → ui → toolchain → restore
        → dataplane/fault/observability/vm/live when their documented
          external runners and installed targets are available
```

External modes require a clean tree and evidence manifests whose commit SHA is
this checkpoint or a later tested commit. Do not replace a missing runner with
unit-test evidence.

## Documentation synchronized in this checkpoint

- OpenAPI documents the human digest/length contract and 411/413/429 behavior.
- ADR 0035 documents migration 0046, default limits, reconciliation, restrictive
  deletion effects, and the physical-storage caveat.
- The Railway parity delivery plan and review grade the human CAS quota as
  Integrated while preserving the live-race and legacy-path gaps.
- OpenAPI, ADRs 0031/0032, the security boundary, CLI guidance, and GUI
  authority copy enumerate the admitted deployment actions. Canonical GitHub
  source linking is separately admitted by ADR 0038; other product routes,
  GraphQL, and MCP remain closed, and failure marking remains recovery-only.
- ADR 0036 and the API, CLI, GUI, security, and error-code guidance define the
  safe-point cancellation contract and distinguish an unavailable worker-safe
  point from queue infrastructure failure.
- ADR 0037 and the OpenAPI, CLI, GUI, review, and delivery-plan guidance define
  the exact generated-route opt-in, deployment binding, and evidence-aware read
  boundary without claiming custom domains, TLS, public DNS, or Internet
  reachability.
- ADR 0040, migration 0052, OpenAPI, CLI, Networking panel, routing/ingress
  contracts, parity plan/review, and this handoff define the tenant-safe
  generated-domain singleton, exact current/future multi-route bindings,
  strict group publication and canonical evidence fence without claiming
  custom ownership, TLS, public DNS, or Internet reachability.
- ADR 0038, migration 0049, the GitHub App guide, OpenAPI, CLI, GUI, and
  delivery-plan guidance define the exact workspace installation, repository
  grant, canonical link, recovery, revocation, and repository-scoped token
  boundary.
- ADR 0039, migration 0050, OpenAPI, the GitHub App guide, error-code
  registry, parity plan/review, and this handoff define persist-before-ack,
  temporal target discovery, fenced at-least-once delivery, metadata-only
  recovery, and atomic redrive without claiming exactly once or a live
  private-repository/process-fault run.
- OpenAPI, CLI guidance, parity plan/review, and this handoff describe the
  Loop 3.1 ordinary environment-create boundary: required idempotency,
  `environment.write`, protected-create denial, exact failure responses, and
  no human item-mutation claim at that checkpoint.
- ADR 0029, OpenAPI, API/CLI guidance, parity plan/review, and this handoff
  describe the Loop 3.2 service/environment config boundary: exact
  `service.read`/`service.write`, restricted-environment concealment, singleton
  idempotency, 32 KiB strict body, exact-version CAS, atomic replay/audit, and
  no deployment or runtime-apply claim.
- ADR 0032, OpenAPI, CLI guidance, parity plan/review, and this handoff describe
  the Loop 3.3 ordinary environment metadata-update boundary: strict 16 KiB
  name/slug/tier PATCH, current `environment.write`, restricted-environment
  concealment, singleton idempotency, resource-version CAS, exact terminal
  replay, and no protection/delete/duplicate/copy/runtime claim.
- CLI and build-pipeline docs list the upload behavior and all seven shared
  quota settings.
- Debian and Compose defaults keep API/build-worker quota policy aligned.
- Error-code guidance distinguishes retained quota exhaustion from temporary
  upload backpressure.

The authoritative dependency and loop backlog remains
`docs/archive/railway-parity-delivery-plan-2026-07-27.md`.
