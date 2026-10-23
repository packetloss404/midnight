package identity

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is a deterministic test/development identity store. Production
// wiring uses PostgresStore.
type MemoryStore struct {
	mu            sync.Mutex
	authority     *memoryAuthority
	users         map[string]UserRecord
	sessions      map[string]Session
	loginAttempts map[string]memoryLoginAttempt
}

type memoryLoginAttempt struct {
	WindowStartedAt time.Time
	Count           int
}

func NewMemoryStore(users ...UserRecord) *MemoryStore {
	return newMemoryStore(&memoryAuthority{}, users...)
}

func newMemoryStore(authority *memoryAuthority, users ...UserRecord) *MemoryStore {
	store := &MemoryStore{
		authority:     authority,
		users:         make(map[string]UserRecord),
		sessions:      make(map[string]Session),
		loginAttempts: make(map[string]memoryLoginAttempt),
	}
	for _, user := range users {
		store.users[user.ID] = cloneUser(user)
	}
	return store
}

func (s *MemoryStore) UserByEmail(ctx context.Context, email string) (UserRecord, error) {
	if err := ctx.Err(); err != nil {
		return UserRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.users {
		if strings.EqualFold(user.Email, strings.TrimSpace(email)) {
			return cloneUser(user), nil
		}
	}
	return UserRecord{}, ErrIdentityNotFound
}

func (s *MemoryStore) InsertSession(ctx context.Context, user UserRecord, session Session, lastLogin time.Time, maxActive int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.users[user.ID]
	if !ok || current.Disabled || current.Deleted || current.SessionGeneration != session.SessionGeneration {
		return ErrInvalidCredentials
	}
	for _, existing := range s.sessions {
		if bytes.Equal(existing.TokenHash, session.TokenHash) {
			return errors.New("session token hash already exists")
		}
	}
	if maxActive > 0 {
		active := make([]Session, 0)
		for _, existing := range s.sessions {
			if existing.UserID == user.ID && existing.RevokedAt == nil &&
				lastLogin.Before(existing.ExpiresAt) && lastLogin.Before(existing.IdleExpiresAt) {
				active = append(active, existing)
			}
		}
		sort.Slice(active, func(i, j int) bool {
			if active[i].CreatedAt.Equal(active[j].CreatedAt) {
				return active[i].ID > active[j].ID
			}
			return active[i].CreatedAt.After(active[j].CreatedAt)
		})
		for i := maxActive - 1; i < len(active); i++ {
			existing := s.sessions[active[i].ID]
			existing.RevokedAt = timePointer(lastLogin)
			existing.RevokedReason = "session-limit"
			s.sessions[existing.ID] = existing
		}
	}
	s.sessions[session.ID] = cloneSession(session)
	return nil
}

func (s *MemoryStore) ConsumeLoginAttempts(ctx context.Context, bucketHashes [][]byte, now time.Time, window time.Duration, limit int) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var retryAfter time.Duration
	for _, hash := range bucketHashes {
		key := string(hash)
		attempt := s.loginAttempts[key]
		if attempt.WindowStartedAt.IsZero() || !now.Before(attempt.WindowStartedAt.Add(window)) {
			attempt = memoryLoginAttempt{WindowStartedAt: now, Count: 1}
		} else {
			attempt.Count++
		}
		s.loginAttempts[key] = attempt
		if attempt.Count > limit {
			retry := attempt.WindowStartedAt.Add(window).Sub(now)
			if retry > retryAfter {
				retryAfter = retry
			}
		}
	}
	return retryAfter, nil
}

func (s *MemoryStore) ClearLoginAttempts(ctx context.Context, bucketHashes [][]byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, hash := range bucketHashes {
		delete(s.loginAttempts, string(hash))
	}
	return nil
}

func (s *MemoryStore) AuthenticateSession(ctx context.Context, tokenHash []byte, now, idleExpiresAt time.Time) (UserRecord, Session, error) {
	if err := ctx.Err(); err != nil {
		return UserRecord{}, Session{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, session := range s.sessions {
		if !bytes.Equal(session.TokenHash, tokenHash) {
			continue
		}
		user, ok := s.users[session.UserID]
		if !ok || user.Disabled || user.Deleted || user.MustRotatePassword || session.RevokedAt != nil ||
			!now.Before(session.ExpiresAt) || !now.Before(session.IdleExpiresAt) ||
			session.SessionGeneration != user.SessionGeneration {
			return UserRecord{}, Session{}, ErrSessionNotFound
		}
		session.LastSeenAt = now
		if idleExpiresAt.After(session.ExpiresAt) {
			session.IdleExpiresAt = session.ExpiresAt
		} else {
			session.IdleExpiresAt = idleExpiresAt
		}
		s.sessions[id] = session
		return cloneUser(user), cloneSession(session), nil
	}
	return UserRecord{}, Session{}, ErrSessionNotFound
}

func (s *MemoryStore) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0)
	for _, session := range s.sessions {
		if session.UserID == userID {
			out = append(out, cloneSession(session))
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

func (s *MemoryStore) RevokeSession(ctx context.Context, userID, sessionID, reason string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if session.UserID != userID {
		return ErrSessionForbidden
	}
	if session.RevokedAt == nil {
		session.RevokedAt = timePointer(now)
		session.RevokedReason = reason
		s.sessions[sessionID] = session
	}
	return nil
}

func (s *MemoryStore) RevokeAllSessions(ctx context.Context, userID, reason string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[userID]
	if !ok {
		return ErrIdentityNotFound
	}
	user.SessionGeneration++
	s.users[userID] = user
	for id, session := range s.sessions {
		if session.UserID == userID && session.RevokedAt == nil {
			session.RevokedAt = timePointer(now)
			session.RevokedReason = reason
			s.sessions[id] = session
		}
	}
	return nil
}

func (s *MemoryStore) DisableUser(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[userID]
	user.Disabled = true
	s.users[userID] = user
}

func (s *MemoryStore) RequirePasswordRotation(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user := s.users[userID]
	user.MustRotatePassword = true
	user.SessionGeneration++
	s.users[userID] = user
}

func cloneUser(user UserRecord) UserRecord {
	user.TOTPSecretEnvelope = append([]byte(nil), user.TOTPSecretEnvelope...)
	return user
}

func cloneSession(session Session) Session {
	session.TokenHash = append([]byte(nil), session.TokenHash...)
	if session.RevokedAt != nil {
		value := *session.RevokedAt
		session.RevokedAt = &value
	}
	return session
}

func timePointer(value time.Time) *time.Time {
	return &value
}
