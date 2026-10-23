package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/network"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/conformance"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
)

func newTestAdapter(t *testing.T) (*Adapter, *fakeEngine) {
	t.Helper()
	return newTestAdapterWithHost(t, "host-test")
}

func newTestAdapterWithHost(t *testing.T, hostID string) (*Adapter, *fakeEngine) {
	t.Helper()
	fake := newFakeEngine()
	a, _, err := New(context.Background(), Config{
		AgentID:    "agent-test",
		HostID:     hostID,
		Engine:     fake,
		CGroupMode: CGroupModeV2,
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return a, fake
}

type healthInspectEngine struct {
	*fakeEngine
	healthByRef map[string]*container.Health
}

func wrapFakeInspectHealth(a *Adapter, fake *fakeEngine) *healthInspectEngine {
	engine := &healthInspectEngine{
		fakeEngine:  fake,
		healthByRef: make(map[string]*container.Health),
	}
	a.client = engine
	return engine
}

func (h *healthInspectEngine) ContainerInspect(ctx context.Context, ref string) (container.InspectResponse, error) {
	resp, err := h.fakeEngine.ContainerInspect(ctx, ref)
	if err != nil || resp.State == nil {
		return resp, err
	}
	if health, ok := h.healthByRef[ref]; ok {
		resp.State.Health = health
		return resp, nil
	}
	if health, ok := h.healthByRef[resp.ID]; ok {
		resp.State.Health = health
	}
	return resp, nil
}

func setFakeContainerHealth(t *testing.T, engine *healthInspectEngine, fake *fakeEngine, req runtime.ScheduleRequest, health *container.Health) {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	c := fake.containers[canonicalContainerName(req)]
	if c == nil {
		t.Fatalf("fake container %q not found", canonicalContainerName(req))
	}
	engine.healthByRef[c.id] = health
	engine.healthByRef[c.name] = health
}

func TestAdapterRefusesCGroupV1(t *testing.T) {
	fake := newFakeEngine()
	_, _, err := New(context.Background(), Config{
		Engine:     fake,
		CGroupMode: CGroupModeV1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.CGroupV2Required {
		t.Fatalf("expected CGROUP_V2_REQUIRED, got %s", CodeOf(err))
	}
}

func TestAdapterUsesEngineCGroupVersionWhenNoOverrideIsSet(t *testing.T) {
	fake := newFakeEngine()
	fake.info.CgroupVersion = "2"
	_, _, err := New(context.Background(), Config{
		Engine: fake,
	})
	if err != nil {
		t.Fatalf("New() error = %v, want engine-reported cgroup v2 to pass", err)
	}
}

func TestAdapterRefusesEngineReportedCGroupV1(t *testing.T) {
	fake := newFakeEngine()
	fake.info.CgroupVersion = "1"
	_, _, err := New(context.Background(), Config{
		Engine: fake,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.CGroupV2Required {
		t.Fatalf("expected CGROUP_V2_REQUIRED, got %s", CodeOf(err))
	}
}

func TestAdapterRefusesUnsupportedEngineAPI(t *testing.T) {
	fake := newFakeEngine()
	fake.apiVersion = "1.40"
	_, _, err := New(context.Background(), Config{
		Engine:     fake,
		CGroupMode: CGroupModeV2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.DockerEngineUnsupported {
		t.Fatalf("expected DOCKER_ENGINE_UNSUPPORTED, got %s", CodeOf(err))
	}
}

func TestAdapterRefusesDaemonBaselineConflict(t *testing.T) {
	fake := newFakeEngine()
	fake.info.Driver = "devicemapper"
	_, _, err := New(context.Background(), Config{
		Engine:     fake,
		CGroupMode: CGroupModeV2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.DockerDaemonBaselineConflict {
		t.Fatalf("expected DOCKER_DAEMON_BASELINE_CONFLICT, got %s", CodeOf(err))
	}
}

func TestAdapterRefusesPingFailure(t *testing.T) {
	fake := newFakeEngine()
	fake.pingErr = errors.New("connection refused")
	_, _, err := New(context.Background(), Config{
		Engine:     fake,
		CGroupMode: CGroupModeV2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.AgentRuntimeUnavailable {
		t.Fatalf("expected AGENT_RUNTIME_UNAVAILABLE, got %s", CodeOf(err))
	}
}

func TestScheduleStartStopRemove(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_01",
		InstanceID:     "inst_01",
		ProjectID:      "prj_01",
		EnvironmentID:  "env_01",
		ServiceID:      "svc_01",
		Image:          "registry.example.com/foo:bar",
		IdempotencyKey: "abc",
		Labels: map[string]string{
			"midnight.project_id":     "prj_01",
			"midnight.environment_id": "env_01",
			"midnight.service_id":     "svc_01",
			"midnight.deployment_id":  "dep_01",
			"midnight.instance_id":    "inst_01",
		},
	}
	status, err := a.Schedule(ctx, req)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if status.State != runtime.InstanceStateAllocated {
		t.Fatalf("expected ALLOCATED, got %s", status.State)
	}
	if fake.createCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", fake.createCalls)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if fake.startCalls != 1 {
		t.Fatalf("expected 1 start call, got %d", fake.startCalls)
	}
	statuses, err := a.StreamInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != runtime.InstanceStateRunning {
		t.Fatalf("expected one running instance, got %+v", statuses)
	}
	if err := a.Stop(ctx, req.DeploymentID, req.InstanceID, time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := a.Remove(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fake.removeCalls != 1 {
		t.Fatalf("expected 1 remove call, got %d", fake.removeCalls)
	}
	statuses, _ = a.StreamInstanceStatus(ctx)
	if len(statuses) != 0 {
		t.Fatalf("expected zero instances after Remove, got %+v", statuses)
	}
}

func TestResolveRoutingTargetProvidesServiceURLIngredients(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapterWithHost(t, "host-a")
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_route",
		InstanceID:     "inst_route",
		ProjectID:      "prj_route",
		EnvironmentID:  "env_route",
		ServiceID:      "svc_route",
		Image:          "registry.example.com/web@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		IdempotencyKey: "route-key",
		Labels: map[string]string{
			"midnight.host":          "host-a",
			"midnight.internal_port": "3000",
		},
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}

	target, err := a.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		t.Fatalf("resolve routing target: %v", err)
	}

	if target.DeploymentID != req.DeploymentID || target.InstanceID != req.InstanceID {
		t.Fatalf("target instance = %s/%s, want %s/%s", target.DeploymentID, target.InstanceID, req.DeploymentID, req.InstanceID)
	}
	if target.HostID != "host-a" {
		t.Fatalf("HostID = %q, want host-a", target.HostID)
	}
	if target.ContainerID == "" || target.RoutingTargetID != target.ContainerID {
		t.Fatalf("target identity = %q container = %q, want matching non-empty ids", target.RoutingTargetID, target.ContainerID)
	}
	if target.ContainerIP != "10.0.0.1" || target.InternalPort != 3000 {
		t.Fatalf("service URL ingredients = %s:%d, want 10.0.0.1:3000", target.ContainerIP, target.InternalPort)
	}
	if got := fmt.Sprintf("http://%s:%d", target.ContainerIP, target.InternalPort); got != "http://10.0.0.1:3000" {
		t.Fatalf("service URL = %q", got)
	}
	if !target.Ready {
		t.Fatal("expected target ready after start")
	}
	if !target.ObservedAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("ObservedAt = %s, want fixed test clock", target.ObservedAt)
	}
	if target.ProjectID != req.ProjectID || target.EnvironmentID != req.EnvironmentID {
		t.Fatalf("tenant scope = %s/%s, want %s/%s", target.ProjectID, target.EnvironmentID, req.ProjectID, req.EnvironmentID)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	created := fake.containers[canonicalContainerName(req)]
	if created == nil || created.cfg == nil {
		t.Fatal("expected fake container config to be recorded")
	}
	if _, ok := created.cfg.ExposedPorts[dockernetwork.MustParsePort("3000/tcp")]; !ok {
		t.Fatalf("expected schedule to expose 3000/tcp, got %#v", created.cfg.ExposedPorts)
	}
	// PORT must still be present; MIDNIGHT_* built-ins are injected
	// before PORT and user env (none here), but PORT's contract is
	// unchanged.
	if !containsEnv(created.cfg.Env, "PORT=3000") {
		t.Fatalf("expected PORT=3000 in env, got %#v", created.cfg.Env)
	}
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func TestResolveRoutingTargetDefaultsInternalPort(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_default_port",
		InstanceID:    "inst_default_port",
		ProjectID:     "prj",
		EnvironmentID: "env",
		ServiceID:     "svc",
		Image:         "alpine:3",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}
	target, err := a.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		t.Fatalf("resolve routing target: %v", err)
	}
	if target.InternalPort != 8080 {
		t.Fatalf("InternalPort = %d, want 8080", target.InternalPort)
	}
}

func TestResolveRoutingTargetReadinessFromDockerHealth(t *testing.T) {
	tests := []struct {
		name           string
		health         *container.Health
		wantReady      bool
		wantState      runtime.InstanceState
		reasonContains string
	}{
		{
			name:           "healthy healthcheck",
			health:         &container.Health{Status: container.Healthy},
			wantReady:      true,
			wantState:      runtime.InstanceStateReady,
			reasonContains: "healthy",
		},
		{
			name:           "no healthcheck",
			health:         nil,
			wantReady:      true,
			wantState:      runtime.InstanceStateReady,
			reasonContains: "no healthcheck",
		},
		{
			name: "unhealthy healthcheck",
			health: &container.Health{
				Status:        container.Unhealthy,
				FailingStreak: 2,
				Log: []*container.HealthcheckResult{
					{ExitCode: 1, Output: "connection refused"},
				},
			},
			wantReady:      false,
			wantState:      runtime.InstanceStateUnhealthy,
			reasonContains: "connection refused",
		},
		{
			name:           "starting healthcheck",
			health:         &container.Health{Status: container.Starting},
			wantReady:      false,
			wantState:      runtime.InstanceStateStarting,
			reasonContains: "starting",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a, fake := newTestAdapter(t)
			inspect := wrapFakeInspectHealth(a, fake)
			req := runtime.ScheduleRequest{
				DeploymentID:  "dep_health",
				InstanceID:    "inst_health",
				ProjectID:     "prj",
				EnvironmentID: "env",
				ServiceID:     "svc",
				Image:         "alpine:3",
			}
			if _, err := a.Schedule(ctx, req); err != nil {
				t.Fatalf("schedule: %v", err)
			}
			if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
				t.Fatalf("start: %v", err)
			}
			setFakeContainerHealth(t, inspect, fake, req, tt.health)

			target, err := a.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
			if err != nil {
				t.Fatalf("resolve routing target: %v", err)
			}
			if target.Ready != tt.wantReady {
				t.Fatalf("Ready = %v, want %v", target.Ready, tt.wantReady)
			}

			statuses, err := a.StreamInstanceStatus(ctx)
			if err != nil {
				t.Fatalf("stream status: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("got %d statuses, want 1: %+v", len(statuses), statuses)
			}
			if statuses[0].State != tt.wantState {
				t.Fatalf("status state = %s, want %s", statuses[0].State, tt.wantState)
			}
			if !strings.Contains(statuses[0].Reason, tt.reasonContains) {
				t.Fatalf("status reason = %q, want containing %q", statuses[0].Reason, tt.reasonContains)
			}
		})
	}
}

func TestScheduleIdempotent(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_idem",
		InstanceID:     "inst_idem",
		ProjectID:      "prj",
		EnvironmentID:  "env",
		ServiceID:      "svc",
		Image:          "alpine:3",
		IdempotencyKey: "key-1",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("second schedule: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("expected single create on idempotent retry, got %d", fake.createCalls)
	}
}

func TestReconcileMarksMissingContainerLost(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_x",
		InstanceID:    "inst_x",
		ProjectID:     "prj",
		EnvironmentID: "env",
		ServiceID:     "svc",
		Image:         "alpine:3",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	// Simulate the container vanishing from the daemon.
	for _, c := range fake.containers {
		delete(fake.containersByID, c.id)
		delete(fake.containers, c.name)
		break
	}
	if err := a.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	statuses, err := a.StreamInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != runtime.InstanceStateLost {
		t.Fatalf("expected LOST, got %+v", statuses)
	}
}

func TestReconcileObservesRunningContainers(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_run",
		InstanceID:    "inst_run",
		ProjectID:     "prj",
		EnvironmentID: "env",
		ServiceID:     "svc",
		Image:         "alpine:3",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := a.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if fake.listCalls != 1 {
		t.Fatalf("expected 1 list call, got %d", fake.listCalls)
	}
	statuses, _ := a.StreamInstanceStatus(ctx)
	if statuses[0].State != runtime.InstanceStateRunning {
		t.Fatalf("expected RUNNING, got %s", statuses[0].State)
	}
}

func TestReconcileRebuildsRecordsAfterRestart(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapterWithHost(t, "host-a")
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_restart",
		InstanceID:     "inst_restart",
		ProjectID:      "prj_restart",
		EnvironmentID:  "env_restart",
		ServiceID:      "svc_restart",
		Image:          "registry.example.com/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		IdempotencyKey: "restart-key",
		Labels: map[string]string{
			"midnight.internal_port": "3001",
		},
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}

	restarted, _, err := New(ctx, Config{
		AgentID:    "agent-test",
		HostID:     "host-a",
		Engine:     fake,
		CGroupMode: CGroupModeV2,
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("new restarted adapter: %v", err)
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}

	statuses, err := restarted.StreamInstanceStatus(ctx)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1: %+v", len(statuses), statuses)
	}
	if statuses[0].DeploymentID != req.DeploymentID || statuses[0].InstanceID != req.InstanceID {
		t.Fatalf("rebuilt status identity = %s/%s, want %s/%s", statuses[0].DeploymentID, statuses[0].InstanceID, req.DeploymentID, req.InstanceID)
	}
	if statuses[0].State != runtime.InstanceStateRunning {
		t.Fatalf("rebuilt status state = %s, want RUNNING", statuses[0].State)
	}

	target, err := restarted.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		t.Fatalf("resolve rebuilt routing target: %v", err)
	}
	if target.ProjectID != req.ProjectID || target.EnvironmentID != req.EnvironmentID {
		t.Fatalf("rebuilt target tenant = %s/%s, want %s/%s", target.ProjectID, target.EnvironmentID, req.ProjectID, req.EnvironmentID)
	}
	if target.InternalPort != 3001 {
		t.Fatalf("rebuilt target internal port = %d, want 3001", target.InternalPort)
	}
}

func TestAdapterPassesConformanceSuite(t *testing.T) {
	a, _ := newTestAdapter(t)
	report := conformance.Run(context.Background(), a, conformance.DefaultFixture())
	if !report.Ok() {
		for _, r := range report.Results {
			if !r.Passed {
				t.Errorf("conformance check %s failed: %s", r.Name, r.Message)
			}
		}
	}
}

func TestScheduleRejectsMissingImage(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestAdapter(t)
	_, err := a.Schedule(ctx, runtime.ScheduleRequest{
		DeploymentID:  "dep",
		InstanceID:    "inst",
		ProjectID:     "p",
		EnvironmentID: "e",
		ServiceID:     "s",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != failurecodes.ContainerSpecInvalid {
		t.Fatalf("expected CONTAINER_SPEC_INVALID, got %s", CodeOf(err))
	}
}

func TestApplyNetworkPolicyTenancy(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestAdapter(t)
	if !a.Capabilities().NetworkPolicy {
		t.Fatal("NetworkPolicy capability = false, want true for fail-closed Docker network convergence")
	}
	if err := a.ApplyNetworkPolicy(ctx, runtime.NetworkPolicy{
		ProjectID:     "p1",
		EnvironmentID: "e1",
		BridgeName:    "or-net-p1-e1",
	}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := a.ApplyNetworkPolicy(ctx, runtime.NetworkPolicy{
		ProjectID:     "p2",
		EnvironmentID: "e1",
		BridgeName:    "or-net-p1-e1",
	}); err == nil {
		t.Fatal("expected cross-tenant rejection")
	}
}

func TestVolumeAttachDetachUnsupported(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestAdapter(t)
	if err := a.AttachVolume(ctx, runtime.VolumeAttachment{
		DeploymentID: "dep",
		InstanceID:   "inst",
		VolumeName:   "data",
		MountPath:    "/data",
	}); !errors.Is(err, runtime.ErrUnsupportedOperation) {
		t.Fatalf("AttachVolume() error = %v, want ErrUnsupportedOperation", err)
	}
	if err := a.DetachVolume(ctx, "dep", "inst", "data"); !errors.Is(err, runtime.ErrUnsupportedOperation) {
		t.Fatalf("DetachVolume() error = %v, want ErrUnsupportedOperation", err)
	}
}

func TestStopIsIdempotentForUnknownInstance(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestAdapter(t)
	if err := a.Stop(ctx, "missing", "instance", time.Second); err != nil {
		t.Fatalf("expected nil for unknown instance, got %v", err)
	}
}

// covers the documented stale-name conflict path: if a container exists
// under our deterministic name without managed-by labels, the schedule
// surfaces CONTAINER_NAME_CONFLICT.
func TestScheduleConflictWithUnmanagedContainer(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_conflict",
		InstanceID:    "inst_conflict",
		ProjectID:     "p",
		EnvironmentID: "e",
		ServiceID:     "svc",
		Image:         "alpine:3",
	}
	name := canonicalContainerName(req)
	fake.containers[name] = &fakeContainer{
		id:    "stranger",
		name:  name,
		cfg:   nil,
		hc:    nil,
		state: "running",
	}
	fake.containersByID["stranger"] = fake.containers[name]
	fake.failCreate = fmt.Errorf("Conflict. The container name %q is already in use", name)

	_, err := a.Schedule(ctx, req)
	if err == nil {
		t.Fatal("expected conflict")
	}
	if CodeOf(err) != failurecodes.ContainerNameConflict {
		t.Fatalf("expected CONTAINER_NAME_CONFLICT, got %s", CodeOf(err))
	}
}

// TestScheduleEnsuresPerProjectNetworkAndAliases asserts the private
// networking wiring: each schedule converges a per-(project, environment)
// Docker bridge, the container is attached to that network with both
// {slug} and {slug}.midnight.internal aliases, and the bridge is
// idempotent across schedules from the same scope.
func TestScheduleEnsuresPerProjectNetworkAndAliases(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_net",
		InstanceID:    "inst_net",
		ProjectID:     "prj_alpha",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_api",
		ServiceSlug:   "api",
		Image:         "alpine:3",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	// Second deploy under the same (project, env) re-uses the network.
	req2 := req
	req2.DeploymentID = "dep_net2"
	req2.InstanceID = "inst_net2"
	req2.ServiceID = "svc_web"
	req2.ServiceSlug = "web"
	if _, err := a.Schedule(ctx, req2); err != nil {
		t.Fatalf("second schedule: %v", err)
	}
	if fake.netCreateCalls != 1 {
		t.Fatalf("network create calls = %d, want 1 (idempotent on second schedule)", fake.netCreateCalls)
	}
	// Container must be attached to the project network with both aliases.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	c := fake.containers[canonicalContainerName(req)]
	if c == nil || c.netCfg == nil {
		t.Fatal("expected netCfg on created container")
	}
	pn := network.ProjectNetwork{ProjectID: req.ProjectID, EnvironmentID: req.EnvironmentID}
	endpoint, ok := c.netCfg.EndpointsConfig[pn.Name()]
	if !ok {
		t.Fatalf("netCfg missing endpoint %q: %#v", pn.Name(), c.netCfg.EndpointsConfig)
	}
	wantAliases := []string{"api", "api.midnight.internal"}
	if len(endpoint.Aliases) != len(wantAliases) {
		t.Fatalf("endpoint aliases = %#v, want %#v", endpoint.Aliases, wantAliases)
	}
	for i, alias := range wantAliases {
		if endpoint.Aliases[i] != alias {
			t.Fatalf("endpoint.Aliases[%d] = %q, want %q", i, endpoint.Aliases[i], alias)
		}
	}
}

func TestScheduleFailsClosedWhenPrivateNetworkCannotConverge(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	fake.failNetCreate = errors.New("network driver unavailable")
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_net_failure",
		InstanceID:    "inst_net_failure",
		ProjectID:     "prj_alpha",
		EnvironmentID: "env_prod",
		ServiceID:     "svc_api",
		ServiceSlug:   "api",
		Image:         "alpine:3",
	}

	_, err := a.Schedule(ctx, req)

	if err == nil {
		t.Fatal("Schedule() error = nil, want private-network failure")
	}
	if CodeOf(err) != failurecodes.PrivateNetworkUnavailable {
		t.Fatalf("Schedule() code = %s, want %s; error=%v", CodeOf(err), failurecodes.PrivateNetworkUnavailable, err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("container create calls = %d, want 0 when isolation cannot be guaranteed", fake.createCalls)
	}
}

func TestScheduleRejectsMissingNetworkScopeBeforeContainerCreate(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID: "dep_missing_scope",
		InstanceID:   "inst_missing_scope",
		ServiceID:    "svc_api",
		Image:        "alpine:3",
	}

	_, err := a.Schedule(ctx, req)

	if err == nil {
		t.Fatal("Schedule() error = nil, want private-network failure")
	}
	if CodeOf(err) != failurecodes.PrivateNetworkUnavailable {
		t.Fatalf("Schedule() code = %s, want %s; error=%v", CodeOf(err), failurecodes.PrivateNetworkUnavailable, err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("container create calls = %d, want 0 for missing network scope", fake.createCalls)
	}
}

// TestScheduleInjectsMidnightBuiltinsBeforeUserEnv asserts the env-var
// precedence rule: MIDNIGHT_* built-ins are emitted FIRST, then the
// user-supplied env entries. Docker honors the last occurrence of any
// duplicate key, so user vars override the built-ins — matching
// Railway's contract.
func TestScheduleInjectsMidnightBuiltinsBeforeUserEnv(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:    "dep_env",
		InstanceID:      "inst_env",
		ProjectID:       "prj_env",
		EnvironmentID:   "env_env",
		ServiceID:       "svc_env",
		ServiceSlug:     "api",
		ProjectSlug:     "demo",
		EnvironmentName: "production",
		PublicDomain:    "demo-api.example.com",
		Image:           "alpine:3",
		Env: []string{
			"DB_URL=postgres://example",
			"MIDNIGHT_SERVICE_NAME=user-override",
		},
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	c := fake.containers[canonicalContainerName(req)]
	if c == nil || c.cfg == nil {
		t.Fatal("expected container with cfg")
	}
	env := c.cfg.Env
	// Built-ins come first (sorted alphabetically inside the built-in
	// builder); user entries come after, in their original order.
	builtinPrefix := "MIDNIGHT_"
	firstUserIdx := -1
	lastBuiltinIdx := -1
	for i, kv := range env {
		if strings.HasPrefix(kv, builtinPrefix) {
			// Disambiguate the user override of MIDNIGHT_SERVICE_NAME: if
			// it carries the override value, it's a user entry.
			if kv == "MIDNIGHT_SERVICE_NAME=user-override" {
				if firstUserIdx < 0 {
					firstUserIdx = i
				}
				continue
			}
			lastBuiltinIdx = i
		} else if kv != "PORT=8080" {
			if firstUserIdx < 0 {
				firstUserIdx = i
			}
		}
	}
	if lastBuiltinIdx >= firstUserIdx && firstUserIdx >= 0 {
		t.Fatalf("built-ins must precede user env: lastBuiltinIdx=%d firstUserIdx=%d env=%#v", lastBuiltinIdx, firstUserIdx, env)
	}
	// User override of MIDNIGHT_SERVICE_NAME must appear AFTER the
	// built-in MIDNIGHT_SERVICE_NAME=api entry so Docker's
	// last-wins rule resolves to the override.
	builtinIdx := indexOfEnv(env, "MIDNIGHT_SERVICE_NAME=api")
	overrideIdx := indexOfEnv(env, "MIDNIGHT_SERVICE_NAME=user-override")
	if builtinIdx < 0 || overrideIdx < 0 || overrideIdx <= builtinIdx {
		t.Fatalf("override ordering wrong: builtinIdx=%d overrideIdx=%d env=%#v", builtinIdx, overrideIdx, env)
	}
	if !containsEnv(env, "MIDNIGHT_PRIVATE_DOMAIN=api.midnight.internal") {
		t.Fatalf("missing MIDNIGHT_PRIVATE_DOMAIN entry: %#v", env)
	}
	if !containsEnv(env, "MIDNIGHT_PUBLIC_DOMAIN=demo-api.example.com") {
		t.Fatalf("missing MIDNIGHT_PUBLIC_DOMAIN entry: %#v", env)
	}
	if !containsEnv(env, "MIDNIGHT_ENVIRONMENT_NAME=production") {
		t.Fatalf("missing MIDNIGHT_ENVIRONMENT_NAME entry: %#v", env)
	}
	if !containsEnv(env, "MIDNIGHT_REPLICA_ID=inst_env") {
		t.Fatalf("missing deterministic MIDNIGHT_REPLICA_ID entry: %#v", env)
	}
	if !containsEnv(env, "DB_URL=postgres://example") {
		t.Fatalf("missing user DB_URL entry: %#v", env)
	}
}

func indexOfEnv(env []string, want string) int {
	for i, kv := range env {
		if kv == want {
			return i
		}
	}
	return -1
}

// TestReconcileRediscoversNetworksAfterRestart simulates the worker's
// boot pass: the network already exists on the host, but the in-memory
// provisioner cache was lost. Reconcile must seed the cache without
// re-creating the network.
func TestReconcileRediscoversNetworksAfterRestart(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := runtime.ScheduleRequest{
		DeploymentID:  "dep_recover",
		InstanceID:    "inst_recover",
		ProjectID:     "prj_recover",
		EnvironmentID: "env_recover",
		ServiceID:     "svc_recover",
		ServiceSlug:   "api",
		Image:         "alpine:3",
	}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	if fake.netCreateCalls != 1 {
		t.Fatalf("setup: net create calls = %d, want 1", fake.netCreateCalls)
	}
	// Simulate process restart by rebuilding the adapter against the
	// same fake engine.
	restarted, _, err := New(ctx, Config{
		AgentID:    "agent-test",
		HostID:     "host-test",
		Engine:     fake,
		CGroupMode: CGroupModeV2,
		Now:        func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if err := restarted.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	// A NEW deployment under the same scope must not create a new
	// network: the rediscovery seeded the provisioner cache.
	req2 := req
	req2.DeploymentID = "dep_recover_v2"
	req2.InstanceID = "inst_recover_v2"
	req2.ServiceID = "svc_recover_v2"
	req2.IdempotencyKey = "recover-v2"
	if _, err := restarted.Schedule(ctx, req2); err != nil {
		t.Fatalf("post-restart schedule: %v", err)
	}
	if fake.netCreateCalls != 1 {
		t.Fatalf("net create calls after restart = %d, want still 1", fake.netCreateCalls)
	}
}

func scheduleRequestForPortTest(inferred bool) runtime.ScheduleRequest {
	return runtime.ScheduleRequest{
		DeploymentID:       "dep_port",
		InstanceID:         "inst_port",
		ProjectID:          "prj_01",
		EnvironmentID:      "env_01",
		ServiceID:          "svc_01",
		Image:              "registry.example.com/redis@sha256:3aaec283e6e593bde528077d60280ac1589887067a39273348860837c9346d7e",
		IdempotencyKey:     "port-key",
		TargetPort:         8080,
		TargetPortInferred: inferred,
		Labels: map[string]string{
			"midnight.project_id":     "prj_01",
			"midnight.environment_id": "env_01",
			"midnight.service_id":     "svc_01",
			"midnight.deployment_id":  "dep_port",
			"midnight.instance_id":    "inst_port",
		},
	}
}

func scheduledPortEnv(t *testing.T, fake *fakeEngine) string {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.containers {
		for _, env := range c.cfg.Env {
			if strings.HasPrefix(env, "PORT=") {
				return env
			}
		}
	}
	t.Fatal("no PORT env on scheduled container")
	return ""
}

func TestScheduleInfersTargetPortFromSingleExposedPort(t *testing.T) {
	ctx := context.Background()
	a, fake := newTestAdapter(t)
	req := scheduleRequestForPortTest(true)
	fake.exposedPorts = map[string][]int{req.Image: {6379}}
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got := scheduledPortEnv(t, fake); got != "PORT=6379" {
		t.Fatalf("PORT env = %q, want image-declared 6379", got)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}
	target, err := a.ResolveRoutingTarget(ctx, req.DeploymentID, req.InstanceID)
	if err != nil {
		t.Fatalf("resolve routing target: %v", err)
	}
	if target.InternalPort != 6379 {
		t.Fatalf("routing target port = %d, want 6379", target.InternalPort)
	}
}

func TestScheduleKeepsExplicitTargetPort(t *testing.T) {
	a, fake := newTestAdapter(t)
	req := scheduleRequestForPortTest(false)
	fake.exposedPorts = map[string][]int{req.Image: {6379}}
	if _, err := a.Schedule(context.Background(), req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got := scheduledPortEnv(t, fake); got != "PORT=8080" {
		t.Fatalf("PORT env = %q, want operator-chosen 8080", got)
	}
}

func TestScheduleKeepsDefaultWhenImageDeclaresSeveralPorts(t *testing.T) {
	a, fake := newTestAdapter(t)
	req := scheduleRequestForPortTest(true)
	fake.exposedPorts = map[string][]int{req.Image: {80, 443}}
	if _, err := a.Schedule(context.Background(), req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got := scheduledPortEnv(t, fake); got != "PORT=8080" {
		t.Fatalf("PORT env = %q, want default when the image is ambiguous", got)
	}
}

func TestScheduleKeepsDefaultWhenImageInspectFails(t *testing.T) {
	a, fake := newTestAdapter(t)
	req := scheduleRequestForPortTest(true)
	fake.exposedErr = errors.New("inspect unavailable")
	if _, err := a.Schedule(context.Background(), req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if got := scheduledPortEnv(t, fake); got != "PORT=8080" {
		t.Fatalf("PORT env = %q, want default on inspect failure", got)
	}
}

func TestExposedTCPPortsNormalizes(t *testing.T) {
	got := exposedTCPPorts(map[string]struct{}{"6379/tcp": {}, "53/udp": {}, "80": {}, "bogus": {}})
	if len(got) != 2 || got[0] != 80 || got[1] != 6379 {
		t.Fatalf("exposedTCPPorts = %v, want [80 6379]", got)
	}
	if exposedTCPPorts(nil) != nil {
		t.Fatal("empty set must return nil")
	}
}

func TestScheduleEnrollsProbeContainerOnce(t *testing.T) {
	ctx := context.Background()
	fake := newFakeEngine()
	a, _, err := New(ctx, Config{Engine: fake, CGroupMode: CGroupModeV2, ProbeContainer: "midnight-deploy-worker"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := scheduleRequestForPortTest(false)
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if len(fake.netConnects) != 1 || !strings.HasSuffix(fake.netConnects[0], "<-midnight-deploy-worker") {
		t.Fatalf("network connects = %v, want exactly one probe enrollment", fake.netConnects)
	}
	second := req
	second.DeploymentID, second.InstanceID, second.IdempotencyKey = "dep_port2", "inst_port2", "port-key-2"
	second.Labels["midnight.deployment_id"], second.Labels["midnight.instance_id"] = "dep_port2", "inst_port2"
	if _, err := a.Schedule(ctx, second); err != nil {
		t.Fatalf("second schedule: %v", err)
	}
	if len(fake.netConnects) != 1 {
		t.Fatalf("probe enrollment must be cached per network, got %v", fake.netConnects)
	}
}

func TestScheduleToleratesProbeAlreadyConnected(t *testing.T) {
	ctx := context.Background()
	fake := newFakeEngine()
	fake.failNetConnect = errors.New("endpoint with name midnight-deploy-worker already exists in network")
	a, _, err := New(ctx, Config{Engine: fake, CGroupMode: CGroupModeV2, ProbeContainer: "midnight-deploy-worker"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.Schedule(ctx, scheduleRequestForPortTest(false)); err != nil {
		t.Fatalf("schedule should tolerate an already-attached probe: %v", err)
	}
}

// livenessTestRequest is the minimal scheduled candidate the liveness tests
// need; each test then drives the fake container into a terminal shape.
func livenessTestRequest() runtime.ScheduleRequest {
	return runtime.ScheduleRequest{
		DeploymentID:   "dep_live",
		InstanceID:     "inst_live",
		ProjectID:      "prj_01",
		EnvironmentID:  "env_01",
		ServiceID:      "svc_01",
		Image:          "registry.example.com/worker:v1",
		IdempotencyKey: "live",
		Labels: map[string]string{
			"midnight.project_id":     "prj_01",
			"midnight.environment_id": "env_01",
			"midnight.service_id":     "svc_01",
			"midnight.deployment_id":  "dep_live",
			"midnight.instance_id":    "inst_live",
		},
	}
}

func scheduleLivenessCandidate(t *testing.T, a *Adapter, fake *fakeEngine) *fakeContainer {
	t.Helper()
	ctx := context.Background()
	req := livenessTestRequest()
	if _, err := a.Schedule(ctx, req); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if err := a.Start(ctx, req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("start: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, c := range fake.containersByID {
		return c
	}
	t.Fatal("scheduled candidate produced no container")
	return nil
}

func TestObserveInstanceLivenessReportsExitedCandidate(t *testing.T) {
	a, fake := newTestAdapter(t)
	c := scheduleLivenessCandidate(t, a, fake)
	fake.mu.Lock()
	c.state = "exited"
	c.exitCode = 1
	c.logs = []string{"boot", "FATAL: config file not found"}
	fake.mu.Unlock()

	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_live", "inst_live")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if !live.Observed {
		t.Fatal("liveness was not observed for a scheduled candidate")
	}
	if !live.Exited {
		t.Fatalf("exited container reported Exited=false (state %s)", live.State)
	}
	if live.RestartLooping {
		t.Fatal("a stopped container must not also report a restart loop")
	}
	if live.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1", live.ExitCode)
	}
	if !strings.Contains(live.Reason, "exited with code 1") {
		t.Fatalf("Reason = %q, want the exit code", live.Reason)
	}
	if len(live.LogTail) != 2 || !strings.Contains(live.LogTail[1], "config file not found") {
		t.Fatalf("LogTail = %v, want the container's own last lines", live.LogTail)
	}
}

func TestObserveInstanceLivenessReportsRestartLoop(t *testing.T) {
	a, fake := newTestAdapter(t)
	c := scheduleLivenessCandidate(t, a, fake)
	fake.mu.Lock()
	c.state = "running"
	c.restarting = true
	c.restartCount = restartLoopThreshold
	c.exitCode = 2
	c.logs = []string{"panic: nil map"}
	fake.mu.Unlock()

	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_live", "inst_live")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if live.Exited {
		t.Fatal("a restarting container has not exited for good")
	}
	if !live.RestartLooping {
		t.Fatalf("restart count %d did not report a loop", live.Restarts)
	}
	if len(live.LogTail) == 0 {
		t.Fatal("a restart loop must carry the container's own output")
	}
}

func TestObserveInstanceLivenessTreatsSlowStartAsNotTerminal(t *testing.T) {
	a, fake := newTestAdapter(t)
	c := scheduleLivenessCandidate(t, a, fake)
	fake.mu.Lock()
	c.state = "running"
	c.restartCount = 1
	fake.mu.Unlock()

	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_live", "inst_live")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if live.Exited || live.RestartLooping {
		t.Fatalf("a running container with %d restart reported terminal", live.Restarts)
	}
	if len(live.LogTail) != 0 {
		t.Fatal("a healthy-looking candidate must not pay for a log read")
	}
}

func TestObserveInstanceLivenessReportsOOMKill(t *testing.T) {
	a, fake := newTestAdapter(t)
	c := scheduleLivenessCandidate(t, a, fake)
	fake.mu.Lock()
	c.state = "exited"
	c.exitCode = 137
	c.oomKilled = true
	fake.mu.Unlock()

	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_live", "inst_live")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if !live.OOMKilled || !strings.Contains(live.Reason, "memory limit") {
		t.Fatalf("OOM kill reported as %q (OOMKilled=%t)", live.Reason, live.OOMKilled)
	}
}

func TestObserveInstanceLivenessStaysUnobservedWhenInspectFails(t *testing.T) {
	a, fake := newTestAdapter(t)
	scheduleLivenessCandidate(t, a, fake)
	fake.mu.Lock()
	fake.failInspect = errors.New("docker daemon unreachable")
	fake.mu.Unlock()

	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_live", "inst_live")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if live.Observed {
		t.Fatal("an unreadable daemon must never be reported as evidence about the workload")
	}
}

func TestObserveInstanceLivenessIsUnobservedForUnknownInstance(t *testing.T) {
	a, _ := newTestAdapter(t)
	live, err := a.ObserveInstanceLiveness(context.Background(), "dep_missing", "inst_missing")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if live.Observed {
		t.Fatal("an unknown instance must not report liveness evidence")
	}
}

func TestImageDeclaredPortsReportsImageExposure(t *testing.T) {
	a, fake := newTestAdapter(t)
	fake.exposedPorts = map[string][]int{"redis@sha256:aa": {6379}}

	ports, err := a.ImageDeclaredPorts(context.Background(), "redis@sha256:aa")
	if err != nil {
		t.Fatalf("ImageDeclaredPorts: %v", err)
	}
	if len(ports) != 1 || ports[0] != 6379 {
		t.Fatalf("ImageDeclaredPorts = %v, want [6379]", ports)
	}

	none, err := a.ImageDeclaredPorts(context.Background(), "worker@sha256:bb")
	if err != nil {
		t.Fatalf("ImageDeclaredPorts: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("image with no EXPOSE reported %v", none)
	}
}
