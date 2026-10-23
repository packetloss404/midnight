package build

import (
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
)

// RollbackPlanSchemaVersion identifies the JSON schema produced by
// PlanRollback. It is bumped whenever the step list shape changes.
const RollbackPlanSchemaVersion = "midnight.dev/build-rollback-plan/v1alpha1"

// RollbackStepID identifies one step in the panel-locked 14-step rollback
// algorithm. The values are stable and used by audit consumers as
// machine-readable step identifiers.
type RollbackStepID string

const (
	// RollbackStepResolveTarget — step 1: resolve the rollback selector to
	// a concrete deployment snapshot.
	RollbackStepResolveTarget RollbackStepID = "resolve-target-snapshot"
	// RollbackStepAuthorize — step 2: confirm the actor has scope and any
	// production-environment-protection rules are satisfied.
	RollbackStepAuthorize RollbackStepID = "authorize"
	// RollbackStepAcquireLock — step 3: acquire the per-(service,environment)
	// single-flight advisory lock.
	RollbackStepAcquireLock RollbackStepID = "acquire-single-flight-lock"
	// RollbackStepInsertDeployment — step 4: INSERT a new QUEUED deployment
	// row referencing the source snapshot. Increments the active-pin row.
	RollbackStepInsertDeployment RollbackStepID = "insert-new-deployment"
	// RollbackStepEmitAudit — step 5: emit the rollback.requested audit row
	// and CloudEvents queued event.
	RollbackStepEmitAudit RollbackStepID = "emit-audit-and-event"
	// RollbackStepEnqueueDeploy — step 6: enqueue the deploy job in the same
	// transaction (transactional outbox).
	RollbackStepEnqueueDeploy RollbackStepID = "enqueue-deploy-job"
	// RollbackStepWorkerPull — step 7: deploy worker leases the job and
	// pulls the recorded image digest.
	RollbackStepWorkerPull RollbackStepID = "worker-pull-image"
	// RollbackStepVerifySignature — step 8: agent verifies the cosign
	// signature against the configured key set.
	RollbackStepVerifySignature RollbackStepID = "verify-signature"
	// RollbackStepResolveSecrets — step 9: dereference snapshot secret
	// references against the secret store at deploy time.
	RollbackStepResolveSecrets RollbackStepID = "resolve-secrets"
	// RollbackStepScheduleAndStart — step 10: schedule and start the
	// container.
	RollbackStepScheduleAndStart RollbackStepID = "schedule-and-start"
	// RollbackStepPromote — step 11: reconciler promotes DEPLOYING to
	// ACTIVE.
	RollbackStepPromote RollbackStepID = "promote-to-active"
	// RollbackStepDrainPrevious — step 12: drain the previous ACTIVE
	// instance.
	RollbackStepDrainPrevious RollbackStepID = "drain-previous-active"
	// RollbackStepDecrementPin — step 13: decrement the just-superseded
	// deployment's pin reference and convert it to a rollback-window pin.
	RollbackStepDecrementPin RollbackStepID = "decrement-superseded-pin"
	// RollbackStepFinalAudit — step 14: emit the rollback.completed audit
	// row.
	RollbackStepFinalAudit RollbackStepID = "final-audit"
)

// RollbackInvariantNoBuild is the named invariant the planner enforces on
// every produced plan. It must never re-invoke BuildKit: rollback is a
// digest pull. The CLI surface and tests assert this invariant by checking
// for its presence in the produced plan.
const RollbackInvariantNoBuild = "noBuild=true"

// RollbackInput is the planner input. It bundles the resolved snapshot, the
// actor, and the source classification needed to set noBuild=true and the
// step descriptions.
type RollbackInput struct {
	// ServiceID is the service the rollback runs for.
	ServiceID string
	// EnvironmentID is the environment the rollback runs in.
	EnvironmentID string
	// Target is the user-supplied rollback selector.
	Target RollbackTarget
	// Source is the resolved deployment snapshot the rollback replays.
	Source DeploymentSnapshotRecord
	// ActorID is the actor performing the rollback. Required.
	ActorID string
	// Reason is the operator-supplied free-text reason for the rollback.
	Reason string
	// IdempotencyKey is the rollback request idempotency key.
	IdempotencyKey string
	// RollbackSource classifies how the rollback was triggered. Defaults to
	// "manual" when empty.
	RollbackSource string
}

// RollbackStep is one ordered, human-readable rollback algorithm step.
type RollbackStep struct {
	Order       int            `json:"order"`
	ID          RollbackStepID `json:"id"`
	Description string         `json:"description"`
	// Idempotent flags whether a step may be safely re-run if the worker is
	// resumed mid-flight. Steps that only emit audit events are idempotent;
	// steps that mutate the deployments table are guarded by the operation
	// FSM.
	Idempotent bool `json:"idempotent"`
}

// RollbackPlan is the output of PlanRollback. It is a pure description of
// the steps the deploy worker is expected to execute. The planner does not
// invoke any builder or registry client.
type RollbackPlan struct {
	SchemaVersion    string         `json:"schemaVersion"`
	ServiceID        string         `json:"serviceId"`
	EnvironmentID    string         `json:"environmentId"`
	ActorID          string         `json:"actorId"`
	IdempotencyKey   string         `json:"idempotencyKey,omitempty"`
	RollbackSource   string         `json:"rollbackSource"`
	SourceDeployment string         `json:"sourceDeployment"`
	SourceRevision   string         `json:"sourceRevision,omitempty"`
	ImageDigest      string         `json:"imageDigest"`
	NoBuildInvariant string         `json:"noBuildInvariant"`
	Steps            []RollbackStep `json:"steps"`
}

// PlanRollback returns the panel-locked 14-step rollback plan for the
// supplied input. The plan is purely descriptive; PlanRollback never invokes
// BuildKit and the produced plan always carries noBuild=true.
func PlanRollback(input RollbackInput) (RollbackPlan, error) {
	if strings.TrimSpace(input.ServiceID) == "" {
		return RollbackPlan{}, errors.New("rollback plan serviceId must not be empty")
	}
	if strings.TrimSpace(input.EnvironmentID) == "" {
		return RollbackPlan{}, errors.New("rollback plan environmentId must not be empty")
	}
	if strings.TrimSpace(input.ActorID) == "" {
		return RollbackPlan{}, errors.New("rollback plan actorId must not be empty")
	}
	if input.Source.Deployment == "" {
		return RollbackPlan{}, errors.New("rollback plan source deployment must not be empty")
	}
	pins := input.Source.Snapshot.ImagePins()
	if len(pins) == 0 {
		return RollbackPlan{}, failurecodes.New(failurecodes.RollbackImageUnavailable,
			"rollback plan source snapshot must include a digest pin")
	}
	digest := pins[0].Digest
	if digest == "" {
		return RollbackPlan{}, failurecodes.New(failurecodes.RollbackImageUnavailable,
			"rollback plan source image digest must not be empty")
	}
	rollbackSource := input.RollbackSource
	if rollbackSource == "" {
		rollbackSource = "manual"
	}

	steps := defaultRollbackSteps()
	plan := RollbackPlan{
		SchemaVersion:    RollbackPlanSchemaVersion,
		ServiceID:        input.ServiceID,
		EnvironmentID:    input.EnvironmentID,
		ActorID:          input.ActorID,
		IdempotencyKey:   input.IdempotencyKey,
		RollbackSource:   rollbackSource,
		SourceDeployment: input.Source.Deployment,
		SourceRevision:   input.Source.Revision,
		ImageDigest:      digest,
		NoBuildInvariant: RollbackInvariantNoBuild,
		Steps:            steps,
	}
	if err := plan.Validate(); err != nil {
		return RollbackPlan{}, err
	}
	return plan, nil
}

// Validate enforces the structural invariants on a rollback plan. The most
// important invariant is that the noBuild flag is present and set to true.
func (p RollbackPlan) Validate() error {
	if p.SchemaVersion != RollbackPlanSchemaVersion {
		return fmt.Errorf("rollback plan schema %q is not %q", p.SchemaVersion, RollbackPlanSchemaVersion)
	}
	if p.ServiceID == "" || p.EnvironmentID == "" || p.ActorID == "" {
		return errors.New("rollback plan service/environment/actor must not be empty")
	}
	if p.SourceDeployment == "" {
		return errors.New("rollback plan sourceDeployment must not be empty")
	}
	if p.ImageDigest == "" {
		return errors.New("rollback plan imageDigest must not be empty")
	}
	if p.NoBuildInvariant != RollbackInvariantNoBuild {
		return fmt.Errorf("rollback plan noBuildInvariant must be %q", RollbackInvariantNoBuild)
	}
	if len(p.Steps) != len(defaultRollbackStepIDs) {
		return fmt.Errorf("rollback plan has %d steps, want %d", len(p.Steps), len(defaultRollbackStepIDs))
	}
	for i, step := range p.Steps {
		if step.Order != i+1 {
			return fmt.Errorf("rollback plan step[%d].Order = %d, want %d", i, step.Order, i+1)
		}
		if step.ID != defaultRollbackStepIDs[i] {
			return fmt.Errorf("rollback plan step[%d].ID = %q, want %q", i, step.ID, defaultRollbackStepIDs[i])
		}
		if step.Description == "" {
			return fmt.Errorf("rollback plan step[%d].Description must not be empty", i)
		}
	}
	return nil
}

// HasNoBuildInvariant reports whether the plan carries the noBuild=true
// invariant. It is a convenience for tests and CLI output that need to
// assert the rollback path will not invoke BuildKit.
func (p RollbackPlan) HasNoBuildInvariant() bool {
	return p.NoBuildInvariant == RollbackInvariantNoBuild
}

func defaultRollbackSteps() []RollbackStep {
	return []RollbackStep{
		{Order: 1, ID: RollbackStepResolveTarget, Description: "Resolve the rollback target to a concrete deployment snapshot id.", Idempotent: true},
		{Order: 2, ID: RollbackStepAuthorize, Description: "Authorize the actor and apply environment-protection rules.", Idempotent: true},
		{Order: 3, ID: RollbackStepAcquireLock, Description: "Acquire the per-(service,environment) single-flight advisory lock.", Idempotent: false},
		{Order: 4, ID: RollbackStepInsertDeployment, Description: "Insert a new QUEUED deployment referencing the source snapshot and increment the active pin.", Idempotent: false},
		{Order: 5, ID: RollbackStepEmitAudit, Description: "Emit deployment.rollback.requested audit row and CloudEvents queued event.", Idempotent: true},
		{Order: 6, ID: RollbackStepEnqueueDeploy, Description: "Enqueue the deploy worker job in the same transactional outbox.", Idempotent: false},
		{Order: 7, ID: RollbackStepWorkerPull, Description: "Deploy worker leases the job, transitions QUEUED to DEPLOYING, and pulls the recorded image digest (no rebuild).", Idempotent: true},
		{Order: 8, ID: RollbackStepVerifySignature, Description: "Agent verifies the cosign signature against the configured key set.", Idempotent: true},
		{Order: 9, ID: RollbackStepResolveSecrets, Description: "Dereference snapshot secret references against the secret store at deploy time.", Idempotent: true},
		{Order: 10, ID: RollbackStepScheduleAndStart, Description: "Schedule the instance, start the container, run the readiness probe, and add to the route table.", Idempotent: false},
		{Order: 11, ID: RollbackStepPromote, Description: "Reconciler promotes the deployment from DEPLOYING to ACTIVE.", Idempotent: false},
		{Order: 12, ID: RollbackStepDrainPrevious, Description: "Drain the previous ACTIVE instance with SIGTERM, grace period, and SIGKILL fallback.", Idempotent: false},
		{Order: 13, ID: RollbackStepDecrementPin, Description: "Decrement the just-superseded deployment's pin reference and convert it to a rollback-window pin.", Idempotent: true},
		{Order: 14, ID: RollbackStepFinalAudit, Description: "Emit deployment.rollback.completed audit row with new deployment id, source snapshot id, and signature key id.", Idempotent: true},
	}
}

var defaultRollbackStepIDs = []RollbackStepID{
	RollbackStepResolveTarget,
	RollbackStepAuthorize,
	RollbackStepAcquireLock,
	RollbackStepInsertDeployment,
	RollbackStepEmitAudit,
	RollbackStepEnqueueDeploy,
	RollbackStepWorkerPull,
	RollbackStepVerifySignature,
	RollbackStepResolveSecrets,
	RollbackStepScheduleAndStart,
	RollbackStepPromote,
	RollbackStepDrainPrevious,
	RollbackStepDecrementPin,
	RollbackStepFinalAudit,
}
