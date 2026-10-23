# Local Development Stack

`docker-compose.local.yml` runs the full Midnight control plane against real
infrastructure — Postgres, a local OCI registry, BuildKit, and Traefik — so the
API, CLI, workers, and dashboard can all share one durable local runtime.

## Quick start

Prerequisites:

- Docker Engine with the Compose plugin (`docker compose`).
- The Docker socket (`/var/run/docker.sock`) available on the host — the deploy
  worker uses the host Docker Engine to run deployed containers.
- Free localhost ports (all bind to `127.0.0.1` only): 8080, 15432, 15000,
  11234, 8081, 8443, 8082, and 15173 (dashboard profile).

Bring the stack up, tail logs, and tear it down:

```bash
make dev-up        # docker compose -f docker-compose.local.yml up -d --build
make dev-logs      # docker compose ... logs -f
make dev-down      # docker compose ... down
```

Task equivalents: `task dev-up`, `task dev-logs`, `task dev-down`.

`make dev-up` first creates the git-ignored Traefik state directories under
`.midnight/local/traefik/`.

The local stack deliberately does not call Cloudflare. Public DNS, firewall
NAT, and ACME provider credentials are external gates. Exercise the
Cloudflare adapter with its HTTP test double in unit/contract tests; use a
disposable real zone only in the installed public-route lane documented in
[`docs/networking/cloudflare-dns.md`](networking/cloudflare-dns.md).

## Useful local URLs

- API: `http://localhost:8080`
- Postgres: `127.0.0.1:15432`
- Registry: `127.0.0.1:15000`
- BuildKit: `127.0.0.1:11234`
- Traefik HTTP: `http://localhost:8081`
- Traefik HTTPS: `https://localhost:8443`
- Traefik dashboard: `http://localhost:8082`
- Dashboard (optional `dashboard` profile): `http://localhost:15173`

The product/dashboard API mutations are gated by a bearer token. The default is
`dev-secret` (`MIDNIGHT_API_BEARER_TOKEN`); send it as `Authorization: Bearer
dev-secret`.

## Dashboard

The API serves the built dashboard at `http://localhost:8080/` whenever
`web-next/dist` exists. Build it with `make web-build` (or `cd web-next && npm
ci && npm run build`); no stack restart is needed. The `dashboard` compose
profile below runs the Vite dev server on `:15173` instead, for hot reload
while working on the frontend.

## Logging in

There are two ways in:

- **Operator recovery token.** Open `http://localhost:8080/`, expand
  **Emergency recovery** on the login page, and paste `dev-secret`. This is
  installation-wide recovery access: it can create projects and services and
  deploy digest-pinned images through the dashboard and CLI, but it cannot
  connect a GitHub source (that needs a human workspace session).
- **First human user.** There is no sign-up screen yet. Create the first user
  and workspace by POSTing to `/api/v1alpha/auth/claim` with the recovery
  token, an `Idempotency-Key`, and a TOTP secret plus current code. A
  copy-pasteable version is in
  [`docs/quickstart.md`](quickstart.md#optional-create-the-first-human-user).

For the CLI, `export MIDNIGHT_API_BASE=http://localhost:8080
MIDNIGHT_TOKEN=dev-secret` works with the recovery token, or run
`midnight login` after creating a human user.

## Services

The default stack starts:

| Service         | Image / command                              | Notes |
| --------------- | -------------------------------------------- | ----- |
| `secrets-init`  | `alpine:3.21`                                | One-shot; creates the cursor, variable KEK, independent credential/intent keys, and backup key |
| `postgres`      | `postgres:16-alpine`                         | Durable DB, `pg_isready` healthcheck |
| `migrate`       | `go run ./cmd/midnight admin migrate --no-prompt` | One-shot; applies the manifest-driven schema |
| `registry`      | `registry:3.0.0`                             | Local OCI registry |
| `buildkit`      | `moby/buildkit:v0.28.1-rootless`             | Build daemon (TCP `:1234`) |
| `traefik`       | `traefik:v3.1.5`                             | Ingress; file provider plus unbuffered JSON access logs |
| `api`           | `go run ./cmd/midnight-server serve`         | REST + GraphQL + dashboard; tails durable HTTP logs |
| `deploy-worker` | `go run ./cmd/midnight-worker serve`         | Deploy queue plus Docker logs/stats collector |
| `build-worker`  | `go run ./cmd/midnight-buildkit worker`      | Drains the build queue |

Optional profile:

- `dashboard` — Vite dev server for `web-next/`. Enable with:

  ```bash
  docker compose -f docker-compose.local.yml --profile dashboard up -d --build
  ```

### Migrations

Migrations are applied by the one-shot `migrate` service using Midnight's own
advisory-lock-guarded runner (`midnight admin migrate`), which writes the
manifest-driven `schema_migrations` table. The upstream `migrate/migrate`
(golang-migrate) image is intentionally **not** used — its `schema_migrations`
layout is incompatible. The `api`, `deploy-worker`, and `build-worker` binaries
also run the same idempotent runner at startup, so the schema is safe even if a
service races ahead.

### Networks

Two explicit Docker networks:

- `midnight-control` — Postgres, API, workers, registry, BuildKit, Traefik.
- `midnight-ingress` — Traefik and deployed app containers.

Durable DB/registry/BuildKit state, the shared Traefik access-log file, the
cursor key, API/worker variable KEK, independent automation/invitation/
variable-intent keys, backup key, and the Go module/build caches live in named
volumes. The API writes encrypted variable envelopes to Postgres; the build
worker and deploy worker use the same KEK only at their respective
build/runtime injection boundaries and must persist redacted decrypt audit
evidence before returning plaintext to the injector. Traefik file-provider and
ACME state live under the git-ignored `.midnight/local/` directory.

## Health checks

`scripts/dev/doctor.sh` runs static checks (compose parse, service discovery,
migration-file count, config presence) and, with `--live`, checks against the
running stack (host-port probes, Docker networks, applied-migration count vs
migration files):

```bash
make dev-doctor              # static checks
make dev-doctor ARGS=--live  # static + live checks (stack must be up)
# or: task dev-doctor -- --live
```

Note: the API exposes no unauthenticated `/health` path, so the api liveness
signal is a raw TCP probe of `127.0.0.1:8080` rather than an HTTP status check.

## Configuration knobs

All settings have baked-in defaults, so no `.env` file is required. Override by
exporting the variable or using an `--env-file`. Common ones:

| Variable | Default | Purpose |
| -------- | ------- | ------- |
| `MIDNIGHT_API_PORT` | `8080` | Host API port |
| `MIDNIGHT_API_BEARER_TOKEN` | `dev-secret` | Bearer token for gated routes |
| `MIDNIGHT_AGENT_AUTH_DEVELOPMENT` | `1` | Explicitly allow the shared local agent credential; never use in production |
| `MIDNIGHT_AGENT_HMAC_SECRET` | `dev-agent-hmac-secret` | Shared local-only agent credential used for bearer auth and signed reports |
| `MIDNIGHT_POSTGRES_DB/_USER/_PASSWORD` | `midnight` | Postgres credentials |
| `MIDNIGHT_DATABASE_URL` | derived | Full DSN override |
| `MIDNIGHT_GRAPHQL_PLAYGROUND` | `1` | Enable GraphiQL at `/graphiql` |
| `ANTHROPIC_API_KEY` | empty | Enables the dashboard agent chat panel |
| `MIDNIGHT_GO_IMAGE_TAG` | `1.26.9` | Go toolchain tag for the `golang:<tag>-bookworm` image that runs `api`, `migrate`, and both workers; keep it equal to the `go` line in `go.mod` |
| `MIDNIGHT_WORKER_RUNTIME_BACKEND` | `docker` | `docker` or `local` |
| `MIDNIGHT_WORKER_DOCKER_BASELINE` | `warn` | `enforce` or `warn`. `enforce` (the installed-host default) refuses to start when the Docker daemon does not match the required `daemon.json` baseline; `warn` logs the mismatch and continues, which is what Docker Desktop and rootless engines need |
| `MIDNIGHT_WORKER_DEPLOY_CONCURRENCY` | `4` | Maximum deploy jobs the worker runs at once. Jobs for the same service still run one at a time |
| `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR` | `/etc/traefik/dynamic` | Durable Traefik file-provider publication root |
| `MIDNIGHT_APPS_DOMAIN` | `apps.localhost` | Base domain for generated, single-label service hostnames |
| `MIDNIGHT_WORKER_TRAEFIK_API_URL` | `http://traefik:8080` | Trusted active-config acknowledgement endpoint |
| `MIDNIGHT_WORKER_INGRESS_PROBE_URL` | `http://traefik:80` | Trusted ingress probe address; the worker supplies the desired Host header |
| `MIDNIGHT_WORKER_INGRESS_CONTAINER` | `midnight-traefik` | Explicit ingress container enrolled into environment bridges |
| `MIDNIGHT_WORKER_USAGE_RECONCILE_INTERVAL` | `1m` | Independent usage evidence pass interval |
| `MIDNIGHT_WORKER_USAGE_RECONCILE_LOOKBACK` | `24h` | Initial/recovery evidence window; producer identities make overlap safe |
| `MIDNIGHT_WORKER_USAGE_RECONCILE_TIMEOUT` | `45s` | Whole reconciliation pass deadline |
| `MIDNIGHT_WORKER_USAGE_RECONCILE_STAGE_TIMEOUT` | `5s` | Independent producer-stage deadline |
| `MIDNIGHT_WORKER_USAGE_RECONCILE_WORKSPACE_BATCH` | `100` | Fair workspace cap per pass (`1..1000`) |
| `MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH` | `/var/log/midnight/traefik/access.jsonl` | API-side durable JSON access-log collector |
| `MIDNIGHT_REGISTRY_ENDPOINT` | `registry:5000` | Build worker push target |

## Known limitations

- **Build frontend availability**: the `build-worker` image includes
  checksummed BuildKit v0.28.1 `buildctl` and Railpack v0.23.0 binaries.
  Railpack source builds still require network access from BuildKit to pull
  the digest-pinned `railpack-frontend` image from GHCR.
- **Registry naming**: the build worker pushes to `registry:5000` on the
  compose network, while the deploy worker pulls through the host Docker Engine.
  Digest-pinned refs produced against `registry:5000` may not resolve on the
  host daemon; align `MIDNIGHT_REGISTRY_ENDPOINT` with a name reachable from
  both contexts if you exercise the full build→deploy hop.
- **Public route intent** remains opt-in per domain/deploy (`public: true`).
  The local worker publishes durable route sets, enrolls only the named
  Traefik container into the relevant environment bridge, and does not report
  a route reachable until Traefik acknowledgement and an ingress probe agree.
- **Cloudflare DNS** is not mutated by the local stack. The provider-neutral
  controller is opt-in and dry-run by default; local routes can still exercise
  durable publication and trusted probes without pretending that localhost is
  Internet-reachable.
- **Slow deploy-worker start**: the `deploy-worker` container runs from the
  plain `golang` image and installs `docker.io`, `git`, and CA certificates
  with `apt-get` on every fresh start before compiling the worker. Expect a
  few minutes on first boot and after `make dev-down`; `make dev-logs` shows
  progress.
- **GitHub commit statuses**: `cmd/midnight-github-status-worker` exists but is
  left out of the default stack. A commented `github-status-worker` service in
  `docker-compose.local.yml` shows how to enable it under a `github-status`
  profile once the App credentials are configured.
