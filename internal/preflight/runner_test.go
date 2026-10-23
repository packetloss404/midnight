package preflight

import (
	"context"
	"testing"
)

func TestRunChecks(t *testing.T) {
	t.Parallel()

	checks := []RunnableCheck{
		{ID: "PRE-ONE", Description: "one", Severity: "fatal", Run: func(context.Context) Result { return Result{Status: StatusPass} }},
		{ID: "PRE-TWO", Description: "two", Severity: "warn"},
		{ID: "PRE-THREE", Description: "three", Severity: "fatal", Run: func(context.Context) Result { return Result{} }},
	}

	results := RunChecks(context.Background(), checks)
	if len(results) != len(checks) {
		t.Fatalf("len(results) = %d, want %d", len(results), len(checks))
	}

	tests := []struct {
		index      int
		wantID     string
		wantStatus Status
		wantErr    bool
	}{
		{index: 0, wantID: "PRE-ONE", wantStatus: StatusPass},
		{index: 1, wantID: "PRE-TWO", wantStatus: StatusSkip},
		{index: 2, wantID: "PRE-THREE", wantStatus: StatusFail, wantErr: true},
	}

	for _, tt := range tests {
		got := results[tt.index]
		if got.ID != tt.wantID || got.Status != tt.wantStatus || (got.Err != nil) != tt.wantErr {
			t.Fatalf("results[%d] = {ID:%q Status:%q Err:%v}, want {ID:%q Status:%q Err?%v}", tt.index, got.ID, got.Status, got.Err, tt.wantID, tt.wantStatus, tt.wantErr)
		}
	}
}
