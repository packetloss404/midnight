package webhook

import (
	"context"
	"sync"
	"time"
)

// DedupeRecord is the in-memory shape of a single delivery that has
// already been observed.
type DedupeRecord struct {
	DeliveryID string
	EventHash  string
	Provider   string
	ReceivedAt time.Time
	Endpoint   string
}

// DedupeStore is the panel-locked dedupe boundary: the unique
// (delivery_id, event_hash) tuple uniquely identifies a delivery; a
// second insert returns the original record.
//
// Implementations MUST be safe for concurrent use.
type DedupeStore interface {
	// Reserve attempts to record the supplied DedupeRecord. If a record
	// with the same (provider, delivery_id, event_hash) already exists,
	// Reserve returns it with replayed=true and does NOT insert.
	Reserve(ctx context.Context, rec DedupeRecord) (stored DedupeRecord, replayed bool, err error)
}

// MemoryDedupeStore is the in-memory DedupeStore Lane 4 ships. Lane 1
// will replace this with a Postgres-backed unique index per the panel-
// locked schema (webhook_deliveries(provider, delivery_id, event_hash)
// UNIQUE).
type MemoryDedupeStore struct {
	mu   sync.Mutex
	data map[string]DedupeRecord
}

// NewMemoryDedupeStore constructs a fresh MemoryDedupeStore.
func NewMemoryDedupeStore() *MemoryDedupeStore {
	return &MemoryDedupeStore{data: map[string]DedupeRecord{}}
}

// Reserve implements DedupeStore.
func (s *MemoryDedupeStore) Reserve(_ context.Context, rec DedupeRecord) (DedupeRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := rec.Provider + "\x00" + rec.DeliveryID + "\x00" + rec.EventHash
	if existing, ok := s.data[key]; ok {
		return existing, true, nil
	}
	s.data[key] = rec
	return rec, false, nil
}

// Snapshot returns a deterministic copy of every recorded delivery.
// Tests use this to assert audit-emit shape.
func (s *MemoryDedupeStore) Snapshot() []DedupeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DedupeRecord, 0, len(s.data))
	for _, v := range s.data {
		out = append(out, v)
	}
	return out
}
