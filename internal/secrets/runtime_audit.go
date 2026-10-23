// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/midnight/midnight/internal/audit"
)

type decryptAuditAppender interface {
	Append(context.Context, audit.Row) (audit.Row, error)
}

const (
	DecryptExecutionDeploy = "deploy"
	DecryptExecutionBuild  = "build"
	DecryptExecutionCron   = "cron"
)

// DecryptAuditExecution identifies the immutable worker attempt whose
// injection boundary requested plaintext. It contains no variable name or
// value. ExecutionID is a leased queue-job ID for deploy/build work and a
// cron-occurrence ID for cron work.
type DecryptAuditExecution struct {
	Kind         string
	ExecutionID  string
	DeploymentID string
	OperationID  string
	Attempt      int
}

type decryptAuditExecutionContextKey struct{}

// WithDecryptAuditExecution binds redaction-safe execution correlation to the
// context used for variable resolution. Production audit sinks fail closed
// when this correlation is absent or inconsistent.
func WithDecryptAuditExecution(ctx context.Context, execution DecryptAuditExecution) context.Context {
	return context.WithValue(ctx, decryptAuditExecutionContextKey{}, execution)
}

// PostgresDecryptAuditSink adapts the integrity-protected audit chain to the
// runtime/build decrypt boundary. Its payload contains only the exact
// redaction-safe Vault audit record plus immutable variable metadata.
type PostgresDecryptAuditSink struct {
	Chain   decryptAuditAppender
	ActorID string
}

// NewPostgresDecryptAuditSink constructs worker-owned decrypt evidence.
func NewPostgresDecryptAuditSink(chain decryptAuditAppender, actorID string) *PostgresDecryptAuditSink {
	return &PostgresDecryptAuditSink{Chain: chain, ActorID: strings.TrimSpace(actorID)}
}

// RecordDecrypt persists one allowed decrypt before plaintext is returned to
// the injection resolver.
func (s *PostgresDecryptAuditSink) RecordDecrypt(
	ctx context.Context,
	meta VariableMeta,
	version int,
	record AuditDecryptRecord,
) error {
	if s == nil || s.Chain == nil {
		return errors.New("variable decrypt audit chain is required")
	}
	if s.ActorID == "" {
		return errors.New("variable decrypt audit actor is required")
	}
	if meta.ID == "" || record.VariableID != meta.ID || version < 1 || meta.Version != version {
		return ErrVariableScopeIntegrity
	}
	if record.Scope != meta.Scope || record.Resolved == "" ||
		record.KEKID == "" || record.KEKFingerprint == "" || record.Bucket == "" {
		return ErrVariableScopeIntegrity
	}
	execution, ok := ctx.Value(decryptAuditExecutionContextKey{}).(DecryptAuditExecution)
	if !ok || !validDecryptAuditExecution(execution, record.Resolved) {
		return errors.New("variable decrypt audit execution context is required")
	}
	attrs := map[string]any{
		"scope":            string(record.Scope),
		"resolved":         string(record.Resolved),
		"kek_id":           string(record.KEKID),
		"kek_fingerprint":  record.KEKFingerprint,
		"length_bucket":    record.Bucket,
		"variable_version": version,
		"service_id":       meta.Service,
		"execution_kind":   execution.Kind,
		"execution_id":     execution.ExecutionID,
		"deployment_id":    execution.DeploymentID,
		"attempt":          execution.Attempt,
	}
	if execution.OperationID != "" {
		attrs["operation_id"] = execution.OperationID
	}
	_, err := s.Chain.Append(ctx, audit.Row{
		RequestID: fmt.Sprintf(
			"variable-decrypt:%s:%s:%d",
			execution.Kind,
			execution.ExecutionID,
			execution.Attempt,
		),
		ActorID:        s.ActorID,
		ActorType:      "system",
		AuthMethod:     "worker",
		WorkspaceID:    meta.Workspace,
		ProjectID:      meta.Project,
		EnvironmentID:  meta.Environment,
		ResourceKind:   "variable",
		ResourceID:     string(meta.ID),
		Action:         "variable.decrypt",
		PolicyDecision: "allowed",
		Attrs:          attrs,
	})
	if err != nil {
		return fmt.Errorf("append variable decrypt audit: %w", err)
	}
	return nil
}

func validDecryptAuditExecution(execution DecryptAuditExecution, purpose ResolvedScope) bool {
	if execution.Attempt < 1 ||
		!validDecryptAuditExecutionID(execution.ExecutionID) ||
		!validDecryptAuditExecutionID(execution.DeploymentID) ||
		(execution.OperationID != "" && !validDecryptAuditExecutionID(execution.OperationID)) {
		return false
	}
	switch execution.Kind {
	case DecryptExecutionBuild:
		return purpose == ScopeBuildInject && execution.OperationID != ""
	case DecryptExecutionDeploy:
		return purpose == ScopeRuntimeInject && execution.OperationID != ""
	case DecryptExecutionCron:
		return purpose == ScopeRuntimeInject && execution.OperationID == ""
	default:
		return false
	}
}

func validDecryptAuditExecutionID(value string) bool {
	return value != "" &&
		len(value) <= 255 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

var _ DecryptAuditSink = (*PostgresDecryptAuditSink)(nil)
