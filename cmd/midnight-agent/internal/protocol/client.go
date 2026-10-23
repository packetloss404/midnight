package protocol

import "fmt"

// AgentClient composes request construction with an injected executor.
type AgentClient struct {
	Builder  RequestBuilder
	Executor Executor
}

// Register announces this agent and decodes the registration response.
func (c AgentClient) Register(body RegistrationRequest) (RegistrationResponse, ResponseMeta, error) {
	if c.Executor == nil {
		return RegistrationResponse{}, ResponseMeta{}, fmt.Errorf("executor is required")
	}
	req, err := c.Builder.NewRegistrationRequest(body)
	if err != nil {
		return RegistrationResponse{}, ResponseMeta{}, err
	}
	var out RegistrationResponse
	meta, err := c.Executor.Execute(req, &out)
	if err != nil {
		return RegistrationResponse{}, meta, err
	}
	return out, meta, nil
}

// Heartbeat reports liveness and decodes the heartbeat acknowledgement.
func (c AgentClient) Heartbeat(body HeartbeatRequest) (HeartbeatResponse, ResponseMeta, error) {
	if c.Executor == nil {
		return HeartbeatResponse{}, ResponseMeta{}, fmt.Errorf("executor is required")
	}
	req, err := c.Builder.NewHeartbeatRequest(body)
	if err != nil {
		return HeartbeatResponse{}, ResponseMeta{}, err
	}
	var out HeartbeatResponse
	meta, err := c.Executor.Execute(req, &out)
	if err != nil {
		return HeartbeatResponse{}, meta, err
	}
	return out, meta, nil
}

// ReportStatus pushes the latest observed-state batch to the control plane.
func (c AgentClient) ReportStatus(body StatusReport) (StatusReportResponse, ResponseMeta, error) {
	if c.Executor == nil {
		return StatusReportResponse{}, ResponseMeta{}, fmt.Errorf("executor is required")
	}
	req, err := c.Builder.NewStatusReportRequest(body)
	if err != nil {
		return StatusReportResponse{}, ResponseMeta{}, err
	}
	var out StatusReportResponse
	meta, err := c.Executor.Execute(req, &out)
	if err != nil {
		return StatusReportResponse{}, meta, err
	}
	return out, meta, nil
}

// FetchDesired polls desired state using the supplied cached ETag.
func (c AgentClient) FetchDesired(agentID, etag string) (DesiredStateResponse, error) {
	if c.Executor == nil {
		return DesiredStateResponse{}, fmt.Errorf("executor is required")
	}
	req, err := c.Builder.NewDesiredStateRequest(agentID, etag)
	if err != nil {
		return DesiredStateResponse{}, err
	}
	return c.Executor.ExecuteDesiredState(req)
}
