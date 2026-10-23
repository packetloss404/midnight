// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/build"
	"github.com/midnight/midnight/internal/deploy"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestBuildWorkerHonorsCancellationAtSafePoint(t *testing.T) {
	ctx := context.Background()
	operations := api.NewMemoryOperationStore()
	deployments := api.NewMemoryDeploymentStore(nil)

	if _, err := operations.Create(ctx, orailtypes.Operation{
		ID:    "op_cancel_build",
		Kind:  "deploy",
		State: orailtypes.OperationQueued,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Transition(
		ctx,
		"op_cancel_build",
		orailtypes.OperationRunning,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := deployments.CreateDeployment(ctx, api.DeploymentRecord{
		ID:            "dep_cancel_build",
		ProjectID:     "prj_cancel",
		ServiceID:     "svc_cancel",
		EnvironmentID: "env_cancel",
		Action:        deploy.ActionDeploySource,
		OperationID:   "op_cancel_build",
	}); err != nil {
		t.Fatal(err)
	}
	if err := deployments.MarkDeploymentBuilding(
		ctx,
		"dep_cancel_build",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.AppendEvent(ctx, api.OperationEvent{
		OperationID: "op_cancel_build",
		State:       orailtypes.OperationRunning,
		Message: "cancel requested: worker will stop at its safe point " +
			"before traffic promotion",
	}); err != nil {
		t.Fatal(err)
	}

	executor := &buildJobExecutor{
		operationStore:  operations,
		deploymentStore: deployments,
		redactor:        build.NewRedactor(nil),
	}
	canceled, err := executor.cancelBuildAtSafePoint(
		ctx,
		"op_cancel_build",
		"before build solve",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Fatal("cancelBuildAtSafePoint() = false, want true")
	}

	operation, err := operations.Get(ctx, "op_cancel_build")
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != orailtypes.OperationCanceled {
		t.Fatalf("operation state = %q, want CANCELED", operation.State)
	}
	deployment, err := deployments.GetDeploymentByOperation(
		ctx,
		"op_cancel_build",
	)
	if err != nil {
		t.Fatal(err)
	}
	if deployment.State != deploy.DeploymentCanceled ||
		deployment.CompletedAt == nil {
		t.Fatalf("deployment = %#v, want terminal canceled", deployment)
	}
	events, err := operations.ListEvents(ctx, "op_cancel_build")
	if err != nil {
		t.Fatal(err)
	}
	if got := events[len(events)-1].Message; !strings.Contains(
		got,
		"build canceled at worker safe point before build solve",
	) {
		t.Fatalf("last event = %q", got)
	}
}

func TestBuildWorkerIgnoresUnrelatedCancellationText(t *testing.T) {
	ctx := context.Background()
	operations := api.NewMemoryOperationStore()
	if _, err := operations.Create(ctx, orailtypes.Operation{
		ID:    "op_not_canceled",
		Kind:  "deploy",
		State: orailtypes.OperationRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.AppendEvent(ctx, api.OperationEvent{
		OperationID: "op_not_canceled",
		State:       orailtypes.OperationRunning,
		Message:     "cancel requested by unrelated cleanup",
	}); err != nil {
		t.Fatal(err)
	}
	executor := &buildJobExecutor{operationStore: operations}
	canceled, err := executor.cancelBuildAtSafePoint(
		ctx,
		"op_not_canceled",
		"before build solve",
	)
	if err != nil {
		t.Fatal(err)
	}
	if canceled {
		t.Fatal("unrelated cancellation text stopped a build")
	}
}
