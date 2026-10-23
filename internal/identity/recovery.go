// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package identity

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrAdministratorNotRecoverable is returned when no enabled, undeleted
	// installation administrator matches the requested address. Disabled and
	// soft-deleted accounts deliberately fall in here: operator recovery
	// restores access to an account the operator still wants, it never
	// resurrects one that was taken away.
	ErrAdministratorNotRecoverable = errors.New("no enabled administrator account matches the address")
	// ErrTOTPEnrollmentMissing is returned when a reset asked to preserve the
	// existing second factor and the account has none. Honouring it would
	// downgrade an administrator to a single factor, so it fails closed.
	ErrTOTPEnrollmentMissing = errors.New("account has no TOTP enrollment to preserve")
)

// RecoverableAdministrator is the non-secret description of a reset target.
// It exists so the caller can seal a replacement TOTP envelope against the
// server-resolved user ID before any write is attempted; nothing here is
// derived from the request body except by lookup.
type RecoverableAdministrator struct {
	UserID       string
	Email        string
	DisplayName  string
	TOTPEnrolled bool
}

// ResetAdministratorCredentialsRequest replaces one administrator's local
// sign-in credentials. UserID is the server-resolved target and Email is
// re-checked at write time, so an account that changed between resolution and
// commit fails closed rather than having a stranger's password installed.
//
// Password hashing, TOTP verification, and envelope encryption are completed
// before this value is constructed, exactly as ClaimDefaultWorkspaceRequest
// requires, so the durable path can commit inside one transaction.
type ResetAdministratorCredentialsRequest struct {
	UserID       string
	Email        string
	PasswordHash string
	// PreserveTOTP keeps the account's existing enrollment. When it is false
	// the caller must supply a verified replacement envelope.
	PreserveTOTP       bool
	TOTPSecretEnvelope []byte
	TOTPEnrolledAt     time.Time
	// RevokedReason is recorded on every session this reset revokes.
	RevokedReason string
	Now           time.Time
}

// ResetAdministratorCredentialsResult reports the recovered account and the
// blast radius of the reset. No credential material appears here: the
// replacement TOTP secret exists only in the caller that generated it.
type ResetAdministratorCredentialsResult struct {
	User            User `json:"user"`
	TOTPReenrolled  bool `json:"totp_reenrolled"`
	SessionsRevoked int  `json:"sessions_revoked"`
}

// AdministratorRecoveryStore is the durable credential-recovery boundary.
//
// Like ClaimStatusReader it is a narrow optional interface rather than a
// method set on Store: only the operator recovery route needs it, and every
// session fake in the test suite would otherwise have to grow a credential
// mutation it must never be able to perform.
type AdministratorRecoveryStore interface {
	// RecoverableAdministrator resolves the exact account a reset may target.
	// It returns ErrAdministratorNotRecoverable for a missing, non-admin,
	// disabled, or soft-deleted account without distinguishing between them.
	RecoverableAdministrator(ctx context.Context, email string) (RecoverableAdministrator, error)
	// ResetAdministratorCredentials installs the new password, applies the
	// requested TOTP disposition, invalidates every live session, and reports
	// what it changed.
	ResetAdministratorCredentials(
		ctx context.Context,
		request ResetAdministratorCredentialsRequest,
	) (ResetAdministratorCredentialsResult, error)
}

// SharesRecoveryAuthority reports whether a recovery store mutates the same
// credentials this manager authenticates. Unknown combinations fail closed so
// a misconfigured stack cannot reset a password in one authority while
// sessions are verified against another.
func (m *Manager) SharesRecoveryAuthority(recovery AdministratorRecoveryStore) bool {
	if m == nil || m.store == nil || recovery == nil {
		return false
	}
	switch sessions := m.store.(type) {
	case *PostgresStore:
		credentials, ok := recovery.(*PostgresStore)
		return ok && sessions.Pool != nil && sessions.Pool == credentials.Pool
	case *MemoryStore:
		credentials, ok := recovery.(*MemoryStore)
		return ok && sessions == credentials
	default:
		return false
	}
}
