# Operator Install Path

This runbook gets one operator from a clean single-server MVP host to a first Midnight boot that is ready to hand off to the live deploy check.

It covers the package boot path only. It does not prove a workload deploy; use [Live Deploy Check](../runtime/live-deploy-proof.md) after this checklist is green.

## Target Shape

The single-server MVP assumes one disposable Linux VM running:

- Docker Engine for runtime scheduling and local image operations.
- Postgres for `midnight-server` and `midnight-worker` storage.
- Traefik for public HTTP or HTTPS routing through the file provider.
- Midnight `.deb` package assets under `/etc/midnight`, `/var/lib/midnight`, `/var/log/midnight`, `/run/midnight`, and `/usr/lib/systemd/system`.
- Six Midnight units: `midnight-server`, `midnight-worker`,
  `midnight-agent`, `midnight-buildkit`, `midnight-buildkit-worker`, and
  `midnight-registry`.

The package creates the `midnight` system user and group, installs default
config when absent, generates independent cursor, credential-MAC,
variable-intent, and variable-encryption keys when needed, runs available
migrations when a DSN is configured, writes
`/var/lib/midnight/doctor-postinst.json` from the scoped live
`install-readiness` profile, and runs `systemctl daemon-reload`. Warnings are
recorded without failing package configuration; fatal or configuration errors
leave the package for an operator to correct and retry with
`dpkg --configure -a`. Enabling services remains explicit after validation.

## Prerequisites

Prepare the host before installing or enabling Midnight:

- Ubuntu 24.04 or Debian 12 single VM with `systemd`, `journalctl`, and root or sudo access.
- Cgroup v2 unified hierarchy.
- Upstream `docker-ce` or compatible Docker Engine with API v1.43 or newer; the distro `docker.io` package is not supported.
- `/etc/docker/daemon.json` absent or mergeable with the Midnight baseline in [Docker daemon.json Baseline](daemon-json.md).
- Ports 80 and 443 free for Traefik or already reserved by the operator's Traefik service.
- Host firewall mode known and configured to allow the intended public route.
- AppArmor or SELinux state reviewed.
- Clock synchronized and a resolvable FQDN set.
- Postgres reachable from the host, with a database and role prepared for
  Midnight. The role owns the database; Midnight creates and migrates its own
  schema, so no further grants are needed:

  ```sh
  sudo -u postgres createuser --pwprompt midnight
  sudo -u postgres createdb --owner=midnight midnight
  ```

  The DSN written in step 3 is a standard libpq URL, for example
  `postgres://midnight:<password>@127.0.0.1:5432/midnight?sslmode=disable`.
  Use `sslmode=require` or stronger for any non-loopback database.
- Traefik installed by the operator, with static file-provider config pointing at the Midnight dynamic directory.
- Pinned external runtime binaries installed: `buildkitd`, `buildctl`,
  `registry`, and `railpack`. The exact versions and the `curl | tar` commands
  that install them are in [Build Pipeline](../build-pipeline.md); the package
  does not ship them and does not install them for you. `railpack` is easy to
  miss and has no entry in `midnight.env`: the build worker resolves it from
  `PATH` (override with `MIDNIGHT_RAILPACK_BIN`), and without it every build of
  a repository that has no Dockerfile fails.
- Operator shell tools available: `curl`, `jq`, `openssl`, `git`, `make`, `ss`, and `docker`.

Capture the host facts used by doctor before service enablement:

```sh
docker version --format '{{.Server.APIVersion}}' \
  | sudo tee /tmp/midnight-docker-api.txt >/dev/null
ss -ltn | sudo tee /tmp/midnight-ss-ltn.txt >/dev/null
stat -fc %T /sys/fs/cgroup
dpkg-query -W -f='${Package}\n' 'docker*' 2>/dev/null || true
```

Expected minimums: Docker API `1.43` or newer, cgroup output `cgroup2fs`, no `docker.io` package, and no unexpected listener on ports 80 or 443.

## Package And Config Files

Review these package-managed paths before enabling services:

| Path | Owner | Purpose |
| --- | --- | --- |
| `/etc/midnight/midnight.env` | package default, operator editable | Shared `EnvironmentFile` for all Midnight units. |
| `/etc/midnight/db.dsn` | operator secret | Postgres DSN used when `MIDNIGHT_DB_DSN` is not set directly. |
| `/etc/midnight/cursor.key` | package generated if absent | Cursor signing key; must not be shared in evidence. |
| `/etc/midnight/automation-token.key` | package generated if absent | Independent HMAC key for project automation-token verifiers. |
| `/etc/midnight/invitation.key` | package generated if absent | Independent HMAC key for invitation bearer verifiers. |
| `/etc/midnight/variable-intent.key` | package generated if absent | Independent HMAC key for value-bearing variable request fingerprints. |
| `/etc/midnight/kek` | package generated if absent | Exactly 32 raw bytes used to wrap variable DEKs; mounted by API and workers. |
| `/etc/midnight/registry.yml` | package default, operator editable | Local OCI registry wrapper config. |
| `/var/lib/midnight/registry` | `midnight:midnight` | Registry filesystem root; created writable before the first push. |
| `/etc/docker/daemon.json` | operator-owned with Midnight baseline keys | Docker BuildKit and json-file logging baseline. |
| `/etc/midnight/traefik/dynamic` | operator-created or package-managed directory | Traefik file-provider publication root for route artifacts. |
| `/var/log/midnight/traefik/access.jsonl` | `root:midnight` | Shared Traefik write / Midnight read access log; package upgrades preserve contents. |
| `/var/lib/midnight/doctor-postinst.json` | package output | First package-observed doctor report. |
| `/usr/lib/systemd/system/midnight-*.service` | package | Managed service unit files. |

`/etc/midnight/midnight.env` defaults include:

```sh
MIDNIGHT_CONFIG=/etc/midnight/config.yml
MIDNIGHT_STORAGE=postgres
MIDNIGHT_SERVER_ADDR=127.0.0.1:8080
MIDNIGHT_SERVER_URL=http://127.0.0.1:8080
MIDNIGHT_DB_DSN_FILE=/etc/midnight/db.dsn
MIDNIGHT_CURSOR_KEY_PATH=/etc/midnight/cursor.key
MIDNIGHT_AUTOMATION_TOKEN_KEY_PATH=/etc/midnight/automation-token.key
MIDNIGHT_INVITATION_KEY_PATH=/etc/midnight/invitation.key
MIDNIGHT_VARIABLE_INTENT_KEY_PATH=/etc/midnight/variable-intent.key
MIDNIGHT_KEK_PATH=/etc/midnight/kek
MIDNIGHT_REGISTRY_ADDR=127.0.0.1:5000
MIDNIGHT_REGISTRY_BIN=/usr/local/bin/registry
MIDNIGHT_BUILDKIT_ADDR=unix:///run/midnight/buildkit/buildkitd.sock
MIDNIGHT_BUILDCTL=/usr/local/bin/buildctl
MIDNIGHT_BUILDKITD_BIN=/usr/local/bin/buildkitd
MIDNIGHT_BUILDKIT_ROOT=/var/lib/midnight/buildkit
```

For the live deploy handoff, add or confirm the worker route settings in the same file or in a systemd drop-in:

```sh
MIDNIGHT_WORKER_RUNTIME_BACKEND=docker
MIDNIGHT_WORKER_ROUTE_CONFIG_DIR=/etc/midnight/traefik/dynamic
MIDNIGHT_WORKER_ROUTE_GROUP=live-deploy
MIDNIGHT_WORKER_ROUTE_PUBLIC_HOST=app.example.test
MIDNIGHT_WORKER_ROUTE_PUBLIC_PATH=/
MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=false
MIDNIGHT_WORKER_ROUTE_TARGET_PORT=8080

# Route publication is accepted only after Traefik acknowledges the router, so
# this must be Traefik's own API entryPoint and must not collide with
# midnight-server on 127.0.0.1:8080.
MIDNIGHT_WORKER_TRAEFIK_API_URL=http://127.0.0.1:8090   # verify this port is free
MIDNIGHT_WORKER_INGRESS_PROBE_URL=http://127.0.0.1:80
# Empty selects host-ingress mode, which is this install shape. Set a container
# name only when the ingress itself runs as a container.
MIDNIGHT_WORKER_INGRESS_CONTAINER=

# Parent domain for generated hostnames. Ships empty; while it is empty the
# server refuses `midnight domain add` with CAPABILITY_UNAVAILABLE, which is
# the only way a human session obtains a public route. This one belongs to
# midnight-server, not the worker.
MIDNIGHT_APPS_DOMAIN=apps.example.com

# Optional worker-owned ACME lifecycle; disabled unless explicitly enabled.
# Staging is the default. Provider credentials are read from the provider's
# documented environment variables (for example CLOUDFLARE_DNS_API_TOKEN).
MIDNIGHT_WORKER_ACME_ENABLED=false
# MIDNIGHT_WORKER_ACME_EMAIL=ops@example.com
# MIDNIGHT_WORKER_ACME_STATE_DIR=/var/lib/midnight/acme
# MIDNIGHT_WORKER_ACME_DNS_PROVIDER=cloudflare
# MIDNIGHT_WORKER_ACME_ENVIRONMENT=staging
# MIDNIGHT_WORKER_ACME_PRODUCTION_ACK=false
```

After the first boot, an installation administrator can configure the same
namespace from **Instance settings → Custom domain**. Enter the root domain
(for example `example.com`) and the public ingress target. Midnight derives
`midnight.example.com` for the console and `*.apps.example.com` for generated
service hostnames, then shows the DNS records to add. A saved change becomes
the startup authority on the next `midnight-server` restart; the settings page
shows when that restart is still required. Once DNS reaches the installation,
the exact configured console hostname serves a public "This domain runs on
Midnight" page at `/` with a link to `deployatmidnight.com`. The administrator
console remains available at `/login` and on its existing deep links.

Saving a new instance apps domain changes the authority used for future
generated hostnames. It does not rename generated domain rows that already
exist. Move an existing service deliberately with the
[custom-domain cutover runbook](../runbooks/custom-domain-cutover.md), including
ownership verification, binding and backend readiness, and an external probe.

An operator-managed edge may terminate public TLS before forwarding the
original Host to Midnight's HTTP ingress. In that topology leave the Midnight
route's TLS intent disabled: the edge owns the certificate and HTTPS evidence,
while Midnight owns the HTTP route and backend evidence. Forward the original
Host and standard `X-Forwarded-*` headers. If a portal or controller generates
the edge configuration, update its persistent state instead of editing its
generated Caddyfile or equivalent output.

If proving HTTPS through Midnight itself, set
`MIDNIGHT_WORKER_ROUTE_PUBLIC_TLS=true` and
`MIDNIGHT_WORKER_ROUTE_CERT_RESOLVER` to the Traefik resolver name used by the
static config.

## Systemd Services

The Midnight package installs these units:

| Unit | Runs as | Role |
| --- | --- | --- |
| `midnight-server.service` | `midnight` | Control-plane API server on `MIDNIGHT_SERVER_ADDR`. |
| `midnight-worker.service` | `midnight` | Queue worker and deploy executor. |
| `midnight-agent.service` | `root` | Host-local runtime agent that talks to Docker. |
| `midnight-buildkit.service` | `root` | BuildKit wrapper using the pinned `buildkitd` binary. |
| `midnight-buildkit-worker.service` | `midnight` | Build queue worker that checks out sources and submits BuildKit solves. |
| `midnight-registry.service` | `midnight` | Local OCI registry wrapper on `127.0.0.1:5000`. |

All six units read `EnvironmentFile=-/etc/midnight/midnight.env`, use
`Restart=on-failure`, keep mutable state under `/var/lib/midnight`, logs under
`/var/log/midnight`, and runtime files under `/run/midnight`.

Treat `docker.service`, `containerd.service`, `postgresql.service` or the operator's managed Postgres endpoint, and `traefik.service` as host prerequisites. Do not rely on systemd ordering alone as proof of readiness; run doctor and the validation commands below.

## Docker, Traefik, And Postgres Assumptions

Docker:

- Docker Engine must be reachable by the agent and worker on the local host.
- Docker API must satisfy `OR-CHK-DOC-003`.
- `/etc/docker/daemon.json` must accept the Midnight baseline merge. If the merge changes the file, plan a Docker restart before enabling Midnight.
- Tenant logs use Docker `json-file`; platform service logs use journald.

Traefik:

- Traefik is operator-installed and owns public ports 80 and 443.
- Static config must set `providers.file.directory` to the same path as
  `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR`, with `watch: true`.
- The route publication root must be absolute and writable by the `midnight` service user.
- Artifacts are **flat files directly in that directory**, named
  `<route-group>[.<route>].traefik.dynamic.yaml`. Do not point Traefik at a
  parent directory and expect it to find them one level down: Traefik loads
  files from subdirectories when it starts, but it only *watches* the directory
  named in `providers.file.directory`. An artifact one level down is read once
  at Traefik start and never again, so later route changes are written, hash
  verified, and never observed — publication then fails its acknowledgement
  check and the deployment is rolled back. Earlier releases published into
  `<route-group>/traefik.dynamic.yaml`; the worker removes that file when it
  republishes, so a Traefik restart cannot resurrect a stale router.
- **Traefik must expose its API to the worker.** Route publication is only
  accepted once Traefik itself reports the router active, which the worker
  reads from Traefik's API at `MIDNIGHT_WORKER_TRAEFIK_API_URL`. Give Traefik
  an `api:` section and an entryPoint bound to loopback, and point that variable
  at it. The address **must not** be `127.0.0.1:8080` — that is
  `midnight-server`, so every verification probes the control plane and fails
  with a 404 that names neither service. The packaged default is
  `127.0.0.1:8090`; treat it as a placeholder, not a safe value. Confirm the
  port is free before you commit to it, because a taken port does not degrade
  gracefully — Traefik refuses to start at all with `error opening listener:
  ... bind: address already in use`, which takes public ingress down with it:

  ```sh
  ss -ltn 'sport = :8090'   # any output means pick another port
  ```

  A published container that maps a loopback port is the usual squatter, and
  `docker-proxy` will not appear in a plain `ss -ltn` without `-p`.
- `MIDNIGHT_WORKER_INGRESS_PROBE_URL` is the trusted ingress address the worker
  probes with the route's `Host` header (normally `http://127.0.0.1:80`).
- `MIDNIGHT_WORKER_INGRESS_CONTAINER` selects how the ingress reaches
  workloads. Leave it **empty** for this install shape: Traefik runs as a host
  service and already reaches container addresses directly, so there is nothing
  to attach to the per-environment Docker bridges. Set it to a container name
  only when the ingress itself runs as a container, in which case the worker
  attaches that container to each environment bridge. A name that does not
  resolve to a running container fails every publish with
  `inspect trusted ingress container: No such container`.
- Do not include rendered Traefik YAML in shareable evidence.

A minimal static config that satisfies the above:

```yaml
entryPoints:
  web:
    address: ":80"
  websecure:
    address: ":443"
  traefik:
    address: "127.0.0.1:8090"   # must match MIDNIGHT_WORKER_TRAEFIK_API_URL, and be free

api:
  dashboard: false
  insecure: true                # loopback-only entryPoint above

providers:
  file:
    directory: /etc/midnight/traefik/dynamic
    watch: true
```

Worker runtime knobs (defaults are correct for this install shape; change them
only with a reason):

- `MIDNIGHT_WORKER_DEPLOY_CONCURRENCY` (default `4`) bounds how many deploy
  jobs run at once. Jobs for the same service still run one at a time in lease
  order. Setting it to `1` restores the old serial behaviour, in which one
  rollout waiting on readiness also stalls every other service's deploy, lease
  reclamation, and monitor evaluation.
- `MIDNIGHT_WORKER_DOCKER_BASELINE` (default `enforce`) refuses to start when
  `/etc/docker/daemon.json` drifts from the locked baseline. `warn` downgrades
  those conflicts to log lines and exists for developer engines such as Docker
  Desktop, which cannot satisfy the baseline. Do not set `warn` on an installed
  host; cgroup v2 and the Engine API floor are enforced either way.
- `MIDNIGHT_WORKER_PROBE_CONTAINER` names the container the worker itself runs
  in, so it can be attached to each environment bridge and reach workloads.
  Leave it empty here: the packaged worker is a host service and already has
  that reachability.
- `MIDNIGHT_WORKER_TRUSTED_IMAGES` and the cosign settings remain the operator's
  image-trust policy. The digests of the built-in database templates are trusted
  by construction whenever a policy is active, so `midnight template deploy
  postgres` no longer needs those four digests restated by hand.

Postgres:

- `MIDNIGHT_STORAGE=postgres` is expected for package boot.
- Put the DSN in `/etc/midnight/db.dsn` with restrictive permissions or provide `MIDNIGHT_DB_DSN` through a protected systemd environment source.
- `midnight-server` and `midnight-worker` must both use the same reachable database.
- Do not paste raw DSNs into tickets, release evidence, or live deploy artifacts.
- `MIDNIGHT_STORAGE=auto` **refuses to start** when no DSN can be resolved. A
  bad mount or a missing `/etc/midnight/db.dsn` is a failed unit with an
  explanatory message, not a server that comes up "healthy" on storage that
  disappears on restart. The refusal names the three places it looked
  (`--postgres-dsn`/`$MIDNIGHT_DB_DSN`, `$MIDNIGHT_DB_DSN_FILE`, and the
  configured DSN file path).
- The ephemeral in-memory platform is opt-in only: `--storage=memory`, or
  `MIDNIGHT_ALLOW_MEMORY_FALLBACK=1` (`--allow-memory-fallback`) to let `auto`
  degrade. Either way the server prints a multi-line `WARNING:` banner on
  stderr stating that data is ephemeral and that deployments, rollback/restart
  actions, the observability store, tenant human-write coordinators, GitHub
  linking, template forks and the deploy outbox are disabled. Never set it on a
  real install.

Console:

- The console is compiled into `midnight-server`; the package ships a copy at
  `/usr/share/midnight/web-next` but `MIDNIGHT_WEB_DIST_DIR` is commented out in
  `/etc/midnight/midnight.env` by default. See [console.md](console.md).

## First Boot Procedure

Run commands as root or through `sudo` on the target VM.

1. Install host dependencies and verify Docker/Postgres/Traefik are present:

   ```sh
   systemctl --no-pager --full status docker containerd postgresql traefik
   docker version --format '{{json .Server}}'
   ```

   If Postgres is managed outside systemd, replace the `postgresql` status check with the operator-approved connectivity check for that service.

2. Install the Midnight package:

   ```sh
   sudo apt-get update
   sudo apt-get install ./midnight_<version>_<arch>.deb
   ```

   If using `dpkg -i`, follow with `sudo apt-get -f install` before continuing.

   **Order matters.** `postinst` runs the `install-readiness` doctor profile and
   applies migrations, and both need the DSN. On a host where
   `/etc/midnight/db.dsn` does not exist yet, package configuration ends with
   `OR-CHK-DOC-010 database DSN file is missing` and dpkg leaves the package
   unconfigured — this is the documented "correct and retry" path, not a broken
   package. Either create the DSN before installing:

   ```sh
   sudo install -d -m 0755 /etc/midnight
   sudo install -o root -g root -m 0600 /dev/null /etc/midnight/db.dsn
   sudoedit /etc/midnight/db.dsn
   ```

   or install first, complete step 3, and then finish configuration:

   ```sh
   sudo dpkg --configure -a
   ```

   The `midnight` user does not exist until the package is installed, so a
   pre-created DSN is necessarily root-owned; installation hands it to
   `midnight:midnight` with mode 0600. If you are installing a package older
   than this note, chown it yourself — the units run as `midnight` and a
   root-owned DSN fails every one of them at boot with `read dsn file
   /etc/midnight/db.dsn: permission denied` on a host that otherwise looks
   correctly installed.

   Either way, confirm the migration line appears —
   `midnight: applied N migration(s), schema_version N`. Nothing else in this
   runbook applies migrations, and `midnight-server` does not apply them at
   startup, so a package left unconfigured means an empty database.

3. Write the Postgres DSN without exposing it in shell history:

   ```sh
   sudo install -o midnight -g midnight -m 0600 /dev/null /etc/midnight/db.dsn
   sudoedit /etc/midnight/db.dsn
   ```

4. Confirm external runtime binaries match `/etc/midnight/midnight.env`:

   ```sh
   test -x /usr/local/bin/buildkitd
   test -x /usr/local/bin/buildctl
   test -x /usr/local/bin/registry
   sudo test -s /etc/midnight/registry.yml
   sudo test -s /etc/midnight/cursor.key
   sudo test -s /etc/midnight/automation-token.key
   sudo test -s /etc/midnight/invitation.key
   sudo test -s /etc/midnight/variable-intent.key
   sudo test -s /etc/midnight/kek
   ```

5. Prepare Traefik publication:

   ```sh
   sudo install -d -o midnight -g midnight -m 0750 /etc/midnight/traefik/dynamic
   sudo -u midnight test -w /etc/midnight/traefik/dynamic
   ```

   Confirm Traefik static config points at this directory before starting the live deploy check.

   **Then restart Traefik**, even though its config has not changed:

   ```sh
   sudo systemctl restart traefik
   curl -fsS "$MIDNIGHT_WORKER_TRAEFIK_API_URL/api/http/routers" \
     | grep -q '@file' && echo 'file provider is live'
   ```

   Traefik's file-provider watcher holds the directory it was started with. This
   step creates that directory, and the prerequisites started Traefik before it
   existed, so on a fresh install the watcher is attached to nothing: artifacts
   are written correctly and Traefik never sees them. The symptom is a deploy
   that fails `ROUTE_PUBLICATION_FAILED: ... Traefik has not acknowledged the
   desired route revision` while the artifact sits on disk looking perfect. The
   same applies any time the directory is deleted and recreated — replacing the
   directory is not the same as replacing a file in it.

6. Review and edit `/etc/midnight/midnight.env`:

   ```sh
   sudoedit /etc/midnight/midnight.env
   sudo systemctl daemon-reload
   ```

   Set the live deploy route variables only when this host is intended to run the live deploy check.

7. Review the package-observed doctor output:

   ```sh
   sudo test -s /var/lib/midnight/doctor-postinst.json
   sudo jq '{live, profile, exit_code, results: [.results[] | {id, status, severity, message}]}' \
     /var/lib/midnight/doctor-postinst.json
   ```

8. Run explicit operator doctor with captured host facts:

   ```sh
   sudo midnight-doctor --json --live \
     --mountinfo-fixture=/proc/self/mountinfo \
     --daemon-json-fixture=/etc/docker/daemon.json \
     --docker-version-fixture=/tmp/midnight-docker-api.txt \
     --ss-ltn-fixture=/tmp/midnight-ss-ltn.txt \
     --ingress-reserved-by=traefik \
     --firewall-mode=ufw \
     --mac-mode=apparmor-enforce \
     --time-sync=synchronized \
     --hostname-fqdn="$(hostname -f)" \
     --docker-packages=docker-ce \
     | sudo tee /var/lib/midnight/doctor-pre-enable.json
   ```

   Adjust the firewall, MAC, time-sync, hostname, and Docker package hints to
   match the host. `--ingress-reserved-by` is required on this install shape:
   the prerequisites put an operator-installed Traefik on ports 80 and 443, and
   without the declaration `OR-CHK-DOC-004` reports them as a fatal conflict, so
   a correctly prepared host can never clear the gate. Name the service that
   owns them; any listener you have not claimed still fails the check. Any
   `status="fail"` blocks service enablement. Any
   `status="skip"` means the selected profile lacked evidence for that check;
   checks absent from `results` were outside the named `profile` and did not
   run.

9. Run the narrow live deploy readiness slice when this host will continue to the live deploy check:

   ```sh
   sudo midnight-doctor --json --live-deploy-readiness --live \
     --db-dsn-fixture=/etc/midnight/db.dsn \
     --worker-runtime-backend=docker \
     --route-config-dir=/etc/midnight/traefik/dynamic \
     --route-public-host=app.example.test \
     --route-public-path=/ \
     --route-target-port=8080 \
     | sudo tee /var/lib/midnight/doctor-live-deploy-ready.json
   ```

10. Enable Midnight services:

    ```sh
    sudo systemctl enable --now \
      midnight-server midnight-worker midnight-agent midnight-buildkit \
      midnight-buildkit-worker midnight-registry
    ```

11. Capture first-boot status:

    ```sh
    sudo systemctl --no-pager --full status \
      midnight-server midnight-worker midnight-agent midnight-buildkit \
      midnight-buildkit-worker midnight-registry \
      | sudo tee /var/lib/midnight/systemd-first-boot.txt
    sudo journalctl \
      -u midnight-server -u midnight-worker -u midnight-agent \
      -u midnight-buildkit -u midnight-buildkit-worker -u midnight-registry \
      --since "10 minutes ago" --no-pager \
      | sudo tee /var/lib/midnight/journal-first-boot.log
    curl -fsS http://127.0.0.1:8080/api/v1alpha/compat \
      | tee /tmp/midnight-compat.json
    ```

Expected first-boot result:

- All six Midnight units are active or have a clear operator-owned reason for being deferred.
- `midnight-server` is using Postgres storage, not memory storage. Its startup
  line reads `(storage=postgres:file, console=embedded)` — `:file` records that
  the DSN came from `MIDNIGHT_DB_DSN_FILE`; a `storage=memory` line is
  preceded by a block of `WARNING:` lines and means the install is not durable.
- `midnight-worker` starts with Docker runtime settings when the live deploy check is in scope.
- `midnight-agent` can reach Docker.
- `midnight-buildkit`, `midnight-buildkit-worker`, and `midnight-registry`
  resolve their pinned binaries and start their expected daemon/queue roles.
- `GET /api/v1alpha/compat` returns `200`.
- `midnight-worker` reports its ingress mode on its startup line: `ingress=host`
  for a host-installed Traefik, or `ingress=container:<name>` when the ingress
  runs as a container.

### Create the first administrator

The installation ships with no human account. Until one exists the only
credential is the installation-wide operator recovery token, which deliberately
cannot connect a Git source, so nothing can be deployed from a repository.

Create the administrator once, on the host:

```sh
sudo sh -c 'set -a; . /etc/midnight/midnight.env; set +a; \
  MIDNIGHT_API_BASE=http://127.0.0.1:8080 \
  MIDNIGHT_API_TOKEN="$MIDNIGHT_API_BEARER_TOKEN" \
  midnight admin create-user \
    --email ops@example.com \
    --display-name "Ops" \
    --workspace-slug default \
    --workspace-name "Default"'
```

Sourcing the environment file in place keeps `MIDNIGHT_API_BEARER_TOKEN` out of
your shell history and off the terminal; do not echo it.

It prompts for a password (minimum 12 characters), generates the authenticator
secret, and prints an `otpauth://` enrolment URL plus the raw secret **once**.
Record them before closing the terminal. The command is one-shot: a second run
reports that the workspace is already claimed and exits non-zero.

Operators who prefer the console can instead open `/login`, expand **Emergency
recovery**, and enter the same token: the console probes the claim status and,
on an unclaimed installation, redirects to `/setup` to create the account there
rather than dropping into an empty workspace.

### Recover a lost administrator password

`create-user` is one-shot, so it cannot be rerun to fix a password that was
lost or mistyped. Recover the account with the same operator recovery token:

```sh
sudo sh -c 'set -a; . /etc/midnight/midnight.env; set +a; \
  MIDNIGHT_API_BASE=http://127.0.0.1:8080 \
  MIDNIGHT_API_TOKEN="$MIDNIGHT_API_BEARER_TOKEN" \
  midnight admin reset-password --email ops@example.com'
```

It prompts for the new password (same 12-character minimum), enrols a
**replacement** authenticator, and prints the new `otpauth://` URL and secret
once — the previous authenticator entry stops working, so replace it rather
than adding a second one. Add `--keep-totp` to change only the password and
keep the authenticator the account already has; that form is refused if the
account has no enrolment, since it would leave an administrator on one factor.

Every live session for the account is revoked by the reset, so anyone signed in
as that administrator — including a stolen session — is signed out.

The command accepts only the operator recovery token. A signed-in
administrator session is refused with `AUTH_OPERATOR_RECOVERY_REQUIRED`: no
account can rewrite another account's password and second factor. The target
must also be an enabled, undeleted administrator; a disabled or deleted account
reports `RESOURCE_NOT_FOUND` rather than being brought back.

Operators who prefer the console can open `/login`, expand **Emergency
recovery**, paste the same token, and use **Reset an administrator password**
there. It runs the same route with the same rules, generates the replacement
authenticator in the browser, and shows the enrolment URL and secret once after
the reset lands. The token typed into that panel is used directly, so a locked
out operator does not have to enter recovery mode first.


## Validation Checklist

Record these before handing the VM to the live deploy check:

- `docker version --format '{{json .Server}}'` shows a supported server API.
- `stat -fc %T /sys/fs/cgroup` returns `cgroup2fs`.
- `sudo midnight-doctor --json --live ...` has no fatal findings.
- `sudo midnight-doctor --json --live-deploy-readiness --live ...` has no fatal findings when the live deploy check is next.
- `/etc/midnight/midnight.env` contains the intended storage, binary, and route values.
- `/etc/midnight/db.dsn` exists, is readable by services, and is not copied into evidence.
- `/etc/midnight/cursor.key`, `/etc/midnight/automation-token.key`,
  `/etc/midnight/invitation.key`, `/etc/midnight/variable-intent.key`, and
  `/etc/midnight/kek` exist and are not copied into evidence.
- `/etc/midnight/registry.yml` exists.
- `/var/lib/midnight/registry` exists and is writable by the `midnight` registry unit.
- `/etc/midnight/traefik/dynamic` exists, is absolute, and is writable by the `midnight` user.
- `/var/log/midnight/traefik/access.jsonl` is group-readable by `midnight`.
- Traefik static config points at `/etc/midnight/traefik/dynamic` with `watch: true`.
- `midnight-doctor` reports `OR-CHK-DOC-004` as a pass naming your ingress, not
  a fatal port conflict; a failure there means `--ingress-reserved-by` was omitted.
- `curl -fsS "$MIDNIGHT_WORKER_TRAEFIK_API_URL/api/http/routers"` returns Traefik's
  router JSON — not the Midnight console and not a 404. If it returns HTML, the
  variable is pointing at the wrong service.
- `MIDNIGHT_WORKER_INGRESS_CONTAINER` is empty for a host-installed Traefik, or
  names a container that `docker inspect` resolves.
- `curl -fsS "$MIDNIGHT_WORKER_TRAEFIK_API_URL/api/http/routers"` lists at least
  one `@file` router. If the file provider contributes nothing, Traefik is not
  watching the publication directory and no route will ever be acknowledged.
- `MIDNIGHT_APPS_DOMAIN` is set if this installation should hand out generated
  hostnames. It ships empty, and while it is empty `midnight domain add` — the
  only way a human session gets a public route — refuses with
  `CAPABILITY_UNAVAILABLE: generated domains require the server apps domain to
  be configured`.
- `systemctl status` is clean for Midnight units plus Docker, Postgres, and Traefik or the operator-approved equivalents.
- `journalctl` for the first boot shows no file-provider, DB connection, Docker socket, or external binary resolution errors.
- `curl -fsS http://127.0.0.1:8080/api/v1alpha/compat` succeeds.

## Handoff To Live Deploy

After this runbook passes, continue with [Live Deploy Check](../runtime/live-deploy-proof.md).

Before switching runbooks, gather or confirm:

- Repository commit and clean or explained worktree state for the deploy check.
- Public DNS name resolving to the VM.
- Candidate image pushed to a registry reachable from the VM, referenced by immutable digest.
- Route variables in `/etc/midnight/midnight.env` or equivalent drop-in.
- Deployment evidence directory for the run.
- Redaction rule: do not share raw DSNs, credentials, private keys, Docker socket paths, rendered Traefik YAML, or host-absolute paths.

The live deploy runbook starts by rerunning the local release gate and capturing live evidence. Do not skip those steps because this install path passed.
