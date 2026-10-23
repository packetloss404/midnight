package protocol

import (
	"net/http"
	"testing"
	"time"
)

type fakeExecutor struct {
	execute func(*http.Request, any) (ResponseMeta, error)
	desired func(*http.Request) (DesiredStateResponse, error)
}

func (f fakeExecutor) Execute(req *http.Request, out any) (ResponseMeta, error) {
	return f.execute(req, out)
}

func (f fakeExecutor) ExecuteDesiredState(req *http.Request) (DesiredStateResponse, error) {
	return f.desired(req)
}

func TestAgentClientRegisterBuildsAndExecutesRequest(t *testing.T) {
	client := AgentClient{
		Builder: RequestBuilder{BaseURL: "https://control.midnight.example"},
		Executor: fakeExecutor{execute: func(req *http.Request, out any) (ResponseMeta, error) {
			if req.Method != http.MethodPost || req.URL.Path != RegistrationPath {
				t.Fatalf("request = %s %s", req.Method, req.URL.Path)
			}
			resp := out.(*RegistrationResponse)
			resp.AgentID = "agent-a"
			resp.HeartbeatInterval = 30 * time.Second
			return ResponseMeta{StatusCode: http.StatusCreated}, nil
		}},
	}

	got, meta, err := client.Register(RegistrationRequest{AgentID: "agent-a"})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if got.AgentID != "agent-a" || got.HeartbeatInterval != 30*time.Second || meta.StatusCode != http.StatusCreated {
		t.Fatalf("response = %+v meta = %+v", got, meta)
	}
}

func TestAgentClientHeartbeatBuildsAndExecutesRequest(t *testing.T) {
	client := AgentClient{
		Builder: RequestBuilder{BaseURL: "https://control.midnight.example"},
		Executor: fakeExecutor{execute: func(req *http.Request, out any) (ResponseMeta, error) {
			if req.Method != http.MethodPost || req.URL.EscapedPath() != "/api/v1alpha/agents/agent-a/heartbeat" {
				t.Fatalf("request = %s %s", req.Method, req.URL.EscapedPath())
			}
			resp := out.(*HeartbeatResponse)
			resp.AcceptedSequence = 2
			resp.DesiredStateETag = `"etag-a"`
			return ResponseMeta{StatusCode: http.StatusOK}, nil
		}},
	}

	got, _, err := client.Heartbeat(HeartbeatRequest{AgentID: "agent-a", Sequence: 2})
	if err != nil {
		t.Fatalf("Heartbeat returned error: %v", err)
	}
	if got.AcceptedSequence != 2 || got.DesiredStateETag != `"etag-a"` {
		t.Fatalf("response = %+v", got)
	}
}

func TestAgentClientFetchDesiredBuildsAndExecutesRequest(t *testing.T) {
	client := AgentClient{
		Builder: RequestBuilder{BaseURL: "https://control.midnight.example"},
		Executor: fakeExecutor{desired: func(req *http.Request) (DesiredStateResponse, error) {
			if req.Method != http.MethodGet || req.URL.Path != "/api/v1alpha/agents/agent-a/desired" {
				t.Fatalf("request = %s %s", req.Method, req.URL.Path)
			}
			if req.Header.Get(HeaderETag) != `"etag-a"` {
				t.Fatalf("etag = %q", req.Header.Get(HeaderETag))
			}
			return DesiredStateResponse{ETag: `"etag-b"`, Raw: []byte(`{"deployments":[]}`)}, nil
		}},
	}

	got, err := client.FetchDesired("agent-a", `"etag-a"`)
	if err != nil {
		t.Fatalf("FetchDesired returned error: %v", err)
	}
	if got.ETag != `"etag-b"` || string(got.Raw) != `{"deployments":[]}` {
		t.Fatalf("desired = %+v", got)
	}
}

func TestAgentClientRejectsMissingExecutor(t *testing.T) {
	client := AgentClient{Builder: RequestBuilder{BaseURL: "https://control.midnight.example"}}
	if _, _, err := client.Register(RegistrationRequest{AgentID: "agent-a"}); err == nil {
		t.Fatal("expected Register error")
	}
	if _, _, err := client.Heartbeat(HeartbeatRequest{AgentID: "agent-a"}); err == nil {
		t.Fatal("expected Heartbeat error")
	}
	if _, err := client.FetchDesired("agent-a", ""); err == nil {
		t.Fatal("expected FetchDesired error")
	}
}
