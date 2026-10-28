# Midnight

Midnight is an open-source, self-hosted Railway-like PaaS control plane written in Go, designed for a single trusted VM first: install on a VM, deploy an app, get HTTPS, and keep it running. Clear deployment records, logs, failure states, and rollback make recovery understandable when something breaks — and an embedded Claude agent can drive deploys, roll back services, and autonomously diagnose failures.

Status: Midnight is in MVP acceptance. The repo has the control-plane, package, migration, dashboard, CLI, and runtime wiring needed to run the acceptance checklist, but it is not MVP-complete until `docs/mvp-acceptance.md` passes end to end on a fresh single trusted VM. Some subsystems are honestly scaffolded in-source (multihost scheduler is stubbed per ADR-0018, the reconciler is diff-only, and some `_demo`/enterprise `plan` endpoints return planning artifacts rather than live actions).

MVP target: Midnight reaches MVP when a design partner can install it on a fresh single trusted VM, deploy and operate one real app with HTTPS, use the dashboard/CLI/API to understand deploys, services, routes, hosts, logs, audit evidence, backups, and recovery states, and pass the acceptance checklist in `docs/mvp-acceptance.md` without fake/demo-only data in the primary product path.

## Public Git History

This public repository began on October 23, 2026, with a reviewed snapshot of
Midnight's source, tests, and documentation. Development before that date took
place in a private repository. Its commits and pull requests were not imported
because they included operator-specific deployment details. The current result
of that work is tracked here; the dated [changelog](CHANGELOG.md), design
decisions, and backlog provide the earlier project record. Maintainers retained
the private history for internal provenance. New changes are recorded in this
repository from the initial public commit onward.

## Quickstart

Run the whole control plane on your laptop with Docker Compose, open the
dashboard, and deploy a container in about ten minutes:

```sh
make dev-up      # Postgres, registry, BuildKit, Traefik, API, deploy + build workers
make web-build   # builds web-next/dist; the API then serves it at http://localhost:8080/
```

Then follow [`docs/quickstart.md`](docs/quickstart.md) to log in, create a
project, and deploy through the dashboard and the CLI. Ports, services, and
configuration knobs for the local stack are in
[`docs/dev-local-stack.md`](docs/dev-local-stack.md). For a real single-VM
install, start at
[`docs/install/operator-install-path.md`](docs/install/operator-install-path.md).

## Website And Public Documentation

The public product site and reader-facing documentation live at
[deployatmidnight.com](https://deployatmidnight.com). Their source and
deployment assets are maintained independently in
[`packetloss404/midnight-marketing`](https://github.com/packetloss404/midnight-marketing),
not in this control-plane repository. The site is self-hosted on the project
infrastructure; Railway is no longer its production or rollback host.

## Headline Capabilities

Beyond the core deploy-and-keep-running PaaS loop, Midnight implements several substantial subsystems:

- **Embedded Claude AI agent.** A production-shaped Claude integration (`internal/agent/claude`) with a full tool-use loop, parallel tool execution, ephemeral prompt caching (5m TTL on the system+tools prefix), streaming events, and per-call token/USD cost accounting. The agent tool registry (`internal/agent/tools`) implements `redeploy`, `rollback`, and `set_variable`/`unset_variable`, gated by explicit mutation consent and live tenant authority and audited per call. Tenant chats are private to their creator within an authorized project, with scoped read tools for viewers. The `deploy_from_source` tool accepts [bounded inline source files](docs/agent-source-deployment.md) through the canonical source build pipeline. A streaming **AgentDock** in the dashboard (`web-next/src/components/AgentPanel/`) renders chat, tool calls, and a cost footer. Pins `anthropics/anthropic-sdk-go`. The agent is provider-abstracted behind an `agent.Caller` seam: alongside Claude, an OpenAI-compatible provider (`internal/agent/openai`, OpenAI Responses API, configurable via `MIDNIGHT_OPENAI_BASE_URL`) drives the same tool registry.
- **MCP server.** A standards-compliant Model Context Protocol server (`internal/agent/mcp`) bridges the same tool registry over JSON-RPC 2.0 (protocol `2025-06-18`), with a `/.well-known/mcp` manifest, bearer auth, per-session rate limiting, and a `MUTATION_DISALLOWED` gate — exposing the platform tool set to external MCP clients.
- **Autonomous failure diagnosis.** An opt-in worker (`internal/agent/diagnosis`) watches failed deploy/build operations, calls the configured AI provider within a 2-minute budget, persists a report with canonical tenant/deployment context, and records a `deploy:diagnosis_attached diagnosis_id=...` operation event. The deployment detail panel reads that event and displays the matching report read-only, including reports attached after the operation fails. Authorized project log readers may view it; no operation metadata field is invented. Real paid-provider execution remains a separate acceptance check.
- **ACME / Let's Encrypt issuance engine.** A lego-based engine (`internal/acme/lego_issuer.go`) with Cloudflare/Route53/manual DNS-01 providers, a staging-first production acknowledgement gate, durable certificate-order state, and an opt-in `midnight-worker` domain lifecycle reconciler. The worker records certificate references and keeps routes fail-closed until durable TLS evidence exists; installed/public HTTPS still requires the operator's Traefik state path and external DNS/ingress proof.
- **Provider-neutral DNS reconciliation.** The opt-in worker reconciles
  Midnight-owned TXT, A, AAAA, and CNAME records through a provider-neutral
  desired-state seam. Cloudflare is the first adapter, with paginated reads,
  bounded retries, unmanaged-record conflict protection, a durable
  content-digest ownership ledger, redacted audit evidence, and dry-run by
  default. Its credential boundary and public-VM handoff live in
  [`docs/networking/cloudflare-dns.md`](docs/networking/cloudflare-dns.md).
- **Instance domain setup.** An installation administrator can save a root
  domain and public ingress target, review the derived console and generated-app
  namespace, and use the branded console-host landing page while keeping the
  authenticated console on `/login`. Existing generated service hostnames keep
  their original lineage and require an explicit cutover.
- **BuildKit build pipeline.** A real build path (`internal/build`, `cmd/midnight-buildkit`) with railpack autodetection, Dockerfile builds, git source checkout, SBOM + SLSA-style provenance artifact generation, evidence bundles, and registry push. Midnight does not claim a SLSA Build Level or automatic publication of signed attestations.
- **Tenant-safe GitHub source binding.** Migration 0049 makes workspace-exclusive
  App installations, exact repository-ID grants, and canonical service links
  relational control-plane truth. Human CLI and dashboard flows discover only
  project-workspace grants, live-verify repository branches, require both
  service-write and Git-trigger capabilities, and atomically persist
  idempotency plus audit evidence. Webhook intake revalidates the grant and
  link version under the revocation lock. The worker rechecks the exact link
  version at checkout/secret/build safe points; repository-scoped
  `contents:read` checkout tokens stay memory-only.
- **Postgres lease queue.** A `FOR UPDATE SKIP LOCKED` job queue (`internal/queue/dequeue_sql.go`) with lease owner/TTL, heartbeat, attempt tracking, and `parent_operation_id` threading — plus a lexical contract validator that asserts every load-bearing SQL clause is present.
- **Canonical deployments FSM.** A canonical `deployments` table (migrations `0014_canonical_deployments`, `0015_backfill_deployments`, `0016_deployment_fsm_trigger`) with a database-level FSM guard trigger enforcing legal state transitions. Intake dual-writes into it and the deploy/reconcile worker reconciles against it, giving a single authoritative deployment state record.
- **Tamper-evident audit ledger.** A SHA-256 hash-chained audit log (`internal/audit/chain.go`, `row_hash = SHA-256(prev_hash || canonical_row)`) with advisory-lock-serialized INSERT-only append, signed checkpoints, and verify/export (ADR-011).
- **Envelope-encryption secrets vault.** AES-GCM envelope encryption (`internal/secrets/envelope.go`) with DEKs wrapped under an active KEK, variable-id AAD binding, plain/secret/sealed scopes (sealed is write-only), KEK rotation, immutable encrypted version history, alias-pinned variable snapshots, strict metadata-only human lifecycle routes, and fail-closed execution-attributed redacted decrypt auditing before production build/runtime injection. Saving variable desired state does not deploy or restart a workload.
- **Atomic environment changesets.** Human dashboard edits, environment config sync, and valid local community-template drafts converge on one durable review boundary for runtime config, encrypted variables, generated-domain intent, and volumes. Exact resource versions, RBAC, idempotency, audit evidence, and the terminal transition commit in one serializable PostgreSQL transaction. Apply-only remains available, while the primary Apply & Deploy path also advances the environment configuration revision, snapshots every affected service, and queues a server-selected immutable-image redeploy or pinned-source rebuild in the same durable intake boundary.
- **Evidence-backed usage and limits.** The worker independently reconciles replay-safe resource, build, runtime, and public-traffic evidence with explicit coverage gaps. Atomic changesets reserve positive net logical-volume deltas and commit admission, desired state, and the resulting gauge together; requested volume bytes and all commerce remain outside the enforcement claim.
- **GraphQL v2 + operator-compatibility OAuth.** A `/graphql/v2` surface with GraphiQL playground (`internal/graphql`, using `vektah/gqlparser`), the legacy operator-recovery OAuth authorize/token shim, SSE operation event/log streams, and the canonical `/api/v1alpha/*` REST contract. Human dashboard and CLI login use durable `/api/v1alpha/auth/sessions` instead.
- **Node agent.** A per-host agent (`cmd/midnight-agent`) with a heartbeat protocol, desired-state cache/diff reconciliation, Docker API runtime adapter, host probes, and a supervisor with drift detection and release evidence packets.

## Branches & pull requests

Open PRs against **`main`** (the default branch). Long-lived integration branches should merge **`origin/main`** regularly so drift stays small.

Keep PRs focused on one product or infrastructure outcome. When a change touches docs, runner scripts, API contracts, or dashboard behavior, update the matching acceptance/backlog entry in the same PR so the repo stays understandable to outside contributors.

## Current Product Surface

Midnight currently exposes the OSS Railway MVP path through:

- API server and dashboard served from `cmd/midnight-server` (REST v1alpha, `/graphql/v2` + GraphiQL, `/.well-known/mcp`, OAuth authorize/token, SSE event/log streams). The built console is embedded in the server and served at `/` with an `index.html` SPA fallback for client-side deep links; an explicit asset-directory override supports development.
- CLI commands for tenant-safe environment metadata update, service/environment config review, service-variable list/create/rotate/delete, generated-domain and custom-hostname management, topology config apply, environment config sync, changeset review/validate/apply/discard, workspace usage/evidence inspection, audit verification, project/service creation, local-source/image deployment, and deployment actions/cancellation. The API and console additionally expose atomic changeset Apply & Deploy and aggregate operation-group reads; direct CLI wrappers for those two routes remain an explicit backlog item.
- Postgres-backed control-plane storage with 72 sequential SQL migration pairs plus explicit memory mode for local smoke checks.
- Durable human sessions used by both the dashboard and CLI, with tenant-scoped project/service/environment overview, remote revocation on logout, and a separately labelled installation-wide operator recovery credential. First-run CLI/console setup claims the administrator; `midnight admin reset-password` and console Emergency recovery can reset administrator credentials with explicit operator authority, TOTP preservation/replacement, and session revocation. Project/service creation and service deletion, ordinary unprotected environment creation, strict name/slug/tier environment update, atomic multi-resource environment changesets, exact service-variable metadata/create/rotate/delete, image/local-source deployment, generated-route opt-in, deployment actions/cancellation, GitHub source linking, invitations, and project automation credentials use route-specific RBAC boundaries. Human environment PATCH requires current `environment.write`, singleton idempotency, and positive resource-version CAS. Changeset reads and writes independently prove the capabilities required by every contained resource kind. Protected environment creation/state changes, environment restriction changes, environment delete/duplicate, human shared-variable management, and other unadmitted human writes remain blocked until their own atomic coordinators land.
- Managed `midn_sa_` project automation credentials store only domain-separated HMAC verifiers, return raw bearer material once through an encrypted-idempotency response, and are admitted through a closed `ci-deploy-v1` method/route registry. They can inspect and deploy only their bound project; they cannot manage credentials, variables, installation settings, or operator routes.
- Postgres lease queue, deploy/reconcile worker, BuildKit build worker, Docker runtime adapter, route publishing, runtime inventory, audit, and recovery evidence endpoints. Real backup/restore uses `midnight-recovery` and service-volume workflows; the experimental `/backup/runs` API writes metadata manifests and does not copy application data.
- Human image deploys remain private by default. A durable human session may
  create or remove the service/environment's one server-derived generated HTTP
  domain through the nested API, CLI, or service Networking panel. The
  service-managed route binds exact current and future open deployments, and
  canonical activation stays fenced until the candidate's current
  backend/controller/Traefik/probe evidence agrees. Custom host ownership
  returns exact TXT evidence plus routing-DNS guidance. When the worker's DNS
  controller is explicitly configured, it can own that TXT record and a CNAME
  for subdomains or configured A/AAAA targets at the zone apex. Apply remains
  disabled until the operator reviews dry-run plans and sets
  `MIDNIGHT_WORKER_DNS_APPLY=true`. DNS mutation, TLS, firewall/NAT, and
  public-Internet proof remain separate gates.
- GitHub App source links use workspace-exclusive installation grants and exact
  repository IDs. Signed push intake is strict and revocation-safe, and the
  build worker revalidates the exact link at checkout/secret/build safe points
  and mints a repository-scoped `contents:read` token immediately before
  checkout. Verified pushes are committed before acknowledgement, planned
  against source authority no later than database receipt, and dispatched
  through fenced delivery/target leases with bounded retry, dead-letter, and
  atomic operator redrive. Terminal payload bytes age out after a 30-day
  recovery window while metadata and hashes remain. Delivery is at least once
  and downstream intake is idempotent; installed
  private-repository/process-fault evidence and end-to-end commit-status
  publication remain unproven.
- Embedded Claude agent (tool loop, MCP server, autonomous diagnosis worker) with a streaming dashboard dock, gated by an explicit mutation-allow flag.
- Dashboard views backed by live API calls for overview, deploys, hosts, routes, templates, audit, backups, recovery, the agent dock, and OpenAPI/docs links.
- Executable exact-digest official templates for PostgreSQL, Redis durable, MySQL, and MongoDB single-node, with encrypted variables, private networking, volumes, and replacement-persistence verification.
- Executable contract checks that keep OpenAPI, CLI, dashboard, docs, and MVP acceptance surfaces aligned for the current MVP lane.

The authoritative unfinished work is tracked in [`BACKLOG.md`](BACKLOG.md).
Executable release evidence remains defined by
[`docs/mvp-acceptance.md`](docs/mvp-acceptance.md). Dated delivery plans are
design history and must not be treated as current queues. Recent changes are
tracked in [`CHANGELOG.md`](CHANGELOG.md).

The [feature verification ledger](docs/feature-verification.md) maps advertised
capabilities to executable checks and records what still needs a real worker,
human session, provider credential, or installed-host drill. Fixture browser
tests and memory-mode API tests are explicitly separate from runtime proof.

The current CLI/API deployment verdict and the exact distinction between
implemented, installed, and live-proven behavior are recorded in
[`docs/deployment-readiness.md`](docs/deployment-readiness.md). The September 24
observation records the installed `8ad2845` package, schema 72, six active
units, an operator-owned namespace, and valid external HTTPS for the console and
DocVault custom hostname through the operator-managed edge. The September 25
follow-up records Cloudflare authority, wildcard generated-app DNS and HTTPS,
and restart-safe edge configuration. Fresh-candidate restart/rollback and the
remaining recovery gates still need acceptance evidence.

For the next installed public-route loop, record DNS authority, public ingress,
Midnight route readiness, TLS termination, and the public probe separately.
The observed zone delegates to Cloudflare. Its generated-app wildcard resolves to
the public ingress, and the operator-managed Caddy edge holds a Let's Encrypt
wildcard certificate obtained through Cloudflare DNS-01. Generated services do
not need per-service public DNS records; Midnight must still publish an exact,
ready Traefik route before a generated hostname serves an application.

## Repository Layout

| Path | Purpose |
|---|---|
| `cmd/` | Producing binaries: `midnight-server`, `midnight-worker`, `midnight-buildkit`, `midnight-agent`, `midnight-registry`, `midnight-github-status-worker`, `midnight-doctor`, `midnight-recovery`, and the `midnight` CLI. |
| `internal/` | Private implementation packages (60+, incl. `api`, `queue`, `build`, `acme`, `audit`, `secrets`, `graphql`, `agent/{claude,mcp,tools,diagnosis,conversation,auth}`); dependency boundaries are documented in `agile/engineering/skeletons/MODULE_DEPENDENCY_MATRIX.md`. |
| `pkg/` | Public Go packages for generated clients and shared resource types. |
| `api/` | OpenAPI and AsyncAPI artifacts. |
| `proto/` | Agent protocol definitions. |
| `schemas/` | JSON Schema artifacts. |
| `migrations/` | Database migrations and manifest. |
| `deploy/` | Packaging, systemd, AppArmor, logrotate, UFW, and Prometheus artifacts. |
| `docs/` | Publishable documentation, ADRs, runbooks, compatibility, and governance. |
| `test/` | Integration, contract, and failure-drill harnesses (incl. testcontainers Postgres + MinIO and e2e agent-loop tests). |
| `tools/` | Build-only tooling and contract-test fixtures. |
| `web-next/` | React + Vite + TypeScript + Tailwind dashboard, including the streaming AgentDock. |
| `agile/` | Historical planning archive and architecture notes. Product readiness lives in `docs/mvp-acceptance.md`. |

Start at the [documentation index](docs/README.md) for maintained guides and
the [archive index](docs/archive/README.md) for dated investigations and plans.

## MVP Acceptance

The concrete MVP definition, local smoke commands, VM install checks, dashboard/API probes, owned outside-scope diagnostics, and the single acceptance runner live in [`docs/mvp-acceptance.md`](docs/mvp-acceptance.md). Start with `bash scripts/mvp-acceptance.sh local`; use the document to decide whether the next work item is a product bug, a wire-up gap, or post-MVP scope.

## OSS Railway Readiness Checklist

Before calling Midnight MVP-ready, a fresh supported VM must prove the OSS Railway happy path without demo-only data:

- Install the package, run doctor, and boot package-managed services against Postgres storage.
- Deploy one real app image pinned by digest, create one public HTTPS route, and keep it healthy after `midnight-server` restarts.
- Create the official Postgres template through the dashboard/API path, including volume intent, secret-backed credential references, backup state, and restore guidance without plaintext secrets.
- Show the same projects, services, deploys, hosts, routes, logs, audit evidence, backups, and recovery states through dashboard/API/CLI surfaces.
- Produce typed reason codes for one failed deploy, one host degradation, and one backup/restore failure.
- Pass `local-gate`, `surface-agreement`, `package-template`, `real-app`, and
  `audit` as separate `scripts/mvp-acceptance.sh` runner modes with redacted
  evidence, then complete the real off-host backup/restore/readback drill in
  [`docs/runbooks/backup-restore.md`](docs/runbooks/backup-restore.md). The
  runner's `backup` mode checks experimental metadata and restore plans only.

## Local Commands

Use `task` when available. The `Makefile` is a thin compatibility shim.

```sh
make build
make test
make lint
make supplychain
```

Run the product locally (see [`docs/quickstart.md`](docs/quickstart.md)):

```sh
make dev-up      # start the compose stack (docker-compose.local.yml)
make web-build   # build the dashboard into web-next/dist
make dev-logs    # tail stack logs
make dev-down    # stop the stack
```

## Governance

- License: Apache-2.0 (`LICENSE`)
- Contributing: `CONTRIBUTING.md`
- Security: `SECURITY.md`
- Code of conduct: `CODE_OF_CONDUCT.md`
- Maintainers: `MAINTAINERS.md`
