package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/router"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/serviceconfig"
	templatepkg "github.com/midnight/midnight/internal/templates"
	"github.com/midnight/midnight/internal/varrefs"
	"github.com/midnight/midnight/internal/volume"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestExecutorSuccessPromotesThenDrainsPrevious(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	executor := Executor{Runtime: rt, Promoter: promoter, Operations: ops, DrainGrace: 15 * time.Second}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !out.Succeeded || ops.failed {
		t.Fatalf("outcome = %#v ops = %#v, want success", out, ops)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "promote", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if rt.pullImages[0] != imageRef() {
		t.Fatalf("pull image = %q, want %q", rt.pullImages[0], imageRef())
	}
	if len(rt.scheduleRequests) != 1 {
		t.Fatalf("schedule requests = %#v, want one", rt.scheduleRequests)
	}
	scheduled := rt.scheduleRequests[0]
	if scheduled.DeploymentID != "dep-2" || scheduled.InstanceID != "dep-2-r0" || scheduled.Image != imageRef() || scheduled.IdempotencyKey != "op-1" {
		t.Fatalf("schedule request = %#v, want candidate deployment, image, and idempotency key", scheduled)
	}
	if len(promoter.targets) != 1 || !reflect.DeepEqual(promoter.targets[0], readyTarget()) {
		t.Fatalf("promoted targets = %#v, want ready target", promoter.targets)
	}
	if len(promoter.metadata) != 1 || promoter.metadata[0].RouteName != "api" || promoter.metadata[0].RouteGroup != "apps" || promoter.metadata[0].Host != "api.example.test" || promoter.metadata[0].PathPrefix != "/v1" {
		t.Fatalf("promoter metadata = %#v, want payload route metadata", promoter.metadata)
	}
	if len(rt.drainCalls) != 1 {
		t.Fatalf("drain calls = %#v, want one previous drain", rt.drainCalls)
	}
	if drainCall := rt.drainCalls[0]; drainCall.deploymentID != "dep-1" || drainCall.instanceID != "dep-1-r0" || drainCall.grace != 15*time.Second {
		t.Fatalf("drain call = %#v, want previous active with grace", drainCall)
	}
	if ops.transitions[len(ops.transitions)-1] != orailtypes.OperationSucceeded {
		t.Fatalf("transitions = %#v, want success terminal", ops.transitions)
	}
	drain := findActivationStep(out.Plan, ActivationStepDrainPrevious)
	if drain.Outcome != StepOutcomePlanned {
		t.Fatalf("drain step = %#v, want planned", drain)
	}
}

func TestExecutorDrainsEveryPriorReplicaAfterPromotion(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls:               &calls,
		pull:                verifiedPull(),
		target:              readyTarget(),
		deploymentInstances: []string{"dep-1-r0", "dep-1-r1", "dep-1-r2"},
	}
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{}}
	if _, err := executor.Execute(context.Background(), deployJob()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got, want := len(rt.drainCalls), 3; got != want {
		t.Fatalf("drain calls = %#v, want %d", rt.drainCalls, want)
	}
	for ordinal, call := range rt.drainCalls {
		if want := "dep-1-r" + strconv.Itoa(ordinal); call.instanceID != want {
			t.Fatalf("drain call %d instance = %q, want %q", ordinal, call.instanceID, want)
		}
	}
}

func TestExecutorKeepsCanonicalPrivateDeployActiveWhenPreviousDrainFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls:  &calls,
		pull:   verifiedPull(),
		target: readyTarget(),
		errs:   map[string]error{"drain": errors.New("old container still terminating")},
	}
	ops := &fakeOperationUpdater{}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route = &DeployJobRoute{Public: false}
	job.Payload, _ = json.Marshal(payload)
	executor := Executor{
		Runtime:            rt,
		Promoter:           &fakePromoter{calls: &calls},
		Operations:         ops,
		CanonicalCommitter: fakeCanonicalCommitter{},
	}
	out, err := executor.Execute(context.Background(), job)
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute() = %#v, %v; want active success despite prior-drain cleanup failure", out, err)
	}
	if ops.failed {
		t.Fatalf("operation failure = %q, want no failed active operation", ops.failReason)
	}
	if got := ops.transitions[len(ops.transitions)-1]; got != orailtypes.OperationSucceeded {
		t.Fatalf("transitions = %#v, want succeeded", ops.transitions)
	}
}

func TestExecutorRejectsCanonicalPublicDeployWithoutTransactionalPromoter(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	promoter := &fakePromoter{calls: &calls}
	executor := Executor{
		Runtime:            rt,
		Promoter:           promoter,
		Operations:         &fakeOperationUpdater{},
		CanonicalCommitter: fakeCanonicalCommitter{},
	}
	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil || out.Succeeded {
		t.Fatalf("Execute() = %#v, %v; want fail-closed non-transactional public route rejection", out, err)
	}
	if len(promoter.targets) != 0 {
		t.Fatalf("promoted targets = %#v, want none before canonical-safe route promotion", promoter.targets)
	}
	if got, want := calls, []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}

func TestExecutorCleansScheduledReplicasWhenLaterReplicaScheduleFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget(), scheduleErrors: []error{nil, errors.New("second replica schedule failed")}}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.Replicas, cfg.HealthKind = 3, serviceconfig.HealthNone
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{}, Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}}}
	if _, err := executor.Execute(context.Background(), deployJob()); err == nil {
		t.Fatal("Execute() error = nil, want schedule failure")
	}
	if got, want := len(rt.drainCalls), 2; got != want || rt.drainCalls[0].instanceID != "dep-2-r2" || rt.drainCalls[1].instanceID != "dep-2-r1" {
		t.Fatalf("drained candidates = %#v, want r2 then r1", rt.drainCalls)
	}
}

func TestExecutorCleansExtraReplicasWhenR0ScheduleFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget(), scheduleErrors: []error{nil, nil, errors.New("r0 schedule failed")}}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.Replicas, cfg.HealthKind = 3, serviceconfig.HealthNone
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{}, Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}}}
	if _, err := executor.Execute(context.Background(), deployJob()); err == nil {
		t.Fatal("Execute() error = nil, want r0 schedule failure")
	}
	if got, want := len(rt.drainCalls), 3; got != want || rt.drainCalls[0].instanceID != "dep-2-r0" || rt.drainCalls[1].instanceID != "dep-2-r2" || rt.drainCalls[2].instanceID != "dep-2-r1" {
		t.Fatalf("drained candidates = %#v, want r0, r2, then r1", rt.drainCalls)
	}
}

func TestExecutorAppliesDurableRuntimeConfigAndNonOverlapDrain(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.StartCommand = "bin/server --production"
	cfg.TargetPort = 9090
	cfg.RestartPolicy = serviceconfig.RestartOnFailure
	cfg.RestartRetries = 5
	cfg.Replicas = 3
	cfg.OverlapEnabled = false
	cfg.DrainGrace = 47 * time.Second
	cfg.HealthKind = serviceconfig.HealthNone // explicit background/no-port mode
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg,
			SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		}},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %+v", out)
	}
	wantCalls := []string{"drain", "pull-verify", "schedule", "start", "resolve-routing", "pull-verify", "schedule", "start", "resolve-routing", "pull-verify", "schedule", "start", "resolve-routing", "resolve-routing", "resolve-routing", "promote", "promote", "promote"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %+v, want %+v", calls, wantCalls)
	}
	if len(rt.drainCalls) != 1 || rt.drainCalls[0].grace != 47*time.Second ||
		rt.drainCalls[0].deploymentID != "dep-1" {
		t.Fatalf("drain calls = %+v", rt.drainCalls)
	}
	if len(rt.scheduleRequests) != 3 {
		t.Fatalf("schedule requests = %+v", rt.scheduleRequests)
	}
	got := rt.scheduleRequests[0]
	if got.StartCommand != cfg.StartCommand || got.TargetPort != 9090 ||
		got.RestartPolicy != serviceconfig.RestartOnFailure ||
		got.RestartMaxRetries != 5 || got.Replicas != 1 {
		t.Fatalf("scheduled runtime config = %+v", got)
	}
	if got.TargetPortInferred {
		t.Fatal("an operator-chosen target port must not be marked inferred")
	}
}

func TestExecutorMarksDefaultTargetPortAsInferred(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.HealthKind = serviceconfig.HealthNone
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg,
			SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		}},
	}
	if _, err := executor.Execute(context.Background(), deployJob()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(rt.scheduleRequests) == 0 {
		t.Fatal("expected a schedule request")
	}
	got := rt.scheduleRequests[0]
	if got.TargetPort != serviceconfig.DefaultTargetPort || !got.TargetPortInferred {
		t.Fatalf("schedule request port = %d inferred=%t, want default port marked inferred", got.TargetPort, got.TargetPortInferred)
	}
}

func TestExecutorActivatesCronWithoutSchedulingServiceContainer(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.RuntimeKind, cfg.CronSchedule = serviceconfig.RuntimeKindCron, "*/5 * * * *"
	cfg.StartCommand = "bin/job"
	cfg.RestartPolicy, cfg.HealthKind, cfg.Replicas = serviceconfig.RestartNo, serviceconfig.HealthNone, 1
	cron := &fakeCronActivator{}
	executor := Executor{
		Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{}, Cron: cron,
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg, SHA256: strings.Repeat("d", 64)}},
	}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route.Public = false
	job.Payload, _ = json.Marshal(payload)
	out, err := executor.Execute(context.Background(), job)
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute() = %+v, %v", out, err)
	}
	if len(rt.scheduleRequests) != 0 || len(calls) != 0 {
		t.Fatalf("runtime calls=%+v schedules=%+v, want no service runtime work", calls, rt.scheduleRequests)
	}
	if cron.candidate.DeploymentID != "dep-2" || cron.snapshot.SHA256 != strings.Repeat("d", 64) {
		t.Fatalf("cron activation = %+v %+v", cron.candidate, cron.snapshot)
	}
}

func TestExecutorAtomicallyCommitsCronActivationAndCanonicalDeployment(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.RuntimeKind, cfg.CronSchedule = serviceconfig.RuntimeKindCron, "*/5 * * * *"
	cfg.StartCommand = "bin/job"
	cfg.RestartPolicy, cfg.HealthKind, cfg.Replicas = serviceconfig.RestartNo, serviceconfig.HealthNone, 1
	cron := &fakeCronActivator{}
	instances := &fakeDeploymentInstanceStore{}
	committer := &fakeCronCanonicalCommitter{}
	executor := Executor{
		Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{},
		Cron: cron, Instances: instances, CanonicalCommitter: committer,
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route.Public = false
	job.Payload, _ = json.Marshal(payload)

	out, err := executor.Execute(context.Background(), job)
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute() = %+v, %v", out, err)
	}
	if committer.calls != 1 || committer.operationID != "op-1" {
		t.Fatalf("atomic committer = %+v, want one op-1 commit", committer)
	}
	if committer.activation.Candidate.DeploymentID != "dep-2" ||
		committer.activation.Snapshot.SHA256 != strings.Repeat("d", 64) {
		t.Fatalf("atomic activation = %+v", committer.activation)
	}
	if cron.candidate.DeploymentID != "" {
		t.Fatalf("legacy Cron.Activate was called with %+v", cron.candidate)
	}
	if len(instances.observed) != 0 || len(instances.ensured) != 0 {
		t.Fatalf("cron created service instances: %+v", instances)
	}
}

func TestExecutorFailsCronWhenAtomicCanonicalCommitFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.RuntimeKind, cfg.CronSchedule = serviceconfig.RuntimeKindCron, "*/5 * * * *"
	cfg.StartCommand = "bin/job"
	cfg.RestartPolicy, cfg.HealthKind, cfg.Replicas = serviceconfig.RestartNo, serviceconfig.HealthNone, 1
	ops := &fakeOperationUpdater{}
	cron := &fakeCronActivator{}
	committer := &fakeCronCanonicalCommitter{err: errors.New("promotion CAS moved")}
	executor := Executor{
		Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: ops,
		Cron: cron, CanonicalCommitter: committer,
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route.Public = false
	job.Payload, _ = json.Marshal(payload)

	out, err := executor.Execute(context.Background(), job)
	if err == nil || out.Succeeded || !ops.failed {
		t.Fatalf("Execute() = %+v, %v; want terminal activation failure", out, err)
	}
	if cron.candidate.DeploymentID != "" {
		t.Fatalf("legacy Cron.Activate was called with %+v", cron.candidate)
	}
	for _, state := range ops.transitions {
		if state == orailtypes.OperationSucceeded {
			t.Fatalf("operation transitions = %+v, must not succeed", ops.transitions)
		}
	}
}

func TestExecutorRunsPredeployWithResolvedSnapshotBeforeDrainAndSchedule(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.PredeployCommand = "bin/migrate up"
	cfg.OverlapEnabled = false
	cfg.HealthKind = serviceconfig.HealthNone
	variables := &versionProbeResolver{pinned: map[string]string{"DATABASE_URL": "sealed-runtime-value"}}
	store := newMemoryPredeployStore()
	runner := &recordingPredeployRunner{
		callLog: &calls,
		result:  runtime.OneOffResult{ContainerName: "predeploy", ContainerID: "ctr-pre", ExitCode: 0},
	}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg,
			SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		}},
		Variables: variables,
		Predeploy: &PredeployExecutor{
			Store: store, Runner: runner, Now: func() time.Time { return store.now },
		},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %+v", out)
	}
	wantCalls := []string{"predeploy", "drain", "pull-verify", "schedule", "start", "resolve-routing", "promote"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %+v, want %+v", calls, wantCalls)
	}
	if runner.calls != 1 || len(runner.requests) != 1 {
		t.Fatalf("predeploy calls=%d requests=%+v", runner.calls, runner.requests)
	}
	oneOff := runner.requests[0].ScheduleRequest
	if !reflect.DeepEqual(oneOff.Env, []string{"DATABASE_URL=sealed-runtime-value"}) ||
		len(oneOff.Mounts) != 0 || oneOff.RestartPolicy != serviceconfig.RestartNo {
		t.Fatalf("predeploy immutable runtime request = %+v", oneOff)
	}
}

func TestExecutorRejectsPublicBackgroundModeBeforeRuntimeEffects(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	job := deployJob()
	payload, _, err := DecodeDeployJobPayload(job.Payload)
	if err != nil {
		t.Fatal(err)
	}
	payload.Request.Mode = "background"
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = encoded
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: &fakeOperationUpdater{}}
	out, err := executor.Execute(context.Background(), job)
	if !errors.Is(err, ErrBackgroundPublicRoute) {
		t.Fatalf("Execute error = %v, want ErrBackgroundPublicRoute", err)
	}
	if out.FailureCode != failurecodes.ResourceValidationFailed {
		t.Fatalf("failure code = %q", out.FailureCode)
	}
	if len(calls) != 0 {
		t.Fatalf("runtime calls = %v, want none", calls)
	}
}

func TestExecutorResolvesImmutableSnapshotVariableVersion(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.HealthKind = serviceconfig.HealthNone
	variables := &versionProbeResolver{
		latest: map[string]string{"TOKEN": "latest-v2"},
		pinned: map[string]string{"TOKEN": "snapshot-v1"},
	}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg,
			VariableVersions: []serviceconfig.VariableVersionRef{{
				ID: "var-token", ServiceID: "api", Name: "TOKEN", Version: 1,
			}},
		}},
		Variables: variables,
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute = %+v, %v", out, err)
	}
	if variables.latestCalled {
		t.Fatal("executor resolved latest variables despite an immutable snapshot")
	}
	if len(variables.refs) != 1 || variables.refs[0].ID != "var-token" || variables.refs[0].Version != 1 {
		t.Fatalf("version refs = %+v", variables.refs)
	}
	env := envSliceToMap(rt.scheduleRequests[0].Env)
	if env["TOKEN"] != "snapshot-v1" {
		t.Fatalf("TOKEN = %q, want snapshot-v1", env["TOKEN"])
	}
}

func TestExecutorDefaultHealthUsesTCPOnConfiguredTargetPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)

	calls := []string{}
	target := readyTarget()
	target.ContainerIP = address.IP.String()
	// Leave the runtime hint empty: the durable target port is authoritative.
	target.InternalPort = 0
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: target}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.TargetPort = address.Port
	promoter := &fakePromoter{calls: &calls}
	executor := Executor{
		Runtime:       rt,
		Promoter:      promoter,
		Operations:    &fakeOperationUpdater{},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}},
		Readiness:     singleAttemptReadiness(),
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil || !out.Succeeded || len(promoter.targets) != 1 {
		t.Fatalf("Execute = %+v, %v; promoted=%d", out, err, len(promoter.targets))
	}
	if cfg.HealthKind != serviceconfig.HealthTCP {
		t.Fatalf("default health kind = %q, want tcp", cfg.HealthKind)
	}
}

func TestExecutorHTTPHealthRequiresExact200BeforePromotion(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantSuccess bool
	}{
		{name: "exact 200", status: http.StatusOK, wantSuccess: true},
		{name: "204 is not ready", status: http.StatusNoContent, wantSuccess: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requestedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPath = r.URL.Path
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			parsed, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			port, err := strconv.Atoi(parsed.Port())
			if err != nil {
				t.Fatal(err)
			}
			target := readyTarget()
			target.ContainerIP = parsed.Hostname()
			target.InternalPort = port
			calls := []string{}
			rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: target}
			ops := &fakeOperationUpdater{}
			promoter := &fakePromoter{calls: &calls}
			cfg := serviceconfig.Default("prj-1", "api", "prod")
			cfg.HealthKind = serviceconfig.HealthHTTP
			cfg.HealthPath = "/ready"
			executor := Executor{
				Runtime:       rt,
				Promoter:      promoter,
				Operations:    ops,
				Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}},
				Readiness:     singleAttemptReadiness(),
			}

			out, execErr := executor.Execute(context.Background(), deployJob())
			if test.wantSuccess {
				if execErr != nil || !out.Succeeded || len(promoter.targets) != 1 {
					t.Fatalf("Execute = %+v, %v; promoted=%d", out, execErr, len(promoter.targets))
				}
			} else {
				if execErr == nil || out.Succeeded || len(promoter.targets) != 0 ||
					!strings.Contains(ops.failReason, "status 200") {
					t.Fatalf("Execute = %+v, %v; ops=%+v promoted=%d", out, execErr, ops, len(promoter.targets))
				}
			}
			if requestedPath != "/ready" {
				t.Fatalf("requested path = %q", requestedPath)
			}
		})
	}
}

func TestExecutorLoadsDurableVolumeIntentIntoSchedule(t *testing.T) {
	ctx := context.Background()
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	store := volume.NewMemoryStore()
	vol, err := store.Create(ctx, volume.VolumeCreateInput{
		ProjectID: "prj-1", EnvironmentID: "prod", ServiceID: "api",
		Name: "data", MountPath: "/var/lib/api",
	})
	if err != nil {
		t.Fatalf("create volume: %v", err)
	}
	client := &deployVolumeClient{volumes: map[string]volume.DockerInfo{}}
	manager := volume.NewManager(store, volume.NewDockerDriver(client))
	if _, err := manager.EnsureForDeployment(ctx, volume.DeploymentAttachments{
		DeploymentID: "dep-1", InstanceID: "dep-1-r0", ProjectID: "prj-1",
		EnvironmentID: "prod", ServiceID: "api", Replicas: 1,
		Attachments: []volume.AttachmentInput{{
			VolumeID: vol.ID, MountPath: "/var/lib/api",
		}},
	}); err != nil {
		t.Fatalf("seed previous volume writer: %v", err)
	}
	executor := Executor{
		Runtime: rt, Promoter: &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{}, Volumes: manager,
	}

	out, err := executor.Execute(ctx, deployJob())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.Succeeded || len(rt.scheduleRequests) != 1 {
		t.Fatalf("out=%+v schedule=%+v", out, rt.scheduleRequests)
	}
	if len(calls) < 2 || calls[0] != "drain" || calls[1] != "pull-verify" {
		t.Fatalf("calls = %+v, want previous writer drained before candidate schedule", calls)
	}
	mounts := rt.scheduleRequests[0].Mounts
	if len(mounts) != 1 || mounts[0].VolumeID != vol.ID ||
		mounts[0].Source != volume.DockerVolumeName(vol.ID) ||
		mounts[0].Target != "/var/lib/api" || !mounts[0].IsVolume {
		t.Fatalf("schedule mounts = %+v, want durable volume intent", mounts)
	}
	attachments, _ := store.ListAttachments(ctx, vol.ID)
	statuses := map[string]volume.AttachmentStatus{}
	for _, attachment := range attachments {
		statuses[attachment.DeploymentID] = attachment.Status
	}
	if len(attachments) != 2 ||
		statuses["dep-1"] != volume.AttachmentDetached ||
		statuses["dep-2"] != volume.AttachmentActive {
		t.Fatalf("attachments = %+v, want previous detached and scheduler-proven candidate active", attachments)
	}
}

func TestExecutorSuccessEmitsOrderedStepTelemetry(t *testing.T) {
	calls := []string{}
	observer := &recordingDeployObserver{}
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Observer:   observer,
		DrainGrace: 15 * time.Second,
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %#v, want success", out)
	}
	want := []string{
		"pull-verify:started",
		"pull-verify:succeeded",
		"schedule:started",
		"schedule:succeeded",
		"start:started",
		"start:succeeded",
		"readiness-routing-check:started",
		"readiness-routing-check:succeeded",
		"promote:started",
		"promote:succeeded",
		"drain-previous:started",
		"drain-previous:succeeded",
		"succeeded:succeeded",
	}
	if got := deployEventKeys(observer.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry = %#v, want %#v", got, want)
	}
	for _, event := range observer.events {
		if event.OperationID != "op-1" || event.JobID != "job-1" || event.DeploymentID != "dep-2" || event.InstanceID != "dep-2-r0" {
			t.Fatalf("event identity = %#v, want operation/job/candidate ids", event)
		}
	}
	promote := findDeployEvent(observer.events, DeployStepPromote, DeployStepStateStarted)
	if promote.Route == nil {
		t.Fatalf("promote route event = nil, want route metadata")
	}
	if promote.Route.RouteName != "api" || promote.Route.RouteGroup != "apps" || promote.Route.Host != "api.example.test" || promote.Route.PathPrefix != "/v1" || !promote.Route.TLS {
		t.Fatalf("promote route = %#v, want payload route metadata", promote.Route)
	}
	if promote.Route.ContainerID != "ctr-2" || promote.Route.ContainerIP != "127.0.0.1" || !promote.Route.Ready {
		t.Fatalf("promote route target = %#v, want runtime target metadata", promote.Route)
	}
}

func TestExecutorHonorsCancelRequestBeforePromotion(t *testing.T) {
	calls := []string{}
	observer := &recordingDeployObserver{}
	ops := &fakeOperationUpdater{}
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
		Cancels:    fakeCancelChecker{requested: true},
		Observer:   observer,
		DrainGrace: 15 * time.Second,
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out.Succeeded || out.FailureCode != failurecodes.DeployCanceledBeforePromotion {
		t.Fatalf("outcome = %#v, want canceled before promotion", out)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want candidate cleanup without promote", calls)
	}
	if got := ops.transitions[len(ops.transitions)-1]; got != orailtypes.OperationCanceled {
		t.Fatalf("transitions = %#v, want terminal canceled", ops.transitions)
	}
	cancel := findDeployEvent(observer.events, DeployStepCanceled, DeployStepStateSucceeded)
	if cancel.FailureCode != failurecodes.DeployCanceledBeforePromotion || !strings.Contains(cancel.FailureReason, "before traffic promotion") {
		t.Fatalf("cancel event = %#v, want typed cancellation evidence", cancel)
	}
}

func TestExecutorPullFailureProtectsPrevious(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls: &calls,
		pull:  runtime.PullVerifyResult{Image: imageRef(), Verified: false, Trusted: false, Reason: "signature missing"},
	}
	ops := &fakeOperationUpdater{}
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: ops}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatal("Execute() error = nil, want pull failure")
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ImageSignatureRequired) {
		t.Fatalf("fail = %t/%q, want image signature required", ops.failed, ops.failCode)
	}
	wantCalls := []string{"pull-verify", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if !out.Plan.PreviousActiveProtected {
		t.Fatalf("plan = %#v, want previous active protected", out.Plan)
	}
	drain := findActivationStep(out.Plan, ActivationStepDrainPrevious)
	if drain.Outcome != StepOutcomeSkipped {
		t.Fatalf("drain step = %#v, want skipped", drain)
	}
}

func TestExecutorPullVerifyErrorPreservesTypedFailureCode(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls: &calls,
		pull:  runtime.PullVerifyResult{Image: imageRef(), Verified: false, Trusted: false, Reason: "no trust policy"},
		errs:  map[string]error{"pull-verify": failurecodes.New(failurecodes.ImageSignatureRequired, "no trust policy")},
	}
	ops := &fakeOperationUpdater{}
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: ops}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatal("Execute() error = nil, want pull-verify failure")
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ImageSignatureRequired) {
		t.Fatalf("fail = %t/%q, want image signature required", ops.failed, ops.failCode)
	}
	if out.FailureCode != failurecodes.ImageSignatureRequired {
		t.Fatalf("outcome = %#v, want typed signature-required failure", out)
	}
}

func TestExecutorPromoteFailureFailsOperationAndProtectsPrevious(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	promoteErr := errors.New("router rejected candidate")
	promoter := &fakePromoter{calls: &calls, err: promoteErr}
	executor := Executor{Runtime: rt, Promoter: promoter, Operations: ops, DrainGrace: 15 * time.Second}

	out, err := executor.Execute(context.Background(), deployJob())
	if !errors.Is(err, promoteErr) {
		t.Fatalf("Execute() error = %v, want promote error", err)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "promote", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if len(rt.drainCalls) != 1 || rt.drainCalls[0].instanceID != "dep-2-r0" {
		t.Fatalf("drain calls = %#v, want candidate cleanup after promote failure", rt.drainCalls)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ReconcileStepFailed) {
		t.Fatalf("fail = %t/%q, want reconcile step failed", ops.failed, ops.failCode)
	}
	if !strings.Contains(ops.failReason, "promote failed: router rejected candidate") {
		t.Fatalf("fail reason = %q, want promote failure reason", ops.failReason)
	}
	if out.Succeeded || out.FailureCode != failurecodes.ReconcileStepFailed || out.FailureReason != ops.failReason {
		t.Fatalf("outcome = %#v ops = %#v, want failed reconcile outcome", out, ops)
	}
	if !out.Plan.PreviousActiveProtected {
		t.Fatalf("plan = %#v, want previous active protected", out.Plan)
	}
	drain := findActivationStep(out.Plan, ActivationStepDrainPrevious)
	if drain.Outcome != StepOutcomeSkipped {
		t.Fatalf("drain step = %#v, want skipped", drain)
	}
}

func TestExecutorNoopPromoterRejectsPublicRoute(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	executor := Executor{Runtime: rt, Promoter: NoopRoutePromoter{}, Operations: ops, DrainGrace: 15 * time.Second}

	out, err := executor.Execute(context.Background(), deployJob())
	if !errors.Is(err, ErrRoutePromotionRequired) {
		t.Fatalf("Execute() error = %v, want ErrRoutePromotionRequired", err)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if len(rt.drainCalls) != 1 || rt.drainCalls[0].instanceID != "dep-2-r0" {
		t.Fatalf("drain calls = %#v, want candidate cleanup after missing public route promoter", rt.drainCalls)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.RoutePublicationFailed) {
		t.Fatalf("fail = %t/%q, want route publication failed", ops.failed, ops.failCode)
	}
	if out.Succeeded || out.FailureCode != failurecodes.RoutePublicationFailed {
		t.Fatalf("outcome = %#v, want failed route publication outcome", out)
	}
}

func TestExecutorPrivateDeploySkipsConfiguredPublisherAndActivates(t *testing.T) {
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route = nil
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	job.Payload = encoded

	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls, err: errors.New("must not be called")}
	executor := Executor{
		Runtime:    rt,
		Promoter:   promoter,
		Operations: ops,
		DrainGrace: 15 * time.Second,
	}

	out, err := executor.Execute(context.Background(), job)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !out.Succeeded || ops.failed {
		t.Fatalf("outcome = %#v ops = %#v, want private activation success", out, ops)
	}
	if len(promoter.targets) != 0 {
		t.Fatalf("publisher targets = %#v, want none", promoter.targets)
	}
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if got := ops.transitions[len(ops.transitions)-1]; got != orailtypes.OperationSucceeded {
		t.Fatalf("transitions = %#v, want succeeded terminal state", ops.transitions)
	}
	promote := findActivationStep(out.Plan, ActivationStepPromote)
	if promote.Outcome != StepOutcomePlanned {
		t.Fatalf("promote step = %#v, want successful private activation gate", promote)
	}
}

func TestExecutorRefreshesLateDeploymentRouteBindingBeforePromotion(t *testing.T) {
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Route = nil
	job.Payload, _ = json.Marshal(payload)

	calls := []string{}
	promoter := &fakeTransactionalPromoter{boundPublic: true}
	routeIntent := &fakeRouteIntentCoordinator{
		err: errors.New("exact-bound generated route must not be reconstructed"),
	}
	executor := Executor{
		Runtime: &fakeActivationRuntime{
			calls: &calls, pull: verifiedPull(), target: readyTarget(),
		},
		Promoter:           promoter,
		RouteIntent:        routeIntent,
		Operations:         &fakeOperationUpdater{},
		CanonicalCommitter: fakeCanonicalCommitter{},
		DrainGrace:         15 * time.Second,
	}
	out, err := executor.Execute(context.Background(), job)
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute() = %#v, %v; want bound-route activation", out, err)
	}
	if promoter.bindingReads < 2 {
		t.Fatalf(
			"deployment route binding refreshes = %d, want admission and final refresh",
			promoter.bindingReads,
		)
	}
	if len(promoter.targets) != 1 {
		t.Fatalf("transactional promotion targets = %#v, want one", promoter.targets)
	}
	if routeIntent.calls != 0 {
		t.Fatalf("generated route reconstruction calls = %d, want zero", routeIntent.calls)
	}
}

func TestExecutorRoutePublisherFailureUsesRoutePublicationCode(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	ops := &fakeOperationUpdater{}
	promoteErr := errors.Join(ErrRoutePromotionPublishFailed, errors.New("write dynamic config"))
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls, err: promoteErr}, Operations: ops}

	out, err := executor.Execute(context.Background(), deployJob())
	if !errors.Is(err, ErrRoutePromotionPublishFailed) {
		t.Fatalf("Execute() error = %v, want route promotion publish failure", err)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.RoutePublicationFailed) {
		t.Fatalf("fail = %t/%q, want route publication failed", ops.failed, ops.failCode)
	}
	if out.FailureCode != failurecodes.RoutePublicationFailed || !strings.Contains(out.FailureReason, "write dynamic config") {
		t.Fatalf("outcome = %#v, want route publication failure reason", out)
	}
}

func TestExecutorRuntimeUnsupportedOperationUsesRuntimeCode(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls: &calls,
		pull:  verifiedPull(),
		errs: map[string]error{
			"schedule": runtime.UnsupportedOperationError{
				Adapter:   "test",
				Operation: "Schedule",
				Reason:    "scheduling disabled",
			},
		},
	}
	ops := &fakeOperationUpdater{}
	executor := Executor{Runtime: rt, Promoter: &fakePromoter{calls: &calls}, Operations: ops}

	out, err := executor.Execute(context.Background(), deployJob())
	if !errors.Is(err, runtime.ErrUnsupportedOperation) {
		t.Fatalf("Execute() error = %v, want runtime unsupported operation", err)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.RuntimeUnsupportedOperation) {
		t.Fatalf("fail = %t/%q, want runtime unsupported operation", ops.failed, ops.failCode)
	}
	if out.FailureCode != failurecodes.RuntimeUnsupportedOperation || !strings.Contains(out.FailureReason, "scheduling disabled") {
		t.Fatalf("outcome = %#v, want runtime unsupported operation reason", out)
	}
}

func TestExecutorPromoteFailureEmitsFailedStepTelemetry(t *testing.T) {
	calls := []string{}
	observer := &recordingDeployObserver{}
	promoteErr := errors.New("router rejected candidate")
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls, err: promoteErr},
		Operations: &fakeOperationUpdater{},
		Observer:   observer,
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if !errors.Is(err, promoteErr) {
		t.Fatalf("Execute() error = %v, want promote error", err)
	}
	if out.Succeeded {
		t.Fatalf("outcome = %#v, want failure", out)
	}
	want := []string{
		"pull-verify:started",
		"pull-verify:succeeded",
		"schedule:started",
		"schedule:succeeded",
		"start:started",
		"start:succeeded",
		"readiness-routing-check:started",
		"readiness-routing-check:succeeded",
		"promote:started",
		"promote:failed",
		"failed:failed",
	}
	if got := deployEventKeys(observer.events); !reflect.DeepEqual(got, want) {
		t.Fatalf("telemetry = %#v, want %#v", got, want)
	}
	promote := findDeployEvent(observer.events, DeployStepPromote, DeployStepStateFailed)
	if promote.FailureCode != failurecodes.ReconcileStepFailed || promote.FailureReason != "promote failed: router rejected candidate" {
		t.Fatalf("promote failure = %#v, want reconcile failure with reason", promote)
	}
	if promote.Route == nil || promote.Route.RouteName != "api" || promote.Route.ContainerID != "ctr-2" {
		t.Fatalf("promote route = %#v, want route and target metadata", promote.Route)
	}
	terminal := observer.events[len(observer.events)-1]
	if terminal.Step != DeployStepFailed || terminal.FailureCode != failurecodes.ReconcileStepFailed || terminal.FailureReason != promote.FailureReason {
		t.Fatalf("terminal event = %#v, want failed reconcile terminal", terminal)
	}
}

func TestExecutorNilObserverIsSafe(t *testing.T) {
	calls := []string{}
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %#v, want success with nil observer", out)
	}
}

func TestExecutorReadinessRoutingFailureDoesNotPromoteOrDrainPrevious(t *testing.T) {
	tests := []struct {
		name       string
		target     runtime.RoutingTarget
		wantReason string
	}{
		{
			name:       "runtime and target not ready",
			target:     runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: false},
			wantReason: string(ActivationReadinessRoutingRuntimeNotReady),
		},
		{
			name:       "routing target missing",
			target:     runtime.RoutingTarget{Ready: true},
			wantReason: string(ActivationReadinessRoutingTargetMissing),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := []string{}
			rt := &fakeActivationRuntime{
				calls:  &calls,
				pull:   verifiedPull(),
				target: tt.target,
			}
			ops := &fakeOperationUpdater{}
			promoter := &fakePromoter{calls: &calls}
			executor := Executor{Runtime: rt, Promoter: promoter, Operations: ops, Readiness: singleAttemptReadiness()}

			out, err := executor.Execute(context.Background(), deployJob())
			if err == nil {
				t.Fatal("Execute() error = nil, want readiness/routing failure")
			}
			wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "drain"}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
			}
			if len(promoter.targets) != 0 {
				t.Fatalf("promoted targets = %#v, want none", promoter.targets)
			}
			if len(rt.drainCalls) != 1 || rt.drainCalls[0].instanceID != "dep-2-r0" {
				t.Fatalf("drain calls = %#v, want candidate cleanup", rt.drainCalls)
			}
			if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ReconcileStepFailed) || out.Succeeded {
				t.Fatalf("outcome = %#v ops = %#v, want failed reconcile", out, ops)
			}
			if !strings.Contains(ops.failReason, tt.wantReason) || out.FailureReason != ops.failReason {
				t.Fatalf("failure reason outcome=%q ops=%q, want %q", out.FailureReason, ops.failReason, tt.wantReason)
			}
			if !out.Plan.PreviousActiveProtected {
				t.Fatalf("plan = %#v, want previous active protected", out.Plan)
			}
			promote := findActivationStep(out.Plan, ActivationStepPromote)
			if promote.Outcome != StepOutcomeSkipped && promote.Outcome != StepOutcomeBlocked {
				t.Fatalf("promote step = %#v, want blocked or skipped", promote)
			}
			drain := findActivationStep(out.Plan, ActivationStepDrainPrevious)
			if drain.Outcome != StepOutcomeSkipped {
				t.Fatalf("drain step = %#v, want skipped", drain)
			}
		})
	}
}

func TestExecutorMalformedPayloadFailsBeforeRuntime(t *testing.T) {
	calls := []string{}
	ops := &fakeOperationUpdater{}
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
	}

	out, err := executor.Execute(context.Background(), queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1", Payload: []byte(`{"schema_version":`)})
	if !errors.Is(err, ErrDeployJobMalformed) {
		t.Fatalf("Execute() error = %v, want ErrDeployJobMalformed", err)
	}
	if out.FailureCode != failurecodes.ResourceValidationFailed {
		t.Fatalf("failure code = %q, want resource validation", out.FailureCode)
	}
	if len(calls) != 0 || len(ops.transitions) != 0 || ops.failed {
		t.Fatalf("calls=%#v ops=%#v, want no side effects", calls, ops)
	}
}

func TestExecutorRejectsSourceRefPayloadBeforeRuntime(t *testing.T) {
	calls := []string{}
	ops := &fakeOperationUpdater{}
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-1",
		ServiceID:     "api",
		Request: DeployJobRequest{
			SourceRef: "refs/heads/main",
			Image:     imageRef(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor := Executor{
		Runtime:    &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
	}

	out, err := executor.Execute(context.Background(), queue.Job{ID: "job-1", Queue: queue.Deploy, LeaseID: "worker-1", Payload: payload})
	if !errors.Is(err, ErrSourceBuildNotWired) {
		t.Fatalf("Execute() error = %v, want ErrSourceBuildNotWired", err)
	}
	if out.OperationID != "op-1" || out.FailureCode != failurecodes.DeploySourceRefUnsupported || !strings.Contains(out.FailureReason, "immutable sha256 candidate.image") {
		t.Fatalf("outcome = %#v, want source build validation failure", out)
	}
	if len(calls) != 0 || len(ops.transitions) != 0 || !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.DeploySourceRefUnsupported) {
		t.Fatalf("calls=%#v ops=%#v, want operation failed without runtime side effects", calls, ops)
	}
}

func TestDecodeDeployJobPayloadAllowsTemplateSourceMetadataWithPinnedImage(t *testing.T) {
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-template",
		ServiceID:     "postgres",
		EnvironmentID: "env_prod",
		Request: DeployJobRequest{
			Mode:      "template",
			SourceRef: "template://postgres-16",
			Image:     databaseTemplateVerificationSpecs[officialPostgresTemplateRef].ExpectedImage,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	job, schedule, err := DecodeDeployJobPayload(payload)
	if err != nil {
		t.Fatalf("DecodeDeployJobPayload() error = %v", err)
	}
	if job.Request.SourceRef != "template://postgres-16" || job.Request.Mode != "template" ||
		schedule.Image != databaseTemplateVerificationSpecs[officialPostgresTemplateRef].ExpectedImage {
		t.Fatalf("job=%#v schedule=%#v, want template metadata retained with digest image", job.Request, schedule)
	}
}

func TestDecodeDeployJobPayloadRejectsOfficialTemplateImageDriftBeforeRuntime(t *testing.T) {
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-template-drift",
		ServiceID:     "redis",
		EnvironmentID: "env_prod",
		Request: DeployJobRequest{
			Mode:      "template",
			SourceRef: officialRedisTemplateRef,
			Image:     imageRef(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = DecodeDeployJobPayload(payload)
	if !errors.Is(err, ErrDeployJobMalformed) ||
		!strings.Contains(err.Error(), "does not match the exact official image") {
		t.Fatalf("DecodeDeployJobPayload() error=%v, want early image drift failure", err)
	}
}

func TestExecutorCleansFailedTemplateSmokeContainers(t *testing.T) {
	for _, test := range []struct {
		name     string
		evidence templatepkg.VerificationEvidence
	}{
		{name: "write", evidence: templatepkg.VerificationEvidence{WriteProbeContainerID: "ctr-write"}},
		{name: "read", evidence: templatepkg.VerificationEvidence{
			WriteProbeContainerID: "ctr-write", ReadProbeContainerID: "ctr-read",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := []string{}
			rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget(), errs: map[string]error{}}
			verifier := &fakeTemplateVerifier{
				evidence: test.evidence,
				err:      errors.New("template probe exited with status 9"),
			}
			runs := &fakeTemplateRunRecorder{}
			job := deployJob()
			var payload DeployJobPayload
			if err := json.Unmarshal(job.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			configureOfficialPostgresTemplate(&payload)
			payload.Route = &DeployJobRoute{Name: "postgres", Public: false}
			payload.PreviousActive = ActiveInstanceRef{}
			job.Payload, _ = json.Marshal(payload)
			executor := Executor{
				Runtime: rt, Promoter: &fakePromoter{calls: &calls},
				Operations:       &fakeOperationUpdater{},
				TemplateVerifier: verifier, TemplateRuns: runs,
			}
			out, err := executor.Execute(context.Background(), job)
			if err == nil || out.Succeeded {
				t.Fatalf("Execute=%+v, %v; want template smoke failure", out, err)
			}
			if verifier.cleanupCalls != 1 || !reflect.DeepEqual(verifier.cleaned, test.evidence) {
				t.Fatalf("cleanup calls=%d evidence=%+v, want %+v", verifier.cleanupCalls, verifier.cleaned, test.evidence)
			}
			if runs.failed == 0 || runs.succeeded != 0 {
				t.Fatalf("run recorder=%+v", runs)
			}
		})
	}
}

func TestExecutorDoesNotSucceedTemplateRunWhenCanonicalCommitFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget(), errs: map[string]error{}}
	evidence := templatepkg.VerificationEvidence{
		WriteProbeContainerID: "ctr-write",
		ReadProbeContainerID:  "ctr-read",
		MarkerSHA256:          strings.Repeat("a", 64),
	}
	verifier := &fakeTemplateVerifier{evidence: evidence}
	runs := &fakeTemplateRunRecorder{}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	configureOfficialPostgresTemplate(&payload)
	payload.Route = &DeployJobRoute{Name: "postgres", Public: false}
	payload.PreviousActive = ActiveInstanceRef{}
	job.Payload, _ = json.Marshal(payload)

	executor := Executor{
		Runtime:          rt,
		Promoter:         &fakePromoter{calls: &calls},
		Operations:       &fakeOperationUpdater{},
		TemplateVerifier: verifier,
		TemplateRuns:     runs,
		CanonicalCommitter: fakeCanonicalCommitter{
			err: errors.New("canonical deployment compare-and-swap lost"),
		},
	}
	out, err := executor.Execute(context.Background(), job)
	if err == nil || out.Succeeded {
		t.Fatalf("Execute=%+v, %v; want canonical commit failure", out, err)
	}
	if runs.running != 1 || runs.verified != 1 || runs.succeeded != 0 || runs.failed == 0 {
		t.Fatalf("run recorder=%+v, want verified then failed without success", runs)
	}
	if verifier.cleanupCalls != 1 || !reflect.DeepEqual(verifier.cleaned, evidence) {
		t.Fatalf("cleanup calls=%d evidence=%+v, want %+v", verifier.cleanupCalls, verifier.cleaned, evidence)
	}
}

func TestExecutorCleansTemplateSmokeWhenEvidencePersistenceFails(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget(), errs: map[string]error{}}
	evidence := templatepkg.VerificationEvidence{
		WriteProbeContainerID: "ctr-write",
		ReadProbeContainerID:  "ctr-read",
		MarkerSHA256:          strings.Repeat("b", 64),
	}
	verifier := &fakeTemplateVerifier{evidence: evidence}
	runs := &fakeTemplateRunRecorder{verifiedErr: errors.New("database unavailable")}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	configureOfficialPostgresTemplate(&payload)
	payload.Route = &DeployJobRoute{Name: "postgres", Public: false}
	payload.PreviousActive = ActiveInstanceRef{}
	job.Payload, _ = json.Marshal(payload)

	executor := Executor{
		Runtime: rt, Promoter: &fakePromoter{calls: &calls},
		Operations:       &fakeOperationUpdater{},
		TemplateVerifier: verifier,
		TemplateRuns:     runs,
	}
	out, err := executor.Execute(context.Background(), job)
	if err == nil || out.Succeeded {
		t.Fatalf("Execute=%+v, %v; want evidence persistence failure", out, err)
	}
	if verifier.cleanupCalls != 1 || !reflect.DeepEqual(verifier.cleaned, evidence) {
		t.Fatalf("cleanup calls=%d evidence=%+v, want %+v", verifier.cleanupCalls, verifier.cleaned, evidence)
	}
	if runs.succeeded != 0 || runs.failed == 0 {
		t.Fatalf("run recorder=%+v, want failed without success", runs)
	}
}

func TestExecutorPersistsAndPromotesTemplateReplacementTarget(t *testing.T) {
	calls := []string{}
	oldTarget := readyTarget()
	replacementTarget := oldTarget
	replacementTarget.ContainerID = "ctr-replacement"
	replacementTarget.ContainerIP = "10.44.0.19"
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: oldTarget, errs: map[string]error{}}
	verifier := &fakeTemplateVerifier{
		evidence: templatepkg.VerificationEvidence{
			WriteProbeContainerID: "ctr-write",
			ReadProbeContainerID:  "ctr-read",
			MarkerSHA256:          strings.Repeat("c", 64),
		},
		afterVerify: func() { rt.target = replacementTarget },
	}
	runs := &fakeTemplateRunRecorder{}
	instances := &fakeDeploymentInstanceStore{}
	promoter := &fakeTransactionalPromoter{}
	job := deployJob()
	var payload DeployJobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	configureOfficialPostgresTemplate(&payload)
	payload.PreviousActive = ActiveInstanceRef{}
	job.Payload, _ = json.Marshal(payload)

	executor := Executor{
		Runtime: rt, Promoter: promoter,
		Operations:         &fakeOperationUpdater{},
		Instances:          instances,
		TemplateVerifier:   verifier,
		TemplateRuns:       runs,
		CanonicalCommitter: fakeCanonicalCommitter{},
	}
	out, err := executor.Execute(context.Background(), job)
	if err != nil || !out.Succeeded {
		t.Fatalf("Execute=%+v, %v; want success", out, err)
	}
	if len(instances.observed) < 2 || !reflect.DeepEqual(instances.observed[len(instances.observed)-1], replacementTarget) {
		t.Fatalf("observed=%+v, want replacement target persisted last", instances.observed)
	}
	if len(promoter.targets) != 1 || !reflect.DeepEqual(promoter.targets[0], replacementTarget) {
		t.Fatalf("promoted=%+v, want replacement target %+v", promoter.targets, replacementTarget)
	}
	if runs.verified != 1 || runs.cleanup != 1 || runs.failed != 0 {
		t.Fatalf("run recorder=%+v", runs)
	}
}

func TestExecutorFailsClosedWhenInstanceEvidenceCannotBePersisted(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()}
	instances := &fakeDeploymentInstanceStore{err: errors.New("instance evidence write failed")}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	executor := Executor{
		Runtime: rt, Promoter: promoter, Operations: ops, Instances: instances,
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil || out.Succeeded {
		t.Fatalf("Execute() = %+v, %v; want evidence persistence failure", out, err)
	}
	if !ops.failed || len(promoter.targets) != 0 {
		t.Fatalf("ops=%+v promoted=%+v; candidate must not be promoted", ops, promoter.targets)
	}
	if len(instances.observed) != 1 {
		t.Fatalf("observed=%+v, want one failed evidence write", instances.observed)
	}
}

func TestDecodeDeployJobPayloadRequiresEnvironmentBeforeRuntime(t *testing.T) {
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-no-environment",
		ServiceID:     "api",
		ProjectID:     "prj-1",
		Request:       DeployJobRequest{Image: imageRef()},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = DecodeDeployJobPayload(payload)
	if !errors.Is(err, ErrDeployJobMalformed) || !strings.Contains(err.Error(), "environment_id is required") {
		t.Fatalf("DecodeDeployJobPayload() error = %v, want missing environment failure", err)
	}
}

func TestExecutorRejectsNonTemplateSourceRefEvenWhenModeTemplate(t *testing.T) {
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-1",
		ServiceID:     "api",
		Request: DeployJobRequest{
			Mode:      "template",
			SourceRef: "refs/heads/main",
			Image:     imageRef(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = DecodeDeployJobPayload(payload)
	if !errors.Is(err, ErrSourceBuildNotWired) {
		t.Fatalf("DecodeDeployJobPayload() error = %v, want ErrSourceBuildNotWired", err)
	}
}

func TestExecutorReadinessPollAllowsSlowThenReadyCandidate(t *testing.T) {
	calls := []string{}
	notReady := runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: false}
	rt := &fakeActivationRuntime{
		calls: &calls,
		pull:  verifiedPull(),
		// Schedule consumes the first target (not ready), the first retry the
		// second (still starting), and the third resolve reports ready.
		targetSequence: []runtime.RoutingTarget{notReady, notReady, readyTarget()},
		target:         readyTarget(),
	}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   promoter,
		Operations: ops,
		DrainGrace: 15 * time.Second,
		Readiness:  ReadinessConfig{Timeout: 30 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !out.Succeeded || ops.failed {
		t.Fatalf("outcome = %#v ops = %#v, want success after slow-start", out, ops)
	}
	// Three resolve-routing calls: one from Schedule, two retries; the third
	// resolve returns ready so promotion proceeds.
	wantCalls := []string{"pull-verify", "schedule", "start", "resolve-routing", "resolve-routing", "resolve-routing", "promote", "drain"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	if len(promoter.targets) != 1 || !promoter.targets[0].Ready {
		t.Fatalf("promoted targets = %#v, want ready target after poll", promoter.targets)
	}
	if clock.sleeps != 2 {
		t.Fatalf("readiness sleeps = %d, want 2 retry backoffs", clock.sleeps)
	}
}

func TestExecutorReadinessPollTimesOutNeverReadyWithoutPromotion(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls:  &calls,
		pull:   verifiedPull(),
		target: runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", Ready: false},
	}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   promoter,
		Operations: ops,
		Readiness:  ReadinessConfig{Timeout: 3 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatal("Execute() error = nil, want readiness timeout failure")
	}
	if len(promoter.targets) != 0 {
		t.Fatalf("promoted targets = %#v, want none on timeout", promoter.targets)
	}
	if len(rt.drainCalls) != 1 || rt.drainCalls[0].instanceID != "dep-2-r0" {
		t.Fatalf("drain calls = %#v, want candidate cleanup on timeout", rt.drainCalls)
	}
	if !ops.failed || ops.failCode != orailtypes.FailureCode(failurecodes.ReconcileStepFailed) || out.Succeeded {
		t.Fatalf("outcome = %#v ops = %#v, want failed reconcile on timeout", out, ops)
	}
	if !strings.Contains(ops.failReason, string(ActivationReadinessRoutingRuntimeNotReady)) {
		t.Fatalf("fail reason = %q, want runtime-not-ready block", ops.failReason)
	}
	if !out.Plan.PreviousActiveProtected {
		t.Fatalf("plan = %#v, want previous active protected", out.Plan)
	}
	// Poll retried until the deadline elapsed rather than failing on the first
	// inspect: one resolve from Schedule plus retries.
	resolveCount := 0
	for _, call := range calls {
		if call == "resolve-routing" {
			resolveCount++
		}
	}
	if resolveCount < 2 {
		t.Fatalf("resolve-routing calls = %d, want the poll loop to retry before timing out", resolveCount)
	}
}

func TestExecutorReadinessPollHonorsCancelBeforeTimeout(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{
		calls:  &calls,
		pull:   verifiedPull(),
		target: runtime.RoutingTarget{DeploymentID: "dep-3", InstanceID: "dep-3-r0", ContainerID: "ctr-3", Ready: false},
	}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:  rt,
		Promoter: promoter,
		// A generous timeout: if the poll ignored the cancel it would burn many
		// backoff sleeps before failing. The cancel must abort the wait first.
		Readiness:  ReadinessConfig{Timeout: 30 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
		Operations: ops,
		Cancels:    fakeCancelChecker{requested: true},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out.Succeeded || out.FailureCode != failurecodes.DeployCanceledBeforePromotion {
		t.Fatalf("outcome = %#v, want canceled-before-promotion, not a readiness timeout", out)
	}
	if len(promoter.targets) != 0 {
		t.Fatalf("promoted targets = %#v, want none after cancel", promoter.targets)
	}
	if clock.sleeps != 0 {
		t.Fatalf("readiness sleeps = %d, want the cancel to abort the wait before any backoff", clock.sleeps)
	}
}

// fakeReadinessClock is a deterministic clock for the readiness poll loop: Now
// advances only when Sleep is called, so tests exercise retry/backoff without
// real time.
type fakeReadinessClock struct {
	now    time.Time
	sleeps int
}

func (c *fakeReadinessClock) Now() time.Time { return c.now }

func (c *fakeReadinessClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps++
	c.now = c.now.Add(d)
	return ctx.Err()
}

// singleAttemptReadiness returns a ReadinessConfig whose clock reports the
// deadline as already elapsed on the remaining-time check, so readiness.Wait
// evaluates the probe exactly once and fails fast without sleeping or
// re-resolving. Not-ready tests that assert the one-shot failure contract use
// it to avoid waiting on the real clock.
func singleAttemptReadiness() ReadinessConfig {
	base := time.Unix(0, 0)
	nowCalls := 0
	return ReadinessConfig{
		Timeout:  time.Second,
		Interval: time.Second,
		Now: func() time.Time {
			nowCalls++
			if nowCalls <= 1 {
				return base
			}
			return base.Add(time.Hour)
		},
		Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	}
}

func deployJob() queue.Job {
	payload, err := json.Marshal(DeployJobPayload{
		SchemaVersion: DeployJobPayloadSchema,
		OperationID:   "op-1",
		ServiceID:     "api",
		ProjectID:     "prj-1",
		EnvironmentID: "prod",
		Request:       DeployJobRequest{Image: imageRef()},
		Candidate: DeployJobCandidate{
			DeploymentID: "dep-2",
			InstanceID:   "dep-2-r0",
		},
		Route:          &DeployJobRoute{Name: "api", Group: "apps", Host: "api.example.test", Path: "/v1", Public: true, TLS: true},
		PreviousActive: ActiveInstanceRef{DeploymentID: "dep-1", InstanceID: "dep-1-r0"},
	})
	if err != nil {
		panic(err)
	}
	return queue.Job{ID: "job-1", Queue: queue.Deploy, Attempt: 1, LeaseID: "worker-1", Payload: payload}
}

func configureOfficialPostgresTemplate(payload *DeployJobPayload) {
	image := databaseTemplateVerificationSpecs[officialPostgresTemplateRef].ExpectedImage
	payload.Request.SourceRef = officialPostgresTemplateRef
	payload.Request.Mode = "template"
	payload.Request.Image = image
	payload.Candidate.Image = image
}

func imageRef() string {
	return "registry.example.com/api@sha256:0000000000000000000000000000000000000000000000000000000000000000"
}

func verifiedPull() runtime.PullVerifyResult {
	return runtime.PullVerifyResult{Image: imageRef(), Verified: true, Trusted: true}
}

func readyTarget() runtime.RoutingTarget {
	return runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", ContainerIP: "127.0.0.1", Ready: true}
}

type fakeActivationRuntime struct {
	calls               *[]string
	pull                runtime.PullVerifyResult
	target              runtime.RoutingTarget
	targetSequence      []runtime.RoutingTarget
	errs                map[string]error
	pullImages          []string
	scheduleRequests    []runtime.ScheduleRequest
	startCalls          []fakeInstanceCall
	resolveCalls        []fakeInstanceCall
	drainCalls          []fakeDrainCall
	deploymentInstances []string
	scheduleErrors      []error
}

func (r *fakeActivationRuntime) DeploymentInstanceIDs(_ context.Context, _ string) ([]string, error) {
	return append([]string(nil), r.deploymentInstances...), nil
}

type fakeConfigResolver struct {
	snapshot serviceconfig.Snapshot
	err      error
}

type fakeCronActivator struct {
	candidate runtime.ScheduleRequest
	snapshot  serviceconfig.Snapshot
}

func (f *fakeCronActivator) Activate(_ context.Context, candidate runtime.ScheduleRequest, snapshot serviceconfig.Snapshot) error {
	f.candidate, f.snapshot = candidate, snapshot
	return nil
}

type versionProbeResolver struct {
	latest       map[string]string
	pinned       map[string]string
	refs         []varrefs.VariableVersionRef
	latestCalled bool
}

func (r *versionProbeResolver) Resolve(context.Context, string, string, string) (map[string]string, error) {
	r.latestCalled = true
	return r.latest, nil
}

func (r *versionProbeResolver) ResolveAtVersions(_ context.Context, _, _, _ string, refs []varrefs.VariableVersionRef) (map[string]string, error) {
	r.refs = append([]varrefs.VariableVersionRef(nil), refs...)
	return r.pinned, nil
}

func (r fakeConfigResolver) ResolveDeployment(_ context.Context, deploymentID, projectID, environmentID, serviceID string) (serviceconfig.Snapshot, error) {
	if r.err != nil {
		return serviceconfig.Snapshot{}, r.err
	}
	out := r.snapshot
	out.DeploymentID = deploymentID
	out.ProjectID = projectID
	out.EnvironmentID = environmentID
	out.ServiceID = serviceID
	return out, nil
}

type deployVolumeClient struct {
	volumes map[string]volume.DockerInfo
}

func (c *deployVolumeClient) VolumeCreate(_ context.Context, name, driver string, labels map[string]string, _ map[string]string) (volume.DockerInfo, error) {
	info := volume.DockerInfo{Name: name, Driver: driver, Labels: labels}
	c.volumes[name] = info
	return info, nil
}

func (c *deployVolumeClient) VolumeInspect(_ context.Context, name string) (volume.DockerInfo, error) {
	info, ok := c.volumes[name]
	if !ok {
		return volume.DockerInfo{}, volume.ErrVolumeMissing
	}
	return info, nil
}

func (c *deployVolumeClient) VolumeRemove(_ context.Context, name string, _ bool) error {
	delete(c.volumes, name)
	return nil
}

func (r *fakeActivationRuntime) PullVerify(_ context.Context, image string) (runtime.PullVerifyResult, error) {
	r.record("pull-verify")
	r.pullImages = append(r.pullImages, image)
	return r.pull, r.errs["pull-verify"]
}

func (r *fakeActivationRuntime) Schedule(_ context.Context, req runtime.ScheduleRequest) (runtime.InstanceStatus, error) {
	r.record("schedule")
	r.scheduleRequests = append(r.scheduleRequests, req)
	if len(r.scheduleErrors) > 0 {
		err := r.scheduleErrors[0]
		r.scheduleErrors = r.scheduleErrors[1:]
		return runtime.InstanceStatus{State: runtime.InstanceStateAllocated}, err
	}
	return runtime.InstanceStatus{State: runtime.InstanceStateAllocated}, r.errs["schedule"]
}

func (r *fakeActivationRuntime) Start(_ context.Context, deploymentID, instanceID string) error {
	r.record("start")
	r.startCalls = append(r.startCalls, fakeInstanceCall{deploymentID: deploymentID, instanceID: instanceID})
	return r.errs["start"]
}

func (r *fakeActivationRuntime) ResolveRoutingTarget(_ context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	r.record("resolve-routing")
	r.resolveCalls = append(r.resolveCalls, fakeInstanceCall{deploymentID: deploymentID, instanceID: instanceID})
	if len(r.targetSequence) > 0 {
		target := r.targetSequence[0]
		r.targetSequence = r.targetSequence[1:]
		return target, r.errs["resolve-routing"]
	}
	return r.target, r.errs["resolve-routing"]
}

func (r *fakeActivationRuntime) Drain(_ context.Context, deploymentID, instanceID string, grace time.Duration) error {
	r.record("drain")
	r.drainCalls = append(r.drainCalls, fakeDrainCall{deploymentID: deploymentID, instanceID: instanceID, grace: grace})
	return r.errs["drain"]
}

func (r *fakeActivationRuntime) record(call string) {
	*r.calls = append(*r.calls, call)
}

type fakeInstanceCall struct {
	deploymentID string
	instanceID   string
}

type fakeDrainCall struct {
	deploymentID string
	instanceID   string
	grace        time.Duration
}

type fakePromoter struct {
	calls    *[]string
	err      error
	targets  []runtime.RoutingTarget
	metadata []RoutePromotionMetadata
}

func (p *fakePromoter) Promote(_ context.Context, target runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	*p.calls = append(*p.calls, "promote")
	p.targets = append(p.targets, target)
	p.metadata = append(p.metadata, metadata)
	return p.err
}

func (p *fakePromoter) PromoteReplicas(ctx context.Context, targets []runtime.RoutingTarget, metadata RoutePromotionMetadata) error {
	for _, target := range targets {
		if err := p.Promote(ctx, target, metadata); err != nil {
			return err
		}
	}
	return nil
}

type fakeOperationUpdater struct {
	transitions []orailtypes.OperationState
	failed      bool
	failCode    orailtypes.FailureCode
	failReason  string
}

func (s *fakeOperationUpdater) Transition(_ context.Context, id orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	s.transitions = append(s.transitions, state)
	return orailtypes.Operation{ID: id, Kind: "deploy", State: state}, nil
}

func (s *fakeOperationUpdater) Fail(_ context.Context, id orailtypes.ID, code orailtypes.FailureCode, reason string) (orailtypes.Operation, error) {
	s.failed = true
	s.failCode = code
	s.failReason = reason
	return orailtypes.Operation{ID: id, Kind: "deploy", State: orailtypes.OperationFailed, Code: code, Reason: reason}, nil
}

type fakeCancelChecker struct {
	requested bool
	err       error
}

type fakeTemplateVerifier struct {
	evidence     templatepkg.VerificationEvidence
	err          error
	cleanupErr   error
	cleanupCalls int
	cleaned      templatepkg.VerificationEvidence
	afterVerify  func()
}

func (v *fakeTemplateVerifier) Verify(context.Context, TemplateVerificationRequest) (templatepkg.VerificationEvidence, error) {
	if v.afterVerify != nil {
		v.afterVerify()
	}
	return v.evidence, v.err
}

func (v *fakeTemplateVerifier) Cleanup(_ context.Context, evidence templatepkg.VerificationEvidence) error {
	v.cleanupCalls++
	v.cleaned = evidence
	return v.cleanupErr
}

type fakeTemplateRunRecorder struct {
	running     int
	verified    int
	succeeded   int
	failed      int
	verifiedErr error
	cleanup     int
}

type fakeCanonicalCommitter struct{ err error }

func (c fakeCanonicalCommitter) CommitCanonicalDeployment(context.Context, string) error {
	return c.err
}

func (c fakeCanonicalCommitter) CommitCanonicalTemplateDeployment(context.Context, string, string) error {
	return c.err
}

type fakeCronCanonicalCommitter struct {
	activation  CronActivation
	operationID string
	calls       int
	err         error
}

func (c *fakeCronCanonicalCommitter) CommitCanonicalDeployment(context.Context, string) error {
	return c.err
}

func (c *fakeCronCanonicalCommitter) CommitCanonicalCronDeployment(_ context.Context, operationID string, activation CronActivation) error {
	c.calls++
	c.operationID = operationID
	c.activation = activation
	return c.err
}

type fakeDeploymentInstanceStore struct {
	observed []runtime.RoutingTarget
	ensured  []string
	err      error
}

func (s *fakeDeploymentInstanceStore) Ensure(_ context.Context, deploymentID string, _ int) ([]DeploymentInstance, error) {
	s.ensured = append(s.ensured, deploymentID)
	return []DeploymentInstance{{DeploymentID: "dep-2", InstanceID: "dep-2-r0"}}, nil
}

func (s *fakeDeploymentInstanceStore) Observe(_ context.Context, target runtime.RoutingTarget) error {
	s.observed = append(s.observed, target)
	return s.err
}

func (s *fakeDeploymentInstanceStore) SetDeploymentState(context.Context, string, string, string) error {
	return nil
}

func (s *fakeDeploymentInstanceStore) ReconcileRuntime(context.Context, InstanceRuntimeResolver) error {
	return nil
}

type fakeTransactionalPromoter struct {
	targets      []runtime.RoutingTarget
	boundPublic  bool
	bindingReads int
}

type fakeRouteIntentCoordinator struct {
	calls int
	err   error
}

func (c *fakeRouteIntentCoordinator) EnsureDeployRoute(
	context.Context,
	DeployJobPayload,
	runtime.ScheduleRequest,
) error {
	c.calls++
	return c.err
}

func (p *fakeTransactionalPromoter) HasEligibleDeploymentPublicRouteBindings(
	_ context.Context,
	_ string,
	_ router.RouteScope,
) (bool, error) {
	p.bindingReads++
	return p.boundPublic, nil
}

func (p *fakeTransactionalPromoter) Promote(context.Context, runtime.RoutingTarget, RoutePromotionMetadata) error {
	return errors.New("non-transactional promote was called")
}

func (p *fakeTransactionalPromoter) PromoteWithCommit(
	ctx context.Context,
	target runtime.RoutingTarget,
	_ RoutePromotionMetadata,
	commit func(context.Context) error,
) error {
	p.targets = append(p.targets, target)
	return commit(ctx)
}

func (p *fakeTransactionalPromoter) PromoteReplicasWithCommit(
	ctx context.Context,
	targets []runtime.RoutingTarget,
	_ RoutePromotionMetadata,
	commit func(context.Context) error,
) error {
	p.targets = append(p.targets, targets...)
	return commit(ctx)
}

func (r *fakeTemplateRunRecorder) MarkRunning(context.Context, string) error {
	r.running++
	return nil
}

func (r *fakeTemplateRunRecorder) MarkVerified(context.Context, string, templatepkg.VerificationEvidence) error {
	r.verified++
	return r.verifiedErr
}

func (r *fakeTemplateRunRecorder) MarkSucceeded(context.Context, string) error {
	r.succeeded++
	return nil
}

func (r *fakeTemplateRunRecorder) MarkCleanupCompleted(context.Context, string) error {
	r.cleanup++
	return nil
}

func (r *fakeTemplateRunRecorder) MarkFailed(context.Context, string, failurecodes.Code, string) error {
	r.failed++
	return nil
}

func (c fakeCancelChecker) CancelRequested(context.Context, string) (bool, error) {
	return c.requested, c.err
}

type recordingDeployObserver struct {
	events []DeployStepEvent
}

func (o *recordingDeployObserver) ObserveDeployStep(_ context.Context, event DeployStepEvent) {
	o.events = append(o.events, event)
}

func deployEventKeys(events []DeployStepEvent) []string {
	keys := make([]string, 0, len(events))
	for _, event := range events {
		keys = append(keys, string(event.Step)+":"+string(event.State))
	}
	return keys
}

func findDeployEvent(events []DeployStepEvent, step DeployStep, state DeployStepState) DeployStepEvent {
	for _, event := range events {
		if event.Step == step && event.State == state {
			return event
		}
	}
	return DeployStepEvent{}
}

// livenessRuntime adds the optional liveness and image-port capabilities to
// the activation-runtime fake so readiness tests can drive a candidate that
// has already died.
type livenessRuntime struct {
	*fakeActivationRuntime
	live         runtime.InstanceLiveness
	liveErr      error
	liveCalls    int
	declaredPort []int
	portErr      error
}

func (r *livenessRuntime) ObserveInstanceLiveness(_ context.Context, deploymentID, instanceID string) (runtime.InstanceLiveness, error) {
	r.liveCalls++
	if r.liveErr != nil {
		return runtime.InstanceLiveness{}, r.liveErr
	}
	live := r.live
	live.DeploymentID, live.InstanceID = deploymentID, instanceID
	return live, nil
}

func (r *livenessRuntime) ImageDeclaredPorts(context.Context, string) ([]int, error) {
	if r.portErr != nil {
		return nil, r.portErr
	}
	return append([]int(nil), r.declaredPort...), nil
}

// closedLocalPort returns a loopback port with nothing listening on it, so a
// TCP readiness probe against it is refused rather than accidentally reaching
// whatever the developer happens to be running.
func closedLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a closed port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved port: %v", err)
	}
	return port
}

func notReadyTarget() runtime.RoutingTarget {
	return runtime.RoutingTarget{DeploymentID: "dep-2", InstanceID: "dep-2-r0", ContainerID: "ctr-2", ContainerIP: "127.0.0.1", Ready: false}
}

func TestExecutorFailsFastWhenCandidateHasAlreadyExited(t *testing.T) {
	calls := []string{}
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: notReadyTarget()},
		live: runtime.InstanceLiveness{
			Observed: true,
			State:    runtime.InstanceStateStopped,
			Exited:   true,
			ExitCode: 1,
			Reason:   "container exited with code 1",
			LogTail:  []string{"FATAL: no config file"},
		},
	}
	ops := &fakeOperationUpdater{}
	promoter := &fakePromoter{calls: &calls}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   promoter,
		Operations: ops,
		// A five-minute budget: the point of the test is that none of it is
		// spent once the runtime says the container is gone.
		Readiness: ReadinessConfig{Timeout: 5 * time.Minute, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatal("Execute() error = nil, want the exited candidate to fail the deploy")
	}
	if out.FailureCode != failurecodes.DeployCandidateExited {
		t.Fatalf("failure code = %q, want %q", out.FailureCode, failurecodes.DeployCandidateExited)
	}
	if !strings.Contains(out.FailureReason, "exited with code 1") {
		t.Fatalf("failure reason = %q, want the exit code", out.FailureReason)
	}
	if !strings.Contains(out.FailureReason, "no config file") {
		t.Fatalf("failure reason = %q, want the container's own last log line", out.FailureReason)
	}
	if clock.sleeps != 0 {
		t.Fatalf("readiness slept %d times, want the wait to end on the first attempt", clock.sleeps)
	}
	if len(promoter.targets) != 0 {
		t.Fatalf("promoted targets = %#v, want none for a dead candidate", promoter.targets)
	}
	if len(rt.drainCalls) != 1 {
		t.Fatalf("drain calls = %#v, want the candidate cleaned up", rt.drainCalls)
	}
	if !out.Plan.PreviousActiveProtected {
		t.Fatalf("plan = %#v, want the previous active release protected", out.Plan)
	}
}

func TestExecutorFailsFastOnRestartLoopingCandidate(t *testing.T) {
	calls := []string{}
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: notReadyTarget()},
		live: runtime.InstanceLiveness{
			Observed:       true,
			State:          runtime.InstanceStateRestarting,
			RestartLooping: true,
			Restarts:       4,
			ExitCode:       2,
			Reason:         "container restarted 4 times (last exit code 2)",
		},
	}
	ops := &fakeOperationUpdater{}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: ops,
		Readiness:  ReadinessConfig{Timeout: 5 * time.Minute, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
	}

	out, _ := executor.Execute(context.Background(), deployJob())
	if out.FailureCode != failurecodes.DeployCandidateRestartLoop {
		t.Fatalf("failure code = %q, want %q", out.FailureCode, failurecodes.DeployCandidateRestartLoop)
	}
	if !strings.Contains(out.FailureReason, "keeps restarting") {
		t.Fatalf("failure reason = %q, want plain language about the restart loop", out.FailureReason)
	}
	if ops.failCode != orailtypes.FailureCode(failurecodes.DeployCandidateRestartLoop) {
		t.Fatalf("operation failure code = %q, want the typed restart-loop code", ops.failCode)
	}
}

func TestExecutorKeepsWaitingWhenLivenessIsUnobserved(t *testing.T) {
	calls := []string{}
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: notReadyTarget()},
		// Observed false is "we could not look", never "the workload is dead".
		live: runtime.InstanceLiveness{Observed: false},
	}
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Readiness:  ReadinessConfig{Timeout: 3 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil {
		t.Fatal("Execute() error = nil, want the readiness timeout")
	}
	if out.FailureCode != failurecodes.ReconcileStepFailed {
		t.Fatalf("failure code = %q, want the plain readiness timeout code", out.FailureCode)
	}
	if clock.sleeps == 0 {
		t.Fatal("unobserved liveness must not shorten the readiness wait")
	}
}

func TestExecutorSkipsPortProbeForImageThatDeclaresNoPort(t *testing.T) {
	calls := []string{}
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		live:                  runtime.InstanceLiveness{Observed: true, State: runtime.InstanceStateRunning},
		// A worker-shaped image: no EXPOSE at all.
		declaredPort: nil,
	}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Readiness:  ReadinessConfig{Timeout: 5 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err != nil {
		t.Fatalf("Execute() error = %v, want a portless image to become ready by running", err)
	}
	if !out.Succeeded {
		t.Fatalf("outcome = %#v, want success without a TCP probe", out)
	}
}

func TestExecutorStillProbesImageThatDeclaresAPort(t *testing.T) {
	calls := []string{}
	// The routing target names a port with nothing behind it: the probe must
	// dial it and fail rather than assume the container is serving.
	target := readyTarget()
	target.InternalPort = closedLocalPort(t)
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: target},
		live:                  runtime.InstanceLiveness{Observed: true, State: runtime.InstanceStateRunning},
		// The image says it listens somewhere, so it has to prove it.
		declaredPort: []int{8080},
	}
	// Everything else stays at the platform defaults, so the image's own
	// declaration is the only reason the probe still runs.
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Readiness:  ReadinessConfig{Timeout: 3 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil || out.Succeeded {
		t.Fatalf("outcome = %#v, want the unlistening port to block promotion", out)
	}
}

func TestExecutorKeepsProbingWhenOperatorChoseAPort(t *testing.T) {
	calls := []string{}
	rt := &livenessRuntime{
		fakeActivationRuntime: &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: readyTarget()},
		live:                  runtime.InstanceLiveness{Observed: true, State: runtime.InstanceStateRunning},
		declaredPort:          nil,
	}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	// An operator-chosen port is an assertion that something listens there.
	cfg.TargetPort = closedLocalPort(t)
	clock := &fakeReadinessClock{now: time.Unix(0, 0)}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Readiness:  ReadinessConfig{Timeout: 3 * time.Second, Interval: time.Second, Now: clock.Now, Sleep: clock.Sleep},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}

	out, err := executor.Execute(context.Background(), deployJob())
	if err == nil || out.Succeeded {
		t.Fatalf("outcome = %#v, want a configured port to still be proven", out)
	}
}

func TestExecutorUsesShorterReadinessBudgetForImageDeploys(t *testing.T) {
	calls := []string{}
	rt := &fakeActivationRuntime{calls: &calls, pull: verifiedPull(), target: notReadyTarget()}
	cfg := serviceconfig.Default("prj-1", "api", "prod")
	cfg.HealthKind = serviceconfig.HealthNone
	start := time.Unix(0, 0)
	clock := &fakeReadinessClock{now: start}
	executor := Executor{
		Runtime:    rt,
		Promoter:   &fakePromoter{calls: &calls},
		Operations: &fakeOperationUpdater{},
		Readiness:  ReadinessConfig{Now: clock.Now, Sleep: clock.Sleep},
		Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{
			Config: cfg, SHA256: strings.Repeat("d", 64),
		}},
	}

	if _, err := executor.Execute(context.Background(), deployJob()); err == nil {
		t.Fatal("Execute() error = nil, want a readiness timeout")
	}
	elapsed := clock.now.Sub(start)
	if elapsed != serviceconfig.DefaultImageHealthTimeout {
		t.Fatalf("image deploy waited %s, want the %s image budget", elapsed, serviceconfig.DefaultImageHealthTimeout)
	}
}
