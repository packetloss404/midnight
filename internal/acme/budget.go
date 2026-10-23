package acme

import (
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	IssueTimeout      = 10 * time.Minute
	DNSPollInterval   = 30 * time.Second
	DNSPropagateLimit = 5 * time.Minute
	RenewBefore       = 30 * 24 * time.Hour

	RestoreCertificatesPerHour          = 20
	RegisteredDomainCertificatesPerWeek = 50
)

type BudgetStore struct {
	mu     sync.Mutex
	counts map[string]map[time.Time]int
}

func NewBudgetStore() *BudgetStore {
	return &BudgetStore{counts: make(map[string]map[time.Time]int)}
}

func (store *BudgetStore) Count(registeredDomain string, at time.Time) int {
	if store == nil {
		return 0
	}
	domain := normalizeRegisteredDomain(registeredDomain)
	if domain == "" {
		return 0
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	return store.counts[domain][weekStart(at)]
}

func (store *BudgetStore) Reserve(registeredDomain string, at time.Time) (int, error) {
	if store == nil {
		return 0, errors.New("ACME budget store is nil")
	}
	domain := normalizeRegisteredDomain(registeredDomain)
	if domain == "" {
		return 0, errors.New("registered domain is required")
	}

	week := weekStart(at)
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.counts[domain] == nil {
		store.counts[domain] = make(map[time.Time]int)
	}
	if store.counts[domain][week] >= RegisteredDomainCertificatesPerWeek {
		return store.counts[domain][week], errors.New("registered domain weekly ACME budget exceeded")
	}
	store.counts[domain][week]++
	return store.counts[domain][week], nil
}

func normalizeRegisteredDomain(domain string) string {
	return strings.ToLower(strings.TrimSpace(domain))
}

func weekStart(at time.Time) time.Time {
	at = at.UTC()
	year, month, day := at.Date()
	dayStart := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(dayStart.Weekday()) + 6) % 7
	return dayStart.AddDate(0, 0, -daysSinceMonday)
}
