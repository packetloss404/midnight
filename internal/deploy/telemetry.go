package deploy

import (
	"context"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

type DeployStep string

const (
	DeployStepResolveVariables      DeployStep = "resolve-variables"
	DeployStepPredeploy             DeployStep = "predeploy"
	DeployStepPullVerify            DeployStep = "pull-verify"
	DeployStepSchedule              DeployStep = "schedule"
	DeployStepStart                 DeployStep = "start"
	DeployStepReadinessRoutingCheck DeployStep = "readiness-routing-check"
	DeployStepTemplateSmoke         DeployStep = "template-smoke"
	DeployStepPromote               DeployStep = "promote"
	DeployStepDrainPrevious         DeployStep = "drain-previous"
	DeployStepSucceeded             DeployStep = "succeeded"
	DeployStepFailed                DeployStep = "failed"
	DeployStepCanceled              DeployStep = "canceled"
)

type DeployStepState string

const (
	DeployStepStateStarted   DeployStepState = "started"
	DeployStepStateSucceeded DeployStepState = "succeeded"
	DeployStepStateFailed    DeployStepState = "failed"
)

// DeployStepObserver receives best-effort executor progress events.
// Implementations should not mutate the event after receiving it.
type DeployStepObserver interface {
	ObserveDeployStep(context.Context, DeployStepEvent)
}

type DeployStepObserverFunc func(context.Context, DeployStepEvent)

func (f DeployStepObserverFunc) ObserveDeployStep(ctx context.Context, event DeployStepEvent) {
	if f != nil {
		f(ctx, event)
	}
}

type DeployStepEvent struct {
	OperationID   string            `json:"operation_id,omitempty"`
	JobID         string            `json:"job_id,omitempty"`
	DeploymentID  string            `json:"deployment_id,omitempty"`
	InstanceID    string            `json:"instance_id,omitempty"`
	Step          DeployStep        `json:"step"`
	State         DeployStepState   `json:"state"`
	FailureCode   failurecodes.Code `json:"failure_code,omitempty"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Route         *DeployRouteEvent `json:"route,omitempty"`
	// Variables carries the redaction-safe snapshot of the resolved
	// environment-variable map (names + length buckets only, never
	// plaintext) emitted by the resolve-variables step.
	Variables *DeployVariablesSnapshot `json:"variables,omitempty"`
}

// DeployVariablesSnapshot is a redaction-safe description of the
// resolved environment-variable map. The Items field is sorted by name
// for deterministic event ordering.
type DeployVariablesSnapshot struct {
	Count int                       `json:"count"`
	Items []DeployVariableSnapshotI `json:"items"`
}

// DeployVariableSnapshotI is one entry in a DeployVariablesSnapshot.
type DeployVariableSnapshotI struct {
	Name         string `json:"name"`
	LengthBucket string `json:"length_bucket"`
}

type DeployRouteEvent struct {
	RouteGroup      string `json:"route_group,omitempty"`
	RouteName       string `json:"route_name,omitempty"`
	ServiceID       string `json:"service_id,omitempty"`
	Host            string `json:"host,omitempty"`
	PathPrefix      string `json:"path_prefix,omitempty"`
	TLS             bool   `json:"tls,omitempty"`
	HostID          string `json:"host_id,omitempty"`
	RoutingTargetID string `json:"routing_target_id,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
	ContainerIP     string `json:"container_ip,omitempty"`
	InternalPort    int    `json:"internal_port,omitempty"`
	Ready           bool   `json:"ready,omitempty"`
}

func (e Executor) observeDeployStep(ctx context.Context, event DeployStepEvent) {
	if e.Observer == nil {
		return
	}
	e.Observer.ObserveDeployStep(ctx, event)
}

func routeDeployEvent(metadata RoutePromotionMetadata, target runtime.RoutingTarget) *DeployRouteEvent {
	return &DeployRouteEvent{
		RouteGroup:      metadata.RouteGroup,
		RouteName:       metadata.RouteName,
		ServiceID:       metadata.ServiceID,
		Host:            metadata.Host,
		PathPrefix:      metadata.PathPrefix,
		TLS:             metadata.TLS,
		HostID:          target.HostID,
		RoutingTargetID: target.RoutingTargetID,
		ContainerID:     target.ContainerID,
		ContainerIP:     target.ContainerIP,
		InternalPort:    target.InternalPort,
		Ready:           target.Ready,
	}
}
