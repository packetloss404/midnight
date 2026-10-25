# MVP Acceptance

This document is the acceptance path for deciding whether Midnight is MVP-ready. It is intentionally command-first: every checkpoint either has an exact local command, an exact VM command, or an owned outside-scope diagnostic with rationale.

The current implementation and dated installed evidence are summarized in
[`deployment-readiness.md`](deployment-readiness.md). The
[feature verification ledger](feature-verification.md) separates browser/API
coverage from runtime and external-provider acceptance.
The September 5 records prove fresh schema-71 installation and a real source
deployment served through Traefik over HTTP. They do not complete the HTTPS,
restart/rollback, audit, and recovery criteria below or attest to today's host.

MVP means a design partner can install Midnight on one trusted VM, deploy and operate one real app with HTTPS, inspect deploys, services, routes, hosts, logs, audit evidence, backups, and recovery states through the dashboard/CLI/API, and complete the checks below without fake or demo-only data in the primary product path.

The target remains a single trusted VM. Multi-host scheduling, enterprise tenancy, external billing, and broad marketplace breadth are not MVP acceptance requirements.

## Next Acceptance Slice: OSS Railway Happy Path

The next acceptance slice is the smallest open-source Railway-like path that can be run on a fresh supported VM and shown to a design partner: package install, one real app deploy, one HTTPS route, one official Postgres template, one audit trail, one real off-host backup/restore drill, and typed recovery states. It is not accepted while the product only reaches queued draft/job state, memory-mode diagnostics, synthetic failures, or dashboard-only fixture data.

Run the slice as these runner modes, keeping the redacted evidence directory from each mode:

```sh
bash scripts/mvp-acceptance.sh local-gate
bash scripts/mvp-acceptance.sh surface-agreement
bash scripts/mvp-acceptance.sh package-template
bash scripts/mvp-acceptance.sh human-domain
bash scripts/mvp-acceptance.sh real-app
bash scripts/mvp-acceptance.sh audit
bash scripts/mvp-acceptance.sh backup
```

The review findings map to these positive assertions:

| Finding to close | Next positive assertion |
| --- | --- |
| Official Postgres template runtime | The canonical REST saga creates durable service/config/encrypted-variable/volume/deployment/outbox truth. Success requires a private `psql` write, container restart, marker read from the same volume, and probe cleanup. |
| Source-to-HTTP deployment is recorded; HTTPS and restart acceptance remain. | A digest-pinned app reaches healthy runtime inventory and remains reachable over HTTPS after `midnight-server` restart. |
| Failure visibility still allows synthetic proof. | A real failed deploy, host degradation, and backup/restore failure each emit typed actionable reason codes. |
| Dashboard/API evidence can diverge from operator reality. | `surface-agreement` checks API/CLI/projection agreement using test inputs; real API and PostgreSQL browser lanes exercise user workflows separately. Installed inventory, real logs, and recovery evidence still require the VM checks. |
| Memory-mode blockers are easy to mistake for acceptance. | `memory-blockers` records owned diagnostics only; package/VM runner modes provide the passing assertions. |

## OSS Railway Readiness Checklist

- Fresh VM package install, doctor, migrations, service boot, and Postgres-backed storage are proven with package-managed configuration.
- One real app image pinned by immutable digest deploys through the public API and reaches healthy runtime state.
- Public HTTPS routing works for the deployed app, including certificate metadata evidence and no private key material in artifacts.
- Dashboard, API, and CLI surfaces show the same deploy, service, route, host, logs, audit, backup, and recovery state.
- Official Postgres template creation includes an attached persistent volume,
  generated sealed credentials, private networking, real TCP/database
  readiness, write/restart/read persistence evidence, and probe cleanup without
  exposing plaintext secrets. It does not imply managed HA or off-host backup.
- Backup and restore checks create or report real configured destination state; missing destination remains a typed degraded state, not a pass.
- Failed deploy, host degradation, and backup/restore failure states carry actionable reason codes operators can use.
- Primary product paths contain no fake/demo-only data; unavailable actions are disabled or return real reason codes.

## Acceptance Runner

Use `bash scripts/mvp-acceptance.sh` as the single current runner for the checks in this document. It writes evidence under `artifacts/mvp-acceptance/<run-id>` by default. `memory-blockers` writes `blockers.tsv` as an owned diagnostic ledger; it is not a passing MVP criterion.

```sh
bash scripts/mvp-acceptance.sh local-gate
bash scripts/mvp-acceptance.sh local-smoke
bash scripts/mvp-acceptance.sh surface-agreement
bash scripts/mvp-acceptance.sh memory-blockers
bash scripts/mvp-acceptance.sh local
```

For package and VM checks, set the target API/evidence variables and run the specific mode that matches the section:

```sh
export MIDNIGHT_API_BASE="http://127.0.0.1:8080"
export MVP_RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
export MVP_EVIDENCE_DIR="artifacts/mvp-acceptance/${MVP_RUN_ID}"

bash scripts/mvp-acceptance.sh package-template
bash scripts/mvp-acceptance.sh real-app
bash scripts/mvp-acceptance.sh audit
bash scripts/mvp-acceptance.sh backup
```

The manual commands below are the canonical checklist behind the runner. If the runner and this document diverge, update both in the same change.

## Required Tools

Local no-secret checks require:

```sh
make --version
go version
npm --version
curl --version
jq --version
```

VM acceptance additionally requires:

```sh
docker version
systemctl --version
openssl version
```

## Local Bootstrap Gate

Run these from the repository root before touching a VM. They prove the current checkout can build, serve the dashboard/API, and expose known incomplete product states as machine-readable diagnostics.

```sh
make bootstrap
make build
make test
go test ./tools/contract-tests
go run ./tools/contract-tests
npm --prefix web-next ci
npm --prefix web-next run typecheck
npm --prefix web-next test
npm --prefix web-next run build
make release-check
```

Expected result: every command exits `0`. If `make release-check` writes evidence to its default artifact directory, keep those files with the acceptance run.

### Disposable integration, browser, and runtime lanes

Run the additional lanes before promoting a candidate:

```sh
npm --prefix web-next run test:e2e:install
npm --prefix web-next run test:gate0
(cd web-next && npx playwright test --config playwright.postgres.config.ts)
bash scripts/ci/integration-gate.sh
bash scripts/ci/live-runtime-gate.sh
```

The default browser lane mixes explicitly labelled fixture tests with actual
memory-API HTTP journeys. The separate PostgreSQL browser lane uses a fresh
disposable database and real enrollment/TOTP/sessions, project/service
creation, variable/runtime changeset review and apply, stale-version
rejection, secret metadata checks, and logout revocation. It requires
Linux/macOS key-file permissions; its startup script may create its own
PostgreSQL Docker container. It never targets the installed production DB.

The integration gate builds an exact-source MinIO fixture and requires
actual durable boot, source materialization, Docker volume/one-off, and S3
credential/storage canaries. It records optional skips. The Linux live
runtime gate separately builds and pushes a minimal image through real
BuildKit/registry, then checks Traefik HTTP bodies before and after backend
replacement plus an unknown-host 404. The image-build fixture and Traefik
test backends are independent; this is not an installed source-to-public-app
proof. See the [verification ledger](feature-verification.md) and
[CI guide](contributing/ci.md) for prerequisites and evidence paths.

Each lane's pass is determined by its actual retained run. These mechanisms
do not establish external AI/GitHub/DNS credentials, public HTTPS, package
installation, complete worker promotion, or off-host destruction/restore.
Those remain explicit checks below.

## Local Dashboard/API Smoke Path

Start the API and dashboard in memory mode. This path needs no external secrets and is intentionally not sufficient for MVP by itself.

```sh
export MIDNIGHT_API_BASE="http://127.0.0.1:8080"
go run ./cmd/midnight-server serve --storage=memory --listen 127.0.0.1:8080
```

In another shell, run the checks:

```sh
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/compat" | jq -e '.api_version == "v1alpha"'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/summary" | jq -e '.status == "ready"'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/template-marketplace" | jq -e '.cards | length >= 1'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" | tee /tmp/midnight-runtime-inventory.json
jq -e '(.services | length) == 0 and (.routes | length) == 0 and (.hosts | length) == 0' /tmp/midnight-runtime-inventory.json
curl -fsS "$MIDNIGHT_API_BASE/dashboard/" >/tmp/midnight-dashboard.html
test -s /tmp/midnight-dashboard.html
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/schema-evidence" | jq -e '.status == "ready"'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/migration-plan" | jq -e '.status == "ready" or .status == "needs-review" or .status == "blocked"'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/release-evidence" | jq -e '.issue_codes | type == "array"'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/evidence-index" | jq -e '(.references | length) == 4'
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/_demo/api-data-readiness" | jq -e '.openapi.available == true'
```

Expected result: all commands exit `0`, and the dashboard opens at `http://127.0.0.1:8080/dashboard/`.

## Surface Agreement Check

Run the executable agreement check against a local memory server or an existing `MIDNIGHT_API_BASE`:

```sh
bash scripts/mvp-acceptance.sh surface-agreement
```

Expected result: all commands exit `0`. The runner creates one project and
service and verifies the same IDs through API and CLI JSON output. It submits
a test agent registration and route observation, creates a `_demo` operation,
and checks audit/export, restore-status, and generated evidence shapes. These
are synthetic runtime inputs sent to a real API. This mode does not launch a
node agent, container, or ingress, read real application logs, or use a browser;
it proves API/CLI/projection agreement only. Browser workflows are a separate
gate described in [feature verification](feature-verification.md).

## Human-Session Generated Domain Check

This check uses the same human-session-only contract as the console. It creates
or converges the server-managed singleton, waits for current trusted-ingress
evidence, then sends an HTTP request through the supplied ingress address with
the generated hostname in the `Host` header. It does not delete the domain.

```sh
export MIDNIGHT_API_BASE="https://app.example.com"
export MIDNIGHT_HUMAN_SESSION_TOKEN="midn_user_..."
export MVP_PROJECT_ID="prj_..."
export MVP_SERVICE_ID="svc_..."
export MVP_ENVIRONMENT_ID="env_..."
export MVP_INGRESS_URL="http://192.0.2.10"

bash scripts/mvp-acceptance.sh human-domain
```

Expected result: the command exits `0` and writes
`generated-domain-acceptance.json`. The token is used only as an authorization
header and is never written to evidence. `route_reachable` proves Midnight's
current Traefik acknowledgement and trusted ingress probe; the final explicit
Host-header request proves this runner can reach the selected ingress address.

## Package Template Deploy And Inventory Check

After the package/Postgres API and Docker worker are running, use the canonical
REST saga (the legacy dashboard deploy route is intentionally unavailable):

```sh
curl -fsS \
  -H "Idempotency-Key: mvp-template-project" \
  -H "Content-Type: application/json" \
  --data '{"workspace_id":"wrk_default","slug":"mvp-template","display_name":"MVP Template"}' \
  "$MIDNIGHT_API_BASE/api/v1alpha/projects" \
  | tee "$MVP_EVIDENCE_DIR/template-project.json"
export MVP_PROJECT_ID="$(jq -r '.id' "$MVP_EVIDENCE_DIR/template-project.json")"

curl -fsS \
  -H "Idempotency-Key: mvp-template-postgres" \
  -H "Content-Type: application/json" \
  --data '{"project_id":"'"$MVP_PROJECT_ID"'","name":"Postgres 16 MVP"}' \
  "$MIDNIGHT_API_BASE/api/v1alpha/templates/postgres/instances" \
  | tee "$MVP_EVIDENCE_DIR/template-deploy.json"

jq -e '
  .template_slug == "postgres" and
  .state == "queued" and
  (.provision_run_id | length > 0) and
  (.operation_id | length > 0) and
  (.deployment_id | length > 0) and
  (.created_services | length) == 1 and
  (.created_volumes | length) == 1
' "$MVP_EVIDENCE_DIR/template-deploy.json"

export MVP_TEMPLATE_STATUS_URL="$(jq -r '.status_url' "$MVP_EVIDENCE_DIR/template-deploy.json")"
for _ in $(seq 1 120); do
  curl -fsS "$MIDNIGHT_API_BASE$MVP_TEMPLATE_STATUS_URL" \
    | tee "$MVP_EVIDENCE_DIR/template-status.json"
  state="$(jq -r '.state' "$MVP_EVIDENCE_DIR/template-status.json")"
  [ "$state" = succeeded ] && break
  [ "$state" = failed ] && exit 1
  sleep 1
done
jq -e '
  .state == "succeeded" and
  .connectivity_verified == true and
  .persistence_verified == true and
  .cleanup_completed == true
' "$MVP_EVIDENCE_DIR/template-status.json"

if grep -E 'postgres://[^[:space:]]+:[^*@[:space:]]+@|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|MIDNIGHT_.*SECRET=' "$MVP_EVIDENCE_DIR/template-deploy.json" "$MVP_EVIDENCE_DIR/template-status.json"; then
  echo "secret-like material found in template evidence"
  exit 1
fi
```

Expected result: all commands exit `0`. This proves real PostgreSQL worker
execution, private connectivity, and persistence across a container restart,
with metadata-only evidence and probe cleanup. It does not prove managed HA,
public TCP reachability, an off-host backup, or destructive restore execution.

## Outside-Scope Diagnostics

Run diagnostics only when you need to explain why a local memory-mode smoke server is not a full MVP pass:

```sh
bash scripts/mvp-acceptance.sh memory-blockers
```

The runner writes `blockers.tsv` with `state`, `name`, `owner`, and `rationale`. These rows are not passing acceptance criteria; they keep local-only gaps explicit while the package/VM runner modes provide positive MVP assertions.

| Diagnostic | Owner | Rationale |
| --- | --- | --- |
| Canonical memory-mode template deploy has no durable provisioner and returns `503 CAPABILITY_UNAVAILABLE`. | control-plane-api | Memory mode is diagnostic; MVP package mode must use Postgres and the Docker worker and pass `package-template`. |
| No generated route exists before runtime reports one. | runtime-agent | Local smoke does not run Docker or Traefik; `surface-agreement` records a route observation and `real-app` must prove HTTPS. |
| No runtime host is registered before agent registration. | runtime-agent | Local smoke starts only the API; `surface-agreement` registers an agent and checks host inventory. |
| Backup run API has no configured destination in unconfigured memory mode. | operator-config | `backup` requires a real configured destination; missing config must remain the typed degraded state `BACKUP_OFFHOST_MISSING`. |
| Managed-database HA, failover, and administration UI are absent. | database-template-runtime | PostgreSQL, Redis durable, MySQL, and MongoDB single-node have self-managed runtime sagas; none claims managed HA, and engine backup/restore remains a separate acceptance gate. |

## Package Install Acceptance

MVP acceptance must include a package install/update path on a fresh supported VM, not only dev-server mode.

Build or obtain the package:

```sh
make package-deb
ls -lh build/deb/midnight_*.deb \
  build/deb/midnight-dbgsym_* \
  build/deb/midnight_*.changes \
  build/deb/midnight_*.buildinfo
```

The package target accepts only a pristine detached worktree at the reviewed
source commit. It also refuses untracked or ignored `web-next/.env*` files and
every inherited `VITE_*` variable, because those inputs can change shipped
JavaScript without changing Git or Go VCS metadata. Verify that the `.changes`
and `.buildinfo` manifests reference both the installable `.deb` and retained
debug-symbol package (`.deb` on Debian or `.ddeb` on Ubuntu), and record the
SHA-256 of the complete artifact set.

Inspect the generated maintainer scripts, not only their source templates:

```sh
control_dir="$(mktemp -d)"
dpkg-deb --control build/deb/midnight_0.1.0~alpha-6_<arch>.deb "$control_dir"
grep -nE 'UPGRADE_ACTIVE_UNITS|deb-systemd-(helper|invoke)' \
  "$control_dir/prerm" "$control_dir/postinst"
packaging/release/verify-debian-systemd-lifecycle.sh \
  "$control_dir/postinst" "$control_dir/prerm"
```

The extracted scripts must retain Midnight's exact-active snapshot lifecycle,
must not enable or start services on a fresh install, and must contain no
generated post-install activation or prerm service path. Debhelper's
marker-delimited `--no-enable` block is expected to preserve a unit that was
already installed and enabled, but only through matching literal-unit
`debian-installed` and `was-enabled` guards; that boot enablement state never
selects the runtime restart set. The lifecycle verifier accepts only those six
reviewed blocks and the custom exact-active `systemctl start "$@"` path. The
source rule must use `--no-start --no-stop-on-upgrade` so debhelper cannot
duplicate the custom lifecycle.

On the target VM, install and configure the package:

```sh
sudo apt-get update
sudo apt-get install ./midnight_<version>_<arch>.deb
sudo install -o midnight -g midnight -m 0600 /dev/null /etc/midnight/db.dsn
sudoedit /etc/midnight/db.dsn
sudo install -d -o midnight -g midnight -m 0750 /etc/midnight/traefik/dynamic
sudoedit /etc/midnight/midnight.env
sudo systemctl daemon-reload
```

The first upgrade from `0.1.0~alpha-2` to `0.1.0~alpha-3` needs one explicit
compatibility step. Alpha-2's installed `prerm` predates the exact-active
snapshot and stops services before alpha-3 can observe them. Copy
`deploy/debian/upgrade-alpha2-bridge` to the VM with the package, then let the
bridge capture the snapshot and replace itself with `dpkg` without a gap:

```sh
sudo /tmp/upgrade-alpha2-bridge \
  /tmp/midnight_0.1.0~alpha-3_<arch>.deb
```

The bridge records only the fixed six-unit allowlist that is active at that
moment, writes a root-owned mode-`0600` snapshot atomically, and never infers
activity from enablement. It requires exactly alpha-2 to be installed and a
newer Midnight package as its sole argument. A retained snapshot from a failed
attempt is validated and reused rather than replaced. Alpha-3 consumes the
snapshot only after migration, doctor, daemon reload, and exact-set restart
succeed.

Run doctor before enabling services:

```sh
sudo midnight-doctor --json --live | sudo tee /var/lib/midnight/doctor-live.json
sudo jq -e '[.results[] | select(.status == "fail")] | length == 0' /var/lib/midnight/doctor-live.json
sudo midnight-doctor --json --live-deploy-readiness --live \
  --db-dsn-fixture=/etc/midnight/db.dsn \
  --worker-runtime-backend=docker \
  --route-config-dir=/etc/midnight/traefik/dynamic \
  --route-public-host="$MIDNIGHT_PUBLIC_HOST" \
  --route-public-path=/ \
  --route-target-port=8080 \
  | sudo tee /var/lib/midnight/doctor-live-deploy-ready.json
sudo jq -e '[.results[] | select(.status == "fail")] | length == 0' /var/lib/midnight/doctor-live-deploy-ready.json
```

Enable services and prove the server is running with package-managed configuration:

```sh
sudo systemctl enable --now midnight-server midnight-worker midnight-agent midnight-buildkit midnight-buildkit-worker midnight-registry
sudo systemctl --no-pager --full status midnight-server midnight-worker midnight-agent midnight-buildkit midnight-buildkit-worker midnight-registry
export MIDNIGHT_API_BASE="http://127.0.0.1:8080"
export MVP_RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
export MVP_EVIDENCE_DIR="artifacts/mvp-acceptance/${MVP_RUN_ID}"
mkdir -p "$MVP_EVIDENCE_DIR"
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/compat" | jq -e '.api_version == "v1alpha"'
curl -fsS "$MIDNIGHT_API_BASE/dashboard/" >/tmp/midnight-dashboard-package.html
test -s /tmp/midnight-dashboard-package.html
```

Expected result: all commands exit `0`, the server uses Postgres storage, and service logs contain no DB connection, Docker socket, binary resolution, migration, or Traefik file-provider errors.

Now run the package template deploy and inventory check above. It must pass before the real app deploy check starts.

## Real App And HTTPS Acceptance

The local smoke path does not prove MVP. The VM run must deploy a real app image by immutable digest and verify the public route.

Prepare a project, service, and production environment with the dashboard or
`midnight project create`, `midnight service create`, and
`midnight environment list`. Record their exact IDs. `real-app` deploys into
that existing scope; it no longer resolves a service by a slug shared across
projects or submits installation-wide config on a human's behalf.

Create the service's managed public domain before this check. A human session
uses `midnight link` followed by `midnight domain add`, or the service
Networking panel. The server owns the generated hostname, root path, port,
and HTTP intent. Set `MVP_PUBLIC_HOST` to that returned hostname. An explicitly
selected recovery credential may instead use an existing generated domain or
an ownership-verified custom domain; see the
[custom-domain runbook](runbooks/custom-domain-cutover.md). The runner selects
that persisted route and its saved TLS intent rather than inventing a route
name, group, hostname, or TLS policy.

Configure public DNS, ingress, and HTTPS before running. Edge-terminated HTTPS
is valid public probe evidence while Midnight's own route remains HTTP; this
does not establish that Midnight issued the edge certificate.

```sh
export MIDNIGHT_API_TOKEN="midn_user_..."  # current durable human session
export MVP_PROJECT_ID="prj_..."
export MVP_SERVICE_ID="svc_..."
export MVP_ENVIRONMENT_ID="env_..."
export MVP_IMAGE="registry.example/web@sha256:<64-hex-digest>"
export MVP_PUBLIC_HOST="<returned-managed-hostname>"
export MVP_PUBLIC_PATH="/"
export MVP_EXPECTED_RESPONSE="a literal marker identifying your application"

bash scripts/mvp-acceptance.sh real-app
```

The runner validates project/service/environment lineage and finds exactly
one existing public domain for the requested scope, hostname, and path before
submitting a mutation. Its deploy body includes `environment_id`. Human
sessions send only the admitted public opt-in; recovery requests select the
saved route, use a host selector only for a custom hostname, and preserve its
TLS policy.

It polls `GET /operations/{id}` for an exact terminal state, with bounded HTTP
requests and a configurable positive `MVP_APP_POLL_ATTEMPTS` budget (default
90, with two seconds between attempts). `FAILED`, cancellation, unknown
states, and exhaustion fail. An old event containing the word `SUCCEEDED`
cannot pass an operation whose current state is still running or failed.

A pass additionally requires the exact service/environment's canonical
`latest-active` deployment to match the submitted operation and immutable
image, the same managed domain to report `ready` and reachable, and public
HTTPS to return the expected literal application marker. Certificate metadata
is recorded separately, without private keys. Evidence files include
`deploy-request.json`, `operation-status.json`, `active-deployment.json`,
`domain-before.json`, `domain-after.json`, `public-response.body`, and
`public-certificate.txt`.

This mode proves the candidate deployment and public response at that time.
It does not restart the server or roll back; perform and retain those separate
fresh-host acceptance checks before declaring MVP complete.

## Operations And Failure-State Checks

At least one failed deploy, one host degradation, and one backup/restore failure must produce typed reason codes.

Create a separate failed deployment on the disposable acceptance service, for
example with an admitted test image whose configured health endpoint never
becomes ready. Record that failed operation's ID and inspect its actual state:

```sh
export MVP_FAILED_OPERATION_ID="op_..."
curl -fsS -H "Authorization: Bearer $MIDNIGHT_API_TOKEN" \
  "$MIDNIGHT_API_BASE/api/v1alpha/operations/$MVP_FAILED_OPERATION_ID" \
  | tee "$MVP_EVIDENCE_DIR/operation-failure.json"
jq -e '.state == "FAILED" and (.code | type == "string" and length > 0)' \
  "$MVP_EVIDENCE_DIR/operation-failure.json"
```

Retain the concrete failure reason and evidence of the initiating fault. Do
the same for real agent/Docker degradation and a real backup/restore failure
in the disposable environment. A manual `/operations/{id}/fail` call tests
error presentation only and does not prove worker failure handling; never
apply that synthetic transition to the successful public-app candidate.

## Audit And Safety Checks

Privileged mutations must emit audit rows and generated audit evidence must verify.

API evidence:

```sh
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/audit/events?limit=20" | tee "$MVP_EVIDENCE_DIR/audit-events.json"
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/audit-evidence" | tee "$MVP_EVIDENCE_DIR/audit-evidence.json"
jq -e '.events | length >= 1' "$MVP_EVIDENCE_DIR/audit-events.json"
jq -e '.status == "ready" or .status == "pass" or .status == "verified"' "$MVP_EVIDENCE_DIR/audit-evidence.json"
```

CLI verification when a JSONL audit export is available:

```sh
midnight audit verify --from-jsonl "$MVP_EVIDENCE_DIR/audit-export.jsonl" --json \
  | tee "$MVP_EVIDENCE_DIR/audit-verify.json"
jq -e '.status == "verified" or .valid == true' "$MVP_EVIDENCE_DIR/audit-verify.json"
```

Safety assertion:

```sh
if grep -R -E 'postgres://[^[:space:]]+:[^*@[:space:]]+@|BEGIN (RSA|OPENSSH|EC) PRIVATE KEY|MIDNIGHT_.*SECRET=' "$MVP_EVIDENCE_DIR"; then
  echo "secret-like material found in evidence"
  exit 1
fi
```

## Backup And Restore Acceptance

MVP cannot pass until app/database and platform-data backup/restore have been
executed against a configured off-host destination, restored, and read back.
Follow the [backup and restore runbook](runbooks/backup-restore.md) for real
`midnight-recovery` and service-volume execution. Keep that evidence separate
from the API metadata diagnostic below.

`scripts/mvp-acceptance.sh backup` exercises only export manifests and restore
plans. `POST /backup/runs` returns `501 NOT_IMPLEMENTED` normally; an API
started with `MIDNIGHT_EXPERIMENTAL=1` can write a manifest containing the
caller-supplied byte counts. Those counts do not cause a database dump,
application archive, encryption, restore, or readback. A successful diagnostic
does not satisfy the backup acceptance criterion. Run the following only on an
explicitly experimental test instance with a configured destination:

```sh
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/status" | tee "$MVP_EVIDENCE_DIR/restore-status.json"
jq -e '.status == "available" or .status == "unavailable"' "$MVP_EVIDENCE_DIR/restore-status.json"

curl -fsS -X POST \
  -H "Content-Type: application/json" \
  --data '{"id":"backup-acceptance","database_dump_bytes":1,"app_data_bytes":1,"registry_metadata_bytes":1,"config_snapshot_bytes":1,"support_reference_bytes":1,"support_reference_count":1}' \
  "$MIDNIGHT_API_BASE/api/v1alpha/backup/runs" \
  | tee "$MVP_EVIDENCE_DIR/backup-run.json"
jq -e '.id == "backup-acceptance" and .manifest.id == "backup-acceptance" and .object.key == "runs/backup-acceptance/manifest.json"' "$MVP_EVIDENCE_DIR/backup-run.json"
jq -e '[.manifest.entries[].dataClass] | contains(["database","app","platform"])' "$MVP_EVIDENCE_DIR/backup-run.json"

curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/runs" | tee "$MVP_EVIDENCE_DIR/backup-runs.json"
jq -e '.data[] | select(.id == "backup-acceptance")' "$MVP_EVIDENCE_DIR/backup-runs.json"

curl -fsS -X POST \
  -H "Content-Type: application/json" \
  --data '{"backup_id":"backup-acceptance","operator":"mvp-acceptance","rollback_owner":"platform-oncall","notes":"MVP acceptance restore plan"}' \
  "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/plans" \
  | tee "$MVP_EVIDENCE_DIR/restore-plan.json"
jq -e '.backup_id == "backup-acceptance" and .plan.backupId == "backup-acceptance" and (.plan.steps | length) >= 1' "$MVP_EVIDENCE_DIR/restore-plan.json"
jq -e '[.plan.steps[].dataClass] | contains(["database","app","platform"])' "$MVP_EVIDENCE_DIR/restore-plan.json"

curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/backup/restore/plans" | tee "$MVP_EVIDENCE_DIR/restore-plans.json"
jq -e '.data[] | select(.backup_id == "backup-acceptance")' "$MVP_EVIDENCE_DIR/restore-plans.json"
```

Current unconfigured-memory diagnostic:

```sh
test "$(curl -sS -o /tmp/midnight-backup-runs.json -w "%{http_code}" "$MIDNIGHT_API_BASE/api/v1alpha/backup/runs")" = "400" &&
  jq -e '.error.code == "BACKUP_OFFHOST_MISSING"' /tmp/midnight-backup-runs.json
```

## Final Exit Criteria

MVP is accepted only when all of these are true:

- Local bootstrap, build, test, and dashboard/API smoke checks pass.
- `surface-agreement` passes and records API/CLI/dashboard parity evidence for the surfaces owned by this MVP lane.
- Memory-mode diagnostics are recorded outside the MVP pass condition with owner and rationale.
- Package install on a fresh supported VM passes doctor and service-readiness checks.
- Package-mode PostgreSQL template intake creates one atomic outbox-backed
  deployment saga and exposes a durable status URL.
- One real app deploy reaches healthy runtime state and remains reachable over HTTPS after `midnight-server` restart.
- Dashboard/API/CLI inventory agree on projects, services, deploy operations, hosts, routes, logs, audit state, recovery state, and generated evidence.
- The official Postgres template becomes successful only with
  connectivity/restart-persistence/probe-cleanup evidence. Backup/restore state
  is evaluated separately and is never inferred from restart persistence.
- Built-in Docker integration proves engine-native write/read persistence across
  container replacement for all four official database images; the installed
  package acceptance still uses PostgreSQL as its representative end-to-end
  database flow.
- App/database and platform-data backup/restore drills copy real bytes to an
  off-host destination, restore them, and pass application readback. A typed
  degraded state such as `BACKUP_OFFHOST_MISSING` is a useful diagnostic and
  leaves this criterion incomplete.
- One failed deploy, one host degradation, and one backup/restore failure produce actionable reason codes.
- `midnight audit verify` passes for the generated audit chain/export.
- The primary dashboard contains no fake/demo-only data in the normal product path; any unavailable action is disabled or returns a real reason.
- Every outside-scope diagnostic in this document is explicitly owned with rationale and has a positive package/VM assertion where code exists.
