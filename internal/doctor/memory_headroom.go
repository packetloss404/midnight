package doctor

import (
	"encoding/json"
	"fmt"
	"math"
)

// MemoryHeadroomStatus is the normalized status for available host memory.
type MemoryHeadroomStatus string

const (
	// MemoryHeadroomOK indicates available memory is above the warning threshold.
	MemoryHeadroomOK MemoryHeadroomStatus = "ok"
	// MemoryHeadroomLow indicates available memory is below the warning threshold.
	MemoryHeadroomLow MemoryHeadroomStatus = "low"
	// MemoryHeadroomCritical indicates available memory is below the critical threshold.
	MemoryHeadroomCritical MemoryHeadroomStatus = "critical"
)

const (
	memoryHeadroomWarningRatio  = 0.20
	memoryHeadroomCriticalRatio = 0.10
)

// MemoryHeadroomReviewSummary is a JSON-friendly summary used by doctor wiring.
type MemoryHeadroomReviewSummary struct {
	AvailableRatio    float64              `json:"available_ratio"`
	Status            MemoryHeadroomStatus `json:"status"`
	WarningThreshold  float64              `json:"warning_threshold"`
	CriticalThreshold float64              `json:"critical_threshold"`
	Reason            string               `json:"reason"`
}

// ClassifyMemoryHeadroom classifies the supplied available-memory ratio.
func ClassifyMemoryHeadroom(availableRatio float64) (MemoryHeadroomStatus, error) {
	if math.IsNaN(availableRatio) || math.IsInf(availableRatio, 0) || availableRatio < 0 || availableRatio > 1 {
		return "", fmt.Errorf("memory available ratio %.4f must be between 0 and 1", availableRatio)
	}
	if availableRatio <= memoryHeadroomCriticalRatio {
		return MemoryHeadroomCritical, nil
	}
	if availableRatio < memoryHeadroomWarningRatio {
		return MemoryHeadroomLow, nil
	}
	return MemoryHeadroomOK, nil
}

// NewMemoryHeadroomReviewSummary returns a deterministic summary for callers.
func NewMemoryHeadroomReviewSummary(availableRatio float64) (MemoryHeadroomReviewSummary, error) {
	status, err := ClassifyMemoryHeadroom(availableRatio)
	if err != nil {
		return MemoryHeadroomReviewSummary{}, err
	}
	return MemoryHeadroomReviewSummary{
		AvailableRatio:    availableRatio,
		Status:            status,
		WarningThreshold:  memoryHeadroomWarningRatio,
		CriticalThreshold: memoryHeadroomCriticalRatio,
		Reason:            memoryHeadroomReason(status),
	}, nil
}

// MarshalMemoryHeadroomReviewSummaryJSON emits the stable JSON representation.
func MarshalMemoryHeadroomReviewSummaryJSON(summary MemoryHeadroomReviewSummary) ([]byte, error) {
	return json.Marshal(summary)
}

func memoryHeadroomReason(status MemoryHeadroomStatus) string {
	switch status {
	case MemoryHeadroomCritical:
		return "available memory is at or below the Phase 0 doctor critical alert threshold"
	case MemoryHeadroomLow:
		return "available memory is below the Phase 0 doctor warning threshold"
	default:
		return "available memory is above the Phase 0 doctor warning threshold"
	}
}
