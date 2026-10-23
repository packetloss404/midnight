package doctor

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// TimeSyncDriftStatus is the normalized clock drift status.
type TimeSyncDriftStatus string

const (
	// TimeSyncDriftOK indicates clock drift is within the warning threshold.
	TimeSyncDriftOK TimeSyncDriftStatus = "ok"
	// TimeSyncDriftWarning indicates clock drift is above the warning threshold.
	TimeSyncDriftWarning TimeSyncDriftStatus = "warning"
	// TimeSyncDriftCritical indicates clock drift is at or above the ACME-blocking threshold.
	TimeSyncDriftCritical TimeSyncDriftStatus = "critical"
)

const (
	// timeSyncWarnDriftSeconds is the panel-locked warning threshold (>= 1s).
	timeSyncWarnDriftSeconds = 1.0
	// timeSyncCriticalDriftSeconds is the ACME-blocking critical threshold (>= 5s).
	timeSyncCriticalDriftSeconds = 5.0
)

// TimeSyncDriftReviewSummary is a JSON-friendly classifier output.
type TimeSyncDriftReviewSummary struct {
	OffsetSeconds            float64             `json:"offset_seconds"`
	AbsoluteOffsetSeconds    float64             `json:"absolute_offset_seconds"`
	Status                   TimeSyncDriftStatus `json:"status"`
	WarningThresholdSeconds  float64             `json:"warning_threshold_seconds"`
	CriticalThresholdSeconds float64             `json:"critical_threshold_seconds"`
	Reason                   string              `json:"reason"`
}

// ClassifyTimeSyncDrift classifies a clock offset (signed seconds vs reference).
// Positive offsets indicate the host is ahead of the reference; negative behind.
func ClassifyTimeSyncDrift(offsetSeconds float64) (TimeSyncDriftStatus, error) {
	if math.IsNaN(offsetSeconds) || math.IsInf(offsetSeconds, 0) {
		return "", fmt.Errorf("time sync offset %.4f must be a finite number", offsetSeconds)
	}
	abs := math.Abs(offsetSeconds)
	if abs >= timeSyncCriticalDriftSeconds {
		return TimeSyncDriftCritical, nil
	}
	if abs >= timeSyncWarnDriftSeconds {
		return TimeSyncDriftWarning, nil
	}
	return TimeSyncDriftOK, nil
}

// ClassifyTimeSyncDriftDuration is a duration-typed wrapper around ClassifyTimeSyncDrift.
func ClassifyTimeSyncDriftDuration(offset time.Duration) (TimeSyncDriftStatus, error) {
	return ClassifyTimeSyncDrift(offset.Seconds())
}

// NewTimeSyncDriftReviewSummary returns a deterministic clock drift summary.
func NewTimeSyncDriftReviewSummary(offsetSeconds float64) (TimeSyncDriftReviewSummary, error) {
	status, err := ClassifyTimeSyncDrift(offsetSeconds)
	if err != nil {
		return TimeSyncDriftReviewSummary{}, err
	}
	return TimeSyncDriftReviewSummary{
		OffsetSeconds:            offsetSeconds,
		AbsoluteOffsetSeconds:    math.Abs(offsetSeconds),
		Status:                   status,
		WarningThresholdSeconds:  timeSyncWarnDriftSeconds,
		CriticalThresholdSeconds: timeSyncCriticalDriftSeconds,
		Reason:                   timeSyncDriftReason(status),
	}, nil
}

// MarshalTimeSyncDriftReviewSummaryJSON emits the stable JSON representation.
func MarshalTimeSyncDriftReviewSummaryJSON(summary TimeSyncDriftReviewSummary) ([]byte, error) {
	return json.Marshal(summary)
}

func timeSyncDriftReason(status TimeSyncDriftStatus) string {
	switch status {
	case TimeSyncDriftCritical:
		return "host clock drift is at or above the panel-locked ACME-blocking threshold"
	case TimeSyncDriftWarning:
		return "host clock drift is above the doctor warning threshold but below the ACME-blocking threshold"
	default:
		return "host clock drift is below the doctor warning threshold"
	}
}
