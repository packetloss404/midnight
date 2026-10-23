package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func deployFromSourceTool() Tool {
	return Tool{
		Name: "deploy_from_source",
		Description: "Kick off a new build for a service from a repo + git ref. Returns the in-flight deployment " +
			"record so the agent can poll get_deployment/get_logs. This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id": {"type": "string"},
				"repo":       {"type": "string", "description": "Repository identifier (e.g. \"owner/name\" or full clone URL)."},
				"ref":        {"type": "string", "description": "Git ref (branch, tag, or commit SHA)."}
			},
			"required": ["service_id", "repo", "ref"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			if env.Builds == nil {
				return nil, errors.New("deploy_from_source: build intake not wired")
			}
			var args struct {
				ServiceID string `json:"service_id"`
				Repo      string `json:"repo"`
				Ref       string `json:"ref"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" || args.Repo == "" || args.Ref == "" {
				return nil, errors.New("deploy_from_source: service_id, repo, and ref are all required")
			}
			return env.Builds.DeployFromSource(ctx, args.ServiceID, args.Repo, args.Ref)
		},
	}
}
