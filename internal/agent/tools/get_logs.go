package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

const (
	defaultLogLines = 200
	maxLogLines     = 2000
)

func getLogsTool() Tool {
	return Tool{
		Name: "get_logs",
		Description: "Fetch logs for a deployment. Set lines (1-2000) for the tail size and optional search to grep " +
			"(case-insensitive substring). Returns the matching log lines ordered oldest first.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"deployment_id": {"type": "string", "description": "The Midnight deployment id."},
				"lines": {"type": "integer", "minimum": 1, "maximum": 2000, "description": "How many tail lines to fetch. Defaults to 200."},
				"search": {"type": "string", "description": "Optional case-insensitive substring filter."}
			},
			"required": ["deployment_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Operations == nil {
				return nil, errors.New("get_logs: operations store not wired")
			}
			var args struct {
				DeploymentID string `json:"deployment_id"`
				Lines        int    `json:"lines"`
				Search       string `json:"search"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.DeploymentID == "" {
				return nil, errors.New("get_logs: deployment_id is required")
			}
			if args.Lines <= 0 {
				args.Lines = defaultLogLines
			}
			if args.Lines > maxLogLines {
				args.Lines = maxLogLines
			}
			lines, err := env.Operations.GetLogs(ctx, args.DeploymentID, args.Lines, args.Search)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"deployment_id": args.DeploymentID,
				"lines":         lines,
				"count":         len(lines),
				"search":        args.Search,
			}, nil
		},
	}
}
