# Changelog

All notable changes to Midnight are tracked here. The project has not cut a public release yet, so entries remain under `Unreleased` until the first tagged version.

Entries before October 23, 2026, describe private development. The public Git
history starts with a source snapshot on that date, so those entries do not
correspond to individual public commits. See [Public Git History](README.md#public-git-history).

## Unreleased

### Added and fixed (2026-10-27, acceptance gaps)

- Added restart-safe UTC audit checkpoint scheduling, atomic signing under
  PostgreSQL's audit lock, durable private keys, consistent signed exports,
  and explicit public-key trust. Dashboard and CLI evidence now distinguish
  hash validity, signature coverage, missing trust, and unsigned tails.
- Made invitation acceptance reachable by ordinary users and preserved the
  displayed resource version on invitation/token revocation. Expanded real
  PostgreSQL/Chromium journeys to invitations, role changes, scoped automation
  credentials, and administrator recovery/session revocation.
- Selected the configured AI provider and model for automatic diagnosis;
  prevented missing MiniMax keys and SDK environment credentials from
  forwarding another provider's API key or bearer token.
- Wired diagnosis to canonical deployment context, redacted durable logs,
  scoped report persistence, and operation-event attachment. Added a required
  PostgreSQL/provider-adapter canary and persisted attachment replay refusal.
- Added credential-ready product AI/diagnosis, GitHub, and native ACME staging
  acceptance with private configuration, scoped writes, deadlines, honest
  blocked outcomes, and cleanup evidence. External execution still requires
  the documented operator setup and credentials.
- Added dedicated real Railpack toolchain and offline recovery CI lanes.
  Recovery checks destruction/readback, wrong passphrases/confirmation,
  staged tampering, replay, and verified fixture cleanup; local fixtures do
  not claim off-host or whole-host-loss recovery.

### Fixed (2026-10-25, feature verification)

- Updated the pinned Go toolchain to 1.26.10 to address the `crypto/x509`
  vulnerability GO-2026-6809 across builds and CI.
- Added default browser journeys against a real local Go API for project and
  service creation, typed unavailable image-deploy intake in memory mode,
  responsive inspection, documentation, and unavailable usage. Added the
  missing Vite GraphQL proxy; no running workload is invented by the tests.
- Kept `midnight up` follow timeouts resumable when SSE connection setup hits
  its deadline, using REST status before reporting a deployment failure.
- Corrected namespaced Buildctl image targets to use the configured registry,
  including the returned image reference and provenance/SBOM subject.
- Made retained operator source uploads inherit existing eligible managed
  routes in the durable intake transaction, preserving private defaults and
  the canonical publication fence. Added generated/custom eligibility,
  replay, and injected-failure rollback regression coverage.
- Made restart actions wait for configured application readiness, record
  current runtime addresses, and republish managed routes before reporting
  success. Scoped route refresh and startup canonical repair preserve a
  prepared successor's publication. Added restart/address and promotion-fence
  regression checks; installed acceptance remains separate.
- Aligned worker removal with the API's stop-then-remove contract: a running
  active deployment remains protected, while a stopped one can be removed.
  Completion retires the target and clears only its own active pointer,
  preserving a successor promoted concurrently. Stop records stopped intent
  so startup route repair cannot restore traffic for a stopped workload.
- Kept human administrators on tenant changeset and variable authority, refreshed
  tenant metadata after apply, restored the Changes advanced menu, and added
  required runtime-write preconditions and stale-write reload recovery.
- Retried membership transactions after PostgreSQL serialization/deadlock
  conflicts so a losing conditional writer receives the typed version
  conflict. Canceling or failing a build with no recorded start leaves its
  build window unknown instead of fabricating completion evidence.
- Corrected the MVP runner's obsolete dashboard directory and replaced the
  legacy template queue assertion with canonical PostgreSQL provisioning,
  connectivity, replacement-persistence, and cleanup checks. Added runner
  regression coverage for failed, incomplete, timed-out, and unsafe replies.
- Made real-app acceptance select exact project/service/environment IDs and
  an existing managed domain, poll terminal operation state, and require the
  matching canonical active deployment plus an identifying HTTPS response.
  Saved HTTP/TLS policy is preserved when HTTPS terminates at an external edge.
- Added a Docker/PostgreSQL CI gate that requires real durable canaries and
  records skipped tests. Published a feature verification ledger, corrected
  the pinned Go version, and clarified that backup metadata/restore plans and
  synthetic surface inputs do not satisfy runtime or recovery acceptance.
- Added separate real BuildKit/registry and Traefik HTTP replacement checks,
  exact-source MinIO S3/credential tests, and a PostgreSQL browser lane for
  real TOTP enrollment, human sessions, variables, changeset apply, and stale
  configuration writes. Installed and external-provider proof remains separate.

### Fixed (2026-10-24, public dependency scan)

- Updated Vitest and its development-only packages to 4.1.12 to address two
  `@vitest/mocker` advisories reported after the public repository opened.
  Production npm dependencies remain free of reported vulnerabilities.

### Fixed (2026-10-06, release-candidate checks)

- Made installation-domain updates use the stable API's strong ETag and
  `If-Match` precondition contract, including the initial `"0"` configuration,
  and regenerated the stable and operator OpenAPI views with request/response
  examples. Stale updates now return the registered `412 PRECONDITION_FAILED`
  response instead of an undocumented `409` pairing.
- Updated the UI lockfile to patched `seroval` and Vitest releases, removing all
  known production dependency advisories and the critical test-runner findings.
  The two remaining Tailwind 3 build-only findings have documented, expiring
  OSV exceptions because they process repository-controlled input and are absent
  from production installs. Stabilized long-form console tests and isolated the
  GitHub setup CLI test from host keyring availability.
- Moved builds and CI to Go 1.26.6 and `golang.org/x/crypto` 0.56.0, clearing
  reachable standard-library and SSH denial-of-service advisories found during
  the final supply-chain scan.

### Changed (2026-09-25, generated-app wildcard HTTPS)

- Recorded completed Cloudflare delegation for an operator-owned domain and restart-safe
  wildcard DNS and edge HTTPS for generated-app hostnames. The
  operator-managed Caddy edge uses a zone-scoped Cloudflare DNS-01 credential,
  preserves the original Host for Midnight ingress, and no longer requires a
  per-service DNS or edge entry for generated domains.

### Changed (2026-09-24, instance and service domains)

- Added durable instance root/console/apps-domain setup and a branded public
  landing page on the configured console hostname. The authenticated console
  remains available at `/login` and on deep links.
- Reconciled maintained status and operator documentation with the installed
  schema-72 host and the public-domain evidence. Added a custom-domain
  cutover runbook covering immutable generated-host lineage, ownership
  verification, binding/backend readiness, and external edge TLS.

### Changed (2026-09-12, documentation reconciliation)

- Reconciled the README, handoff, backlog, and deployment-readiness summary
  against schema 71, the September 5 fresh-install/source-deployment reports,
  and the merged administrator-recovery implementation. Completed integration
  work is closed; remaining CLI parity and installed acceptance are explicit.
- Added `docs/README.md` and `docs/archive/README.md`; moved eleven historical
  reports into the archive and updated their links. Retained the existing
  planning bundle and a consolidation redirect where stable paths are referenced
  by tooling and immutable migrations. No new live-host evidence is claimed.

### Changed (2026-09-11, public website ownership)

- Extracted the public marketing and documentation application into the
  independent `packetloss404/midnight-marketing` repository. The live
  `deployatmidnight.com` site now runs on project-owned infrastructure behind
  Caddy, and the former Railway project is no longer part of the production or
  rollback path.

### Added (2026-09-06, administrator recovery)

- A lost or mistyped first-run administrator password was unrecoverable. `midnight admin create-user` drives the one-shot `POST /auth/claim`, which refuses a second run once the installation is claimed, and `midnight admin` offered no reset — so on a live install the only way back in was dropping and re-migrating the database. `midnight admin reset-password --email ...` now recovers one enabled installation administrator through the new `POST /api/v1alpha/auth/password-reset`, under the same operator recovery credential (`MIDNIGHT_API_BEARER_TOKEN`) that already authenticates `create-user` and the console's Emergency recovery. A signed-in session is refused with the new `AUTH_OPERATOR_RECOVERY_REQUIRED`: recovery is deliberately unreachable from any account, so no administrator can rewrite another's credentials.
- The second factor is never silently dropped. The reset enrols a replacement authenticator and prints its `otpauth://` URL and Base32 secret once — the same one-shot report `create-user` gives — or `--keep-totp` preserves the existing enrolment and is refused when the account has none, since that would leave an administrator on one factor. A reset also clears any pending password-rotation requirement, bumps the account's session generation, and revokes every live session, so a stolen session cannot outlive the recovery. Disabled and soft-deleted accounts are not recoverable this way; they report `RESOURCE_NOT_FOUND` rather than being brought back.
- The durable path commits the new credential, its hash-chained audit row (`identity.administrator.credentials.reset`), and the exact encrypted idempotency response in one SERIALIZABLE transaction, mirroring the identity-claim coordinator. It adds no schema: the reserved `BOOTSTRAP_TOKEN_CONSUMED` code and the unused `users.bootstrap_token_hash` column from migration 0007 both belong to the removed one-shot bootstrap-token flow and stay unused.
- The console offers the same reset inside **Emergency recovery** on `/login`: paste the operator token, name the account, set the new password, and either keep the existing authenticator or confirm a replacement the browser generates and shows once. It uses the token typed into that panel directly, so an operator who cannot sign in does not have to enter recovery mode and navigate back first.

### Changed (2026-09-05, install documentation)

- The runbook now covers the two things that stop a fresh install from ever serving a public route: Traefik's file-provider watcher holds the directory it was started with, so the step that creates the publication directory has to be followed by a Traefik restart (otherwise artifacts are written correctly and never seen, and deploys fail `ROUTE_PUBLICATION_FAILED ... Traefik has not acknowledged`), and `MIDNIGHT_APPS_DOMAIN` ships empty, which makes `midnight domain add` — the only way a human session gets a public route — refuse with `CAPABILITY_UNAVAILABLE`.

### Fixed (2026-09-05, route publication)

- Redeploying a service with a public route failed with `ROUTE_PUBLICATION_FAILED: route "..." service URL is duplicated`. A redeploy leaves one durable backend row per deployment, and the replacement container routinely reuses the address the removed one had, so the assembler emitted the same server twice and tripped the renderer's own duplicate-URL validation — taking the whole publication down over what is one server. Ready backends sharing an address now collapse to a single entry.

### Fixed (2026-09-05, source builds)

- Any image that runs as a non-root user and reads a file copied from the build context was broken. Source checkouts normalised extracted files to `0750`/`0640`, withholding the world-readable bit, and Docker `COPY` carries those bits straight into the image: an executable script arrived as `0751` after a `chmod +x` (mode bits `+x` cannot take away), so a container running `USER app` got execute without read and died in a restart loop on `cannot open ...: Permission denied`. Checkouts now extract `0755`/`0644`; host confidentiality still comes from the checkout root, which is `0750` and owned by the service user.

### Fixed (2026-09-05, CLI)

- `midnight service config put` could never succeed. The service-config resource requires a strong `If-Match` validator and the command sent the request through the generic JSON helper, which does not carry one, so every invocation returned `428 PRECONDITION_REQUIRED: If-Match is required` after the CLI had already validated `expected_version`. It now sends the conditional request the resource requires, through the helper that exists for exactly this purpose.

### Fixed (2026-09-05, install path)

- `midnight-doctor` failed every correctly prepared host. The install path requires an operator-installed Traefik owning ports 80 and 443, but `OR-CHK-DOC-004` treated any listener on those ports as a fatal conflict, with no way to say the port was claimed — so the gate the runbook makes mandatory could never be cleared. `--ingress-reserved-by=<service>` now records the reservation the check's own description already promised; an unclaimed listener still fails.
- The install runbook now says where the prerequisite binaries come from (and that `railpack` is one of them, resolved from `PATH`, absent from every config file), how to create the Postgres role and database with the DSN format, and that the documented step order leaves the package unconfigured until `dpkg --configure -a` runs the migrations — which nothing else in the runbook does and the server does not do at startup.

### Fixed (2026-09-05, packaging)

- A DSN written before the package was installed left every unit dead. The `midnight` user does not exist until installation, so a pre-created `/etc/midnight/db.dsn` is necessarily root-owned; postinst read it as root, migrated happily, and reported success, then all six units failed at boot with `read dsn file /etc/midnight/db.dsn: permission denied` on a host that otherwise looked correctly installed. Installation now hands the file to `midnight:midnight` with mode 0600, the same way it treats the keys it generates.
- postinst invoked `midnight admin migrate --no-prompt`, so the package printed a deprecation warning about itself on every install. It now passes `--yes`.
- The package could not be installed on a clean host. `postinst` still ran `midnight admin generate-bootstrap-token --out`, a command deleted with the bootstrap-token mechanism, and treated its failure as fatal — so `apt-get install` ended with `unknown flag: --out` and left the package unconfigured on every fresh install. The dead step is removed; the first administrator is created with `midnight admin create-user`.

### Changed (2026-09-05, install documentation)

- The operator install path now documents the parts of a working install that only existed in code: the mandatory Traefik API endpoint (and that it must not collide with midnight-server's own port), the flat route-artifact naming and why nesting silently breaks publication, the ingress-container and probe-container semantics, the deploy-concurrency and Docker-baseline knobs, and — the step with no prior documentation at all — creating the first administrator with `midnight admin create-user`.
- Doctor check `OR-CHK-DOC-016` was validating the removed nested artifact shape. It now fails on route artifacts stranded in `<group>/` subdirectories, which Traefik loads only at startup, and reports the flat path it expects. Route-artifact paths in the ingress contract, live-deploy proof, TLS runbook, and Traefik packaging README were corrected to match what ships.

### Fixed (2026-09-05, route publication)

- Traefik never observed route changes. Its file provider loads subdirectory files at startup but only watches the configured directory itself, so artifacts published as `<group>/<file>` were read once when Traefik started and never again — every later change was written and hash-verified but never seen. Artifacts now publish flat in the watched directory, and publishing removes any legacy subdirectory artifact so a restart cannot bring back a stale router.

### Fixed (2026-09-05, public routes)

- A public route now serves on a real host. One service's public route with no ready backend used to block route publication for its entire environment, so a service with a domain it had never deployed to permanently failed every other service's deploys there. A blocked route now only describes the group when nothing else can be published, and the promoter confirms its own routes published instead of inferring it from a group-wide error.
- The packaged Traefik API address pointed at midnight-server's own port, so every route acknowledgement check probed the control plane and failed with a 404 naming neither service. The shipped default now uses a non-colliding port and says Traefik needs a matching API entrypoint. The packaged ingress default also assumed a container, which the package does not install.
- Traefik's file provider reloads asynchronously, but publication was verified with a single immediate check, so routes failed on timing and were rolled back. The acknowledgement check now retries within a bounded budget; a real disagreement with the desired artifact still fails immediately.

### Fixed (2026-09-05)

- Domains created through the operator recovery path were never bound to their service, so their routes could never receive traffic: the domain and route were created, no `service_domain_bindings` row was written, and every deploy carrying a public route intent was refused as an unmatched selector while the domain sat at `blocked_backend`. The handler now records the binding and fails the request rather than creating an unusable domain. The tenant path already bound correctly and was never affected.
- A service's only deployment could never be removed. Removal refused whenever the deployment was the active record, but `active` only clears when a newer deployment supersedes it, so stopping never made removal possible. Removal now refuses only while the deployment is still running and asks the caller to stop it first, and `midnight remove` resolves the active deployment like every other runtime action instead of reporting that an active deployment is inactive.

### Added (2026-09-04 priority build-out)

- First human account without hand-crafted requests: `midnight admin create-user` and a console setup flow that detects an unclaimed installation, plus an unauthenticated `GET /api/v1alpha/auth/claim/status`. The dead parallel bootstrap-token mechanism is deleted.
- Service deletion: `DELETE /api/v1alpha/services/{id}` with resource-version CAS, idempotency, a single serializable transaction, migration 0071, matching CLI verbs, and a console danger zone. A service that is still running is refused with `SERVICE_DELETE_RUNNING` rather than silently orphaning containers.
- Deploy now for a source-linked service, mutable image tags resolved to digests at intake, and rejection of digests the registry does not hold.
- `midnight init`, a `status` that shows the serving release and any in-flight rollout, one `--output table|json` contract, and command flags in `--help` for 98 of 108 leaf commands (previously 16 of 105).
- The embedded agent now calls real mutating tools under the caller's own permissions, streams token by token, supports adaptive thinking and effort, enforces per-conversation budgets, and the failure-diagnosis worker is wired for the first time.

### Fixed (2026-09-04 priority build-out)

- Security: `set_variable` was writing plaintext secret values onto the tamper-evident audit chain; tool input is now redacted before it reaches the sink.
- A container that exits or restart-loops fails in about a second with its exit code and last log lines instead of after a five-minute readiness timeout; images that declare no port are no longer probed as web servers.
- The console is embedded in `midnight-server` instead of being resolved from a path relative to the Go source file, and `--storage auto` refuses to start without a database instead of silently running on an ephemeral in-memory store.
- Packaging: official template images are trusted by construction; `RuntimeDirectoryPreserve=yes` stops a restart of one unit from destroying the BuildKit socket; the build worker's cache directory is provisioned with the right ownership; and a failed build now reports the daemon's own output instead of `exit status 1`.
- Roughly 19,850 lines of unreachable code removed, including twelve dead packages, a self-validating SQL contract, and the legacy dashboard.

### Fixed (2026-09-04 hands-on run)

- Local compose stack: Go image now follows `go.mod` (`MIDNIGHT_GO_IMAGE_TAG`), the deploy worker tolerates developer Docker engines with `MIDNIGHT_WORKER_DOCKER_BASELINE=warn`, and enrolls its own container into each environment network (`MIDNIGHT_WORKER_PROBE_CONTAINER`) so readiness probes reach workloads.
- Deploy worker executes jobs through a bounded pool (`MIDNIGHT_WORKER_DEPLOY_CONCURRENCY`, default 4) with per-service ordering; one slow rollout no longer blocks other deploys, lease reclamation, or monitors.
- Readiness waits emit throttled progress events with the current block reason; images that declare a single `EXPOSE` port are routed and probed on that port when no runtime port is configured.
- Agent chat passes a bound tool environment (projects, services, deployments, logs), audits every tool call, honours the operator mutation gate, defaults to `claude-opus-5` with provider env overrides, and prices current models correctly; the MCP endpoints are mounted; an unconfigured provider returns `AGENT_NOT_CONFIGURED` (503).
- Console: starter service name defaults to `api`; resource cards have accessible names; deployment timeline rows wrap; agent errors are readable and keep the draft; implementation language removed from six primary strings.
- CLI: framework flags accepted anywhere; `project create`, `service create`, `environment create`, `deploy image` honour `--output json`; `up` reconnects after an idle stream instead of exiting 1; configuration errors give the next command and exit 64; `--help` lists command flags; `whoami` verifies bearer credentials.
- Docs: added `docs/quickstart.md`; documented local login; fixed Go version, ADR-0003 status, archive index, and stale local-stack claims.

### Added

- Added the reviewed environment configuration journey. Migration 70 records
  monotonic applied/deployed configuration revisions and tenant-scoped
  operation groups. `POST .../changeset/apply-and-deploy` atomically applies
  one exact reviewed revision, snapshots affected services, and queues the
  server-derived `rebuild`, `redeploy`, or no-op action; `GET
  .../operation-groups/{group_id}` reports aggregate and per-service progress.
  The generated Go client and stable-v1 candidate contract include both
  operations.

- Added a consolidated console Changes flow with safe non-secret autosave,
  explicit sensitive-value confirmation, grouped review, Apply & Deploy as the
  primary action, and apply-only under Advanced. Resource cards and service
  Overview now distinguish the traffic-serving release from a newer rollout;
  searchable Configure/Add Resource registries, shared command vocabulary,
  keyboard shortcuts, small-screen behavior, forced-colors, reduced-motion,
  and focus/overflow coverage complete the automated 9A-9E UX gates.

- Made Debian post-install diagnostics run the exact live
  `install-readiness` profile and label that scope in JSON. Package warnings
  remain non-failing, fatal/configuration exits propagate, and extracted-package
  verification now executes the packaged doctor binary against all five
  intended checks.

- Added a workspace-admin GitHub App installation flow across dashboard, CLI,
  and REST. Signed ten-minute state is bound to the initiating active human
  session and consumed once; the callback requires GitHub user OAuth proof,
  resolves installation and the exhaustive repository grant set from GitHub,
  and atomically synchronizes grants, link revocations, and audit evidence.

- Added provider-neutral DNS reconciliation for owned TXT, A, AAAA, and CNAME
  records, with a Cloudflare HTTP adapter, pagination, bounded retry, full-plan
  unmanaged-record conflict protection, mutation-free dry-run, redacted audit
  evidence, and a migration-63 ownership/content-digest drift fence. The
  worker is opt-in, keeps apply disabled by default, and reads the Cloudflare
  token from a worker-only systemd environment file. Real-zone/public HTTP and
  HTTPS proof remains an operator gate.

- Fixed the self-started MVP acceptance server to use the same explicit local
  bearer credential as its REST, CLI, and GUI probes, keeping local surface
  agreement aligned with the CLI authentication contract.

- Added the bounded tenant custom-hostname lifecycle (migration 0053): exact
  nested HumanSession API and CLI create/list/get/verify/delete, fresh
  per-incarnation TXT ownership challenges, strict hostname-only input, and
  positive-version retired-ID deletion. Pending custom routes stay private;
  exact TXT verification enables route publication and inheritance by current
  and future open deployments. The Networking panel shows copyable TXT and
  routing-DNS guidance, including apex A/ALIAS/ANAME operator handling, and
  keeps ownership, trusted ingress, TLS, and public reachability as separate
  states. TLS, wildcard hosts, live-zone DNS apply, and public-Internet/HTTPS
  proof remain separate gates; recovery `/domains` is never used as a tenant
  fallback.
- Added explicit custom-domain `tls` intent across REST, CLI, and GUI. HTTPS
  requests remain fail-closed until the worker-owned, staging-first ACME
  lifecycle records ready certificate evidence and the operator's Traefik
  resolver is configured.

- Added the tenant-safe generated HTTP domain lifecycle for durable human
  sessions: nested service/environment list/create/get/delete across
  API/CLI/GUI, current `domains.read`/`domains.write`, restricted-environment
  concealment, server-owned hostname/path/port/public/TLS policy,
  positive-version delete CAS, singleton idempotency, exact reauthorized
  replay, and one atomic domain/route/binding/response/audit transaction.
  Migration 0052 adds exact multi-route deployment bindings and
  service-managed generated domains, backfills compatible generated routes and
  open deployments, and rejects ambiguous legacy singletons with an actionable
  preflight. Each product incarnation receives a fresh opaque domain ID while
  retaining its generated hostname and route identity. Deleted IDs are
  transactionally retained in `retired_product_domain_ids`; product create
  refuses recovery-mediated reuse, and downgrade refuses to erase nonempty
  retirement history. Existing and future open deployments inherit the route, while
  canonical promotion requires ready candidate backend and current
  controller/Traefik/ingress-probe evidence. Durable Traefik resources are
  revision-scoped and verification requires the exact rendered hash, rule,
  service, backend set, and backend health while rejecting an overlapping old
  revision. Indeterminate canonical COMMIT acknowledgement is recovered only
  from exact PostgreSQL active-pointer, prior-state, route-publication, and
  template evidence. Custom domains, TLS, live-zone DNS apply, and
  public-Internet reachability remain separately gated.
- Added the tenant-safe service-variable lifecycle for durable human sessions:
  exact nested service/environment metadata reads and create/rotate/tombstone
  routes, server-derived tenant ownership, current `variables.read`/
  `variables.write`, restricted-environment concealment, strict 64 KiB
  write-only bodies, portable 255-byte create names, positive-version CAS,
  singleton idempotency, exact
  authorized replay, atomic audit, metadata-only API/CLI/GUI responses, and
  migration 0051 scope-integrity constraints. Immutable deployment snapshots
  now pin service aliases as well as variable versions, and production
  build/runtime decryption fails closed unless it can append a redacted
  hash-chained, execution-attributed audit record before injection.
  Infrastructure causes are retained for trusted error inspection but
  redacted from durable product failure reasons. Saving variable desired state
  does not deploy or restart a workload; shared-variable management and staged
  multi-resource changes remain gaps.
- Added tenant-safe ordinary environment metadata PATCH for durable human
  sessions: strict 16 KiB name/slug/tier input, current `environment.write`,
  restricted-environment concealment, positive resource-version CAS,
  singleton idempotency, exact terminal replay, atomic audit, and a
  deterministic fail-closed CLI flow. Protection/restriction changes, delete,
  duplicate, configuration copy, and runtime application remain closed.
- Added tenant-safe service/environment configuration GET/PUT for durable human
  sessions: current `service.read`/`service.write`, restricted-environment
  concealment, exact-version CAS, singleton idempotency, atomic exact replay and
  audit, active-session CLI/GUI flows, and deterministic CLI retry keys. Saving
  config does not imply deployment or runtime apply.
- Added canonical tenant-safe GitHub source binding: migration 0049
  workspace-exclusive installation grants, exact repository grants, canonical
  service links, legacy-link quarantine, recovery-operator assignment and
  revocation, human project-scoped repository/branch discovery, strict
  API/CLI/GUI link flows, exact signed push identity, revocation-safe staged
  intake, and repository-scoped worker token minting.
- Added the tenant-safe generated-route first-deploy gate: strict
  `route:{public:true}` API/CLI/GUI opt-in, migration 0048 exact deployment
  bindings, atomic domain/route/deploy/outbox/idempotency/audit admission,
  evidence-aware deployment reads, and route-preserving image replay.
- Consolidated the `midnightlight` clean-room reimplementation back into main (PR #18), porting its verified correctness fixes and closing two feature gaps while keeping Midnight's richer execution layers.
- Added a GitHub commit-status write-back path (token minting, status publisher, and drain worker) so deploys can report pending and terminal states back to commits.
- Added GitHub push-webhook path-filter fail-open on truncated pushes (>=20 commits) so a watch-path change hidden in a dropped commit no longer silently skips auto-deploy.
- Added CAA `accounturi` binding support (RFC 8657) so account-scoped CAA parameters are classified instead of discarded.
- Added a single `mutation(scope, handler)` middleware seam that enforces `Idempotency-Key` presence and scope across every mutation route.
- Added a local docker-compose dev stack (postgres, migrate, registry, buildkit, traefik, api, workers) with a `make dev-up`-style target for single-command local bring-up.
- Added the `web-next` React/Vite/Tailwind dashboard, rebuilt Railway-style and wired to the live backend (REST, `/graphql/v2`, and SSE) with service-detail tabs, creation/GitHub flows, and a Cmd+K palette; the server now serves it at `/` via `internal/api/webui.go` when a built dist is present, with the legacy `web/` kept at `/dashboard/`.
- Added canonical deployment records (branch `feat/canonical-deployments`): migrations 0014 (`deployments`, `service_environment_state`, typed `deployment_events`, DEFERRABLE one-active invariant, FSM validator), 0015 (gated historical backfill), and 0016 (attach the FSM trigger); the deploy-contract FSM guard and `PostgresDeploymentStore`; dual-write on deploy intake, worker outcome reconciliation, and redeploy/rollback/restart mirroring; and a guarded read-flip in the deploy API (reads the table when populated, else falls back to the operation-event path). CI-gated Postgres integration tests; live-DB migration apply remains a human/CI step.
- Added CLI coverage for the core MVP operator path: project listing/creation, service listing/creation, and image deploy submission.
- Added optional bearer-token protection for API deployments so single-VM operators can put a basic guard in front of mutation paths before the full auth model is hardened.
- Added live dashboard/runtime wiring for deployed services, routes, hosts, templates, audit evidence, backup state, recovery state, and API documentation links.
- Added deploy intake visibility so queued image deploys appear in runtime inventory instead of disappearing behind dashboard-only state.
- Added official database template planning metadata for Postgres, Redis, MySQL, and MongoDB, including volume intent, private connection guidance, backup readiness, and support gates.
- Added public launch governance docs for contribution, security, maintainership, branch protection, release-key trust, and public issue hygiene.
- Added MVP acceptance runner modes and machine-checkable blocker probes in `docs/mvp-acceptance.md`.
- Added contract-test reporting for README/MVP readiness docs so product status stays visible in automation.

### Changed

- Build-affecting reviewed runtime edits now enqueue a pinned-source rebuild
  from a freshly captured immutable snapshot. Runtime-only edits replay the
  active immutable image. The API reports this server-derived deployment
  impact before apply, rather than asking the browser to infer it.

- Ported a one-shot readiness check into a poll loop with timeout/backoff and a typed timeout error, so a container with a slow-passing `HEALTHCHECK` no longer fails the deploy on first inspect.
- Wrapped the GitHub App installation token in a redacting `Secret` type so a stray `%v`/marshal no longer leaks it, plus a response-body scrubber.
- Reworked the router local-publication writer to emit one artifact file per (service, environment) so a second service no longer clobbers the first, and made atomic writes fsync the parent directory after rename for power-loss durability.
- Demoted the agent dock to an overlay and moved the agent stream off `EventSource` to authenticated fetch-based SSE, fixing its previously broken send/stream contract.
- Reworked the dashboard away from demo fallbacks toward live API-backed product state.
- Cleaned up stale sprint and phase language in the primary product docs so MVP status is tied to acceptance evidence, not internal planning labels.
- Clarified that official database templates are currently planned/metadata-only until the worker path creates durable services, volumes, credentials, backup runs, and restore guidance.
- Clarified that fake/demo data is allowed only in explicit diagnostics or test fixtures, not in the normal product path.

### Fixed

- Fixed a deleted generated product domain being reconstructible from a stale
  host-empty worker payload or, after recovery reuse, becoming vulnerable to a
  stale ID/version delete. Generated workers now require exact durable
  deployment bindings, and retired product identities cannot re-enter the
  product lifecycle.
- Fixed concurrent route reconcilers being able to publish from the same stale
  group snapshot. A cross-process environment publication lock now spans
  snapshot load, artifact publication, Traefik acknowledgement/probe, and the
  final observation CAS.
- Fixed an indeterminate canonical deployment COMMIT causing route
  compensation and candidate cleanup even when PostgreSQL had made that
  candidate active. Recovery now acquires a dedicated session advisory lock
  before opening its read-only Repeatable Read proof snapshot; normal and
  template workers accept only exact durable post-commit proof, while definite
  failures still fail.
- Fixed route publication verification accepting a prior Traefik generation
  with the same stable router name. Durable resources now carry
  revision-scoped identities and exact active-config/backend evidence.
- Fixed generated-domain recovery controls sharing custom-domain TLS state,
  and made migration 0052 refuse lossy downgrade while service-managed domains
  or retired product identities remain.
- Restored the flat operator-recovery domain GUI alongside the tenant-only
  nested generated-domain panel, removed strong ETags from the dynamic
  route-evidence projection, and stopped presenting trusted local ingress
  evidence as public browser reachability.
- Fixed GitHub source authorization that previously trusted mutable
  `service.spec.github` metadata and exposed App-global installation discovery
  to product flows; canonical links now revalidate current tenant, grant,
  repository, branch, and link-version authority.
- Fixed GitHub branch discovery silently stopping at 100 entries; it now
  paginates to an explicit safety bound and fails instead of returning a
  truncated list.
- Fixed generic deploy intake accepting caller-supplied GitHub App
  installation/repository coordinates, which could have let a project
  credential reach repositories outside its canonical workspace grant.
- Fixed pending GitHub builds trusting authority only at staged intake. Jobs
  now carry the exact link version and revalidate it before token mint, build
  variable resolution, and build solve; checkout tokens request only the exact
  repository with `contents:read`.
- Fixed mixed-case canonical repository names being dropped after exact-ID
  lookup and branch-deletion pushes being treated as dispatch failures.
- Fixed durable route readiness that previously probed only the first published
  route, and stopped private deployments from inheriting another deployment's
  service/environment route during reads or replay.
- Fixed build digest extraction to read `containerimage.digest` from the buildctl metadata file instead of scanning stdout for the first `sha256:`, which could return a base-image, cache, or layer digest rather than the pushed image.
- Fixed secret hygiene in the build and checkout paths: redacted build-output progress/failure logs and reworked git checkout to inject auth via headers (rejecting credential-carrying URLs) with a redaction layer over stderr, so tokens no longer leak into persisted logs.
- Fixed the SPA catch-all so it no longer swallows unmatched backend paths (a PR #18 regression that returned an HTML 200 instead of a 404).
- Fixed stale dashboard and documentation references that made queued or unavailable states look like completed product behavior.
- Fixed CLI/API/docs drift around deploy, service, template, backup, recovery, and audit surfaces.
- Fixed local lint/test leftovers from earlier coverage and dashboard cleanup passes.

### Known MVP Gaps

- The alpha-6 package and schema 70 are installed on the primary upgraded VPS,
  but the current CLI/API source-build and immutable-image deployment journey
  still needs one retained, redacted live acceptance run. Fresh-host package
  acceptance remains separately open.
- The CLI does not yet wrap changeset Apply & Deploy or operation-group reads,
  and legacy-handler `--help` output does not enumerate all accepted flags.
  Local-source and immutable-image deployment commands themselves are
  implemented and tested.
- Installed private-GitHub push/status recovery, public DNS/HTTP/HTTPS, and
  product-level off-host backup/destructive restore still require operator
  evidence.
- Hosted release controls and CLI package-channel publication remain blocked
  on repository plan/billing, signing custody, namespace ownership, and trusted
  publisher setup.
- Gates 9A-9E of the reviewed configuration journey pass automated validation;
  an independent Gate 9F human re-score is still required before claiming the
  9/10 target.
- Multi-host scheduling, distributed networking, managed database HA,
  Kubernetes execution, and global edge routing remain later profiles.
