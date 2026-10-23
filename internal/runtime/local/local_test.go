package local

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
)

func TestAdapterScheduleIsIdempotentByKey(t *testing.T) {
	adapter := New()
	ctx := context.Background()
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_a",
		InstanceID:     "inst_a",
		IdempotencyKey: "key_1",
	}
	first, err := adapter.Schedule(ctx, req)
	if err != nil {
		t.Fatalf("Schedule first: %v", err)
	}
	second, err := adapter.Schedule(ctx, req)
	if err != nil {
		t.Fatalf("Schedule second: %v", err)
	}
	if first.ObservedAt != second.ObservedAt {
		t.Fatalf("expected idempotent schedule to keep ObservedAt; got %v vs %v", first, second)
	}
	if adapter.IntentCount(IntentSchedule) != 2 {
		t.Fatalf("expected two schedule intents recorded; got %d", adapter.IntentCount(IntentSchedule))
	}
}

func TestAdapterStartStopRemoveTransitions(t *testing.T) {
	adapter := New()
	ctx := context.Background()
	if _, err := adapter.Schedule(ctx, runtime.ScheduleRequest{DeploymentID: "d", InstanceID: "i"}); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if err := adapter.Start(ctx, "d", "i"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	statuses, err := adapter.StreamInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("StreamInstanceStatus: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != runtime.InstanceStateRunning {
		t.Fatalf("expected running status, got %+v", statuses)
	}
	if err := adapter.Stop(ctx, "d", "i", time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := adapter.Remove(ctx, "d", "i"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	statuses, err = adapter.StreamInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("StreamInstanceStatus after remove: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("expected no statuses after remove, got %+v", statuses)
	}
}

func TestAdapterApplyNetworkPolicyRejectsCrossTenant(t *testing.T) {
	adapter := New()
	ctx := context.Background()
	policy := runtime.NetworkPolicy{ProjectID: "p1", EnvironmentID: "e1", BridgeName: "br-1"}
	if err := adapter.ApplyNetworkPolicy(ctx, policy); err != nil {
		t.Fatalf("ApplyNetworkPolicy first: %v", err)
	}
	conflict := policy
	conflict.ProjectID = "p2"
	if err := adapter.ApplyNetworkPolicy(ctx, conflict); err == nil {
		t.Fatal("expected cross-tenant conflict error")
	}
}

func TestAdapterPullVerifyRecordsReason(t *testing.T) {
	adapter := New()
	ctx := context.Background()
	result, err := adapter.PullVerify(ctx, "registry.example.com/foo@sha256:abc")
	if err != nil {
		t.Fatalf("PullVerify: %v", err)
	}
	if result.Verified || result.Reason == "" {
		t.Fatalf("expected unverified pull with reason; got %+v", result)
	}
}

func TestAdapterResolveRoutingTargetCarriesHostAndTargetIdentity(t *testing.T) {
	adapter := New()
	ctx := context.Background()
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep-a",
		InstanceID:     "dep-a-r0",
		ProjectID:      "prj-a",
		EnvironmentID:  "env-a",
		IdempotencyKey: "key-routing",
		Labels: map[string]string{
			"midnight.host": "host-a",
		},
	}
	if _, err := adapter.Schedule(ctx, req); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	adapter.MarkReady(req.DeploymentID, req.InstanceID)
	target, err := adapter.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		t.Fatalf("ResolveRoutingTarget: %v", err)
	}
	if target.HostID != "host-a" || target.RoutingTargetID != "dep-a/dep-a-r0" {
		t.Fatalf("target identity = host:%q target:%q", target.HostID, target.RoutingTargetID)
	}
	if target.ProjectID != req.ProjectID || target.EnvironmentID != req.EnvironmentID {
		t.Fatalf("target scope = project:%q env:%q", target.ProjectID, target.EnvironmentID)
	}
	if !target.Ready {
		t.Fatalf("target Ready = false")
	}
}
