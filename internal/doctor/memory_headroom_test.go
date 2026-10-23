package doctor

import (
	"encoding/json"
	"math"
	"testing"
)

func TestClassifyMemoryHeadroom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		available float64
		want      MemoryHeadroomStatus
	}{
		{name: "ok above warning", available: 0.50, want: MemoryHeadroomOK},
		{name: "ok at warning boundary", available: 0.20, want: MemoryHeadroomOK},
		{name: "low between thresholds", available: 0.15, want: MemoryHeadroomLow},
		{name: "low just above critical", available: 0.1001, want: MemoryHeadroomLow},
		{name: "critical at boundary", available: 0.10, want: MemoryHeadroomCritical},
		{name: "critical near zero", available: 0.01, want: MemoryHeadroomCritical},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyMemoryHeadroom(tt.available)
			if err != nil {
				t.Fatalf("ClassifyMemoryHeadroom(%v) error = %v", tt.available, err)
			}
			if got != tt.want {
				t.Fatalf("ClassifyMemoryHeadroom(%v) = %q, want %q", tt.available, got, tt.want)
			}
		})
	}
}

func TestClassifyMemoryHeadroomRejectsInvalid(t *testing.T) {
	t.Parallel()

	for _, ratio := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		if _, err := ClassifyMemoryHeadroom(ratio); err == nil {
			t.Fatalf("ClassifyMemoryHeadroom(%v) error = nil, want error", ratio)
		}
	}
}

func TestNewMemoryHeadroomReviewSummary(t *testing.T) {
	t.Parallel()

	summary, err := NewMemoryHeadroomReviewSummary(0.05)
	if err != nil {
		t.Fatalf("NewMemoryHeadroomReviewSummary() error = %v", err)
	}
	if summary.Status != MemoryHeadroomCritical || summary.WarningThreshold != memoryHeadroomWarningRatio {
		t.Fatalf("summary = %+v, want critical with thresholds", summary)
	}
	data, err := MarshalMemoryHeadroomReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalMemoryHeadroomReviewSummaryJSON() error = %v", err)
	}
	var decoded MemoryHeadroomReviewSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}
	if decoded.Status != MemoryHeadroomCritical || decoded.AvailableRatio != 0.05 {
		t.Fatalf("decoded = %+v, want critical 0.05", decoded)
	}
}
