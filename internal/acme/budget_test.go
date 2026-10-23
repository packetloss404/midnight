package acme

import (
	"testing"
	"time"
)

func TestBudgetStoreTracksRegisteredDomainWeeklyCount(t *testing.T) {
	store := NewBudgetStore()
	at := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	count, err := store.Reserve(" Example.Test ", at)
	if err != nil {
		t.Fatalf("Reserve returned error: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if got := store.Count("example.test", at.Add(24*time.Hour)); got != 1 {
		t.Fatalf("weekly count = %d, want 1", got)
	}
	if got := store.Count("example.test", at.AddDate(0, 0, 8)); got != 0 {
		t.Fatalf("next weekly count = %d, want 0", got)
	}
}

func TestBudgetStoreRejectsReservationsOverWeeklyLimit(t *testing.T) {
	store := NewBudgetStore()
	at := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < RegisteredDomainCertificatesPerWeek; i++ {
		if _, err := store.Reserve("example.test", at); err != nil {
			t.Fatalf("Reserve %d returned error: %v", i, err)
		}
	}
	if count, err := store.Reserve("example.test", at); err == nil || count != RegisteredDomainCertificatesPerWeek {
		t.Fatalf("Reserve over limit count=%d err=%v, want limit error", count, err)
	}
}
