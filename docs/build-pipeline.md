# Build Pipeline (Railpack + BuildKit + Distribution)

This document describes the source-build pipeline that turns a git repository
into an immutable `image@sha256:<digest>` reference the existing deploy worker
can run.

The pipeline is built around three external binaries the operator installs:

1. **Railpack** (`railpack`) — Railway's MIT-licensed Rust successor to
   Nixpacks. Analyzes sources and persists a JSON build plan plus detected
   provider/package information.
2. **BuildKit** (`buildctl`/`buildkitd`) — performs the actual image build
   from a repo Dockerfile or through Railpack's custom `gateway.v0` frontend.
3. **Distribution** (`registry`) — github.com/distribution/distribution. The
   local OCI registry that stores the built layers + manifest. Midnight ships
   a wrapper at `cmd/midnight-registry`.

The build worker (`cmd/midnight-buildkit worker`) drains `build`-kind jobs
from the same Postgres-backed queue the deploy worker uses, runs the chosen
builder, pushes the result to the registry, and re-enqueues a `deploy` job
with the resulting digest-pinned reference.

## What the operator installs

| Component         | Pinned version       | Purpose                                                                                                                            |
|-------------------|----------------------|------------------------------------------------------------------------------------------------------------------------------------|
| `buildctl`        | `v0.28.1`            | CLI for BuildKit. This matches Railpack v0.23.0's upstream BuildKit dependency and is enforced in `internal/build/versions.go`.     |
| `buildkitd`       | `v0.28.1`            | BuildKit daemon. Rootless mode supported. Started out-of-band by the operator (`midnight-buildkit serve` wraps the launch).        |
| `railpack`        | `v0.23.0`            | Source analyzer. `prepare` persists `railpack-plan.json`, `railpack-info.json`, and pretty output.                                  |
| Railpack frontend | `v0.23.0` + digest   | BuildKit `gateway.v0` frontend. The immutable multi-platform digest is pinned in `versions.yaml`.                                   |
| `registry`        | `v3.0.0`             | Distribution registry. Wrapper at `cmd/midnight-registry`.                                                                          |
| `git`             | any modern release   | Used to clone source repos. The worker shells out with `git clone --depth 1 --branch <ref>`.                                       |

### Linux install snippets

```sh
# BuildKit
curl -fsSL https://github.com/moby/buildkit/releases/download/v0.28.1/buildkit-v0.28.1.linux-amd64.tar.gz \
  | tar -xz -C /usr/local
# verify
buildctl --version  # buildctl github.com/moby/buildkit v0.28.1 …

# Railpack (x86_64 Linux; use the arm64 asset + checksum on arm64)
curl -fsSLO https://github.com/railwayapp/railpack/releases/download/v0.23.0/railpack-v0.23.0-x86_64-unknown-linux-musl.tar.gz
echo 'e8bffc181c13e68c1c78ec618f1418e60b1602ec548f3ec1054996394ce0f06a  railpack-v0.23.0-x86_64-unknown-linux-musl.tar.gz' | sha256sum -c -
tar -xzf railpack-v0.23.0-x86_64-unknown-linux-musl.tar.gz -C /usr/local/bin railpack
# verify
railpack --version

# Distribution
curl -fsSL https://github.com/distribution/distribution/releases/download/v3.0.0/registry_3.0.0_linux_amd64.tar.gz \
  | tar -xz -C /usr/local/bin registry
# verify
registry --version
```

## Running the pieces

### 1. BuildKit daemon

The build worker reaches BuildKit through the `buildctl` CLI which talks to a
running `buildkitd` daemon over the unix socket. Operators start the daemon
out-of-band:

```sh
midnight-buildkit serve \
  --buildkitd /usr/local/bin/buildkitd \
  --addr unix:///run/midnight/buildkit/buildkitd.sock \
  --root /var/lib/midnight/buildkit
```

The wrapper enforces the pinned `buildkitd --version` against
`RequiredBuildKitVersion` before exec-ing the daemon.

### 2. Local registry

```sh
midnight-registry \
  --binary /usr/local/bin/registry \
  --config /etc/midnight/registry/config.yml
```

A starter config:

```yaml
version: 0.1
storage:
  filesystem:
    rootdirectory: /var/lib/midnight/registry
http:
  addr: 0.0.0.0:5000
  headers:
    X-Content-Type-Options: [nosniff]
```

Wire DNS so `registry.midnight.local` resolves to the host running the
registry — the build worker uses this name as the push target and includes it
in the manifest reference embedded in deploy jobs.

### 3. Build worker

```sh
midnight-buildkit worker \
  --worker-id midnight-buildkit-1 \
  --postgres-dsn "postgres://midnight@localhost/midnight" \
  --buildctl /usr/local/bin/buildctl \
  --railpack /usr/local/bin/railpack \
  --git /usr/bin/git \
  --registry registry.midnight.local:5000 \
  --registry-insecure \
  --output-root /var/lib/midnight/build \
  --checkout-root /var/lib/midnight/build/checkout \
  --cache-root /var/lib/midnight/buildkit/cache
```

`--registry-insecure` is enabled by default because the embedded registry
ships without TLS for the loopback case. Production endpoints behind a
reverse-proxy with a real cert should drop it.

### Environment variables

Every flag above maps to an env var so systemd unit files stay clean:

| Flag                  | Env                                  |
|-----------------------|--------------------------------------|
| `--worker-id`         | `MIDNIGHT_BUILDKIT_WORKER_ID`        |
| `--postgres-dsn`      | `MIDNIGHT_DB_DSN` / `MIDNIGHT_DATABASE_URL` |
| `--buildctl`          | `MIDNIGHT_BUILDCTL`                  |
| `--railpack`          | `MIDNIGHT_RAILPACK_BIN`              |
| `--railpack-frontend` | `MIDNIGHT_RAILPACK_FRONTEND`         |
| `--git`               | `MIDNIGHT_GIT_BIN`                   |
| `--github-app-id`     | `MIDNIGHT_GITHUB_APP_ID`             |
| `--github-private-key` | `MIDNIGHT_GITHUB_PRIVATE_KEY_PATH`  |
| `--github-api-base-url` | `MIDNIGHT_GITHUB_API_BASE_URL`     |
| `--github-clone-host` | `MIDNIGHT_GITHUB_CLONE_HOST`         |
| `--registry`          | `MIDNIGHT_REGISTRY_ENDPOINT`         |
| `--registry-insecure` | `MIDNIGHT_REGISTRY_INSECURE`         |
| `--output-root`       | `MIDNIGHT_BUILDKIT_OUTPUT_ROOT`      |
| `--checkout-root`     | `MIDNIGHT_BUILDKIT_CHECKOUT_ROOT`    |
| `--cache-root`        | `MIDNIGHT_BUILDKIT_CACHE_ROOT`       |
| `--poll-interval`     | `MIDNIGHT_BUILDKIT_POLL_INTERVAL`    |
| `--reclaim-interval`  | `MIDNIGHT_BUILDKIT_RECLAIM_INTERVAL` |
| `--lease-ttl`         | `MIDNIGHT_BUILDKIT_LEASE_TTL`        |
| `--source-artifact-gc-interval` | `MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL` |
| `--source-artifact-gc-timeout` | `MIDNIGHT_SOURCE_ARTIFACT_GC_TIMEOUT` |
| `--source-artifact-retention` | `MIDNIGHT_SOURCE_ARTIFACT_RETENTION` |
| `--source-artifact-reference-retention` | `MIDNIGHT_SOURCE_ARTIFACT_REFERENCE_RETENTION` |
| `--source-artifact-work-retention` | `MIDNIGHT_SOURCE_ARTIFACT_WORK_RETENTION` |
| `--source-artifact-quarantine-retention` | `MIDNIGHT_SOURCE_ARTIFACT_QUARANTINE_RETENTION` |
| `--source-artifact-gc-max-items` | `MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_ITEMS` |
| `--source-artifact-gc-max-verify-bytes` | `MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_VERIFY_BYTES` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_QUOTA_BYTES` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_BYTES` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_COUNT` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_BYTES` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_COUNT` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_UPLOAD_LEASE` |
| n/a | `MIDNIGHT_SOURCE_ARTIFACT_ORPHAN_CHARGE_RETENTION` |
| n/a                   | `MIDNIGHT_KEK_PATH`                   |

The worker runs the source-artifact janitor once at startup and then on the
configured interval. Defaults are conservative: 24-hour blob/reference/work
retention, one-hour quarantine grace, 128 candidates, and 128 MiB of hashing
per pass. Configuration that could overlap valid build work or verify fewer
than two maximum-sized uploads fails startup.

The human content-addressed upload path also defaults to a 5 GiB retained
logical quota per workspace, 200 MiB/four concurrent uploads per workspace,
1 GiB/32 concurrent uploads installation-wide, a ten-minute lease, and a
26-hour conservative orphan charge. The API reserves advertised bytes before
reading a body. An accepted or replayed upload is charged once per
workspace/digest before its reservation is consumed. Ledger charges survive
operation/deployment cascades and are released only after a successful CAS
janitor pass proves both canonical and quarantine locators absent. API and
worker quota/timing values must remain identical; unsafe explicit values fail
startup.

This accounting covers retained, committed human-session CAS archives. It is
not a hard physical-disk quota: random crash-staging files are age-cleaned but
are not linked to reservation-absence proof, and project-automation or
recovery-operator uploads retain the legacy extraction boundary.

GitHub App settings are optional for public repositories. Caller-supplied
generic deploy intake cannot submit GitHub App coordinates. A canonical signed
push job contains only non-secret workspace, installation, repository ID/name,
branch, and source-link-version coordinates; it never contains a token. The
worker revalidates that exact current link before checkout, before reading
build variables, and immediately before the build solve. It then reads the App
key and mints a token restricted to the exact repository with only
`contents:read`, verifies that the HTTPS clone URL host and path match the
durable repository identity, and passes the token to git through an
environment-only `http.extraHeader`. With a custom GitHub API base URL (GHES),
`MIDNIGHT_GITHUB_CLONE_HOST` is required so a credential is never sent to a
host inferred from untrusted job data. The webhook HMAC secret belongs on the
server and is not required by the build worker.

## Build-time variables and secrets

The production build worker opens the same PostgreSQL envelope store and
operator KEK as the API and deploy worker. Startup fails if the KEK is missing,
has the wrong mode, or cannot unwrap the durable envelopes.

For each source build, the worker:

1. lists redaction-safe variable names for the selected
   project/environment/service;
2. gives Railpack only each name plus a fixed non-secret placeholder so
   `railpack prepare` records the secret IDs without receiving plaintext;
3. after plan generation and immediately before the solve, resolves templates
   using the `build_inject` purpose;
4. invokes `buildctl --secret id=<NAME>,env=<generated-source>` with the real
   values present only in the child process environment;
5. supplies Railpack's deterministic `secrets-hash` cache-invalidation input,
   but redacts that digest from provenance; and
6. overwrites mutable plaintext byte buffers after the solve.

Variable values are never added to job/deploy payloads, process arguments,
Railpack artifacts, provenance, SBOMs, audit rows, or operation events.
BuildKit stdout and stderr pass through an exact-value streaming redactor
before the durable log file is written, including when a value is split across
subprocess writes or the solve fails.

Current limits are intentional and important:

- Every service-scoped variable with a portable environment name is offered as
  a BuildKit secret. There is not yet a user-facing build-only/runtime-only
  exposure selector or an immutable variable-version snapshot.
- Build commands are trusted with the secrets they request. They can read,
  print, write, or exfiltrate those values; pre-persistence redaction reduces
  accidental platform leakage but is not a sandbox or egress control.
- BuildKit's env-backed source necessarily places plaintext in the short-lived
  `buildctl` child environment. Go strings and the OS process environment
  cannot be reliably zeroed.
- Variable names containing characters outside `[A-Za-z_][A-Za-z0-9_]*` fail
  closed for builds even though a non-shell runtime consumer may accept them.
- The cache-invalidation digest changes with secret values. It is not a
  confidentiality boundary and is omitted from durable provenance.

`scripts/railway-parity-loop.sh toolchain` builds a fixture that refuses to
continue without the secret, deliberately echoes it from a build command, then
asserts that the pushed app works and the raw value appears in none of the
persisted plan/info/BuildKit/provenance/SBOM/log evidence.

## How source-ref deploys flow

```
POST /api/v1alpha/services/{id}/deployments
    body: {"source": {"repo": "https://…", "ref": "main", "root_directory": "apps/web"}, ...}
        │
        ▼
intake handler (internal/api/deploy_intake.go)
    • validate source.repo / source.ref / source.root_directory
    • atomically create operation, canonical deployment, first event, and outbox row
    • outbox dispatcher enqueues job on queue.Build
        │
        ▼
midnight-buildkit worker (cmd/midnight-buildkit/worker.go)
    • Lease(queue.Build)
    • Transition op -> running
    • SourceCheckoutPlanner.Checkout — git clone --depth 1 --branch <ref>
    • Builder selection
        ├── Dockerfile present → DockerfileBuilder
        └── otherwise          → RailpackBuilder (`railpack prepare`)
    • Dockerfile: BuildKitDriver.Solve using `dockerfile.v0`
    • Railpack: BuildKitDriver.SolveGateway using `gateway.v0` and the
      digest-pinned v0.23.0 frontend
    • Persist plan/info/pretty output beside provenance, SBOM, digest, and logs
    • Enqueue follow-up `deploy` job with image@sha256:<digest>
    • Leave the shared operation running for the deploy phase
        │
        ▼
midnight-worker
    • Lease(queue.Deploy) — existing digest-pinned path
    • Pull image, start container, promote route
    • Own the terminal operation/deployment transition
```

## Selecting a builder

The selection logic is centralized in `internal/build/builder.go`:

```go
func SelectBuilder(checkout SourceCheckout, dockerfile, railpack Builder) Builder
```

If `checkout.WorkDir/Dockerfile` exists, `DockerfileBuilder` wins. Otherwise
the build worker hands off to `RailpackBuilder`. Subpath builds anchor at the
subdirectory the operator supplied in `source.subpath` (useful for
monorepos), so a `Dockerfile` at the repo root that doesn't match the actual
build context is ignored.

## Both image and source paths remain supported

The deploy intake handler accepts either:

- `{"image": "<repo>@sha256:<digest>"}` — the original digest-pinned path,
  enqueues a `deploy` job directly.
- `{"source": {"repo": "<url>", "ref": "<git-ref>", "subpath": "<dir>"}}` —
  the new source-build path, enqueues a `build` job that re-enters the deploy
  path on success.

- `{"deploy_now": true}` — no image and no source. Intake derives the intent
  from durable service state and then follows one of the two paths above.

The legacy `source_ref` *string* field continues to be rejected with
`DEPLOY_SOURCE_REF_UNSUPPORTED` because nothing ever consumed that shape —
new callers must use the structured `source` object.

## "Deploy now": the first deployment of a linked service

Linking a source deliberately starts nothing, and every deployment action
(`redeploy`, `rollback`, `restart`) needs a deployment that already exists. A
freshly linked service therefore had no way to produce its first deployment
except by pushing a commit.

`POST /services/{service_id}/deployments` with `{"environment_id": "...",
"deploy_now": true}` closes that gap. Intake resolves the request against
durable state, in order:

1. the canonical source link (`ServiceSourceLinkLookup`, when the server has
   installed one), then
2. the service's `spec.github` link, then
3. the newest source-backed deployment in that environment, then
4. the newest digest-pinned image the service has run in that environment.

The resolved request is then validated, authorized, and committed exactly like
an explicit one, so RBAC, the `Idempotency-Key` contract, the operation record,
the canonical deployment row, and the queue payload are unchanged. When a
service has neither a source nor image history the request is rejected with
`RESOURCE_VALIDATION_FAILED` and nothing is queued. `deploy_now` is cleared
before validation; a request that still carries it on any other deploy path is
rejected rather than persisted as an intent with no image and no source.

Source links are resolved into a *generic git* intent (public HTTPS clone URL
plus branch). Generic intake mints no GitHub App credential, so a link that
only works with one is reported as "nothing to deploy" rather than queued as a
build that cannot authenticate.

## Image admission: mutable tags in, immutable digests out

Deploy intake resolves the caller's image against the registry before any
durable row or queue job exists (`internal/registry.Resolver`,
`internal/api/deploy_image_admission.go`):

| Caller supplies | Result |
|---|---|
| `nginx:1.27` | Resolved to the registry's digest and stored as `nginx:1.27@sha256:<hex>`. The platform owns the pin; the tag survives for display. |
| `repo@sha256:<hex>` that the registry holds | Accepted unchanged. |
| `repo@sha256:<hex>` that the registry does not hold | `400 RESOURCE_VALIDATION_FAILED`, nothing queued. Previously this was accepted and failed minutes later inside the deploy worker. |
| any reference, registry unreachable | `503 CAPABILITY_UNAVAILABLE`, nothing queued. An instance-side outage is never reported as a bad request. |

Manifest reads send both the OCI and the Docker v2 `Accept` media types
(manifest and index), prefer `HEAD` and fall back to `GET` when a registry does
not implement it, and perform the anonymous bearer-token exchange a public
registry challenges for. No credential is ever presented.

Every read goes through `internal/ssrf`: DNS is resolved once, the complete
answer set is validated, and only the approved literal is dialled. Only the
operator-configured local registry may resolve to a loopback or private
address; a cross-host redirect is refused rather than followed. Requests are
bounded by a short per-request budget and an overall resolve budget, so a
black-holed registry surfaces as a typed error instead of hanging the intake.

### Configuring image admission

| Variable | Default | Meaning |
|---|---|---|
| `MIDNIGHT_REGISTRY_ENDPOINT` / `MIDNIGHT_REGISTRY_ADDR` | unset | Private registry `host[:port]`; the only endpoint allowed to be non-public. |
| `MIDNIGHT_REGISTRY_INSECURE` | `false` | Serve that endpoint over plain HTTP. |
| `MIDNIGHT_DEPLOY_IMAGE_RESOLVE` | auto | `on`/`off` override. Auto means enabled when a registry endpoint is configured. |

With admission disabled the historical contract is preserved exactly: a digest
is required and no registry read happens, so an instance with no registry
wiring behaves as it always did instead of failing closed on every deploy.

## Provenance and SBOM

`BuildKitDriver.Solve` reuses the existing `internal/build` provenance and
SBOM helpers, so every build writes:

- `<output-root>/<svc>-<op>/provenance.json` — SLSA v1 provenance statement.
- `<output-root>/<svc>-<op>/sbom.json` — CycloneDX SBOM with resolved FROM digests.
- `<output-root>/<svc>-<op>/digest.txt` — the produced `sha256:<hex>` digest.
- `<output-root>/<svc>-<op>/buildctl.{stdout,stderr}.log` — solve logs.

These artifacts are byte-identical across runs over the same inputs because
the underlying buildctl runner normalises timestamps via the `Now` seam.

## Troubleshooting

| Symptom                                                  | Likely cause                                                                  |
|----------------------------------------------------------|-------------------------------------------------------------------------------|
| `buildctl --version` mismatch error                       | Installed BuildKit drifted from the pin. Reinstall `v0.28.1`.                  |
| Build job stays `running`, last event `build progress: …` | BuildKit is solving. Watch buildctl logs under `<output-root>/<svc>-<op>/`.    |
| `registry head manifest: not found`                       | Registry didn't accept the push. Check `registry --version` and TLS settings.  |
| `git clone failed: … remote: Repository not found`        | The repo URL or ref is wrong, or the worker's git credentials are missing.    |
| `deploy image does not exist in its registry`             | The digest was never pushed, or the tag was deleted. Nothing was queued.       |
| `image registry is unreachable; the deploy was not queued`| DNS, egress, or TLS to the registry. Check `MIDNIGHT_REGISTRY_ENDPOINT`.       |
| `service has nothing to deploy yet`                       | `deploy_now` on a service with no source link and no image history.           |
| `IMAGE_SIGNATURE_REQUIRED` on an official template image  | Should no longer happen: shipped digests are trusted by construction (below).  |

## Image trust for the images Midnight ships

The four official database templates (`internal/templates`) ship Docker Hub
images pinned by exact sha256 digest. Those digests are compiled into the
release, so the pin *is* the integrity guarantee: an operator cannot make it
stronger by re-declaring it in a runtime allowlist, and anyone who could change
it already controls the release artifact.

The deploy worker therefore composes `templates.OfficialImageReferences()` into
its image-trust policy automatically, alongside anything the operator
configured. Before this, an instance with `MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY`
set but no `MIDNIGHT_WORKER_TRUSTED_IMAGES` rejected `midnight template deploy
postgres` with:

```
IMAGE_SIGNATURE_REQUIRED: image "postgres:16.14@sha256:33f923b0…" is outside
the trusted local registry
```

Rules:

- Trust is composed only when a policy is already active
  (`MIDNIGHT_WORKER_TRUST_LOCAL_REGISTRY` or `MIDNIGHT_WORKER_TRUSTED_IMAGES`).
  A worker with no trust policy still performs no verification, exactly as
  before — composing here would silently fail-close every deploy on an instance
  that never asked for image trust.
- `MIDNIGHT_WORKER_VERIFY_SIGNATURES` (cosign) is unchanged: it remains the
  sole policy when enabled.
- The operator allowlist and the local-registry policy keep working unchanged;
  an image is admitted when any composed policy accepts it.
- At verifier construction the worker prints the active policy — each shipped
  digest with the template that ships it — so an operator can see what is
  trusted and why.

See also: `docs/error-codes.md` for the operator-facing recovery hints
attached to every failure code surfaced by the build path.
