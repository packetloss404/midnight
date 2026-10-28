// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
)

// Existing accounts must authenticate through the normal login flow. Invitation
// possession can never reset a password or replace an existing identity.
var ErrInvitationAccountExists = errors.New("invited account already exists; sign in")

// UsesOnboardingStore lets the transaction coordinator reject split identity
// and limiter authorities without exposing the service's MAC key.
func (s *InvitationService) UsesOnboardingStore(store *PostgresStore) bool {
	return s != nil && store != nil && s.store == store
}

// PrepareOnboarding validates the bearer without requiring an existing user.
// Only its domain-separated verifier crosses the database boundary.
func (s *InvitationService) PrepareOnboarding(token string) ([]byte, error) {
	if s == nil || !validRawInvitationToken(strings.TrimSpace(token)) {
		return nil, ErrInvalidInvitation
	}
	return s.tokenMAC(strings.TrimSpace(token)), nil
}

// ConsumeOnboardingAttempt shares the durable limiter implementation, with a
// distinct MAC domain so onboarding cannot spend an account's login budget.
// Network identity comes from the socket peer, never a forwarded HTTP header.
func (s *InvitationService) ConsumeOnboardingAttempt(ctx context.Context, remoteAddr, token string) (time.Duration, error) {
	limiter, ok := s.store.(interface {
		ConsumeLoginAttempts(context.Context, [][]byte, time.Time, time.Duration, int) (time.Duration, error)
	})
	if !ok {
		return 0, errors.New("durable invitation onboarding limiter is unavailable")
	}
	bucket := func(kind, value string) []byte {
		mac := hmac.New(sha256.New, s.macKey)
		_, _ = mac.Write([]byte("midnight/invitation-onboarding-limit/v1\x00" + kind + "\x00" + value))
		return mac.Sum(nil)
	}
	network := bucket("network", normalizeRemoteHost(remoteAddr))
	defer clear(network)
	retry, err := limiter.ConsumeLoginAttempts(ctx, [][]byte{network}, s.now().UTC(), DefaultLoginWindow, 30)
	if err != nil || retry > 0 {
		return retry, err
	}
	verifier := bucket("invitation", token)
	defer clear(verifier)
	return limiter.ConsumeLoginAttempts(ctx, [][]byte{verifier}, s.now().UTC(), DefaultLoginWindow, 10)
}
