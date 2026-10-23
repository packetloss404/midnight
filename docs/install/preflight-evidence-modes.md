# Preflight Evidence Modes

Preflight evidence has four supported modes. The default posture is offline and deterministic: package defaults do not inspect the live host, and tests should not execute host commands.

## Injected Fake Runner

Use an injected fake runner for unit tests, fixture-backed support tooling, and offline packet generation. The fake runner supplies captured stdout, stderr, exit code, timeout state, and optional runner errors for the fixed command allow-list.

The current allow-list is exactly:

| Spec ID | Command | Timeout | Root |
| --- | --- | --- | --- |
| `preflight.cgroup_fs_type` | `stat -fc %T /sys/fs/cgroup` | `3s` | false |
| `preflight.docker_api_version` | `docker version --format {{.Server.APIVersion}}` | `3s` | false |

Injected evidence is still normalized through the same command evidence path as live evidence. Output and errors are redacted before serialization, and batch collection maps parseable evidence into the preflight report shape.

## Opt-In OS Runner

The OS runner is only for live installer or doctor wiring that explicitly opts into host command execution. It is not constructed by package defaults.

Before `exec`, the runner validates the executable and arguments against the fixed allow-list. It applies the spec timeout through context cancellation and records stdout, stderr, exit code, timeout state, and redacted errors as `midnight.preflight.command_evidence.v1` JSON.

## Release Evidence Packet

A release evidence packet is a deterministic support packet built from fake or already-collected inputs. It does not collect new host data.

The packet combines:

- command evidence
- preflight assessment summary
- install remediation plan
- install readiness summary
- doctor readiness summary
- resource pressure review summaries
- issue rollup and release status

Packet status is `ready`, `needs-review`, or `blocked` based on the rollup. Timed-out command evidence and fatal readiness states block release; non-zero command evidence, remediation steps, warnings, degraded readiness, and warning-level resource pressure require review.

## Redaction Review

Command evidence redaction is automatic for common bearer, token, password, secret, credential, API key, AWS access key, and private key patterns. The batch payload also includes a redaction review over the already-redacted `stdout`, `stderr`, and `error` fields.

Redaction review statuses are:

| Status | Meaning |
| --- | --- |
| `clean` | No redaction markers, sensitive keywords, or obvious unredacted secret patterns were found. |
| `needs-review` | A field contains `<redacted>` or sensitive-keyword context and should be checked before sharing. |
| `blocked` | A field still contains an obvious unredacted secret pattern. Do not share the evidence until fixed. |

Operators should review any `needs-review` or `blocked` result before attaching evidence to a ticket or release record.
