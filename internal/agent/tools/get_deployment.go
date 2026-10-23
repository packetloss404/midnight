package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func getDeploymentTool() Tool {
	return Tool{
		Name: "get_deployment",
		Description: "Fetch a single deployment by id. Returns status, route, commit SHA, started/finished timestamps, " +
			"and the head of the deployment log so the model can reason about failures without a separate logs call.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"deployment_id": {"type": "string", "description": "The Midnight deployment id."}
			},
			"required": ["deployment_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Operations == nil {
				return nil, errors.New("get_deployment: operations store not wired")
			}
			var args struct {
				DeploymentID string `json:"deployment_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.DeploymentID == "" {
				return nil, errors.New("get_deployment: deployment_id is required")
			}
			return env.Operations.GetDeployment(ctx, args.DeploymentID)
		},
	}
}
