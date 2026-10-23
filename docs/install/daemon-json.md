# Docker daemon.json Baseline

Preflight treats `/etc/docker/daemon.json` as an operator-owned file with a small Midnight-owned baseline. The merge library is pure: it accepts existing JSON bytes and returns merged JSON bytes without reading or writing disk.

## Baseline

Midnight requires these daemon defaults:

```json
{
  "features": {
    "buildkit": true
  },
  "live-restore": true,
  "log-driver": "json-file",
  "log-opts": {
    "max-file": "5",
    "max-size": "10m"
  },
  "no-new-privileges": true,
  "storage-driver": "overlay2"
}
```

Daemon-level `init: true` is not part of this baseline. The current runtime default is per-container `HostConfig.Init=true`.

## Merge Rules

- Empty or absent existing content is treated as `{}`.
- Existing content and the baseline must be JSON objects.
- Missing baseline keys are added recursively.
- Existing values for baseline-owned keys must match the baseline.
- Differing values for baseline-owned keys are conflicts and must be refused.
- Unknown operator keys are preserved, including nested keys under baseline objects.
- Invalid JSON fails closed and must not produce merged output.

## Operational Notes

The pure library does not restart Docker or write `/etc/docker/daemon.json`. Installer wiring should write atomically, preserve file mode/ownership expectations, and report that Docker requires a restart when the merged content differs from the current file.

## Preflight Input Seams

Preflight parsers accept captured bytes or injected command output. Package defaults do not inspect the live host unless caller wiring provides a command runner or explicit file paths.

For the full operator-facing mode split between injected fake runners, opt-in OS runners, release evidence packets, and redaction review, see [Preflight Evidence Modes](preflight-evidence-modes.md).

Command-backed preflight checks are planned from a fixed allow-list before any runner is invoked. The command plan is data-only by default:

```json
[
  {
    "id": "preflight.cgroup_fs_type",
    "check_id": "PRE-CGROUP-001",
    "executable": "stat",
    "args": ["-fc", "%T", "/sys/fs/cgroup"],
    "timeout": "3s",
    "requires_root": false
  },
  {
    "id": "preflight.docker_api_version",
    "check_id": "PRE-DOCKER-API-001",
    "executable": "docker",
    "args": ["version", "--format", "{{.Server.APIVersion}}"],
    "timeout": "3s",
    "requires_root": false
  }
]
```

Installer wiring must validate the executable and arguments exactly against the allow-list before using a command runner. Tests should use fake runners and captured output; they must not execute these commands against the live host.

The OS command runner is opt-in only for live installer or doctor wiring. It rejects any non-allowlisted spec before `exec`, enforces the allow-list timeout with context cancellation, and converts stdout, stderr, exit status, timeout state, and redacted errors to the existing command evidence JSON. Package defaults and unit tests must continue to use captured inputs or fake runners unless a test is specifically asserting pre-exec rejection behavior.

Evidence collection uses the same allow-list and injected runner seam. A richer fake runner can return stdout, stderr, an exit-like code, and timeout state; older fake runners that only return stdout and an error are still recorded with `exit_code: 0` for success or `exit_code: -1` for runner errors. Output is redacted before JSON serialization for common token, bearer, password, secret, credential, and API key patterns.

The batch collector runs the full allow-list through an injected runner and maps the evidence back into the existing preflight report shape when the output can be parsed. It does not construct an OS runner by default, so tests and offline support tooling can use fake runners only.

Docker API input is the output of:

```sh
docker version --format '{{.Server.APIVersion}}'
```

Example parser input:

```text
1.44
```

Cgroup v2 input can be the output of:

```sh
stat -fc %T /sys/fs/cgroup
```

Example parser input for cgroup v2:

```text
cgroup2fs
```

Daemon JSON input is read through an injected path provider and file reader, then passed to the pure merge routine as bytes. This keeps tests fixture-backed and avoids live `/etc/docker/daemon.json` reads.

Fake runner output can be recorded as command evidence without executing anything by default:

```json
{
  "schema": "midnight.preflight.command_evidence.v1",
  "spec_id": "preflight.docker_api_version",
  "check_id": "PRE-DOCKER-API-001",
  "executable": "docker",
  "args": ["version", "--format", "{{.Server.APIVersion}}"],
  "timeout": "3s",
  "requires_root": false,
  "exit_code": 0,
  "stdout": "1.44\n"
}
```

Failure evidence preserves stderr and a process-like status without exposing obvious secrets:

```json
{
  "schema": "midnight.preflight.command_evidence.v1",
  "spec_id": "preflight.docker_api_version",
  "check_id": "PRE-DOCKER-API-001",
  "executable": "docker",
  "args": ["version", "--format", "{{.Server.APIVersion}}"],
  "timeout": "3s",
  "requires_root": false,
  "exit_code": 17,
  "stderr": "Authorization: Bearer <redacted>\n",
  "error": "docker failed token=<redacted>"
}
```

Batch evidence renders a deterministic support payload with the derived report first and raw command evidence second:

```json
{
  "schema": "midnight.preflight.command_evidence_batch.v1",
  "report": {
    "status": "pass",
    "severity": "healthy",
    "exit_code": 0,
    "checks": [
      {
        "id": "PRE-CGROUP-001",
        "status": "pass",
        "reason": "cgroup v2 filesystem type found"
      },
      {
        "id": "PRE-DOCKER-API-001",
        "status": "pass",
        "reason": "Docker API 1.44 satisfies required 1.43"
      }
    ]
  },
  "evidence": [
    {
      "schema": "midnight.preflight.command_evidence.v1",
      "spec_id": "preflight.cgroup_fs_type",
      "check_id": "PRE-CGROUP-001",
      "executable": "stat",
      "args": ["-fc", "%T", "/sys/fs/cgroup"],
      "timeout": "3s",
      "requires_root": false,
      "exit_code": 0,
      "stdout": "cgroup2fs\n"
    },
    {
      "schema": "midnight.preflight.command_evidence.v1",
      "spec_id": "preflight.docker_api_version",
      "check_id": "PRE-DOCKER-API-001",
      "executable": "docker",
      "args": ["version", "--format", "{{.Server.APIVersion}}"],
      "timeout": "3s",
      "requires_root": false,
      "exit_code": 0,
      "stdout": "1.44\n"
    }
  ]
}
```

## Preflight JSON Examples

When cgroup v2, Docker API, and daemon baseline checks pass, JSON output uses stable check IDs:

```json
{
  "status": "pass",
  "severity": "healthy",
  "exit_code": 0,
  "checks": [
    {
      "id": "PRE-CGROUP-001",
      "status": "pass",
      "reason": "cgroup v2 mount found"
    },
    {
      "id": "PRE-DOCKER-API-001",
      "status": "pass",
      "reason": "Docker API 1.44 satisfies required 1.43"
    },
    {
      "id": "PRE-DOCKER-DAEMON-001",
      "status": "pass",
      "reason": "daemon.json already satisfies baseline"
    }
  ]
}
```

Summary-only callers can render a pure assessment from already-collected results without inspecting the host:

```json
{
  "status_counts": {
    "pass": 2,
    "warn": 1
  },
  "severity_counts": {
    "fatal": 2,
    "warn": 1
  },
  "has_blockers": false,
  "safe_fix_available_count": 1,
  "recommended_next_action": "review-warnings"
}
```

A daemon baseline merge need is warning-level because the installer can present the required merge without overwriting operator-owned keys:

```json
{
  "status": "warn",
  "severity": "warn",
  "exit_code": 1,
  "checks": [
    {
      "id": "PRE-CGROUP-001",
      "status": "pass",
      "reason": "cgroup v2 mount found"
    },
    {
      "id": "PRE-DOCKER-API-001",
      "status": "pass",
      "reason": "Docker API 1.44 satisfies required 1.43"
    },
    {
      "id": "PRE-DOCKER-DAEMON-001",
      "status": "warn",
      "reason": "daemon.json requires baseline merge"
    }
  ]
}
```

## Remediation Plan JSON Example

The remediation planner is dry-run only. It converts preflight results into data-only steps and does not write files, restart Docker, upgrade packages, or reboot the host.

```json
{
  "steps": [
    {
      "command": "sudoedit /etc/default/grub && sudo update-grub && sudo reboot",
      "title": "Enable cgroup v2",
      "risk": "host_reboot",
      "requiresRestart": true,
      "applyMode": "manual"
    },
    {
      "command": "sudo apt-get update && sudo apt-get install --only-upgrade docker-ce docker-ce-cli containerd.io",
      "title": "Upgrade Docker API provider",
      "risk": "package_upgrade",
      "requiresRestart": true,
      "applyMode": "manual"
    },
    {
      "command": "sudoedit /etc/docker/daemon.json && sudo systemctl restart docker",
      "title": "Resolve Docker daemon.json baseline",
      "risk": "operator_config_conflict",
      "requiresRestart": true,
      "applyMode": "manual"
    }
  ]
}
```

## Install Readiness JSON Example

Install readiness is a pure summary composed from already-collected preflight results and dry-run remediation steps. It does not inspect the host and does not execute remediation.

```json
{
  "readiness": "ready-with-warnings",
  "blocker_count": 0,
  "warning_count": 1,
  "safe_fix_count": 1,
  "restart_required_count": 1,
  "next_actions": [
    "review-warnings",
    "apply-safe-fixes",
    "plan-restart"
  ]
}
```
