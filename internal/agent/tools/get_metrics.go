package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/midnight/midnight/internal/agent"
)

func getMetricsTool() Tool {
	return Tool{
		Name: "get_metrics",
		Description: "Fetch the latest CPU / memory / restart counters for a service. When the metrics store is not " +
			"yet wired this returns a friendly \"metrics not yet wired\" payload — the model should not retry.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id": {"type": "string"}
			},
			"required": ["service_id"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			var args struct {
				ServiceID string `json:"service_id"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, err
			}
			if args.ServiceID == "" {
				return nil, errors.New("get_metrics: service_id is required")
			}
			if env.Metrics == nil {
				return map[string]any{
					"service_id": args.ServiceID,
					"available":  false,
					"message":    "metrics not yet wired",
				}, nil
			}
			m, err := env.Metrics.GetServiceMetrics(ctx, args.ServiceID)
			if err != nil {
				if errors.Is(err, agent.ErrMetricsUnavailable) {
					return map[string]any{
						"service_id": args.ServiceID,
						"available":  false,
						"message":    "metrics not yet wired",
					}, nil
				}
				return nil, err
			}
			return map[string]any{
				"service_id": args.ServiceID,
				"available":  true,
				"metrics":    m,
			}, nil
		},
	}
}
