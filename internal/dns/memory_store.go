// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package dns

import (
	"context"
	"sync"
)

// MemoryOwnershipStore is a concurrency-safe test and local-development
// implementation. Installed workers use PostgresOwnershipStore.
type MemoryOwnershipStore struct {
	mu   sync.RWMutex
	rows map[string]Ownership
}

func NewMemoryOwnershipStore() *MemoryOwnershipStore {
	return &MemoryOwnershipStore{rows: make(map[string]Ownership)}
}

func (s *MemoryOwnershipStore) ListOwnerships(_ context.Context, provider, zoneID, ownerKind, ownerID string) ([]Ownership, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Ownership{}
	for _, row := range s.rows {
		if row.Provider == provider && row.ZoneID == zoneID && row.OwnerKind == ownerKind && row.OwnerID == ownerID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *MemoryOwnershipStore) PutOwnership(_ context.Context, ownership Ownership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = make(map[string]Ownership)
	}
	s.rows[ownershipMapKey(ownership.Provider, ownership.ZoneID, ownership.OwnerKind, ownership.OwnerID, ownership.Type, ownership.Name)] = ownership
	return nil
}

func (s *MemoryOwnershipStore) DeleteOwnership(_ context.Context, provider, zoneID, ownerKind, ownerID string, recordType RecordType, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, ownershipMapKey(provider, zoneID, ownerKind, ownerID, recordType, name))
	return nil
}

func ownershipMapKey(provider, zoneID, ownerKind, ownerID string, recordType RecordType, name string) string {
	return provider + "\x00" + zoneID + "\x00" + ownerKind + "\x00" + ownerID + "\x00" + recordKey(recordType, name)
}
