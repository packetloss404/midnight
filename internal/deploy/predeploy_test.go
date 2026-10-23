package deploy

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/runtime"
)

func TestPredeployExecutorPersistsSuccessAndDoesNotInvokeTerminalRunAgain(t *testing.T) {
	store := newMemoryPredeployStore()
	runner := &recordingPredeployRunner{result: runtime.OneOffResult{
		ContainerName: "or-predeploy-test", ContainerID: "ctr-test", ExitCode: 0,
	}}
	executor := &PredeployExecutor{Store: store, Runner: runner}
	request := predeployFixture()

	first, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if first.State != PredeployStateSucceeded || second.State != PredeployStateSucceeded {
		t.Fatalf("states = %q, %q", first.State, second.State)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}
	if got := runner.requests[0].ScheduleRequest; len(got.Mounts) != 0 ||
		got.RestartPolicy != "no" || got.RestartMaxRetries != 0 {
		t.Fatalf("one-off runtime constraints = %+v", got)
	}
	if !reflect.DeepEqual(runner.requests[0].ScheduleRequest.Env, request.Candidate.Env) {
		t.Fatalf("one-off env = %#v, want exact immutable env %#v",
			runner.requests[0].ScheduleRequest.Env, request.Candidate.Env)
	}
	if store.run.FailureCode != "" || store.run.ExitCode == nil || *store.run.ExitCode != 0 {
		t.Fatalf("stored terminal evidence = %+v", store.run)
	}
}

func TestPredeployExecutorReturnsTypedNonzeroAndTimeoutFailures(t *testing.T) {
	tests := []struct {
		name   string
		result runtime.OneOffResult
		code   failurecodes.Code
	}{
		{name: "nonzero", result: runtime.OneOffResult{ExitCode: 23}, code: failurecodes.PredeployFailed},
		{name: "timeout", result: runtime.OneOffResult{ExitCode: -1, TimedOut: true}, code: failurecodes.PredeployTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryPredeployStore()
			runner := &recordingPredeployRunner{result: tt.result}
			executor := &PredeployExecutor{Store: store, Runner: runner}
			_, err := executor.Execute(context.Background(), predeployFixture())
			var failure *PredeployFailure
			if !errors.As(err, &failure) || failure.Code != tt.code {
				t.Fatalf("error = %T %v, want PredeployFailure %s", err, err, tt.code)
			}
			// A failed terminal attempt is immutable too.
			_, err = executor.Execute(context.Background(), predeployFixture())
			if !errors.As(err, &failure) || runner.calls != 1 {
				t.Fatalf("terminal retry error=%v runner calls=%d, want no rerun", err, runner.calls)
			}
		})
	}
}

func TestPredeployExecutorLeavesRunningOnRuntimeTransportFailure(t *testing.T) {
	store := newMemoryPredeployStore()
	runner := &recordingPredeployRunner{err: errors.New("engine disconnected")}
	executor := &PredeployExecutor{Store: store, Runner: runner}
	if _, err := executor.Execute(context.Background(), predeployFixture()); err == nil {
		t.Fatal("Execute error = nil")
	}
	if store.run.State != PredeployStateRunning || store.run.CompletedAt != nil {
		t.Fatalf("run = %+v, want recoverable running state", store.run)
	}
}

func TestPredeploySpecDigestUsesSnapshotIdentityNotPlaintextValues(t *testing.T) {
	first := predeployFixture()
	second := predeployFixture()
	second.Candidate.Env = []string{"A=different", "SECRET=another-plaintext-value"}
	firstSHA, err := predeploySpecSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondSHA, err := predeploySpecSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSHA != secondSHA {
		t.Fatalf("plaintext-only rotation changed digest: %s != %s", firstSHA, secondSHA)
	}
	second.SnapshotSHA256 = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	rotatedSHA, err := predeploySpecSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedSHA == firstSHA {
		t.Fatal("immutable variable-version snapshot rotation did not change digest")
	}
}

func predeployFixture() PredeployRequest {
	return PredeployRequest{
		Candidate: runtime.ScheduleRequest{
			DeploymentID: "dep-1", InstanceID: "dep-1-r0",
			ProjectID: "prj-1", EnvironmentID: "env-1", ServiceID: "svc-1",
			Image:  "registry.example/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Labels: map[string]string{"midnight.test": "true"},
			Env:    []string{"A=first", "SECRET=value"},
		},
		Command:        "bin/migrate up",
		Timeout:        2 * time.Minute,
		SnapshotSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
}

type recordingPredeployRunner struct {
	calls    int
	requests []runtime.OneOffRequest
	result   runtime.OneOffResult
	err      error
	callLog  *[]string
}

func (r *recordingPredeployRunner) RunOneOff(_ context.Context, request runtime.OneOffRequest) (runtime.OneOffResult, error) {
	r.calls++
	r.requests = append(r.requests, request)
	if r.callLog != nil {
		*r.callLog = append(*r.callLog, "predeploy")
	}
	return r.result, r.err
}

type memoryPredeployStore struct {
	mu  sync.Mutex
	run PredeployRun
	now time.Time
}

func newMemoryPredeployStore() *memoryPredeployStore {
	return &memoryPredeployStore{now: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)}
}

func (s *memoryPredeployStore) Ensure(_ context.Context, seed PredeployRun) (PredeployRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.DeploymentID == "" {
		seed.State = PredeployStatePending
		seed.CreatedAt, seed.UpdatedAt, seed.ResourceVersion = s.now, s.now, 1
		s.run = seed
	} else if s.run.SpecSHA256 != seed.SpecSHA256 {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	return s.run, nil
}

func (s *memoryPredeployStore) MarkRunning(_ context.Context, deploymentID, specSHA string) (PredeployRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.DeploymentID != deploymentID || s.run.SpecSHA256 != specSHA {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	if s.run.State == PredeployStatePending || s.run.State == PredeployStateRunning {
		s.run.State = PredeployStateRunning
		if s.run.StartedAt == nil {
			started := s.now
			s.run.StartedAt = &started
		}
		s.run.ResourceVersion++
	}
	return s.run, nil
}

func (s *memoryPredeployStore) MarkTerminal(
	_ context.Context,
	deploymentID, specSHA string,
	result runtime.OneOffResult,
	code failurecodes.Code,
	message string,
) (PredeployRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run.DeploymentID != deploymentID || s.run.SpecSHA256 != specSHA {
		return PredeployRun{}, ErrPredeploySpecConflict
	}
	if s.run.State == PredeployStatePending || s.run.State == PredeployStateRunning {
		state := PredeployStateSucceeded
		if code != "" {
			state = PredeployStateFailed
		}
		completed, exitCode := s.now.Add(time.Second), result.ExitCode
		s.run.State, s.run.CompletedAt, s.run.ExitCode = state, &completed, &exitCode
		s.run.ContainerName, s.run.ContainerID = result.ContainerName, result.ContainerID
		s.run.FailureCode, s.run.FailureMessage = code, message
		s.run.ResourceVersion++
	}
	return s.run, nil
}
