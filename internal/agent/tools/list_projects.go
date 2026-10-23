package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

// listProjectsTool returns every project visible to the current actor.
// When env.ProjectScope is set, the result is filtered to that project so
// the agent has a single project view in dashboards scoped that way.
func listProjectsTool() Tool {
	return Tool{
		Name: "list_projects",
		Description: "List every Midnight project visible to the caller. Returns project id, name, slug, " +
			"workspace id, and creation timestamp. Use this to enumerate projects before drilling into services.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {},
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, _ json.RawMessage, env agent.Env) (any, error) {
			if env.Services == nil {
				return nil, errors.New("list_projects: services API not wired")
			}
			projects, err := env.Services.ListProjects(ctx)
			if err != nil {
				return nil, err
			}
			if env.ProjectScope != "" {
				filtered := projects[:0]
				for _, p := range projects {
					if p.ID == env.ProjectScope {
						filtered = append(filtered, p)
					}
				}
				projects = filtered
			}
			return map[string]any{"projects": projects, "count": len(projects)}, nil
		},
	}
}
