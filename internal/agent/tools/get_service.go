package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func getServiceTool() Tool {
	return Tool{
		Name: "get_service",
		Description: "Fetch a single service by id. Returns the service metadata, recent deployments, variable names " +
			"(values are redacted server-side), and the GitHub link when present.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id": {"type": "string", "description": "The Midnight service id."}
			},
			"required": ["service_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Services == nil {
				return nil, errors.New("get_service: services API not wired")
			}
			var args struct {
				ServiceID string `json:"service_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" {
				return nil, errors.New("get_service: service_id is required")
			}
			svc, err := env.Services.GetService(ctx, args.ServiceID)
			if err != nil {
				return nil, err
			}
			// Redact variable values — only names land in the model
			// context. The ServicesAPI implementation may already strip
			// secrets, but we belt-and-brace here.
			redacted := make(map[string]string, len(svc.Variables))
			for k := range svc.Variables {
				redacted[k] = "<redacted>"
			}
			svc.Variables = redacted
			return svc, nil
		},
	}
}
