// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package automationtoken

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/auth"
)

const (
	tokenSecretBytes = 32
	tokenIDBytes     = 18
	publicPrefixSize = 18
	verifierDomain   = "midnight/project-automation-token/v1\x00"
)

// Manager owns raw credential lifecycle. It is intentionally not wired into
// product middleware in this foundation.
type Manager struct {
	store  Store
	macKey []byte
	now    func() time.Time
	random func([]byte) error
}

type ManagerConfig struct {
	Store  Store
	MACKey []byte
	Clock  func() time.Time
	Random func([]byte) error
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Store == nil {
		return nil, errors.New("automation token manager: store is required")
	}
	if len(cfg.MACKey) < MinMACKeyBytes {
		return nil, fmt.Errorf(
			"automation token manager: MAC key is %d bytes, want at least %d",
			len(cfg.MACKey),
			MinMACKeyBytes,
		)
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Random == nil {
		cfg.Random = func(dst []byte) error {
			_, err := rand.Read(dst)
			return err
		}
	}
	return &Manager{
		store:  cfg.Store,
		macKey: append([]byte(nil), cfg.MACKey...),
		now:    cfg.Clock,
		random: cfg.Random,
	}, nil
}

// Mint creates a one-time raw credential for an existing scoped service
// account. Callers must not log or persist MintResult.AccessToken.
func (m *Manager) Mint(ctx context.Context, request MintRequest) (MintResult, error) {
	plan, err := m.PrepareMint(request)
	if err != nil {
		return MintResult{}, err
	}
	defer plan.Destroy()
	return plan.Commit(ctx, m.store)
}

// MintPlan contains one generated raw credential and its verifier without
// exposing either field. Coordinators prepare a plan once before a retryable
// serializable transaction, then commit the same plan through a transaction
// adapter. A subsequent HTTP replay may prepare and discard a different plan,
// but the committed encrypted idempotency response remains authoritative.
type MintPlan struct {
	accessToken string
	credential  Credential
}

// PrepareMint validates and generates a stable credential without persisting
// it. Callers must promptly Commit or discard the plan and must never format it.
func (m *Manager) PrepareMint(request MintRequest) (*MintPlan, error) {
	now := m.now().UTC()
	request, err := validateMintRequest(request, now)
	if err != nil {
		return nil, err
	}
	tokenID, err := m.newID()
	if err != nil {
		return nil, fmt.Errorf("mint automation token id: %w", err)
	}
	raw, err := m.newToken()
	if err != nil {
		return nil, fmt.Errorf("mint automation token: %w", err)
	}
	credential := Credential{
		Token: Token{
			ID:               tokenID,
			ServiceAccountID: request.ServiceAccountID,
			WorkspaceID:      request.WorkspaceID,
			ProjectID:        request.ProjectID,
			Name:             request.Name,
			TokenPrefix:      publicTokenPrefix(raw),
			Profile:          request.Profile,
			CreatedByUserID:  request.CreatedByUserID,
			CreatedAt:        now,
			UpdatedAt:        now,
			ExpiresAt:        request.ExpiresAt,
			ResourceVersion:  1,
		},
		Verifier: m.tokenMAC(raw),
	}
	return &MintPlan{accessToken: raw, credential: credential}, nil
}

// Commit persists a prepared credential through the supplied creator.
func (p *MintPlan) Commit(ctx context.Context, creator Creator) (MintResult, error) {
	if p == nil || creator == nil || p.accessToken == "" ||
		p.credential.ID == "" || len(p.credential.Verifier) != sha256.Size {
		return MintResult{}, ErrInvalidRequest
	}
	metadata, err := creator.Create(ctx, cloneCredential(p.credential))
	if err != nil {
		return MintResult{}, fmt.Errorf("persist automation token verifier: %w", err)
	}
	return MintResult{AccessToken: p.accessToken, Token: metadata}, nil
}

// Destroy drops the plan's references after commit/rollback. Go strings may
// have been copied by the runtime, so this is defensive lifetime reduction,
// not a guarantee of physical memory erasure.
func (p *MintPlan) Destroy() {
	if p == nil {
		return
	}
	clear(p.credential.Verifier)
	p.credential = Credential{}
	p.accessToken = ""
}

// Authenticate verifies an exact midn_sa_ bearer and returns scoped metadata.
// Store failures remain errors and are never converted to another principal.
func (m *Manager) Authenticate(ctx context.Context, authorization string) (Principal, error) {
	parsed, err := auth.ParseBearerToken(authorization)
	if err != nil || parsed.Class != auth.TokenClassSA || !validRawToken(parsed.Raw) {
		return Principal{}, ErrInvalidToken
	}
	metadata, err := m.store.Authenticate(ctx, m.tokenMAC(parsed.Raw), m.now().UTC())
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) || errors.Is(err, ErrScopeNotFound) {
			return Principal{}, ErrInvalidToken
		}
		return Principal{}, fmt.Errorf("authenticate automation token: %w", err)
	}
	if metadata.Profile != ProfileCIDeployV1 {
		return Principal{}, ErrInvalidToken
	}
	return Principal{
		Kind:             "automation",
		TokenID:          metadata.ID,
		ServiceAccountID: metadata.ServiceAccountID,
		WorkspaceID:      metadata.WorkspaceID,
		ProjectID:        metadata.ProjectID,
		Profile:          metadata.Profile,
		ExpiresAt:        metadata.ExpiresAt,
	}, nil
}

// List returns metadata only for one explicit workspace/project scope.
func (m *Manager) List(ctx context.Context, workspaceID, projectID string) ([]Token, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	projectID = strings.TrimSpace(projectID)
	if workspaceID == "" || projectID == "" {
		return nil, ErrInvalidRequest
	}
	tokens, err := m.store.List(ctx, workspaceID, projectID)
	if err != nil {
		return nil, fmt.Errorf("list automation tokens: %w", err)
	}
	return tokens, nil
}

// Get returns metadata only for one exact workspace/project/token scope.
func (m *Manager) Get(ctx context.Context, workspaceID, projectID, tokenID string) (Token, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	projectID = strings.TrimSpace(projectID)
	tokenID = strings.TrimSpace(tokenID)
	if workspaceID == "" || projectID == "" || tokenID == "" {
		return Token{}, ErrInvalidRequest
	}
	token, err := m.store.Get(ctx, workspaceID, projectID, tokenID)
	if err != nil {
		return Token{}, fmt.Errorf("get automation token: %w", err)
	}
	return token, nil
}

// Revoke idempotently records the first revocation attribution.
func (m *Manager) Revoke(ctx context.Context, request RevokeRequest) (Token, error) {
	return m.RevokeWith(ctx, request, m.store)
}

// RevokeWith commits revocation through a transaction-aware adapter while
// retaining the manager's canonical request normalization.
func (m *Manager) RevokeWith(ctx context.Context, request RevokeRequest, revoker Revoker) (Token, error) {
	request.TokenID = strings.TrimSpace(request.TokenID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RevokedByUserID = strings.TrimSpace(request.RevokedByUserID)
	request.Reason = cleanReason(request.Reason)
	if request.TokenID == "" || request.WorkspaceID == "" ||
		request.ProjectID == "" || request.RevokedByUserID == "" || revoker == nil {
		return Token{}, ErrInvalidRequest
	}
	token, err := revoker.Revoke(ctx, request, m.now().UTC())
	if err != nil {
		return Token{}, fmt.Errorf("revoke automation token: %w", err)
	}
	return token, nil
}

func validateMintRequest(request MintRequest, now time.Time) (MintRequest, error) {
	request.ServiceAccountID = strings.TrimSpace(request.ServiceAccountID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.Name = strings.TrimSpace(request.Name)
	request.CreatedByUserID = strings.TrimSpace(request.CreatedByUserID)
	request.ExpiresAt = request.ExpiresAt.UTC()
	if request.ServiceAccountID == "" || request.WorkspaceID == "" ||
		request.ProjectID == "" || request.Name == "" ||
		request.CreatedByUserID == "" || len(request.Name) > 128 ||
		request.Profile != ProfileCIDeployV1 || !request.ExpiresAt.After(now) {
		return MintRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func (m *Manager) newToken() (string, error) {
	var secret [tokenSecretBytes]byte
	if err := m.random(secret[:]); err != nil {
		return "", err
	}
	raw := TokenPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	clear(secret[:])
	return raw, nil
}

func (m *Manager) newID() (string, error) {
	var value [tokenIDBytes]byte
	if err := m.random(value[:]); err != nil {
		return "", err
	}
	id := "tok_" + base64.RawURLEncoding.EncodeToString(value[:])
	clear(value[:])
	return id, nil
}

func (m *Manager) tokenMAC(raw string) []byte {
	mac := hmac.New(sha256.New, m.macKey)
	_, _ = mac.Write([]byte(verifierDomain))
	_, _ = mac.Write([]byte(raw))
	return mac.Sum(nil)
}

func validRawToken(raw string) bool {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(raw, TokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	defer clear(decoded)
	return len(decoded) == tokenSecretBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

func publicTokenPrefix(raw string) string {
	if len(raw) <= publicPrefixSize {
		return raw
	}
	return raw[:publicPrefixSize]
}

func cleanReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "user-requested"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}
