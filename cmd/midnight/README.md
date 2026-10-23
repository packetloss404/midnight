# `midnight` CLI

The `midnight` binary is two CLIs in one process:

- **Developer commands** (`login`, `link`, `up`, `logs`, `status`, …)
  use a familiar project-link-and-deploy workflow while retaining Midnight's
  own explicit revision, evidence, and recovery semantics. Developers run them
  from inside the directory of the project they want to deploy.
- **Operator commands** (`admin create-user`, `audit verify`,
  `config apply`, …) continue to live here for parity with the original
  operator tooling. They are unchanged and remain reachable under the same
  flags they had before.

This README covers the developer surface end-to-end: **`login` → `link` →
`up`**. See `midnight --help` for the operator surface.

## Install

Install a standalone archive from the GitHub Release for your platform, verify
it against the release checksum manifest, then place the binary on `PATH`:

```sh
# Replace VERSION and the archive name with the release you downloaded.
sha256sum --check midnight_VERSION_SHA256SUMS --ignore-missing
tar -xzf midnight_VERSION_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 midnight "$HOME/.local/bin/midnight"
midnight version
```

macOS can use `shasum -a 256 --check` in place of `sha256sum`. Windows users
can verify the archive with `Get-FileHash -Algorithm SHA256` and compare it to
the matching line in `midnight_VERSION_SHA256SUMS` before extracting the ZIP.
Protected tag releases carry a Sigstore bundle for the checksum manifest. Verify
that bundle against the release workflow identity before trusting the SHA-256
lines; a checksum downloaded from the same compromised location is not a trust
anchor. Workflow-dispatch smoke artifacts are intentionally unsigned.

For source-based installation:

```sh
go install github.com/midnight/midnight/cmd/midnight@latest
midnight version
```

GoReleaser produces versioned, checksum-protected archives for `linux/amd64`,
`linux/arm64`, `darwin/amd64`, `darwin/arm64`, and `windows/amd64`. The
archives include `LICENSE`, CLI documentation, and static Bash, Zsh, Fish, and
PowerShell completion files. The release workflow verifies archive checksums,
the native binary version, completion output, and a script-free npm installation
before uploading them. Homebrew, Scoop, and npm publication remain disabled
until their operator-owned namespaces, repositories, and trusted-publisher
settings are configured; do not advertise those install paths before then.

The Debian package installs Bash, Zsh, and Fish completions into their
standard system locations automatically.

## Shell completion

Every standalone archive contains a `completions/` directory. Install the file
for your shell after installing the binary.

### Bash

```sh
mkdir -p "$HOME/.local/share/bash-completion/completions"
install -m 0644 completions/midnight.bash \
  "$HOME/.local/share/bash-completion/completions/midnight"
```

Start a new shell after installation. The `bash-completion` package must be
installed and loaded by the shell.

### Zsh

```zsh
mkdir -p "$HOME/.zfunc"
install -m 0644 completions/_midnight "$HOME/.zfunc/_midnight"
print -r -- 'fpath=("$HOME/.zfunc" $fpath)' >> "$HOME/.zshrc"
autoload -Uz compinit && compinit
```

### Fish

```fish
mkdir -p "$HOME/.config/fish/completions"
cp completions/midnight.fish "$HOME/.config/fish/completions/midnight.fish"
```

### PowerShell

The Windows ZIP contains `completions/midnight.ps1`. Dot-source it for the
current session, or add the same line (using a permanent absolute path) to
`$PROFILE`:

```powershell
. "$PWD\completions\midnight.ps1"
```

When the CLI was installed with `go install` instead of an archive, completions
can be generated directly:

```sh
midnight completion bash       # also: zsh, fish, powershell
```

## End-to-end flow

```sh
# 1. Sign in with a durable human account session. Password and TOTP, when
#    required, are read without terminal echo.
midnight login --api-base https://api.midnight.test --email you@example.com

# 2. From inside the project directory, create or select a project,
#    environment, and service in one step, then write .midnight/project.json
#    (which travels with the repo). `init` is safe to re-run: it reuses
#    whatever already exists and ends by printing the next command.
midnight init

#    `midnight link` remains the way to point the directory at resources
#    that already exist without offering to create anything.
midnight link

# 3. Deploy with the scoped human session. `up` never silently falls back to a
#    preserved installation-wide recovery credential.
midnight up

# Optional: create a project-scoped CI/deploy credential for automation. Its
# raw value is shown once and can drive the same project-scoped deploy.
midnight token create --name ci-deploy
MIDNIGHT_TOKEN=midn_sa_... midnight up --detach

# Helpers
midnight whoami     # show the server-verified human identity and session
midnight status     # linked context, serving release, in-flight rollout, last failure
midnight status --output json
midnight environment create --project-id prj_... --name Staging --tier staging staging
midnight environment update --expected-version 1 --name "Staging Next" env_...
midnight service github repositories
midnight service github branches --repository-id 123456
midnight service github link --installation-id 1234 --repository-id 123456 --branch main
midnight logs -f    # stream the latest deploy's logs
midnight observability logs --project-id prj_... --service-id svc_... --follow
midnight observability metrics --project-id prj_... --service-id svc_...
midnight redeploy   # queue an immutable image/config replay
midnight rollback --previous
midnight restart
midnight stop
midnight remove --deployment-id dep_...
midnight service delete svc_...   # confirmation required; --yes to skip
midnight project delete --yes prj_...
midnight cancel     # cancel the latest queued/running linked deployment
midnight unlink     # remove .midnight/project.json
midnight logout     # revoke the human session, then remove it locally
```

## Flags and output

Every leaf command lists its own flags in `midnight <command> --help`. One
concept is named one way — `--project-id`, `--environment-id`, `--service-id`,
`--api-base`, `--limit`, `--follow`, `--search` — and the historic spellings
(`--project`, `--env`, `--service`, `--endpoint`, `--n`, `--f`, `--q`) still
parse as hidden aliases so existing scripts keep working.

`--output table|json|jsonl|name` selects the output format on every command;
`--json` remains a documented alias for `--output json`. See
[docs/cli/flags.md](../../docs/cli/flags.md) for the canonical set, the
framework flags, and the exit codes.

## Deployment paths

Use `midnight up` for a linked local directory. It creates a deterministic
content-addressed upload intent, queues a source build, and streams operation
events unless `--detach` is supplied.

Use `midnight deploy image` when an immutable registry artifact already exists:

```sh
midnight deploy image \
  --service-id svc_web \
  --environment-id env_prod \
  --image registry.example/web@sha256:<64-lowercase-hex>

midnight deploy status --operation-id op_...
midnight deploy events --operation-id op_...
midnight deploy logs --operation-id op_...
```

For a human session, add `--route-public` to request Midnight's generated
public route. The server derives deployment, instance, and route identities;
custom route flags are recovery-only and rejected for a human session. Both
deployment paths require a durable server-side worker pipeline. Local tests
prove request and state contracts, while
[`../../docs/deployment-readiness.md`](../../docs/deployment-readiness.md)
tracks the installed live proof separately.

## Reviewed environment changes

The CLI can inspect and transition the durable changeset produced by the
console or config sync:

```sh
midnight changeset get --environment-id env_prod
midnight changeset validate --environment-id env_prod --revision 7
midnight changeset apply \
  --environment-id env_prod \
  --revision 7 \
  --idempotency-key apply-env-prod-r7
midnight changeset discard \
  --environment-id env_prod \
  --revision 7 \
  --idempotency-key discard-env-prod-r7
```

`changeset apply` is deliberately apply-only: it changes desired state without
queuing a rollout. The API and console also expose the primary atomic Apply &
Deploy transition and its aggregate operation group. Direct CLI commands for
those two routes are tracked as `MID-107`; until that lands, use the console or
generated/API client for the combined workflow.

Human sessions currently cover identity, tenant-scoped project/service/
environment discovery used by `link`, project creation, ordinary unprotected
environment creation, strict environment metadata update, service-variable
metadata and compare-and-swap writes, one generated HTTP-domain lifecycle,
project automation credential management, and project-scoped GitHub
repository/branch discovery plus service source linking. Environment create
and update are intentionally narrow: human callers need `environment.write` on
the exact project. Create rejects `--protected`; update accepts only an explicit
partial `--slug`, `--name`, or `--tier` plus the positive resource version
shown by environment list/get. Protected-state changes, delete, duplicate,
configuration copy, and runtime application remain recovery-only or separately
gated even though the shared CLI retains recovery commands.

`midnight environment create` accepts `--idempotency-key`. When omitted, the
CLI generates a non-empty request key using the same convention as project and
service creation. If a response is lost or uncertain, retry with the same
explicit key and unchanged arguments rather than starting a new mutation:

```sh
midnight environment create \
  --project-id prj_alpha \
  --name Staging \
  --tier staging \
  --idempotency-key environment-staging-v1 \
  staging
```

`midnight environment update` requires compare-and-swap so a stale editor
cannot overwrite a newer change. The command requires the active durable human
session and rejects recovery or external bearer credentials before sending
HTTP. It sends only the explicitly named metadata fields and
`expected_version`; there is no protection, delete, duplicate, copy, or runtime
control in the human request. The CLI validates the returned
environment identity, requested values, and exactly incremented
`resource_version` before reporting success. When `--idempotency-key` is
omitted, the CLI derives one deterministic versioned key from the exact
environment ID and canonical request body, so an unchanged uncertain retry
reuses the same intent:

```sh
midnight environment list --project-id prj_alpha
midnight environment update \
  --expected-version 7 \
  --name "Staging Next" \
  env_staging
```

## Variables

Human variable commands require the exact service and environment IDs and use
only tenant-scoped nested routes. The server derives workspace and project
ownership; do not pass `--workspace-id` or `--project-id` with a human session.
Human-created names must be portable environment identifiers matching
`[A-Za-z_][A-Za-z0-9_]*` and may contain at most 255 bytes. Responses contain
metadata only, and the table includes the current version needed for
compare-and-swap:

```sh
midnight variable list --service-id svc_api --environment-id env_prod

# Create a new name. `printf` avoids adding an unintended newline.
printf %s 'postgres://user:password@db/app' |
  midnight variable set \
    --service-id svc_api \
    --environment-id env_prod \
    --scope sealed \
    --stdin \
    DATABASE_URL

# Update and delete address the exact opaque ID returned by list/set.
printf %s 'postgres://user:new-password@db/app' |
  midnight variable update \
    --service-id svc_api \
    --environment-id env_prod \
    --expected-version 1 \
    --stdin \
    sealed_database_url

midnight variable delete \
  --service-id svc_api \
  --environment-id env_prod \
  --expected-version 2 \
  sealed_database_url
```

`--stdin` preserves the bytes it reads exactly, including a trailing newline.
`--value` remains available for compatibility but can expose a secret through
shell history and process listings. When `--idempotency-key` is omitted, human
mutations derive a stable, versioned HMAC from the active session and exact
route/body intent; the visible key does not contain a raw or guessable value
digest. `set` is create-only for humans. Use `update` with an exact variable ID
and positive version for replacement. New human-created names must match
`[A-Za-z_][A-Za-z0-9_]*` and are limited to 255 bytes; list/update/delete
remain compatible with recovery-created legacy names. The API and CLI accept
an explicitly empty value. Saving variable desired state does not deploy or
restart a service.

Stored operator-recovery credentials retain the legacy global variable routes
and ownership flags. Arbitrary explicit or environment-supplied non-human
tokens are rejected before any variable HTTP request; use a human login for
tenant access or the explicitly stored recovery credential for compatibility.

## Generated HTTP domains

With an active human session, domain commands use the exact project, service,
and environment in `.midnight/project.json` and only the nested tenant API. The
server owns the singleton generated hostname, root path, target port, public
HTTP intent, and disabled-TLS state:

```sh
midnight domain list
midnight domain add
midnight domain get dom_...
midnight domain delete --expected-version 1 dom_...
```

`domain add` accepts no hostname, `--generated`, path, port, public/private, or
TLS override from a human session. It creates or converges the one
service-managed generated domain. Delete requires the positive desired-state
`version` returned by list/get. Add and delete generate fresh secure
idempotency keys when omitted; after an uncertain response, retry the unchanged
operation with the same explicit `--idempotency-key`.

`route_apply_state` is environment route-group state projected onto the domain,
so a sibling route can block the group. `route_reachable` proves current
backend/controller/Traefik/trusted Host-header probe agreement only; it does not
prove public DNS or Internet reachability. Custom hostname ownership, editable
route properties, TLS, and certificate lifecycle remain unavailable to the
tenant commands.

Stored operator-recovery credentials retain flat `/domains*` compatibility for
list/add/delete and print an explicit recovery notice; the typed `domain get`
view is human-only. Project automation credentials cannot manage domains.

The GitHub source commands use only the tenant routes; they never fall back to
installation-global recovery routes. Pass a fixed `--idempotency-key` when
retrying the same source link or unlink after an uncertain response. Human
sessions can submit content-addressed `up` or digest-pinned image deploys, read
tenant-scoped progress, and use the exact REST-backed redeploy, rollback,
restart, stop, and remove actions. A managed `midn_sa_` credential can submit
project-scoped automation deploys but cannot use deployment-action or
operation-cancel routes. Cancellation withdraws unleased work immediately and
asks a leased build/deploy worker to stop at a named safe point before traffic
promotion; it does not interrupt an active BuildKit solve. For a human image
deploy, `midnight deploy image --route-public` requests the single
server-derived generated HTTP route; omitting the flag keeps the deployment
private. Human sessions cannot provide a custom host, route name, group, path,
or TLS setting. Tenant failure marking and other separately gated mutations
remain unavailable until their tenant coordinators land.

The observability commands read the same authenticated durable store as the
GUI. Logs support project/environment/service/deployment/operation/replica
scope, surface and level filters, search, bounded history, and opaque
continuation cursors. Metrics support summed or per-replica Docker samples.
When a collector is absent, the CLI reports that state instead of inventing
zero-valued history.

## Authentication

`midnight login` posts the account email and securely prompted password to
`POST /api/v1alpha/auth/sessions` with `client_kind: cli`. If the server
responds with `MFA_REQUIRED`, the CLI securely prompts for a TOTP code and
retries. The returned `midn_user_` credential is a durable, expiring,
revocable human session; the CLI rejects any other token class from this
endpoint. Remote login requires HTTPS, with plain HTTP allowed only for a
loopback IP during local development.

The human session is stored under `~/.midnight/config.json`, keyed by API base
URL, so a single laptop can hold credentials for several Midnight servers.
Every ordinary authenticated command uses the same precedence:

```text
--token -> MIDNIGHT_TOKEN -> MIDNIGHT_API_TOKEN -> stored server token -> error
```

Endpoints resolve through `--api-base`, `MIDNIGHT_API_BASE` (or the
legacy `MIDNIGHT_BASE_URL`), the current project link, then a stored
server only when exactly one exists. Missing or ambiguous credentials
fail rather than selecting a server or environment arbitrarily.

Older installations may also have an installation-wide operator recovery
credential from the former OAuth shim. Midnight preserves that credential
separately when a human logs in and never silently promotes it into the
ordinary developer credential slot. The compatibility flow remains explicitly
operator-scoped as `midnight admin authorize-cli`; it is not human login.
Explicit `--token`, `MIDNIGHT_TOKEN`, and `MIDNIGHT_API_TOKEN` inputs remain
available for operator/automation use.

Durable CLI credentials are stored in the operating system's native keyring
by default (macOS Keychain, Windows Credential Manager, or the desktop secret
service on Linux). `~/.midnight/config.json` contains only endpoint, identity,
and opaque credential-reference metadata; newly written bearer tokens are not
placed there. Existing version 1 or 2 plaintext config files remain readable
and migrate to reference-only version 3 metadata on the next successful
credential mutation. Keep the legacy file owner-readable until that migration
has completed.

Headless and test environments may explicitly set `MIDNIGHT_CONFIG_HOME`.
Doing so selects a separate owner-only `credentials.json` file inside that
exact directory instead of the OS keyring. This fallback is never selected
automatically when the native keyring is unavailable, so desktop credential
writes fail closed rather than silently downgrading storage security. Config
mutations use a per-config cross-process lock and atomic metadata replacement.

Project administrators manage scoped automation credentials with:

```sh
midnight token list
midnight token create --name github-actions
midnight token revoke --token-id tok_...
```

Creation defaults to a 30-day lifetime and prints the raw `midn_sa_` value
once. For a caller-controlled retry key, also pass a fixed RFC3339
`--expires-at`; this preserves the exact request body required to replay the
encrypted one-time response safely.

`midnight logout` revokes the current human session on the server before
deleting it locally. Network and server failures preserve the local credential
so an active session is not mistaken for a revoked one. `--local-only`
deliberately forgets the local session without revocation. Operator recovery
credentials are never sent to human identity endpoints and are not changed by
human logout.

## Linking

`midnight link` interactively picks a project, environment, and
service, then writes:

```jsonc
// .midnight/project.json
{
  "version": 1,
  "api_base": "https://api.midnight.test",
  "project_id": "prj_alpha",
  "project_slug": "alpha",
  "environment_id": "env_prod",
  "environment_name": "prod",
  "service_id": "svc_web",
  "service_slug": "web"
}
```

The file lives in the **current working directory**, not in `$HOME`. This
is a deliberate departure from Railway, where the link is stored in a
global `$HOME/.config/railway/config.json` keyed by absolute cwd:
users complained about the global file getting out of sync, and the
per-cwd store also makes container CI work without a persistent `$HOME`.

Pass `--project`, `--environment`, or `--service` to skip the interactive
prompt.

## Uploading

`midnight up` tar-gzips the working directory (honouring `.gitignore`
and `.midnightignore`) and uploads the raw bytes to:

```
POST /api/v1alpha/services/{service_id}/up
Content-Type: application/gzip
Content-Length: {exact compressed byte count}
Idempotency-Key: midnight-up-v1-{sha256-of-upload-intent}
X-Midnight-Archive-Digest: sha256:{digest of exact compressed bytes}
X-Midnight-Commit-Message: {--message, optional}
X-Midnight-Environment:    {--env or linked environment, required}
```

Pass `--idempotency-key` to supply an explicit retry key. Otherwise the CLI
derives a stable key from the exact prepared gzip bytes, resolved service,
environment, and optional message. Repeating the same invocation therefore
replays safely, while changing any of those inputs creates a different key.

Human-session uploads have a hard 50 MiB compressed maximum and also bound
archive entries and expanded bytes. `MIDNIGHT_UP_BODY_LIMIT_BYTES` may lower
that human ceiling; it cannot raise it. With a human session, the server
authorizes and durably reserves the advertised logical bytes before reading
the body, validates the complete archive, and reauthorizes while publishing it
to immutable content-addressed storage. The accepting PostgreSQL transaction
creates the operation, deployment, event, outbox intent, artifact ownership,
workspace/digest charge, idempotent response, and audit evidence. The API does
not extract human uploads; the build worker materializes a unique checkout for
each attempt.

The defaults admit 5 GiB of retained logical source archives per workspace,
200 MiB/four concurrent uploads per workspace, and 1 GiB/32 concurrent uploads
installation-wide. Retained quota exhaustion returns `413
SOURCE_ARTIFACT_QUOTA_EXCEEDED`; temporary upload backpressure returns `429
SOURCE_ARTIFACT_UPLOAD_BACKPRESSURE` with `Retry-After`. This is a logical
committed-CAS quota, not a hard filesystem quota: crash-orphaned staging files
and the legacy compatibility path remain outside it.

Project automation and recovery-operator credentials currently retain the
legacy `local://` staged-extraction compatibility path and its separately
configurable body-limit semantics. Both paths return a canonical JSON envelope:

```jsonc
{
  "operation_id": "op_abc",
  "operation":    { "id": "op_abc", "kind": "build", "state": "QUEUED", ... },
  "deployment_id": "dep_abc"
}
```

By default the CLI then streams `GET /api/v1alpha/operations/{op}/events?stream=sse`
to your terminal using the same scoped credential. `--detach` returns
immediately after queueing. Upload and event-stream requests refuse redirects
so bearer credentials are never forwarded to another location.

## Ignore patterns

The packager honours, in order:

1. A built-in deny list: `.git`, `node_modules`, `.midnight`, `.DS_Store`.
2. Patterns from `.gitignore` at the repo root.
3. Patterns from `.midnightignore` at the repo root (highest precedence
   for project-specific overrides).

Patterns use Docker-compatible matching with Git-style basename expansion.
Negations (`!pattern`) are supported, and later patterns can re-include a path
except for the immutable safety exclusions: `.git` and `.midnight` are never
packaged.

## Configuration files

| File                              | Owner    | Purpose                                  |
|-----------------------------------|----------|------------------------------------------|
| `~/.midnight/config.json`         | User     | Per-API-base identity and keyring-reference metadata |
| `./.midnight/project.json`        | Project  | Per-cwd link to project + env + service  |
| `./.midnightignore`               | Project  | Optional ignore overrides for `up`       |
| `MIDNIGHT_TOKEN`                  | CI env   | Project/CI token; overrides stored token |
| `MIDNIGHT_API_TOKEN`              | env      | Legacy token alias; overrides storage    |
| `MIDNIGHT_API_BASE`               | env      | Default for `--api-base`                 |
| `MIDNIGHT_BASE_URL`               | env      | Legacy API-base alias                    |
| `MIDNIGHT_UP_BODY_LIMIT_BYTES`    | server   | Lower human 50 MiB max; set legacy cap   |
| `MIDNIGHT_UP_EXTRACTION_ROOT`     | shared   | Human CAS/checkouts; legacy extraction   |
| `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_QUOTA_BYTES` | server + worker | Retained logical bytes/workspace |
| `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_BYTES` | server + worker | Concurrent bytes/workspace |
| `MIDNIGHT_SOURCE_ARTIFACT_WORKSPACE_INFLIGHT_COUNT` | server + worker | Concurrent uploads/workspace |
| `MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_BYTES` | server + worker | Concurrent bytes/installation |
| `MIDNIGHT_SOURCE_ARTIFACT_GLOBAL_INFLIGHT_COUNT` | server + worker | Concurrent uploads/installation |
| `MIDNIGHT_SOURCE_ARTIFACT_UPLOAD_LEASE` | server + worker | Active upload reservation lease |
| `MIDNIGHT_SOURCE_ARTIFACT_ORPHAN_CHARGE_RETENTION` | server + worker | Conservative crash-charge lifetime |
| `MIDNIGHT_SOURCE_ARTIFACT_GC_INTERVAL` | worker | Automatic cleanup cadence             |
| `MIDNIGHT_SOURCE_ARTIFACT_GC_TIMEOUT` | worker | Per-pass cleanup deadline             |
| `MIDNIGHT_SOURCE_ARTIFACT_RETENTION` | worker | Unowned blob minimum age              |
| `MIDNIGHT_SOURCE_ARTIFACT_REFERENCE_RETENTION` | worker | Released-ref minimum age    |
| `MIDNIGHT_SOURCE_ARTIFACT_WORK_RETENTION` | worker | Abandoned work minimum age       |
| `MIDNIGHT_SOURCE_ARTIFACT_QUARANTINE_RETENTION` | worker | Delete grace after quarantine |
| `MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_ITEMS` | worker | Candidate cap per pass            |
| `MIDNIGHT_SOURCE_ARTIFACT_GC_MAX_VERIFY_BYTES` | worker | Hash-byte cap per pass      |
| `MIDNIGHT_CONFIG_HOME`            | env      | Test/CI override for `~/.midnight`       |

Both config files are written atomically via tmpfile + rename so a
crashed CLI never leaves a half-written token or link behind.

The API and build worker must use identical source-artifact quota and janitor
timing values. Explicit malformed or retention-incompatible values fail
startup instead of silently disabling accounting.

## Operator commands

Run `midnight --help` to see the command groups. The legacy PKCE recovery-token exchange is
available only as the explicitly labeled `midnight admin authorize-cli`
compatibility command. Other operator commands share the same `flag` parser,
so explicit `--token`, `--api-base`, and `--json` inputs remain available.
Some legacy-handler leaf commands currently print their complete accepted
flags only in their usage error rather than Cobra's `--help`; correcting that
discoverability gap is tracked as `MID-107`.
