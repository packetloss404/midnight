package control_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/control"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/api/middleware"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/runtime/docker"
	"github.com/midnight/midnight/internal/version"
)

// TestAgentLoopRegistersAndHeartbeatsAgainstServer is the end-to-end check
// the prompt's acceptance bar names: "agent registers, heartbeats, and
// reconciles a no-op desired state against a running midnight-server in an
// integration test."
//
// The test runs the v1alpha agent handlers behind httptest.NewServer (no
// Docker), a synthetic Docker EngineClient via the in-memory test seam in
// the runtime/docker package, and the same control.Loop the production
// agent uses. We assert the server records the registration + heartbeat +
// status report inside a wall-clock window.
func TestAgentLoopRegistersAndHeartbeatsAgainstServer(t *testing.T) {
	registry := api.NewAgentRegistry([]byte("integration-secret"), nil)
	mux := http.NewServeMux()
	api.RegisterAgentHandlers(mux, registry)
	server := httptest.NewServer(middleware.RequestID(mux))
	defer server.Close()

	httpClient, err := control.NewHTTPClient(control.HTTPClientConfig{}, server.URL)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}
	client := control.NewAgentClient(httpClient, server.URL, "integration-secret", "midnight-agent-test")

	engine := docker.NewFakeEngineForTests()
	adapter, baseline, err := docker.New(context.Background(), docker.Config{
		AgentID:    "agent-it",
		HostID:     "host-it",
		Engine:     engine,
		CGroupMode: docker.CGroupModeV2,
	})
	if err != nil {
		t.Fatalf("docker adapter: %v", err)
	}

	loop, err := control.NewLoop(control.LoopConfig{
		AgentID:             "agent-it",
		Hostname:            "host-it",
		AgentVersion:        version.Version,
		HMACSecret:          []byte("integration-secret"),
		HeartbeatInterval:   50 * time.Millisecond,
		HeartbeatTimeout:    2 * time.Second,
		StatusReportTimeout: 2 * time.Second,
		Adapter:             adapter,
		Baseline:            baseline,
		CGroupMode:          docker.CGroupModeV2,
	}, client)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	regCtx, regCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer regCancel()
	if _, err := loop.Register(regCtx); err != nil {
		t.Fatalf("register: %v", err)
	}

	tickCtx, tickCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer tickCancel()
	if err := loop.SendHeartbeat(tickCtx); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}
	loop.Tick(tickCtx)

	hb, ok := registry.LastHeartbeat("agent-it")
	if !ok || hb.IsZero() {
		t.Logf("freshness review: %+v", loop.HeartbeatFreshness())
		t.Fatal("expected heartbeat to be recorded")
	}

	report, ok := registry.LastStatusReport("agent-it")
	if !ok || report.ObservedAt.IsZero() {
		t.Fatal("expected status report after Tick")
	}
}

func TestAgentLoopReportsRouteTargetsRecoveredFromRuntime(t *testing.T) {
	registry := api.NewAgentRegistry([]byte("integration-secret"), nil)
	mux := http.NewServeMux()
	api.RegisterAgentHandlers(mux, registry)
	server := httptest.NewServer(middleware.RequestID(mux))
	defer server.Close()

	httpClient, err := control.NewHTTPClient(control.HTTPClientConfig{}, server.URL)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}
	client := control.NewAgentClient(httpClient, server.URL, "integration-secret", "midnight-agent-test")

	engine := docker.NewFakeEngineForTests()
	seed, _, err := docker.New(context.Background(), docker.Config{
		AgentID:    "agent-it",
		HostID:     "host-it",
		Engine:     engine,
		CGroupMode: docker.CGroupModeV2,
	})
	if err != nil {
		t.Fatalf("seed docker adapter: %v", err)
	}
	req := runtime.ScheduleRequest{
		DeploymentID:   "dep_recovered",
		InstanceID:     "inst_recovered_0",
		ProjectID:      "prj_recovered",
		EnvironmentID:  "env_recovered",
		ServiceID:      "svc_recovered",
		Image:          "registry.example/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey: "op_recovered",
		Labels: map[string]string{
			"midnight.internal_port": "3000",
		},
	}
	if _, err := seed.Schedule(context.Background(), req); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	if err := seed.Start(context.Background(), req.DeploymentID, req.InstanceID); err != nil {
		t.Fatalf("seed start: %v", err)
	}

	restarted, baseline, err := docker.New(context.Background(), docker.Config{
		AgentID:    "agent-it",
		HostID:     "host-it",
		Engine:     engine,
		CGroupMode: docker.CGroupModeV2,
	})
	if err != nil {
		t.Fatalf("restarted docker adapter: %v", err)
	}
	loop, err := control.NewLoop(control.LoopConfig{
		AgentID:             "agent-it",
		Hostname:            "host-it",
		AgentVersion:        version.Version,
		HMACSecret:          []byte("integration-secret"),
		HeartbeatInterval:   50 * time.Millisecond,
		HeartbeatTimeout:    2 * time.Second,
		StatusReportTimeout: 2 * time.Second,
		Adapter:             restarted,
		Baseline:            baseline,
		CGroupMode:          docker.CGroupModeV2,
	}, client)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := loop.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	loop.Tick(ctx)

	report, ok := registry.LastStatusReport("agent-it")
	if !ok {
		t.Fatal("expected status report after recovered tick")
	}
	if len(report.Instances) != 1 || report.Instances[0].DeploymentID != req.DeploymentID || report.Instances[0].State != "RUNNING" {
		t.Fatalf("instances = %#v, want recovered running instance", report.Instances)
	}
	if len(report.RouteTargets) != 1 {
		t.Fatalf("route targets = %#v, want one recovered route target", report.RouteTargets)
	}
	target := report.RouteTargets[0]
	if target.DeploymentID != req.DeploymentID || target.InstanceID != req.InstanceID || target.ContainerIP != "10.0.0.1" || target.InternalServicePort != 3000 || target.ReadinessStatus != "ready" {
		t.Fatalf("route target = %#v, want recovered ready target", target)
	}
}

func TestAgentLoopRefusesVersionSkew(t *testing.T) {
	registry := api.NewAgentRegistry([]byte("integration-secret"), nil)
	mux := http.NewServeMux()
	api.RegisterAgentHandlers(mux, registry)
	server := httptest.NewServer(middleware.RequestID(mux))
	defer server.Close()

	httpClient, err := control.NewHTTPClient(control.HTTPClientConfig{}, server.URL)
	if err != nil {
		t.Fatalf("http client: %v", err)
	}
	client := control.NewAgentClient(httpClient, server.URL, "integration-secret", "midnight-agent-test")

	engine := docker.NewFakeEngineForTests()
	adapter, baseline, err := docker.New(context.Background(), docker.Config{
		AgentID:    "skew",
		HostID:     "host-skew",
		Engine:     engine,
		CGroupMode: docker.CGroupModeV2,
	})
	if err != nil {
		t.Fatalf("docker adapter: %v", err)
	}

	loop, err := control.NewLoop(control.LoopConfig{
		AgentID:      "skew",
		AgentVersion: "0.0.0-from-mars",
		HMACSecret:   []byte("integration-secret"),
		Adapter:      adapter,
		Baseline:     baseline,
		CGroupMode:   docker.CGroupModeV2,
	}, client)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := loop.Register(ctx); err == nil {
		t.Fatal("expected version skew rejection")
	} else if got := control.CodeOf(err); got != "VERSION_SKEW_REJECTED" {
		t.Fatalf("expected VERSION_SKEW_REJECTED, got %q", got)
	}
}
