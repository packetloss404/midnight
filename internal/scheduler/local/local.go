// Package local implements the single-host LocalDockerScheduler used today.
//
// LocalDockerScheduler is the byte-identical replacement for the direct
// runtime-adapter calls that internal/deploy/executor.go used to make. It
// owns the host-side mechanics (pull-verify, schedule, start, resolve
// routing target, drain) and delegates them to the runtime adapter
// (internal/runtime/docker.Adapter in production; internal/runtime/local in
// tests / dry-run profiles).
//
// The route promoter call stays in internal/deploy/executor.go — see ADR-0018
// for why the scheduler does not own promotion: the executor is the single
// place that owns the operations FSM and route promotion is one step of that
// FSM, not a scheduler primitive.
package local

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
)

const (
	// DefaultHostID is the host id LocalDockerScheduler reports when the
	// caller does not configure one. It mirrors the worker's HostID
	// default and is intentionally not "localhost" so the multi-host
	// scheduler can use the same id space.
	DefaultHostID = "local"

	// DefaultRuntimeCapability is the only capability the local scheduler
	// advertises. Multi-host placement matches the same key/value pair.
	DefaultRuntimeCapability = "docker"
)

// Runtime is the adapter-level boundary LocalDockerScheduler depends on. It
// is intentionally the same shape as deploy.ActivationRuntime so the existing
// runtime adapters (docker, local) can be reused without changes. The
// deploy package wires its ActivationRuntime through this scheduler instead
// of calling the runtime adapter directly.
type Runtime interface {
	PullVerify(ctx context.Context, image string) (runtime.PullVerifyResult, error)
	Schedule(ctx context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error)
	Start(ctx context.Context, deploymentID, instanceID string) error
	ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error)
	Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error
}

// LocalDockerScheduler implements scheduler.Scheduler against a single
// host's runtime adapter. Its behavior is byte-identical to the pre-ADR-0018
// executor: pull-verify, schedule, start, resolve routing target, return.
// The executor still owns the cancel check + route promotion + drain
// trigger.
type LocalDockerScheduler struct {
	// Runtime is the runtime adapter LocalDockerScheduler drives. Required.
	Runtime Runtime
	// HostID is the host id reported on placements. Defaults to DefaultHostID.
	HostID string
	// Capabilities echoes the host capability set in placements. Defaults
	// to {"runtime": "docker"} which matches the only adapter shipping
	// today.
	Capabilities map[string]string
}

var (
	// Compile-time confirmation that LocalDockerScheduler satisfies the
	// scheduler.Scheduler boundary.
	_ scheduler.Scheduler = (*LocalDockerScheduler)(nil)

	// ErrRuntimeRequired is returned when the scheduler is invoked without
	// a runtime adapter wired in.
	ErrRuntimeRequired = errors.New("local docker scheduler: runtime is required")
)

// New constructs a LocalDockerScheduler with the supplied runtime adapter.
// Callers should prefer this over a struct literal so the default capability
// set and host id are applied consistently.
func New(rt Runtime) *LocalDockerScheduler {
	return &LocalDockerScheduler{Runtime: rt}
}

// Schedule executes the host-side activation sequence the executor used to
// drive directly: PullVerify → Schedule → Start → ResolveRoutingTarget. The
// returned DeploymentPlacement carries the PullVerify result and the
// RoutingTarget so the executor can drive its readiness / route-promotion
// telemetry without reaching back into the runtime adapter.
//
// Behavior MUST be byte-identical to the pre-refactor executor flow. The
// executor_test.go suite (now in internal/scheduler/local/local_test.go) is
// the regression contract.
func (s *LocalDockerScheduler) Schedule(ctx context.Context, intent scheduler.ScheduleIntent) (scheduler.DeploymentPlacement, error) {
	if s == nil || s.Runtime == nil {
		return scheduler.DeploymentPlacement{}, ErrRuntimeRequired
	}
	req := intent.ScheduleRequest
	placement := scheduler.DeploymentPlacement{
		DeploymentID: req.DeploymentID,
		InstanceID:   req.InstanceID,
		Assignments: []scheduler.HostAssignment{{
			HostID:       s.hostID(),
			Capabilities: s.capabilities(),
		}},
	}
	observe := func(step scheduler.ScheduleStep, state scheduler.ScheduleStepState, err error) {
		if intent.StepObserver != nil {
			intent.StepObserver.ObserveScheduleStep(step, state, err)
		}
	}
	observe(scheduler.ScheduleStepPullVerify, scheduler.ScheduleStepStarted, nil)
	pull, err := s.Runtime.PullVerify(ctx, req.Image)
	placement.PullVerify = pull
	if err != nil {
		observe(scheduler.ScheduleStepPullVerify, scheduler.ScheduleStepFailed, err)
		return placement, err
	}
	if !pull.Verified || !pull.Trusted {
		// Mirror the executor's pre-refactor contract: PullVerify failures
		// surface as nil errors from the runtime adapter; the executor
		// inspects Verified/Trusted itself and emits the typed failure code.
		// We deliberately do NOT emit a step event here — the executor
		// classifies the verification failure and emits its own step.
		return placement, nil
	}
	observe(scheduler.ScheduleStepPullVerify, scheduler.ScheduleStepSucceeded, nil)
	observe(scheduler.ScheduleStepSchedule, scheduler.ScheduleStepStarted, nil)
	status, err := s.Runtime.Schedule(ctx, req)
	if err != nil {
		observe(scheduler.ScheduleStepSchedule, scheduler.ScheduleStepFailed, err)
		return placement, err
	}
	placement.InstanceStatus = status
	observe(scheduler.ScheduleStepSchedule, scheduler.ScheduleStepSucceeded, nil)
	observe(scheduler.ScheduleStepStart, scheduler.ScheduleStepStarted, nil)
	if err := s.Runtime.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		observe(scheduler.ScheduleStepStart, scheduler.ScheduleStepFailed, err)
		return placement, err
	}
	observe(scheduler.ScheduleStepStart, scheduler.ScheduleStepSucceeded, nil)
	observe(scheduler.ScheduleStepResolveRouting, scheduler.ScheduleStepStarted, nil)
	target, err := s.Runtime.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		observe(scheduler.ScheduleStepResolveRouting, scheduler.ScheduleStepFailed, err)
		return placement, err
	}
	placement.RoutingTarget = target
	observe(scheduler.ScheduleStepResolveRouting, scheduler.ScheduleStepSucceeded, nil)
	return placement, nil
}

// Reconcile delegates to the runtime adapter when it exposes Reconcile(ctx).
// The docker adapter's Reconcile is label-based and idempotent; the local
// in-memory adapter exposes the same method. The result is intentionally
// minimal because today's executor reconstructs its plan from the
// CandidateActivationRequest, not from the scheduler's Reconcile output.
func (s *LocalDockerScheduler) Reconcile(ctx context.Context, deploymentID string) (scheduler.ReconcileResult, error) {
	if s == nil || s.Runtime == nil {
		return scheduler.ReconcileResult{}, ErrRuntimeRequired
	}
	type reconciler interface {
		Reconcile(context.Context) error
	}
	r, ok := s.Runtime.(reconciler)
	if !ok {
		return scheduler.ReconcileResult{DeploymentID: deploymentID, Converged: true}, nil
	}
	if err := r.Reconcile(ctx); err != nil {
		return scheduler.ReconcileResult{DeploymentID: deploymentID}, err
	}
	return scheduler.ReconcileResult{DeploymentID: deploymentID, Converged: true}, nil
}

// Drain forwards to the runtime adapter. The executor calls this when the
// candidate is promoted and the previous active instance needs to be
// drained; the DrainGrace from the executor is forwarded unchanged.
//
// Drain requires both a deployment id and an instance id to be unambiguous.
// The single-host adapter expects the executor to format the deployment id
// as "<deployment>/<instance>" when calling Drain; if no slash is present
// the scheduler treats deploymentID as the instance id (preserving the
// pre-refactor call shape where executor passed both ids explicitly).
//
// Callers that need precise (deployment, instance) drains should use
// DrainInstance below; this Scheduler-interface variant exists so the
// scheduler.Scheduler boundary stays runtime-neutral.
func (s *LocalDockerScheduler) Drain(ctx context.Context, deploymentID string, grace time.Duration) error {
	if s == nil || s.Runtime == nil {
		return ErrRuntimeRequired
	}
	depID, instID := splitDeploymentInstance(deploymentID)
	return s.Runtime.Drain(ctx, depID, instID, grace)
}

// DrainInstance is the explicit (deployment, instance) drain variant the
// executor uses today. It bypasses the deploymentID-encoding contract of
// Scheduler.Drain so the executor can keep passing both ids without
// re-encoding them.
func (s *LocalDockerScheduler) DrainInstance(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	if s == nil || s.Runtime == nil {
		return ErrRuntimeRequired
	}
	return s.Runtime.Drain(ctx, deploymentID, instanceID, grace)
}

// State returns the runtime adapter's last-observed view of the deployment.
// The local scheduler resolves routing target on demand; multi-host will
// surface the agent status report instead.
func (s *LocalDockerScheduler) State(ctx context.Context, deploymentID string) (scheduler.DeploymentState, error) {
	if s == nil || s.Runtime == nil {
		return scheduler.DeploymentState{}, ErrRuntimeRequired
	}
	depID, instID := splitDeploymentInstance(deploymentID)
	target, err := s.Runtime.ResolveRoutingTarget(ctx, depID, instID)
	if err != nil {
		return scheduler.DeploymentState{DeploymentID: depID, InstanceID: instID, HostID: s.hostID()}, err
	}
	return scheduler.DeploymentState{
		DeploymentID:  depID,
		InstanceID:    instID,
		HostID:        s.hostID(),
		RoutingTarget: target,
	}, nil
}

// ObserveInstanceLiveness forwards the optional liveness capability from the
// runtime adapter. The scheduler adds nothing of its own: a caller wired
// only with a Scheduler must be able to learn that a candidate has already
// exited, exactly as one holding the adapter directly would.
func (s *LocalDockerScheduler) ObserveInstanceLiveness(ctx context.Context, deploymentID, instanceID string) (runtime.InstanceLiveness, error) {
	if s == nil || s.Runtime == nil {
		return runtime.InstanceLiveness{}, ErrRuntimeRequired
	}
	observer, ok := s.Runtime.(runtime.InstanceLivenessObserver)
	if !ok {
		return runtime.InstanceLiveness{}, nil
	}
	return observer.ObserveInstanceLiveness(ctx, deploymentID, instanceID)
}

// ImageDeclaredPorts forwards the optional image-port capability from the
// runtime adapter.
func (s *LocalDockerScheduler) ImageDeclaredPorts(ctx context.Context, image string) ([]int, error) {
	if s == nil || s.Runtime == nil {
		return nil, ErrRuntimeRequired
	}
	reporter, ok := s.Runtime.(runtime.ImagePortReporter)
	if !ok {
		return nil, errImagePortsUnsupported
	}
	return reporter.ImageDeclaredPorts(ctx, image)
}

// errImagePortsUnsupported keeps "this adapter cannot answer" distinct from
// "this image declares no ports", so callers never read silence as evidence.
var errImagePortsUnsupported = errors.New("local scheduler: runtime adapter does not report image ports")

func (s *LocalDockerScheduler) hostID() string {
	if id := strings.TrimSpace(s.HostID); id != "" {
		return id
	}
	return DefaultHostID
}

func (s *LocalDockerScheduler) capabilities() map[string]string {
	if len(s.Capabilities) == 0 {
		return map[string]string{"runtime": DefaultRuntimeCapability}
	}
	out := make(map[string]string, len(s.Capabilities))
	for k, v := range s.Capabilities {
		out[k] = v
	}
	return out
}

// splitDeploymentInstance accepts "deployment/instance" or just a single
// id. It exists so the scheduler.Scheduler boundary (which takes a single
// deploymentID for Drain / State) can still address (deployment, instance)
// pairs the runtime adapter expects.
func splitDeploymentInstance(id string) (string, string) {
	if idx := strings.Index(id, "/"); idx >= 0 {
		return id[:idx], id[idx+1:]
	}
	return id, id
}
