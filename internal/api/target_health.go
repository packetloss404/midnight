package api

import "time"

const targetHealthStaleAfter = 5 * time.Minute

type TargetHealthStatus string

const (
	TargetHealthHealthy TargetHealthStatus = "healthy"
	TargetHealthDown    TargetHealthStatus = "down"
	TargetHealthStale   TargetHealthStatus = "stale"
)

const (
	TargetHealthReasonUp            = "TARGET_UP"
	TargetHealthReasonDown          = "TARGET_DOWN"
	TargetHealthReasonScrapeMissing = "SCRAPE_MISSING"
	TargetHealthReasonScrapeStale   = "SCRAPE_STALE"
)

type TargetHealthReview struct {
	TargetName        string             `json:"target_name"`
	Status            TargetHealthStatus `json:"status"`
	ReasonCode        string             `json:"reason_code"`
	LastScrapeAt      time.Time          `json:"last_scrape_at"`
	ObservedAt        time.Time          `json:"observed_at"`
	StaleAfterSeconds int64              `json:"stale_after_seconds"`
}

func ReviewTargetHealth(targetName string, up bool, lastScrapeAt time.Time, nowFunc func() time.Time) TargetHealthReview {
	now := nowFunc().UTC()
	review := TargetHealthReview{
		TargetName:        targetName,
		LastScrapeAt:      lastScrapeAt.UTC(),
		ObservedAt:        now,
		StaleAfterSeconds: int64(targetHealthStaleAfter.Seconds()),
	}

	if lastScrapeAt.IsZero() {
		review.Status = TargetHealthStale
		review.ReasonCode = TargetHealthReasonScrapeMissing
		return review
	}
	if now.Sub(lastScrapeAt) > targetHealthStaleAfter {
		review.Status = TargetHealthStale
		review.ReasonCode = TargetHealthReasonScrapeStale
		return review
	}
	if !up {
		review.Status = TargetHealthDown
		review.ReasonCode = TargetHealthReasonDown
		return review
	}

	review.Status = TargetHealthHealthy
	review.ReasonCode = TargetHealthReasonUp
	return review
}
