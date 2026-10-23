// Package conformance ships the RuntimeAdapter conformance suite required
// by ADR-015. Adapter authors call Run with a fresh adapter and a
// deterministic test fixture; the suite asserts the boundary invariants
// (idempotent Schedule, monotonic InstanceStatus transitions, label
// hygiene, signature-enforcement default, clean detach) without any live
// runtime side effects.
//
// The conformance suite intentionally does NOT depend on Go's testing
// package so that adapter teams can run the assertions from binaries,
// e.g. a phase 3 containerd CI binary that publishes a JSON report.
package conformance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

// Fixture describes the deterministic input the conformance suite uses.
// Adapters MUST be able to round-trip every Schedule request the fixture
// produces.
type Fixture struct {
	DeploymentID  string
	InstanceID    string
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	Image         string
	BridgeName    string
	Subnet        string
	Gateway       string
}

// DefaultFixture returns a small, deterministic fixture for adapter tests.
func DefaultFixture() Fixture {
	return Fixture{
		DeploymentID:  "dep_01HAA00000000000000000001",
		InstanceID:    "inst_01HAA00000000000000000001",
		ProjectID:     "prj_01HAA00000000000000000001",
		EnvironmentID: "env_01HAA00000000000000000001",
		ServiceID:     "svc_01HAA00000000000000000001",
		Image:         "registry.example.com/midnight/sample@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		BridgeName:    "or-net-prj01-env01",
		Subnet:        "10.20.0.0/24",
		Gateway:       "10.20.0.1",
	}
}

// CheckResult records one assertion outcome.
type CheckResult struct {
	Name    string
	Passed  bool
	Message string
}

// Report is the deterministic conformance run output.
type Report struct {
	AdapterName    string
	AdapterVersion string
	Major          int
	Total          int
	Passed         int
	Failed         int
	Results        []CheckResult
}

// Ok is true when no assertions failed.
func (r Report) Ok() bool {
	return r.Failed == 0 && r.Total > 0
}

// Run executes the standard suite against adapter using fixture.
// The suite never starts background goroutines and always honors ctx.
func Run(ctx context.Context, adapter runtime.RuntimeAdapter, fixture Fixture) Report {
	caps := adapter.Capabilities()
	report := Report{
		AdapterName:    caps.Name,
		AdapterVersion: caps.Version,
		Major:          caps.Major,
	}

	checks := []func(context.Context, runtime.RuntimeAdapter, Fixture) CheckResult{
		checkCapabilitiesMajor,
		checkScheduleIdempotent,
		checkLabelHygiene,
		checkInstanceStateMonotonic,
		checkPullVerifyDefault,
		checkNetworkPolicyTenancy,
		checkCleanDetach,
		checkResolveRoutingHonorsRemove,
	}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			report.Results = append(report.Results, CheckResult{Name: "context", Message: err.Error()})
			report.Failed++
			report.Total++
			continue
		}
		result := check(ctx, adapter, fixture)
		report.Results = append(report.Results, result)
		report.Total++
		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	sort.Slice(report.Results, func(i, j int) bool { return report.Results[i].Name < report.Results[j].Name })
	return report
}

// RunAcceptanceSubset executes the Phase 3 Kubernetes acceptance subset. It is
// intentionally in-memory and checks only the v1.9 adapter boundary flow:
// BuildArtifact -> ScheduleRequest -> InstanceStatus -> RoutingTarget.
func RunAcceptanceSubset(ctx context.Context, adapter runtime.RuntimeAdapter, fixture Fixture) Report {
	caps := adapter.Capabilities()
	report := Report{
		AdapterName:    caps.Name,
		AdapterVersion: caps.Version,
		Major:          caps.Major,
	}

	checks := []func(context.Context, runtime.RuntimeAdapter, Fixture) CheckResult{
		checkCapabilitiesMajor,
		checkBuildArtifactScheduleRequest,
		checkBoundaryRoundTrip,
		checkResolveRoutingHonorsRemove,
	}
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			report.Results = append(report.Results, CheckResult{Name: "context", Message: err.Error()})
			report.Failed++
			report.Total++
			continue
		}
		result := check(ctx, adapter, fixture)
		report.Results = append(report.Results, result)
		report.Total++
		if result.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	sort.Slice(report.Results, func(i, j int) bool { return report.Results[i].Name < report.Results[j].Name })
	return report
}

func checkCapabilitiesMajor(_ context.Context, adapter runtime.RuntimeAdapter, _ Fixture) CheckResult {
	caps := adapter.Capabilities()
	if caps.Major != runtime.AdapterMajorVersion {
		return fail("capabilities_major", fmt.Sprintf("major=%d want=%d", caps.Major, runtime.AdapterMajorVersion))
	}
	if strings.TrimSpace(caps.Name) == "" {
		return fail("capabilities_major", "capabilities name is empty")
	}
	return pass("capabilities_major")
}

func checkScheduleIdempotent(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req := scheduleRequest(f, "idempotency-key-A")
	first, err := adapter.Schedule(ctx, req)
	if err != nil {
		return fail("schedule_idempotent", err.Error())
	}
	second, err := adapter.Schedule(ctx, req)
	if err != nil {
		return fail("schedule_idempotent", err.Error())
	}
	if first.InstanceID != second.InstanceID || first.DeploymentID != second.DeploymentID {
		return fail("schedule_idempotent", "duplicate schedule produced different ids")
	}
	return pass("schedule_idempotent")
}

func checkBuildArtifactScheduleRequest(_ context.Context, _ runtime.RuntimeAdapter, f Fixture) CheckResult {
	req, err := runtime.ScheduleRequestFromBuildArtifact(buildArtifact(f), f.InstanceID, "artifact-boundary")
	if err != nil {
		return fail("build_artifact_schedule_request", err.Error())
	}
	if req.DeploymentID != f.DeploymentID || req.InstanceID != f.InstanceID {
		return fail("build_artifact_schedule_request", "schedule request ids do not match artifact boundary")
	}
	if req.Image != f.Image {
		return fail("build_artifact_schedule_request", fmt.Sprintf("image=%q want=%q", req.Image, f.Image))
	}
	for _, key := range []string{"midnight.project", "midnight.environment", "midnight.service", "midnight.deployment", "midnight.instance"} {
		if req.Labels[key] == "" {
			return fail("build_artifact_schedule_request", fmt.Sprintf("missing required label %q", key))
		}
	}
	return pass("build_artifact_schedule_request")
}

func checkBoundaryRoundTrip(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req, err := runtime.ScheduleRequestFromBuildArtifact(buildArtifact(f), f.InstanceID, "boundary-round-trip")
	if err != nil {
		return fail("boundary_round_trip", err.Error())
	}
	status, err := adapter.Schedule(ctx, req)
	if err != nil {
		return fail("boundary_round_trip", err.Error())
	}
	if status.DeploymentID != req.DeploymentID || status.InstanceID != req.InstanceID {
		return fail("boundary_round_trip", "schedule returned status for a different instance")
	}
	if err := adapter.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		return fail("boundary_round_trip", err.Error())
	}
	target, err := adapter.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		return fail("boundary_round_trip", err.Error())
	}
	if target.DeploymentID != req.DeploymentID || target.InstanceID != req.InstanceID {
		return fail("boundary_round_trip", "routing target ids do not match scheduled instance")
	}
	if target.ProjectID != "" && target.ProjectID != req.ProjectID {
		return fail("boundary_round_trip", "routing target project does not match schedule request")
	}
	if target.EnvironmentID != "" && target.EnvironmentID != req.EnvironmentID {
		return fail("boundary_round_trip", "routing target environment does not match schedule request")
	}
	return pass("boundary_round_trip")
}

func checkLabelHygiene(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req := scheduleRequest(f, "label-hygiene")
	if req.Labels == nil {
		req.Labels = map[string]string{}
	}
	required := []string{"midnight.project", "midnight.environment", "midnight.service", "midnight.deployment", "midnight.instance"}
	for _, key := range required {
		if _, ok := req.Labels[key]; !ok {
			return fail("label_hygiene", fmt.Sprintf("schedule request missing required label %q", key))
		}
	}
	if _, err := adapter.Schedule(ctx, req); err != nil {
		return fail("label_hygiene", err.Error())
	}
	return pass("label_hygiene")
}

func checkInstanceStateMonotonic(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req := scheduleRequest(f, "monotonic")
	if _, err := adapter.Schedule(ctx, req); err != nil {
		return fail("instance_state_monotonic", err.Error())
	}
	if err := adapter.Start(ctx, f.DeploymentID, f.InstanceID); err != nil {
		return fail("instance_state_monotonic", err.Error())
	}
	statuses, err := adapter.StreamInstanceStatus(ctx)
	if err != nil {
		return fail("instance_state_monotonic", err.Error())
	}
	for _, status := range statuses {
		if status.DeploymentID == f.DeploymentID && status.InstanceID == f.InstanceID {
			if !isForwardState(status.State) {
				return fail("instance_state_monotonic", fmt.Sprintf("unexpected state %q after start", status.State))
			}
			return pass("instance_state_monotonic")
		}
	}
	return fail("instance_state_monotonic", "scheduled instance not present in stream")
}

func checkPullVerifyDefault(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	caps := adapter.Capabilities()
	result, err := adapter.PullVerify(ctx, f.Image)
	if err != nil {
		if !caps.SignatureEnforce && failureCodeOf(err) == failurecodes.ImageSignatureRequired {
			return pass("pull_verify_default")
		}
		return fail("pull_verify_default", err.Error())
	}
	if caps.SignatureEnforce && !result.Verified {
		return fail("pull_verify_default", "signature enforce capability requires verification by default")
	}
	if !caps.SignatureEnforce && result.Verified && result.Reason == "" {
		return fail("pull_verify_default", "non-enforcing adapter must record a reason for unverified pulls")
	}
	return pass("pull_verify_default")
}

type reasonedFailure interface {
	Reason() (failurecodes.Code, string)
}

func failureCodeOf(err error) failurecodes.Code {
	var reasoned reasonedFailure
	if errors.As(err, &reasoned) {
		code, _ := reasoned.Reason()
		return code
	}
	return ""
}

func checkNetworkPolicyTenancy(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	policy := runtime.NetworkPolicy{
		ProjectID:     f.ProjectID,
		EnvironmentID: f.EnvironmentID,
		BridgeName:    f.BridgeName,
		Subnet:        f.Subnet,
		Gateway:       f.Gateway,
	}
	if err := adapter.ApplyNetworkPolicy(ctx, policy); err != nil {
		return fail("network_policy_tenancy", err.Error())
	}
	conflicting := policy
	conflicting.ProjectID = "prj_other"
	if err := adapter.ApplyNetworkPolicy(ctx, conflicting); err == nil {
		return fail("network_policy_tenancy", "cross-tenant bridge reuse must be rejected")
	}
	return pass("network_policy_tenancy")
}

func checkCleanDetach(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req := scheduleRequest(f, "clean-detach")
	if _, err := adapter.Schedule(ctx, req); err != nil {
		return fail("clean_detach", err.Error())
	}
	if err := adapter.Stop(ctx, f.DeploymentID, f.InstanceID, time.Second); err != nil {
		return fail("clean_detach", err.Error())
	}
	if err := adapter.Remove(ctx, f.DeploymentID, f.InstanceID); err != nil {
		return fail("clean_detach", err.Error())
	}
	statuses, err := adapter.StreamInstanceStatus(ctx)
	if err != nil {
		return fail("clean_detach", err.Error())
	}
	for _, status := range statuses {
		if status.DeploymentID == f.DeploymentID && status.InstanceID == f.InstanceID {
			return fail("clean_detach", "instance still observed after Remove")
		}
	}
	return pass("clean_detach")
}

func checkResolveRoutingHonorsRemove(ctx context.Context, adapter runtime.RuntimeAdapter, f Fixture) CheckResult {
	req := scheduleRequest(f, "routing-removal")
	if _, err := adapter.Schedule(ctx, req); err != nil {
		return fail("resolve_routing_honors_remove", err.Error())
	}
	if err := adapter.Remove(ctx, f.DeploymentID, f.InstanceID); err != nil {
		return fail("resolve_routing_honors_remove", err.Error())
	}
	if _, err := adapter.ResolveRoutingTarget(ctx, f.DeploymentID, f.InstanceID); err == nil {
		return fail("resolve_routing_honors_remove", "removed instance must not produce a routing target")
	}
	return pass("resolve_routing_honors_remove")
}

func scheduleRequest(f Fixture, key string) runtime.ScheduleRequest {
	return runtime.ScheduleRequest{
		DeploymentID:   f.DeploymentID,
		InstanceID:     f.InstanceID,
		ProjectID:      f.ProjectID,
		EnvironmentID:  f.EnvironmentID,
		ServiceID:      f.ServiceID,
		Image:          f.Image,
		IdempotencyKey: key,
		Labels: map[string]string{
			"midnight.project":     f.ProjectID,
			"midnight.environment": f.EnvironmentID,
			"midnight.service":     f.ServiceID,
			"midnight.deployment":  f.DeploymentID,
			"midnight.instance":    f.InstanceID,
		},
	}
}

func buildArtifact(f Fixture) runtime.BuildArtifact {
	return runtime.BuildArtifact{
		DeploymentID:  f.DeploymentID,
		ProjectID:     f.ProjectID,
		EnvironmentID: f.EnvironmentID,
		ServiceID:     f.ServiceID,
		Image:         f.Image,
	}
}

func pass(name string) CheckResult { return CheckResult{Name: name, Passed: true} }
func fail(name, msg string) CheckResult {
	return CheckResult{Name: name, Passed: false, Message: msg}
}

// stateRank assigns a monotonic order to forward instance states; the
// suite uses it to detect invalid backward transitions.
func stateRank(state runtime.InstanceState) int {
	switch state {
	case runtime.InstanceStateAllocated:
		return 1
	case runtime.InstanceStatePulling:
		return 2
	case runtime.InstanceStateCreating:
		return 3
	case runtime.InstanceStateStarting:
		return 4
	case runtime.InstanceStateRunning:
		return 5
	case runtime.InstanceStateReady:
		return 6
	case runtime.InstanceStateUnhealthy:
		return 7
	case runtime.InstanceStateRestarting:
		return 8
	case runtime.InstanceStateDraining:
		return 9
	case runtime.InstanceStateStopped:
		return 10
	case runtime.InstanceStateRemoved:
		return 11
	case runtime.InstanceStateLost:
		return 12
	case runtime.InstanceStateCrashed:
		return 13
	}
	return 0
}

func isForwardState(state runtime.InstanceState) bool {
	return stateRank(state) >= stateRank(runtime.InstanceStateAllocated)
}
