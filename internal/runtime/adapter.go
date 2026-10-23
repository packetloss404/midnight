package runtime

import (
	"context"
	"time"
)

// AdapterMajorVersion is the major version of the RuntimeAdapter interface
// shape, per ADR-015. Adapter authors and the conformance suite use this to
// refuse adapters that target an older boundary.
const AdapterMajorVersion = 1

// InstanceState enumerates the panel-locked agent-observed instance states.
// These are independent of public deployment state and are owned by the agent
// (FEATURE_DEPLOYMENT_STATE_MACHINE.md and INTERFACES_AGENT.md).
type InstanceState string

const (
	InstanceStateAllocated  InstanceState = "ALLOCATED"
	InstanceStatePulling    InstanceState = "PULLING"
	InstanceStateCreating   InstanceState = "CREATING"
	InstanceStateStarting   InstanceState = "STARTING"
	InstanceStateRunning    InstanceState = "RUNNING"
	InstanceStateReady      InstanceState = "READY"
	InstanceStateUnhealthy  InstanceState = "UNHEALTHY"
	InstanceStateCrashed    InstanceState = "CRASHED"
	InstanceStateRestarting InstanceState = "RESTARTING"
	InstanceStateDraining   InstanceState = "DRAINING"
	InstanceStateStopped    InstanceState = "STOPPED"
	InstanceStateRemoved    InstanceState = "REMOVED"
	InstanceStateLost       InstanceState = "LOST"
)

// ResourceKind enumerates runtime objects an adapter manages on behalf of the
// platform. The conformance suite asserts every Midnight-managed resource
// carries the canonical label set keyed by these resource kinds.
type ResourceKind string

const (
	ResourceContainer ResourceKind = "container"
	ResourceNetwork   ResourceKind = "network"
	ResourceVolume    ResourceKind = "volume"
)

// ScheduleRequest is the boundary input that maps a desired deployment
// instance to one runtime-side intent. It is the top-half of the
// `BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget`
// adapter boundary called out by ADR-015.
type ScheduleRequest struct {
	// DeploymentID and InstanceID identify the desired instance.
	DeploymentID string
	InstanceID   string
	// ProjectID and EnvironmentID scope the instance for tenant invariants.
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	// ServiceSlug, ProjectSlug, EnvironmentName mirror the human-friendly
	// names assigned by the control plane. They are optional inputs; when
	// blank the adapter falls back to the corresponding id. These power
	// the per-service DNS alias and the MIDNIGHT_* built-in env vars.
	ServiceSlug     string
	ProjectSlug     string
	EnvironmentName string
	// Image is the verified, digest-pinned reference the adapter must run.
	Image string
	// StartCommand is an optional operator-defined shell command. The Docker
	// adapter runs it through /bin/sh -lc as the container entrypoint.
	StartCommand string
	// TargetPort is the application port exposed to private routing. Zero is
	// normalized by adapters to their documented default.
	TargetPort int
	// TargetPortInferred reports that TargetPort is the platform default
	// rather than an operator choice. Adapters may then prefer the port the
	// image itself declares (a single EXPOSE) so images such as Redis or
	// Postgres become ready without hand-configured runtime settings.
	TargetPortInferred bool
	// RestartPolicy and RestartMaxRetries are the durable runtime restart
	// policy. Supported names are no, always, unless-stopped, and on-failure.
	RestartPolicy     string
	RestartMaxRetries int
	// Labels carries the canonical midnight.* label set; adapters MUST apply
	// these labels to every managed resource produced for the request.
	Labels map[string]string
	// Env carries the resolved environment in KEY=VALUE form. The deploy
	// executor renders the variable-reference templates from internal/varrefs
	// and converts the resulting map into this slice; the adapter then
	// prepends MIDNIGHT_* built-ins so user entries can override the auto-
	// injected values. See internal/network.BuildBuiltins for the canonical
	// list of built-ins.
	Env []string
	// PublicDomain is the assigned public route hostname for the service,
	// or empty when no public route has been promoted yet. The adapter
	// surfaces it via MIDNIGHT_PUBLIC_DOMAIN.
	PublicDomain string
	// IdempotencyKey lets callers detect duplicate Schedule calls without
	// requiring the adapter to scan resource state.
	IdempotencyKey string
	// Replicas is the desired service replica count. The current local
	// scheduler normalizes zero to one; runtime adapters fail closed when a
	// single-writer mount is combined with a value greater than one.
	Replicas int
	// Mounts is the optional volume-mount slice the adapter folds into the
	// runtime container/pod definition. Each entry is materialized BEFORE
	// the adapter call (the volume Manager has already ensured the host-
	// side volume exists), so the adapter only translates intent into the
	// underlying SDK shape (HostConfig.Mounts for Docker, PodSpec.Volumes
	// for Kubernetes, etc.).
	Mounts []ScheduleMount
}

// OneOffRequest is an immutable, non-service workload executed from the same
// image and tenant network as a deployment candidate. It deliberately embeds
// ScheduleRequest so adapters reuse the exact resolved environment and
// MIDNIGHT_* built-ins while enforcing one-off-specific constraints: no
// persistent mounts and restart policy "no".
//
// SpecSHA256 is the durable idempotency identity. Runtime adapters MUST use a
// deterministic resource name derived from DeploymentID + SpecSHA256 and MUST
// observe an existing terminal resource instead of starting it again.
type OneOffRequest struct {
	ScheduleRequest ScheduleRequest
	// WorkloadKind distinguishes retained one-off resources such as
	// "predeploy" and "cron". Empty retains the original predeploy default.
	WorkloadKind string
	Command      string
	SpecSHA256   string
	Timeout      time.Duration
	// Deadline is derived from the durable first-start timestamp. Retries use
	// the same absolute deadline rather than granting a fresh timeout window.
	Deadline time.Time
}

// OneOffResult is terminal evidence from a one-off workload. ContainerName
// and ContainerID are metadata only and safe to persist. A timeout is
// terminal for the deployment even when Docker reports a signal-derived exit
// code after the worker stops the container.
type OneOffResult struct {
	ContainerName string
	ContainerID   string
	ExitCode      int
	TimedOut      bool
}

// ScheduleMount is one host-side mount the adapter applies when creating
// the container. Source is the runtime-specific resource name (e.g. the
// Docker volume name); Target is the container path. The volume Manager
// produces these entries from the volume_attachments table.
type ScheduleMount struct {
	// VolumeID is the durable control-plane id. Source is derived from it by
	// the volume manager and both values are checked again at runtime attach.
	VolumeID string
	Source   string
	Target   string
	ReadOnly bool
	IsVolume bool
}

// InstanceStatus reports an adapter's last-observed state for one instance.
// StreamInstanceStatus implementations MUST emit monotonic transitions; the
// conformance suite asserts no invalid backward transitions occur.
type InstanceStatus struct {
	DeploymentID string
	InstanceID   string
	State        InstanceState
	ObservedAt   time.Time
	Reason       string
}

// InstanceLiveness is optional evidence about why an instance is not ready
// yet. Readiness polling is a waiting game, and waiting only pays off while
// the workload can still become ready: a container that already exited, or
// one the restart policy keeps resurrecting, never will. Adapters that can
// inspect the workload report that here so callers stop waiting immediately
// instead of dialing a dead port until their timeout elapses.
//
// Observed is false when the adapter has no evidence (the instance is
// unknown, or the inspect call failed). Callers MUST treat an unobserved
// liveness as "keep waiting" and never as a failure.
type InstanceLiveness struct {
	DeploymentID string
	InstanceID   string
	// Observed reports the adapter actually inspected the workload.
	Observed bool
	State    InstanceState
	// Exited reports the workload stopped and nothing is bringing it back.
	Exited bool
	// RestartLooping reports the workload keeps exiting and being restarted
	// by its runtime restart policy, so it cannot settle into readiness.
	RestartLooping bool
	// ExitCode and OOMKilled describe the last termination. They are only
	// meaningful when Exited or RestartLooping is true.
	ExitCode  int
	OOMKilled bool
	// Restarts is the runtime-reported restart count for the instance.
	Restarts int
	// Reason is a short human-readable description of the last termination.
	Reason string
	// LogTail is a bounded slice of the workload's own most recent output,
	// oldest first. It is best-effort: adapters that cannot read logs leave
	// it empty. Callers MUST treat entries as untrusted workload output.
	LogTail    []string
	ObservedAt time.Time
}

// InstanceLivenessObserver is the optional adapter capability that reports
// terminal or restart-looping instances. It is deliberately not part of
// RuntimeAdapter: adapters that cannot cheaply inspect a workload simply do
// not implement it, and callers fall back to plain timeout behavior.
type InstanceLivenessObserver interface {
	ObserveInstanceLiveness(ctx context.Context, deploymentID, instanceID string) (InstanceLiveness, error)
}

// ImagePortReporter is the optional adapter capability that reports the TCP
// ports an image declares for itself (Docker EXPOSE). An image that declares
// no port and has no operator-chosen port cannot be probed meaningfully, so
// callers use this to decide whether a port-based readiness probe is even a
// question the image can answer.
type ImagePortReporter interface {
	ImageDeclaredPorts(ctx context.Context, image string) ([]int, error)
}

// RoutingTarget identifies one ingress-routable target produced by an
// adapter. RouteApplier consumers depend on these fields to build Traefik
// configuration (INTERFACES_AGENT.md, S06 contract).
type RoutingTarget struct {
	DeploymentID    string
	InstanceID      string
	HostID          string
	RoutingTargetID string
	ContainerID     string
	ContainerIP     string
	InternalPort    int
	ProjectID       string
	EnvironmentID   string
	Ready           bool
	ObservedAt      time.Time
}

// NetworkPolicy describes the per-(project, environment) bridge intent the
// adapter must apply. The conformance suite asserts cross-tenant attachment
// is rejected.
type NetworkPolicy struct {
	ProjectID     string
	EnvironmentID string
	BridgeName    string
	Subnet        string
	Gateway       string
}

// VolumeAttachment describes one volume attachment intent.
type VolumeAttachment struct {
	DeploymentID string
	InstanceID   string
	VolumeName   string
	MountPath    string
	ReadOnly     bool
}

// ObservedVolumeAttachment is live runtime evidence recovered from a running
// managed container. It is intentionally optional on RuntimeAdapter; the
// single-host Docker worker uses it to reconcile Postgres attachment truth
// after a process restart.
type ObservedVolumeAttachment struct {
	VolumeID      string
	DeploymentID  string
	InstanceID    string
	ProjectID     string
	ServiceID     string
	EnvironmentID string
	Source        string
	MountPath     string
	ReadOnly      bool
}

// PullVerifyResult records whether image signature verification succeeded
// before the adapter performed a pull. Default-profile adapters MUST refuse
// pulls when Verified is false and Trusted is false.
type PullVerifyResult struct {
	Image    string
	Digest   string
	Verified bool
	Trusted  bool
	Reason   string
}

// AdapterCapabilities advertises optional features per ADR-015 §5. The
// boundary intentionally ships with only required methods on RuntimeAdapter;
// optional behaviors are queried via Capabilities().
type AdapterCapabilities struct {
	Name                string
	Version             string
	Major               int
	PlanningOnly        bool
	GPUScheduling       bool
	MicroVMIsolation    bool
	ReplicasSharedVol   bool
	NetworkPolicy       bool
	SignatureEnforce    bool
	IdempotentSchedule  bool
	ConformanceVerified bool
}

// RuntimeAdapter is the abstract boundary every Midnight runtime backend
// implements. ADR-015 ratifies this interface as the v1 boundary; phase 3
// adapters (containerd, Kubernetes, Nomad) MUST implement it without
// modification.
//
// Implementations MUST be:
//   - Idempotent on Schedule when the IdempotencyKey is reused.
//   - Cancellable via context at named safe points.
//   - Side-effect free in dry-run modes (the local adapter records intents
//     to memory rather than touching real resources).
type RuntimeAdapter interface {
	// Capabilities returns the adapter's declared feature set.
	Capabilities() AdapterCapabilities
	// Schedule converges one ScheduleRequest. Idempotent on retries with the
	// same IdempotencyKey.
	Schedule(ctx context.Context, req ScheduleRequest) (InstanceStatus, error)
	// Start transitions a previously-scheduled instance into the running
	// state.
	Start(ctx context.Context, deploymentID, instanceID string) error
	// Stop sends SIGTERM with the adapter's stop grace period.
	Stop(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error
	// Remove releases all adapter resources for the instance.
	Remove(ctx context.Context, deploymentID, instanceID string) error
	// Drain marks the instance as draining and removes it from active
	// routing. Used by the cooperative cancellation safe point set.
	Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error
	// StreamInstanceStatus returns the latest observed status for every
	// adapter-managed instance.
	StreamInstanceStatus(ctx context.Context) ([]InstanceStatus, error)
	// Reconcile performs a no-op-on-converged sweep against desired state.
	Reconcile(ctx context.Context) error
	// ApplyNetworkPolicy ensures the per-tenant bridge is configured.
	ApplyNetworkPolicy(ctx context.Context, policy NetworkPolicy) error
	// ResolveRoutingTarget returns the routable target for one instance.
	ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (RoutingTarget, error)
	// AttachVolume binds a volume to the instance.
	AttachVolume(ctx context.Context, attach VolumeAttachment) error
	// DetachVolume releases a previously-attached volume.
	DetachVolume(ctx context.Context, deploymentID, instanceID, volumeName string) error
	// PullVerify resolves an image reference to a digest and verifies the
	// signature against the configured trust policy. Adapters MUST run this
	// before any image pull.
	PullVerify(ctx context.Context, image string) (PullVerifyResult, error)
}
