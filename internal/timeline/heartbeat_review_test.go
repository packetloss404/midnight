package timeline

import (
	"encoding/json"
	"testing"
	"time"
)

func TestReviewDeploymentHeartbeatGapClassifiesStatus(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		last       time.Time
		wantStatus DeploymentHeartbeatGapStatus
		wantGap    int64
		wantIssues []DeploymentHeartbeatGapIssueCode
		wantDiag   string
	}{
		{
			name:       "ok before warning threshold",
			last:       now.Add(-90 * time.Second),
			wantStatus: DeploymentHeartbeatGapOK,
			wantGap:    90,
			wantDiag:   "deployment job heartbeat is current",
		},
		{
			name:       "warning before stuck threshold",
			last:       now.Add(-91 * time.Second),
			wantStatus: DeploymentHeartbeatGapWarning,
			wantGap:    91,
			wantIssues: []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueWarning},
			wantDiag:   "deployment job heartbeat gap exceeded warning threshold",
		},
		{
			name:       "exact stuck threshold is warning",
			last:       now.Add(-180 * time.Second),
			wantStatus: DeploymentHeartbeatGapWarning,
			wantGap:    180,
			wantIssues: []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueWarning},
			wantDiag:   "deployment job heartbeat gap exceeded warning threshold",
		},
		{
			name:       "greater than stuck threshold is stuck",
			last:       now.Add(-181 * time.Second),
			wantStatus: DeploymentHeartbeatGapStuck,
			wantGap:    181,
			wantIssues: []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueStuck},
			wantDiag:   "deployment job heartbeat gap exceeded stuck threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReviewDeploymentHeartbeatGap(DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: tt.last})
			if got.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.GapSeconds != tt.wantGap {
				t.Fatalf("GapSeconds = %d, want %d", got.GapSeconds, tt.wantGap)
			}
			assertHeartbeatIssueCodes(t, got.IssueCodes, tt.wantIssues)
			assertHeartbeatDiagnostics(t, got.Diagnostics, []string{tt.wantDiag})
		})
	}
}

func TestReviewDeploymentHeartbeatGapHandlesMissingAndClockSkew(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	missing := ReviewDeploymentHeartbeatGap(DeploymentHeartbeatGapReviewInput{Now: now})
	if missing.Status != DeploymentHeartbeatGapWarning {
		t.Fatalf("missing Status = %q, want %q", missing.Status, DeploymentHeartbeatGapWarning)
	}
	assertHeartbeatIssueCodes(t, missing.IssueCodes, []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueMissing})
	assertHeartbeatDiagnostics(t, missing.Diagnostics, []string{"deployment job heartbeat has not been observed"})

	skew := ReviewDeploymentHeartbeatGap(DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(time.Second)})
	if skew.Status != DeploymentHeartbeatGapOK {
		t.Fatalf("clock skew Status = %q, want %q", skew.Status, DeploymentHeartbeatGapOK)
	}
	if skew.GapSeconds != 0 {
		t.Fatalf("clock skew GapSeconds = %d, want 0", skew.GapSeconds)
	}
	assertHeartbeatIssueCodes(t, skew.IssueCodes, []DeploymentHeartbeatGapIssueCode{DeploymentHeartbeatIssueClockSkew})
	assertHeartbeatDiagnostics(t, skew.Diagnostics, []string{"deployment job heartbeat timestamp is ahead of review time"})
}

func TestDeploymentHeartbeatGapReviewJSONIsStable(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	review := ReviewDeploymentHeartbeatGap(DeploymentHeartbeatGapReviewInput{Now: now, LastHeartbeatAt: now.Add(-181 * time.Second)})

	got, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	want := `{"status":"stuck","gapSeconds":181,"warningAfterSeconds":90,"stuckAfterSeconds":180,"issueCodes":["deployment_heartbeat_gap_stuck"],"diagnostics":["deployment job heartbeat gap exceeded stuck threshold"]}`
	if string(got) != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestReviewDeploymentHeartbeatGapNormalizesInvalidThresholds(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	review := ReviewDeploymentHeartbeatGap(DeploymentHeartbeatGapReviewInput{
		Now:             now,
		LastHeartbeatAt: now.Add(-6 * time.Second),
		WarningAfter:    10 * time.Second,
		StuckAfter:      10 * time.Second,
	})

	if review.WarningAfterSeconds != 5 || review.StuckAfterSeconds != 10 {
		t.Fatalf("thresholds = warning %d stuck %d, want 5 and 10", review.WarningAfterSeconds, review.StuckAfterSeconds)
	}
	if review.Status != DeploymentHeartbeatGapWarning {
		t.Fatalf("Status = %q, want %q", review.Status, DeploymentHeartbeatGapWarning)
	}
}

func assertHeartbeatIssueCodes(t *testing.T, got, want []DeploymentHeartbeatGapIssueCode) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("IssueCodes = %#v, want %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("IssueCodes = %#v, want %#v", got, want)
		}
	}
}

func assertHeartbeatDiagnostics(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Diagnostics = %#v, want %#v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("Diagnostics = %#v, want %#v", got, want)
		}
	}
}
