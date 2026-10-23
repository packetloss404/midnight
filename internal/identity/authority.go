// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

// memoryAuthority is an opaque identity-domain marker. Separate memory stores
// are intentionally separate authorities unless they are created together.
type memoryAuthority struct {
	marker byte
}

// NewMemoryAuthority creates session and access stores whose user IDs belong
// to the same explicit in-memory authority. It is intended for development and
// deterministic tests; production uses one PostgreSQL authority.
func NewMemoryAuthority(users ...UserRecord) (*MemoryStore, *MemoryAccessStore) {
	authority := &memoryAuthority{}
	return newMemoryStore(authority, users...), newMemoryAccessStore(authority)
}

// SharesAccessAuthority reports whether authenticated user IDs and access
// grants come from one verifiable identity authority. Unknown/custom store
// combinations fail closed.
func (m *Manager) SharesAccessAuthority(access AccessStore) bool {
	if m == nil || m.store == nil || access == nil {
		return false
	}
	switch sessions := m.store.(type) {
	case *PostgresStore:
		grants, ok := access.(*PostgresStore)
		return ok && sessions.Pool != nil && sessions.Pool == grants.Pool
	case *MemoryStore:
		grants, ok := access.(*MemoryAccessStore)
		return ok && sessions.authority != nil && sessions.authority == grants.authority
	default:
		return false
	}
}
