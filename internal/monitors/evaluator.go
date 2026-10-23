// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package monitors

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/midnight/midnight/internal/observability"
)

type Evaluator struct {
	Store     Store
	Metrics   MetricReader
	WorkerID  string
	Clock     func() time.Time
	Lease     time.Duration
	BatchSize int
}

func (e Evaluator) RunOnce(ctx context.Context) ([]Transition, error) {
	if e.Store == nil || e.Metrics == nil {
		return nil, errors.New("monitor evaluator requires store and metrics")
	}
	now := e.now()
	definitions, err := e.Store.LeaseDue(ctx, e.WorkerID, now, e.lease(), e.batchSize())
	if err != nil {
		return nil, fmt.Errorf("lease due monitors: %w", err)
	}
	transitions := make([]Transition, 0, len(definitions))
	for _, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return transitions, fmt.Errorf("monitor %s: %w", definition.ID, err)
		}
		series, queryErr := e.Metrics.QueryMetrics(ctx, observability.MetricQuery{
			Scope: definition.Scope, Since: now.Add(-definition.Window),
			Until: now, Step: definition.Window, Mode: "aggregate", Limit: 2,
		})
		evaluation := Evaluation{MonitorID: definition.ID, ObservedAt: now}
		if queryErr == nil {
			value, ok := monitorValue(definition.Signal, series.Points)
			if ok {
				evaluation.HasData = true
				evaluation.Value = &value
				evaluation.Breached = breached(definition.Operator, value, definition.Threshold)
			}
		}
		transition, recordErr := e.Store.RecordEvaluation(ctx, e.WorkerID, evaluation, now)
		if recordErr != nil {
			return transitions, fmt.Errorf("record monitor %s evaluation: %w", definition.ID, recordErr)
		}
		if transition.EventType != "" {
			transitions = append(transitions, transition)
		}
		if queryErr != nil {
			return transitions, fmt.Errorf("query monitor %s metrics: %w", definition.ID, queryErr)
		}
	}
	return transitions, nil
}

func monitorValue(signal string, points []observability.MetricPoint) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	value := -math.MaxFloat64
	for _, point := range points {
		var candidate float64
		switch signal {
		case SignalCPUPercent:
			candidate = point.CPUPercent
		case SignalMemoryPercent:
			if point.MemoryLimitBytes <= 0 {
				continue
			}
			candidate = float64(point.MemoryBytes) * 100 / float64(point.MemoryLimitBytes)
		case SignalRestartCount:
			candidate = float64(point.RestartCount)
		default:
			return 0, false
		}
		if candidate > value {
			value = candidate
		}
	}
	if value == -math.MaxFloat64 {
		return 0, false
	}
	return value, true
}

func breached(operator string, value, threshold float64) bool {
	if operator == OperatorGreaterThanOrEqual {
		return value >= threshold
	}
	return value > threshold
}

func (e Evaluator) now() time.Time {
	if e.Clock != nil {
		return e.Clock().UTC()
	}
	return time.Now().UTC()
}

func (e Evaluator) lease() time.Duration {
	if e.Lease > 0 {
		return e.Lease
	}
	return DefaultLease
}

func (e Evaluator) batchSize() int {
	if e.BatchSize > 0 && e.BatchSize <= 100 {
		return e.BatchSize
	}
	return 25
}
