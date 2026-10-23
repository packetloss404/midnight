package supervisor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/desiredstate"
	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

func TestShutdownDeadlineUsesGrace(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	deadline := ShutdownDeadline(now, 5*time.Second)

	if !deadline.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("deadline = %v", deadline)
	}
}

func TestShutdownDeadlineDefaultsInvalidGrace(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	deadline := ShutdownDeadline(now, 0)

	if !deadline.Equal(now.Add(DefaultShutdownDeadline)) {
		t.Fatalf("deadline = %v", deadline)
	}
}

func TestNewLoopPlanListsRuntimeAgentPhases(t *testing.T) {
	plan := NewLoopPlan()
	want := []string{PhaseRegister, PhaseHeartbeat, PhaseDesiredPoll}

	if len(plan.Phases) != len(want) {
		t.Fatalf("phases = %v", plan.Phases)
	}
	for i := range want {
		if plan.Phases[i] != want[i] {
			t.Fatalf("phases = %v, want %v", plan.Phases, want)
		}
	}
}

func TestRunnerRunOnceRegistersHeartbeatsFetchesAndPersistsDesired(t *testing.T) {
	cache := desiredstate.CacheFile{Path: filepath.Join(t.TempDir(), "desired.json")}
	if err := cache.Save(desiredstate.State{ETag: `"cached"`, Raw: []byte(`{"old":true}`)}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	registrar := &fakeRegistrar{response: protocol.RegistrationResponse{AgentID: "agent-registered"}}
	heartbeater := &fakeHeartbeater{}
	fetcher := &fakeDesiredFetcher{result: DesiredFetchResult{State: desiredstate.State{ETag: `"fresh"`, Raw: []byte(`{"deployments":[]}`)}}}
	runner := Runner{
		Registrar:      registrar,
		Heartbeater:    heartbeater,
		DesiredFetcher: fetcher,
		DesiredCache:   cache,
		Now:            func() time.Time { return now },
	}

	summary, err := runner.RunOnce(context.Background(), RunInput{
		AgentID:         "agent-local",
		Version:         "v0-test",
		Hostname:        "host-a",
		OS:              "linux",
		Arch:            "amd64",
		Runtime:         "docker",
		Labels:          map[string]string{"zone": "test"},
		HeartbeatSecret: []byte("secret"),
	})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}

	if registrar.calls != 1 || heartbeater.calls != 1 || fetcher.calls != 1 {
		t.Fatalf("calls registrar=%d heartbeater=%d fetcher=%d", registrar.calls, heartbeater.calls, fetcher.calls)
	}
	if registrar.request.AgentID != "agent-local" || registrar.request.Timestamp != now {
		t.Fatalf("registration request = %+v", registrar.request)
	}
	if heartbeater.request.AgentID != "agent-registered" || heartbeater.request.Sequence != 1 {
		t.Fatalf("heartbeat request = %+v", heartbeater.request)
	}
	if heartbeater.request.Status != "ok" || heartbeater.request.MaintenanceMode || heartbeater.request.DrainMode {
		t.Fatalf("heartbeat mode = %+v", heartbeater.request)
	}
	if !protocol.VerifyHeartbeatRequestMAC([]byte("secret"), heartbeater.request, heartbeater.request.HMAC) {
		t.Fatalf("heartbeat hmac = %q", heartbeater.request.HMAC)
	}
	if fetcher.agentID != "agent-registered" || fetcher.etag != `"cached"` {
		t.Fatalf("desired fetch agent=%q etag=%q", fetcher.agentID, fetcher.etag)
	}
	stored, err := cache.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if stored.ETag != `"fresh"` || string(stored.Raw) != `{"deployments":[]}` {
		t.Fatalf("stored desired = %+v", stored)
	}
	if summary.AgentID != "agent-registered" || summary.DesiredETag != `"fresh"` || summary.HeartbeatSequence != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if !summary.Registration.OK || !summary.Heartbeat.OK || !summary.Desired.OK {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRunnerRunOnceReportsDrainModeInHeartbeat(t *testing.T) {
	cache := desiredstate.CacheFile{Path: filepath.Join(t.TempDir(), "desired.json")}
	if err := cache.Save(desiredstate.State{ETag: `"cached"`, Raw: []byte(`{"old":true}`)}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	heartbeater := &fakeHeartbeater{}
	runner := Runner{
		Registrar:      &fakeRegistrar{response: protocol.RegistrationResponse{AgentID: "agent-registered"}},
		Heartbeater:    heartbeater,
		DesiredFetcher: &fakeDesiredFetcher{result: DesiredFetchResult{NotModified: true}},
		DesiredCache:   cache,
		Now:            func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) },
	}

	_, err := runner.RunOnce(context.Background(), RunInput{AgentID: "agent-local", DrainMode: true})
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if heartbeater.request.Status != "draining" || !heartbeater.request.MaintenanceMode || !heartbeater.request.DrainMode {
		t.Fatalf("heartbeat request = %+v", heartbeater.request)
	}
}

func TestRunnerRunOnceStopsAfterRegistrationFailure(t *testing.T) {
	wantErr := errors.New("register failed")
	registrar := &fakeRegistrar{err: wantErr}
	heartbeater := &fakeHeartbeater{}
	fetcher := &fakeDesiredFetcher{}
	runner := Runner{
		Registrar:      registrar,
		Heartbeater:    heartbeater,
		DesiredFetcher: fetcher,
		DesiredCache:   desiredstate.CacheFile{Path: filepath.Join(t.TempDir(), "desired.json")},
		Now:            func() time.Time { return time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC) },
	}

	summary, err := runner.RunOnce(context.Background(), RunInput{AgentID: "agent-local"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunOnce error = %v, want %v", err, wantErr)
	}
	if registrar.calls != 1 || heartbeater.calls != 0 || fetcher.calls != 0 {
		t.Fatalf("calls registrar=%d heartbeater=%d fetcher=%d", registrar.calls, heartbeater.calls, fetcher.calls)
	}
	if !summary.Registration.Attempted || summary.Registration.OK || summary.Registration.Error == "" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.Heartbeat.Attempted || summary.Desired.Attempted {
		t.Fatalf("summary = %+v", summary)
	}
}

type fakeRegistrar struct {
	calls    int
	request  protocol.RegistrationRequest
	response protocol.RegistrationResponse
	err      error
}

func (f *fakeRegistrar) Register(_ context.Context, request protocol.RegistrationRequest) (protocol.RegistrationResponse, error) {
	f.calls++
	f.request = request
	return f.response, f.err
}

type fakeHeartbeater struct {
	calls   int
	request protocol.HeartbeatRequest
	err     error
}

func (f *fakeHeartbeater) Heartbeat(_ context.Context, request protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	f.calls++
	f.request = request
	return protocol.HeartbeatResponse{AcceptedSequence: request.Sequence}, f.err
}

type fakeDesiredFetcher struct {
	calls   int
	agentID string
	etag    string
	result  DesiredFetchResult
	err     error
}

func (f *fakeDesiredFetcher) FetchDesired(_ context.Context, agentID, etag string) (DesiredFetchResult, error) {
	f.calls++
	f.agentID = agentID
	f.etag = etag
	return f.result, f.err
}
