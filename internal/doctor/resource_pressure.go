package doctor

import (
	"encoding/json"
	"fmt"
	"math"
)

// ResourcePressureStatus is the normalized review status for resource pressure.
type ResourcePressureStatus string

const (
	ResourcePressureOK       ResourcePressureStatus = "ok"
	ResourcePressureWarning  ResourcePressureStatus = "warning"
	ResourcePressureCritical ResourcePressureStatus = "critical"
)

const (
	diskUsageWarningRatio       = 0.80
	diskUsageCriticalRatio      = 0.85
	conntrackUsageWarningRatio  = 0.80
	conntrackUsageCriticalRatio = 0.95
)

// ResourcePressureReviewSummary is a JSON-friendly, support-safe pressure view.
type ResourcePressureReviewSummary struct {
	Resource          string                 `json:"resource"`
	Status            ResourcePressureStatus `json:"status"`
	UsedRatio         float64                `json:"used_ratio"`
	WarningThreshold  float64                `json:"warning_threshold,omitempty"`
	CriticalThreshold float64                `json:"critical_threshold,omitempty"`
	Reason            string                 `json:"reason"`
}

// ClassifyDiskUsageRatio classifies a disk used ratio without inspecting the host.
func ClassifyDiskUsageRatio(usedRatio float64) (ResourcePressureStatus, error) {
	if err := validateResourceRatio(usedRatio); err != nil {
		return "", err
	}
	if usedRatio >= diskUsageCriticalRatio {
		return ResourcePressureCritical, nil
	}
	if usedRatio > diskUsageWarningRatio {
		return ResourcePressureWarning, nil
	}
	return ResourcePressureOK, nil
}

// ClassifyConntrackUsageRatio classifies a conntrack used ratio without inspecting the host.
func ClassifyConntrackUsageRatio(usedRatio float64) (ResourcePressureStatus, error) {
	if err := validateResourceRatio(usedRatio); err != nil {
		return "", err
	}
	if usedRatio >= conntrackUsageCriticalRatio {
		return ResourcePressureCritical, nil
	}
	if usedRatio > conntrackUsageWarningRatio {
		return ResourcePressureWarning, nil
	}
	return ResourcePressureOK, nil
}

// NewDiskPressureReviewSummary returns a deterministic disk pressure summary.
func NewDiskPressureReviewSummary(usedRatio float64) (ResourcePressureReviewSummary, error) {
	status, err := ClassifyDiskUsageRatio(usedRatio)
	if err != nil {
		return ResourcePressureReviewSummary{}, err
	}
	return ResourcePressureReviewSummary{
		Resource:          "disk",
		Status:            status,
		UsedRatio:         usedRatio,
		WarningThreshold:  diskUsageWarningRatio,
		CriticalThreshold: diskUsageCriticalRatio,
		Reason:            diskPressureReason(status),
	}, nil
}

// NewConntrackPressureReviewSummary returns a deterministic conntrack pressure summary.
func NewConntrackPressureReviewSummary(usedRatio float64) (ResourcePressureReviewSummary, error) {
	status, err := ClassifyConntrackUsageRatio(usedRatio)
	if err != nil {
		return ResourcePressureReviewSummary{}, err
	}
	return ResourcePressureReviewSummary{
		Resource:          "conntrack",
		Status:            status,
		UsedRatio:         usedRatio,
		WarningThreshold:  conntrackUsageWarningRatio,
		CriticalThreshold: conntrackUsageCriticalRatio,
		Reason:            conntrackPressureReason(status),
	}, nil
}

// MarshalResourcePressureReviewSummaryJSON emits stable JSON for review-only callers.
func MarshalResourcePressureReviewSummaryJSON(summary ResourcePressureReviewSummary) ([]byte, error) {
	return json.Marshal(summary)
}

func validateResourceRatio(usedRatio float64) error {
	if math.IsNaN(usedRatio) || math.IsInf(usedRatio, 0) || usedRatio < 0 || usedRatio > 1 {
		return fmt.Errorf("resource used ratio %.4f must be between 0 and 1", usedRatio)
	}
	return nil
}

func diskPressureReason(status ResourcePressureStatus) string {
	switch status {
	case ResourcePressureCritical:
		return "disk usage is at or above the Phase 0 critical alert threshold"
	case ResourcePressureWarning:
		return "disk usage is above the pre-critical review threshold"
	default:
		return "disk usage is at or below the pre-critical review threshold"
	}
}

func conntrackPressureReason(status ResourcePressureStatus) string {
	switch status {
	case ResourcePressureCritical:
		return "conntrack usage is at or above the critical saturation threshold"
	case ResourcePressureWarning:
		return "conntrack usage is above the Phase 0 warning alert threshold"
	default:
		return "conntrack usage is at or below the Phase 0 warning alert threshold"
	}
}
