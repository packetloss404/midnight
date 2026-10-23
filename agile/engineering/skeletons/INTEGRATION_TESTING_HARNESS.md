# INTEGRATION_TESTING_HARNESS — How Midnight Integration Tests Run End-to-End

- **Skeleton ID:** SKEL-TEST-HARNESS
- **Owner:** Senior Engineer 4 of 4 (skeleton specifications)
- **Status:** Locked by panel decisions; ready for engineering kickoff

This file is the source of truth for how the engineering team runs end-to-end
integration tests. It encodes the panel-locked failure-drill list (line 195),
the contract-test rubric (line 171), the asciicast verification rule (line
131), the SSRF fixture requirement (line 124), and the local-mode harness
(line 191).

Engineering implements the harness in `internal/testutil/harness` and exposes
it through `task test:integration`, `task test:contract`, `task test:drill`.

---

## 1. testcontainers-go Topology

Each integration test suite spins up a disposable Midnight instance using
`testcontainers-go`. Topology per suite:

```text
┌─────────────────────────── test process (Go) ────────────────────────────┐
│                                                                          │
│  HarnessFactory.New(t, profile)                                          │
│        │                                                                 │
│        ├─▶ postgres:16                          (testcontainers)         │
│        ├─▶ traefik:3.x                          (testcontainers)         │
│        ├─▶ docker-in-docker:24-dind             (testcontainers)         │
│        │   └ rootless dind preferred when host kernel allows; rootful   │
│        │     dind tagged BUILD_ROOTFUL in test labels                    │
│        ├─▶ moby/buildkit:rootless               (testcontainers)         │
│        ├─▶ pebble (RFC 8555 ACME server)        (testcontainers)         │
│        ├─▶ minio (S3 surrogate)                 (testcontainers)         │
│        ├─▶ openssh-server (SFTP/rsync target)   (testcontainers)         │
│        └─▶ challtestsrv (DNS-01 fixture)        (testcontainers)         │
│                                                                          │
│  Inside the test process (or a sidecar container, depending on suite):   │
│        ├─▶ midnight-server  (built from current branch)                  │
│        ├─▶ midnight-worker  (one per queue class)                        │
│        └─▶ midnight-agent   (binds to the dind container's docker.sock)  │
└──────────────────────────────────────────────────────────────────────────┘
```

The harness exposes:

```go
package harness

// New returns a fresh, disposable Midnight instance bound to ephemeral
// ports on localhost. Containers are torn down when t.Cleanup fires.
func New(t *testing.T, profile Profile) *Midnight

type Profile int
const (
    ProfileLocal      Profile = iota // panel line 191: --profile=local fast feedback
    ProfileFull                       // full topology incl. ACME, off-host backup
    ProfileDrill                      // ProfileFull plus failure-injection hooks
    ProfileContract                   // ProfileFull plus Schemathesis/Pact verifiers
)

type Midnight struct {
    APIEndpoint   string
    AgentEndpoint string
    DB            *sql.DB
    Docker        *client.Client   // bound to the dind container
    Traefik       *traefik.Test
    Pebble        *pebble.Test
    Storage       *minio.Test
    DNSFixture    *challtestsrv.Test
    Clock         *fakeclock.Clock // fake clock; real clock when ProfileFull
    BootstrapTok  string
}
```

`HarnessFactory` reuses an outer Docker client to launch sub-containers; it
does NOT use the host system's Docker socket directly when the host is a CI
runner — it nests via dind to keep tests reproducible across runners.

---

## 2. Disposable Midnight Instance per Suite

One `Midnight` per `TestMain` or per `t.Run` group, depending on the test
class:

| Test class | Lifecycle | Why |
|---|---|---|
| Pure unit | none | no harness; just `*testing.T` |
| Per-package integration | one per `TestMain` | shared schema migration cost |
| Per-feature E2E | one per top-level `t.Run` | independent state, parallel-safe |
| Drill / chaos | one per drill scenario | isolation between fault injections |
| Contract | one per contract suite | shared OpenAPI fixture |

State teardown:

- Postgres container: `t.Cleanup` removes the container.
- BuildKit cache: scoped to a per-test tmpfs subtree; reclaimed automatically.
- ACME state: per-test `acme.json`; never persisted.
- Asciicast files: emitted to `t.TempDir()`.

Parallel safety: every container binds an ephemeral port; the harness reads
the bound port from the container record and rewrites every URL in the
in-process configuration.

---

## 3. Let's Encrypt Staging + Pebble Integration

ACME tests use **Pebble** by default for fast iteration and **LE staging**
for the panel-mandated cold-start drill (line 203: ACME-state restore at
scale).

```go
type ACMEMode int
const (
    ACMEPebble    ACMEMode = iota // default; fast, deterministic
    ACMELEStaging                  // for restore-at-scale drill; opt-in
)

func (h *Midnight) UseACME(mode ACMEMode) error
```

Tests that touch real LE staging are gated behind a build tag
`//go:build acme_staging` so default `task test` does not hammer LE staging
endpoints.

The `challtestsrv` fixture stands in for a public DNS server during DNS-01
tests:

- `manual` DnsProvider implementations write the TXT and the harness
  registers it with `challtestsrv` so propagation polls succeed.
- `cloudflare` / `route53` providers are stubbed with the cloud SDK's
  testing transports; real-cloud tests are tagged `//go:build cloud_dns`.

---

## 4. Schemathesis Against the Live OpenAPI Spec

Every integration test run starts with a Schemathesis property-test pass
against the **live** server's OpenAPI spec, satisfying the panel's
v1-graduation rubric (line 171, item b: ≥ 95% endpoint coverage by
Schemathesis property tests).

```bash
# task test:contract invokes:
schemathesis run \
    --base-url "${MIDNIGHT_API_URL}" \
    --checks all \
    --hypothesis-database :memory: \
    --hypothesis-seed=$(date +%s) \
    --request-timeout 30 \
    --workers auto \
    "${MIDNIGHT_OPENAPI_URL}"
```

Coverage reporting:

- The harness inspects Schemathesis output and writes
  `out/contract/coverage.json` with per-operation hit counts.
- CI gate: any operation not covered for ≥ 90 days fails the gate (panel
  line 171, item a).

The Schemathesis runner is launched inside a sidecar container so that
`docker run` is the canonical invocation; this keeps the host runner free of
Python.

---

## 5. Failure-Drill Harness

The panel locked an explicit failure-drill list (line 195). Every drill is
implemented as a Go test under `internal/testutil/drills/` and runs in a
nightly CI job. Each drill is a pure function over `*Midnight` plus a
named injection.

```go
type FaultInjector interface {
    Inject(ctx context.Context, h *Midnight, p Params) (Recovery, error)
}
```

Injectors required by the panel list:

| Drill | Injector | Recovery assertion |
|---|---|---|
| Clock skew > 5min | `injectors.ClockSkew{Offset: 6*time.Minute}` | Agent registration fails with `AGENT_CLOCK_SKEW`; ACME ops refuse with offset > 5s; deployment FSM unaffected for offsets < 30s |
| journald wedged | `injectors.JournaldWedge{}` (sends SIGSTOP to journald) | Tenant logs continue via Docker json-file (panel line 95); platform alerts fire |
| /var/log full | `injectors.VarLogFull{ResidualBytes: 4096}` | logrotate copytruncate triggers; agent log emit drops to lossy mode without crashing |
| Postgres connection exhaustion | `injectors.PGConnExhaustion{}` | Reconciler enters degraded mode; queue dequeue backs off; no double-promotion to ACTIVE |
| Network partition × 3 durations | `injectors.NetworkPartition{Durations: []time.Duration{10*time.Second, 60*time.Second, 10*time.Minute}}` | Reconnect uses jittered backoff (panel line 111); after recovery the warmup phase admits only Class-A remediation |
| BuildKit cache corruption | `injectors.BuildKitCorrupt{}` | Build worker classifies as cache-poison, evicts cache, retries clean; reason code `BUILD_FAILED` recorded |
| Docker socket EOF | `injectors.DockerSocketEOF{}` | Agent enters `AGENT_RUNTIME_UNAVAILABLE`; does NOT crash-loop (panel line 94) |
| Volume read-only remount | `injectors.VolumeReadOnly{}` | New deploys fail `VOLUME_MOUNT_FAILED`; existing instances continue serving until next restart |
| KEK loss | `injectors.KEKLoss{}` | Restore drill from operator passphrase succeeds; rotated-password fixture passes; documented as irrecoverable without operator backup (panel line 118) |
| ACME state restore at scale | `injectors.ACMEColdStart{Domains: 200}` | Issuance batches at ≤ 20/hour with priority queue (panel line 203); LE-staging used for first batch; surfaces `RESTORE_ACME_THROTTLED`, never as failure |

Drill output is one JSON manifest per scenario, archived for 90 days. A
drill is "green" only when the recovery assertions all pass within the
declared SLA window.

---

## 6. Asciicast Comparison

`midnight shell` and `midnight ssh` sessions MUST be recorded (panel line
131). The harness verifies this with byte-level asciicast comparison.

```go
type AsciicastVerifier interface {
    // Compare returns nil iff want and got are equivalent under the
    // panel-locked redaction rules and the asciicast spec v2 framing.
    Compare(want, got []byte, scope RedactionScope) error
}
```

Test flow:

1. Test invokes `midnight shell --service svc_… --command 'echo HELLO; echo TOK=midn_test_…'`.
2. Harness captures the shell session and the on-disk asciicast file.
3. Verifier asserts:
   - The asciicast frame stream replays to a deterministic terminal state.
   - Every secret prefix from the redaction fixture set is replaced by
     `[REDACTED]` (panel line 126: release gate `secret_leakage_rate == 0`).
   - The `header` block records actor id, deployment id, session id, and
     two-person-approval id (when applicable).
4. The asciicast hash is recorded in audit; the harness verifies the audit
   row's hash matches.

---

## 7. Pact Contract Verification

The panel rubric (line 171, item c) requires green Pact contracts for CLI,
dashboard, Terraform provider, MCP. The harness runs:

```text
task test:contract
  ├─▶ pact-verify cli       (consumer pact in cmd/midnight/pacts/)
  ├─▶ pact-verify dashboard (consumer pact in web/pacts/)
  ├─▶ pact-verify terraform (consumer pact in vendor/midnight-terraform/pacts/)
  └─▶ pact-verify mcp       (consumer pact in vendor/midnight-mcp/pacts/)
```

Pact broker:

- Local: an ephemeral `pact-broker` container per CI job.
- Pre-release: a hosted broker; release gate refuses to publish without
  green verification across all four consumers.

Pacts are generated by consumers and **uploaded** by their CI; the Midnight
server CI **verifies** them. Engineering does not hand-write pact files.

---

## 8. Coverage and Race Detector

Required gates (panel-implied, codified here):

| Metric | Gate |
|---|---|
| Race detector | All `go test` runs use `-race`. CI rejects on data race. |
| Test shuffle | `-shuffle=on` to surface order-dependent tests. |
| Coverage (control plane) | ≥ 75% line coverage for `internal/api`, `internal/queue`, `internal/reconciler`, `internal/audit`, `internal/idempotency` |
| Coverage (agent) | ≥ 60% line coverage for `cmd/midnight-agent/...` (host-bound code is harder to unit-test; integration tests fill the gap) |
| Mutation testing | Optional in MVP Beta; required by v1 for `internal/audit` and `internal/idempotency` (load-bearing crypto / state machines) |
| Property testing | `gopter`-style property tests required for the deployment FSM and the queue lease/heartbeat algorithm |

Race-detector exemptions are recorded in `.test-exemptions.yaml` with an ADR
reference. There are zero exemptions at MVP.

---

## 9. Local-Mode Harness (`--profile=local`)

Panel line 191 splits Phase-0 install into local mode (under 15 min on a
fresh VM) and public mode. The integration harness mirrors this with
`ProfileLocal`:

- Uses `*.midnight.local` and self-signed TLS (no Pebble, no LE).
- Runs Postgres + Traefik + dind only; skips minio, pebble, challtestsrv.
- Disables ACME tests entirely.
- Mounts a tmpfs for `/var/lib/midnight` to keep iteration cycles fast.

Developers run `task test:integration:local` for the inner-loop suite. CI
runs `ProfileFull` and `ProfileDrill` on every PR; `ProfileContract`
nightly.

---

## 10. SSRF Fixture Set

Panel line 124 requires test fixtures for the SSRF-safe outbound HTTP
client. The harness ships:

```go
package ssrffixture

// MustReject lists URLs that the SafeHTTPClient MUST refuse:
//   loopback, RFC1918, CGNAT, link-local, multicast, cloud metadata
//   (169.254.169.254 and IPv6 fd00:ec2::254), and DNS-rebinding probes.
var MustReject = []string{
    "http://127.0.0.1/",
    "http://10.0.0.1/",
    "http://192.168.1.1/",
    "http://100.64.0.1/",
    "http://169.254.169.254/latest/meta-data/",
    "http://[fd00:ec2::254]/",
    "http://[::1]/",
    "http://[fe80::1%lo0]/",
    "http://attacker.test.midnight.invalid/", // resolves to 127.0.0.1; rebinding
}

// MustAccept lists URLs the client MUST allow.
var MustAccept = []string{
    "https://api.example.com/",
    "https://acme-v02.api.letsencrypt.org/directory",
}
```

A property test loops over both lists for every release.

---

## 11. CI Job Composition

```yaml
# .github/workflows/ci.yml (sketch)

jobs:
  unit:
    steps: [task lint, task test]
  integration:
    needs: unit
    services: [docker]   # dind out-of-the-box
    steps: [task test:integration]
  contract:
    needs: unit
    steps: [task test:contract]
  drill:
    if: github.event_name == 'schedule'   # nightly
    steps: [task test:drill]
  supplychain:
    steps: [task supplychain]   # govulncheck, osv-scanner, npm audit --omit=dev
```

The release job depends on all four (`unit`, `integration`, `contract`,
`supplychain`) being green. Drill is non-gating but is reviewed weekly.

---

## 12. Determinism Rules

- Tests MUST NOT call `time.Now()` directly. They go through `clock.Clock`
  from the harness, backed by `fakeclock` in unit tests and the wall clock
  in integration. A custom golangci-lint rule enforces this (`forbid-time-now`
  outside `internal/clock`).
- Tests MUST NOT depend on container IPs, hostnames, or ports beyond what
  the harness reports.
- Tests MUST NOT share Postgres state across `t.Run` boundaries.
- Tests MUST set `t.Parallel()` unless they own a global like `flag` or
  modify env; the parallel default is panel-implied by reconcile-storm
  guard testing requirements (line 111).

---

## 13. What This Harness Does NOT Do

- It does NOT validate cosign signatures against the real Sigstore public
  good infrastructure; cosign tests use ephemeral keys.
- It does NOT exercise the multi-host (WireGuard) Phase-3 path; that
  has its own dedicated harness `internal/testutil/wg-harness` that
  spins up multiple dind containers in a virtual mesh.
- It does NOT measure latency SLOs; dedicated load-test rigs (k6 +
  Grafana) live outside this skeleton and are tracked separately.

This boundary is intentional: integration tests are for **correctness and
contract**. Performance gating lives in the load-test rig because mixing
the two produces flaky correctness signals that the panel's "zero
secret_leakage_rate" + "no double-promotion" gates cannot tolerate.
