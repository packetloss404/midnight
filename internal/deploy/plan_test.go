package deploy

import (
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/tcpproxy"
)

func TestBuildPlanComposesDeploymentJourney(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	plan, err := BuildPlan(Request{
		Now:           now,
		DeploymentID:  "dep_01",
		ProjectID:     "prj_01",
		EnvironmentID: "prod",
		ServiceID:     "api",
		Replicas:      2,
		BuildArtifact: runtime.BuildArtifact{Image: "registry.example.com/api:v1", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		Hosts: []scheduler.Host{
			{ID: "host-b", Status: scheduler.HostStatusReady, Capacity: 2},
			{ID: "host-a", Status: scheduler.HostStatusReady, Capacity: 2},
		},
		Predeploy: PredeploySpec{Enabled: true, Command: []string{" migrate ", "up"}, Timeout: time.Minute},
		Cron:      CronMarker{Available: true, Schedule: "*/5 * * * *", Timezone: "UTC"},
		Static:    StaticMarker{Available: true, Dist: "dist", Index: "index.html", PromoteRef: "static/dep_01"},
		TCPProxy: TCPProxySpec{Enabled: true, Request: tcpproxy.EndpointRequest{
			BaseDomain:     "example.test",
			BackendPort:    8080,
			SourceIPPolicy: tcpproxy.SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.10/32"}},
		}},
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if !plan.ReadyForDispatch {
		t.Fatalf("ReadyForDispatch = false, steps = %#v", plan.Steps)
	}
	if plan.SchemaVersion != SchemaVersion || plan.GeneratedAt != now {
		t.Fatalf("plan header = %#v", plan)
	}
	if plan.Predeploy == nil || plan.Predeploy.Command[0] != "migrate" || plan.Cron == nil || plan.Static == nil {
		t.Fatalf("markers/predeploy missing: %#v", plan)
	}
	if len(plan.Placement.Placements) != 2 || plan.Placement.Placements[0].HostID != "host-a" || plan.Placement.Placements[1].HostID != "host-b" {
		t.Fatalf("placements = %#v", plan.Placement.Placements)
	}
	if len(plan.RuntimeRequests) != 2 {
		t.Fatalf("runtime requests = %#v", plan.RuntimeRequests)
	}
	if plan.RuntimeRequests[0].DeploymentID != "dep_01" || plan.RuntimeRequests[0].Labels["midnight.host"] != "host-a" {
		t.Fatalf("runtime request[0] = %#v", plan.RuntimeRequests[0])
	}
	if plan.TCPProxy == nil || plan.TCPProxy.PublicAddress != "api--prod--prj-01.tcp.example.test:20000" || plan.TCPProxy.BackendHost != "dep_01-r0" {
		t.Fatalf("tcp proxy = %#v", plan.TCPProxy)
	}
	if len(plan.AuditHints) < 4 || len(plan.TimelineHints) != len(plan.Steps)+1 {
		t.Fatalf("hints audit=%#v timeline=%#v", plan.AuditHints, plan.TimelineHints)
	}
}

func TestBuildPlanBlocksWhenPredeployCommandMissing(t *testing.T) {
	plan, err := BuildPlan(baseRequest(Request{Predeploy: PredeploySpec{Enabled: true}}))
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ReadyForDispatch {
		t.Fatal("ReadyForDispatch = true, want blocked")
	}
	if plan.Steps[0].Outcome != StepOutcomeBlocked {
		t.Fatalf("predeploy step = %#v", plan.Steps[0])
	}
	if got := plan.AuditHints[len(plan.AuditHints)-1].Action; got != "deployment.plan.blocked" {
		t.Fatalf("last audit action = %q", got)
	}
}

func TestBuildPlanBlocksWhenPlacementCapacityMissing(t *testing.T) {
	plan, err := BuildPlan(baseRequest(Request{Replicas: 2, Hosts: []scheduler.Host{{ID: "host-a", Status: scheduler.HostStatusReady, Capacity: 1}}}))
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ReadyForDispatch {
		t.Fatal("ReadyForDispatch = true, want blocked")
	}
	if len(plan.RuntimeRequests) != 1 {
		t.Fatalf("runtime requests = %d, want accepted placement only", len(plan.RuntimeRequests))
	}
	if plan.Steps[3].Name != "scheduler-placement" || plan.Steps[3].Outcome != StepOutcomeBlocked {
		t.Fatalf("scheduler step = %#v", plan.Steps[3])
	}
}

func TestBuildPlanBlocksTCPProxyWithoutRuntimeTargets(t *testing.T) {
	plan, err := BuildPlan(baseRequest(Request{Hosts: []scheduler.Host{}, TCPProxy: TCPProxySpec{Enabled: true, Request: tcpproxy.EndpointRequest{
		BaseDomain:     "example.test",
		BackendPort:    8080,
		SourceIPPolicy: tcpproxy.SourceIPPolicy{AllowedCIDRs: []string{"203.0.113.10/32"}},
	}}}))
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if plan.ReadyForDispatch {
		t.Fatal("ReadyForDispatch = true, want blocked")
	}
	last := plan.Steps[len(plan.Steps)-1]
	if last.Name != "tcp-proxy-contract" || last.Outcome != StepOutcomeBlocked {
		t.Fatalf("tcp step = %#v", last)
	}
}

func baseRequest(overrides Request) Request {
	req := Request{
		Now:           time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		DeploymentID:  "dep_01",
		ProjectID:     "prj_01",
		EnvironmentID: "prod",
		ServiceID:     "api",
		Replicas:      1,
		BuildArtifact: runtime.BuildArtifact{Image: "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		Hosts:         []scheduler.Host{{ID: "host-a", Status: scheduler.HostStatusReady, Capacity: 2}},
	}
	if !overrides.Now.IsZero() {
		req.Now = overrides.Now
	}
	if overrides.DeploymentID != "" {
		req.DeploymentID = overrides.DeploymentID
	}
	if overrides.ProjectID != "" {
		req.ProjectID = overrides.ProjectID
	}
	if overrides.EnvironmentID != "" {
		req.EnvironmentID = overrides.EnvironmentID
	}
	if overrides.ServiceID != "" {
		req.ServiceID = overrides.ServiceID
	}
	if overrides.Replicas != 0 {
		req.Replicas = overrides.Replicas
	}
	if overrides.BuildArtifact.Image != "" || overrides.BuildArtifact.Digest != "" {
		req.BuildArtifact = overrides.BuildArtifact
	}
	if overrides.Hosts != nil {
		req.Hosts = overrides.Hosts
	}
	req.Predeploy = overrides.Predeploy
	req.Cron = overrides.Cron
	req.Static = overrides.Static
	req.TCPProxy = overrides.TCPProxy
	return req
}
