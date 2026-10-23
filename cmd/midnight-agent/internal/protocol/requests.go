package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	RegistrationPath = "/api/v1alpha/agents/register"
	HeartbeatPath    = "/api/v1alpha/agents/%s/heartbeat"
	DesiredStatePath = "/api/v1alpha/agents/%s/desired"
	StatusReportPath = "/api/v1alpha/agents/%s/status"

	HeaderAgentID  = "X-Midnight-Agent-ID"
	HeaderETag     = "If-None-Match"
	HeaderAgentMAC = "X-Midnight-Agent-MAC"
	HeaderAgentVer = "X-Midnight-Agent-Version"
	HeaderHMACSeq  = "X-Midnight-Agent-Seq"
)

// RequestBuilder constructs agent control-plane HTTP requests without sending them.
type RequestBuilder struct {
	BaseURL   string
	Token     string
	UserAgent string
}

// NewRegistrationRequest builds the initial agent registration request.
func (b RequestBuilder) NewRegistrationRequest(body RegistrationRequest) (*http.Request, error) {
	return b.newJSONRequest(http.MethodPost, RegistrationPath, body.AgentID, body)
}

// NewHeartbeatRequest builds a heartbeat liveness request for an already registered agent.
func (b RequestBuilder) NewHeartbeatRequest(body HeartbeatRequest) (*http.Request, error) {
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	return b.newJSONRequest(http.MethodPost, fmt.Sprintf(HeartbeatPath, url.PathEscape(agentID)), agentID, body)
}

// NewStatusReportRequest builds a status-report request for the §1.16
// endpoint.
func (b RequestBuilder) NewStatusReportRequest(body StatusReport) (*http.Request, error) {
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	return b.newJSONRequest(http.MethodPost, fmt.Sprintf(StatusReportPath, url.PathEscape(agentID)), agentID, body)
}

// NewDesiredStateRequest builds a desired-state polling request.
func (b RequestBuilder) NewDesiredStateRequest(agentID, etag string) (*http.Request, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	req, err := b.newRequest(http.MethodGet, fmt.Sprintf(DesiredStatePath, url.PathEscape(agentID)), agentID, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(etag) != "" {
		req.Header.Set(HeaderETag, strings.TrimSpace(etag))
	}
	return req, nil
}

func (b RequestBuilder) newJSONRequest(method, path, agentID string, body any) (*http.Request, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	req, err := b.newRequest(method, path, agentID, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (b RequestBuilder) newRequest(method, path, agentID string, body *bytes.Reader) (*http.Request, error) {
	endpoint, err := joinBasePath(b.BaseURL, path)
	if err != nil {
		return nil, err
	}
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = body
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(agentID) != "" {
		req.Header.Set(HeaderAgentID, strings.TrimSpace(agentID))
	}
	if strings.TrimSpace(b.Token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(b.Token))
	}
	if strings.TrimSpace(b.UserAgent) != "" {
		req.Header.Set("User-Agent", strings.TrimSpace(b.UserAgent))
	}
	return req, nil
}

func joinBasePath(baseURL, path string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("base url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("base url must be absolute")
	}
	joined, err := url.JoinPath(parsed.String(), path)
	if err != nil {
		return "", fmt.Errorf("join request path: %w", err)
	}
	return joined, nil
}
