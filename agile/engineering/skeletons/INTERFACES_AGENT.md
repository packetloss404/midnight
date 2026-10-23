# INTERFACES_AGENT — Host Agent Interface Map

- **Skeleton ID:** SKEL-IFACE-AGENT
- **Owner:** Senior Engineer 4 of 4 (skeleton specifications)
- **Status:** Locked by panel decisions; ready for engineering kickoff

This file lists the public Go types and interface signatures the **host
agent** team implements. The agent is `cmd/midnight-agent`. It is a
static binary, runs as a systemd unit on every Linux host, and crosses a
strong trust boundary into tenant containers.

The agent **does not import** the server's `internal/*`. Its only
shared surface with the server is `pkg/orailtypes` and `proto/agent/v1`.

Conventions:

- Public state and reason codes come from `pkg/orailtypes` and
  `internal/failurecodes` (re-exported there).
- All long-running goroutines accept `context.Context` and shut down
  cleanly on cancellation; the supervisor honors a 10s shutdown deadline
  before a hard exit.
- The agent never writes deployment state (panel line 105 — agent reports
  observed instance state but cannot transition deployment state).

---

## Agent Lifecycle

```go
package agent

// Agent is the top-level type owning all host-side subsystems. One Agent
// per process; one process per host.
//
// Panel decision pinning:
//   line 86–98 — Linux runtime contract. Agent MUST refuse to start when
//   cgroup is not v2, when Docker Engine API is < v1.43, or when
//   AppArmor/SELinux profiles cannot be loaded.
//   line 94 — "systemd unit contract: every platform unit has
//   Restart=on-failure, RestartSec=5, StartLimitBurst=5,
//   StartLimitIntervalSec=300, OOMScoreAdjust=-900 for the agent."
//   line 94 — "Agent does not crash-loop on Docker absence; it enters
//   AGENT_RUNTIME_UNAVAILABLE with backoff."
type Agent struct {
    // Configuration loaded once at start; not reloaded from disk during
    // run (a SIGHUP triggers a controlled re-exec via systemd).
    Config Config

    // Subsystem handles populated during Register; nil before that.
    Docker      DockerClient
    Reconciler  Reconciler
    Logs        LogCollector
    Routes      RouteApplier
    Heart       HeartbeatLoop
    Networks    NetworkManager
    Volumes     VolumeManager
    Quotas      QuotaManager
    Drift       DriftDetector
    Asciicast   AsciicastRecorder
    Specs       ContainerSpecValidator
}

// Register dials the control plane, presents the agent-bootstrap token,
// reports CapabilityReport, and receives an AgentID + reconcile session
// token. Refused with AGENT_CLOCK_SKEW if the offset is > 30s
// (panel line 97).
func (a *Agent) Register(ctx context.Context) (orailtypes.ID, error)

// Heartbeat is the long-running heartbeat loop. Sends host_heartbeat
// every cfg.HeartbeatInterval (panel: agent reports clock offset). Re-
// connection uses jittered exponential backoff with randomized window
// (panel line 111: "reconcile-storm guard after control-plane outage").
func (a *Agent) HeartbeatLoop(ctx context.Context) error

// Reconcile is the long-running converge loop. Subscribes to the
// agent-control-stream gRPC, applies desired-state messages to the
// runtime, and reports observed state back. Cooperative cancellation
// at named safe points (panel line 109).
func (a *Agent) ReconcileLoop(ctx context.Context) error

// Shutdown stops accepting new work, drains in-flight reconciles to a
// safe point, flushes log buffers, and exits cleanly. The agent does
// NOT stop tenant containers — live-restore=true handles that
// (panel line 89: daemon.json baseline).
func (a *Agent) Shutdown(ctx context.Context) error
```

---

## DockerClient

```go
package agent

// DockerClient wraps the Docker Engine API client used by the agent.
//
// Panel decision pinning (line 90):
//   "Docker Engine API floor: v1.43 minimum (Engine ≥ 24.0); the agent
//    refuses to start with DOCKER_ENGINE_UNSUPPORTED outside the
//    supported range. The compatibility matrix is a published artifact,
//    not a promise — /docs/compat/docker.md and CI-gated."
//
// Implementations MUST perform version negotiation at construction
// time. If the negotiated API version is below v1.43, New returns
// DOCKER_ENGINE_UNSUPPORTED and the agent main() refuses to proceed.
//
// Implementations MUST verify the daemon.json baseline (panel line 89)
// at every Inspect cycle and surface drift via DriftDetector.
type DockerClient interface {
    // NegotiatedAPIVersion returns the version the client is using.
    NegotiatedAPIVersion() string

    // ServerInfo returns the live `docker system info` payload, which
    // the daemon-baseline drift check compares against the install
    // manifest (panel line 89).
    ServerInfo(ctx context.Context) (DaemonInfo, error)

    // CreateContainer applies a validated ContainerSpec. The spec MUST
    // have already passed ContainerSpecValidator.Validate (the
    // implementation re-validates as a defense-in-depth check).
    CreateContainer(ctx context.Context, spec ContainerSpec) (containerID string, err error)

    // StartContainer transitions the container to running. ECONNREFUSED
    // on the daemon socket returns AGENT_RUNTIME_UNAVAILABLE; the
    // caller must back off and retry (panel line 94).
    StartContainer(ctx context.Context, id string) error

    // StopContainer issues SIGTERM with the spec's stop_grace_period;
    // SIGKILL after grace expires.
    StopContainer(ctx context.Context, id string, grace time.Duration) error

    RemoveContainer(ctx context.Context, id string, opts RemoveOptions) error

    // Inspect returns the observed state. Used by the reconciler.
    Inspect(ctx context.Context, id string) (InspectResult, error)

    // Events streams Docker Engine events. The agent uses these for
    // hot-path container lifecycle observation; periodic Inspect is
    // still required because event streams drop on daemon restart.
    Events(ctx context.Context) (<-chan EngineEvent, error)

    // PullImage pulls a digest. Cosign verification happens in the
    // ImageRegistry layer BEFORE this call; a digest mismatch here is
    // a hard failure that emits IMAGE_PULL_FAILED.
    PullImage(ctx context.Context, ref ImageRef) error

    // CreateNetwork ensures a (project_id, environment_id) bridge
    // network exists with the project-scoped CIDR (panel line 163:
    // "agent runs a periodic invariant check that every Docker bridge's
    // connected containers share one (project_id, environment_id);
    // failure raises a P1 drift alert").
    CreateNetwork(ctx context.Context, n NetworkSpec) (string, error)
    RemoveNetwork(ctx context.Context, id string) error

    // CreateVolume / RemoveVolume manage volume objects. The agent's
    // VolumeManager handles ownership and quotas; this is the thin
    // Engine API call.
    CreateVolume(ctx context.Context, v VolumeSpec) (string, error)
    RemoveVolume(ctx context.Context, name string, force bool) error
}
```

---

## Reconciler (host-side)

```go
package agent

// Reconciler is the host-side desired ↔ observed converger. It receives
// desired-state messages from the control plane via gRPC, plans the
// minimal set of operations against DockerClient, and reports observed
// state back.
//
// Panel decision pinning:
//   line 105 — agent reports observed instance state but cannot
//   transition deployment state.
//   line 110 — LOST instance handling: route membership requires
//   instance.last_observed_at > now() - route_freshness_ttl (default
//   30s); below that, the instance is removed from the route. Re-add
//   requires N consecutive healthy heartbeats (hysteresis).
//   line 111 — reconcile-storm guard: jittered exponential backoff,
//   warmup mode after start.
type Reconciler interface {
    // Apply converges one DesiredInstance to the local runtime. Returns
    // the observed transition. Idempotent on retry. Cooperatively
    // cancellable (panel line 109).
    Apply(ctx context.Context, desired DesiredInstance) (Observed, error)

    // Drain begins SIGTERM-then-SIGKILL on a deployment's instances and
    // updates RouteApplier to remove the instance from the route.
    // Drain is a named safe point in the deployment FSM (panel
    // line 109: cancellation between route-add and drain-completion is
    // CANCEL_NOT_SAFE).
    Drain(ctx context.Context, depID orailtypes.ID, drain time.Duration) error

    // Reconvergence is invoked at agent start. It diff-replays the last
    // known desired-state cache (persisted to /var/lib/midnight/agent/
    // desired.json) against live runtime to recover from agent crashes
    // without depending on control-plane availability.
    Reconvergence(ctx context.Context) error

    // ObservedSnapshot returns the agent's current view of every
    // managed instance. Heartbeat batches this and ships it.
    ObservedSnapshot(ctx context.Context) ([]Observed, error)
}

// DesiredInstance is one per-host instruction. Composed of a fully
// validated ContainerSpec plus auxiliary route/volume/network refs.
type DesiredInstance struct {
    DeploymentID  orailtypes.ID
    InstanceID    orailtypes.ID
    Spec          ContainerSpec
    Routes        []RouteRef
    Volumes       []VolumeMount
    Networks      []NetworkRef
    MinReady      int
    DrainGrace    time.Duration
}

// Observed is one per-instance observation report.
type Observed struct {
    InstanceID       orailtypes.ID
    State            orailtypes.InstanceState // ALLOCATED, PULLING, … LOST
    LastObservedAt   time.Time
    ReadinessOK      bool
    HealthcheckState orailtypes.HealthState   // STARTUP / READINESS / LIVENESS state
    ExitCode         int
    OOMKilled        bool
    Restarts         int
    Reason           orailtypes.ReasonCode
}
```

---

## LogCollector

```go
package agent

// LogCollector captures container stdout/stderr from Docker's json-file
// driver, rotates on cap, and hands off to the structured-log indexer.
//
// Panel decision pinning:
//   line 89 — "log-driver: json-file with size/file caps".
//   line 95 — "journald drop-in shipped … Tenant logs route through
//   Docker json-file (not journald) as the source of truth."
//   line 197 — logrotate drop-in `/etc/logrotate.d/midnight` ships in
//   the .deb with documented defaults (100M, 14 days, 7 rotations,
//   copytruncate).
//
// The collector MUST never block container stdout/stderr; lossy is
// preferred to backpressure into the runtime.
type LogCollector interface {
    // Attach begins tailing logs for one container. Returns a handle
    // used to detach.
    Attach(ctx context.Context, containerID string, meta LogMeta) (Attachment, error)

    // Detach stops tailing and flushes pending lines.
    Detach(ctx context.Context, handle Attachment) error

    // RotateAndHandoff invokes the logrotate hand-off contract: when
    // the on-disk file rotates, the collector reopens, replays any
    // unread tail of the rotated file, and continues on the new file.
    RotateAndHandoff(ctx context.Context, handle Attachment) error

    // EmitNDJSON writes one structured log line to the agent's local
    // ndjson buffer. Shape locked by panel line 182:
    //   {ts, stream, level, msg, attrs}
    EmitNDJSON(ctx context.Context, line LogLine) error
}

type LogLine struct {
    TS      time.Time
    Stream  string // "stdout" | "stderr"
    Level   string // "info"|"warn"|"error"|"debug"|"trace"
    Msg     string
    Attrs   map[string]any
}
```

---

## RouteApplier

```go
package agent

// RouteApplier writes Traefik file-provider config files atomically
// (temp + rename, same filesystem).
//
// Panel decision pinning (line 159):
//   "Dynamic Traefik config files are written via temp-file + atomic
//    rename on the same filesystem; never truncated in place. Midnight
//    reserves /.well-known/acme-challenge/* at maximum router priority
//    on every entrypoint serving :80; user routes claiming this path
//    are rejected at validation."
//   line 110 — route membership freshness TTL drives Apply input.
//
// The applier owns /etc/midnight/traefik/dynamic/*.yaml. It does NOT
// own static config; the installer writes that.
type RouteApplier interface {
    // Apply writes the desired Traefik dynamic config for a given
    // (project_id, environment_id) scope. Atomic temp+rename. Returns
    // the file's post-write hash so the reconciler can confirm.
    Apply(ctx context.Context, scope RouteScope, cfg TraefikConfig) (Hash, error)

    // Remove deletes the dynamic config file for a scope (atomic). The
    // ACME challenge file is never removed by this method.
    Remove(ctx context.Context, scope RouteScope) error

    // EnsureACMEChallengeReserved writes the reserved
    // /.well-known/acme-challenge/* router at maximum priority on every
    // entrypoint serving :80. Idempotent. Re-applied on every agent
    // start so the reserved path cannot be dropped by drift.
    EnsureACMEChallengeReserved(ctx context.Context) error

    // VerifyAtomic re-reads the file post-write and confirms the hash;
    // surfaces ROUTE_RECONCILIATION_FAILED on mismatch (panel error
    // code list line 1457).
    VerifyAtomic(ctx context.Context, scope RouteScope, expected Hash) error
}
```

---

## ContainerSpec Validator

```go
package agent

// ContainerSpec is the host-bound container shape the agent submits to
// DockerClient.CreateContainer. EVERY field listed in the panel
// allowlist is required; absence is rejected by Validate.
//
// Panel decision pinning (lines 91, 93, 96 conjoined):
//   - capability allowlist (drop ALL, then add only:
//       CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID,
//       NET_BIND_SERVICE, SETPCAP, SYS_CHROOT)
//   - no privileged (Privileged=false)
//   - no host PID/network/IPC namespaces
//   - no-new-privileges always true
//   - HostConfig.Init = true (tini)
//   - explicit ulimits (nofile=65536:1048576, nproc=<pids-limit>)
//   - AppArmor profile = midnight-default (or docker-default fallback
//     if AppArmor unavailable)
//   - SELinux :z pass-through on platform-managed mounts when SELinux
//     is enabled (panel: SELinux promoted to Tier 2 supported in v1)
//
// Validate refuses any spec violating these constraints with
// CONTAINER_SPEC_INVALID.
type ContainerSpec struct {
    Image            ImageRef
    Cmd              []string
    Entrypoint       []string
    Env              []string  // already redacted; never carries plaintext sealed values
    Mounts           []Mount
    Networks         []NetworkRef
    Labels           map[string]string

    // Hardening (validator enforces these; defaults applied if zero)
    DropCaps         []string  // MUST contain "ALL"
    AddCaps          []string  // MUST be ⊆ panel allowlist
    NoNewPrivileges  bool      // MUST be true
    Init             bool      // MUST be true
    AppArmorProfile  string    // "midnight-default" or "docker-default"
    SELinuxLabel     string    // when applicable
    Privileged       bool      // MUST be false
    HostNamespaces   HostNS    // MUST all be false
    Ulimits          []Ulimit  // MUST include nofile + nproc
    PidsLimit        int64
    ReadOnlyRootfs   bool

    // Lifecycle
    StopGrace        time.Duration
    HealthCheck      HealthCheckSpec
}

// ContainerSpecValidator is the host-side allowlist enforcement seam.
type ContainerSpecValidator interface {
    Validate(ctx context.Context, spec ContainerSpec) error
}

// HostNS is the host-namespace flag set; ALL fields MUST be false.
type HostNS struct {
    PID     bool
    Network bool
    IPC     bool
}
```

---

## DriftReport + LOST Handling

```go
package agent

// DriftReport is the periodic per-host drift summary the agent ships in
// the heartbeat. Drift is anything where observed state diverges from
// the install manifest, the daemon.json baseline, the network
// invariants, or the desired-state cache.
//
// Panel decision pinning:
//   line 89 — daemon.json baseline drift detection.
//   line 96 — iptables-nft vs iptables-legacy mismatch and UFW
//   DOCKER-USER integration drift.
//   line 110 — LOST instance handling and route hysteresis.
//   line 162 — conntrack pressure is a required metric, not "where
//   available."
//   line 163 — periodic invariant check that every Docker bridge's
//   connected containers share one (project_id, environment_id).
type DriftReport struct {
    HostID            orailtypes.ID
    GeneratedAt       time.Time

    DaemonJSONDrift   *DaemonJSONDrift   // nil if clean
    IPTablesBackend   IPTablesBackend    // nft|legacy|mixed
    UFWIntegration    UFWStatus
    NetworkInvariants []NetworkInvariantViolation
    ConntrackPressure ConntrackPressure
    ClockOffset       time.Duration
    ContainerOrphans  []OrphanedContainer

    // LOST instances detected since the last heartbeat. The route
    // applier has already removed them from active routes per panel
    // line 110; this report announces the events.
    LostInstances     []LostEvent
}

// DriftDetector runs the periodic drift sweep.
type DriftDetector interface {
    Sweep(ctx context.Context) (DriftReport, error)

    // ObserveInstance updates the freshness window used by LostHandler.
    ObserveInstance(ctx context.Context, instID orailtypes.ID) error
}

// LostHandler implements the route-freshness TTL + hysteresis algorithm
// for instances that disappear from heartbeats. Pseudo-code follows the
// panel-locked rules.
//
// Panel decision pinning (line 110):
//   "LOST instance handling: route membership requires
//    instance.last_observed_at > now() - route_freshness_ttl (default
//    30s); below that, the instance is removed from the route but the
//    deployment stays ACTIVE if other instances remain. Zero ready
//    instances for degraded_grace > 0 produces a documented degraded
//    sub-state or FAILED with HOST_UNAVAILABLE. Re-add requires N
//    consecutive healthy heartbeats (hysteresis)."
type LostHandler interface {
    // OnHeartbeatTick advances the algorithm one tick. Returns the set
    // of instances whose membership changed (added or removed).
    OnHeartbeatTick(ctx context.Context, now time.Time) ([]MembershipChange, error)

    // OnObservation registers a fresh observation. Resets the LOST
    // counter for that instance and increments the consecutive-healthy
    // counter used by hysteresis.
    OnObservation(ctx context.Context, obs Observed, now time.Time) error
}

type LostEvent struct {
    InstanceID         orailtypes.ID
    DeploymentID       orailtypes.ID
    LastObservedAt     time.Time
    DurationLost       time.Duration
    RouteRemovedAt     time.Time
    ConsecutiveHealthy int  // for re-add hysteresis
}

// MembershipChange is the per-tick output of LostHandler.
type MembershipChange struct {
    DeploymentID  orailtypes.ID
    InstanceID    orailtypes.ID
    Before        bool  // was member of route?
    After         bool  // is member of route now?
    Reason        ChangeReason // FRESHNESS_TTL_EXPIRED | HYSTERESIS_RE_ADD
}

// ChangeReason enumerates the LOST-handling state transitions.
type ChangeReason string
const (
    ReasonFreshnessTTLExpired ChangeReason = "FRESHNESS_TTL_EXPIRED"
    ReasonHysteresisReAdd     ChangeReason = "HYSTERESIS_RE_ADD"
    ReasonHostUnavailable     ChangeReason = "HOST_UNAVAILABLE"
    ReasonGracePeriodExceeded ChangeReason = "GRACE_PERIOD_EXCEEDED"
)
```

---

## NetworkManager / VolumeManager / QuotaManager

```go
package agent

// NetworkManager owns per-(project, environment) bridge networks and
// enforces the panel invariant that one bridge contains one tenant
// scope (panel line 163).
type NetworkManager interface {
    EnsureBridge(ctx context.Context, scope RouteScope, cidr Cidr) (string, error)
    RemoveBridge(ctx context.Context, scope RouteScope) error
    InvariantCheck(ctx context.Context) ([]NetworkInvariantViolation, error)

    // EnsureSMTPDeny installs the panel-locked nftables rule blocking
    // outbound 25/465/587 in DOCKER-USER (panel line 162). Per-workspace
    // allowlist additions are applied here.
    EnsureSMTPDeny(ctx context.Context, allowlist []string) error
}

// VolumeManager owns volume directories and ownership negotiation.
//
// Panel decision pinning (line 140):
//   "Volume directories owned by root:midnight, mode 0700, until first
//    mount. On first mount the agent reads the template's expected
//    container UID/GID and chowns appropriately, computing mapped host
//    UID via /etc/subuid if userns-remap is enabled. UID conflict
//    produces OWNERSHIP_CONFLICT and blocks mount."
type VolumeManager interface {
    Provision(ctx context.Context, v VolumeSpec) (VolumeRef, error)
    EnsureOwnership(ctx context.Context, ref VolumeRef, expectedUID, expectedGID int) error
    Mount(ctx context.Context, ref VolumeRef, containerID string, mountpoint string) error
    Unmount(ctx context.Context, ref VolumeRef, containerID string) error
    SoftDelete(ctx context.Context, ref VolumeRef, grace time.Duration) error
    ResizeOnline(ctx context.Context, ref VolumeRef, newSize Bytes) error // shrink unsupported
}

// QuotaManager enforces XFS/ext4 project quotas (panel line 146:
// prjquota mount option required).
type QuotaManager interface {
    EnsurePrjquotaMount(ctx context.Context) error
    SetVolumeQuota(ctx context.Context, ref VolumeRef, soft, hard Bytes) error
    SetPlatformSubtreeQuotas(ctx context.Context) error  // BuildKit cache + registry blobs
    Usage(ctx context.Context, ref VolumeRef) (Usage, error)
}
```

---

## AsciicastRecorder

```go
package agent

// AsciicastRecorder records midnight shell / ssh sessions as
// asciicast-equivalent streams.
//
// Panel decision pinning (line 131):
//   "midnight shell/ssh sessions are recorded as asciicast-equivalent
//    streams stored with audit retention, surfaced in the deployment
//    timeline, redacted by the same pipeline; cannot be disabled.
//    Production-tier sessions require two-person approval."
//
// The recorder pipes session bytes through RedactionPipeline.RedactStream
// before writing to disk. The "cannot be disabled" rule is enforced by
// refusing to start a session if the recorder reports unhealthy.
type AsciicastRecorder interface {
    // Begin opens a new asciicast file under
    //   /var/lib/midnight/sessions/<deployment_id>/<session_id>.cast
    // and returns a writer that the shell/ssh adapter pipes through.
    Begin(ctx context.Context, sess SessionMeta) (Recording, error)

    // End finalizes the file (writes the trailing manifest line) and
    // ships it to the audit storage system.
    End(ctx context.Context, rec Recording) error

    // Health returns ok if the recorder has a writable target with
    // sufficient disk and the redaction pipeline is initialized.
    // Sessions refuse to start when Health returns an error.
    Health(ctx context.Context) error
}
```

---

## HeartbeatLoop

```go
package agent

// HeartbeatLoop ships periodic Heartbeat messages to the control plane.
//
// Panel decision pinning:
//   line 97 — agent reports clock offset; refuses ACME ops if
//   |offset| > 5s; control plane refuses registration with
//   AGENT_CLOCK_SKEW if |offset| > 30s.
//   line 110 — last_observed_at drives route membership freshness.
type HeartbeatLoop interface {
    Tick(ctx context.Context, now time.Time) (Heartbeat, error)
    Run(ctx context.Context) error
    LastClockOffset() time.Duration
}

type Heartbeat struct {
    AgentID       orailtypes.ID
    HostID        orailtypes.ID
    SentAt        time.Time
    ClockOffset   time.Duration
    Drift         DriftReport
    Observed      []Observed
    SchemaVersion int
}
```

---

## Doc-Comment Style

Every interface above includes the panel decision quote pinning it.
Engineering MUST preserve this comment verbatim when moving the
interface to its package; the agent's small footprint and high blast
radius make per-line traceability to panel decisions a release-blocker
audit artifact, not a nicety.
