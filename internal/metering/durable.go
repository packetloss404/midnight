// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package metering

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"
)

const (
	ResourceSourceArtifactBytes     Resource = "source_artifact_bytes"
	ResourceProjectCount            Resource = "project_count"
	ResourceServiceCount            Resource = "service_count"
	ResourceEnvironmentCount        Resource = "environment_count"
	ResourceVolumeCount             Resource = "volume_count"
	ResourcePublicHTTPRequests      Resource = "public_http_requests"
	ResourcePublicHTTPResponseBytes Resource = "public_http_response_bytes"
)

type ValueKind string

const (
	ValueIncrement ValueKind = "increment"
	ValueGauge     ValueKind = "gauge"
)

type CoverageStatus string

const (
	CoverageComplete    CoverageStatus = "complete"
	CoveragePartial     CoverageStatus = "partial"
	CoverageUnavailable CoverageStatus = "unavailable"
)

var (
	ErrNotAuthorized       = errors.New("metering: workspace access denied")
	ErrEventConflict       = errors.New("metering: producer replay conflicts with existing event")
	ErrLimitExceeded       = errors.New("metering: usage limit exceeded")
	ErrReservationConflict = errors.New("metering: reservation conflicts with existing intent")
	ErrUnavailable         = errors.New("metering: durable usage store unavailable")
)

var ledgerOpaqueIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{0,254}$`)

// LedgerEvent is immutable producer evidence. SourceInstance and
// SourceEventID form the replay/deduplication identity; Sequence is assigned by
// the durable store and is the replay cursor exposed to internal consumers.
type LedgerEvent struct {
	Sequence       int64          `json:"sequence,omitempty"`
	EventID        string         `json:"event_id"`
	Source         string         `json:"source"`
	SourceInstance string         `json:"source_instance"`
	SourceEventID  string         `json:"source_event_id"`
	WorkspaceID    string         `json:"workspace_id"`
	ProjectID      string         `json:"project_id,omitempty"`
	EnvironmentID  string         `json:"environment_id,omitempty"`
	Resource       Resource       `json:"dimension"`
	Quantity       int64          `json:"quantity"`
	Kind           ValueKind      `json:"value_kind"`
	IntervalStart  *time.Time     `json:"interval_start,omitempty"`
	ObservedAt     time.Time      `json:"observed_at"`
	RecordedAt     time.Time      `json:"recorded_at,omitempty"`
	Attributes     map[string]any `json:"attributes,omitempty"`
}

func (event LedgerEvent) Validate() error {
	for name, value := range map[string]string{
		"event_id": event.EventID, "source": event.Source,
		"source_instance": event.SourceInstance, "source_event_id": event.SourceEventID,
		"workspace_id": event.WorkspaceID,
	} {
		if !ledgerOpaqueIDRE.MatchString(value) {
			return fmt.Errorf("metering: %s must be a bounded opaque identifier", name)
		}
	}
	if event.Source != "resource_reconciler" && event.Source != "runtime_metrics" && event.Source != "traefik_access" && event.Source != "build_lifecycle" {
		return fmt.Errorf("metering: unsupported evidence source %q", event.Source)
	}
	for name, value := range map[string]string{"project_id": event.ProjectID, "environment_id": event.EnvironmentID} {
		if value != "" && !ledgerOpaqueIDRE.MatchString(value) {
			return fmt.Errorf("metering: %s must be a bounded opaque identifier", name)
		}
	}
	if event.EnvironmentID != "" && event.ProjectID == "" {
		return errors.New("metering: environment evidence requires project scope")
	}
	if !KnownResource(event.Resource) {
		return fmt.Errorf("metering: unsupported dimension %q", event.Resource)
	}
	if event.Quantity < 0 {
		return errors.New("metering: quantity must not be negative")
	}
	if event.Kind != ValueIncrement && event.Kind != ValueGauge {
		return fmt.Errorf("metering: unsupported value kind %q", event.Kind)
	}
	if event.ObservedAt.IsZero() {
		return ErrZeroObservedAt
	}
	if event.IntervalStart != nil && event.IntervalStart.After(event.ObservedAt) {
		return errors.New("metering: interval start must not be after observed_at")
	}
	for key, value := range event.Attributes {
		if key != "sample_id" && key != "previous_sample_id" {
			return fmt.Errorf("metering: attribute %q is not in the redaction-safe evidence vocabulary", key)
		}
		text, ok := value.(string)
		if !ok || !ledgerOpaqueIDRE.MatchString(text) {
			return fmt.Errorf("metering: attribute %q must be an opaque identifier", key)
		}
	}
	return nil
}

func sameLedgerEventIntent(left, right LedgerEvent) bool {
	return left.EventID == right.EventID && left.Source == right.Source &&
		left.SourceInstance == right.SourceInstance && left.SourceEventID == right.SourceEventID &&
		left.WorkspaceID == right.WorkspaceID && left.ProjectID == right.ProjectID &&
		left.EnvironmentID == right.EnvironmentID && left.Resource == right.Resource &&
		left.Quantity == right.Quantity && left.Kind == right.Kind &&
		equalOptionalTime(left.IntervalStart, right.IntervalStart) &&
		left.ObservedAt.Equal(right.ObservedAt) && reflect.DeepEqual(normalizeAttributes(left.Attributes), normalizeAttributes(right.Attributes))
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func normalizeAttributes(attributes map[string]any) map[string]any {
	if len(attributes) == 0 {
		return map[string]any{}
	}
	return attributes
}

func KnownResource(resource Resource) bool {
	switch resource {
	case ResourceSourceArtifactBytes, ResourceProjectCount, ResourceServiceCount,
		ResourceEnvironmentCount, ResourceVolumeCount, ResourceBuildSeconds, ResourceCPUSeconds,
		ResourceMemoryByteSeconds, ResourcePublicHTTPRequests, ResourcePublicHTTPResponseBytes:
		return true
	default:
		return false
	}
}

type AppendResult struct {
	Sequence int64 `json:"sequence"`
	Stored   bool  `json:"stored"`
}

type Coverage struct {
	Status           CoverageStatus `json:"status"`
	Source           string         `json:"source"`
	LastObservedAt   *time.Time     `json:"last_observed_at,omitempty"`
	MissingIntervals int            `json:"missing_intervals"`
	Note             string         `json:"note,omitempty"`
}

type UsageLine struct {
	Resource    Resource `json:"dimension"`
	Used        int64    `json:"used"`
	Limit       *int64   `json:"limit,omitempty"`
	Enforceable bool     `json:"enforceable"`
	Unit        string   `json:"unit"`
	Coverage    Coverage `json:"coverage"`
}

type Summary struct {
	WorkspaceID   string      `json:"workspace_id"`
	PolicyProfile string      `json:"policy_profile"`
	WindowStart   time.Time   `json:"window_start"`
	WindowEnd     time.Time   `json:"window_end"`
	AsOf          time.Time   `json:"as_of"`
	ReplayAfter   int64       `json:"replay_after"`
	Usage         []UsageLine `json:"usage"`
	Disclosures   []string    `json:"disclosures"`
}

type SummaryQuery struct {
	WorkspaceID string
	ActorUserID string
	WindowStart time.Time
	WindowEnd   time.Time
}

type ReservationRequest struct {
	WorkspaceID    string
	ActorUserID    string
	Resource       Resource
	Quantity       int64
	IdempotencyKey string
	IntentHash     string
	TTL            time.Duration
}

type Reservation struct {
	ID               string    `json:"id"`
	WorkspaceID      string    `json:"workspace_id"`
	Resource         Resource  `json:"dimension"`
	Quantity         int64     `json:"quantity"`
	State            string    `json:"state"`
	ExpiresAt        time.Time `json:"expires_at"`
	IdempotentReplay bool      `json:"idempotent_replay"`
}

// ScheduledReconcileRequest bounds one background evidence pass. The worker
// supplies a recent recovery window; replay identities make overlapping passes
// harmless while the workspace batch keeps a single pass finite.
type ScheduledReconcileRequest struct {
	Since         time.Time
	Until         time.Time
	MaxWorkspaces int
	StageTimeout  time.Duration
}

type ScheduledReconcileResult struct {
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
	WorkspacesExamined int       `json:"workspaces_examined"`
	WorkspaceErrors    int       `json:"workspace_errors"`
	ProducerTimeouts   int       `json:"producer_timeouts"`
	ProducerBounds     int       `json:"producer_bounds"`
	SkippedConcurrent  bool      `json:"skipped_concurrent"`
}

type LimitError struct {
	Resource  Resource
	Used      int64
	Reserved  int64
	Requested int64
	Limit     int64
}

func (err *LimitError) Error() string { return ErrLimitExceeded.Error() }
func (err *LimitError) Unwrap() error { return ErrLimitExceeded }

type Store interface {
	Append(context.Context, LedgerEvent) (AppendResult, error)
	Summary(context.Context, SummaryQuery) (Summary, error)
	Reserve(context.Context, ReservationRequest) (Reservation, error)
}

func DefaultWindow(now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now
}

func Disclosures() []string {
	return []string{
		"Source artifact bytes and logical resource counts are durable gauges.",
		"Build time is measured from canonical source-build lifecycle markers; deployments created before those markers are reported as an evidence gap.",
		"CPU and memory are estimates from observed samples; gaps are reported and never silently interpolated.",
		"HTTP request and response-byte totals include public Traefik access records only; request-body bytes are not counted.",
		"Docker network counters are excluded because they cannot distinguish private from public traffic.",
		"Volume capacity is requested configuration, not a kernel-enforced byte quota; volume count is logical control-plane admission.",
		"Contributions and donations do not change limits or plan assignment.",
	}
}

func unitFor(resource Resource) string {
	switch resource {
	case ResourceSourceArtifactBytes, ResourcePublicHTTPResponseBytes:
		return "bytes"
	case ResourceBuildSeconds, ResourceCPUSeconds:
		return "seconds"
	case ResourceMemoryByteSeconds:
		return "byte-seconds"
	default:
		return "count"
	}
}
