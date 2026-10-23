package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/agent"
)

// builtinPrefix is the reserved namespace for Midnight-supplied variables
// (MIDNIGHT_DATABASE_URL, MIDNIGHT_RAILPACK_PROVIDER, etc.). The agent
// refuses to overwrite these unless env.AllowBuiltinVar is true (the
// "--allow-builtin-override" semantics from the CLI).
const builtinPrefix = "MIDNIGHT_"

func setVariableTool() Tool {
	return Tool{
		Name: "set_variable",
		Description: "Set a single environment variable on a service, in one environment. Returns ok on success; the " +
			"value is never echoed back. Refuses names that start with MIDNIGHT_ unless the operator explicitly " +
			"enabled builtin overrides. Omit environment_id only when the service has exactly one environment. " +
			"This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id":     {"type": "string"},
				"environment_id": {"type": "string", "description": "Target environment. Optional only when the service has exactly one environment."},
				"name":           {"type": "string", "description": "Variable name; must match /^[A-Z_][A-Z0-9_]*$/."},
				"value":          {"type": "string", "description": "Variable value. Plaintext; the platform stores it encrypted and never reads it back to you."}
			},
			"required": ["service_id", "name", "value"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Services == nil {
				return nil, errors.New("set_variable: services API not wired")
			}
			var args struct {
				ServiceID     string `json:"service_id"`
				EnvironmentID string `json:"environment_id"`
				Name          string `json:"name"`
				Value         string `json:"value"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" || args.Name == "" {
				return nil, errors.New("set_variable: service_id and name are required")
			}
			if !validVarName(args.Name) {
				return nil, fmt.Errorf("set_variable: invalid variable name %q (must match /^[A-Z_][A-Z0-9_]*$/)", args.Name)
			}
			if strings.HasPrefix(args.Name, builtinPrefix) && !env.AllowBuiltinVar {
				return nil, fmt.Errorf("set_variable: %q is in the reserved MIDNIGHT_ namespace; pass --allow-builtin-override to set", args.Name)
			}
			write := agent.VariableWrite{
				ServiceID:     args.ServiceID,
				EnvironmentID: args.EnvironmentID,
				Name:          args.Name,
				Value:         args.Value,
			}
			if err := env.Services.SetVariable(ctx, write); err != nil {
				return nil, err
			}
			// Deliberately no value in the result: the tool result is
			// persisted in the conversation transcript, and a secret
			// that round-trips through the transcript is a secret in
			// the clear.
			return map[string]any{
				"service_id":     args.ServiceID,
				"environment_id": args.EnvironmentID,
				"name":           args.Name,
				"ok":             true,
			}, nil
		},
	}
}

func validVarName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
