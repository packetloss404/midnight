package midnightctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// These assignments are intentional compile gates. If canonical generation
// removes or changes a core public model or interface, this package fails CI.
var (
	_ ClientInterface              = (*Client)(nil)
	_ ClientWithResponsesInterface = (*ClientWithResponses)(nil)
	_                              = ErrorResponse{Error: ErrorEnvelope{}}
	_                              = CursorPage{NextCursor: nil}
	_                              = Operation{CreatedAt: time.Time{}, UpdatedAt: time.Time{}}
	_                              = OperationListResponse{Data: []Operation{}, Page: CursorPage{}}
	_ AgentRegistrationRequest     = AgentRegistrationRequest{}
	_ AgentHeartbeatRequest        = AgentHeartbeatRequest{}
	_ AgentStatusReport            = AgentStatusReport{}
)

func TestGeneratedOperationStateContract(t *testing.T) {
	t.Parallel()
	want := []OperationState{"QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "CANCELED"}
	got := []OperationState{OperationStateQUEUED, OperationStateRUNNING, OperationStateSUCCEEDED, OperationStateFAILED, OperationStateCANCELED}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("operation state %d = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestAgentCredentialAndTypedRequestSurface(t *testing.T) {
	t.Parallel()

	received := make(chan AgentRegistrationRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Errorf("Authorization = %q, want scoped agent bearer", got)
		}
		if got := request.Header.Get("X-Midnight-Agent-ID"); got != "agent-a" {
			t.Errorf("X-Midnight-Agent-ID = %q, want agent-a", got)
		}
		var body AgentRegistrationRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode typed registration body: %v", err)
		}
		received <- body
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClientWithResponses(server.URL+"/api/v1alpha", WithAgentCredential("agent-a", "agent-secret"))
	if err != nil {
		t.Fatal(err)
	}
	hostname, osName, arch := "host-a", "linux", "amd64"
	timestamp := time.Unix(1, 0).UTC()
	want := AgentRegistrationRequest{AgentId: "agent-a", Version: "1.2.3", Hostname: &hostname, Os: &osName, Arch: &arch, Timestamp: &timestamp}
	response, err := client.RegisterRuntimeAgentWithResponse(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode())
	}
	if got := <-received; got.AgentId != want.AgentId || got.Version != want.Version || got.Timestamp == nil || !got.Timestamp.Equal(*want.Timestamp) {
		t.Fatalf("registration body = %#v, want %#v", got, want)
	}
}

func TestTypedAgentHeartbeatAndStatusRequestSurfaces(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("Authorization"); got != "Bearer agent-secret" {
			t.Errorf("Authorization = %q, want scoped agent bearer", got)
		}
		if got := request.Header.Get("X-Midnight-Agent-ID"); got != "agent-a" {
			t.Errorf("X-Midnight-Agent-ID = %q, want agent-a", got)
		}
		switch requests {
		case 1:
			if request.URL.Path != "/api/v1alpha/agents/agent-a/heartbeat" {
				t.Errorf("heartbeat path = %q", request.URL.Path)
			}
			var body AgentHeartbeatRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.AgentId == nil || *body.AgentId != "agent-a" || body.Sequence != 7 {
				t.Errorf("heartbeat body = %#v, error = %v", body, err)
			}
		case 2:
			if request.URL.Path != "/api/v1alpha/agents/agent-a/status" {
				t.Errorf("status path = %q", request.URL.Path)
			}
			var body AgentStatusReport
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.AgentId != "agent-a" || body.Sequence != 8 {
				t.Errorf("status body = %#v, error = %v", body, err)
			}
		default:
			t.Errorf("unexpected request %d: %s", requests, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	client, err := NewClientWithResponses(server.URL+"/api/v1alpha", WithAgentCredential("agent-a", "agent-secret"))
	if err != nil {
		t.Fatal(err)
	}
	agentID := "agent-a"
	heartbeat, err := client.RecordRuntimeAgentHeartbeatWithResponse(context.Background(), "agent-a", AgentHeartbeatRequest{AgentId: &agentID, Sequence: 7})
	if err != nil {
		t.Fatal(err)
	}
	if heartbeat.StatusCode() != http.StatusOK {
		t.Fatalf("heartbeat response status = %d", heartbeat.StatusCode())
	}
	status, err := client.RecordRuntimeAgentStatusWithResponse(context.Background(), "agent-a", AgentStatusReport{AgentId: "agent-a", Sequence: 8})
	if err != nil {
		t.Fatal(err)
	}
	if status.StatusCode() != http.StatusOK {
		t.Fatalf("status response status = %d", status.StatusCode())
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestWithAgentCredentialRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentID    string
		credential AgentCredential
	}{
		{"", "secret"},
		{" agent-a", "secret"},
		{"agent-a\r\nInjected", "secret"},
		{"agent-a", ""},
		{"agent-a", " secret"},
		{"agent-a", "secret\r\nInjected"},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%q/%q", test.agentID, test.credential), func(t *testing.T) {
			if _, err := NewClient("https://api.midnight.invalid", WithAgentCredential(test.agentID, test.credential)); err == nil {
				t.Fatal("NewClient accepted unsafe agent credential")
			}
		})
	}
}
