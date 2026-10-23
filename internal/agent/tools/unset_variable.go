package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/agent"
)

func unsetVariableTool() Tool {
	return Tool{
		Name: "unset_variable",
		Description: "Remove a single environment variable from a service, in one environment. Returns ok on success. " +
			"Refuses names starting with MIDNIGHT_ unless the operator explicitly enabled builtin overrides. Omit " +
			"environment_id only when the service has exactly one environment. This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id":     {"type": "string"},
				"environment_id": {"type": "string", "description": "Target environment. Optional only when the service has exactly one environment."},
				"name":           {"type": "string"}
			},
			"required": ["service_id", "name"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Services == nil {
				return nil, errors.New("unset_variable: services API not wired")
			}
			var args struct {
				ServiceID     string `json:"service_id"`
				EnvironmentID string `json:"environment_id"`
				Name          string `json:"name"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" || args.Name == "" {
				return nil, errors.New("unset_variable: service_id and name are required")
			}
			if strings.HasPrefix(args.Name, builtinPrefix) && !env.AllowBuiltinVar {
				return nil, fmt.Errorf("unset_variable: %q is in the reserved MIDNIGHT_ namespace; pass --allow-builtin-override to unset", args.Name)
			}
			write := agent.VariableWrite{
				ServiceID:     args.ServiceID,
				EnvironmentID: args.EnvironmentID,
				Name:          args.Name,
			}
			if err := env.Services.UnsetVariable(ctx, write); err != nil {
				return nil, err
			}
			return map[string]any{
				"service_id":     args.ServiceID,
				"environment_id": args.EnvironmentID,
				"name":           args.Name,
				"ok":             true,
			}, nil
		},
	}
}
