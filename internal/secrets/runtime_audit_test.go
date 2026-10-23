// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/varrefs"
)

func TestAuditedRuntimeLookupPersistsRedactedDecryptEvidence(t *testing.T) {
	ctx := WithDecryptAuditExecution(context.Background(), DecryptAuditExecution{
		Kind:         DecryptExecutionDeploy,
		ExecutionID:  "job-deploy-one",
		DeploymentID: "dep_one",
		OperationID:  "op_one",
		Attempt:      1,
	})
	provider, err := NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(provider, AllowAll{})
	const plaintext = "sensitive-runtime-value"
	if _, err := vault.Set(ctx, VariableInput{
		Name:        "TOKEN",
		Workspace:   "wrk_one",
		Project:     "prj_one",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSecret,
		Plaintext:   []byte(plaintext),
		ValueKind:   "literal",
	}); err != nil {
		t.Fatal(err)
	}
	chain := audit.NewMemoryChain()
	sink := NewPostgresDecryptAuditSink(chain, "deploy-worker-one")
	resolver := varrefs.NewResolver(NewAuditedRuntimeLookup(vault, nil, sink))
	env, err := resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if env["TOKEN"] != plaintext {
		t.Fatalf("TOKEN=%q", env["TOKEN"])
	}
	rows, err := chain.Read(ctx, audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if row.ActorID != "deploy-worker-one" || row.Action != "variable.decrypt" ||
		row.ProjectID != "prj_one" || row.EnvironmentID != "env_prod" ||
		row.ResourceKind != "variable" || row.ResourceID == "" ||
		row.PolicyDecision != "allowed" ||
		row.Attrs["resolved"] != string(ScopeRuntimeInject) ||
		row.Attrs["variable_version"] != 1 ||
		row.Attrs["execution_kind"] != DecryptExecutionDeploy ||
		row.Attrs["execution_id"] != "job-deploy-one" ||
		row.Attrs["deployment_id"] != "dep_one" ||
		row.Attrs["operation_id"] != "op_one" ||
		row.Attrs["attempt"] != 1 {
		t.Fatalf("audit row=%#v", row)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), plaintext) || strings.Contains(string(encoded), "TOKEN") {
		t.Fatalf("decrypt audit leaked variable name or plaintext: %s", encoded)
	}
}

type failingDecryptAuditSink struct{}

func (failingDecryptAuditSink) RecordDecrypt(context.Context, VariableMeta, int, AuditDecryptRecord) error {
	return errors.New("audit unavailable")
}

func TestAuditedRuntimeLookupFailsClosedWhenAuditUnavailable(t *testing.T) {
	ctx := context.Background()
	provider, err := NewMemoryKeyProvider([]byte("fedcba9876543210fedcba9876543210"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(provider, AllowAll{})
	if _, err := vault.Set(ctx, VariableInput{
		Name:        "TOKEN",
		Project:     "prj_one",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSecret,
		Plaintext:   []byte("must-not-cross-boundary"),
		ValueKind:   "literal",
	}); err != nil {
		t.Fatal(err)
	}

	for _, sink := range []DecryptAuditSink{nil, failingDecryptAuditSink{}} {
		resolver := varrefs.NewResolver(NewAuditedRuntimeLookup(vault, nil, sink))
		env, err := resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api")
		if err == nil || env != nil {
			t.Fatalf("Resolve with unavailable audit = %#v, %v; want fail closed", env, err)
		}
	}
}

func TestVariableInfrastructureErrorRedactsWrappedCause(t *testing.T) {
	cause := errors.New("open /etc/midnight/kek: permission denied")
	err := variableInfrastructureError(cause)
	if got := err.Error(); got != "midnight: variable infrastructure failure" {
		t.Fatalf("Error() = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause is unavailable to trusted diagnostics")
	}
}

func TestPostgresDecryptAuditSinkRequiresExecutionContext(t *testing.T) {
	ctx := context.Background()
	provider, err := NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(provider, AllowAll{})
	if _, err := vault.Set(ctx, VariableInput{
		Name:        "TOKEN",
		Workspace:   "wrk_one",
		Project:     "prj_one",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSecret,
		Plaintext:   []byte("must-not-cross-boundary"),
		ValueKind:   "literal",
	}); err != nil {
		t.Fatal(err)
	}
	resolver := varrefs.NewResolver(NewAuditedRuntimeLookup(
		vault,
		nil,
		NewPostgresDecryptAuditSink(audit.NewMemoryChain(), "deploy-worker-one"),
	))
	env, err := resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api")
	if err == nil || env != nil {
		t.Fatalf("Resolve without execution context = %#v, %v; want fail closed", env, err)
	}
}

func TestPostgresDecryptAuditSinkDistinguishesWorkerAttempts(t *testing.T) {
	baseCtx := context.Background()
	provider, err := NewMemoryKeyProvider([]byte("0123456789abcdef0123456789abcdef"), 1)
	if err != nil {
		t.Fatal(err)
	}
	vault := NewVault(provider, AllowAll{})
	if _, err := vault.Set(baseCtx, VariableInput{
		Name:        "TOKEN",
		Workspace:   "wrk_one",
		Project:     "prj_one",
		Environment: "env_prod",
		Service:     "svc_api",
		Scope:       ScopeSecret,
		Plaintext:   []byte("same-encrypted-version"),
		ValueKind:   "literal",
	}); err != nil {
		t.Fatal(err)
	}
	chain := audit.NewMemoryChain()
	resolver := varrefs.NewResolver(NewAuditedRuntimeLookup(
		vault,
		nil,
		NewPostgresDecryptAuditSink(chain, "deploy-worker-one"),
	))
	for attempt := 1; attempt <= 2; attempt++ {
		ctx := WithDecryptAuditExecution(baseCtx, DecryptAuditExecution{
			Kind:         DecryptExecutionDeploy,
			ExecutionID:  "job-deploy-one",
			DeploymentID: "dep_one",
			OperationID:  "op_one",
			Attempt:      attempt,
		})
		if _, err := resolver.Resolve(ctx, "prj_one", "env_prod", "svc_api"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := chain.Read(baseCtx, audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 ||
		rows[0].RequestID == rows[1].RequestID ||
		rows[0].Attrs["attempt"] == rows[1].Attrs["attempt"] {
		t.Fatalf("decrypt rows are not separately attributable: %#v", rows)
	}
}
