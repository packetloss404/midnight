package scheduler

// This file introduces the deployment-execution Scheduler boundary, separate
// from the deterministic Plan() / PlanStatelessService() planners that live in
// scheduler.go and multihost.go. The Plan*() functions answer "what would a
// fair scheduler decide?" without side effects. The Scheduler interface below
// answers "make this deployment exist on a host, right now".
//
// ADR-0018 records the migration path:
//
//   - Today: LocalDockerScheduler (internal/scheduler/local) drives the local
//     Docker daemon on the worker process. The control plane and worker run
//     on the same host. This is what executor.go used to call into directly.
//   - Tomorrow: MultiHostScheduler (internal/scheduler/multihost) picks a
//     host by capability/label/capacity using the existing hosts +
//     host_heartbeats tables, writes desired-state rows that the existing
//     /api/v1alpha/agents/{id}/desired endpoint will return to remote
//     midnight-agent binaries, and reconciles status via the existing
//     /api/v1alpha/agents/{id}/status endpoint.
//
// Callers (today: internal/deploy.Executor) MUST NOT know which Scheduler is
// wired. The interface is the seam that lets the multi-host implementation
// drop in without re-plumbing the deploy intake or the operations FSM.

import (
	"context"
	"errors"
	"time"

	"github.com/midnight/midnight/internal/runtime"
)

// ErrNotImplemented is returned by stubbed scheduler methods (e.g. the
// MultiHostScheduler's Reconcile and Drain) so callers can fail loudly
// instead of silently falling back to the local scheduler.
var ErrNotImplemented = errors.New("scheduler: operation not implemented for this backend")

// ScheduleIntent is the input to Scheduler.Schedule. It is intentionally a
// superset of runtime.ScheduleRequest so the LocalDockerScheduler can forward
// the embedded ScheduleRequest to the docker adapter unchanged, while the
// MultiHostScheduler has the additional metadata it needs for placement
// (RequiredCapabilities, RouteHint, etc.).
type ScheduleIntent struct {
	// OperationID ties the schedule call back to the operations FSM so the
	// scheduler can record evidence on the same row the deploy intake created.
	OperationID string
	// ScheduleRequest is the runtime-level intent the local adapter consumes
	// directly. It carries the verified digest-pinned image, the canonical
	// label set, and the idempotency key.
	ScheduleRequest runtime.ScheduleRequest
	// RequiredCapabilities is the placement filter the MultiHostScheduler
	// applies against host labels. The default contract is
	//   {"runtime": "docker"}
	// which matches the only adapter shipping today. Additional keys (e.g.
	// "gpu": "true", "region": "us-east") mirror the Railway region/GPU/RAM
	// tier matching the platform plan calls out.
	RequiredCapabilities map[string]string
	// PreviousActive identifies the active instance the new candidate will
	// replace once promoted. It is forwarded to Drain() if non-empty.
	PreviousActive ActiveInstance
	// StepObserver, if non-nil, receives a callback at every sub-step
	// boundary inside Schedule (pull-verify, schedule, start, resolve-
	// routing). The executor uses this to drive its per-step telemetry
	// without splitting the scheduler call into four round-trips.
	StepObserver StepObserver
}

// ScheduleStep identifies one named sub-step inside Scheduler.Schedule. The
// LocalDockerScheduler emits one Started + one Succeeded/Failed pair per
// step in the order listed below.
type ScheduleStep string

const (
	ScheduleStepPullVerify     ScheduleStep = "pull-verify"
	ScheduleStepSchedule       ScheduleStep = "schedule"
	ScheduleStepStart          ScheduleStep = "start"
	ScheduleStepResolveRouting ScheduleStep = "resolve-routing"
)

// ScheduleStepState is the lifecycle phase of one ScheduleStep emission.
type ScheduleStepState string

const (
	ScheduleStepStarted   ScheduleStepState = "started"
	ScheduleStepSucceeded ScheduleStepState = "succeeded"
	ScheduleStepFailed    ScheduleStepState = "failed"
)

// StepObserver is the optional callback the scheduler invokes at every sub-
// step boundary. The signature is intentionally simple so callers can adapt
// it to their own telemetry shape (e.g. internal/deploy.DeployStepEvent).
type StepObserver interface {
	ObserveScheduleStep(step ScheduleStep, state ScheduleStepState, err error)
}

// StepObserverFunc adapts a plain function to the StepObserver interface.
type StepObserverFunc func(step ScheduleStep, state ScheduleStepState, err error)

// ObserveScheduleStep implements StepObserver.
func (f StepObserverFunc) ObserveScheduleStep(step ScheduleStep, state ScheduleStepState, err error) {
	if f != nil {
		f(step, state, err)
	}
}

// ActiveInstance is the (deployment, instance) pair the executor passes to
// Drain when retiring the previous active version after a successful
// promotion. The scheduler-side type is intentionally separate from
// deploy.ActiveInstanceRef so the deploy package can keep its job-payload
// shape while the scheduler interface stays runtime-neutral.
type ActiveInstance struct {
	DeploymentID string
	InstanceID   string
}

// HasPrevious reports whether the active reference is fully populated.
func (a ActiveInstance) HasPrevious() bool {
	return a.DeploymentID != "" && a.InstanceID != ""
}

// HostAssignment is one host that the scheduler selected for a placement.
// LocalDockerScheduler always returns exactly one HostAssignment with the
// local host's id ("local" by default). MultiHostScheduler returns the host
// id it wrote desired state for; future replica-aware scheduling can return
// multiple HostAssignments without changing the interface.
type HostAssignment struct {
	HostID string
	// Capabilities echoes the labels the scheduler matched against. The
	// MultiHostScheduler populates this from the hosts.labels JSON column;
	// the LocalDockerScheduler reports the local adapter capabilities (e.g.
	// {"runtime": "docker"}).
	Capabilities map[string]string
}

// DeploymentPlacement is the result of Scheduler.Schedule. It is named
// DeploymentPlacement (not Placement) to avoid colliding with the existing
// scheduler.Placement type emitted by PlanStatelessService(); the two
// answer different questions (planning vs. live execution).
type DeploymentPlacement struct {
	// DeploymentID and InstanceID echo the ScheduleRequest so callers do
	// not have to re-thread them from the intent.
	DeploymentID string
	InstanceID   string
	// Assignments is the set of hosts the scheduler picked. Always
	// non-empty on success.
	Assignments []HostAssignment
	// InstanceStatus is the runtime adapter's observed state at the end of
	// Schedule. LocalDockerScheduler reports the live state; MultiHostScheduler
	// reports a "pending agent ack" placeholder until the agent reports back.
	InstanceStatus runtime.InstanceStatus
	// PullVerify is the signature/trust verification result. The executor
	// uses this to drive its pull-verify telemetry step without having to
	// reach into the runtime adapter directly.
	PullVerify runtime.PullVerifyResult
	// RoutingTarget is the routable container address. LocalDockerScheduler
	// resolves this synchronously after Start; MultiHostScheduler returns a
	// zero value and the caller must use State() to poll until the agent
	// reports the routing target via the /agents/{id}/status callback.
	RoutingTarget runtime.RoutingTarget
}

// PrimaryHostID returns the first assigned host id, or "" if none.
func (p DeploymentPlacement) PrimaryHostID() string {
	if len(p.Assignments) == 0 {
		return ""
	}
	return p.Assignments[0].HostID
}

// DeploymentState reports the scheduler's current view of one deployment. It
// is the read-side counterpart to Schedule and is what the executor's planner
// consumes when reconstructing CandidateActivationPlan after a worker restart.
type DeploymentState struct {
	DeploymentID  string
	InstanceID    string
	HostID        string
	RuntimeStatus runtime.InstanceStatus
	RoutingTarget runtime.RoutingTarget
}

// ReconcileResult summarizes what the scheduler converged on this pass. The
// MultiHostScheduler returns ErrNotImplemented until the agent-status
// callback path lands; LocalDockerScheduler delegates to the docker adapter's
// label-based recovery.
type ReconcileResult struct {
	DeploymentID string
	InstanceID   string
	Converged    bool
	Status       runtime.InstanceStatus
}

// Scheduler is the deployment-execution boundary. Implementations:
//
//   - MUST be idempotent on Schedule when ScheduleRequest.IdempotencyKey is
//     reused.
//   - MUST respect ctx cancellation at named safe points (after pull-verify,
//     before promote — the executor enforces this via CancelChecker).
//   - MUST NOT perform route promotion. The route promoter call stays in the
//     deploy executor; the scheduler returns the RoutingTarget the promoter
//     needs.
//
// Today: LocalDockerScheduler wraps internal/runtime/docker.Adapter.
// Tomorrow: MultiHostScheduler reuses the existing /api/v1alpha/agents/*
// endpoints to dispatch work to remote midnight-agent binaries.
type Scheduler interface {
	// Schedule selects host(s) for the intent and writes the desired state
	// the runtime (local adapter or remote agent) will converge to.
	Schedule(ctx context.Context, intent ScheduleIntent) (DeploymentPlacement, error)
	// Reconcile re-asserts the scheduler's desired state for a deployment.
	// LocalDockerScheduler delegates to the docker adapter's Reconcile;
	// MultiHostScheduler will re-push the desired state to the agent and
	// surface the agent's last status report.
	Reconcile(ctx context.Context, deploymentID string) (ReconcileResult, error)
	// Drain marks the deployment as draining and removes it from active
	// routing. The executor calls this once the candidate is promoted.
	Drain(ctx context.Context, deploymentID string, grace time.Duration) error
	// State returns the scheduler's current view of the deployment. The
	// executor uses this to rebuild a CandidateActivationPlan after a worker
	// restart, and to verify the routing target before promotion.
	State(ctx context.Context, deploymentID string) (DeploymentState, error)
}
