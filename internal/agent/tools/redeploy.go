package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func redeployTool() Tool {
	return Tool{
		Name: "redeploy",
		Description: "Trigger a new deployment of a service using its current source (re-runs the build with the same " +
			"ref). Returns the new deployment record. This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id": {"type": "string"}
			},
			"required": ["service_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Operations == nil {
				return nil, errors.New("redeploy: operations store not wired")
			}
			var args struct {
				ServiceID string `json:"service_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" {
				return nil, errors.New("redeploy: service_id is required")
			}
			return env.Operations.Redeploy(ctx, args.ServiceID)
		},
	}
}
