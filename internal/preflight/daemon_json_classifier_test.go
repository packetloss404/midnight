package preflight

import (
	"encoding/json"
	"testing"
)

func TestClassifyDaemonBaselineConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		err             *DockerDaemonConflictError
		wantOverall     DaemonBaselineSeverity
		wantBlockedLen  int
		wantWarningsLen int
	}{
		{
			name:        "nil error",
			err:         nil,
			wantOverall: DaemonBaselineSeverityWarn,
		},
		{
			name:           "log driver block",
			err:            &DockerDaemonConflictError{Paths: []string{"log-driver"}},
			wantOverall:    DaemonBaselineSeverityBlock,
			wantBlockedLen: 1,
		},
		{
			name:            "log opts warn",
			err:             &DockerDaemonConflictError{Paths: []string{"log-opts.max-size"}},
			wantOverall:     DaemonBaselineSeverityWarn,
			wantWarningsLen: 1,
		},
		{
			name:            "mixed",
			err:             &DockerDaemonConflictError{Paths: []string{"log-opts.max-size", "log-driver"}},
			wantOverall:     DaemonBaselineSeverityBlock,
			wantBlockedLen:  1,
			wantWarningsLen: 1,
		},
		{
			name:           "unknown path defaults to block",
			err:            &DockerDaemonConflictError{Paths: []string{"some.unknown.key"}},
			wantOverall:    DaemonBaselineSeverityBlock,
			wantBlockedLen: 1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			review := ClassifyDaemonBaselineConflict(tt.err)
			if review.OverallSeverity != tt.wantOverall {
				t.Fatalf("OverallSeverity = %q, want %q", review.OverallSeverity, tt.wantOverall)
			}
			if len(review.BlockedPaths) != tt.wantBlockedLen {
				t.Fatalf("BlockedPaths = %+v, want len %d", review.BlockedPaths, tt.wantBlockedLen)
			}
			if len(review.WarningPaths) != tt.wantWarningsLen {
				t.Fatalf("WarningPaths = %+v, want len %d", review.WarningPaths, tt.wantWarningsLen)
			}
		})
	}
}

func TestMarshalDaemonBaselineConflictReviewJSON(t *testing.T) {
	t.Parallel()

	review := ClassifyDaemonBaselineConflict(&DockerDaemonConflictError{Paths: []string{"log-driver"}})
	data, err := MarshalDaemonBaselineConflictReviewJSON(review)
	if err != nil {
		t.Fatalf("MarshalDaemonBaselineConflictReviewJSON() error = %v", err)
	}
	var decoded DaemonBaselineConflictReview
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(review) error = %v", err)
	}
	if decoded.OverallSeverity != DaemonBaselineSeverityBlock || len(decoded.Classifications) != 1 {
		t.Fatalf("decoded = %+v, want block with one classification", decoded)
	}
}

func TestMergeDockerDaemonBaselinePreservingOperatorKeysReports(t *testing.T) {
	t.Parallel()

	existing := []byte(`{
		"features": {"buildkit": true},
		"live-restore": true,
		"log-driver": "json-file",
		"log-opts": {"max-file": "5", "max-size": "10m"},
		"no-new-privileges": true,
		"registry-mirrors": ["https://mirror.example.test"],
		"dns": ["10.0.0.1"],
		"storage-driver": "overlay2"
	}`)
	result, preserved, err := MergeDockerDaemonBaselinePreservingOperatorKeys(existing)
	if err != nil {
		t.Fatalf("MergeDockerDaemonBaselinePreservingOperatorKeys() error = %v", err)
	}
	if result.Changed {
		t.Fatal("Changed = true, want false for already-baseline content")
	}
	wantPreserved := map[string]bool{"registry-mirrors": true, "dns": true}
	if len(preserved) != len(wantPreserved) {
		t.Fatalf("preserved keys = %+v, want %v", preserved, wantPreserved)
	}
	for _, key := range preserved {
		if !wantPreserved[key] {
			t.Fatalf("preserved key %q is unexpected", key)
		}
	}
}

func TestMergeDockerDaemonBaselinePreservingOperatorKeysHandlesEmpty(t *testing.T) {
	t.Parallel()

	_, preserved, err := MergeDockerDaemonBaselinePreservingOperatorKeys(nil)
	if err != nil {
		t.Fatalf("MergeDockerDaemonBaselinePreservingOperatorKeys(nil) error = %v", err)
	}
	if len(preserved) != 0 {
		t.Fatalf("preserved = %+v, want none for empty existing", preserved)
	}
}
