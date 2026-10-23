package acme

import (
	"testing"
	"time"
)

func TestPlanRestoreThrottleLimitsCertificatesPerHour(t *testing.T) {
	start := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	slots, err := PlanRestoreThrottle(21, start)
	if err != nil {
		t.Fatalf("PlanRestoreThrottle returned error: %v", err)
	}
	if len(slots) != 21 {
		t.Fatalf("slot count = %d, want 21", len(slots))
	}
	if slots[0].NotBefore != start || slots[19].NotBefore != start {
		t.Fatalf("first 20 certs should be restorable at start: %#v", slots[:20])
	}
	if want := start.Add(time.Hour); slots[20].NotBefore != want {
		t.Fatalf("slot 20 NotBefore = %s, want %s", slots[20].NotBefore, want)
	}
}

func TestPlanRestoreThrottleRejectsNegativeCount(t *testing.T) {
	if _, err := PlanRestoreThrottle(-1, time.Time{}); err == nil {
		t.Fatal("expected negative certificate count error")
	}
}
