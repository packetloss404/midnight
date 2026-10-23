package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func listServicesTool() Tool {
	return Tool{
		Name: "list_services",
		Description: "List the services inside a Midnight project. Returns service id, name, latest deployment status, " +
			"and GitHub link when present. Variable values are NOT included — use get_service for that.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"project_id": {
					"type": "string",
					"description": "The Midnight project id. If the call is implicitly scoped to a single project the caller may omit this."
				}
			},
			"required": ["project_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Services == nil {
				return nil, errors.New("list_services: services API not wired")
			}
			var args struct {
				ProjectID string `json:"project_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ProjectID == "" {
				args.ProjectID = env.ProjectScope
			}
			if args.ProjectID == "" {
				return nil, errors.New("list_services: project_id is required")
			}
			services, err := env.Services.ListServices(ctx, args.ProjectID)
			if err != nil {
				return nil, err
			}
			// Strip variable values — listing is metadata only. Variables
			// are surfaced (with values redacted) by get_service.
			for i := range services {
				services[i].Variables = nil
				services[i].Deployments = nil
			}
			return map[string]any{
				"project_id": args.ProjectID,
				"services":   services,
				"count":      len(services),
			}, nil
		},
	}
}
