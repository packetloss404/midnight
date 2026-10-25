# Advertised feature verification

Audit date: 2026-10-25. This ledger covers the capability claims in the
[README](../README.md), the console, and the
[MVP acceptance contract](mvp-acceptance.md). It is a coverage map, not a
declaration that every feature has passed an installed end-to-end drill.
Deployment identity, hosted CI, and current host health require separate
commit-bound evidence.

## What each kind of check proves

- **Unit and contract:** handlers, state transitions, request validation,
  authorization, or provider interactions against doubles. A passing double
  does not establish an external provider or a running workload.
- **Browser with fixtures:** Chromium exercises the rendered UI while
  `page.route` supplies controlled responses. This covers navigation,
  accessibility, error presentation, and interaction contracts.
- **Browser with a real local API:**
  `web-next/e2e/real-control-plane.e2e.ts` makes actual HTTP requests through
  Vite to a Playwright-owned Go memory server, without request interception.
  Projects/services survive a browser reload, not an API-process restart.
  Its image journey proves queued intake, not image pull, container health,
  traffic, or rollout completion. It deliberately skips against an external
  URL so test mutations cannot touch an operator installation.
- **Durable integration:** `-tags=integration` enables disposable PostgreSQL
  and Docker tests. Read their pass/skip events; Go may exit successfully
  when a live dependency is missing and the relevant test skips.
- **Installed/external:** package installation, real provider credentials,
  public DNS/TLS, source builds, worker promotion, restart/rollback, and
  destructive recovery require their own environment and retained evidence.
  Earlier dated reports prove only their recorded commit and assertion.

## Coverage by advertised capability

- **Dashboard, projects, and services.** Default real-API browser journeys
  create a project/starter service through the UI, reload server state,
  navigate desktop/mobile grid, map, inspector, and empty deployments, and
  read actual API documentation. Other UI states remain covered by fixture
  browser tests and component tests. Human identity, invitations, automation
  tokens, and recovery have handler/CLI/PostgreSQL coverage; this browser
  lane uses memory developer mode and does not prove a durable human login.
- **Image deployment and actions.** The real browser submits a digest-pinned
  image through `/graphql/v2`, checks canonical REST deployment state, and
  requires queued/inactive truth. Backend intake/actions/cancellation and
  transition tests cover authorization and state behavior. Real image pull,
  healthy promotion, restart, stop/remove, rollback, and public traffic still
  require the worker and installed `real-app`/recovery drills.
- **Embedded Claude and OpenAI-compatible agent.** `internal/agent/claude`,
  `openai`, `tools`, and API tests cover provider framing, tool-use loops,
  mutation gates, tenant authority, streams, and accounting with test
  providers. AgentDock component/fixture tests cover rendering. No successful
  paid provider call or autonomous production mutation is established by the
  local browser lane.
- **MCP server.** `internal/agent/mcp/server_test.go` covers JSON-RPC,
  discovery, authentication, rate limits, and mutation admission. A real
  external MCP client's authenticated tool round trip is a separate check.
- **Autonomous diagnosis.** `internal/agent/diagnosis` and API tests cover
  observer/result persistence and event attachment. A real worker failure
  followed by a real provider diagnosis is not implied by those tests.
- **ACME and HTTPS.** `internal/acme` tests cover durable order, budgets,
  diagnostics, provider contracts, and fail-closed route behavior. Live
  `TestLegoIssuerStagingIntegration` explicitly requires
  `MIDNIGHT_LE_STAGING=1`, a disposable domain, and DNS credentials. It may
  skip in ordinary integration runs. Edge-terminated HTTPS is distinct from
  Midnight issuing/managing a certificate; retain DNS, ingress, route, TLS,
  and public-probe evidence separately.
- **Provider-neutral DNS.** `internal/dns` HTTP doubles and PostgreSQL tests
  cover ownership, pagination, conflict handling, retry, audit, and restart.
  Applying a disposable real zone and reading it back externally is not
  covered by memory-mode browser tests. Dry-run plans are not DNS mutation.
- **Instance domain setup.** API/console tests cover administrative authority,
  conditional settings updates, derived hostnames, landing, and existing
  hostname lineage. A current public console/app namespace requires external
  DNS and ingress proof; the September host observations are historical.
- **BuildKit and source builds.** Build/worker tests cover source archives,
  path confinement, secret handling, queue handoff, artifact generation,
  registry publication contracts, and durable source intake. A BuildKit
  secret-mount test remains explicitly opt-in through `MIDNIGHT_LIVE_*`.
  A queued build or a mocked build runner does not prove a real Railpack or
  Dockerfile build, registry push, runtime activation, or BuildKit survival
  across a server restart (`MID-111`).
- **GitHub source binding, setup, webhooks, statuses, and PR previews.** API,
  integration-store, webhook, and status-worker tests cover grant/link
  authority, replay, revocation, durable delivery, exact-SHA fencing, and
  cleanup. A real App/private repository must still prove push receipt,
  scoped checkout, build/deploy, pending/terminal status publication, preview
  lifecycle, and process-fault recovery (`MID-002`).
- **Lease queue and canonical deployment FSM.** Queue contracts, worker
  tests, migration checks, and PostgreSQL integration tests cover locking,
  leases, attempts, durable operations, legal transitions, and fencing. The
  browser's queued row does not prove dequeue or successful promotion.
- **Audit ledger.** Hash-chain/CLI tests and PostgreSQL integration tests
  cover canonical rows, serialization, verification, export, and scoped
  pagination. `surface-agreement` checks real API projections using local
  test mutations. An installed export must be verified independently; an
  `empty` projection is not proof of a populated tamper-evident chain.
- **Secrets and variable lifecycle.** Vault/API/integration tests cover
  envelope encryption, AAD, versioning, sealed scopes, KEK rotation,
  redaction, and atomic mutation. Build/runtime injection and decrypt-audit
  evidence require actual workers. Saving desired state does not restart or
  deploy a workload.
- **Atomic environment changesets.** API/PostgreSQL tests cover capability
  revalidation, exact revisions, all-resource atomicity, usage reservation,
  and Apply & Deploy intake. Fixture UI tests cover review interactions.
  A completed apply does not mean the subsequent rollout succeeded; read
  aggregate/per-service operation progress. Direct CLI Apply & Deploy and
  operation-group commands remain `MID-107`.
- **Usage and limits.** Metering/admission tests and PostgreSQL integrations
  cover replay-safe evidence, coverage gaps, reservations, and denial.
  The real browser verifies the human-session requirement in developer mode;
  it does not prove live resource/build/runtime/public-traffic measurements.
  Payment processing and commerce are outside the claim.
- **REST, GraphQL, OAuth compatibility, and SSE.** Generated-client,
  OpenAPI/stateful contract, CLI, GraphQL, and stream tests cover the public
  shapes and admission boundaries; the real browser specifically covers
  image intake through GraphQL and REST reads. OAuth is the operator
  compatibility surface; human login uses sessions. GraphQL subscriptions
  remain later work (`MID-203`).
- **Node agent and observability.** Agent protocol/runtime/supervisor and
  telemetry tests cover desired state, probes, drift, redaction, and explicit
  coverage gaps. Registering a test agent and submitting route observations
  in `surface-agreement` does not run a host agent or collect Docker/Traefik
  logs. Installed host degradation and real application-log streaming remain
  separate acceptance assertions.
- **Official database templates.** The canonical route is
  `POST /templates/{slug}/instances`. PostgreSQL, durable Redis, MySQL, and
  single-node MongoDB have API/worker/Docker persistence tests. The
  `package-template` runner now polls the canonical PostgreSQL status until
  success and requires connectivity, replacement persistence, and probe
  cleanup. Queued/planned state cannot pass. The legacy
  `/dashboard/template-deploy` route is unavailable and is not acceptance.
  Managed HA, automatic engine upgrades, off-host backup, and destructive
  restore are not implied by template persistence.
- **Community templates.** HTTP-double and PostgreSQL tests cover pinned
  discovery, SSRF controls, forks, provenance, drafts, and reviewed changeset
  materialization. Browsing/forking a template is not proof that an arbitrary
  upstream workload deploys successfully.
- **Volumes, backups, and recovery.** Volume/Docker/recovery tests cover
  write/read persistence, encrypted objects, staging, journaled apply, and
  error handling. Real execution uses service-volume workflows and
  `midnight-recovery`; see the [runbook](runbooks/backup-restore.md). The
  experimental `/backup/runs` API writes metadata containing caller-supplied
  byte counts. `scripts/mvp-acceptance.sh backup` checks those manifests and
  restore plans only. It cannot replace the off-host
  copy/destruction/restore/application-readback drill (`MID-004`).

## Reproduce the checks

```sh
go test ./... -count=1
go test ./... -race -count=1
npm --prefix web-next ci
npm --prefix web-next run test:e2e:install
npm --prefix web-next run test:gate0
bash scripts/mvp-acceptance_test.sh
bash scripts/ci/integration-gate.sh
```

The default browser command owns its local API and Vite processes. It needs
Go, Node/npm, and Playwright Chromium. `MIDNIGHT_E2E_BASE_URL` selects an
external browser target and disables the throwaway mutation suite.
`e2e/live-api.e2e.ts` is a separate seeded external-resource journey and
skips unless its resource IDs are supplied. Login fixture tests do not
become live identity-provider tests when served by a production asset server.

The integration gate requires a working Docker daemon and records JSON test
events plus skipped-test names under `artifacts/ci-integration/`. Required
durable canaries must actually pass. Provider opt-ins and unavailable runtime
fixtures may still skip; inspect that ledger before making feature claims.

For installed acceptance, run the specific modes in
[MVP acceptance](mvp-acceptance.md) and the real recovery runbook. Keep raw
host-specific evidence in excluded local reports, not tracked documentation
or public CI artifacts. Check hosted CI for the pushed commit before deploying
that commit; a local gate cannot impersonate a hosted check.
