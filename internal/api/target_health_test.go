package api

import (
	"testing"
	"time"
)

func TestReviewTargetHealthClassifiesHealthy(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	got := ReviewTargetHealth("api", true, now.Add(-time.Minute), func() time.Time { return now })

	if got.TargetName != "api" || got.Status != TargetHealthHealthy || got.ReasonCode != TargetHealthReasonUp {
		t.Fatalf("review = %#v, want healthy target up", got)
	}
	if got.ObservedAt != now || got.LastScrapeAt != now.Add(-time.Minute) || got.StaleAfterSeconds != 300 {
		t.Fatalf("unexpected timing fields: %#v", got)
	}
}

func TestReviewTargetHealthClassifiesDown(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	got := ReviewTargetHealth("api", false, now.Add(-time.Minute), func() time.Time { return now })

	if got.Status != TargetHealthDown || got.ReasonCode != TargetHealthReasonDown {
		t.Fatalf("review = %#v, want down target", got)
	}
}

func TestReviewTargetHealthClassifiesMissingScrapeAsStale(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	got := ReviewTargetHealth("api", true, time.Time{}, func() time.Time { return now })

	if got.Status != TargetHealthStale || got.ReasonCode != TargetHealthReasonScrapeMissing {
		t.Fatalf("review = %#v, want stale missing scrape", got)
	}
}

func TestReviewTargetHealthClassifiesStaleBeforeDown(t *testing.T) {
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)

	got := ReviewTargetHealth("api", false, now.Add(-targetHealthStaleAfter-time.Second), func() time.Time { return now })

	if got.Status != TargetHealthStale || got.ReasonCode != TargetHealthReasonScrapeStale {
		t.Fatalf("review = %#v, want stale scrape", got)
	}
}
