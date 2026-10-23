package midnightctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/midnight/midnight/pkg/agentapi"
)

// AgentCredential is an installation-managed bearer credential scoped to one
// runtime agent. It is deliberately distinct from the human/operator token
// accepted by WithBearerToken.
type AgentCredential string

// Desired-state polling returns an intentionally extensible raw payload, so
// this response helper remains owned by the shared runtime-agent package. The
// concrete register/heartbeat/status DTOs are generated from canonical
// OpenAPI alongside the request methods below.
type AgentDesiredStateResponse = agentapi.DesiredStateResponse

var errInvalidAgentCredential = errors.New("midnightctl: agent id and credential must be non-empty, trimmed, and contain no line breaks")

// WithAgentCredential authenticates generated-client requests as exactly one
// runtime agent. Item-route callers must still use the same agentID in the
// generated method's path argument; the server enforces that binding.
func WithAgentCredential(agentID string, credential AgentCredential) ClientOption {
	return func(client *Client) error {
		credentialValue := string(credential)
		if strings.TrimSpace(agentID) == "" || strings.TrimSpace(agentID) != agentID || strings.ContainsAny(agentID, "\r\n") ||
			strings.TrimSpace(credentialValue) == "" || strings.TrimSpace(credentialValue) != credentialValue || strings.ContainsAny(credentialValue, "\r\n") {
			return errInvalidAgentCredential
		}
		client.RequestEditors = append(client.RequestEditors, func(_ context.Context, request *http.Request) error {
			request.Header.Set("Authorization", "Bearer "+credentialValue)
			request.Header.Set(agentapi.HeaderAgentID, agentID)
			return nil
		})
		return nil
	}
}

// RegisterRuntimeAgentWithResponse sends the concrete shared registration DTO
// through the generated response-aware client.
func (client *ClientWithResponses) RegisterRuntimeAgentWithResponse(ctx context.Context, request AgentRegistrationRequest, editors ...RequestEditorFn) (*RegisterAgentResponse, error) {
	body, err := marshalAgentBody(request)
	if err != nil {
		return nil, err
	}
	return client.RegisterAgentWithBodyWithResponse(ctx, "application/json", body, editors...)
}

// RecordRuntimeAgentHeartbeatWithResponse sends the concrete shared heartbeat
// DTO through the generated response-aware client.
func (client *ClientWithResponses) RecordRuntimeAgentHeartbeatWithResponse(ctx context.Context, agentID string, request AgentHeartbeatRequest, editors ...RequestEditorFn) (*RecordAgentHeartbeatResponse, error) {
	body, err := marshalAgentBody(request)
	if err != nil {
		return nil, err
	}
	return client.RecordAgentHeartbeatWithBodyWithResponse(ctx, agentID, "application/json", body, editors...)
}

// RecordRuntimeAgentStatusWithResponse sends the concrete shared status DTO
// through the generated response-aware client.
func (client *ClientWithResponses) RecordRuntimeAgentStatusWithResponse(ctx context.Context, agentID string, request AgentStatusReport, editors ...RequestEditorFn) (*RecordAgentStatusResponse, error) {
	body, err := marshalAgentBody(request)
	if err != nil {
		return nil, err
	}
	return client.RecordAgentStatusWithBodyWithResponse(ctx, agentID, "application/json", body, editors...)
}

func marshalAgentBody(value any) (*bytes.Reader, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(body), nil
}
