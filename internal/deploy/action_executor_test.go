// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/pkg/orailtypes"
)

func TestActionExecutorRestartConvergesDurably(t *testing.T) {
	t.Parallel()
	store := &actionStoreFake{record: RuntimeActionRecord{
		ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0",
		Action: RuntimeActionRestart, State: "queued", DeploymentActive: true,
	}}
	runtime := &actionRuntimeFake{}
	ops := &actionOperationFake{}
	payload, err := json.Marshal(DeploymentActionJobPayload{
		SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1",
		DeploymentID: "dep_1", Action: RuntimeActionRestart,
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := (ActionExecutor{Store: store, Runtime: runtime, Operations: ops}).Execute(context.Background(), queue.Job{ID: "job_1", Payload: payload})
	if err != nil || !outcome.Succeeded {
		t.Fatalf("Execute() = %#v, %v", outcome, err)
	}
	if got, want := runtime.calls, []string{"stop", "start"}; !equalStrings(got, want) {
		t.Fatalf("runtime calls = %v, want %v", got, want)
	}
	if got, want := store.steps, []string{"stopped", "started"}; !equalStrings(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if store.completeState != "running" {
		t.Fatalf("complete runtime state = %q, want running", store.completeState)
	}
	if got, want := ops.states, []orailtypes.OperationState{orailtypes.OperationRunning, orailtypes.OperationSucceeded}; !equalStates(got, want) {
		t.Fatalf("operation states = %v, want %v", got, want)
	}
}

func TestActionExecutorCancellationStopsBeforeEffects(t *testing.T) {
	t.Parallel()
	store := &actionStoreFake{record: RuntimeActionRecord{
		ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0",
		Action: RuntimeActionStop, State: "queued", DeploymentActive: true,
	}}
	runtime := &actionRuntimeFake{}
	ops := &actionOperationFake{}
	payload, _ := json.Marshal(DeploymentActionJobPayload{
		SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1",
		DeploymentID: "dep_1", Action: RuntimeActionStop,
	})
	outcome, err := (ActionExecutor{Store: store, Runtime: runtime, Operations: ops, Cancels: canceledActionChecker{}}).Execute(context.Background(), queue.Job{ID: "job_1", Payload: payload})
	if err != nil || outcome.FailureCode == "" {
		t.Fatalf("Execute() = %#v, %v", outcome, err)
	}
	if len(runtime.calls) != 0 {
		t.Fatalf("runtime calls = %v, want none", runtime.calls)
	}
	if store.failureCode == "" {
		t.Fatal("action cancellation was not recorded")
	}
}

func TestActionExecutorRestartFansOutToEveryDeploymentInstance(t *testing.T) {
	t.Parallel()
	store := &actionStoreFake{record: RuntimeActionRecord{
		ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0",
		Action: RuntimeActionRestart, State: "queued", DeploymentActive: true,
	}}
	runtime := &replicaActionRuntimeFake{ids: []string{"dep_1-r0", "dep_1-r1", "dep_1-r2"}}
	instances := &actionInstanceStoreFake{}
	payload, _ := json.Marshal(DeploymentActionJobPayload{
		SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1",
		DeploymentID: "dep_1", Action: RuntimeActionRestart,
	})
	outcome, err := (ActionExecutor{Store: store, Runtime: runtime, Instances: instances, Operations: &actionOperationFake{}}).Execute(context.Background(), queue.Job{ID: "job_1", Payload: payload})
	if err != nil || !outcome.Succeeded {
		t.Fatalf("Execute() = %#v, %v", outcome, err)
	}
	if got, want := runtime.calls, []string{
		"stop:dep_1-r0", "stop:dep_1-r1", "stop:dep_1-r2",
		"start:dep_1-r0", "start:dep_1-r1", "start:dep_1-r2",
	}; !equalStrings(got, want) {
		t.Fatalf("runtime calls = %v, want %v", got, want)
	}
	if got, want := instances.states, []string{"dep_1:stopped:stopped", "dep_1:running:starting"}; !equalStrings(got, want) {
		t.Fatalf("durable instance states = %v, want %v", got, want)
	}
}

type actionStoreFake struct {
	record        RuntimeActionRecord
	steps         []string
	completeState string
	failureCode   string
}

func (s *actionStoreFake) BeginRuntimeAction(context.Context, string, string) (RuntimeActionRecord, error) {
	s.record.State = "running"
	return s.record, nil
}
func (s *actionStoreFake) RecordRuntimeActionStep(_ context.Context, _ string, step string) error {
	s.steps = append(s.steps, step)
	return nil
}
func (s *actionStoreFake) CompleteRuntimeAction(_ context.Context, _ string, state string) error {
	s.completeState = state
	return nil
}
func (s *actionStoreFake) FailRuntimeAction(_ context.Context, _ string, code failurecodes.Code, _ string) error {
	s.failureCode = string(code)
	return nil
}

type actionRuntimeFake struct{ calls []string }

func (r *actionRuntimeFake) Start(context.Context, string, string) error {
	r.calls = append(r.calls, "start")
	return nil
}
func (r *actionRuntimeFake) Stop(context.Context, string, string, time.Duration) error {
	r.calls = append(r.calls, "stop")
	return nil
}
func (r *actionRuntimeFake) Remove(context.Context, string, string) error {
	r.calls = append(r.calls, "remove")
	return nil
}

type replicaActionRuntimeFake struct {
	ids   []string
	calls []string
}

type actionInstanceStoreFake struct{ states []string }

func (s *actionInstanceStoreFake) SetDeploymentState(_ context.Context, deploymentID, desired, observed string) error {
	s.states = append(s.states, deploymentID+":"+desired+":"+observed)
	return nil
}

func (r *replicaActionRuntimeFake) DeploymentInstanceIDs(context.Context, string) ([]string, error) {
	return append([]string(nil), r.ids...), nil
}
func (r *replicaActionRuntimeFake) Start(_ context.Context, _ string, instanceID string) error {
	r.calls = append(r.calls, "start:"+instanceID)
	return nil
}
func (r *replicaActionRuntimeFake) Stop(_ context.Context, _ string, instanceID string, _ time.Duration) error {
	r.calls = append(r.calls, "stop:"+instanceID)
	return nil
}
func (r *replicaActionRuntimeFake) Remove(_ context.Context, _ string, instanceID string) error {
	r.calls = append(r.calls, "remove:"+instanceID)
	return nil
}

type actionOperationFake struct{ states []orailtypes.OperationState }

func (o *actionOperationFake) Transition(_ context.Context, _ orailtypes.ID, state orailtypes.OperationState) (orailtypes.Operation, error) {
	o.states = append(o.states, state)
	return orailtypes.Operation{State: state}, nil
}
func (o *actionOperationFake) Fail(context.Context, orailtypes.ID, orailtypes.FailureCode, string) (orailtypes.Operation, error) {
	return orailtypes.Operation{}, nil
}

type canceledActionChecker struct{}

func (canceledActionChecker) CancelRequested(context.Context, string) (bool, error) { return true, nil }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func equalStates(a, b []orailtypes.OperationState) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
