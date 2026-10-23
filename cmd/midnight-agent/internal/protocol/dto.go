// Package protocol contains agent control-plane DTOs and request builders.
// The wire shapes are defined in pkg/agentapi (the only Go-source seam
// between the agent and control plane); this package re-exports them so
// existing call sites under cmd/midnight-agent keep working without
// pkg/agentapi imports leaking into every file.
package protocol

import (
	"time"

	"github.com/midnight/midnight/pkg/agentapi"
)

// RegistrationRequest is sent when an agent first announces itself.
type RegistrationRequest = agentapi.RegistrationRequest

// RegistrationResponse contains control-plane registration metadata.
type RegistrationResponse = agentapi.RegistrationResponse

// HeartbeatRequest reports agent liveness and local runtime status.
type HeartbeatRequest = agentapi.HeartbeatRequest

// HeartbeatResponse acknowledges the latest accepted heartbeat sequence.
type HeartbeatResponse = agentapi.HeartbeatResponse

// DesiredStateResponse is the Phase 1 placeholder for desired-state polling.
type DesiredStateResponse = agentapi.DesiredStateResponse

// InstanceStatus mirrors the runtime.InstanceStatus shape.
type InstanceStatus = agentapi.InstanceStatus

// RouteTargetObservation is the agent-owned ingress target discovery shape.
type RouteTargetObservation = agentapi.RouteTargetObservation

// Drift carries the agent's daemon.json baseline + cgroup mode summary.
type Drift = agentapi.Drift

// StatusReport is the agent → control-plane observed-state batch.
type StatusReport = agentapi.StatusReport

// StatusReportResponse is the control plane's acknowledgement.
type StatusReportResponse = agentapi.StatusReportResponse

// RuntimeObservation is the legacy agent → server observation envelope used
// by the existing supervisor / decision-artifact pipeline. The Phase 1
// status loop uses StatusReport above; this type is preserved so the
// existing supervisor tests keep passing.
type RuntimeObservation struct {
	AgentID      string                   `json:"agent_id"`
	HostID       string                   `json:"host_id"`
	Runtime      string                   `json:"runtime"`
	ObservedAt   time.Time                `json:"observed_at"`
	RouteTargets []RouteTargetObservation `json:"route_targets,omitempty"`
}
