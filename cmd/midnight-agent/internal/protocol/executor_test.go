package protocol

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type fakeDoer func(*http.Request) (*http.Response, error)

func (f fakeDoer) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientExecuteDecodesRegistrationResponse(t *testing.T) {
	req := mustRequest(t, http.MethodPost, "https://control.midnight.example/api/v1/agents/register")
	client := Client{Doer: fakeDoer(func(got *http.Request) (*http.Response, error) {
		if got != req {
			t.Fatalf("request was not passed through")
		}
		return jsonResponse(http.StatusCreated, `{"agent_id":"agent-a","heartbeat_interval":30000000000,"desired_state_url":"/desired"}`), nil
	})}

	var got RegistrationResponse
	meta, err := client.Execute(req, &got)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if meta.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", meta.StatusCode, http.StatusCreated)
	}
	if got.AgentID != "agent-a" || got.HeartbeatInterval != 30*time.Second || got.DesiredStateURL != "/desired" {
		t.Fatalf("response = %+v", got)
	}
}

func TestClientExecuteParsesErrorEnvelope(t *testing.T) {
	req := mustRequest(t, http.MethodPost, "https://control.midnight.example/api/v1/agents/register")
	client := Client{Doer: fakeDoer(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusConflict, `{"error":{"code":"agent_conflict","message":"agent already registered","request_id":"req-a","docs_url":"https://docs.example/errors/agent_conflict","details":{"agent_id":"agent-a"}}}`), nil
	})}

	_, err := client.Execute(req, &RegistrationResponse{})
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusConflict || httpErr.Code != "agent_conflict" || httpErr.Message != "agent already registered" || httpErr.RequestID != "req-a" {
		t.Fatalf("http error = %+v", httpErr)
	}
	if httpErr.Details["agent_id"] != "agent-a" {
		t.Fatalf("details = %+v", httpErr.Details)
	}
}

func TestClientExecuteDesiredStateReturnsRawBodyAndETag(t *testing.T) {
	req := mustRequest(t, http.MethodGet, "https://control.midnight.example/api/v1/agents/agent-a/desired-state")
	client := Client{Doer: fakeDoer(func(*http.Request) (*http.Response, error) {
		resp := jsonResponse(http.StatusOK, `{"deployments":[]}`)
		resp.Header.Set("ETag", `"etag-a"`)
		return resp, nil
	})}

	got, err := client.ExecuteDesiredState(req)
	if err != nil {
		t.Fatalf("ExecuteDesiredState returned error: %v", err)
	}
	if got.ETag != `"etag-a"` || got.NotModified {
		t.Fatalf("desired-state metadata = %+v", got)
	}
	if string(got.Raw) != `{"deployments":[]}` {
		t.Fatalf("raw = %s", string(got.Raw))
	}
}

func TestClientExecuteDesiredStateHandlesNotModified(t *testing.T) {
	req := mustRequest(t, http.MethodGet, "https://control.midnight.example/api/v1/agents/agent-a/desired-state")
	client := Client{Doer: fakeDoer(func(*http.Request) (*http.Response, error) {
		resp := jsonResponse(http.StatusNotModified, "")
		resp.Header.Set("ETag", `"etag-a"`)
		return resp, nil
	})}

	got, err := client.ExecuteDesiredState(req)
	if err != nil {
		t.Fatalf("ExecuteDesiredState returned error: %v", err)
	}
	if got.ETag != `"etag-a"` || !got.NotModified || got.Raw != nil {
		t.Fatalf("desired-state response = %+v", got)
	}
}

func TestClientRejectsMissingDoer(t *testing.T) {
	req := mustRequest(t, http.MethodGet, "https://control.midnight.example")
	if _, err := (Client{}).Execute(req, nil); err == nil {
		t.Fatal("expected missing doer error")
	}
}

func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return req
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
