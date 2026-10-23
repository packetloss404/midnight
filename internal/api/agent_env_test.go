// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package api

import (
	"context"
	"errors"
	"github.com/midnight/midnight/internal/agent"
	"testing"
	"time"

	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildAgentEnvBindsReadStores(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, err := resources.CreateProject(ctx, Project{WorkspaceID: DefaultWorkspaceID, Slug: "demo", DisplayName: "Demo"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service, err := resources.CreateService(ctx, Service{ProjectID: project.ID, Slug: "web", DisplayName: "Web", Spec: map[string]any{"github": map[string]any{"repository": "acme/web", "branch": "main"}}})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	operations := NewMemoryOperationStore()
	op, err := operations.Create(ctx, orailtypes.Operation{ID: "op_1", Kind: "deploy", State: orailtypes.OperationRunning})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	if _, err := operations.AppendEvent(ctx, OperationEvent{OperationID: op.ID, Seq: 1, State: orailtypes.OperationRunning, Message: "deploy worker step schedule succeeded", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	env := buildAgentEnv(DataStack{ProjectStore: resources, ServiceStore: resources, OperationStore: operations}, "agent:test")
	if env.Services == nil || env.Operations == nil || env.Builds == nil {
		t.Fatalf("env not bound: %+v", env)
	}
	projects, err := env.Services.ListProjects(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != string(project.ID) || projects[0].Name != "Demo" {
		t.Fatalf("ListProjects = %+v, %v", projects, err)
	}
	services, err := env.Services.ListServices(ctx, string(project.ID))
	if err != nil || len(services) != 1 || services[0].GitHubRepo != "acme/web" || services[0].GitHubRef != "main" {
		t.Fatalf("ListServices = %+v, %v", services, err)
	}
	got, err := env.Services.GetService(ctx, string(service.ID))
	if err != nil || got.ID != string(service.ID) || got.ProjectID != string(project.ID) {
		t.Fatalf("GetService = %+v, %v", got, err)
	}
	// Memory mode installs no tenant variable coordinator, so the write
	// path must refuse rather than silently no-op.
	setErr := env.Services.SetVariable(ctx, agent.VariableWrite{ServiceID: string(service.ID), Name: "FOO", Value: "bar"})
	if !errors.Is(setErr, ErrAgentMutationUnavailable) {
		t.Fatalf("SetVariable error = %v, want ErrAgentMutationUnavailable", setErr)
	}
	lines, err := env.Operations.GetLogs(ctx, "op_1", 50, "schedule")
	if err != nil || len(lines) != 1 || lines[0].Stream != "build" {
		t.Fatalf("GetLogs = %+v, %v", lines, err)
	}
	if lines, _ := env.Operations.GetLogs(ctx, "op_1", 50, "nomatch"); len(lines) != 0 {
		t.Fatalf("search filter should drop non-matching lines, got %+v", lines)
	}
	// Same for deployment actions: DeploymentActions is nil in memory
	// mode by design, because these mutations need one transaction
	// spanning operations, deployment snapshots and the outbox.
	if _, err := env.Operations.Redeploy(ctx, string(service.ID)); err == nil {
		t.Fatal("Redeploy should refuse in memory mode")
	}
	if _, err := env.Builds.DeployFromSource(ctx, string(service.ID), "acme/web", "main"); !errors.Is(err, ErrAgentSourceDeployUnavailable) {
		t.Fatalf("DeployFromSource error = %v, want ErrAgentSourceDeployUnavailable", err)
	}
}

func TestBuildAgentEnvWithoutStoresLeavesToolsUnbound(t *testing.T) {
	env := buildAgentEnv(DataStack{}, "agent:test")
	if env.Services != nil || env.Operations != nil {
		t.Fatalf("expected nil read seams without stores, got %+v", env)
	}
	if env.Actor != "agent:test" {
		t.Fatalf("actor = %q", env.Actor)
	}
}
