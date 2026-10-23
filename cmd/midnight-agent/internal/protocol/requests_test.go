package protocol

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestNewRegistrationRequest(t *testing.T) {
	builder := RequestBuilder{
		BaseURL:   "https://control.midnight.example/root/",
		Token:     " token-a ",
		UserAgent: " midnight-agent/test ",
	}
	body := RegistrationRequest{
		AgentID:   "agent-a",
		Version:   "0.0.1",
		Hostname:  "host-a",
		OS:        "linux",
		Arch:      "amd64",
		Timestamp: time.Unix(100, 0).UTC(),
	}

	req, err := builder.NewRegistrationRequest(body)
	if err != nil {
		t.Fatalf("NewRegistrationRequest returned error: %v", err)
	}
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.Path != "/root/api/v1alpha/agents/register" {
		t.Fatalf("path = %s", req.URL.Path)
	}
	assertCommonHeaders(t, req, "agent-a")

	var got RegistrationRequest
	if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.AgentID != body.AgentID || got.Version != body.Version {
		t.Fatalf("body = %+v, want %+v", got, body)
	}
}

func TestNewHeartbeatRequestEscapesAgentID(t *testing.T) {
	builder := RequestBuilder{BaseURL: "https://control.midnight.example"}
	body := HeartbeatRequest{AgentID: "agent/a", Sequence: 7, Status: "ok", Runtime: "docker", SentAt: time.Unix(200, 0).UTC(), HMAC: "abc"}

	req, err := builder.NewHeartbeatRequest(body)
	if err != nil {
		t.Fatalf("NewHeartbeatRequest returned error: %v", err)
	}
	if req.Method != http.MethodPost {
		t.Fatalf("method = %s, want POST", req.Method)
	}
	if req.URL.EscapedPath() != "/api/v1alpha/agents/agent%2Fa/heartbeat" {
		t.Fatalf("escaped path = %s", req.URL.EscapedPath())
	}
	if req.Header.Get(HeaderAgentID) != "agent/a" {
		t.Fatalf("agent header = %q", req.Header.Get(HeaderAgentID))
	}
}

func TestNewDesiredStateRequest(t *testing.T) {
	builder := RequestBuilder{BaseURL: "https://control.midnight.example"}

	req, err := builder.NewDesiredStateRequest(" agent-a ", `"etag-a"`)
	if err != nil {
		t.Fatalf("NewDesiredStateRequest returned error: %v", err)
	}
	if req.Method != http.MethodGet {
		t.Fatalf("method = %s, want GET", req.Method)
	}
	if req.URL.Path != "/api/v1alpha/agents/agent-a/desired" {
		t.Fatalf("path = %s", req.URL.Path)
	}
	if req.Header.Get(HeaderETag) != `"etag-a"` {
		t.Fatalf("etag header = %q", req.Header.Get(HeaderETag))
	}
	if req.Header.Get("Content-Type") != "" {
		t.Fatalf("content type = %q, want empty", req.Header.Get("Content-Type"))
	}
}

func TestRequestBuilderRejectsInvalidInputs(t *testing.T) {
	if _, err := (RequestBuilder{}).NewDesiredStateRequest("agent-a", ""); err == nil {
		t.Fatal("expected missing base URL error")
	}
	if _, err := (RequestBuilder{BaseURL: "https://control.midnight.example"}).NewHeartbeatRequest(HeartbeatRequest{}); err == nil {
		t.Fatal("expected missing agent ID error")
	}
}

func assertCommonHeaders(t *testing.T, req *http.Request, agentID string) {
	t.Helper()
	if req.Header.Get("Accept") != "application/json" {
		t.Fatalf("accept = %q", req.Header.Get("Accept"))
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", req.Header.Get("Content-Type"))
	}
	if req.Header.Get(HeaderAgentID) != agentID {
		t.Fatalf("agent header = %q", req.Header.Get(HeaderAgentID))
	}
	if req.Header.Get("Authorization") != "Bearer token-a" {
		t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("User-Agent") != "midnight-agent/test" {
		t.Fatalf("user agent = %q", req.Header.Get("User-Agent"))
	}
}
