// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package oauth implements a minimal PKCE OAuth 2.0 shim used by the
// midnight CLI's `midnight login` flow. It is intentionally thin: the
// "access token" returned by the token endpoint is just the operator's
// existing bearer token (see ADR on OAuth shim). Real multi-user
// accounts are out of scope until a user store lands.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultClientID is the only client id the v1 shim accepts.
const DefaultClientID = "midnight-cli"

// AuthorizationCodeTTL is how long a returned authorization code remains
// redeemable at the token endpoint.
const AuthorizationCodeTTL = 5 * time.Minute

// ErrCodeChallengeRequired is returned when a PKCE challenge is missing.
var ErrCodeChallengeRequired = errors.New("oauth: code_challenge is required")

// ErrCodeChallengeMethodUnsupported is returned for non-S256 PKCE methods.
var ErrCodeChallengeMethodUnsupported = errors.New("oauth: only S256 code_challenge_method is supported")

// ErrUnknownClient rejects authorize/token requests for clients other than
// DefaultClientID.
var ErrUnknownClient = errors.New("oauth: unknown client_id")

// ErrUnknownCode is returned when the token endpoint receives an
// authorization_code that was never issued (or has been redeemed/expired).
var ErrUnknownCode = errors.New("oauth: unknown or expired authorization code")

// ErrCodeVerifierMismatch is returned when the token endpoint's
// code_verifier does not match the stored code_challenge.
var ErrCodeVerifierMismatch = errors.New("oauth: code_verifier does not match code_challenge")

// ErrRedirectURIMismatch is returned when the token endpoint's redirect_uri
// does not match the redirect_uri the code was issued for.
var ErrRedirectURIMismatch = errors.New("oauth: redirect_uri does not match the authorize request")

// AuthorizeRequest carries the validated fields from the authorize endpoint.
type AuthorizeRequest struct {
	ClientID            string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	RedirectURI         string
	Scope               string
}

// IssuedCode is an authorization code minted by the store. It is single-use
// and short-lived.
type IssuedCode struct {
	Code        string
	State       string
	RedirectURI string
	ExpiresAt   time.Time
}

// Store keeps authorization codes alive between the authorize call and the
// token call. The default implementation is in-memory. Production Postgres
// storage is tracked separately (see ADR).
type Store struct {
	mu    sync.Mutex
	codes map[string]storedCode
	now   func() time.Time
}

type storedCode struct {
	challenge   string
	method      string
	state       string
	redirectURI string
	clientID    string
	expiresAt   time.Time
}

// NewStore constructs an empty in-memory store. Pass a clock func for
// deterministic tests; nil falls back to time.Now.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{codes: map[string]storedCode{}, now: now}
}

// ValidateAuthorize checks the authorize request without minting a code yet.
// Use this to drive a consent page; once the operator clicks consent, call
// IssueCode.
func ValidateAuthorize(req AuthorizeRequest) error {
	if req.ClientID != DefaultClientID {
		return ErrUnknownClient
	}
	if strings.TrimSpace(req.CodeChallenge) == "" {
		return ErrCodeChallengeRequired
	}
	method := strings.TrimSpace(req.CodeChallengeMethod)
	if method == "" {
		method = "S256"
	}
	if method != "S256" {
		return ErrCodeChallengeMethodUnsupported
	}
	return nil
}

// IssueCode mints a new authorization code for the supplied request. The
// returned IssuedCode embeds the cryptographically random code value plus
// the original state for CSRF round-tripping.
func (s *Store) IssueCode(req AuthorizeRequest) (IssuedCode, error) {
	if err := ValidateAuthorize(req); err != nil {
		return IssuedCode{}, err
	}
	code, err := newRandomString(32)
	if err != nil {
		return IssuedCode{}, fmt.Errorf("oauth: mint authorization code: %w", err)
	}
	expiresAt := s.now().Add(AuthorizationCodeTTL)
	s.mu.Lock()
	s.codes[code] = storedCode{
		challenge:   req.CodeChallenge,
		method:      "S256",
		state:       req.State,
		redirectURI: req.RedirectURI,
		clientID:    req.ClientID,
		expiresAt:   expiresAt,
	}
	s.mu.Unlock()
	return IssuedCode{Code: code, State: req.State, RedirectURI: req.RedirectURI, ExpiresAt: expiresAt}, nil
}

// TokenRequest carries the fields from a `POST /auth/oauth/token` body.
type TokenRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
}

// Redeem validates and consumes the supplied authorization code. The caller
// then maps the redeemed code onto whatever access token the surrounding
// system uses (for the v1 shim that is the operator bearer token).
func (s *Store) Redeem(req TokenRequest) error {
	if req.GrantType != "authorization_code" {
		return fmt.Errorf("oauth: unsupported grant_type %q", req.GrantType)
	}
	if req.ClientID != "" && req.ClientID != DefaultClientID {
		return ErrUnknownClient
	}
	if strings.TrimSpace(req.Code) == "" {
		return ErrUnknownCode
	}
	if strings.TrimSpace(req.CodeVerifier) == "" {
		return ErrCodeVerifierMismatch
	}
	s.mu.Lock()
	stored, ok := s.codes[req.Code]
	if ok {
		delete(s.codes, req.Code)
	}
	s.mu.Unlock()
	if !ok {
		return ErrUnknownCode
	}
	if s.now().After(stored.expiresAt) {
		return ErrUnknownCode
	}
	if !verifyPKCEChallenge(req.CodeVerifier, stored.challenge) {
		return ErrCodeVerifierMismatch
	}
	if stored.redirectURI != req.RedirectURI {
		return ErrRedirectURIMismatch
	}
	return nil
}

// verifyPKCEChallenge implements RFC 7636 S256: BASE64URL(SHA256(verifier))
// must equal the stored challenge. Both inputs are expected to be raw
// base64url (no padding).
func verifyPKCEChallenge(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return got == strings.TrimRight(challenge, "=")
}

// ComputeS256Challenge returns the BASE64URL-NOPAD SHA-256 of verifier. The
// CLI uses this to generate the authorize URL.
func ComputeS256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewCodeVerifier returns a fresh PKCE verifier (43..128 chars from the
// RFC 7636 unreserved set, sourced from crypto/rand).
func NewCodeVerifier() (string, error) {
	return newRandomString(48)
}

// NewState returns a fresh CSRF state token.
func NewState() (string, error) {
	return newRandomString(24)
}

func newRandomString(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
