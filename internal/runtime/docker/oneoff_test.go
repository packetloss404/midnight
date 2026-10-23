package docker

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/midnight/midnight/internal/runtime"
)

func TestRunOneOffRetainsTerminalContainerAndRetryOnlyObserves(t *testing.T) {
	adapter, engine := newTestAdapter(t)
	request := oneOffFixture(2 * time.Second)
	adapter.SetSignatureVerifier(StaticSignatureVerifier{
		Signed: map[string]struct{}{request.ScheduleRequest.Image: {}},
	})

	exitDone := make(chan struct{})
	go func() {
		defer close(exitDone)
		waitForFakeStarts(engine, 1)
		exitFakePredeploy(engine, 0, time.Now().UTC())
	}()
	first, err := adapter.RunOneOff(context.Background(), request)
	if err != nil {
		t.Fatalf("RunOneOff: %v", err)
	}
	<-exitDone
	if first.ExitCode != 0 || first.TimedOut {
		t.Fatalf("first result = %+v", first)
	}

	engine.mu.Lock()
	created := engine.containers[first.ContainerName]
	if created == nil {
		engine.mu.Unlock()
		t.Fatalf("container %q not retained", first.ContainerName)
	}
	cfg, hc, netCfg := created.cfg, created.hc, created.netCfg
	createCalls, startCalls, pulls := engine.createCalls, engine.startCalls, len(engine.pulled)
	engine.mu.Unlock()
	if hc.RestartPolicy.Name != container.RestartPolicyDisabled || len(hc.Mounts) != 0 {
		t.Fatalf("host config restart=%q mounts=%+v, want restart=no and no mounts",
			hc.RestartPolicy.Name, hc.Mounts)
	}
	if !slices.Contains(cfg.Env, "SECRET=immutable-value") {
		t.Fatalf("container env = %#v, missing immutable snapshot value", cfg.Env)
	}
	if len(cfg.Entrypoint) != 2 || cfg.Entrypoint[0] != "/bin/sh" || cfg.Entrypoint[1] != "-lc" ||
		len(cfg.Cmd) != 1 || cfg.Cmd[0] != request.Command {
		t.Fatalf("command entrypoint=%#v cmd=%#v", cfg.Entrypoint, cfg.Cmd)
	}
	if netCfg == nil || len(netCfg.EndpointsConfig) != 1 {
		t.Fatalf("network config = %+v, want one tenant network", netCfg)
	}
	for _, endpoint := range netCfg.EndpointsConfig {
		if len(endpoint.Aliases) != 0 || len(endpoint.DNSNames) != 0 {
			t.Fatalf("one-off endpoint aliases=%#v dns=%#v, must not claim service DNS", endpoint.Aliases, endpoint.DNSNames)
		}
	}

	second, err := adapter.RunOneOff(context.Background(), request)
	if err != nil {
		t.Fatalf("retry RunOneOff: %v", err)
	}
	if second.ExitCode != 0 || second.ContainerID != first.ContainerID {
		t.Fatalf("retry result = %+v, first = %+v", second, first)
	}
	engine.mu.Lock()
	gotCreates, gotStarts, gotPulls := engine.createCalls, engine.startCalls, len(engine.pulled)
	engine.mu.Unlock()
	if gotCreates != createCalls || gotStarts != startCalls || gotPulls != pulls {
		t.Fatalf("retry effects create/start/pull=%d/%d/%d, want unchanged %d/%d/%d",
			gotCreates, gotStarts, gotPulls, createCalls, startCalls, pulls)
	}

	if err := adapter.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	statuses, err := adapter.StreamInstanceStatus(context.Background())
	if err != nil {
		t.Fatalf("StreamInstanceStatus: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("predeploy container leaked into service instances: %+v", statuses)
	}
}

func TestRunOneOffTimeoutStopsAndRetryPreservesTimeoutEvidence(t *testing.T) {
	adapter, engine := newTestAdapter(t)
	request := oneOffFixture(30 * time.Millisecond)
	request.Deadline = time.Now().UTC().Add(request.Timeout)
	adapter.SetSignatureVerifier(StaticSignatureVerifier{
		Signed: map[string]struct{}{request.ScheduleRequest.Image: {}},
	})
	first, err := adapter.RunOneOff(context.Background(), request)
	if err != nil {
		t.Fatalf("RunOneOff timeout: %v", err)
	}
	if !first.TimedOut || first.ExitCode != -1 {
		t.Fatalf("timeout result = %+v", first)
	}
	engine.mu.Lock()
	stopCalls := engine.stopCalls
	engine.mu.Unlock()
	if stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", stopCalls)
	}

	second, err := adapter.RunOneOff(context.Background(), request)
	if err != nil {
		t.Fatalf("retry timeout observation: %v", err)
	}
	if !second.TimedOut || second.ExitCode != -1 || second.ContainerID != first.ContainerID {
		t.Fatalf("retry timeout result = %+v, first = %+v", second, first)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.startCalls != 1 || engine.stopCalls != 1 || engine.createCalls != 1 {
		t.Fatalf("retry reran timed-out container: create/start/stop=%d/%d/%d",
			engine.createCalls, engine.startCalls, engine.stopCalls)
	}
}

func TestCronOneOffUsesSeparateDeterministicIdentity(t *testing.T) {
	request := oneOffFixture(time.Minute)
	request.WorkloadKind = WorkloadKindCron
	if got, want := oneOffContainerName(request), "or-cron-"+request.SpecSHA256[:24]; got != want {
		t.Fatalf("container name = %q, want %q", got, want)
	}
	if got := oneOffSpecLabel(request); got != LabelCronSpec {
		t.Fatalf("spec label = %q, want %q", got, LabelCronSpec)
	}
	if err := validateOneOffRequest(request); err != nil {
		t.Fatalf("validate cron one-off: %v", err)
	}
}

func TestTemplateSmokeCleanupRemovesSecretBearingContainer(t *testing.T) {
	adapter, engine := newTestAdapter(t)
	request := oneOffFixture(2 * time.Second)
	request.WorkloadKind = WorkloadKindTemplateSmoke
	request.Command = `test "$SECRET" = immutable-value`
	adapter.SetSignatureVerifier(StaticSignatureVerifier{
		Signed: map[string]struct{}{request.ScheduleRequest.Image: {}},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForFakeStarts(engine, 1)
		engine.mu.Lock()
		for _, item := range engine.containers {
			if item.cfg != nil && item.cfg.Labels[LabelWorkloadKind] == WorkloadKindTemplateSmoke {
				item.state = "exited"
				item.exitCode = 9
				item.finishedAt = time.Now().UTC().Format(time.RFC3339Nano)
				break
			}
		}
		engine.mu.Unlock()
	}()
	result, err := adapter.RunOneOff(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if result.ExitCode != 9 || result.ContainerID == "" {
		t.Fatalf("result=%+v", result)
	}
	if err := adapter.CleanupTemplateSmoke(context.Background(), []string{result.ContainerID}); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if _, exists := engine.containersByID[result.ContainerID]; exists {
		t.Fatal("secret-bearing template smoke container remains after cleanup")
	}
}

func oneOffFixture(timeout time.Duration) runtime.OneOffRequest {
	const spec = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return runtime.OneOffRequest{
		ScheduleRequest: runtime.ScheduleRequest{
			DeploymentID: "dep-1", InstanceID: "dep-1-r0",
			ProjectID: "prj-1", EnvironmentID: "env-1", ServiceID: "svc-1",
			Image: "registry.example/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Env:   []string{"SECRET=immutable-value"},
		},
		Command:    "bin/migrate up",
		SpecSHA256: spec,
		Timeout:    timeout,
		Deadline:   time.Now().UTC().Add(timeout),
	}
}

func waitForFakeStarts(engine *fakeEngine, count int) {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		engine.mu.Lock()
		starts := engine.startCalls
		engine.mu.Unlock()
		if starts >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func exitFakePredeploy(engine *fakeEngine, exitCode int, finishedAt time.Time) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	for _, item := range engine.containers {
		if item.cfg != nil && item.cfg.Labels[LabelWorkloadKind] == WorkloadKindPredeploy {
			item.state = "exited"
			item.exitCode = exitCode
			item.finishedAt = finishedAt.Format(time.RFC3339Nano)
			return
		}
	}
}
