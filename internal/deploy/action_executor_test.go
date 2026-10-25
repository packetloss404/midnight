// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/queue"
	"github.com/midnight/midnight/internal/runtime"
	"github.com/midnight/midnight/internal/serviceconfig"
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

func (*actionInstanceStoreFake) Observe(context.Context, runtime.RoutingTarget) error { return nil }

func (*actionRuntimeFake) ResolveRoutingTarget(_ context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	return runtime.RoutingTarget{DeploymentID: deploymentID, InstanceID: instanceID, ContainerIP: "127.0.0.1", Ready: true}, nil
}

func (*replicaActionRuntimeFake) ResolveRoutingTarget(_ context.Context, deploymentID, instanceID string) (runtime.RoutingTarget, error) {
	return runtime.RoutingTarget{DeploymentID: deploymentID, InstanceID: instanceID, ContainerIP: "127.0.0.1", Ready: true}, nil
}

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

func TestActionExecutorRestartWaitsForHTTPAndPublicationBeforeSuccess(t *testing.T) {
	for _, scenario := range []struct{ resume, publishFails bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("resume=%t/publicationFails=%t", scenario.resume, scenario.publishFails), func(t *testing.T) {
			var ready atomic.Bool
			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				probes.Add(1)
				if !ready.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer server.Close()
			host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			store := &actionStoreFake{record: RuntimeActionRecord{ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0", Action: RuntimeActionRestart, DeploymentActive: true}}
			if scenario.resume {
				store.record.Step = "started"
			}
			rt := &restartReadinessRuntime{target: runtime.RoutingTarget{DeploymentID: "dep_1", InstanceID: "dep_1-r0", ContainerIP: host, InternalPort: port, Ready: true}}
			instances := &restartObservationStore{}
			publicationErr := errors.New("ingress has not observed fresh backends")
			routes := &restartRoutesFake{restore: func() error {
				if probes.Load() < 2 || instances.observed.ContainerIP != host || store.completeState != "" {
					t.Fatal("publication ran before readiness and observation, or success preceded publication")
				}
				if scenario.publishFails {
					return publicationErr
				}
				return nil
			}}
			cfg := serviceconfig.Default("", "", "")
			cfg.HealthKind, cfg.HealthPath = serviceconfig.HealthHTTP, "/"
			e := ActionExecutor{Store: store, Runtime: rt, Instances: instances, Routes: routes, Operations: &actionOperationFake{}, Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}}, Readiness: ReadinessConfig{Timeout: time.Second, Interval: time.Millisecond, Sleep: func(context.Context, time.Duration) error { ready.Store(true); return nil }}}
			if scenario.resume {
				e.Cancels = canceledActionChecker{}
			}
			payload, _ := json.Marshal(DeploymentActionJobPayload{SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", Action: RuntimeActionRestart})
			out, err := e.Execute(context.Background(), queue.Job{Payload: payload})
			if scenario.resume && len(rt.calls) != 1 {
				t.Fatalf("resumed restart repeated stop: %v", rt.calls)
			}
			if scenario.publishFails {
				if !errors.Is(err, publicationErr) || out.Succeeded || store.completeState != "" {
					t.Fatalf("unpublished restart=%#v err=%v completed=%q", out, err, store.completeState)
				}
			} else if err != nil || !out.Succeeded || store.completeState != "running" {
				t.Fatalf("converged restart=%#v err=%v completed=%q", out, err, store.completeState)
			}
		})
	}
}

func TestActionExecutorRemoveRequiresStoppedActiveDeployment(t *testing.T) {
	for _, desired := range []string{"running", "stopped"} {
		t.Run(desired, func(t *testing.T) {
			store := &actionStoreFake{record: RuntimeActionRecord{ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0", Action: RuntimeActionRemove, DeploymentActive: true, DesiredState: desired}}
			rt := &actionRuntimeFake{}
			payload, _ := json.Marshal(DeploymentActionJobPayload{SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", Action: RuntimeActionRemove})
			out, err := (ActionExecutor{Store: store, Runtime: rt, Operations: &actionOperationFake{}}).Execute(context.Background(), queue.Job{Payload: payload})
			if desired == "running" {
				if err == nil || out.Succeeded || len(rt.calls) != 0 {
					t.Fatalf("active running removal=%#v %v calls=%v", out, err, rt.calls)
				}
				return
			}
			if err != nil || !out.Succeeded || !equalStrings(rt.calls, []string{"remove"}) || store.completeState != "removed" {
				t.Fatalf("stopped removal=%#v %v calls=%v completed=%s", out, err, rt.calls, store.completeState)
			}
		})
	}
}

func TestActionExecutorRestartReadinessFailureNeverPublishes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	host, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	store := &actionStoreFake{record: RuntimeActionRecord{ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0", Action: RuntimeActionRestart, DeploymentActive: true}}
	rt := &restartReadinessRuntime{target: runtime.RoutingTarget{DeploymentID: "dep_1", InstanceID: "dep_1-r0", ContainerIP: host, InternalPort: port, Ready: true}}
	cfg := serviceconfig.Default("", "", "")
	cfg.HealthKind = serviceconfig.HealthHTTP
	clock := time.Now()
	routes := &restartRoutesFake{restore: func() error { t.Fatal("readiness failure published a route"); return nil }}
	payload, _ := json.Marshal(DeploymentActionJobPayload{SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", Action: RuntimeActionRestart})
	out, err := (ActionExecutor{Store: store, Runtime: rt, Routes: routes, Configuration: fakeConfigResolver{snapshot: serviceconfig.Snapshot{Config: cfg}}, Operations: &actionOperationFake{}, Readiness: ReadinessConfig{Timeout: time.Millisecond, Now: func() time.Time { clock = clock.Add(2 * time.Millisecond); return clock }}}).Execute(context.Background(), queue.Job{Payload: payload})
	if err == nil || out.Succeeded || store.completeState != "" || !equalStrings(store.steps, []string{"stopped"}) {
		t.Fatalf("unready restart=%#v err=%v steps=%v complete=%s", out, err, store.steps, store.completeState)
	}
}

func TestActionExecutorResumedStopWithdrawsAgainAndConvergesDespiteCancel(t *testing.T) {
	store := &actionStoreFake{record: RuntimeActionRecord{ID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", InstanceID: "dep_1-r0", Action: RuntimeActionStop, DeploymentActive: true, Step: "route_withdrawn"}}
	routes := &actionWithdrawalFake{}
	payload, _ := json.Marshal(DeploymentActionJobPayload{SchemaVersion: DeploymentActionJobPayloadSchema, ActionID: "act_1", OperationID: "op_1", DeploymentID: "dep_1", Action: RuntimeActionStop})
	out, err := (ActionExecutor{Store: store, Runtime: &actionRuntimeFake{}, Routes: routes, Operations: &actionOperationFake{}, Cancels: canceledActionChecker{}}).Execute(context.Background(), queue.Job{Payload: payload})
	if err != nil || !out.Succeeded || routes.calls != 1 || store.completeState != "stopped" {
		t.Fatalf("resumed stop=%#v %v withdrawals=%d completed=%s", out, err, routes.calls, store.completeState)
	}
}

type actionWithdrawalFake struct{ calls int }

func (r *actionWithdrawalFake) WithdrawDeployment(context.Context, RuntimeActionRecord) error {
	r.calls++
	return nil
}

type restartReadinessRuntime struct {
	actionRuntimeFake
	target runtime.RoutingTarget
}

func (r *restartReadinessRuntime) ResolveRoutingTarget(context.Context, string, string) (runtime.RoutingTarget, error) {
	return r.target, nil
}

type restartObservationStore struct {
	actionInstanceStoreFake
	observed runtime.RoutingTarget
}

func (s *restartObservationStore) Observe(_ context.Context, target runtime.RoutingTarget) error {
	s.observed = target
	return nil
}

type restartRoutesFake struct{ restore func() error }

func (*restartRoutesFake) WithdrawDeployment(context.Context, RuntimeActionRecord) error { return nil }
func (r *restartRoutesFake) RestoreDeployment(context.Context, RuntimeActionRecord) error {
	return r.restore()
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
