package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func rollbackTool() Tool {
	return Tool{
		Name: "rollback",
		Description: "Roll back to a previously-successful deployment by id. The named deployment becomes the active " +
			"one and a fresh deployment record is created pointing at the same artifact. This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"deployment_id": {"type": "string", "description": "The id of a prior, successful deployment to roll back to."}
			},
			"required": ["deployment_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Operations == nil {
				return nil, errors.New("rollback: operations store not wired")
			}
			var args struct {
				DeploymentID string `json:"deployment_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.DeploymentID == "" {
				return nil, errors.New("rollback: deployment_id is required")
			}
			return env.Operations.Rollback(ctx, args.DeploymentID)
		},
	}
}
