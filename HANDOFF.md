# Handoff: Midnight

Midnight is an open-source, self-hosted PaaS control plane written in Go with a
React console. The current product profile is one trusted Linux VM: create a
project and environment, deploy local source or an immutable image, operate the
service through the console/CLI/API, and retain explicit evidence for failure
and recovery. Multi-host execution is a later profile.

Read [`README.md`](README.md), [`BACKLOG.md`](BACKLOG.md), and
[`docs/mvp-acceptance.md`](docs/mvp-acceptance.md) before changing product
status. Dated parity and sprint documents are design history; `BACKLOG.md` is
the only ordered unfinished-work queue.

## Current checkpoint

Reconciled on 2026-09-25 against repository commit `4e802df`, the installed
`8ad2845` package, the committed September 5 install/deployment reports, and
the live domain observations in
`docs/research/installed-vps-verification-2026-09-24.md` and
`docs/research/cloudflare-wildcard-tls-verification-2026-09-25.md`.

- Repository schema: 72 migrations (`0001`-`0072`), including durable instance
  domain settings.
- First-administrator setup, tenant-authorized agent tools and streaming,
  diagnosis worker wiring, intake image resolution, CLI help improvements,
  embedded console assets, and build/route/package repairs are committed.
- Administrator credential recovery landed in `0891e0c`, merged by `4923a4a`:
  CLI `admin reset-password` and the console Emergency recovery panel use
  explicit operator authority, preserve or replace TOTP, and revoke sessions.
- CLI registration for that recovery command landed in `a580043`, merged by
  `f0c132c`; the current package exposes its help and validation path.
- The September 5 fresh-install report (`MID-116`, commit `b9ae643`) records
  schema 71, both doctor profiles without fatal failures, six active units,
  console/API responses, and first-administrator setup.
- The September 5 DocVault report (`MID-117`, commit `d073931`) records a
  source deployment served by Traefik over HTTP with its Host header, matching
  container/backend identity, and PostgreSQL/Redis connectivity and persistence.
- The September 24 observation records `8ad2845`, package
  `0.1.0~alpha-6`, schema 72, six active units, and the configured
  operator-owned namespace. It also records valid external HTTPS for the console
  landing page and the healthy DocVault workload at its custom hostname.
  Caddy terminates public TLS at the operator-managed edge and proxies the
  original Host to Midnight's HTTP ingress; Midnight therefore correctly
  records that workload route with `tls_desired=false`.
- The September 25 follow-up records completed delegation of the operator domain to
  Cloudflare, generated-app wildcard resolution, and a trusted Let's
  Encrypt wildcard certificate at the Caddy edge. HTTP redirects to HTTPS and
  an unused generated hostname reaches Midnight ingress after a Caddy/portal
  restart. Generated hosts therefore need no per-service DNS or edge entry;
  the exact Midnight route and a ready backend still determine whether the
  hostname serves an application.
- The DocVault cutover removed its generated hostname, verified the custom
  hostname, attached it to the active service/environment, and left it as the
  one public address shown by the console. The observation does not replace a
  fresh-host restart/rollback, private-GitHub, or recovery acceptance run.
- The public website and reader-facing documentation were extracted into
  `packetloss404/midnight-marketing`; this repo owns the control plane.

See [`docs/deployment-readiness.md`](docs/deployment-readiness.md) for the
dated evidence boundaries and remaining acceptance steps. Resolve the actual
checkout with `git log -1 --oneline --decorate`; test results belong to the
commit and environment on which they ran.

## Next work

Follow [`BACKLOG.md`](BACKLOG.md) in order and skip only `OPERATOR` work whose
external input is unavailable.

The next repository-owned item is `MID-107`: add CLI commands for changeset
Apply & Deploy and operation-group reads. Command help now exposes the shared
legacy flag definitions; do not reopen that completed work. The combined
transition and group read still require the console or API.

The nearest operator evidence is:

- `MID-002`: private-GitHub push, scoped checkout, build/deploy, status
  publication, and process-death recovery;
- `MID-003`: repeat the public-domain proof on the final fresh-host candidate
  and complete restart/rollback, audit, and recovery acceptance;
- `MID-111`: retain the exact BuildKit socket survival/source-build proof
  across a server restart; the packaging fixes are already committed;
- `MID-004`: product-level off-host backup, destruction, restore, and readback;
- `MID-010`/`MID-105`: hosted release controls and package-channel setup;
- `MID-108`: independent human Gate 9F re-score of the reviewed configuration
  journey.

Do not convert operator gates into fixtures or close them with local tests.

## What works now

| Surface | Current authority |
| --- | --- |
| Human identity | First-administrator setup; durable password/TOTP sessions shared by console and CLI; native keyring references; logout revocation; explicit operator credential recovery with session revocation |
| Projects and services | Tenant-scoped create/read flows, environment lifecycle where admitted, service source linking, invitations, and project automation tokens |
| Deployment | Local source through `midnight up`; digest-pinned image through CLI/API; durable operations, events, logs, cancellation, redeploy, rollback, restart, stop, and removal |
| Reviewed configuration | One durable environment changeset for variables, runtime, generated networking, and storage; apply-only or atomic Apply & Deploy with monotonic applied/deployed revisions |
| Apply & Deploy | Server-derived `rebuild`, `redeploy`, or `none`; fresh immutable snapshots; canonical deployment/operation/outbox intake; tenant operation groups and truthful partial results |
| Console | Environment Grid/Map, compact service state, serving-versus-rollout distinction, Changes separate from Activity, searchable Configure/Add Resource flows, command palette, responsive and accessibility coverage |
| GitHub | Workspace-bound App setup, repository grants, canonical service links, signed durable webhook intake, revocation-safe worker checkout, PR previews, and durable status delivery machinery; installed private-repository proof remains open |
| Runtime | Single-host Docker scheduler, BuildKit/Railpack or Dockerfile build path, local registry, private service DNS, Traefik file-provider routing, runtime observations, and structured logs/metrics |
| Public domains | Administrator-configured root, console, and generated-app namespace; branded public console-host landing page; generated and ownership-verified custom service domains; live Cloudflare wildcard DNS and edge-terminated HTTPS for the operator-owned apps domain |
| Storage and recovery | Durable volume, backup, restore-plan, monitor, incident, and audit stores; product-level off-host destructive-restore proof remains open |
| Templates | Runtime-backed PostgreSQL, Redis, MySQL, and MongoDB single-node templates plus reviewed local community forks |
| AI/MCP | Provider-abstracted agent dock, tool-use loop, failure diagnosis, and authenticated MCP surface with mutation policy and budgets |

## Important boundaries

- PostgreSQL is canonical production truth. Memory mode is for local tests and
  must not be cited as installed evidence.
- A changeset draft is proposed state. Apply-only advances desired state but
  creates no rollout. Apply & Deploy records deployment intake; workers and
  runtime evidence determine whether a build, deployment, or promotion
  actually succeeded.
- `operation_group.state=partial` means configuration committed while one or
  more safe deployment intakes failed. Never present it as rollback or success.
- Public intent, generated hostname, route publication, DNS resolution,
  firewall/NAT, TLS readiness, and external reachability are separate facts.
- Changing the instance apps domain does not rename existing generated domain
  rows. Cut over each existing service deliberately, and verify DNS, ownership,
  binding, backend readiness, edge routing, and public HTTPS as separate steps.
- When an external edge terminates TLS, its persistent configuration and
  certificate are operator evidence. Midnight's route should remain HTTP-only
  unless Midnight itself owns the certificate and TLS router.
- Human product routes never silently fall back to installation-wide operator
  recovery authority.
- Secrets are write-only or metadata-only at product boundaries. Never retain
  bearer tokens, passwords, DSNs, private keys, ciphertext, or rendered route
  configuration in evidence.
- The local scheduler is the only production execution profile. Multi-host,
  Kubernetes, global edge, and managed database HA remain later work.

## Repository map

```text
cmd/midnight/             developer and operator CLI
cmd/midnight-server/      REST, optional GraphQL, MCP, and embedded console
cmd/midnight-worker/      deploy/runtime/routing/reconciliation workers
cmd/midnight-buildkit/    source build worker
cmd/midnight-agent/       node agent
internal/api/             REST handlers and PostgreSQL coordinators
internal/build/           source detection, BuildKit, SBOM, provenance
internal/deploy/          deployment planning and execution contracts
internal/queue/           PostgreSQL leased queues
internal/secrets/         encrypted variable lifecycle
internal/router/          durable Traefik publication
migrations/               ordered SQL and manifest; schema version 72
api/openapi/              v1alpha and v1 contracts plus generated views
pkg/midnightctl/          generated Go API client
web-next/                 active React/Vite console
docs/                     product, operator, architecture, and acceptance docs
agile/                    historical planning and handoff archive
```

The legacy `web/` directory was removed in `48a25bb`; `web-next/` is the active
console, embedded into the server at build time. Generated `web-next/dist/`
output is not committed. Start at [`docs/README.md`](docs/README.md) for the
documentation map and [`docs/archive/README.md`](docs/archive/README.md) for history.

## Build and verification

```sh
go build ./...
go test ./...
make lint
make api-contract-check

cd web-next
npm ci
npm test
npm run build
npx playwright test
```

Use the narrower test set while iterating, but run verification proportional to
the change before updating status. Package and live acceptance have their own
procedures:

- [`docs/release-verify.md`](docs/release-verify.md)
- [`docs/mvp-acceptance.md`](docs/mvp-acceptance.md)
- [`docs/runtime/live-deploy-proof.md`](docs/runtime/live-deploy-proof.md)

## Architecture invariants

- Mutating REST routes require the documented credential class, current RBAC,
  strict bounded input, and exactly one valid `Idempotency-Key` unless the
  contract explicitly says the operation is read-only.
- Tenant mutations reauthorize inside the transaction and atomically commit
  canonical state, exact replay response, and audit evidence.
- Compare-and-swap versions and `If-Match` protect mutable resources and
  changeset transitions.
- Deployment workers consume immutable snapshots; rollback reuses a digest and
  never rebuilds.
- Failure codes belong in `internal/failurecodes`; user-facing recovery hints
  must not expose infrastructure secrets.
- Migrations are immutable once applied. Add the next numbered up/down pair,
  update `migrations/manifest.yaml`, and preserve prior checksums.
- New `TODO`, placeholder, planner-only path, or `ErrNotImplemented` in a
  production surface must map to a reviewed `BACKLOG.md` item.

## CLI deployment summary

```sh
midnight login --api-base https://midnight.example --email user@example.com
midnight link
midnight up

midnight deploy image \
  --service-id svc_... \
  --environment-id env_... \
  --image registry.example/app@sha256:<digest>

midnight deploy status --operation-id op_...
midnight deploy events --operation-id op_...
midnight deploy logs --operation-id op_...
```

The CLI currently exposes `changeset get|validate|apply|discard`. The console
or API is required for the combined Apply & Deploy transition until `MID-107`
lands.

## Git workflow

- Start from a clean, current `main` and use a `codex/` feature branch.
- Preserve unrelated user changes and inspect every shared worktree before
  staging.
- Follow [`docs/governance/merge-policy.md`](docs/governance/merge-policy.md)
  and the configured branch rules; this history includes merge commits.
- Never commit generated console bundles, credentials, evidence containing
  secrets, or package build output.
- After landing, verify both local `main` and `origin/main` resolve to the same
  commit.
