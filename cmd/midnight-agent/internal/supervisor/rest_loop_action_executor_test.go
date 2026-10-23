package supervisor

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/midnight/midnight/cmd/midnight-agent/internal/protocol"
)

type fakeRESTExecutor struct {
	executeCalls int
	desiredCalls int
	execute      func(*http.Request, any) (protocol.ResponseMeta, error)
	desired      func(*http.Request) (protocol.DesiredStateResponse, error)
}

func (f *fakeRESTExecutor) Execute(req *http.Request, out any) (protocol.ResponseMeta, error) {
	f.executeCalls++
	return f.execute(req, out)
}

func (f *fakeRESTExecutor) ExecuteDesiredState(req *http.Request) (protocol.DesiredStateResponse, error) {
	f.desiredCalls++
	return f.desired(req)
}

func TestRESTLoopActionExecutorCallsInjectedProtocolExecutor(t *testing.T) {
	fake := &fakeRESTExecutor{
		execute: func(req *http.Request, out any) (protocol.ResponseMeta, error) {
			switch req.URL.EscapedPath() {
			case protocol.RegistrationPath:
				resp := out.(*protocol.RegistrationResponse)
				resp.AgentID = "agent-a"
				return protocol.ResponseMeta{StatusCode: http.StatusCreated}, nil
			case "/api/v1alpha/agents/agent-a/heartbeat":
				resp := out.(*protocol.HeartbeatResponse)
				resp.AcceptedSequence = 9
				return protocol.ResponseMeta{StatusCode: http.StatusOK}, nil
			default:
				t.Fatalf("unexpected execute request: %s %s", req.Method, req.URL.EscapedPath())
				return protocol.ResponseMeta{}, nil
			}
		},
		desired: func(req *http.Request) (protocol.DesiredStateResponse, error) {
			if req.Method != http.MethodGet || req.URL.EscapedPath() != "/api/v1alpha/agents/agent-a/desired" {
				t.Fatalf("unexpected desired request: %s %s", req.Method, req.URL.EscapedPath())
			}
			if req.Header.Get(protocol.HeaderETag) != `"etag-a"` {
				t.Fatalf("etag = %q", req.Header.Get(protocol.HeaderETag))
			}
			return protocol.DesiredStateResponse{ETag: `"etag-b"`, Raw: []byte(`{"deployments":[]}`)}, nil
		},
	}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.midnight.example"},
			Executor: fake,
		},
		Registration:     protocol.RegistrationRequest{AgentID: "agent-a", Version: "0.0.1", Timestamp: time.Unix(100, 0).UTC()},
		HeartbeatRequest: protocol.HeartbeatRequest{AgentID: "agent-a", Sequence: 9, Status: "ready", Runtime: "docker", SentAt: time.Unix(101, 0).UTC()},
		DesiredAgentID:   "agent-a",
		DesiredETag:      `"etag-a"`,
	}

	register, err := executor.Register(context.Background())
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	heartbeat, err := executor.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}
	fetch, err := executor.FetchDesired(context.Background())
	if err != nil {
		t.Fatalf("FetchDesired returned error: %v", err)
	}

	if register.Message != "registered agent agent-a with status 201" {
		t.Fatalf("register outcome = %+v", register)
	}
	if heartbeat.Message != "heartbeat accepted sequence 9 with status 200" {
		t.Fatalf("heartbeat outcome = %+v", heartbeat)
	}
	if fetch.Message != "fetched desired state 18 bytes with etag \"etag-b\"" {
		t.Fatalf("fetch outcome = %+v", fetch)
	}
	if fake.executeCalls != 2 || fake.desiredCalls != 1 {
		t.Fatalf("calls execute=%d desired=%d", fake.executeCalls, fake.desiredCalls)
	}
}

func TestRESTLoopActionExecutorDryRunRecordsIntentWithoutNetwork(t *testing.T) {
	fake := &fakeRESTExecutor{
		execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
			t.Fatal("dry-run must not execute HTTP requests")
			return protocol.ResponseMeta{}, nil
		},
		desired: func(*http.Request) (protocol.DesiredStateResponse, error) {
			t.Fatal("dry-run must not fetch desired state")
			return protocol.DesiredStateResponse{}, nil
		},
	}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.midnight.example/root"},
			Executor: fake,
		},
		Registration:     protocol.RegistrationRequest{AgentID: "agent-a"},
		HeartbeatRequest: protocol.HeartbeatRequest{AgentID: "agent/a", Sequence: 3},
		DesiredAgentID:   "agent-a",
		DesiredETag:      `"etag-a"`,
		DryRun:           true,
	}

	for _, action := range []func(context.Context) (LoopInjectedOutcome, error){executor.Register, executor.Heartbeat, executor.FetchDesired} {
		if _, err := action(context.Background()); err != nil {
			t.Fatalf("dry-run action returned error: %v", err)
		}
	}

	want := []RESTLoopActionIntent{
		{Action: LoopActionRegister, Method: http.MethodPost, Path: "/root/api/v1alpha/agents/register", AgentID: "agent-a", Attempt: 1},
		{Action: LoopActionHeartbeat, Method: http.MethodPost, Path: "/root/api/v1alpha/agents/agent%2Fa/heartbeat", AgentID: "agent/a", Attempt: 1},
		{Action: LoopActionFetchDesired, Method: http.MethodGet, Path: "/root/api/v1alpha/agents/agent-a/desired", AgentID: "agent-a", ETag: `"etag-a"`, Attempt: 1},
	}
	if !reflect.DeepEqual(executor.Intents, want) {
		t.Fatalf("intents = %+v, want %+v", executor.Intents, want)
	}
	if fake.executeCalls != 0 || fake.desiredCalls != 0 {
		t.Fatalf("dry-run calls execute=%d desired=%d", fake.executeCalls, fake.desiredCalls)
	}
}

func TestRESTLoopActionExecutorDryRunReturnsBuildErrorWithoutIntent(t *testing.T) {
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder: protocol.RequestBuilder{},
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-a"},
		DryRun:       true,
	}

	outcome, err := executor.Register(context.Background())
	if err == nil {
		t.Fatal("expected dry-run build error")
	}
	if outcome.Completed || len(executor.Intents) != 0 {
		t.Fatalf("outcome = %+v, intents = %+v", outcome, executor.Intents)
	}
}

func TestRESTLoopActionExecutorPreservesHTTPErrorIssueCode(t *testing.T) {
	fake := &fakeRESTExecutor{execute: func(*http.Request, any) (protocol.ResponseMeta, error) {
		return protocol.ResponseMeta{StatusCode: http.StatusConflict}, protocol.HTTPError{StatusCode: http.StatusConflict, Code: "agent_conflict", Message: "agent already registered"}
	}}
	executor := &RESTLoopActionExecutor{
		Client: protocol.AgentClient{
			Builder:  protocol.RequestBuilder{BaseURL: "https://control.midnight.example"},
			Executor: fake,
		},
		Registration: protocol.RegistrationRequest{AgentID: "agent-a"},
	}

	outcome, err := executor.Register(context.Background())
	if err == nil {
		t.Fatal("expected register error")
	}
	if outcome.IssueCode != "agent_conflict" {
		t.Fatalf("outcome = %+v", outcome)
	}
}

func TestRESTLoopActionExecutorHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &RESTLoopActionExecutor{DryRun: true}

	_, err := executor.Register(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
}
