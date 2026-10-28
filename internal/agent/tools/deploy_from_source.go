package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/midnight/midnight/internal/agent"
)

func deployFromSourceTool() Tool {
	return Tool{
		Name: "deploy_from_source",
		Description: "Build and deploy a bounded UTF-8 source file tree to the exact service and environment. " +
			"First call get_source_deployment_context and copy its exact expected_*_version fields. " +
			"Use a stable request_key for retries and a new key for a new logical build. Files are relative paths; " +
			"host paths, repository URLs, and GitHub authority are never accepted. Returns queued deployment metadata; " +
			"poll get_deployment/get_logs for completion. This tool MUTATES state.",
		Mutates: true,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"service_id": {"type": "string"},
				"environment_id": {"type": "string"},
				"request_key": {"type": "string", "description": "Stable per logical build; 1-128 letters, numbers, dots, hyphens or underscores."},
				"expected_service_version": {"type":"integer","minimum":1},
				"expected_environment_version": {"type":"integer","minimum":1},
				"expected_config_version": {"type":"integer","minimum":0,"description":"Version zero means the default config has no persisted row."},
				"files": {"type": "object", "description": "1-64 relative POSIX paths to UTF-8 file contents; at most 128 KiB per file and 512 KiB total.", "additionalProperties": {"type":"string"}}
			},
			"required": ["service_id", "environment_id", "request_key", "expected_service_version", "expected_environment_version", "expected_config_version", "files"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage, env agent.Env) (any, error) {
			intake, ok := env.Builds.(agent.ScopedBuildIntake)
			if !ok {
				return nil, errors.New("deploy_from_source: scoped durable source intake is unavailable")
			}
			if len(raw) > 1<<20 {
				return nil, errors.New("deploy_from_source: source request exceeds 1 MiB")
			}
			var request agent.SourceDeployRequest
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil {
				return nil, errors.New("deploy_from_source: invalid source request")
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				return nil, errors.New("deploy_from_source: one JSON request is required")
			}
			// Config version zero is meaningful, so distinguish an explicit
			// snapshot value from an omitted or null JSON field.
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil || len(fields["expected_config_version"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["expected_config_version"]), []byte("null")) {
				return nil, errors.New("deploy_from_source: expected_config_version from the source context is required")
			}
			if err := agent.ValidateSourceDeployRequest(request); err != nil {
				return nil, err
			}
			return intake.DeployScopedSource(ctx, request)
		},
	}
}
