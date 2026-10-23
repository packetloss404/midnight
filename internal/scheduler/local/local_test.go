package local_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/scheduler"
	"github.com/midnight/midnight/internal/scheduler/local"
)

// These tests are ports of internal/deploy/executor_test.go's runtime-level
// expectations, asserted against LocalDockerScheduler directly so the
// scheduler boundary is exercised in isolation from the executor's FSM
// telemetry. The full executor test suite still runs through this scheduler
// via deploy.Executor.resolveScheduler().

func TestLocalDockerSchedulerSuccessRunsRuntimeStepsInOrder(t *testing.T) {
	calls := []string{}
	rt := &fakeRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	s := local.New(rt)

	placement, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{
		ScheduleRequest: scheduleRequest(),
	})
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	want := []string{"pull-verify", "schedule", "start", "resolve-routing"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if placement.PrimaryHostID() != local.DefaultHostID {
		t.Fatalf("primary host id = %q, want %q", placement.PrimaryHostID(), local.DefaultHostID)
	}
	if got := placement.Assignments[0].Capabilities["runtime"]; got != local.DefaultRuntimeCapability {
		t.Fatalf("runtime capability = %q, want %q", got, local.DefaultRuntimeCapability)
	}
	if !reflect.DeepEqual(placement.RoutingTarget, readyTarget()) {
		t.Fatalf("routing target = %#v, want ready target", placement.RoutingTarget)
	}
}

func TestLocalDockerSchedulerEmitsStepObserverEvents(t *testing.T) {
	calls := []string{}
	observer := &recordingStepObserver{}
	s := local.New(&fakeRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()})

	if _, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{
		ScheduleRequest: scheduleRequest(),
		StepObserver:    observer,
	}); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	want := []string{
		"pull-verify:started", "pull-verify:succeeded",
		"schedule:started", "schedule:succeeded",
		"start:started", "start:succeeded",
		"resolve-routing:started", "resolve-routing:succeeded",
	}
	if got := observer.keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("observer keys = %#v, want %#v", got, want)
	}
}

func TestLocalDockerSchedulerStopsAtUnverifiedPullWithoutError(t *testing.T) {
	calls := []string{}
	rt := &fakeRuntime{
		calls: &calls,
		pull:  runtime.PullVerifyResult{Image: scheduleRequest().Image, Verified: false, Trusted: false, Reason: "signature missing"},
	}
	s := local.New(rt)

	placement, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if err != nil {
		t.Fatalf("Schedule() error = %v, want nil so executor classifies the unverified pull itself", err)
	}
	want := []string{"pull-verify"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if placement.PullVerify.Verified || placement.PullVerify.Trusted {
		t.Fatalf("placement pull-verify = %#v, want unverified", placement.PullVerify)
	}
	if placement.RoutingTarget.ContainerID != "" {
		t.Fatalf("placement routing target = %#v, want empty when pull is unverified", placement.RoutingTarget)
	}
}

func TestLocalDockerSchedulerSurfacesScheduleError(t *testing.T) {
	calls := []string{}
	rtErr := errors.New("docker schedule failed")
	rt := &fakeRuntime{
		calls: &calls,
		pull:  verifiedPull(),
		errs:  map[string]error{"schedule": rtErr},
	}
	s := local.New(rt)

	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, rtErr) {
		t.Fatalf("Schedule() error = %v, want %v", err, rtErr)
	}
	want := []string{"pull-verify", "schedule"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLocalDockerSchedulerSurfacesStartError(t *testing.T) {
	calls := []string{}
	rtErr := errors.New("docker start failed")
	rt := &fakeRuntime{calls: &calls, pull: verifiedPull(), errs: map[string]error{"start": rtErr}}
	s := local.New(rt)

	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, rtErr) {
		t.Fatalf("Schedule() error = %v, want %v", err, rtErr)
	}
	want := []string{"pull-verify", "schedule", "start"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLocalDockerSchedulerSurfacesResolveRoutingError(t *testing.T) {
	calls := []string{}
	rtErr := errors.New("docker resolve-routing failed")
	rt := &fakeRuntime{calls: &calls, pull: verifiedPull(), errs: map[string]error{"resolve-routing": rtErr}}
	s := local.New(rt)

	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, rtErr) {
		t.Fatalf("Schedule() error = %v, want %v", err, rtErr)
	}
	want := []string{"pull-verify", "schedule", "start", "resolve-routing"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestLocalDockerSchedulerRequiresRuntime(t *testing.T) {
	s := local.New(nil)
	_, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: scheduleRequest()})
	if !errors.Is(err, local.ErrRuntimeRequired) {
		t.Fatalf("Schedule() error = %v, want ErrRuntimeRequired", err)
	}
}

func TestLocalDockerSchedulerDrainInstanceForwardsCall(t *testing.T) {
	rt := &fakeRuntime{calls: new([]string)}
	s := local.New(rt)

	if err := s.DrainInstance(context.Background(), "dep-1", "dep-1-r0", 15*time.Second); err != nil {
		t.Fatalf("DrainInstance() error = %v", err)
	}
	if len(rt.drainCalls) != 1 {
		t.Fatalf("drain calls = %#v, want one", rt.drainCalls)
	}
	got := rt.drainCalls[0]
	if got.deploymentID != "dep-1" || got.instanceID != "dep-1-r0" || got.grace != 15*time.Second {
		t.Fatalf("drain call = %#v, want (dep-1, dep-1-r0, 15s)", got)
	}
}

func TestLocalDockerSchedulerDrainHonorsSlashConvention(t *testing.T) {
	rt := &fakeRuntime{calls: new([]string)}
	s := local.New(rt)

	if err := s.Drain(context.Background(), "dep-1/dep-1-r0", 5*time.Second); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if len(rt.drainCalls) != 1 || rt.drainCalls[0].deploymentID != "dep-1" || rt.drainCalls[0].instanceID != "dep-1-r0" {
		t.Fatalf("drain calls = %#v, want split deployment/instance ids", rt.drainCalls)
	}
}

func TestLocalDockerSchedulerImplementsSchedulerInterface(t *testing.T) {
	var _ scheduler.Scheduler = (*local.LocalDockerScheduler)(nil)
}

// TestLocalDockerSchedulerPropagatesNetworkContextThroughSchedule asserts
// that the scheduler forwards the (project_id, environment_id,
// service_slug, env) fields to the runtime so the docker adapter can
// converge the per-project network and inject MIDNIGHT_* built-ins. The
// adapter's behavior is covered in its own test suite; this test pins the
// scheduler boundary as the conduit.
func TestLocalDockerSchedulerPropagatesNetworkContextThroughSchedule(t *testing.T) {
	calls := []string{}
	rt := &fakeRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	s := local.New(rt)

	req := runtime.ScheduleRequest{
		DeploymentID:    "dep-3",
		InstanceID:      "dep-3-r0",
		ProjectID:       "prj-1",
		EnvironmentID:   "prod",
		ServiceID:       "api",
		ServiceSlug:     "api",
		ProjectSlug:     "demo",
		EnvironmentName: "production",
		PublicDomain:    "demo-api.example.com",
		Image:           "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		IdempotencyKey:  "op-3",
		Env:             []string{"DB_URL=postgres://example"},
	}
	if _, err := s.Schedule(context.Background(), scheduler.ScheduleIntent{ScheduleRequest: req}); err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}
	if rt.lastSchedule == nil {
		t.Fatal("runtime never observed schedule request")
	}
	got := *rt.lastSchedule
	if got.ServiceSlug != "api" || got.ProjectSlug != "demo" || got.EnvironmentName != "production" {
		t.Fatalf("scheduler dropped slug fields: %#v", got)
	}
	if got.PublicDomain != "demo-api.example.com" {
		t.Fatalf("scheduler dropped public domain: %#v", got)
	}
	if len(got.Env) != 1 || got.Env[0] != "DB_URL=postgres://example" {
		t.Fatalf("scheduler dropped env: %#v", got.Env)
	}
}

// --- fixtures -------------------------------------------------------------

func scheduleRequest() runtime.ScheduleRequest {
	return runtime.ScheduleRequest{
		DeploymentID:   "dep-2",
		InstanceID:     "dep-2-r0",
		ProjectID:      "prj-1",
		EnvironmentID:  "prod",
		ServiceID:      "api",
		Image:          "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		IdempotencyKey: "op-1",
	}
}

func verifiedPull() runtime.PullVerifyResult {
	return runtime.PullVerifyResult{Image: scheduleRequest().Image, Verified: true, Trusted: true}
}

func readyTarget() runtime.RoutingTarget {
	return runtime.RoutingTarget{
		DeploymentID: "dep-2",
		InstanceID:   "dep-2-r0",
		ContainerID:  "ctr-2",
		ContainerIP:  "127.0.0.1",
		Ready:        true,
	}
}

type fakeRuntime struct {
	calls        *[]string
	pull         runtime.PullVerifyResult
	target       runtime.RoutingTarget
	errs         map[string]error
	drainCalls   []fakeDrainCall
	lastSchedule *runtime.ScheduleRequest
}

type fakeDrainCall struct {
	deploymentID string
	instanceID   string
	grace        time.Duration
}

func (r *fakeRuntime) PullVerify(_ context.Context, _ string) (runtime.PullVerifyResult, error) {
	r.record("pull-verify")
	return r.pull, r.errs["pull-verify"]
}

func (r *fakeRuntime) Schedule(_ context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	r.record("schedule")
	copy := req
	r.lastSchedule = &copy
	return runtime.InstanceStatus{State: runtime.InstanceStateAllocated}, r.errs["schedule"]
}

func (r *fakeRuntime) Start(_ context.Context, _, _ string) error {
	r.record("start")
	return r.errs["start"]
}

func (r *fakeRuntime) ResolveRoutingTarget(_ context.Context, _, _ string) (runtime.RoutingTarget, error) {
	r.record("resolve-routing")
	return r.target, r.errs["resolve-routing"]
}

func (r *fakeRuntime) Drain(_ context.Context, deploymentID, instanceID string, grace time.Duration) error {
	r.record("drain")
	r.drainCalls = append(r.drainCalls, fakeDrainCall{deploymentID: deploymentID, instanceID: instanceID, grace: grace})
	return r.errs["drain"]
}

func (r *fakeRuntime) record(call string) {
	if r.calls != nil {
		*r.calls = append(*r.calls, call)
	}
}

type recordingStepObserver struct {
	events []recordedStep
}

type recordedStep struct {
	step  scheduler.ScheduleStep
	state scheduler.ScheduleStepState
}

func (o *recordingStepObserver) ObserveScheduleStep(step scheduler.ScheduleStep, state scheduler.ScheduleStepState, _ error) {
	o.events = append(o.events, recordedStep{step: step, state: state})
}

func (o *recordingStepObserver) keys() []string {
	keys := make([]string, 0, len(o.events))
	for _, e := range o.events {
		keys = append(keys, string(e.step)+":"+string(e.state))
	}
	return keys
}

// livenessAdapter is a runtime that can answer the optional liveness and
// image-port questions the readiness gate asks.
type livenessAdapter struct {
	local.Runtime
	live  runtime.InstanceLiveness
	ports []int
}

func (a livenessAdapter) ObserveInstanceLiveness(_ context.Context, deploymentID, instanceID string) (runtime.InstanceLiveness, error) {
	live := a.live
	live.DeploymentID, live.InstanceID = deploymentID, instanceID
	return live, nil
}

func (a livenessAdapter) ImageDeclaredPorts(context.Context, string) ([]int, error) {
	return append([]int(nil), a.ports...), nil
}

func TestLocalSchedulerForwardsInstanceLiveness(t *testing.T) {
	sched := local.New(livenessAdapter{
		Runtime: &fakeRuntime{},
		live:    runtime.InstanceLiveness{Observed: true, Exited: true, ExitCode: 3},
		ports:   []int{6379},
	})

	live, err := sched.ObserveInstanceLiveness(context.Background(), "dep-1", "dep-1-r0")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if !live.Observed || !live.Exited || live.ExitCode != 3 {
		t.Fatalf("liveness = %#v, want the adapter's answer forwarded unchanged", live)
	}
	if live.DeploymentID != "dep-1" || live.InstanceID != "dep-1-r0" {
		t.Fatalf("liveness identity = %s/%s, want the requested instance", live.DeploymentID, live.InstanceID)
	}

	ports, err := sched.ImageDeclaredPorts(context.Background(), "redis@sha256:aa")
	if err != nil {
		t.Fatalf("ImageDeclaredPorts: %v", err)
	}
	if len(ports) != 1 || ports[0] != 6379 {
		t.Fatalf("ports = %v, want [6379]", ports)
	}
}

func TestLocalSchedulerReportsUnansweredLivenessRatherThanGuessing(t *testing.T) {
	sched := local.New(&fakeRuntime{})

	live, err := sched.ObserveInstanceLiveness(context.Background(), "dep-1", "dep-1-r0")
	if err != nil {
		t.Fatalf("ObserveInstanceLiveness: %v", err)
	}
	if live.Observed {
		t.Fatal("an adapter without the capability must not report evidence about the workload")
	}
	if _, err := sched.ImageDeclaredPorts(context.Background(), "redis@sha256:aa"); err == nil {
		t.Fatal("an adapter that cannot report image ports must say so, not report none")
	}
}
