// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

import (
	"context"
	"strings"
)

// RecoverableAdministrator mirrors the PostgreSQL predicate so the in-memory
// stack answers an operator recovery reset exactly the way production does.
func (s *MemoryStore) RecoverableAdministrator(ctx context.Context, email string) (RecoverableAdministrator, error) {
	if err := ctx.Err(); err != nil {
		return RecoverableAdministrator{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.recoverableAdministratorLocked(email)
	if !ok {
		return RecoverableAdministrator{}, ErrAdministratorNotRecoverable
	}
	return RecoverableAdministrator{
		UserID:       user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		TOTPEnrolled: !user.TOTPEnrolledAt.IsZero(),
	}, nil
}

func (s *MemoryStore) ResetAdministratorCredentials(
	ctx context.Context,
	request ResetAdministratorCredentialsRequest,
) (ResetAdministratorCredentialsResult, error) {
	if err := ctx.Err(); err != nil {
		return ResetAdministratorCredentialsResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.recoverableAdministratorLocked(request.Email)
	if !ok || user.ID != request.UserID {
		return ResetAdministratorCredentialsResult{}, ErrAdministratorNotRecoverable
	}
	if request.PreserveTOTP && user.TOTPEnrolledAt.IsZero() {
		return ResetAdministratorCredentialsResult{}, ErrTOTPEnrollmentMissing
	}
	now := request.Now.UTC()
	user.PasswordHash = request.PasswordHash
	user.MustRotatePassword = false
	if !request.PreserveTOTP {
		enrolledAt := request.TOTPEnrolledAt
		if enrolledAt.IsZero() {
			enrolledAt = now
		}
		user.TOTPSecretEnvelope = append([]byte(nil), request.TOTPSecretEnvelope...)
		user.TOTPEnrolledAt = enrolledAt
	}
	user.SessionGeneration++
	s.users[user.ID] = user

	revoked := 0
	reason := administratorRecoveryRevokeReason(request.RevokedReason)
	for id, session := range s.sessions {
		if session.UserID != user.ID || session.RevokedAt != nil {
			continue
		}
		session.RevokedAt = timePointer(now)
		session.RevokedReason = reason
		s.sessions[id] = session
		revoked++
	}
	return ResetAdministratorCredentialsResult{
		User: User{
			ID:            user.ID,
			Email:         user.Email,
			DisplayName:   user.DisplayName,
			InstanceAdmin: true,
			MFAEnabled:    true,
		},
		TOTPReenrolled:  !request.PreserveTOTP,
		SessionsRevoked: revoked,
	}, nil
}

func (s *MemoryStore) recoverableAdministratorLocked(email string) (UserRecord, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return UserRecord{}, false
	}
	for _, user := range s.users {
		if !strings.EqualFold(user.Email, email) {
			continue
		}
		if user.Deleted || user.Disabled || !user.InstanceAdmin {
			return UserRecord{}, false
		}
		return cloneUser(user), true
	}
	return UserRecord{}, false
}
