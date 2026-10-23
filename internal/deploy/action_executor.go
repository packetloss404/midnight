// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

const DeploymentActionJobPayloadSchema = "midnight.dev/deployment-action-job/v1"

type RuntimeAction string

const (
	RuntimeActionStop    RuntimeAction = "stop"
	RuntimeActionRestart RuntimeAction = "restart"
	RuntimeActionRemove  RuntimeAction = "remove"
)

func ValidRuntimeAction(action RuntimeAction) bool {
	switch action {
	case RuntimeActionStop, RuntimeActionRestart, RuntimeActionRemove:
		return true
	default:
		return false
	}
}

type DeploymentActionJobPayload struct {
	SchemaVersion string        `json:"schema_version"`
	ActionID      string        `json:"action_id"`
	OperationID   string        `json:"operation_id"`
	DeploymentID  string        `json:"deployment_id"`
	Action        RuntimeAction `json:"action"`
}

type RuntimeActionRecord struct {
	ID               string
	OperationID      string
	DeploymentID     string
	ProjectID        string
	ServiceID        string
	EnvironmentID    string
	InstanceID       string
	Action           RuntimeAction
	State            string
	Step             string
	DeploymentActive bool
	DesiredState     string
}

type RuntimeActionStore interface {
	BeginRuntimeAction(context.Context, string, string) (RuntimeActionRecord, error)
	RecordRuntimeActionStep(context.Context, string, string) error
	CompleteRuntimeAction(context.Context, string, string) error
	FailRuntimeAction(context.Context, string, failurecodes.Code, string) error
}

type RuntimeActionRuntime interface {
	Start(context.Context, string, string) error
	Stop(context.Context, string, string, time.Duration) error
	Remove(context.Context, string, string) error
}

type deploymentInstanceRuntime interface {
	DeploymentInstanceIDs(context.Context, string) ([]string, error)
}

type RuntimeActionRouteWithdrawer interface {
	WithdrawDeployment(context.Context, RuntimeActionRecord) error
}

type RuntimeActionInstanceStore interface {
	SetDeploymentState(context.Context, string, string, string) error
}

type ActionExecutor struct {
	Store      RuntimeActionStore
	Runtime    RuntimeActionRuntime
	Routes     RuntimeActionRouteWithdrawer
	Instances  RuntimeActionInstanceStore
	Operations OperationUpdater
	Cancels    CancelChecker
	StopGrace  time.Duration
}

func IsDeploymentActionJob(payload []byte) bool {
	var envelope struct {
		SchemaVersion string `json:"schema_version"`
	}
	return json.Unmarshal(payload, &envelope) == nil &&
		strings.TrimSpace(envelope.SchemaVersion) == DeploymentActionJobPayloadSchema
}

func DecodeDeploymentActionJob(payload []byte) (DeploymentActionJobPayload, error) {
	var job DeploymentActionJobPayload
	if err := json.Unmarshal(payload, &job); err != nil {
		return DeploymentActionJobPayload{}, fmt.Errorf("deployment action job: decode: %w", err)
	}
	job.SchemaVersion = strings.TrimSpace(job.SchemaVersion)
	job.ActionID = strings.TrimSpace(job.ActionID)
	job.OperationID = strings.TrimSpace(job.OperationID)
	job.DeploymentID = strings.TrimSpace(job.DeploymentID)
	job.Action = RuntimeAction(strings.TrimSpace(string(job.Action)))
	if job.SchemaVersion != DeploymentActionJobPayloadSchema ||
		job.ActionID == "" || job.OperationID == "" || job.DeploymentID == "" ||
		!ValidRuntimeAction(job.Action) {
		return DeploymentActionJobPayload{}, errors.New("deployment action job: invalid identity, schema, or action")
	}
	return job, nil
}

func (e ActionExecutor) Execute(ctx context.Context, job queue.Job) (ExecutionOutcome, error) {
	payload, err := DecodeDeploymentActionJob(job.Payload)
	if err != nil {
		return ExecutionOutcome{JobID: job.ID, FailureCode: failurecodes.ResourceValidationFailed, FailureReason: err.Error()}, err
	}
	out := ExecutionOutcome{JobID: job.ID, OperationID: payload.OperationID}
	if e.Store == nil || e.Runtime == nil || e.Operations == nil {
		err := errors.New("deployment action executor is not configured")
		out.FailureCode, out.FailureReason = failurecodes.InternalError, err.Error()
		return out, err
	}
	if _, err := e.Operations.Transition(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationRunning); err != nil {
		out.FailureCode, out.FailureReason = failurecodes.OperationInvalidTransition, err.Error()
		return out, err
	}
	record, err := e.Store.BeginRuntimeAction(ctx, payload.ActionID, payload.OperationID)
	if err != nil {
		return e.fail(ctx, out, payload.ActionID, failurecodes.ResourceConflict, err)
	}
	if record.DeploymentID != payload.DeploymentID || record.Action != payload.Action {
		return e.fail(ctx, out, payload.ActionID, failurecodes.ResourceValidationFailed,
			errors.New("deployment action job does not match durable action"))
	}

	// The only cancellation safe point is after the lease is held and the
	// durable action is marked running, but before the first external side
	// effect. Once route/runtime mutation begins the action converges forward.
	if e.Cancels != nil {
		canceled, cancelErr := e.Cancels.CancelRequested(ctx, payload.OperationID)
		if cancelErr != nil {
			return e.fail(ctx, out, payload.ActionID, failurecodes.InternalError, cancelErr)
		}
		if canceled {
			if err := e.Store.FailRuntimeAction(ctx, payload.ActionID, failurecodes.DeployCanceledBeforePromotion,
				"deployment action canceled before its first runtime side effect"); err != nil {
				return e.fail(ctx, out, payload.ActionID, failurecodes.InternalError, err)
			}
			if _, err := e.Operations.Transition(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationCanceled); err != nil {
				return e.fail(ctx, out, payload.ActionID, failurecodes.OperationInvalidTransition, err)
			}
			out.FailureCode = failurecodes.DeployCanceledBeforePromotion
			out.FailureReason = "deployment action canceled at its pre-effect safe point"
			return out, nil
		}
	}

	switch record.Action {
	case RuntimeActionStop:
		if !record.DeploymentActive {
			return e.fail(ctx, out, record.ID, failurecodes.ResourceConflict,
				errors.New("stop requires the active deployment"))
		}
		record.InstanceID = "" // withdraw every durable backend for this deployment
		if err := e.withdraw(ctx, record); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.ReconcileStepFailed, err)
		}
		if err := e.forEachInstance(ctx, record, func(id string) error { return e.Runtime.Stop(ctx, record.DeploymentID, id, e.stopGrace()) }); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.AgentRuntimeUnavailable, err)
		}
		if err := e.setInstanceState(ctx, record.DeploymentID, "stopped", "stopped"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.RecordRuntimeActionStep(ctx, record.ID, "stopped"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.CompleteRuntimeAction(ctx, record.ID, "stopped"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
	case RuntimeActionRestart:
		if !record.DeploymentActive {
			return e.fail(ctx, out, record.ID, failurecodes.ResourceConflict,
				errors.New("restart requires the active deployment"))
		}
		if record.Step == "" {
			if err := e.forEachInstance(ctx, record, func(id string) error { return e.Runtime.Stop(ctx, record.DeploymentID, id, e.stopGrace()) }); err != nil {
				return e.fail(ctx, out, record.ID, failurecodes.AgentRuntimeUnavailable, err)
			}
			if err := e.setInstanceState(ctx, record.DeploymentID, "stopped", "stopped"); err != nil {
				return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
			}
			if err := e.Store.RecordRuntimeActionStep(ctx, record.ID, "stopped"); err != nil {
				return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
			}
		}
		if err := e.forEachInstance(ctx, record, func(id string) error { return e.Runtime.Start(ctx, record.DeploymentID, id) }); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.AgentRuntimeUnavailable, err)
		}
		if err := e.setInstanceState(ctx, record.DeploymentID, "running", "starting"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.RecordRuntimeActionStep(ctx, record.ID, "started"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.CompleteRuntimeAction(ctx, record.ID, "running"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
	case RuntimeActionRemove:
		if record.DeploymentActive {
			return e.fail(ctx, out, record.ID, failurecodes.ResourceConflict,
				errors.New("remove refuses an active deployment"))
		}
		record.InstanceID = ""
		if err := e.withdraw(ctx, record); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.ReconcileStepFailed, err)
		}
		if err := e.forEachInstance(ctx, record, func(id string) error { return e.Runtime.Remove(ctx, record.DeploymentID, id) }); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.AgentRuntimeUnavailable, err)
		}
		if err := e.setInstanceState(ctx, record.DeploymentID, "removed", "removed"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.RecordRuntimeActionStep(ctx, record.ID, "removed"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
		if err := e.Store.CompleteRuntimeAction(ctx, record.ID, "removed"); err != nil {
			return e.fail(ctx, out, record.ID, failurecodes.InternalError, err)
		}
	}

	if _, err := e.Operations.Transition(ctx, orailtypes.ID(payload.OperationID), orailtypes.OperationSucceeded); err != nil {
		return e.fail(ctx, out, record.ID, failurecodes.OperationInvalidTransition, err)
	}
	out.Succeeded = true
	return out, nil
}

func (e ActionExecutor) setInstanceState(ctx context.Context, deploymentID, desired, observed string) error {
	if e.Instances == nil {
		return nil
	}
	return e.Instances.SetDeploymentState(ctx, deploymentID, desired, observed)
}

func (e ActionExecutor) forEachInstance(ctx context.Context, record RuntimeActionRecord, apply func(string) error) error {
	ids := []string{record.InstanceID}
	if lister, ok := e.Runtime.(deploymentInstanceRuntime); ok {
		values, err := lister.DeploymentInstanceIDs(ctx, record.DeploymentID)
		if err != nil {
			return err
		}
		if len(values) > 0 {
			ids = values
		}
	}
	for _, id := range ids {
		if err := apply(id); err != nil {
			return err
		}
	}
	return nil
}

func (e ActionExecutor) withdraw(ctx context.Context, record RuntimeActionRecord) error {
	if record.Step == "route_withdrawn" || record.Step == "stopped" || record.Step == "removed" {
		return nil
	}
	if e.Routes != nil {
		if err := e.Routes.WithdrawDeployment(ctx, record); err != nil {
			return fmt.Errorf("withdraw deployment routes: %w", err)
		}
	}
	return e.Store.RecordRuntimeActionStep(ctx, record.ID, "route_withdrawn")
}

func (e ActionExecutor) stopGrace() time.Duration {
	if e.StopGrace > 0 {
		return e.StopGrace
	}
	return 30 * time.Second
}

func (e ActionExecutor) fail(ctx context.Context, out ExecutionOutcome, actionID string, code failurecodes.Code, cause error) (ExecutionOutcome, error) {
	out.FailureCode, out.FailureReason = code, cause.Error()
	_ = e.Store.FailRuntimeAction(ctx, actionID, code, cause.Error())
	_, _ = e.Operations.Fail(ctx, orailtypes.ID(out.OperationID), orailtypes.FailureCode(code), cause.Error())
	return out, cause
}
