// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/midnight/midnight/internal/agent"
	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/agent/diagnosis"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/redaction"
	"github.com/midnight/midnight/pkg/orailtypes"
)

// workerDiagnosisOptions supplies durable context and the same event store the
// operation API reads. There is no operations.metadata column: the persisted
// diagnosis_attached event is the operation's link to its completed report.
func workerDiagnosisOptions(pool *pg.Pool, events diagnosis.EventAppender, caller agent.Caller, model string) (diagnosis.Options, error) {
	if pool == nil || events == nil {
		return diagnosis.Options{}, fmt.Errorf("diagnosis persistence is unavailable")
	}
	adapted, err := diagnosis.NewAgentCaller(caller, diagnosis.AgentCallerOptions{})
	if err != nil {
		return diagnosis.Options{}, err
	}
	service, err := conversation.NewService(conversation.Config{Store: conversation.NewPostgresStore(pool)})
	if err != nil {
		return diagnosis.Options{}, err
	}
	contextStore := workerDiagnosisContext{pool: pool}
	return diagnosis.Options{
		Caller: adapted, Model: model,
		Conversations: diagnosis.NewConversationStore(service, "agent:diagnosis", model),
		Services:      contextStore, Logs: contextStore, Events: events,
	}, nil
}

type workerDiagnosisContext struct{ pool *pg.Pool }

// Resolve derives tenant lineage from the operation's unique canonical
// deployment, never from free-form event strings or model/request metadata.
// Only variable names and a spec hash leave this store; ciphertext and values
// are deliberately never read.
func (s workerDiagnosisContext) Resolve(ctx context.Context, op orailtypes.Operation) (diagnosis.ServiceContext, error) {
	var scope diagnosis.ServiceContext
	var spec []byte
	err := s.pool.QueryRow(ctx, `
SELECT p.workspace_id, d.project_id, d.environment_id, d.service_id, svc.slug, d.id, svc.spec,
       COALESCE((SELECT previous.id FROM deployments previous
                 WHERE previous.service_id=d.service_id AND previous.environment_id=d.environment_id
                   AND previous.state IN ('active','superseded') AND previous.created_at<d.created_at
                 ORDER BY previous.created_at DESC, previous.id DESC LIMIT 1), '')
FROM deployments d
JOIN services svc ON svc.id=d.service_id AND svc.project_id=d.project_id AND svc.deleted_at IS NULL
JOIN projects p ON p.id=d.project_id AND p.deleted_at IS NULL
JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
JOIN environments e ON e.id=d.environment_id AND e.project_id=d.project_id AND e.deleted_at IS NULL
WHERE d.operation_id=$1`, string(op.ID)).Scan(&scope.WorkspaceID, &scope.ProjectID, &scope.EnvironmentID,
		&scope.ServiceID, &scope.ServiceSlug, &scope.DeploymentID, &spec, &scope.PreviousDeploy)
	if err != nil {
		return diagnosis.ServiceContext{}, fmt.Errorf("resolve diagnosis deployment: %w", err)
	}
	scope.SpecDigest = fmt.Sprintf("sha256:%x", sha256.Sum256(spec))
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT name FROM service_variables
WHERE project_id=$1 AND environment_id=$2 AND service_id IN ('',$3) AND deleted_at IS NULL
ORDER BY name LIMIT 200`, scope.ProjectID, scope.EnvironmentID, scope.ServiceID)
	if err != nil {
		return diagnosis.ServiceContext{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return diagnosis.ServiceContext{}, err
		}
		scope.Variables = append(scope.Variables, name)
	}
	return scope, rows.Err()
}

func (s workerDiagnosisContext) BuildLogTail(ctx context.Context, deploymentID string, maxLines int) ([]string, error) {
	return s.logTail(ctx, deploymentID, "build", maxLines)
}

func (s workerDiagnosisContext) RuntimeLogTail(ctx context.Context, deploymentID string, maxLines int) ([]string, error) {
	return s.logTail(ctx, deploymentID, "runtime", maxLines)
}

func (s workerDiagnosisContext) logTail(ctx context.Context, deploymentID, surface string, maxLines int) ([]string, error) {
	if deploymentID == "" || maxLines <= 0 {
		return nil, nil
	}
	maxLines = min(maxLines, diagnosis.MaxBuildLogLines)
	// Newest first keeps work bounded even for a noisy deployment; reverse the
	// selected tail before rendering. Exact lineage excludes unrelated logs.
	rows, err := s.pool.Query(ctx, `
SELECT LEFT(l.message,4096) FROM observability_log_records l
JOIN deployments d ON d.id=l.deployment_id AND d.project_id=l.project_id
 AND d.service_id=l.service_id AND d.environment_id=l.environment_id
WHERE d.id=$1 AND l.surface=$2
ORDER BY l.id DESC LIMIT $3`, deploymentID, surface, maxLines)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lines []string
	redactor := redaction.DefaultRedactor()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		// Also scrub legacy records written before ingest-time redaction.
		line = redactor.RedactString((build.Redactor{}).RedactText(line))
		lines = append(lines, line)
	}
	for left, right := 0, len(lines)-1; left < right; left, right = left+1, right-1 {
		lines[left], lines[right] = lines[right], lines[left]
	}
	return lines, rows.Err()
}
