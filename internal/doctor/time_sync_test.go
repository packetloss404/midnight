package doctor

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestClassifyTimeSyncDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		offset float64
		want   TimeSyncDriftStatus
	}{
		{name: "ok at zero", offset: 0, want: TimeSyncDriftOK},
		{name: "ok below warning", offset: 0.5, want: TimeSyncDriftOK},
		{name: "warning at threshold", offset: 1.0, want: TimeSyncDriftWarning},
		{name: "warning negative", offset: -1.5, want: TimeSyncDriftWarning},
		{name: "critical at threshold", offset: 5.0, want: TimeSyncDriftCritical},
		{name: "critical negative", offset: -10, want: TimeSyncDriftCritical},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ClassifyTimeSyncDrift(tt.offset)
			if err != nil {
				t.Fatalf("ClassifyTimeSyncDrift(%v) error = %v", tt.offset, err)
			}
			if got != tt.want {
				t.Fatalf("ClassifyTimeSyncDrift(%v) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

func TestClassifyTimeSyncDriftRejectsInvalid(t *testing.T) {
	t.Parallel()

	for _, offset := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := ClassifyTimeSyncDrift(offset); err == nil {
			t.Fatalf("ClassifyTimeSyncDrift(%v) error = nil, want error", offset)
		}
	}
}

func TestClassifyTimeSyncDriftDuration(t *testing.T) {
	t.Parallel()

	got, err := ClassifyTimeSyncDriftDuration(2 * time.Second)
	if err != nil {
		t.Fatalf("ClassifyTimeSyncDriftDuration() error = %v", err)
	}
	if got != TimeSyncDriftWarning {
		t.Fatalf("got = %q, want %q", got, TimeSyncDriftWarning)
	}
}

func TestNewTimeSyncDriftReviewSummary(t *testing.T) {
	t.Parallel()

	summary, err := NewTimeSyncDriftReviewSummary(-3.5)
	if err != nil {
		t.Fatalf("NewTimeSyncDriftReviewSummary() error = %v", err)
	}
	if summary.Status != TimeSyncDriftWarning || summary.AbsoluteOffsetSeconds != 3.5 {
		t.Fatalf("summary = %+v, want warning with 3.5 absolute offset", summary)
	}
	data, err := MarshalTimeSyncDriftReviewSummaryJSON(summary)
	if err != nil {
		t.Fatalf("MarshalTimeSyncDriftReviewSummaryJSON() error = %v", err)
	}
	var decoded TimeSyncDriftReviewSummary
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(summary) error = %v", err)
	}
	if decoded.Status != TimeSyncDriftWarning || decoded.OffsetSeconds != -3.5 {
		t.Fatalf("decoded = %+v, want warning -3.5", decoded)
	}
}
