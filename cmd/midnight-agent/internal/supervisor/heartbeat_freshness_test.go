package supervisor

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestReviewHeartbeatFreshnessClassifiesFreshStaleAndLost(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want HeartbeatFreshnessReview
	}{
		{
			name: "fresh within configured window",
			age:  5 * time.Second,
			want: HeartbeatFreshnessReview{
				Status:           HeartbeatFreshnessStatusFresh,
				LastHeartbeatAge: 5 * time.Second,
				FreshWithin:      10 * time.Second,
				LostAfter:        HeartbeatLostAfter,
			},
		},
		{
			name: "stale after configured window",
			age:  12 * time.Second,
			want: HeartbeatFreshnessReview{
				Status:           HeartbeatFreshnessStatusStale,
				LastHeartbeatAge: 12 * time.Second,
				FreshWithin:      10 * time.Second,
				LostAfter:        HeartbeatLostAfter,
				Issues:           []HeartbeatFreshnessReviewIssue{{Code: HeartbeatFreshnessIssueStale}},
			},
		},
		{
			name: "lost only after phase zero alert threshold",
			age:  HeartbeatLostAfter + time.Nanosecond,
			want: HeartbeatFreshnessReview{
				Status:           HeartbeatFreshnessStatusLost,
				LastHeartbeatAge: HeartbeatLostAfter + time.Nanosecond,
				FreshWithin:      10 * time.Second,
				LostAfter:        HeartbeatLostAfter,
				Issues:           []HeartbeatFreshnessReviewIssue{{Code: HeartbeatFreshnessIssueLost}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReviewHeartbeatFreshness(tt.age, 10*time.Second)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("review = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReviewHeartbeatFreshnessTreatsThresholdAsStaleNotLost(t *testing.T) {
	review := ReviewHeartbeatFreshness(HeartbeatLostAfter, 10*time.Second)

	if review.Status != HeartbeatFreshnessStatusStale {
		t.Fatalf("status = %q, want %q", review.Status, HeartbeatFreshnessStatusStale)
	}
	if !reflect.DeepEqual(review.Issues, []HeartbeatFreshnessReviewIssue{{Code: HeartbeatFreshnessIssueStale}}) {
		t.Fatalf("issues = %+v", review.Issues)
	}
}

func TestReviewHeartbeatFreshnessNormalizesInvalidFreshWindow(t *testing.T) {
	review := ReviewHeartbeatFreshness(12*time.Second, 0)

	if review.Status != HeartbeatFreshnessStatusFresh || review.FreshWithin != HeartbeatLostAfter {
		t.Fatalf("review = %+v", review)
	}
}

func TestReviewHeartbeatFreshnessReportsInvalidNegativeAge(t *testing.T) {
	review := ReviewHeartbeatFreshness(-time.Nanosecond, 10*time.Second)

	want := HeartbeatFreshnessReview{
		Status:           HeartbeatFreshnessStatusStale,
		LastHeartbeatAge: -time.Nanosecond,
		FreshWithin:      10 * time.Second,
		LostAfter:        HeartbeatLostAfter,
		Issues:           []HeartbeatFreshnessReviewIssue{{Code: HeartbeatFreshnessIssueInvalidAge}},
	}
	if !reflect.DeepEqual(review, want) {
		t.Fatalf("review = %+v, want %+v", review, want)
	}
}

func TestMarshalHeartbeatFreshnessReviewUsesStableJSONFields(t *testing.T) {
	review := ReviewHeartbeatFreshness(16*time.Second, 10*time.Second)

	encoded, err := MarshalHeartbeatFreshnessReview(review)
	if err != nil {
		t.Fatalf("marshal review: %v", err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("review json is invalid: %s", encoded)
	}
	want := `{"status":"lost","last_heartbeat_age":16000000000,"fresh_within":10000000000,"lost_after":15000000000,"issues":[{"code":"lost_heartbeat"}]}`
	if string(encoded) != want {
		t.Fatalf("json = %s, want %s", encoded, want)
	}
}
