// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"context"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/observability"
)

type evaluatorStoreFake struct {
	definitions []Definition
	evaluations []Evaluation
}

func (store *evaluatorStoreFake) LeaseDue(context.Context, string, time.Time, time.Duration, int) ([]Definition, error) {
	return store.definitions, nil
}

func (store *evaluatorStoreFake) RecordEvaluation(_ context.Context, _ string, evaluation Evaluation, now time.Time) (Transition, error) {
	store.evaluations = append(store.evaluations, evaluation)
	return Transition{MonitorID: evaluation.MonitorID, From: StateHealthy, To: StateFiring, EventType: EventFiring, Value: evaluation.Value, At: now}, nil
}

type metricReaderFake struct {
	series observability.MetricSeries
}

func (reader metricReaderFake) QueryMetrics(context.Context, observability.MetricQuery) (observability.MetricSeries, error) {
	return reader.series, nil
}

func TestEvaluatorUsesMaximumWindowValueAndDurableTransition(t *testing.T) {
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	store := &evaluatorStoreFake{definitions: []Definition{{
		ID: "mon_1", Name: "CPU high",
		Scope:  observability.Scope{ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1"},
		Signal: SignalCPUPercent, Operator: OperatorGreaterThan, Threshold: 80,
		Window: 5 * time.Minute, EvaluationInterval: time.Minute,
		ConsecutiveFailures: 2, ConsecutiveRecoveries: 2, Enabled: true, State: StateHealthy,
	}}}
	evaluator := Evaluator{
		Store: store, Metrics: metricReaderFake{series: observability.MetricSeries{
			Points: []observability.MetricPoint{{CPUPercent: 25}, {CPUPercent: 91}},
		}},
		WorkerID: "worker_1", Clock: func() time.Time { return now },
	}

	transitions, err := evaluator.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.evaluations) != 1 || store.evaluations[0].Value == nil || *store.evaluations[0].Value != 91 {
		t.Fatalf("evaluations = %#v", store.evaluations)
	}
	if !store.evaluations[0].Breached || !store.evaluations[0].HasData {
		t.Fatalf("evaluation = %#v", store.evaluations[0])
	}
	if len(transitions) != 1 || transitions[0].EventType != EventFiring {
		t.Fatalf("transitions = %#v", transitions)
	}
}

func TestMonitorValueRequiresMemoryLimit(t *testing.T) {
	value, ok := monitorValue(SignalMemoryPercent, []observability.MetricPoint{
		{MemoryBytes: 90, MemoryLimitBytes: 0},
	})
	if ok || value != 0 {
		t.Fatalf("value = %v, ok = %v", value, ok)
	}
}
