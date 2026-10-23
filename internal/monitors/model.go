// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package monitors implements Midnight's built-in metric monitor evaluator and
// durable notification delivery outbox. It does not require Prometheus or
// Alertmanager.
package monitors

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/observability"
)

const (
	SignalCPUPercent    = "cpu_percent"
	SignalMemoryPercent = "memory_percent"
	SignalRestartCount  = "restart_count"

	OperatorGreaterThan        = "gt"
	OperatorGreaterThanOrEqual = "gte"

	StateUnknown = "unknown"
	StateHealthy = "healthy"
	StateFiring  = "firing"

	EventFiring   = "monitor.firing"
	EventResolved = "monitor.resolved"

	IncidentStateOpen     = "open"
	IncidentStateResolved = "resolved"

	DeliveryStatePending   = "pending"
	DeliveryStateLeased    = "leased"
	DeliveryStateDelivered = "delivered"
	DeliveryStateDead      = "dead"

	DefaultEvaluationInterval = time.Minute
	DefaultWindow             = 5 * time.Minute
	DefaultLease              = 30 * time.Second
	MaxDeliveryAttempts       = 8
)

type Definition struct {
	ID                    string              `json:"id"`
	Name                  string              `json:"name"`
	Scope                 observability.Scope `json:"scope"`
	Signal                string              `json:"signal"`
	Operator              string              `json:"operator"`
	Threshold             float64             `json:"threshold"`
	Window                time.Duration       `json:"-"`
	EvaluationInterval    time.Duration       `json:"-"`
	ConsecutiveFailures   int                 `json:"consecutive_failures"`
	ConsecutiveRecoveries int                 `json:"consecutive_recoveries"`
	Enabled               bool                `json:"enabled"`
	State                 string              `json:"state"`
	FailureStreak         int                 `json:"failure_streak"`
	RecoveryStreak        int                 `json:"recovery_streak"`
	NextEvaluationAt      time.Time           `json:"next_evaluation_at"`
	LastEvaluatedAt       time.Time           `json:"last_evaluated_at,omitempty"`
	LastValue             *float64            `json:"last_value,omitempty"`
}

func (definition Definition) Validate() error {
	if strings.TrimSpace(definition.ID) == "" || strings.TrimSpace(definition.Name) == "" {
		return errors.New("monitor id and name are required")
	}
	if err := definition.Scope.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(definition.Scope.ServiceID) == "" {
		return errors.New("monitor service scope is required")
	}
	switch definition.Signal {
	case SignalCPUPercent, SignalMemoryPercent, SignalRestartCount:
	default:
		return errors.New("monitor signal is unsupported")
	}
	switch definition.Operator {
	case OperatorGreaterThan, OperatorGreaterThanOrEqual:
	default:
		return errors.New("monitor operator is unsupported")
	}
	if definition.Threshold < 0 {
		return errors.New("monitor threshold cannot be negative")
	}
	if definition.Window < time.Minute {
		return errors.New("monitor window must be at least one minute")
	}
	if definition.EvaluationInterval < time.Minute {
		return errors.New("monitor evaluation interval must be at least one minute")
	}
	if definition.ConsecutiveFailures < 1 || definition.ConsecutiveRecoveries < 1 {
		return errors.New("monitor consecutive thresholds must be positive")
	}
	return nil
}

type Evaluation struct {
	MonitorID  string    `json:"monitor_id"`
	ObservedAt time.Time `json:"observed_at"`
	Value      *float64  `json:"value,omitempty"`
	Breached   bool      `json:"breached"`
	HasData    bool      `json:"has_data"`
}

type Transition struct {
	MonitorID string    `json:"monitor_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	EventType string    `json:"event_type,omitempty"`
	Value     *float64  `json:"value,omitempty"`
	At        time.Time `json:"at"`
}

type Store interface {
	LeaseDue(context.Context, string, time.Time, time.Duration, int) ([]Definition, error)
	RecordEvaluation(context.Context, string, Evaluation, time.Time) (Transition, error)
}

type MetricReader interface {
	QueryMetrics(context.Context, observability.MetricQuery) (observability.MetricSeries, error)
}

type Channel struct {
	ID                    string `json:"id"`
	ProjectID             string `json:"project_id"`
	DisplayName           string `json:"display_name"`
	Type                  string `json:"type"`
	EndpointVariableID    string `json:"endpoint_variable_id"`
	SigningVariableID     string `json:"signing_variable_id"`
	SigningKeyID          string `json:"signing_key_id"`
	AllowInsecureLoopback bool   `json:"allow_insecure_loopback"`
}

type Delivery struct {
	ID          string         `json:"id"`
	MonitorID   string         `json:"monitor_id"`
	Channel     Channel        `json:"channel"`
	EventType   string         `json:"event_type"`
	Payload     map[string]any `json:"payload"`
	Attempts    int            `json:"attempts"`
	AvailableAt time.Time      `json:"available_at"`
}

type DeliveryStore interface {
	LeaseDeliveries(context.Context, string, time.Time, time.Duration, int) ([]Delivery, error)
	CompleteDelivery(context.Context, string, time.Time, int) error
	RetryDelivery(context.Context, string, time.Time, int, string, bool) error
}

type IncidentQuery struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	MonitorID     string
	State         string
	Limit         int
}

func (query IncidentQuery) Validate() error {
	if strings.TrimSpace(query.ProjectID) == "" {
		return errors.New("incident project scope is required")
	}
	switch strings.TrimSpace(query.State) {
	case "", IncidentStateOpen, IncidentStateResolved:
	default:
		return errors.New("incident state must be open or resolved")
	}
	if query.Limit < 1 || query.Limit > 200 {
		return errors.New("incident limit must be between 1 and 200")
	}
	return nil
}

type IncidentStatus struct {
	ID            string              `json:"id"`
	MonitorID     string              `json:"monitor_id"`
	MonitorName   string              `json:"monitor_name"`
	Scope         observability.Scope `json:"scope"`
	State         string              `json:"state"`
	OpenedAt      time.Time           `json:"opened_at"`
	ResolvedAt    *time.Time          `json:"resolved_at,omitempty"`
	OpenValue     *float64            `json:"open_value,omitempty"`
	CloseValue    *float64            `json:"close_value,omitempty"`
	DeliveryCount int                 `json:"delivery_count"`
	DeadCount     int                 `json:"dead_letter_count"`
}

type DeliveryStatusQuery struct {
	ProjectID     string
	EnvironmentID string
	ServiceID     string
	MonitorID     string
	IncidentID    string
	State         string
	Limit         int
}

func (query DeliveryStatusQuery) Validate() error {
	if strings.TrimSpace(query.ProjectID) == "" {
		return errors.New("delivery project scope is required")
	}
	switch strings.TrimSpace(query.State) {
	case "", DeliveryStatePending, DeliveryStateLeased, DeliveryStateDelivered, DeliveryStateDead:
	default:
		return errors.New("delivery state is unsupported")
	}
	if query.Limit < 1 || query.Limit > 200 {
		return errors.New("delivery limit must be between 1 and 200")
	}
	return nil
}

// DeliveryStatus is deliberately metadata-only. It never contains the durable
// payload, webhook endpoint, sealed-variable references, signing identifiers,
// lease owner, or raw delivery error.
type DeliveryStatus struct {
	ID             string     `json:"id"`
	IncidentID     string     `json:"incident_id"`
	MonitorID      string     `json:"monitor_id"`
	MonitorName    string     `json:"monitor_name"`
	ProjectID      string     `json:"project_id"`
	EnvironmentID  string     `json:"environment_id"`
	ServiceID      string     `json:"service_id"`
	ChannelID      string     `json:"channel_id"`
	ChannelName    string     `json:"channel_name"`
	ChannelType    string     `json:"channel_type"`
	EventType      string     `json:"event_type"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	ManualRetries  int        `json:"manual_retries"`
	AvailableAt    time.Time  `json:"available_at"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	ErrorSummary   string     `json:"error_summary,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

var ErrDeliveryNotDead = errors.New("monitor delivery is not dead-lettered")

type StatusStore interface {
	ListIncidents(context.Context, IncidentQuery) ([]IncidentStatus, error)
	ListDeliveryStatuses(context.Context, DeliveryStatusQuery) ([]DeliveryStatus, error)
	RetryDeadDelivery(context.Context, string, string, time.Time) (DeliveryStatus, error)
}

// SecretResolver resolves a sealed variable only at the delivery boundary.
// Durable delivery rows contain variable IDs, never endpoint or signing
// plaintext.
type SecretResolver interface {
	ResolveSecret(context.Context, string) ([]byte, error)
}
