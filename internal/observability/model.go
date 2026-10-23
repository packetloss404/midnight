// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package observability owns durable runtime logs and resource metrics.
package observability

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	DefaultRetention     = 30 * 24 * time.Hour
	DefaultHeartbeatAge  = 2 * time.Minute
	DefaultIngestPerMin  = 6000
	DefaultQueryLimit    = 500
	MaxQueryLimit        = 2000
	MaxMetricPoints      = 720
	MaxAttributes        = 32
	MaxAttributeJSONSize = 16 << 10
	MaxMessageBytes      = 64 << 10
)

const (
	CollectorKindDocker  = "docker"
	CollectorKindTraefik = "traefik"
)

type Scope struct {
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id,omitempty"`
	ServiceID     string `json:"service_id,omitempty"`
	DeploymentID  string `json:"deployment_id,omitempty"`
	OperationID   string `json:"operation_id,omitempty"`
	ReplicaID     string `json:"replica_id,omitempty"`
}

func (s Scope) Validate() error {
	if strings.TrimSpace(s.ProjectID) == "" {
		return errors.New("project scope is required")
	}
	if s.DeploymentID != "" && s.ServiceID == "" {
		return errors.New("deployment scope requires service scope")
	}
	if s.ReplicaID != "" && s.ServiceID == "" {
		return errors.New("replica scope requires service scope")
	}
	return nil
}

type LogRecord struct {
	ID             int64          `json:"-"`
	ObservedAt     time.Time      `json:"observed_at"`
	Scope          Scope          `json:"scope"`
	Surface        string         `json:"surface"`
	Stream         string         `json:"stream,omitempty"`
	Level          string         `json:"level"`
	Component      string         `json:"component"`
	Message        string         `json:"message"`
	Attributes     map[string]any `json:"attributes,omitempty"`
	SourceKey      string         `json:"-"`
	SourceSequence string         `json:"-"`
}

type IngestResult struct {
	Stored  bool  `json:"stored"`
	Dropped bool  `json:"dropped"`
	LogID   int64 `json:"log_id,omitempty"`
}

type IngestionCounter struct {
	SourceKey       string    `json:"source_key"`
	WindowStartedAt time.Time `json:"window_started_at"`
	Accepted        int64     `json:"accepted"`
	Dropped         int64     `json:"dropped"`
}

type LogQuery struct {
	Scope      Scope
	Surfaces   []string
	Streams    []string
	Levels     []string
	Components []string
	Search     string
	Since      time.Time
	Until      time.Time
	AfterID    int64
	Limit      int
}

type LogPage struct {
	Records    []LogRecord `json:"records"`
	NextID     int64       `json:"-"`
	Truncated  bool        `json:"truncated"`
	DropCounts int64       `json:"dropped_at_ingest"`
}

// CollectorHealth is the public, topology-safe state of one collector kind.
// Collector ids and raw driver errors stay server-side; callers only need to
// know whether a current pass succeeded and whether the heartbeat is missing.
type CollectorHealth struct {
	Kind         string     `json:"kind"`
	Status       string     `json:"status"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	HeartbeatGap bool       `json:"heartbeat_gap"`
	Reason       string     `json:"reason,omitempty"`
}

// CollectorHealthStore is optional so non-production log fixtures do not have
// to synthesize collector state. The PostgreSQL production store implements it.
type CollectorHealthStore interface {
	CollectorHealth(ctx context.Context, expectedKinds []string, maxAge time.Duration) ([]CollectorHealth, error)
}

type MetricSample struct {
	ID               int64     `json:"id,omitempty"`
	ObservedAt       time.Time `json:"observed_at"`
	Scope            Scope     `json:"scope"`
	Source           string    `json:"source"`
	Resolution       string    `json:"resolution"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryBytes      int64     `json:"memory_bytes"`
	MemoryLimitBytes int64     `json:"memory_limit_bytes"`
	NetworkRXBytes   int64     `json:"network_rx_bytes"`
	NetworkTXBytes   int64     `json:"network_tx_bytes"`
	BlockReadBytes   int64     `json:"block_read_bytes"`
	BlockWriteBytes  int64     `json:"block_write_bytes"`
	DiskUsageBytes   *int64    `json:"disk_usage_bytes,omitempty"`
	RestartCount     int64     `json:"restart_count"`
	Marker           string    `json:"marker,omitempty"`
}

type DeploymentMarker struct {
	DeploymentID string    `json:"deployment_id"`
	Marker       string    `json:"marker"`
	ObservedAt   time.Time `json:"observed_at"`
}

type MetricQuery struct {
	Scope Scope
	Since time.Time
	Until time.Time
	Step  time.Duration
	Mode  string
	Limit int
}

type MetricPoint struct {
	ObservedAt       time.Time `json:"observed_at"`
	ReplicaID        string    `json:"replica_id,omitempty"`
	CPUPercent       float64   `json:"cpu_percent"`
	MemoryBytes      int64     `json:"memory_bytes"`
	MemoryLimitBytes int64     `json:"memory_limit_bytes"`
	NetworkRXBytes   int64     `json:"network_rx_bytes"`
	NetworkTXBytes   int64     `json:"network_tx_bytes"`
	BlockReadBytes   int64     `json:"block_read_bytes"`
	BlockWriteBytes  int64     `json:"block_write_bytes"`
	DiskUsageBytes   *int64    `json:"disk_usage_bytes,omitempty"`
	RestartCount     int64     `json:"restart_count"`
	Replicas         int       `json:"replicas"`
	Samples          int       `json:"samples"`
}

type MetricSeries struct {
	Mode            string             `json:"mode"`
	Step            string             `json:"step"`
	Points          []MetricPoint      `json:"points"`
	Markers         []DeploymentMarker `json:"deployment_markers"`
	Collectors      []string           `json:"collectors"`
	CollectorHealth []CollectorHealth  `json:"collector_health"`
}

type Store interface {
	IngestLog(ctx context.Context, record LogRecord) (IngestResult, error)
	QueryLogs(ctx context.Context, query LogQuery) (LogPage, error)
	IngestionCounters(ctx context.Context, scope Scope, since time.Time) ([]IngestionCounter, error)
	IngestMetric(ctx context.Context, sample MetricSample) error
	RecordDeploymentMarker(ctx context.Context, scope Scope, marker string, at time.Time) error
	QueryMetrics(ctx context.Context, query MetricQuery) (MetricSeries, error)
	ValidateScope(ctx context.Context, scope Scope) error
	Cleanup(ctx context.Context, before time.Time) (CleanupResult, error)
}

type CleanupResult struct {
	Logs        int64 `json:"logs"`
	Metrics     int64 `json:"metrics"`
	Windows     int64 `json:"ingestion_windows"`
	Checkpoints int64 `json:"collector_checkpoints"`
	Collectors  int64 `json:"collectors"`
}
