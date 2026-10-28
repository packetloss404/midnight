# Doctor Checks

Midnight doctor checks use stable IDs so installer, support bundle, and runbook output can refer to the same condition over time.

## Canonical Registry

| ID | Severity | Description |
| --- | --- | --- |
| `OR-CHK-DOC-001` | fatal | host runs the cgroup v2 unified hierarchy |
| `OR-CHK-DOC-002` | fatal | `/etc/docker/daemon.json` is mergeable with the Midnight baseline |
| `OR-CHK-DOC-003` | fatal | Docker Engine API is at v1.43 or newer |
| `OR-CHK-DOC-004` | fatal | ingress ports 80/443 are free or reserved by Midnight |
| `OR-CHK-DOC-005` | warn | host firewall mode is known |
| `OR-CHK-DOC-006` | warn | AppArmor or SELinux is enforcing or complain/permissive |
| `OR-CHK-DOC-007` | warn | host clock is synchronized |
| `OR-CHK-DOC-008` | warn | hostname resolves and FQDN is set |
| `OR-CHK-DOC-009` | fatal | distro `docker.io` package is not installed |
| `OR-CHK-DOC-010` | fatal | Postgres DSN is configured for package boot |
| `OR-CHK-DOC-011` | fatal | cursor signing key exists and meets the length budget |
| `OR-CHK-DOC-012` | fatal | local registry config is present |
| `OR-CHK-DOC-013` | fatal | external runtime binaries are resolvable |
| `OR-CHK-DOC-014` | fatal | worker runtime backend is configured for the live deploy check |
| `OR-CHK-DOC-015` | fatal | worker route config directory is configured and writable |
| `OR-CHK-DOC-016` | fatal | Traefik dynamic directory uses the expected route-group artifact shape |
| `OR-CHK-DOC-017` | fatal | worker route environment is coherent for the live deploy check |
| `OR-CHK-DOC-018` | fatal | package environment file is readable for systemd units |
| `OR-CHK-DOC-019` | fatal | required systemd units are present and loadable |
| `OR-CHK-DOC-020` | warn | configured off-host backup destination has a supported URI scheme |

The disposable-VM alpha floor currently asserts `OR-CHK-DOC-001..009`
on the fresh-host path because those checks cover the host/runtime
preflight contract locked for 0.1-EC1 and 0.1-EC2. Checks
`OR-CHK-DOC-010..013` are package-boot readiness checks; a design
partner should review them before enabling services, but the current
EC1/EC2 harness does not treat them as part of the historical
`001..009` pass/fail assertion.

## Operator Runbook

On Ubuntu 24.04 and Debian 12, run the doctor after the `.deb`
postinst has completed and before enabling the Midnight systemd units.
The postinst writes its first JSON report to
`/var/lib/midnight/doctor-postinst.json` by running
`midnight-doctor --json --install-readiness --live`. The report is labeled
`live=true` and `profile=install-readiness` and contains exactly checks `010`,
`013`, `015`, `018`, and `019`. Checks outside that profile were not run; they
are omitted instead of being represented as misleading skips. Use that file to
see what the package observed, then run an explicit operator check with the
host facts you want to rely on.

With `--live`, cgroup and Docker API checks run the fixed read-only commands
`stat -fc %T /sys/fs/cgroup` and
`docker version --format '{{.Server.APIVersion}}'`, each with a three-second
deadline. Explicit captured-file inputs override those probes. Without `--live`,
no command runner is installed. `--hostsource=fixture:<dir>` disables live
commands and installed-path defaults even if `--live` is also supplied. Missing
bundle files do not fall back to environment or captured package-config paths,
including runtime binary paths. Explicit path flags and runtime binary hints
remain supported for deliberately selected captured inputs.

Ingress listeners still require captured `ss -ltn` output. Firewall, MAC,
time-sync, and Docker-package checks require facts verified by the operator;
missing hints remain skips. A read-only diagnostic subset can be run as follows:

```sh
capture_dir="$(mktemp -d)"
trap 'rm -f "$capture_dir/ss-ltn.txt"; rmdir "$capture_dir"' EXIT
ss -ltn > "$capture_dir/ss-ltn.txt"

sudo midnight-doctor --json --live \
  --check=OR-CHK-DOC-001,OR-CHK-DOC-002,OR-CHK-DOC-003,OR-CHK-DOC-004,OR-CHK-DOC-005,OR-CHK-DOC-006,OR-CHK-DOC-007,OR-CHK-DOC-008,OR-CHK-DOC-009,OR-CHK-DOC-010,OR-CHK-DOC-011,OR-CHK-DOC-012,OR-CHK-DOC-013,OR-CHK-DOC-014,OR-CHK-DOC-017,OR-CHK-DOC-018,OR-CHK-DOC-019,OR-CHK-DOC-020 \
  --ss-ltn-fixture="$capture_dir/ss-ltn.txt"
```

Use the firewall, MAC, time-sync, hostname, and Docker-package hints
that match the host. On Debian 12 the package policy is the same as on
Ubuntu 24.04: upstream `docker-ce` or compatible Docker Engine is
expected, and distro `docker.io` is a fatal finding.

The default full registry and install/MVP profiles include `OR-CHK-DOC-015`,
which creates and removes a temporary directory inside the route publication
root to test writability. Use an explicit subset, as above, when runtime
directories must remain unchanged. A zero exit code can include skipped checks;
inspect each result rather than treating it as proof of complete host health.
The optional `TestLiveDoctorOnHost` canary proves only the actual cgroup/Docker
read probes and fails if either probe skips or fails; it does not prove installed
service readiness.

The `--live` flag also uses default package paths for
`OR-CHK-DOC-010..013`. To make
the package-boot checks pass, provide or verify these inputs before the
service enablement step:

- `OR-CHK-DOC-010`: set `MIDNIGHT_DB_DSN` or write a DSN to
  `/etc/midnight/db.dsn`.
- `OR-CHK-DOC-011`: confirm `/etc/midnight/cursor.key` exists; the
  postinst generates it when absent.
- `OR-CHK-DOC-012`: confirm `/etc/midnight/registry.yml` exists; the
  postinst installs the default when absent.
- `OR-CHK-DOC-013`: install pinned `buildkitd`, `buildctl`, and
  `registry` binaries or set the `MIDNIGHT_*` paths in
  `/etc/midnight/midnight.env`.

For live deploy readiness, run the narrow local prerequisite slice before
attempting a real deploy:

```sh
sudo midnight-doctor --json --live-deploy-readiness --live \
  --db-dsn-fixture=/etc/midnight/db.dsn \
  --worker-runtime-backend=docker \
  --route-config-dir=/etc/midnight/traefik/dynamic \
  --route-public-host=app.example.test \
  --route-public-path=/ \
  --route-target-port=8080
```

This command does not enqueue, schedule, promote, or drain a deployment.
It verifies local configuration prerequisites only: Postgres DSN
presence, Docker worker backend selection, route publication directory
writability, Traefik file-provider artifact shape, and coherent worker
route environment hints.

For install-path readiness, run the package slice before enabling units:

```sh
sudo midnight-doctor --json --install-readiness --live
```

This slice checks `OR-CHK-DOC-010`, `013`, `015`, `018`, and `019`.
Fixture runs can provide `--package-env-fixture`, `--db-dsn-fixture`,
`--runtime-binaries`, `--route-config-dir`, and `--systemd-unit-dir`.
In live mode the CLI defaults to `/etc/midnight/midnight.env`,
`/etc/midnight/db.dsn`, `/etc/midnight/traefik/dynamic`, and
`/lib/systemd/system`, while still accepting explicit overrides.

`status="fail"` blocks the install or service enablement path.
`status="warn"` needs operator review and usually exits `1`.
`status="skip"` means the doctor did not receive enough live fixture,
hint, or path input to decide; do not treat a skipped check as proof
that the host is ready. The JSON `profile` names the selected check set; a
missing check was outside that set and is not equivalent to `skip`.

## OR-CHK-DOC-001

Verifies the host runs the cgroup v2 unified hierarchy required by ADR-005. In fixture mode use `--mountinfo-fixture`; live host probing may also use the explicit cgroup filesystem command seam.

## OR-CHK-DOC-002

Verifies `/etc/docker/daemon.json` can be merged with the Midnight Docker daemon baseline without operator-owned key conflicts. In fixture mode use `--daemon-json-fixture`.

## OR-CHK-DOC-003

Verifies Docker Engine API support is at v1.43 or newer. In fixture mode use `--docker-version-fixture`; live wiring may use the allow-listed Docker API command seam.

## OR-CHK-DOC-004

Verifies ingress ports 80 and 443 are free or already reserved by Midnight. In fixture mode use `--ss-ltn-fixture`.

## OR-CHK-DOC-005

Verifies the host firewall mode is known. Use `--firewall-mode` with `ufw`, `firewalld`, `nftables`, `iptables`, or `none`.

## OR-CHK-DOC-006

Verifies AppArmor or SELinux is enforcing or in a permissive/complain mode appropriate for Phase 0 Alpha. Use `--mac-mode` with `apparmor-enforce`, `apparmor-complain`, `selinux-enforcing`, `selinux-permissive`, or `disabled`.

## OR-CHK-DOC-007

Verifies the host clock is synchronized. Use `--time-sync` with `synchronized`, `drift`, or `unsynchronized`.

## OR-CHK-DOC-008

Verifies the hostname resolves to a fully qualified domain name. Use `--hostname-fqdn` to provide a deterministic fixture hint.

## OR-CHK-DOC-009

Verifies the distro `docker.io` package is not installed, because Midnight requires upstream `docker-ce` or a compatible Docker Engine. Use `--docker-packages` to provide fixture package names.

## OR-CHK-DOC-010

Verifies the package boot path has a Postgres DSN without exposing its value. In fixture mode use `--db-dsn-fixture` or `--package-env-fixture`; in live mode `midnight-doctor` checks `MIDNIGHT_DB_DSN` or `MIDNIGHT_DATABASE_URL` first, then the package env file, then `MIDNIGHT_DB_DSN_FILE` or `/etc/midnight/db.dsn`.

## OR-CHK-DOC-011

Verifies the cursor signing key exists, is at least 32 bytes after trimming line endings, and is no larger than 4 KiB. In fixture mode use `--cursor-key-fixture`; in live mode the path comes from `MIDNIGHT_CURSOR_KEY_PATH` or `/etc/midnight/cursor.key`.

## OR-CHK-DOC-012

Verifies the local OCI registry config file is present and non-empty. In fixture mode use `--registry-config-fixture`; in live mode the path comes from `MIDNIGHT_REGISTRY_CONFIG` or `/etc/midnight/registry.yml`.

## OR-CHK-DOC-013

Verifies `buildkitd`, `buildctl`, and `registry` are resolvable by the service wrappers. In fixture mode use `--runtime-binaries` or `--package-env-fixture`; in live mode defaults come from `MIDNIGHT_BUILDKITD_BIN`, `MIDNIGHT_BUILDCTL`, and `MIDNIGHT_REGISTRY_BIN`.

## OR-CHK-DOC-014

Verifies `MIDNIGHT_WORKER_RUNTIME_BACKEND` is set to `docker` for the live deploy check path. `MIDNIGHT_WORKER_DOCKER_HOST` or `DOCKER_HOST` may be empty when the Docker SDK should use its default local socket.

## OR-CHK-DOC-015

Verifies `MIDNIGHT_WORKER_ROUTE_CONFIG_DIR` points at an existing absolute Traefik dynamic publication directory and that the current process can create a child route-group directory there. The probe creates and removes a temporary directory; it does not publish a route artifact.

## OR-CHK-DOC-016

Verifies the Traefik dynamic directory holds no route artifacts that Traefik cannot observe. The publisher writes flat files directly in the directory, named `<route-group>[.<route>].traefik.dynamic.yaml`, because Traefik's file provider loads subdirectory files when it starts but only *watches* the configured directory itself. Any `*.traefik.dynamic.yaml` found one level down is therefore a stale pre-flat artifact that Traefik will re-load on its next restart, resurrecting a router that conflicts with the live one; the check fails and names the offending relative paths.

## OR-CHK-DOC-017

Verifies the worker route environment is coherent without exposing secrets.
A fixed public host is optional because explicit public deploys may request a
generated hostname. Runtime readiness additionally requires trusted Traefik
API, ingress-probe, and explicit ingress-container settings.

## OR-CHK-DOC-018

Verifies `/etc/midnight/midnight.env` is present, readable, and contains at least one `KEY=value` entry. In fixture mode use `--package-env-fixture`; in live mode the path comes from `MIDNIGHT_ENV_FILE` or `/etc/midnight/midnight.env`.

## OR-CHK-DOC-019

Verifies the five required Midnight systemd unit files are present and parseable enough for service enablement review: `midnight-server.service`, `midnight-worker.service`, `midnight-agent.service`, `midnight-buildkit.service`, and `midnight-registry.service`. In fixture mode use `--systemd-unit-dir`; in live mode the directory comes from `MIDNIGHT_SYSTEMD_UNIT_DIR` or `/lib/systemd/system`.

## Runner Contract

The doctor registry is validated before execution. Empty IDs and duplicate IDs fail validation so output remains stable and machine-readable.

Checks run in registry order. A registered check without executable logic returns `skip`, which lets the registry publish stable IDs before every probe is wired to host state.

Installer preflight command seams use injected command output by default. The command plan allow-list currently permits exactly `docker version --format '{{.Server.APIVersion}}'` and `stat -fc %T /sys/fs/cgroup`, each with a `3s` timeout and `requires_root: false`. Tests should provide fake runner output such as Docker returning `1.44` and stat returning `cgroup2fs`; they must not inspect the live host.

Command evidence collection also uses injected runners only. Evidence JSON includes the validated spec, stdout, stderr, an exit-like code, timeout state when applicable, and redacted errors so support data is deterministic without running live host commands in tests.

The command evidence batch collector runs the complete allow-list through the injected runner, preserves allow-list order, maps parseable evidence into the existing preflight report shape, and renders deterministic JSON under `midnight.preflight.command_evidence_batch.v1`. Batch JSON also includes a pure `redaction_review` summary over the already-redacted `stdout`, `stderr`, and `error` fields. Caller wiring must provide the runner; package defaults do not execute host commands.

The operator-facing evidence mode guide is maintained with install docs at [`docs/install/preflight-evidence-modes.md`](../install/preflight-evidence-modes.md). It covers injected fake runners, opt-in OS runners, release evidence packets, and redaction review expectations.

Redaction review statuses are:

| Status | Meaning |
| --- | --- |
| `clean` | No redaction markers, sensitive keywords, or obvious unredacted secret patterns were found in reviewed fields. |
| `needs-review` | A field contains `<redacted>` or sensitive-keyword context and should be checked before evidence is shared. |
| `blocked` | A field still contains an obvious unredacted secret pattern such as bearer credentials, `token=value`, API keys, AWS access key IDs, or private key headers. Do not share the evidence packet until fixed. |

An OS command runner exists only as an explicit opt-in bridge for installer or doctor wiring. It validates the requested spec against the fixed allow-list before `exec`, applies the spec timeout through context cancellation, and records stdout, stderr, exit code, timeout state, and redacted errors as command evidence. Do not construct this bridge in package defaults or unit tests that can use fake runners.

## Resource Pressure Review Summaries

Resource pressure classifiers are pure functions over already-collected ratios. They do not inspect filesystems, conntrack state, `/proc`, or the live host.

## Read-Only AI Diagnosis Foundation

AI diagnosis cases are pure support artifacts. `internal/diagnostics.BuildReadOnlyAIDiagnosisCase` accepts already-collected observations, redacts secret-shaped values through the support-bundle redaction rules, emits deterministic observation ordering, and sets `mutationAllowed: false`.

The only baseline capabilities are `summarize`, `classify`, and `suggest-runbook`. Foundations tests pin three eval baselines: no mutation, redacted inputs, and deterministic ordering.

Class-2 AI draft actions are modeled separately by `internal/diagnostics.BuildReadOnlyAIDraftAction`. Draft artifacts may propose runbook, config, policy, or rollback-plan changes, but they always set `mutationAllowed`, `executionAllowed`, and `approvalAllowed` to `false`. Unsupported action kinds such as execute/apply/approve are rejected before artifact creation. Draft lists are redacted and sorted for deterministic review.

Disk usage uses a pre-critical review band above `0.80` and the alert-compatible critical threshold at `>= 0.85`.

```json
{
  "resource": "disk",
  "status": "critical",
  "used_ratio": 0.85,
  "warning_threshold": 0.8,
  "critical_threshold": 0.85,
  "reason": "disk usage is at or above the critical alert threshold"
}
```

Conntrack usage matches the warning alert boundary at `> 0.80` and uses a deterministic critical saturation boundary at `>= 0.95`.

```json
{
  "resource": "conntrack",
  "status": "warning",
  "used_ratio": 0.8001,
  "warning_threshold": 0.8,
  "critical_threshold": 0.95,
  "reason": "conntrack usage is above the warning alert threshold"
}
```

## Combined Readiness JSON Example

Doctor readiness is a pure summary composed from already-collected preflight assessment data, install readiness data, and doctor check results. It does not execute checks or inspect the live host.

```json
{
  "status": "degraded",
  "counts_by_source": {
    "preflight": {
      "total": 2,
      "status_counts": {
        "pass": 1,
        "warn": 1
      }
    },
    "install": {
      "total": 1,
      "status_counts": {
        "ready-with-warnings": 1
      }
    },
    "doctor": {
      "total": 2,
      "status_counts": {
        "pass": 1,
        "skip": 1
      }
    }
  },
  "restart_required": true,
  "safe_fix_count": 2,
  "next_actions": [
    "review-warnings",
    "inspect-doctor-results",
    "apply-safe-fixes",
    "plan-restart"
  ]
}
```

## Operator Readiness

`internal/doctor.NewOperatorReadiness` builds a pure local operator readiness model from the combined doctor readiness summary plus caller-supplied gate evidence from existing planners. It does not contact Slack, SMTP, marketplaces, network services, Kubernetes, registries, storage backends, or the host filesystem.

Required local domains are:

| Phase | Domain | Expected local source |
| --- | --- | --- |
| 0 | `install_gate` | Combined preflight/install/doctor readiness summary |
| 1 | `doctor_snapshot` | Doctor snapshot/readiness summary |
| 2 | `notifications` | Notification channel/subscription delivery preview planner |
| 2 | `marketplace` | Local marketplace card/search/template gates |
| 3 | `networking` | WireGuard, DNS, route, or TCP proxy planning gates |
| 3 | `runtime_adapter` | Runtime adapter conformance or acceptance report |
| 3 | `registry_cache` | Registry cache planner output |
| 3 | `volume_attach` | Volume attach/detach planner output |

Missing required evidence is reported as `needs-review` rather than guessed. Any local gate with `blocked` makes the aggregate readiness `blocked`; otherwise any `needs-review` makes the aggregate `needs-review`.

```json
{
  "schema_version": "midnight.dev/operator-readiness/v1alpha1",
  "status": "needs-review",
  "phases": [
    {
      "phase": 0,
      "status": "ready",
      "domains": [
        {
          "domain": "install_gate",
          "status": "ready",
          "gates": [
            {
              "phase": 0,
              "domain": "install_gate",
              "source": "doctor.readiness_summary",
              "status": "ready",
              "reason": "derived from combined doctor readiness summary"
            }
          ]
        }
      ]
    }
  ],
  "missing_domains": [
    {
      "phase": 2,
      "domain": "notifications"
    }
  ]
}
```
