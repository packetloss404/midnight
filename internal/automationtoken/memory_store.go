// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package automationtoken

import (
	"context"
	"sort"
	"sync"
	"time"
)

type memoryProjectScope struct {
	workspaceID string
	projectID   string
}

type memoryServiceAccountScope struct {
	workspaceID      string
	serviceAccountID string
}

// MemoryStore is a fail-closed model of the durable composite-scope store.
// Callers must register every allowed Scope before minting.
type MemoryStore struct {
	mu              sync.Mutex
	records         map[string]Credential
	byVerifier      map[string]string
	projects        map[memoryProjectScope]bool
	serviceAccounts map[memoryServiceAccountScope]bool
}

func NewMemoryStore(scopes ...Scope) *MemoryStore {
	store := &MemoryStore{
		records:         make(map[string]Credential),
		byVerifier:      make(map[string]string),
		projects:        make(map[memoryProjectScope]bool),
		serviceAccounts: make(map[memoryServiceAccountScope]bool),
	}
	for _, scope := range scopes {
		store.RegisterScope(scope)
	}
	return store
}

func (s *MemoryStore) RegisterScope(scope Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[memoryProjectScope{
		workspaceID: scope.WorkspaceID,
		projectID:   scope.ProjectID,
	}] = true
	s.serviceAccounts[memoryServiceAccountScope{
		workspaceID:      scope.WorkspaceID,
		serviceAccountID: scope.ServiceAccountID,
	}] = true
}

// SetProjectActive models project soft deletion for fail-closed tests.
func (s *MemoryStore) SetProjectActive(workspaceID, projectID string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects[memoryProjectScope{workspaceID: workspaceID, projectID: projectID}] = active
}

// SetServiceAccountActive models service-account soft deletion.
func (s *MemoryStore) SetServiceAccountActive(workspaceID, serviceAccountID string, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serviceAccounts[memoryServiceAccountScope{
		workspaceID: workspaceID, serviceAccountID: serviceAccountID,
	}] = active
}

func (s *MemoryStore) Create(ctx context.Context, credential Credential) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.scopeActive(credential.Token) {
		return Token{}, ErrScopeNotFound
	}
	if len(credential.Verifier) != 32 || credential.ID == "" {
		return Token{}, ErrInvalidRequest
	}
	if _, ok := s.records[credential.ID]; ok {
		return Token{}, ErrInvalidRequest
	}
	verifierKey := string(credential.Verifier)
	if _, ok := s.byVerifier[verifierKey]; ok {
		return Token{}, ErrInvalidRequest
	}
	stored := cloneCredential(credential)
	s.records[stored.ID] = stored
	s.byVerifier[verifierKey] = stored.ID
	return cloneToken(stored.Token), nil
}

func (s *MemoryStore) Authenticate(ctx context.Context, verifier []byte, now time.Time) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byVerifier[string(verifier)]
	if !ok {
		return Token{}, ErrTokenNotFound
	}
	record := s.records[id]
	if record.Profile != ProfileCIDeployV1 ||
		record.RevokedAt != nil ||
		!now.Before(record.ExpiresAt) ||
		!s.scopeActive(record.Token) {
		return Token{}, ErrTokenNotFound
	}
	usedAt := now.UTC()
	record.LastUsedAt = &usedAt
	if usedAt.After(record.UpdatedAt) {
		record.UpdatedAt = usedAt
	}
	s.records[id] = record
	return cloneToken(record.Token), nil
}

func (s *MemoryStore) List(ctx context.Context, workspaceID, projectID string) ([]Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, 0)
	for _, record := range s.records {
		if record.WorkspaceID == workspaceID && record.ProjectID == projectID {
			out = append(out, cloneToken(record.Token))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) Get(ctx context.Context, workspaceID, projectID, tokenID string) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[tokenID]
	if !ok || record.WorkspaceID != workspaceID || record.ProjectID != projectID {
		return Token{}, ErrTokenNotFound
	}
	return cloneToken(record.Token), nil
}

func (s *MemoryStore) Revoke(
	ctx context.Context,
	request RevokeRequest,
	now time.Time,
) (Token, error) {
	if err := ctx.Err(); err != nil {
		return Token{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[request.TokenID]
	if !ok || record.WorkspaceID != request.WorkspaceID ||
		record.ProjectID != request.ProjectID {
		return Token{}, ErrTokenNotFound
	}
	if request.ExpectedVersion != nil && record.ResourceVersion != *request.ExpectedVersion {
		return Token{}, ErrVersionConflict
	}
	if record.RevokedAt == nil {
		revokedAt := now.UTC()
		record.RevokedAt = &revokedAt
		record.RevokedByUserID = request.RevokedByUserID
		record.RevokedReason = request.Reason
		record.UpdatedAt = revokedAt
		record.ResourceVersion++
		s.records[record.ID] = record
	}
	return cloneToken(record.Token), nil
}

func (s *MemoryStore) scopeActive(token Token) bool {
	return s.projects[memoryProjectScope{
		workspaceID: token.WorkspaceID,
		projectID:   token.ProjectID,
	}] && s.serviceAccounts[memoryServiceAccountScope{
		workspaceID:      token.WorkspaceID,
		serviceAccountID: token.ServiceAccountID,
	}]
}

func cloneCredential(value Credential) Credential {
	value.Token = cloneToken(value.Token)
	value.Verifier = append([]byte(nil), value.Verifier...)
	return value
}

func cloneToken(value Token) Token {
	if value.LastUsedAt != nil {
		cloned := *value.LastUsedAt
		value.LastUsedAt = &cloned
	}
	if value.RevokedAt != nil {
		cloned := *value.RevokedAt
		value.RevokedAt = &cloned
	}
	return value
}
