package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
)

type ActivationReadinessRoutingReasonCode string

const (
	ActivationReadinessRoutingReady                 ActivationReadinessRoutingReasonCode = "ready"
	ActivationReadinessRoutingCheckError            ActivationReadinessRoutingReasonCode = "check-error"
	ActivationReadinessRoutingRuntimeStatusMissing  ActivationReadinessRoutingReasonCode = "runtime-status-missing"
	ActivationReadinessRoutingRuntimeStatusMismatch ActivationReadinessRoutingReasonCode = "runtime-status-mismatch"
	ActivationReadinessRoutingRuntimeNotReady       ActivationReadinessRoutingReasonCode = "runtime-not-ready"
	ActivationReadinessRoutingTargetMissing         ActivationReadinessRoutingReasonCode = "routing-target-missing"
	ActivationReadinessRoutingTargetMismatch        ActivationReadinessRoutingReasonCode = "routing-target-mismatch"
	ActivationReadinessRoutingHostMismatch          ActivationReadinessRoutingReasonCode = "routing-target-host-mismatch"
	ActivationReadinessRoutingTargetNotReady        ActivationReadinessRoutingReasonCode = "routing-target-not-ready"
	ActivationReadinessRoutingRoutePending          ActivationReadinessRoutingReasonCode = "route-pending"
)

// ActivationReadinessRoutingChecker is the small execution boundary a deploy
// worker needs before promotion. Tests can fake it; live adapters can compose
// RuntimeAdapter.ResolveRoutingTarget with router reconciliation evidence.
type ActivationReadinessRoutingChecker interface {
	CheckActivationReadinessRouting(context.Context, ActivationReadinessRoutingRequest) (ActivationReadinessRoutingCheck, error)
}

type ActivationReadinessRoutingRequest struct {
	DeploymentID    string
	InstanceID      string
	HostID          string
	RoutingTargetID string
	RouteGroup      string
}

type ActivationReadinessRoutingEvidence struct {
	Candidate               runtime.ScheduleRequest
	ExpectedHostID          string
	ExpectedRoutingTargetID string
	InstanceStatus          runtime.InstanceStatus
	RoutingTarget           runtime.RoutingTarget
	RouteSummary            router.RouteReconciliationSummary
}

type ActivationReadinessRoutingCheck struct {
	Evaluated             bool
	CandidateDeploymentID string
	CandidateInstanceID   string
	HostID                string
	RoutingTargetID       string
	ObservedHostID        string
	ObservedRoutingTarget string
	RuntimeReady          bool
	RoutingReady          bool
	RouteReady            bool
	ReadyToPromote        bool
	ReasonCode            ActivationReadinessRoutingReasonCode
	Reason                string
	Error                 string
}

func EvaluateActivationReadinessRouting(e ActivationReadinessRoutingEvidence) ActivationReadinessRoutingCheck {
	deploymentID := strings.TrimSpace(e.Candidate.DeploymentID)
	instanceID := strings.TrimSpace(e.Candidate.InstanceID)
	hostID := firstNonEmpty(e.ExpectedHostID, e.Candidate.Labels["midnight.host"], e.RoutingTarget.HostID)
	routingTargetID := firstNonEmpty(e.ExpectedRoutingTargetID, e.RoutingTarget.RoutingTargetID)

	check := ActivationReadinessRoutingCheck{
		Evaluated:             true,
		CandidateDeploymentID: deploymentID,
		CandidateInstanceID:   instanceID,
		HostID:                hostID,
		RoutingTargetID:       routingTargetID,
		ObservedHostID:        strings.TrimSpace(e.RoutingTarget.HostID),
		ObservedRoutingTarget: strings.TrimSpace(e.RoutingTarget.RoutingTargetID),
		RouteReady:            true,
	}

	if strings.TrimSpace(e.InstanceStatus.DeploymentID) == "" || strings.TrimSpace(e.InstanceStatus.InstanceID) == "" {
		return check.block(ActivationReadinessRoutingRuntimeStatusMissing, "runtime status is missing")
	}
	if e.InstanceStatus.DeploymentID != deploymentID || e.InstanceStatus.InstanceID != instanceID {
		return check.block(ActivationReadinessRoutingRuntimeStatusMismatch, "runtime status belongs to a different candidate")
	}
	if e.InstanceStatus.State != runtime.InstanceStateReady {
		return check.block(ActivationReadinessRoutingRuntimeNotReady, fmt.Sprintf("runtime status is %s", e.InstanceStatus.State))
	}
	check.RuntimeReady = true

	if strings.TrimSpace(e.RoutingTarget.DeploymentID) == "" || strings.TrimSpace(e.RoutingTarget.InstanceID) == "" {
		return check.block(ActivationReadinessRoutingTargetMissing, "routing target is missing")
	}
	if e.RoutingTarget.DeploymentID != deploymentID || e.RoutingTarget.InstanceID != instanceID {
		return check.block(ActivationReadinessRoutingTargetMismatch, "routing target belongs to a different candidate")
	}
	if hostID != "" && e.RoutingTarget.HostID != "" && e.RoutingTarget.HostID != hostID {
		return check.block(ActivationReadinessRoutingHostMismatch, "routing target belongs to a different host")
	}
	if routingTargetID != "" && e.RoutingTarget.RoutingTargetID != "" && e.RoutingTarget.RoutingTargetID != routingTargetID {
		return check.block(ActivationReadinessRoutingTargetMismatch, "routing target id does not match expected target")
	}
	if !e.RoutingTarget.Ready {
		return check.block(ActivationReadinessRoutingTargetNotReady, "routing target is not ready")
	}
	check.RoutingReady = true

	if hasRouteSummary(e.RouteSummary) && !routeSummaryReady(e.RouteSummary) {
		check.RouteReady = false
		return check.block(ActivationReadinessRoutingRoutePending, e.RouteSummary.CompactString())
	}
	check.ReadyToPromote = true
	check.ReasonCode = ActivationReadinessRoutingReady
	check.Reason = "candidate runtime and routing checks are ready"
	return check
}

func FailedActivationReadinessRoutingCheck(req ActivationReadinessRoutingRequest, err error) ActivationReadinessRoutingCheck {
	reason := "readiness/routing check failed"
	if err != nil {
		reason = err.Error()
	}
	return ActivationReadinessRoutingCheck{
		Evaluated:             true,
		CandidateDeploymentID: strings.TrimSpace(req.DeploymentID),
		CandidateInstanceID:   strings.TrimSpace(req.InstanceID),
		HostID:                strings.TrimSpace(req.HostID),
		RoutingTargetID:       strings.TrimSpace(req.RoutingTargetID),
		RouteReady:            false,
		ReasonCode:            ActivationReadinessRoutingCheckError,
		Reason:                reason,
		Error:                 reason,
	}
}

func (check ActivationReadinessRoutingCheck) BlockReason() string {
	if check.Reason == "" {
		return string(check.ReasonCode)
	}
	if check.ReasonCode == "" {
		return check.Reason
	}
	return string(check.ReasonCode) + ": " + check.Reason
}

func (check ActivationReadinessRoutingCheck) block(code ActivationReadinessRoutingReasonCode, reason string) ActivationReadinessRoutingCheck {
	check.ReadyToPromote = false
	check.ReasonCode = code
	check.Reason = reason
	return check
}

func hasRouteSummary(summary router.RouteReconciliationSummary) bool {
	return summary.RouteGroup != "" ||
		summary.ArtifactHash != "" ||
		summary.Status != "" ||
		summary.DesiredRoutes > 0 ||
		summary.RenderedRoutes > 0 ||
		summary.ActiveRoutes > 0 ||
		summary.PendingRoutes > 0 ||
		summary.StaleRoutes > 0 ||
		summary.UnknownRoutes > 0 ||
		summary.Mismatches > 0
}

func routeSummaryReady(summary router.RouteReconciliationSummary) bool {
	if summary.Status != router.RouteReconciliationStatusReady {
		return false
	}
	if summary.PendingRoutes > 0 || summary.StaleRoutes > 0 || summary.UnknownRoutes > 0 || summary.Mismatches > 0 {
		return false
	}
	return summary.DesiredRoutes == summary.ActiveRoutes
}
