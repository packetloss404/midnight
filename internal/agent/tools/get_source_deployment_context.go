package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/midnight/midnight/internal/agent"
)

func getSourceDeploymentContextTool() Tool {
	return Tool{Name: "get_source_deployment_context", Description: "Read the current exact service, environment and saved-config versions before deploy_from_source. Copy the returned expected_*_version fields into the source request; refresh after a version conflict. This does not deploy or read variable values.", InputSchema: json.RawMessage(`{"type":"object","properties":{"service_id":{"type":"string"},"environment_id":{"type":"string"}},"required":["service_id","environment_id"],"additionalProperties":false}`), Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
		reader, ok := env.Builds.(agent.ScopedSourceContext)
		if !ok {
			return nil, errors.New("scoped source context is unavailable")
		}
		var input struct {
			ServiceID     string `json:"service_id"`
			EnvironmentID string `json:"environment_id"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if len(raw) > 1024 || decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.ServiceID == "" || input.EnvironmentID == "" {
			return nil, errors.New("service_id and environment_id must identify one scoped source target")
		}
		return reader.SourceDeploymentContext(ctx, input.ServiceID, input.EnvironmentID)
	}}
}
