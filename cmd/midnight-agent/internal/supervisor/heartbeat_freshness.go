package supervisor

import (
	"encoding/json"
	"time"
)

const (
	HeartbeatFreshnessStatusFresh = "fresh"
	HeartbeatFreshnessStatusStale = "stale"
	HeartbeatFreshnessStatusLost  = "lost"

	HeartbeatFreshnessIssueInvalidAge = "invalid_heartbeat_age"
	HeartbeatFreshnessIssueStale      = "stale_heartbeat"
	HeartbeatFreshnessIssueLost       = "lost_heartbeat"

	// Phase 0 alert semantics classify a heartbeat as lost only after 15 seconds.
	HeartbeatLostAfter = 15 * time.Second
)

// HeartbeatFreshnessReview is a JSON-friendly status derived from the last heartbeat age.
// Phase 0 treats stale heartbeats as reviewable and lost heartbeats as blocking.
type HeartbeatFreshnessReview struct {
	Status           string                          `json:"status"`
	LastHeartbeatAge time.Duration                   `json:"last_heartbeat_age"`
	FreshWithin      time.Duration                   `json:"fresh_within"`
	LostAfter        time.Duration                   `json:"lost_after"`
	Issues           []HeartbeatFreshnessReviewIssue `json:"issues"`
}

// HeartbeatFreshnessReviewIssue identifies one deterministic freshness issue.
type HeartbeatFreshnessReviewIssue struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// ReviewHeartbeatFreshness classifies the last heartbeat age without side effects.
// freshWithin controls the non-alert freshness window; invalid values collapse
// to the lost threshold. A heartbeat is lost only when age is greater than
// HeartbeatLostAfter, preserving the exact threshold as stale.
func ReviewHeartbeatFreshness(lastHeartbeatAge, freshWithin time.Duration) HeartbeatFreshnessReview {
	normalizedFreshWithin := normalizeHeartbeatFreshWithin(freshWithin)
	review := HeartbeatFreshnessReview{
		Status:           HeartbeatFreshnessStatusFresh,
		LastHeartbeatAge: lastHeartbeatAge,
		FreshWithin:      normalizedFreshWithin,
		LostAfter:        HeartbeatLostAfter,
	}

	if lastHeartbeatAge < 0 {
		review.Status = HeartbeatFreshnessStatusStale
		review.Issues = append(review.Issues, HeartbeatFreshnessReviewIssue{Code: HeartbeatFreshnessIssueInvalidAge})
		return review
	}
	if lastHeartbeatAge > HeartbeatLostAfter {
		review.Status = HeartbeatFreshnessStatusLost
		review.Issues = append(review.Issues, HeartbeatFreshnessReviewIssue{Code: HeartbeatFreshnessIssueLost})
		return review
	}
	if lastHeartbeatAge > normalizedFreshWithin {
		review.Status = HeartbeatFreshnessStatusStale
		review.Issues = append(review.Issues, HeartbeatFreshnessReviewIssue{Code: HeartbeatFreshnessIssueStale})
	}

	return review
}

// MarshalHeartbeatFreshnessReview marshals the review with stable field names for transport or logs.
func MarshalHeartbeatFreshnessReview(review HeartbeatFreshnessReview) ([]byte, error) {
	return json.Marshal(review)
}

func normalizeHeartbeatFreshWithin(freshWithin time.Duration) time.Duration {
	if freshWithin <= 0 || freshWithin > HeartbeatLostAfter {
		return HeartbeatLostAfter
	}
	return freshWithin
}
