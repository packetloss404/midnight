package doctor

import (
	"encoding/json"
	"math"
	"testing"
)

func TestClassifyDiskUsageRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		usedRatio float64
		want      ResourcePressureStatus
	}{
		{name: "ok at warning boundary", usedRatio: 0.80, want: ResourcePressureOK},
		{name: "warning above warning boundary", usedRatio: 0.8001, want: ResourcePressureWarning},
		{name: "warning below critical boundary", usedRatio: 0.8499, want: ResourcePressureWarning},
		{name: "critical at alert boundary", usedRatio: 0.85, want: ResourcePressureCritical},
		{name: "critical above alert boundary", usedRatio: 0.99, want: ResourcePressureCritical},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyDiskUsageRatio(tt.usedRatio)
			if err != nil {
				t.Fatalf("ClassifyDiskUsageRatio() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ClassifyDiskUsageRatio(%v) = %q, want %q", tt.usedRatio, got, tt.want)
			}
		})
	}
}

func TestClassifyConntrackUsageRatio(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		usedRatio float64
		want      ResourcePressureStatus
	}{
		{name: "ok at warning boundary", usedRatio: 0.80, want: ResourcePressureOK},
		{name: "warning above alert boundary", usedRatio: 0.8001, want: ResourcePressureWarning},
		{name: "warning below critical boundary", usedRatio: 0.9499, want: ResourcePressureWarning},
		{name: "critical at saturation boundary", usedRatio: 0.95, want: ResourcePressureCritical},
		{name: "critical above saturation boundary", usedRatio: 1, want: ResourcePressureCritical},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyConntrackUsageRatio(tt.usedRatio)
			if err != nil {
				t.Fatalf("ClassifyConntrackUsageRatio() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ClassifyConntrackUsageRatio(%v) = %q, want %q", tt.usedRatio, got, tt.want)
			}
		})
	}
}

func TestClassifyResourcePressureRejectsInvalidRatios(t *testing.T) {
	t.Parallel()

	for _, usedRatio := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		if _, err := ClassifyDiskUsageRatio(usedRatio); err == nil {
			t.Fatalf("ClassifyDiskUsageRatio(%v) error = nil, want error", usedRatio)
		}
		if _, err := ClassifyConntrackUsageRatio(usedRatio); err == nil {
			t.Fatalf("ClassifyConntrackUsageRatio(%v) error = nil, want error", usedRatio)
		}
	}
}

func TestResourcePressureReviewSummaryJSON(t *testing.T) {
	t.Parallel()

	summary, err := NewDiskPressureReviewSummary(0.85)
	if err != nil {
		t.Fatalf("NewDiskPressureReviewSummary() error = %v", err)
	}
	data, err := MarshalResourcePressureReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalResourcePressureReviewSummaryJSON() error = %v", err)
	}
	want := `{"resource":"disk","status":"critical","used_ratio":0.85,"warning_threshold":0.8,"critical_threshold":0.85,"reason":"disk usage is at or above the Phase 0 critical alert threshold"}`
	if string(data) != want {
		t.Fatalf("MarshalResourcePressureReviewSummaryJSON() = %s, want %s", data, want)
	}

	var decoded ResourcePressureReviewSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}
	if decoded.Resource != "disk" || decoded.Status != ResourcePressureCritical || decoded.UsedRatio != 0.85 {
		t.Fatalf("decoded summary = %+v, want disk critical", decoded)
	}
}

func TestConntrackPressureReviewSummary(t *testing.T) {
	t.Parallel()

	summary, err := NewConntrackPressureReviewSummary(0.8001)
	if err != nil {
		t.Fatalf("NewConntrackPressureReviewSummary() error = %v", err)
	}
	if summary.Resource != "conntrack" || summary.Status != ResourcePressureWarning {
		t.Fatalf("summary = %+v, want conntrack warning", summary)
	}
	if summary.WarningThreshold != 0.80 || summary.CriticalThreshold != 0.95 || summary.Reason == "" {
		t.Fatalf("summary thresholds/reason = %+v, want populated thresholds and reason", summary)
	}
}
