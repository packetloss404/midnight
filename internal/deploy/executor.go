package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/deploy/readiness"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	schedulerlocal "github.com/midnight/midnight/internal/scheduler/local"
	"github.com/midnight/midnight/internal/secrets"
	"github.com/midnight/midnight/internal/serviceconfig"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const DeployJobPayloadSchema = "midnight.dev/deploy-job/v1alpha1"

var (
	ErrDeployExecutorNotReady = errors.New("deploy executor: dependencies are not ready")
	ErrDeployJobMalformed     = errors.New("deploy executor: malformed deploy job payload")
	ErrRoutePromotionRequired = errors.New("deploy executor: public route promotion requires a configured promoter")
	ErrBackgroundPublicRoute  = errors.New("deploy executor: background mode cannot declare a public route")
	ErrSourceBuildNotWired    = errors.New("deploy executor: source_ref requires source build integration")
	ErrDeployCanceled         = errors.New("deploy executor: deploy canceled before traffic promotion")
)

type DeployJobPayload struct {
	SchemaVersion  string             `json:"schema_version"`
	OperationID    string             `json:"operation_id"`
	ServiceID      string             `json:"service_id"`
	ProjectID      string             `json:"project_id,omitempty"`
	EnvironmentID  string             `json:"environment_id,omitempty"`
	Request        DeployJobRequest   `json:"request"`
	Candidate      DeployJobCandidate `json:"candidate,omitempty"`
	Route          *DeployJobRoute    `json:"route,omitempty"`
	PreviousActive ActiveInstanceRef  `json:"previous_active,omitempty"`
	EnqueuedAt     time.Time          `json:"enqueued_at"`
}

type DeployJobRequest struct {
	EnvironmentID string `json:"environment_id,omitempty"`
	SourceRef     string `json:"source_ref,omitempty"`
	Image         string `json:"image,omitempty"`
	Mode          string `json:"mode,omitempty"`
}

type DeployJobCandidate struct {
	DeploymentID   string            `json:"deployment_id,omitempty"`
	InstanceID     string            `json:"instance_id,omitempty"`
	ProjectID      string            `json:"project_id,omitempty"`
	EnvironmentID  string            `json:"environment_id,omitempty"`
	ServiceID      string            `json:"service_id,omitempty"`
	Image          string            `json:"image,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Replicas       int               `json:"replicas,omitempty"`
}

type DeployJobRoute struct {
	Name   string `json:"name,omitempty"`
	Group  string `json:"group,omitempty"`
	Host   string `json:"host,omitempty"`
	Path   string `json:"path,omitempty"`
	Public bool   `json:"public"`
	TLS    bool   `json:"tls"`
}

// ActivationRuntime is the runtime-adapter boundary the deploy package
// expected before ADR-0018 introduced internal/scheduler.Scheduler. The
// interface is preserved so existing wirings (cmd/midnight-worker/main.go
// and the executor test suite) compile unchanged. When Executor.Scheduler
// is nil, the executor builds a LocalDockerScheduler over this Runtime;
// when both are set, Scheduler wins and Runtime is ignored.
//
// New callers SHOULD set Scheduler directly.
type ActivationRuntime interface {
	PullVerify(ctx context.Context, image string) (runtime.PullVerifyResult, error)
	Schedule(ctx context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error)
	Start(ctx context.Context, deploymentID, instanceID string) error
	ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error)
	Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error
}

type RoutePromoter interface {
	Promote(ctx context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata) error
}

type ReplicaRoutePromoter interface {
	PromoteReplicas(context.Context, []runtime.RoutingTarget, RoutePromotionMetadata) error
}

// CanonicalDeploymentCommitter advances the API-owned deployment pointer. It
// is wired only by the production worker, avoiding an api->deploy import
// cycle while making the database CAS a required activation boundary.
type CanonicalDeploymentCommitter interface {
	CommitCanonicalDeployment(context.Context, string) error
}

// CanonicalTemplateDeploymentCommitter atomically advances the API-owned
// deployment pointer and the verified template run. The two states must never
// diverge if the database commit fails.
type CanonicalTemplateDeploymentCommitter interface {
	CommitCanonicalTemplateDeployment(context.Context, string, string) error
}

// CanonicalCronDeploymentCommitter atomically writes cron activation state and
// promotes its canonical deployment. Production must never expose an enabled
// cursor whose deployment CAS rolled back (or an active deployment with no
// cursor).
type CanonicalCronDeploymentCommitter interface {
	CommitCanonicalCronDeployment(context.Context, string, CronActivation) error
}

// TransactionalRoutePromoter retains the old durable backends until commit
// succeeds. Its rollback contract prevents a canonical CAS race from leaving
// traffic on a candidate that cannot become the active deployment.
type TransactionalRoutePromoter interface {
	PromoteWithCommit(context.Context, runtime.RoutingTarget, RoutePromotionMetadata, func(context.Context) error) error
	PromoteReplicasWithCommit(context.Context, []runtime.RoutingTarget, RoutePromotionMetadata, func(context.Context) error) error
}

// RouteIntentCoordinator persists explicit public route intent before runtime
// scheduling. It is separate from RoutePromoter so a worker crash between
// scheduling and promotion cannot lose the desired domain/route state.
type RouteIntentCoordinator interface {
	EnsureDeployRoute(context.Context, DeployJobPayload, runtime.ScheduleRequest) error
}

// DeploymentPublicRouteBindingResolver is implemented by durable promoters.
// It lets the executor refresh exact deployment-bound route truth independently
// from an older queue payload immediately before the activation boundary.
type DeploymentPublicRouteBindingResolver interface {
	HasEligibleDeploymentPublicRouteBindings(
		context.Context,
		string,
		router.RouteScope,
	) (bool, error)
}

type OperationUpdater interface {
	Transition(ctx context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error)
	Fail(ctx context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error)
}

type CancelChecker interface {
	CancelRequested(ctx context.Context, operationID string) (bool, error)
}

// InstanceDrainer is the optional extra capability the executor uses for
// retiring the previous active instance after promotion. The local
// scheduler exposes DrainInstance(deploymentID, instanceID, grace) to keep
// the (deployment, instance) pair explicit; the scheduler.Scheduler
// interface's Drain method takes a single deploymentID. If the executor's
// Scheduler also satisfies InstanceDrainer, the executor uses it; otherwise
// the legacy ActivationRuntime.Drain is invoked.
type InstanceDrainer interface {
	DrainInstance(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error
}

// VariableResolver renders the resolved environment-variable map for a
// service deploy. The deploy executor calls Resolve before scheduling
// the candidate; any error short-circuits the operation with a typed
// failure code (VARIABLE_REFERENCE_UNRESOLVED / VARIABLE_REFERENCE_CYCLE
// / RESOURCE_VALIDATION_FAILED).
//
// The map keys are the variable names and the values are the rendered
// strings. Implementations MUST NOT return any plaintext payload other
// than the final resolved value (no template strings, no audit-leaking
// detail in the err wrapper). The executor converts the map into the
// runtime.ScheduleRequest.Env []string form expected by adapters.
type VariableResolver interface {
	Resolve(ctx context.Context, projectID, envID, serviceID string) (map[string]string, error)
}

type versionedVariableResolver interface {
	ResolveAtVersions(ctx context.Context, projectID, envID, serviceID string, refs []varrefs.VariableVersionRef) (map[string]string, error)
}

type Executor struct {
	// Scheduler is the deployment-execution boundary introduced by
	// ADR-0018. The executor calls Scheduler.Schedule(intent) instead of
	// reaching into the runtime adapter directly. When nil, the executor
	// auto-wraps the legacy Runtime field with a LocalDockerScheduler so
	// existing callers (worker, tests) keep working.
	Scheduler   scheduler.Scheduler
	Runtime     ActivationRuntime
	Promoter    RoutePromoter
	RouteIntent RouteIntentCoordinator
	Operations  OperationUpdater
	Cancels     CancelChecker
	Observer    DeployStepObserver
	Variables   VariableResolver
	// Configuration resolves the durable, immutable per-deployment config
	// snapshot. Production workers always set it; nil preserves the default
	// configuration for isolated executor tests.
	Configuration serviceconfig.Resolver
	// Predeploy executes an immutable one-off command before application
	// scheduling. It is required only when the config snapshot requests one.
	Predeploy *PredeployExecutor
	// Cron records an immutable cron definition. Cron deployments do not call
	// Scheduler.Schedule and therefore never create a long-running container.
	Cron CronActivator
	// Instances is the durable per-replica source of truth for stateless
	// Docker deployments. Nil keeps isolated legacy tests in single-instance mode.
	Instances          DeploymentInstanceStore
	CanonicalCommitter CanonicalDeploymentCommitter
	// Volumes resolves durable service/environment mount intent immediately
	// before scheduling. Queue payloads never carry mount paths or volume ids.
	Volumes *volume.Manager
	// TemplateVerifier performs database-specific connectivity and
	// restart-persistence probes after generic readiness and before promotion.
	TemplateVerifier TemplateVerifier
	// TemplateRuns persists redaction-safe saga status/evidence.
	TemplateRuns templatepkg.RunRecorder
	DrainGrace   time.Duration
	// Readiness tunes the post-schedule readiness/routing poll loop. Zero
	// values are normalized by readiness.Wait (Timeout -> DefaultTimeout,
	// Interval -> DefaultInterval, Now -> time.Now, Sleep -> real sleep), so
	// existing callers that leave it unset get the default retry/backoff
	// behavior without any wiring changes.
	Readiness ReadinessConfig
}

// ReadinessConfig controls how the executor polls the candidate's
// readiness/routing signal after Schedule returns. A container whose Docker
// HEALTHCHECK is still "starting" reports not-ready on first inspect; the
// executor re-resolves the routing target and re-evaluates the gate on an
// interval until the candidate becomes ready-to-promote or Timeout elapses.
//
// Now and Sleep are injection seams for deterministic tests; production
// callers leave them nil and readiness.Wait supplies the real clock.
type ReadinessConfig struct {
	Timeout  time.Duration
	Interval time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

type ExecutionOutcome struct {
	JobID         string
	OperationID   string
	Plan          CandidateActivationPlan
	Succeeded     bool
	FailureCode   failurecodes.Code
	FailureReason string
}

type NoopRoutePromoter struct{}

func (NoopRoutePromoter) Promote(_ context.Context, _ runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	if metadata.Public {
		return ErrRoutePromotionRequired
	}
	return nil
}

// resolveScheduler returns the scheduler the executor will drive. If
// Executor.Scheduler is set, it is returned unchanged. Otherwise the
// legacy ActivationRuntime is auto-wrapped with a LocalDockerScheduler so
// the pre-ADR-0018 wiring keeps working.
func (e Executor) resolveScheduler() scheduler.Scheduler {
	if e.Scheduler != nil {
		return e.Scheduler
	}
	if e.Runtime == nil {
		return nil
	}
	return schedulerlocal.New(runtimeAdapter{runtime: e.Runtime})
}

// runtimeAdapter bridges the legacy deploy.ActivationRuntime to the
// scheduler-local.Runtime shape. They have the same method set; the
// indirection exists so internal/deploy does not have to import the
// scheduler/local package's interface name when constructing a fallback
// LocalDockerScheduler.
type runtimeAdapter struct {
	runtime ActivationRuntime
}

func (a runtimeAdapter) PullVerify(ctx context.Context, image string) (runtime.PullVerifyResult, error) {
	return a.runtime.PullVerify(ctx, image)
}

func (a runtimeAdapter) Schedule(ctx context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	return a.runtime.Schedule(ctx, req)
}

func (a runtimeAdapter) Start(ctx context.Context, deploymentID, instanceID string) error {
	return a.runtime.Start(ctx, deploymentID, instanceID)
}

func (a runtimeAdapter) ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	return a.runtime.ResolveRoutingTarget(ctx, deploymentID, instanceID)
}

func (a runtimeAdapter) Drain(ctx context.Context, deploymentID, instanceID string, grace time.Duration) error {
	return a.runtime.Drain(ctx, deploymentID, instanceID, grace)
}

func DecodeDeployJobPayload(payload []byte) (DeployJobPayload, runtime.ScheduleRequest, error) {
	var job DeployJobPayload
	if err := json.Unmarshal(payload, &job); err != nil {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: %v", ErrDeployJobMalformed, err)
	}
	job.SchemaVersion = strings.TrimSpace(job.SchemaVersion)
	job.OperationID = strings.TrimSpace(job.OperationID)
	job.ServiceID = strings.TrimSpace(job.ServiceID)
	job.ProjectID = strings.TrimSpace(job.ProjectID)
	job.EnvironmentID = strings.TrimSpace(firstNonEmpty(job.EnvironmentID, job.Request.EnvironmentID))
	job.Request.Image = strings.TrimSpace(job.Request.Image)
	job.Request.SourceRef = strings.TrimSpace(job.Request.SourceRef)
	job.Request.Mode = strings.TrimSpace(job.Request.Mode)
	if job.SchemaVersion != DeployJobPayloadSchema {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: unsupported schema %q", ErrDeployJobMalformed, job.SchemaVersion)
	}
	if job.OperationID == "" {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: operation_id is required", ErrDeployJobMalformed)
	}
	if job.ServiceID == "" {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: service_id is required", ErrDeployJobMalformed)
	}
	if job.Request.SourceRef != "" && (job.Request.Mode != "template" || !strings.HasPrefix(job.Request.SourceRef, "template://")) {
		return job, runtime.ScheduleRequest{}, fmt.Errorf("%w: source_ref %q cannot be deployed until the build pipeline writes an immutable sha256 candidate.image", ErrSourceBuildNotWired, job.Request.SourceRef)
	}

	candidate := job.Candidate
	candidate.DeploymentID = strings.TrimSpace(firstNonEmpty(candidate.DeploymentID, "dep-"+job.OperationID))
	candidate.InstanceID = strings.TrimSpace(firstNonEmpty(candidate.InstanceID, candidate.DeploymentID+"-r0"))
	candidate.ProjectID = strings.TrimSpace(firstNonEmpty(candidate.ProjectID, job.ProjectID))
	candidate.EnvironmentID = strings.TrimSpace(firstNonEmpty(candidate.EnvironmentID, job.EnvironmentID))
	candidate.ServiceID = strings.TrimSpace(firstNonEmpty(candidate.ServiceID, job.ServiceID))
	candidate.Image = strings.TrimSpace(firstNonEmpty(candidate.Image, job.Request.Image))
	candidate.IdempotencyKey = strings.TrimSpace(firstNonEmpty(candidate.IdempotencyKey, job.OperationID))
	if candidate.Replicas <= 0 {
		candidate.Replicas = 1
	}
	if candidate.Image == "" {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: request.image or candidate.image is required", ErrDeployJobMalformed)
	}
	if candidate.EnvironmentID == "" {
		return DeployJobPayload{}, runtime.ScheduleRequest{}, fmt.Errorf("%w: environment_id is required", ErrDeployJobMalformed)
	}
	if strings.HasPrefix(job.Request.SourceRef, "template://") {
		if err := validateOfficialTemplateImage(job.Request.SourceRef, candidate.Image); err != nil {
			return job, runtime.ScheduleRequest{}, fmt.Errorf("%w: %v", ErrDeployJobMalformed, err)
		}
	}
	return job, runtime.ScheduleRequest{
		DeploymentID:   candidate.DeploymentID,
		InstanceID:     candidate.InstanceID,
		ProjectID:      candidate.ProjectID,
		EnvironmentID:  candidate.EnvironmentID,
		ServiceID:      candidate.ServiceID,
		Image:          candidate.Image,
		Labels:         copyStringMap(candidate.Labels),
		IdempotencyKey: candidate.IdempotencyKey,
		Replicas:       candidate.Replicas,
	}, nil
}

func attachedVolumeMounts(attached []volume.AttachedVolume) []runtime.ScheduleMount {
	out := make([]runtime.ScheduleMount, 0, len(attached))
	for _, item := range attached {
		out = append(out, runtime.ScheduleMount{
			VolumeID: item.VolumeID,
			Source:   item.DockerVolumeName,
			Target:   item.MountPath,
			ReadOnly: item.ReadOnly,
			IsVolume: true,
		})
	}
	return out
}

func (e Executor) Execute(ctx context.Context, job queue.Job) (ExecutionOutcome, error) {
	sched := e.resolveScheduler()
	if sched == nil || e.Promoter == nil || e.Operations == nil {
		out := ExecutionOutcome{JobID: job.ID, FailureCode: failurecodes.InternalError, FailureReason: ErrDeployExecutorNotReady.Error()}
		e.observeDeployStep(ctx, DeployStepEvent{
			JobID:         job.ID,
			Step:          DeployStepFailed,
			State:         DeployStepStateFailed,
			FailureCode:   out.FailureCode,
			FailureReason: out.FailureReason,
		})
		return out, ErrDeployExecutorNotReady
	}
	payload, candidate, err := DecodeDeployJobPayload(job.Payload)
	if err != nil {
		code := failurecodes.ResourceValidationFailed
		if errors.Is(err, ErrSourceBuildNotWired) {
			code = failurecodes.DeploySourceRefUnsupported
		}
		out := ExecutionOutcome{JobID: job.ID, OperationID: payload.OperationID, FailureCode: code, FailureReason: err.Error()}
		if out.OperationID != "" {
			_, _ = e.Operations.Fail(ctx, orailtypes.ID(out.OperationID), orailtypes.FailureCode(out.FailureCode), out.FailureReason)
		}
		e.observeDeployStep(ctx, DeployStepEvent{
			OperationID:   out.OperationID,
			JobID:         job.ID,
			Step:          DeployStepFailed,
			State:         DeployStepStateFailed,
			FailureCode:   out.FailureCode,
			FailureReason: out.FailureReason,
		})
		return out, err
	}
	out := ExecutionOutcome{JobID: job.ID, OperationID: payload.OperationID}
	baseEvent := DeployStepEvent{
		OperationID:  payload.OperationID,
		JobID:        job.ID,
		DeploymentID: candidate.DeploymentID,
		InstanceID:   candidate.InstanceID,
	}
	emit := func(step DeployStep, state DeployStepState, code failurecodes.Code, reason string, route *DeployRouteEvent) {
		event := baseEvent
		event.Step = step
		event.State = state
		event.FailureCode = code
		event.FailureReason = reason
		event.Route = route
		e.observeDeployStep(ctx, event)
	}
	opID := orailtypes.ID(payload.OperationID)
	if _, err := e.Operations.Transition(ctx, opID, orailtypes.OperationRunning); err != nil {
		out.FailureCode = failurecodes.OperationInvalidTransition
		out.FailureReason = err.Error()
		emit(DeployStepFailed, DeployStepStateFailed, out.FailureCode, out.FailureReason, nil)
		return out, err
	}

	req := CandidateActivationRequest{Candidate: candidate, PreviousActive: payload.PreviousActive}
	routeMetadata := routePromotionMetadata(payload, candidate)
	// Successful scheduler calls are recorded before readiness so any later
	// failure can retire the complete candidate set. This is intentionally
	// independent of the volume cleanup closure installed further below.
	var scheduledCandidateIDs []string
	candidatePromoted := false
	candidateCleaned := false
	drainGrace := e.DrainGrace
	cleanupCandidateBeforePromotion := func() error {
		if candidateCleaned {
			return nil
		}
		candidateCleaned = true
		var cleanupErr error
		for index := len(scheduledCandidateIDs) - 1; index >= 0; index-- {
			instanceID := scheduledCandidateIDs[index]
			if err := drainInstance(ctx, sched, e.Runtime, candidate.DeploymentID, instanceID, drainGrace); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("drain candidate %s: %w", instanceID, err))
			}
			if remover, ok := e.Runtime.(interface {
				Remove(context.Context, string, string) error
			}); ok {
				if err := remover.Remove(ctx, candidate.DeploymentID, instanceID); err != nil {
					cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove candidate %s: %w", instanceID, err))
				}
			}
		}
		if e.Instances != nil && len(scheduledCandidateIDs) > 0 {
			if err := e.Instances.SetDeploymentState(ctx, candidate.DeploymentID, "removed", "removed"); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
		return cleanupErr
	}
	fail := func(step DeployStep, code failurecodes.Code, reason string, cause error, route *DeployRouteEvent) (ExecutionOutcome, error) {
		if !candidatePromoted {
			if cleanupErr := cleanupCandidateBeforePromotion(); cleanupErr != nil {
				reason += "; candidate cleanup failed: " + cleanupErr.Error()
				cause = errors.Join(cause, cleanupErr)
			}
		}
		out.FailureCode = code
		out.FailureReason = reason
		out.Plan, _ = PlanCandidateActivation(req)
		if step != "" {
			emit(step, DeployStepStateFailed, code, reason, route)
		}
		emit(DeployStepFailed, DeployStepStateFailed, code, reason, route)
		if strings.HasPrefix(payload.Request.SourceRef, "template://") && e.TemplateRuns != nil {
			_ = e.TemplateRuns.MarkFailed(ctx, candidate.DeploymentID, code, reason)
		}
		if _, opErr := e.Operations.Fail(ctx, opID, orailtypes.FailureCode(code), reason); opErr != nil {
			if cause != nil {
				return out, errors.Join(cause, opErr)
			}
			return out, opErr
		}
		if cause != nil {
			return out, cause
		}
		return out, errors.New(reason)
	}
	cancelBeforePromotion := func(route *DeployRouteEvent) (ExecutionOutcome, error) {
		reason := "deploy canceled at worker safe point before traffic promotion"
		if err := cleanupCandidateBeforePromotion(); err != nil {
			return fail(DeployStepCanceled, failurecodes.ReconcileStepFailed, "cancel cleanup failed: "+err.Error(), err, route)
		}
		out.FailureCode = failurecodes.DeployCanceledBeforePromotion
		out.FailureReason = reason
		out.Plan, _ = PlanCandidateActivation(req)
		emit(DeployStepCanceled, DeployStepStateSucceeded, out.FailureCode, reason, route)
		if _, err := e.Operations.Transition(ctx, opID, orailtypes.OperationCanceled); err != nil {
			out.FailureCode = failurecodes.OperationInvalidTransition
			out.FailureReason = err.Error()
			emit(DeployStepFailed, DeployStepStateFailed, out.FailureCode, out.FailureReason, route)
			return out, err
		}
		return out, nil
	}
	cancelRequested := func() (bool, error) {
		if e.Cancels == nil {
			return false, nil
		}
		return e.Cancels.CancelRequested(ctx, payload.OperationID)
	}
	effectiveConfig := serviceconfig.Default(candidate.ProjectID, candidate.ServiceID, candidate.EnvironmentID)
	var pinnedVariables []varrefs.VariableVersionRef
	var configSnapshotSHA256 string
	enforceApplicationReadiness := e.Configuration != nil
	if e.Configuration != nil {
		snapshot, configErr := e.Configuration.ResolveDeployment(
			ctx,
			candidate.DeploymentID,
			candidate.ProjectID,
			candidate.EnvironmentID,
			candidate.ServiceID,
		)
		if configErr != nil {
			code := failurecodes.ReconcileStepFailed
			if errors.Is(configErr, serviceconfig.ErrInvalid) || errors.Is(configErr, serviceconfig.ErrScope) {
				code = failurecodes.ResourceValidationFailed
			}
			return fail(DeployStepSchedule, code, "deployment config resolution failed: "+configErr.Error(), configErr, nil)
		}
		effectiveConfig = snapshot.Config
		configSnapshotSHA256 = snapshot.SHA256
		pinnedVariables = make([]varrefs.VariableVersionRef, 0, len(snapshot.VariableVersions))
		for _, ref := range snapshot.VariableVersions {
			pinnedVariables = append(pinnedVariables, varrefs.VariableVersionRef{
				ID:                 ref.ID,
				ServiceID:          ref.ServiceID,
				ServiceSlug:        ref.ServiceSlug,
				ServiceDisplayName: ref.ServiceDisplayName,
				Name:               ref.Name,
				Version:            ref.Version,
			})
		}
	}
	hasExactPublicRouteBinding := false
	refreshDeploymentPublicRouteBindings := func() error {
		resolver, ok := e.Promoter.(DeploymentPublicRouteBindingResolver)
		if !ok {
			return nil
		}
		public, err := resolver.HasEligibleDeploymentPublicRouteBindings(
			ctx,
			candidate.DeploymentID,
			router.RouteScope{
				ProjectID:     candidate.ProjectID,
				EnvironmentID: candidate.EnvironmentID,
				ServiceID:     candidate.ServiceID,
			},
		)
		if err != nil {
			return err
		}
		hasExactPublicRouteBinding = public
		if public {
			routeMetadata.Public = true
		}
		return nil
	}
	if err := refreshDeploymentPublicRouteBindings(); err != nil {
		return fail(
			DeployStepSchedule,
			failurecodes.ReconcileStepFailed,
			"deployment route binding refresh failed: "+err.Error(),
			err,
			nil,
		)
	}
	// Background mode is deliberately an execution-only workload. It may still
	// use an internal target port for health/peer traffic, but it must never
	// create an HTTP ingress intent or reach the route promoter.
	if (payload.Request.Mode == "background" || effectiveConfig.RuntimeKind == serviceconfig.RuntimeKindBackground || effectiveConfig.RuntimeKind == serviceconfig.RuntimeKindCron) && routeMetadata.Public {
		return fail(DeployStepSchedule, failurecodes.ResourceValidationFailed, ErrBackgroundPublicRoute.Error(), ErrBackgroundPublicRoute, nil)
	}
	candidate.StartCommand = effectiveConfig.StartCommand
	candidate.TargetPort = effectiveConfig.TargetPort
	// The platform default is a guess. Let the runtime adapter prefer the
	// image's own single declared port when nobody chose one explicitly.
	candidate.TargetPortInferred = effectiveConfig.TargetPort == serviceconfig.DefaultTargetPort
	candidate.RestartPolicy = effectiveConfig.RestartPolicy
	candidate.RestartMaxRetries = effectiveConfig.RestartRetries
	candidate.Replicas = effectiveConfig.Replicas
	// All replica identities are deployment-derived. The queue's historical
	// instance id is ignored so the durable instance row, container label, and
	// MIDNIGHT_REPLICA_ID agree even on a retry.
	candidate.InstanceID = ReplicaInstanceID(candidate.DeploymentID, 0)
	baseEvent.InstanceID = candidate.InstanceID
	req.Candidate = candidate
	if e.Instances != nil && effectiveConfig.RuntimeKind != serviceconfig.RuntimeKindCron {
		replicaCount := candidate.Replicas
		if replicaCount < 1 {
			replicaCount = 1
		}
		if _, instanceErr := e.Instances.Ensure(ctx, candidate.DeploymentID, replicaCount); instanceErr != nil {
			return fail(DeployStepSchedule, failurecodes.ReconcileStepFailed, "durable replica instance setup failed: "+instanceErr.Error(), instanceErr, nil)
		}
	}
	drainGrace = effectiveConfig.DrainGrace
	if e.Configuration == nil && e.DrainGrace > 0 {
		drainGrace = e.DrainGrace
	}
	// An immutable-image deploy builds nothing at rollout time, so it gets a
	// shorter default readiness budget than a source build whose container
	// may legitimately spend minutes warming up. An operator-set
	// HealthTimeout still wins over either default.
	immutableImageDeploy := strings.TrimSpace(payload.Request.SourceRef) == ""
	readinessConfig := e.Readiness
	if e.Configuration != nil {
		readinessConfig.Timeout = serviceconfig.EffectiveHealthTimeout(effectiveConfig, immutableImageDeploy)
		readinessConfig.Interval = effectiveConfig.HealthInterval
	}
	// Resolve user-defined variable references BEFORE handing the candidate
	// to the scheduler. Convert the resolved map into the KEY=VALUE slice
	// form runtime.ScheduleRequest.Env expects; the runtime adapter prepends
	// MIDNIGHT_* built-ins on top of this slice.
	if e.Variables != nil {
		variableCtx := secrets.WithDecryptAuditExecution(ctx, secrets.DecryptAuditExecution{
			Kind:         secrets.DecryptExecutionDeploy,
			ExecutionID:  job.ID,
			DeploymentID: candidate.DeploymentID,
			OperationID:  payload.OperationID,
			Attempt:      job.Attempt,
		})
		env, snapshot, vErr := resolveDeployVariables(variableCtx, e.Variables, candidate, pinnedVariables, e.Configuration != nil)
		if vErr != nil {
			code := variableFailureCode(vErr)
			reason := variableFailureReason(vErr)
			emitDeployStepWithVariables(e, ctx, baseEvent, DeployStepResolveVariables, DeployStepStateFailed, code, reason, nil)
			return fail("", code, reason, vErr, nil)
		}
		envSlice := make([]string, 0, len(env))
		envKeys := make([]string, 0, len(env))
		for key := range env {
			envKeys = append(envKeys, key)
		}
		sortStrings(envKeys)
		for _, key := range envKeys {
			envSlice = append(envSlice, key+"="+env[key])
		}
		candidate.Env = envSlice
		emitDeployStepWithVariables(e, ctx, baseEvent, DeployStepResolveVariables, DeployStepStateSucceeded, "", "", snapshot)
	}
	if effectiveConfig.PredeployCommand != "" {
		if e.Predeploy == nil {
			err := &PredeployFailure{
				Code: failurecodes.NotImplemented, Message: ErrPredeployUnavailable.Error(),
			}
			return fail(DeployStepPredeploy, failurecodes.NotImplemented, err.Error(), err, nil)
		}
		emit(DeployStepPredeploy, DeployStepStateStarted, "", "", nil)
		if _, err := e.Predeploy.Execute(ctx, PredeployRequest{
			Candidate:      candidate,
			Command:        effectiveConfig.PredeployCommand,
			Timeout:        DefaultPredeployTimeout,
			SnapshotSHA256: configSnapshotSHA256,
		}); err != nil {
			code := failureCodeFromError(err, failurecodes.PredeployFailed)
			return fail(DeployStepPredeploy, code, err.Error(), err, nil)
		}
		emit(DeployStepPredeploy, DeployStepStateSucceeded, "", "", nil)
	}
	if effectiveConfig.RuntimeKind == serviceconfig.RuntimeKindCron {
		if e.Cron == nil {
			return fail(DeployStepSchedule, failurecodes.NotImplemented, "cron runtime is not configured", errors.New("cron runtime is not configured"), nil)
		}
		emit(DeployStepSchedule, DeployStepStateStarted, "", "", nil)
		activation := CronActivation{Candidate: candidate, Snapshot: serviceconfig.Snapshot{
			DeploymentID: candidate.DeploymentID, ProjectID: candidate.ProjectID,
			ServiceID: candidate.ServiceID, EnvironmentID: candidate.EnvironmentID,
			Config: effectiveConfig, SHA256: configSnapshotSHA256,
		}}
		var activationErr error
		if e.CanonicalCommitter != nil {
			committer, ok := e.CanonicalCommitter.(CanonicalCronDeploymentCommitter)
			if !ok {
				activationErr = errors.New("atomic cron canonical deployment committer is unavailable")
			} else {
				activationErr = committer.CommitCanonicalCronDeployment(ctx, payload.OperationID, activation)
			}
		} else {
			// Isolated executor tests and non-production adapters retain the
			// CronActivator seam; production always wires CanonicalCommitter.
			activationErr = e.Cron.Activate(ctx, candidate, activation.Snapshot)
		}
		if activationErr != nil {
			return fail(DeployStepSchedule, failureCodeFromError(activationErr, failurecodes.ReconcileStepFailed), "cron activation failed: "+activationErr.Error(), activationErr, nil)
		}
		emit(DeployStepSchedule, DeployStepStateSucceeded, "", "", nil)
		if _, err := e.Operations.Transition(ctx, opID, orailtypes.OperationSucceeded); err != nil {
			return fail("", failurecodes.OperationInvalidTransition, err.Error(), err, nil)
		}
		out.Succeeded = true
		emit(DeployStepSucceeded, DeployStepStateSucceeded, "", "", nil)
		return out, nil
	}
	if routeMetadata.Public &&
		e.RouteIntent != nil &&
		!hasExactPublicRouteBinding {
		if err := e.RouteIntent.EnsureDeployRoute(ctx, payload, candidate); err != nil {
			return fail(DeployStepSchedule, failurecodes.ReconcileStepFailed, "durable route intent failed: "+err.Error(), err, nil)
		}
	}
	previousDrained := false
	if !effectiveConfig.OverlapEnabled && hasPreviousActive(payload.PreviousActive) {
		emit(DeployStepDrainPrevious, DeployStepStateStarted, "", "", nil)
		if err := drainDeploymentInstances(ctx, sched, e.Runtime, payload.PreviousActive.DeploymentID, payload.PreviousActive.InstanceID, drainGrace); err != nil {
			return fail(DeployStepDrainPrevious, failureCodeFromError(err, failurecodes.ReconcileStepFailed), "drain previous before non-overlap deploy failed: "+err.Error(), err, nil)
		}
		previousDrained = true
		emit(DeployStepDrainPrevious, DeployStepStateSucceeded, "", "", nil)
	}

	var preparedVolumes volume.PreparedAttachments
	serialVolumeTakeover := false
	var volumeDeployment volume.DeploymentAttachments
	if e.Volumes != nil {
		volumeDeployment = volume.DeploymentAttachments{
			DeploymentID:  candidate.DeploymentID,
			InstanceID:    candidate.InstanceID,
			ProjectID:     candidate.ProjectID,
			ServiceID:     candidate.ServiceID,
			EnvironmentID: candidate.EnvironmentID,
			Replicas:      candidate.Replicas,
		}
		serialVolumeTakeover, err = e.Volumes.RequiresSerialTakeover(ctx, volumeDeployment)
		if err != nil {
			code := failureCodeFromError(err, failurecodes.ReconcileStepFailed)
			return fail(DeployStepSchedule, code, "volume intent validation failed: "+err.Error(), err, nil)
		}
		if serialVolumeTakeover && hasPreviousActive(payload.PreviousActive) {
			// The Docker local driver is single-writer. Stateful redeploys
			// therefore have a deliberate downtime boundary: stop the old
			// instance, release its durable lease, then schedule the candidate.
			if !previousDrained {
				emit(DeployStepDrainPrevious, DeployStepStateStarted, "", "", nil)
				if err := drainDeploymentInstances(ctx, sched, e.Runtime, payload.PreviousActive.DeploymentID, payload.PreviousActive.InstanceID, drainGrace); err != nil {
					return fail(DeployStepDrainPrevious, failureCodeFromError(err, failurecodes.ReconcileStepFailed), "drain previous volume writer failed: "+err.Error(), err, nil)
				}
				previousDrained = true
			}
			if err := e.Volumes.DetachInstance(ctx, volumeDeployment, payload.PreviousActive.DeploymentID, payload.PreviousActive.InstanceID); err != nil {
				return fail(DeployStepDrainPrevious, failureCodeFromError(err, failurecodes.ReconcileStepFailed), "detach previous volume writer failed: "+err.Error(), err, nil)
			}
			emit(DeployStepDrainPrevious, DeployStepStateSucceeded, "", "", nil)
		}
		preparedVolumes, err = e.Volumes.PrepareServiceDeployment(ctx, volumeDeployment)
		if err != nil {
			code := failureCodeFromError(err, failurecodes.ReconcileStepFailed)
			return fail(DeployStepSchedule, code, "volume prepare failed: "+err.Error(), err, nil)
		}
		if err := e.Volumes.ConfirmForSchedule(ctx, preparedVolumes); err != nil {
			e.Volumes.DetachPrepared(ctx, preparedVolumes)
			code := failureCodeFromError(err, failurecodes.ReconcileStepFailed)
			return fail(DeployStepSchedule, code, "volume attach check failed: "+err.Error(), err, nil)
		}
		candidate.Mounts = attachedVolumeMounts(preparedVolumes.Mounts)
	}
	cleanupScheduledVolumeCandidate := func() error {
		if e.Volumes == nil || len(preparedVolumes.Claims) == 0 {
			return nil
		}
		if err := drainInstance(ctx, sched, e.Runtime, candidate.DeploymentID, candidate.InstanceID, drainGrace); err != nil {
			return fmt.Errorf("drain failed volume candidate: %w", err)
		}
		if err := e.Volumes.DetachInstance(ctx, volumeDeployment, candidate.DeploymentID, candidate.InstanceID); err != nil {
			return fmt.Errorf("detach failed volume candidate: %w", err)
		}
		return nil
	}

	// observeStep adapts the scheduler.StepObserver callback shape into the
	// deploy package's DeployStepEvent telemetry. Started events for the
	// readiness-routing-check sub-step are emitted by this loop too so the
	// pre-refactor telemetry order is preserved byte-for-byte.
	var lastSchedulerStep scheduler.ScheduleStep
	stepObserver := scheduler.StepObserverFunc(func(step scheduler.ScheduleStep, state scheduler.ScheduleStepState, _ error) {
		lastSchedulerStep = step
		switch step {
		case scheduler.ScheduleStepPullVerify:
			if state == scheduler.ScheduleStepStarted {
				emit(DeployStepPullVerify, DeployStepStateStarted, "", "", nil)
			} else if state == scheduler.ScheduleStepSucceeded {
				emit(DeployStepPullVerify, DeployStepStateSucceeded, "", "", nil)
			}
		case scheduler.ScheduleStepSchedule:
			if state == scheduler.ScheduleStepStarted {
				emit(DeployStepSchedule, DeployStepStateStarted, "", "", nil)
			} else if state == scheduler.ScheduleStepSucceeded {
				emit(DeployStepSchedule, DeployStepStateSucceeded, "", "", nil)
			}
		case scheduler.ScheduleStepStart:
			if state == scheduler.ScheduleStepStarted {
				emit(DeployStepStart, DeployStepStateStarted, "", "", nil)
			} else if state == scheduler.ScheduleStepSucceeded {
				emit(DeployStepStart, DeployStepStateSucceeded, "", "", nil)
			}
		case scheduler.ScheduleStepResolveRouting:
			if state == scheduler.ScheduleStepStarted {
				emit(DeployStepReadinessRoutingCheck, DeployStepStateStarted, "", "", nil)
			}
			// We deliberately do NOT emit Succeeded here; the executor
			// runs the readiness/routing gate after Schedule returns and
			// emits the Succeeded event itself once the gate passes.
		}
	})
	intent := scheduler.ScheduleIntent{
		OperationID:     payload.OperationID,
		ScheduleRequest: candidate,
		PreviousActive: scheduler.ActiveInstance{
			DeploymentID: payload.PreviousActive.DeploymentID,
			InstanceID:   payload.PreviousActive.InstanceID,
		},
		StepObserver: stepObserver,
	}
	// Docker's adapter materializes exactly one named instance per schedule
	// request. Stateless scale-out therefore expands here into deterministic
	// r1..rN requests; volumes were rejected above before any runtime effect.
	replicaTargets := make([]runtime.RoutingTarget, 0, max(0, candidate.Replicas-1))
	if candidate.Replicas > 1 {
		for ordinal := 1; ordinal < candidate.Replicas; ordinal++ {
			replica := candidate
			replica.InstanceID = ReplicaInstanceID(candidate.DeploymentID, ordinal)
			replica.Replicas = 1
			replica.IdempotencyKey = candidate.IdempotencyKey + "-r" + fmt.Sprintf("%d", ordinal)
			// Schedule can fail after Docker has created or started a container
			// (for example while resolving its routing target). Record intent
			// first; Drain/Remove are idempotent if no container materialized.
			scheduledCandidateIDs = append(scheduledCandidateIDs, replica.InstanceID)
			placement, replicaErr := sched.Schedule(ctx, scheduler.ScheduleIntent{OperationID: payload.OperationID, ScheduleRequest: replica, StepObserver: stepObserver})
			if replicaErr != nil {
				return fail(DeployStepSchedule, failureCodeFromError(replicaErr, failurecodes.AgentRuntimeUnavailable), "replica schedule failed: "+replicaErr.Error(), replicaErr, nil)
			}
			if !placement.PullVerify.Verified || !placement.PullVerify.Trusted {
				return fail(DeployStepPullVerify, failurecodes.ImageSignatureRequired, "replica image is not verified or trusted", errors.New("replica image is not verified or trusted"), nil)
			}
			replicaTargets = append(replicaTargets, placement.RoutingTarget)
			if e.Instances != nil {
				if instanceErr := e.Instances.Observe(ctx, placement.RoutingTarget); instanceErr != nil {
					return fail(DeployStepSchedule, failurecodes.ReconcileStepFailed, "replica instance observation failed: "+instanceErr.Error(), instanceErr, nil)
				}
			}
		}
		candidate.Replicas = 1
		intent.ScheduleRequest = candidate
	}
	// See the replica loop above: include r0 before the scheduler can have
	// externally materialized it.
	scheduledCandidateIDs = append(scheduledCandidateIDs, candidate.InstanceID)
	placement, schedErr := sched.Schedule(ctx, intent)
	req.PullVerify = placement.PullVerify
	if schedErr != nil {
		if len(preparedVolumes.Claims) > 0 {
			switch lastSchedulerStep {
			case scheduler.ScheduleStepPullVerify, "":
				e.Volumes.DetachPrepared(ctx, preparedVolumes)
			case scheduler.ScheduleStepStart, scheduler.ScheduleStepResolveRouting:
				// Container create succeeded before the later failure, so this
				// is an observed attachment even though activation failed.
				_ = e.Volumes.ActivatePrepared(ctx, preparedVolumes)
				if cleanupErr := cleanupScheduledVolumeCandidate(); cleanupErr != nil {
					schedErr = errors.Join(schedErr, cleanupErr)
				}
			}
		}
		// Classify the failure by the last sub-step the scheduler reported.
		// PullVerify failures may also surface as schedErr with the
		// PullVerify result populated; we replicate the pre-refactor
		// per-step failure-code mapping by inspecting lastSchedulerStep.
		switch lastSchedulerStep {
		case scheduler.ScheduleStepPullVerify, "":
			return fail(DeployStepPullVerify, failureCodeFromError(schedErr, failurecodes.ImageSignatureInvalid), "pull-verify failed: "+schedErr.Error(), schedErr, nil)
		case scheduler.ScheduleStepSchedule:
			return fail(DeployStepSchedule, failureCodeFromError(schedErr, failurecodes.AgentRuntimeUnavailable), "schedule failed: "+schedErr.Error(), schedErr, nil)
		case scheduler.ScheduleStepStart:
			return fail(DeployStepStart, failureCodeFromError(schedErr, failurecodes.AgentRuntimeUnavailable), "start failed: "+schedErr.Error(), schedErr, nil)
		case scheduler.ScheduleStepResolveRouting:
			target := placement.RoutingTarget
			routeEvent := routeDeployEvent(routeMetadata, target)
			return fail(DeployStepReadinessRoutingCheck, failureCodeFromError(schedErr, failurecodes.AgentRuntimeUnavailable), "routing target failed: "+schedErr.Error(), schedErr, routeEvent)
		default:
			return fail(DeployStepFailed, failureCodeFromError(schedErr, failurecodes.InternalError), "scheduler failed: "+schedErr.Error(), schedErr, nil)
		}
	}
	// Pull verification failed without a transport error (signature missing
	// / not trusted). Mirror the pre-refactor classification.
	if !placement.PullVerify.Verified || !placement.PullVerify.Trusted {
		if len(preparedVolumes.Claims) > 0 {
			e.Volumes.DetachPrepared(ctx, preparedVolumes)
		}
		reason := placement.PullVerify.Reason
		if reason == "" {
			reason = "candidate image is not verified or trusted"
		}
		return fail(DeployStepPullVerify, failurecodes.ImageSignatureRequired, reason, errors.New(reason), nil)
	}
	if len(preparedVolumes.Claims) > 0 {
		if err := e.Volumes.ActivatePrepared(ctx, preparedVolumes); err != nil {
			_ = cleanupScheduledVolumeCandidate()
			return fail(DeployStepSchedule, failureCodeFromError(err, failurecodes.ReconcileStepFailed), "volume activation record failed: "+err.Error(), err, nil)
		}
		cleanupCandidateBeforePromotion = func() error {
			err := cleanupScheduledVolumeCandidate()
			if e.Instances != nil {
				err = errors.Join(err, e.Instances.SetDeploymentState(ctx, candidate.DeploymentID, "removed", "removed"))
			}
			return err
		}
	}
	req.Scheduled = true
	req.Started = true
	target := placement.RoutingTarget
	if e.Instances != nil {
		if instanceErr := e.Instances.Observe(ctx, target); instanceErr != nil {
			return fail(DeployStepSchedule, failurecodes.ReconcileStepFailed, "candidate instance observation failed: "+instanceErr.Error(), instanceErr, nil)
		}
	}
	routeEvent := routeDeployEvent(routeMetadata, target)

	// Readiness/routing gate. Pre-refactor this evaluated exactly once, so a
	// container whose Docker HEALTHCHECK was still "starting" failed the
	// deploy on first inspect. The gate now runs inside readiness.Wait: the
	// probe re-resolves the routing target and re-evaluates
	// EvaluateActivationReadinessRouting on each retry, returning a non-nil
	// error while the candidate is not ready-to-promote so the loop retries
	// until the candidate is healthy or Timeout elapses. The first probe
	// attempt reuses the routing target Schedule already resolved so the
	// happy path performs no extra ResolveRoutingTarget call.
	// An image that declares no port, deployed by an operator who chose no
	// port either, has nothing for a TCP probe to dial: a worker- or cron-
	// shaped image would sit un-ready until the budget ran out. Ask the
	// runtime what the image declares for itself and, in that one case, let
	// "running and not exited" be the readiness signal. A service with a
	// port — configured or declared — still proves it is listening.
	probeConfig := effectiveConfig
	if immutableImageDeploy && enforceApplicationReadiness {
		if declaresPort, known := imageDeclaresPort(ctx, sched, e.Runtime, candidate.Image); known {
			probeConfig.HealthKind = serviceconfig.EffectiveHealthKind(effectiveConfig, declaresPort)
			if probeConfig.HealthKind != effectiveConfig.HealthKind {
				emit(DeployStepReadinessRoutingCheck, DeployStepStateStarted, "",
					"this image declares no port and none is configured, so readiness is the container staying up", routeEvent)
			}
		}
	}
	evaluate := func(t runtime.RoutingTarget) ActivationReadinessRoutingCheck {
		return EvaluateActivationReadinessRouting(ActivationReadinessRoutingEvidence{
			Candidate:      candidate,
			InstanceStatus: readinessStatusFromTarget(candidate, t),
			RoutingTarget:  t,
		})
	}
	// A user-requested cancel must be honored promptly during the readiness
	// poll. Without this, a cancel issued while the candidate is still
	// "starting" would not be observed until the wait ends (up to Timeout).
	// The probe checks the cancel signal each iteration and cancels waitCtx,
	// which aborts readiness.Wait on its next context check.
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	firstProbe := true
	var canceledDuringWait bool
	var cancelCheckErr error
	// Readiness can legitimately take minutes. Emit a throttled progress event
	// carrying the current block reason so operators watching the operation
	// timeline see why activation is waiting instead of a silent gap.
	progress := newReadinessProgressReporter(readinessConfig.Now, func(reason string) {
		emit(DeployStepReadinessRoutingCheck, DeployStepStateStarted, "", "waiting for readiness: "+reason, routeEvent)
	})
	// A candidate that has already exited, or that the restart policy keeps
	// resurrecting, will never become ready. When the runtime can tell us
	// that, the wait ends on the spot with the exit code and the container's
	// own last words instead of spending the whole readiness budget dialing
	// a port nothing is listening on.
	var fatalCode failurecodes.Code
	var fatalReason string
	attemptProbe := func(ctx context.Context) error {
		if canceled, err := cancelRequested(); err != nil {
			cancelCheckErr = err
			cancelWait()
			return err
		} else if canceled {
			canceledDuringWait = true
			cancelWait()
			return errors.New("deploy canceled during readiness wait")
		}
		if !firstProbe {
			resolved, err := resolveRoutingTarget(ctx, sched, e.Runtime, candidate.DeploymentID, candidate.InstanceID)
			if err != nil {
				return err
			}
			target = resolved
			routeEvent = routeDeployEvent(routeMetadata, target)
		}
		firstProbe = false
		check := evaluate(target)
		routingOK := check.RoutingReady && check.RouteReady && routingTargetOK(target)
		if !check.ReadyToPromote || !routingOK {
			reason := check.BlockReason()
			if reason == "" {
				reason = readinessRoutingActivationReason(check.RuntimeReady, routingOK)
			}
			progress.observe(reason)
			return errors.New(reason)
		}
		if enforceApplicationReadiness {
			appProbe, err := applicationReadinessProbe(probeConfig, target)
			if err != nil {
				return err
			}
			if appProbe != nil {
				if err := appProbe.CheckReady(ctx); err != nil {
					progress.observe(err.Error())
					return fmt.Errorf("application readiness failed: %w", err)
				}
			}
		}
		return nil
	}
	probe := readiness.ProbeFunc(func(ctx context.Context) error {
		err := attemptProbe(ctx)
		if err == nil || canceledDuringWait || cancelCheckErr != nil {
			return err
		}
		live, observed := observeInstanceLiveness(ctx, sched, e.Runtime, candidate.DeploymentID, candidate.InstanceID)
		if !observed {
			return err
		}
		code, reason, terminal := terminalReadinessFailure(live)
		if !terminal {
			return err
		}
		fatalCode, fatalReason = code, reason
		return readiness.Fatal(errors.New(reason))
	})
	waitErr := readiness.Wait(waitCtx, probe, readiness.Options{
		Timeout:  readinessConfig.Timeout,
		Interval: readinessConfig.Interval,
		Now:      readinessConfig.Now,
		Sleep:    readinessConfig.Sleep,
	})
	if cancelCheckErr != nil {
		return fail(DeployStepReadinessRoutingCheck, failurecodes.InternalError, "cancel request check failed: "+cancelCheckErr.Error(), cancelCheckErr, routeEvent)
	}
	if canceledDuringWait {
		return cancelBeforePromotion(routeEvent)
	}
	req.ReadinessRoutingCheck = evaluate(target)
	req.Ready = req.ReadinessRoutingCheck.RuntimeReady
	req.RoutingOK = req.ReadinessRoutingCheck.RoutingReady && req.ReadinessRoutingCheck.RouteReady && routingTargetOK(target)
	if waitErr != nil {
		// Timed out (or ctx canceled) before the candidate became
		// ready-to-promote. Fail with the same step/code as the pre-refactor
		// one-shot gate and keep the previous active routed (no promotion).
		reason := req.ReadinessRoutingCheck.BlockReason()
		// Runtime/routing can be ready while the configured application
		// HTTP/TCP probe is not. Preserve the probe timeout/error instead of
		// replacing it with the misleading "ready" activation reason.
		if req.ReadinessRoutingCheck.ReadyToPromote {
			reason = waitErr.Error()
		} else if reason == "" {
			reason = readinessRoutingActivationReason(req.Ready, req.RoutingOK)
		}
		// A terminal candidate says far more than "not ready yet": report the
		// exit code and the container's own output under its own code so the
		// operator reads the cause rather than the symptom.
		code := failurecodes.ReconcileStepFailed
		if errors.Is(waitErr, readiness.ErrFatal) && fatalReason != "" {
			code, reason = fatalCode, fatalReason
		}
		cleanupErr := cleanupCandidateBeforePromotion()
		cause := error(errors.New(reason))
		if cleanupErr != nil {
			cause = errors.Join(cause, cleanupErr)
			reason += "; volume candidate cleanup failed: " + cleanupErr.Error()
		}
		return fail(DeployStepReadinessRoutingCheck, code, reason, cause, routeEvent)
	}
	// Every replica must be ready before a public route is replaced. The first
	// replica used the normal polling gate above; remaining replicas were
	// scheduled first and are re-observed here before the atomic backend set.
	for index := range replicaTargets {
		resolved, replicaErr := resolveRoutingTarget(ctx, sched, e.Runtime, candidate.DeploymentID, replicaTargets[index].InstanceID)
		if replicaErr != nil {
			return fail(DeployStepReadinessRoutingCheck, failurecodes.AgentRuntimeUnavailable, "replica routing target failed: "+replicaErr.Error(), replicaErr, routeEvent)
		}
		replicaTargets[index] = resolved
		if e.Instances != nil {
			if instanceErr := e.Instances.Observe(ctx, resolved); instanceErr != nil {
				return fail(DeployStepReadinessRoutingCheck, failurecodes.ReconcileStepFailed, "replica instance re-observation failed: "+instanceErr.Error(), instanceErr, routeDeployEvent(routeMetadata, resolved))
			}
		}
		// Use the same bounded retry gate as r0. A slow Docker HEALTHCHECK on
		// any replica must keep the previous backend set live until every
		// member of the candidate set is promotable.
		replicaTarget := resolved
		firstReplicaProbe := true
		var replicaFatalCode failurecodes.Code
		attemptReplicaProbe := func(probeCtx context.Context) error {
			if !firstReplicaProbe {
				var err error
				replicaTarget, err = resolveRoutingTarget(probeCtx, sched, e.Runtime, candidate.DeploymentID, replicaTarget.InstanceID)
				if err != nil {
					return err
				}
			}
			firstReplicaProbe = false
			check := evaluate(replicaTarget)
			if !check.ReadyToPromote || !routingTargetOK(replicaTarget) {
				return errors.New("replica is not ready for route promotion")
			}
			if enforceApplicationReadiness {
				probe, err := applicationReadinessProbe(probeConfig, replicaTarget)
				if err != nil {
					return err
				}
				if probe != nil {
					return probe.CheckReady(probeCtx)
				}
			}
			return nil
		}
		// Replicas fail fast on the same evidence as r0; one dead replica must
		// not hold the whole rollout for the full readiness budget.
		replicaProbe := readiness.ProbeFunc(func(probeCtx context.Context) error {
			err := attemptReplicaProbe(probeCtx)
			if err == nil {
				return err
			}
			live, observed := observeInstanceLiveness(probeCtx, sched, e.Runtime, candidate.DeploymentID, replicaTarget.InstanceID)
			if !observed {
				return err
			}
			code, reason, terminal := terminalReadinessFailure(live)
			if !terminal {
				return err
			}
			replicaFatalCode = code
			return readiness.Fatal(errors.New(reason))
		})
		if replicaErr = readiness.Wait(ctx, replicaProbe, readiness.Options{
			Timeout: readinessConfig.Timeout, Interval: readinessConfig.Interval,
			Now: readinessConfig.Now, Sleep: readinessConfig.Sleep,
		}); replicaErr != nil {
			replicaCode := failurecodes.ReconcileStepFailed
			if errors.Is(replicaErr, readiness.ErrFatal) && replicaFatalCode != "" {
				replicaCode = replicaFatalCode
			}
			return fail(DeployStepReadinessRoutingCheck, replicaCode, "replica readiness failed: "+replicaErr.Error(), replicaErr, routeDeployEvent(routeMetadata, replicaTarget))
		}
		replicaTargets[index] = replicaTarget
	}
	emit(DeployStepReadinessRoutingCheck, DeployStepStateSucceeded, "", "", routeEvent)
	if canceled, err := cancelRequested(); err != nil {
		return fail(DeployStepReadinessRoutingCheck, failurecodes.InternalError, "cancel request check failed: "+err.Error(), err, routeEvent)
	} else if canceled {
		return cancelBeforePromotion(routeEvent)
	}
	templateVerificationReady := false
	if strings.HasPrefix(payload.Request.SourceRef, "template://") {
		if e.TemplateVerifier == nil || e.TemplateRuns == nil {
			return fail(DeployStepTemplateSmoke, failurecodes.NotImplemented, ErrTemplateVerificationUnavailable.Error(), ErrTemplateVerificationUnavailable, routeEvent)
		}
		if err := e.TemplateRuns.MarkRunning(ctx, candidate.DeploymentID); err != nil {
			return fail(DeployStepTemplateSmoke, failurecodes.ReconcileStepFailed, "template run start failed: "+err.Error(), err, routeEvent)
		}
		cleanupTemplateSmoke := func(evidence templatepkg.VerificationEvidence) error {
			if err := e.TemplateVerifier.Cleanup(ctx, evidence); err != nil {
				return fmt.Errorf("template smoke cleanup failed: %w", err)
			}
			if err := e.TemplateRuns.MarkCleanupCompleted(ctx, candidate.DeploymentID); err != nil {
				return fmt.Errorf("template smoke cleanup persistence failed: %w", err)
			}
			return nil
		}
		emit(DeployStepTemplateSmoke, DeployStepStateStarted, "", "", routeEvent)
		evidence, err := e.TemplateVerifier.Verify(ctx, TemplateVerificationRequest{
			TemplateRef: payload.Request.SourceRef,
			Candidate:   candidate,
			Target:      target,
		})
		if err != nil {
			if cleanupErr := cleanupTemplateSmoke(evidence); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			return fail(DeployStepTemplateSmoke, failureCodeFromError(err, failurecodes.ReconcileStepFailed), err.Error(), err, routeEvent)
		}
		replacementTarget, err := resolveRoutingTarget(
			ctx, sched, e.Runtime, candidate.DeploymentID, candidate.InstanceID,
		)
		if err != nil {
			if cleanupErr := cleanupTemplateSmoke(evidence); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			return fail(DeployStepTemplateSmoke, failurecodes.AgentRuntimeUnavailable, "replacement routing target failed: "+err.Error(), err, routeEvent)
		}
		target = replacementTarget
		routeEvent = routeDeployEvent(routeMetadata, target)
		if e.Instances != nil {
			if err := e.Instances.Observe(ctx, target); err != nil {
				if cleanupErr := cleanupTemplateSmoke(evidence); cleanupErr != nil {
					err = errors.Join(err, cleanupErr)
				}
				return fail(DeployStepTemplateSmoke, failurecodes.ReconcileStepFailed, "replacement instance observation failed: "+err.Error(), err, routeEvent)
			}
		}
		if err := e.TemplateRuns.MarkVerified(ctx, candidate.DeploymentID, evidence); err != nil {
			if cleanupErr := cleanupTemplateSmoke(evidence); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			return fail(DeployStepTemplateSmoke, failurecodes.ReconcileStepFailed, "template smoke evidence persistence failed: "+err.Error(), err, routeEvent)
		}
		if err := cleanupTemplateSmoke(evidence); err != nil {
			return fail(DeployStepTemplateSmoke, failurecodes.ReconcileStepFailed, err.Error(), err, routeEvent)
		}
		templateVerificationReady = true
		emit(DeployStepTemplateSmoke, DeployStepStateSucceeded, "", "", routeEvent)
	}
	emit(DeployStepPromote, DeployStepStateStarted, "", "", routeEvent)
	if err := refreshDeploymentPublicRouteBindings(); err != nil {
		return fail(
			DeployStepPromote,
			failurecodes.ReconcileStepFailed,
			"deployment route binding refresh failed: "+err.Error(),
			err,
			routeEvent,
		)
	}
	if (payload.Request.Mode == "background" ||
		effectiveConfig.RuntimeKind == serviceconfig.RuntimeKindBackground ||
		effectiveConfig.RuntimeKind == serviceconfig.RuntimeKindCron) &&
		routeMetadata.Public {
		return fail(
			DeployStepPromote,
			failurecodes.ResourceValidationFailed,
			ErrBackgroundPublicRoute.Error(),
			ErrBackgroundPublicRoute,
			routeEvent,
		)
	}
	routeEvent = routeDeployEvent(routeMetadata, target)
	if routeMetadata.Public && e.CanonicalCommitter != nil {
		if _, ok := e.Promoter.(TransactionalRoutePromoter); !ok {
			return fail(DeployStepPromote, failurecodes.ReconcileStepFailed, "canonical deployment promotion requires a transactional route promoter", errors.New("canonical deployment promotion requires a transactional route promoter"), routeEvent)
		}
	}
	commitCanonical := func(commitCtx context.Context) error {
		if e.CanonicalCommitter == nil {
			if templateVerificationReady {
				return errors.New("template canonical deployment committer is unavailable")
			}
			return nil
		}
		if templateVerificationReady {
			templateCommitter, ok := e.CanonicalCommitter.(CanonicalTemplateDeploymentCommitter)
			if !ok {
				return errors.New("atomic template canonical deployment committer is unavailable")
			}
			return templateCommitter.CommitCanonicalTemplateDeployment(
				commitCtx, payload.OperationID, candidate.DeploymentID,
			)
		}
		return e.CanonicalCommitter.CommitCanonicalDeployment(commitCtx, payload.OperationID)
	}
	if routeMetadata.Public {
		var promoteErr error
		if transactional, ok := e.Promoter.(TransactionalRoutePromoter); ok {
			if len(replicaTargets) > 0 {
				promoteErr = transactional.PromoteReplicasWithCommit(ctx, append([]runtime.RoutingTarget{target}, replicaTargets...), routeMetadata, commitCanonical)
			} else {
				promoteErr = transactional.PromoteWithCommit(ctx, target, routeMetadata, commitCanonical)
			}
		} else if len(replicaTargets) > 0 {
			batch, ok := e.Promoter.(ReplicaRoutePromoter)
			if !ok {
				promoteErr = errors.New("configured route promoter does not support replica backend sets")
			} else {
				promoteErr = batch.PromoteReplicas(ctx, append([]runtime.RoutingTarget{target}, replicaTargets...), routeMetadata)
			}
		} else {
			promoteErr = e.Promoter.Promote(ctx, target, routeMetadata)
		}
		if promoteErr != nil {
			if cleanupErr := cleanupCandidateBeforePromotion(); cleanupErr != nil {
				promoteErr = errors.Join(promoteErr, cleanupErr)
			}
			return fail(DeployStepPromote, routePublicationFailureCode(promoteErr), "promote failed: "+promoteErr.Error(), promoteErr, routeEvent)
		}
	} else if err := commitCanonical(ctx); err != nil {
		return fail(DeployStepPromote, failurecodes.ReconcileStepFailed, "canonical deployment promotion failed: "+err.Error(), err, routeEvent)
	}
	req.Promoted = true
	candidatePromoted = true
	emit(DeployStepPromote, DeployStepStateSucceeded, "", "", routeEvent)
	if hasPreviousActive(req.PreviousActive) && !previousDrained {
		emit(DeployStepDrainPrevious, DeployStepStateStarted, "", "", nil)
		if err := drainDeploymentInstances(ctx, sched, e.Runtime, req.PreviousActive.DeploymentID, req.PreviousActive.InstanceID, drainGrace); err != nil {
			// Canonical activation and public route commit have succeeded. The old
			// deployment is now cleanup work: record it for telemetry/retry
			// reconciliation, but never turn an active candidate into a failed
			// operation or attempt candidate cleanup after the fact.
			emit(DeployStepDrainPrevious, DeployStepStateFailed, failurecodes.ReconcileStepFailed, "drain previous pending retry: "+err.Error(), nil)
		} else {
			emit(DeployStepDrainPrevious, DeployStepStateSucceeded, "", "", nil)
		}
	}
	plan, err := PlanCandidateActivation(req)
	if err != nil {
		return fail("", failurecodes.InternalError, err.Error(), err, nil)
	}
	if _, err := e.Operations.Transition(ctx, opID, orailtypes.OperationSucceeded); err != nil {
		return fail("", failurecodes.OperationInvalidTransition, err.Error(), err, nil)
	}
	out.Plan = plan
	out.Succeeded = true
	emit(DeployStepSucceeded, DeployStepStateSucceeded, "", "", routeEvent)
	return out, nil
}

// maxReadinessFailureReasonBytes bounds the failure reason a workload's own
// output can produce. Log lines are untrusted workload text; they explain a
// crash, they do not get to fill an operation event.
const maxReadinessFailureReasonBytes = 2000

// observeInstanceLiveness asks the runtime why a candidate is not ready yet.
// The scheduler is preferred (it is the newer boundary) and the legacy
// ActivationRuntime is the fallback. A runtime that cannot answer reports
// false, and the caller keeps waiting rather than guessing that silence
// means death.
func observeInstanceLiveness(
	ctx context.Context,
	sched scheduler.Scheduler,
	rt ActivationRuntime,
	deploymentID, instanceID string,
) (runtime.InstanceLiveness, bool) {
	for _, source := range []any{sched, rt} {
		observer, ok := source.(runtime.InstanceLivenessObserver)
		if !ok {
			continue
		}
		live, err := observer.ObserveInstanceLiveness(ctx, deploymentID, instanceID)
		if err != nil || !live.Observed {
			continue
		}
		return live, true
	}
	return runtime.InstanceLiveness{}, false
}

// imageDeclaresPort reports whether the image itself names a port, and
// whether the runtime could answer at all. An unanswerable question leaves
// the configured readiness probe exactly as the operator left it.
func imageDeclaresPort(
	ctx context.Context,
	sched scheduler.Scheduler,
	rt ActivationRuntime,
	image string,
) (declares bool, known bool) {
	if strings.TrimSpace(image) == "" {
		return false, false
	}
	for _, source := range []any{sched, rt} {
		reporter, ok := source.(runtime.ImagePortReporter)
		if !ok {
			continue
		}
		ports, err := reporter.ImageDeclaredPorts(ctx, image)
		if err != nil {
			continue
		}
		return len(ports) > 0, true
	}
	return false, false
}

// terminalReadinessFailure turns liveness evidence into the failure an
// operator can act on. Only a stopped or restart-looping candidate is
// terminal; anything else is still legitimately starting and keeps waiting.
func terminalReadinessFailure(live runtime.InstanceLiveness) (failurecodes.Code, string, bool) {
	switch {
	case live.Exited:
		return failurecodes.DeployCandidateExited,
			readinessFailureReason("the container stopped before it was ready", live), true
	case live.RestartLooping:
		headline := fmt.Sprintf("the container keeps restarting (%d restarts) and never became ready", live.Restarts)
		return failurecodes.DeployCandidateRestartLoop, readinessFailureReason(headline, live), true
	default:
		return "", "", false
	}
}

// readinessFailureReason states what happened, then how the container ended,
// then its own last lines. Operators read this in the timeline and on the
// service card, so it stays a sentence rather than a struct dump.
func readinessFailureReason(headline string, live runtime.InstanceLiveness) string {
	parts := []string{headline}
	if detail := strings.TrimSpace(live.Reason); detail != "" {
		parts = append(parts, detail)
	}
	if tail := strings.TrimSpace(strings.Join(live.LogTail, " | ")); tail != "" {
		parts = append(parts, "last log lines: "+tail)
	}
	reason := strings.Join(parts, "; ")
	if len(reason) > maxReadinessFailureReasonBytes {
		reason = reason[:maxReadinessFailureReasonBytes] + "…"
	}
	return reason
}

func applicationReadinessProbe(cfg serviceconfig.Config, target runtime.RoutingTarget) (readiness.Probe, error) {
	if cfg.HealthKind == serviceconfig.HealthNone {
		return nil, nil
	}
	host := strings.TrimSpace(target.ContainerIP)
	if host == "" {
		return nil, fmt.Errorf("%w: routing target has no container IP", readiness.ErrProbeInvalid)
	}
	port := cfg.HealthPort
	if port == 0 {
		port = target.InternalPort
	}
	if port == 0 {
		port = cfg.TargetPort
	}
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	switch cfg.HealthKind {
	case serviceconfig.HealthHTTP:
		targetURL := url.URL{Scheme: "http", Host: address, Path: cfg.HealthPath}
		return readiness.HTTPProbe{URL: targetURL.String()}, nil
	case serviceconfig.HealthTCP:
		return readiness.TCPProbe{Address: address}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported health kind %q", readiness.ErrProbeInvalid, cfg.HealthKind)
	}
}

// routingTargetResolver is the optional capability the readiness poll loop
// uses to re-resolve a candidate's routing target between retries. The legacy
// ActivationRuntime satisfies it directly; a scheduler.Scheduler that also
// exposes ResolveRoutingTarget is preferred over the State() fallback.
type routingTargetResolver interface {
	ResolveRoutingTarget(ctx context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error)
}

// resolveRoutingTarget re-reads the candidate's routing target for a readiness
// retry. It prefers a scheduler that exposes ResolveRoutingTarget, then the
// legacy ActivationRuntime, and finally falls back to Scheduler.State (which
// LocalDockerScheduler backs with ResolveRoutingTarget) so any scheduler wiring
// can be polled without reaching into the runtime adapter directly.
func resolveRoutingTarget(ctx context.Context, sched scheduler.Scheduler, rt ActivationRuntime, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	if resolver, ok := sched.(routingTargetResolver); ok {
		return resolver.ResolveRoutingTarget(ctx, deploymentID, instanceID)
	}
	if rt != nil {
		return rt.ResolveRoutingTarget(ctx, deploymentID, instanceID)
	}
	state, err := sched.State(ctx, deploymentID+"/"+instanceID)
	if err != nil {
		return runtime.RoutingTarget{}, err
	}
	return state.RoutingTarget, nil
}

// drainInstance prefers the scheduler's InstanceDrainer capability (the
// local scheduler exposes DrainInstance which keeps deployment + instance
// ids separate) and falls back to the legacy ActivationRuntime.Drain when
// the executor is wired with a pre-ADR-0018 Runtime.
func drainInstance(ctx context.Context, sched scheduler.Scheduler, runtime ActivationRuntime, deploymentID, instanceID string, grace time.Duration) error {
	if drainer, ok := sched.(InstanceDrainer); ok {
		return drainer.DrainInstance(ctx, deploymentID, instanceID, grace)
	}
	if runtime != nil {
		return runtime.Drain(ctx, deploymentID, instanceID, grace)
	}
	// Last-resort: schedule.Drain expects a single id; encode the
	// (deployment, instance) pair using the local scheduler's "/" convention.
	return sched.Drain(ctx, deploymentID+"/"+instanceID, grace)
}

// deploymentInstanceLister is implemented by the Docker adapter after its
// startup reconciliation. It lets a replacement deploy drain every durable
// instance of the old deployment rather than only its historical r0 record.
type deploymentInstanceLister interface {
	DeploymentInstanceIDs(context.Context, string) ([]string, error)
}

func drainDeploymentInstances(ctx context.Context, sched scheduler.Scheduler, rt ActivationRuntime, deploymentID, fallbackInstanceID string, grace time.Duration) error {
	ids := []string{fallbackInstanceID}
	if lister, ok := rt.(deploymentInstanceLister); ok {
		listed, err := lister.DeploymentInstanceIDs(ctx, deploymentID)
		if err != nil {
			return err
		}
		if len(listed) > 0 {
			ids = listed
		}
	}
	seen := make(map[string]struct{}, len(ids))
	for _, instanceID := range ids {
		if instanceID == "" {
			continue
		}
		if _, ok := seen[instanceID]; ok {
			continue
		}
		seen[instanceID] = struct{}{}
		if err := drainInstance(ctx, sched, rt, deploymentID, instanceID, grace); err != nil {
			return err
		}
	}
	return nil
}

type reasonedFailure interface {
	Reason() (failurecodes.Code, string)
}

func failureCodeFromError(err error, fallback failurecodes.Code) failurecodes.Code {
	var reasoned reasonedFailure
	if errors.As(err, &reasoned) {
		code, _ := reasoned.Reason()
		if failurecodes.IsRegistered(code) {
			return code
		}
	}
	if errors.Is(err, runtime.ErrUnsupportedOperation) {
		return failurecodes.RuntimeUnsupportedOperation
	}
	return fallback
}

func routePublicationFailureCode(err error) failurecodes.Code {
	code := failureCodeFromError(err, failurecodes.ReconcileStepFailed)
	if code != failurecodes.ReconcileStepFailed {
		return code
	}
	if errors.Is(err, ErrRoutePromotionRequired) ||
		errors.Is(err, ErrRoutePromotionPublishFailed) ||
		errors.Is(err, ErrRoutePromotionInvalidTarget) {
		return failurecodes.RoutePublicationFailed
	}
	return failurecodes.ReconcileStepFailed
}

func routePromotionMetadata(payload DeployJobPayload, candidate runtime.ScheduleRequest) RoutePromotionMetadata {
	metadata := RoutePromotionMetadata{
		ServiceID: candidate.ServiceID,
	}
	if payload.Route == nil {
		return metadata
	}
	metadata.RouteName = payload.Route.Name
	metadata.RouteGroup = payload.Route.Group
	metadata.Host = payload.Route.Host
	metadata.PathPrefix = payload.Route.Path
	metadata.Public = payload.Route.Public
	metadata.TLS = payload.Route.TLS
	return metadata
}

func routingTargetOK(target runtime.RoutingTarget) bool {
	return strings.TrimSpace(target.ContainerID) != "" || strings.TrimSpace(target.ContainerIP) != "" || target.InternalPort > 0
}

func readinessStatusFromTarget(candidate runtime.ScheduleRequest, target runtime.RoutingTarget) runtime.InstanceStatus {
	state := runtime.InstanceStateRunning
	if target.Ready {
		state = runtime.InstanceStateReady
	}
	return runtime.InstanceStatus{
		DeploymentID: firstNonEmpty(target.DeploymentID, candidate.DeploymentID),
		InstanceID:   firstNonEmpty(target.InstanceID, candidate.InstanceID),
		State:        state,
		ObservedAt:   target.ObservedAt,
	}
}

func hasPreviousActive(ref ActiveInstanceRef) bool {
	return strings.TrimSpace(ref.DeploymentID) != "" && strings.TrimSpace(ref.InstanceID) != ""
}

// resolveDeployVariables renders the resolved env map for the candidate
// service and returns a redaction-safe snapshot describing the result.
//
// The snapshot includes only the variable names and length buckets so
// the operation event log can record the shape of the resolved map
// without leaking any plaintext value.
func resolveDeployVariables(
	ctx context.Context,
	resolver VariableResolver,
	candidate runtime.ScheduleRequest,
	pinned []varrefs.VariableVersionRef,
	requirePinned bool,
) (map[string]string, *DeployVariablesSnapshot, error) {
	var env map[string]string
	var err error
	if requirePinned {
		versioned, ok := resolver.(versionedVariableResolver)
		if !ok {
			return nil, nil, errors.New("deployment variable resolver does not support immutable versions")
		}
		env, err = versioned.ResolveAtVersions(ctx, candidate.ProjectID, candidate.EnvironmentID, candidate.ServiceID, pinned)
	} else {
		env, err = resolver.Resolve(ctx, candidate.ProjectID, candidate.EnvironmentID, candidate.ServiceID)
	}
	if err != nil {
		return nil, nil, err
	}
	snapshot := &DeployVariablesSnapshot{Count: len(env)}
	keys := make([]string, 0, len(env))
	for name := range env {
		keys = append(keys, name)
	}
	sortStrings(keys)
	for _, name := range keys {
		snapshot.Items = append(snapshot.Items, DeployVariableSnapshotI{
			Name:         name,
			LengthBucket: lengthBucket(len(env[name])),
		})
	}
	return env, snapshot, nil
}

// variableFailureCode maps a resolver error onto the registered failure
// code surfaced through the operation envelope.
func variableFailureCode(err error) failurecodes.Code {
	if err == nil {
		return failurecodes.InternalError
	}
	var infrastructure variableErrInfrastructure
	if errors.As(err, &infrastructure) {
		return failurecodes.InternalError
	}
	var cycle variableErrCycle
	if errors.As(err, &cycle) {
		return failurecodes.VariableReferenceCycle
	}
	var unknown variableErrUnknown
	if errors.As(err, &unknown) {
		return failurecodes.VariableReferenceUnresolved
	}
	var syntax variableErrSyntax
	if errors.As(err, &syntax) {
		return failurecodes.ResourceValidationFailed
	}
	// Fall back to typed unknown if the resolver returns a sentinel via
	// the public varrefs package without our local interfaces wired —
	// callers can plug their own resolver type via the VariableResolver
	// interface; we want the deploy executor to keep working without
	// hard-depending on varrefs symbol names.
	return failurecodes.VariableReferenceUnresolved
}

func variableFailureReason(err error) string {
	if err == nil {
		return "variable resolution failed"
	}
	var infrastructure variableErrInfrastructure
	if errors.As(err, &infrastructure) {
		return "variable resolution infrastructure is unavailable"
	}
	return err.Error()
}

// variableErrUnknown / variableErrCycle / variableErrSyntax are the
// minimal interfaces the executor matches against. Any concrete resolver
// can satisfy them by exposing the relevant Error() shape — see
// internal/varrefs for the canonical implementation. The marker method
// names are exported so they can cross package boundaries without the
// hidden-method visibility quirk that Go applies to unexported methods.
type variableErrUnknown interface{ IsUnknownVariableReference() }
type variableErrInfrastructure interface{ IsVariableInfrastructureFailure() }
type variableErrCycle interface{ IsVariableReferenceCycle() }
type variableErrSyntax interface{ IsInvalidVariableReferenceSyntax() }

// emitDeployStepWithVariables centralises the resolve-variables event
// emission so the snapshot field stays attached.
func emitDeployStepWithVariables(e Executor, ctx context.Context, base DeployStepEvent, step DeployStep, state DeployStepState, code failurecodes.Code, reason string, snapshot *DeployVariablesSnapshot) {
	event := base
	event.Step = step
	event.State = state
	event.FailureCode = code
	event.FailureReason = reason
	event.Variables = snapshot
	e.observeDeployStep(ctx, event)
}

func lengthBucket(n int) string {
	switch {
	case n <= 0:
		return "empty"
	case n <= 16:
		return "1-16"
	case n <= 64:
		return "17-64"
	case n <= 256:
		return "65-256"
	case n <= 1024:
		return "257-1024"
	default:
		return "1024+"
	}
}

func sortStrings(s []string) {
	// Tiny insertion sort to avoid pulling in sort for this single-shot
	// snapshot — n is bounded by the number of variables on a service.
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j-1] > s[j] {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// readinessProgressInterval bounds how often the readiness wait reports the
// current block reason onto the operation timeline.
const readinessProgressInterval = 15 * time.Second

// readinessProgressReporter throttles readiness progress events. The first
// blocked probe never reports (most rollouts are ready within a few polls);
// afterwards it reports at most once per readinessProgressInterval and only
// when the block reason is new or the interval has elapsed.
type readinessProgressReporter struct {
	now        func() time.Time
	report     func(reason string)
	firstSeen  time.Time
	lastReport time.Time
	lastReason string
}

func newReadinessProgressReporter(now func() time.Time, report func(string)) *readinessProgressReporter {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &readinessProgressReporter{now: now, report: report}
}

func (r *readinessProgressReporter) observe(reason string) {
	if r == nil || r.report == nil {
		return
	}
	current := r.now()
	if r.firstSeen.IsZero() {
		r.firstSeen = current
		r.lastReason = reason
		return
	}
	if current.Sub(r.firstSeen) < readinessProgressInterval {
		return
	}
	if !r.lastReport.IsZero() && current.Sub(r.lastReport) < readinessProgressInterval && reason == r.lastReason {
		return
	}
	r.lastReport = current
	r.lastReason = reason
	r.report(reason)
}
