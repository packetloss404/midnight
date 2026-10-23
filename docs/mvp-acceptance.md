# MVP Acceptance

This document is the acceptance path for deciding whether Midnight is MVP-ready. It is intentionally command-first: every checkpoint either has an exact local command, an exact VM command, or an owned outside-scope diagnostic with rationale.

The current implementation and dated installed evidence are summarized in
[`deployment-readiness.md`](deployment-readiness.md), reconciled on 2026-09-12.
The September 5 records prove fresh schema-71 installation and a real source
deployment served through Traefik over HTTP. They do not complete the HTTPS,
restart/rollback, audit, and recovery criteria below or attest to today's host.

MVP means a design partner can install Midnight on one trusted VM, deploy and operate one real app with HTTPS, inspect deploys, services, routes, hosts, logs, audit evidence, backups, and recovery states through the dashboard/CLI/API, and complete the checks below without fake or demo-only data in the primary product path.

The target remains a single trusted VM. Multi-host scheduling, enterprise tenancy, external billing, and broad marketplace breadth are not MVP acceptance requirements.

## Next Acceptance Slice: OSS Railway Happy Path

The next acceptance slice is the smallest open-source Railway-like path that can be run on a fresh supported VM and shown to a design partner: package install, one real app deploy, one HTTPS route, one official Postgres template, one audit trail, one backup/restore plan, and typed recovery states. It is not accepted while the product only reaches queued draft/job state, memory-mode diagnostics, synthetic failures, or dashboard-only fixture data.

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
| Dashboard/API evidence can diverge from operator reality. | `surface-agreement` proves Dashboard/API/CLI agree for projects, services, deploy operations, hosts, routes, logs, audit evidence, recovery state, and generated evidence. |
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
npm --prefix web test
npm --prefix web run build
make release-check
```

Expected result: every command exits `0`. If `make release-check` writes evidence to its default artifact directory, keep those files with the acceptance run.

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

Expected result: all commands exit `0`. The runner creates one project and service, verifies the same IDs through API and CLI JSON output, registers an agent so host inventory is not synthetic, records an agent route observation so route rows are linked to the service, creates a backed operation for log-surface checks, verifies audit API/dashboard/export evidence, checks typed restore status, and runs the generated evidence assertions.

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
| Memory-mode template deploy has no queue store and returns `NOT_IMPLEMENTED`. | control-plane-api | Memory mode is diagnostic; MVP package mode must use the Postgres queue store and pass `package-template`. |
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

Set these variables on the VM:

```sh
export MVP_PROJECT_SLUG="mvp-acceptance"
export MVP_SERVICE_SLUG="web"
export MVP_IMAGE="registry.example/web@sha256:<digest>"
export MVP_PUBLIC_HOST="mvp.example.test"
export MVP_PUBLIC_PATH="/"
```

Create the project/service and submit a deploy request:

```sh
cat >"$MVP_EVIDENCE_DIR/midnight-app.json" <<EOF
{
  "api_version": "midnight/v1alpha",
  "projects": [{"slug": "${MVP_PROJECT_SLUG}", "display_name": "MVP Acceptance"}],
  "services": [{"project_slug": "${MVP_PROJECT_SLUG}", "slug": "${MVP_SERVICE_SLUG}", "display_name": "MVP Web"}]
}
EOF

midnight config apply \
  --file "$MVP_EVIDENCE_DIR/midnight-app.json" \
  --endpoint "$MIDNIGHT_API_BASE" \
  --idempotency-key "mvp-config-${MVP_RUN_ID}" \
  | tee "$MVP_EVIDENCE_DIR/config-apply.json"

curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/services" | tee "$MVP_EVIDENCE_DIR/services.json"
export MVP_SERVICE_ID="$(jq -r --arg slug "$MVP_SERVICE_SLUG" '(.data // .)[] | select(.slug == $slug) | .id' "$MVP_EVIDENCE_DIR/services.json")"

cat >"$MVP_EVIDENCE_DIR/deploy-request.json" <<EOF
{
  "image": "${MVP_IMAGE}",
  "route": {
    "name": "${MVP_SERVICE_SLUG}",
    "group": "mvp",
    "host": "${MVP_PUBLIC_HOST}",
    "path": "${MVP_PUBLIC_PATH}",
    "public": true,
    "tls": true
  }
}
EOF

curl -fsS -D "$MVP_EVIDENCE_DIR/deploy-response.headers" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: mvp-deploy-${MVP_RUN_ID}" \
  --data @"$MVP_EVIDENCE_DIR/deploy-request.json" \
  "$MIDNIGHT_API_BASE/api/v1alpha/services/$MVP_SERVICE_ID/deployments" \
  | tee "$MVP_EVIDENCE_DIR/deploy-response.json"
```

Acceptance assertion:

```sh
jq -e '.kind == "deploy" and (.state == "QUEUED" or .state == "RUNNING" or .state == "SUCCEEDED")' "$MVP_EVIDENCE_DIR/deploy-response.json"
export MVP_OPERATION_ID="$(jq -r '.id // .operation.id' "$MVP_EVIDENCE_DIR/deploy-response.json")"
```

Watch operation events until success:

```sh
for i in $(seq 1 90); do
  curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/operations/$MVP_OPERATION_ID/events" \
    | tee "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"
  if grep -Eq "SUCCEEDED|succeeded" "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"; then
    break
  fi
  if grep -Eq "FAILED|failed|error" "$MVP_EVIDENCE_DIR/operation-events-${i}.sse"; then
    exit 1
  fi
  sleep 2
done
```

Verify runtime, route, and HTTPS:

```sh
curl -fsS "$MIDNIGHT_API_BASE/api/v1alpha/dashboard/runtime-inventory" \
  | tee "$MVP_EVIDENCE_DIR/runtime-inventory.json"
jq -e '.services[] | select(.id == env.MVP_SERVICE_ID and (.health == "healthy" or .health == "ready"))' "$MVP_EVIDENCE_DIR/runtime-inventory.json"
jq -e '.routes[] | select(.service_id == env.MVP_SERVICE_ID or .service == env.MVP_SERVICE_ID or .host == env.MVP_PUBLIC_HOST)' "$MVP_EVIDENCE_DIR/runtime-inventory.json"

curl -fsS -D "$MVP_EVIDENCE_DIR/public-response.headers" \
  "https://${MVP_PUBLIC_HOST}${MVP_PUBLIC_PATH}" \
  -o "$MVP_EVIDENCE_DIR/public-response.body"
openssl s_client -servername "$MVP_PUBLIC_HOST" -connect "${MVP_PUBLIC_HOST}:443" </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates \
  | tee "$MVP_EVIDENCE_DIR/public-certificate.txt"
```

Expected result: service health is live, route inventory is live, HTTPS returns the candidate app response, and certificate metadata is present without private key material.

## Operations And Failure-State Checks

At least one failed deploy, one host degradation, and one backup/restore failure must produce typed reason codes.

Use the operation failure endpoint to prove typed failure shape while the full worker path is still being hardened:

```sh
curl -fsS \
  -H "Content-Type: application/json" \
  --data '{"code":"MVP_ACCEPTANCE_SYNTHETIC_FAILURE","message":"acceptance typed failure check"}' \
  "$MIDNIGHT_API_BASE/api/v1alpha/operations/$MVP_OPERATION_ID/fail" \
  | tee "$MVP_EVIDENCE_DIR/operation-fail.json"
jq -e '.state == "FAILED" and .code == "MVP_ACCEPTANCE_SYNTHETIC_FAILURE"' "$MVP_EVIDENCE_DIR/operation-fail.json"
```

For final MVP acceptance, replace the synthetic failure with a real failed deploy, a real agent/Docker degradation, and a real backup/restore failure, then assert the concrete product reason codes in the same way.

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

MVP cannot pass until app/database and platform-data backup/restore are visible and backed by a configured destination. The current MVP-safe restore path creates deterministic restore plans; destructive restore execution remains an explicit later operator action. Required assertions:

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
- App/database and platform-data backup/restore drills pass or show typed degraded states such as `BACKUP_OFFHOST_MISSING`.
- One failed deploy, one host degradation, and one backup/restore failure produce actionable reason codes.
- `midnight audit verify` passes for the generated audit chain/export.
- The primary dashboard contains no fake/demo-only data in the normal product path; any unavailable action is disabled or returns a real reason.
- Every outside-scope diagnostic in this document is explicitly owned with rationale and has a positive package/VM assertion where code exists.
