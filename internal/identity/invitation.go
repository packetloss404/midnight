// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package identity

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
)

const (
	InvitationTokenPrefix = "midn_inv_"
	InvitationMinMACBytes = 32

	invitationSecretBytes   = 32
	invitationIDBytes       = 18
	invitationPrefixSize    = 18
	invitationMACDomain     = "midnight/identity-invitation/v1\x00"
	invitationReissueReason = "reissued"

	defaultInvitationTTL = 7 * 24 * time.Hour
	minInvitationTTL     = 5 * time.Minute
	maxInvitationTTL     = 30 * 24 * time.Hour

	// InvitationDefaultPageLimit and InvitationMaxPageLimit bound metadata
	// materialization independently of retained invitation history.
	InvitationDefaultPageLimit = 50
	InvitationMaxPageLimit     = 200
)

var (
	ErrInvalidInvitation         = errors.New("invalid identity invitation request")
	ErrInvitationNotFound        = errors.New("identity invitation not found")
	ErrInvitationVersionConflict = errors.New("identity invitation resource version conflict")
	ErrInvitationPending         = errors.New("an active invitation already exists")
	ErrInvitationExpired         = errors.New("identity invitation has expired")
	ErrInvitationRevoked         = errors.New("identity invitation has been revoked")
	ErrInvitationConsumed        = errors.New("identity invitation has already been accepted")
	ErrInvitationReplaced        = errors.New("identity invitation has already been replaced")
	ErrInvitationEmailMismatch   = errors.New("identity invitation email does not match the authenticated user")
	ErrInvitationMemberExists    = errors.New("identity invitation target is already a member")
	ErrInvitationScopeNotActive  = errors.New("identity invitation scope is not active")
)

// Invitation is non-secret lifecycle metadata. It never contains the raw
// invitation secret or its durable verifier.
type Invitation struct {
	ID                   string        `json:"id"`
	Email                string        `json:"email"`
	WorkspaceID          string        `json:"workspace_id"`
	ProjectID            string        `json:"project_id,omitempty"`
	WorkspaceRole        WorkspaceRole `json:"workspace_role,omitempty"`
	ProjectRole          ProjectRole   `json:"project_role,omitempty"`
	TokenPrefix          string        `json:"token_prefix"`
	InvitedByUserID      string        `json:"invited_by_user_id"`
	CreatedAt            time.Time     `json:"created_at"`
	UpdatedAt            time.Time     `json:"updated_at"`
	ExpiresAt            time.Time     `json:"expires_at"`
	RevokedAt            *time.Time    `json:"revoked_at,omitempty"`
	RevokedByUserID      string        `json:"revoked_by_user_id,omitempty"`
	RevokedReason        string        `json:"revoked_reason,omitempty"`
	AcceptedAt           *time.Time    `json:"accepted_at,omitempty"`
	AcceptedByUserID     string        `json:"accepted_by_user_id,omitempty"`
	ReplacesInvitationID string        `json:"replaces_invitation_id,omitempty"`
	ResourceVersion      int64         `json:"resource_version"`
}

// InvitationCredential is the private value passed to durable stores.
// Verifier is a domain-separated HMAC-SHA-256 value, never the bearer secret.
type InvitationCredential struct {
	Invitation
	Verifier []byte `json:"-"`
}

// CreateInvitationRequest grants exactly one workspace or direct-project role
// to one normalized email address. A zero ExpiresAt uses the seven-day default.
type CreateInvitationRequest struct {
	Email           string
	WorkspaceID     string
	ProjectID       string
	WorkspaceRole   WorkspaceRole
	ProjectRole     ProjectRole
	InvitedByUserID string
	ExpiresAt       time.Time
}

// CreateInvitationResult returns the raw secret exactly once. Callers must not
// log or persist InvitationToken.
type CreateInvitationResult struct {
	InvitationToken string     `json:"invitation_token"`
	Invitation      Invitation `json:"invitation"`
}

// ListInvitationsRequest is exact-scope and actor-bound. An empty ProjectID
// lists workspace invitations; a non-empty ProjectID lists only that project's
// direct invitations after server-owned lineage verification.
type ListInvitationsRequest struct {
	WorkspaceID string
	ProjectID   string
	ActorUserID string
	Limit       int
	Offset      int
}

// GetInvitationRequest resolves one invitation only within its caller-supplied
// scope. Stores authorize ActorUserID before returning metadata.
type GetInvitationRequest struct {
	InvitationID string
	WorkspaceID  string
	ProjectID    string
	ActorUserID  string
}

// AcceptInvitationRequest binds redemption to an already-authenticated user.
// Stores verify the durable user's email rather than trusting Email alone.
type AcceptInvitationRequest struct {
	InvitationToken string
	UserID          string
	Email           string
}

// InvitationAcceptance is the verifier-bearing store request produced by the
// service after strict bearer validation.
type InvitationAcceptance struct {
	Verifier []byte
	UserID   string
	Email    string
}

func (a *InvitationAcceptance) Destroy() {
	if a == nil {
		return
	}
	clear(a.Verifier)
	*a = InvitationAcceptance{}
}

// RevokeInvitationRequest includes the expected scope so an invitation ID can
// never be used as a cross-tenant lookup oracle.
type RevokeInvitationRequest struct {
	InvitationID    string
	WorkspaceID     string
	ProjectID       string
	RevokedByUserID string
	Reason          string
	// ExpectedVersion is nil for legacy internal callers. HTTP conditional
	// revocation always supplies it and stores compare it atomically.
	ExpectedVersion *int64
}

// ReissueInvitationRequest replaces one invitation with a new secret while
// inheriting the original email, exact role, and scope. A zero ExpiresAt uses
// the seven-day default.
type ReissueInvitationRequest struct {
	InvitationID     string
	WorkspaceID      string
	ProjectID        string
	ReissuedByUserID string
	ExpiresAt        time.Time
}

// InvitationReplacement is the verifier-bearing store request generated by a
// prepare-once plan. Stores inherit the old invitation's email and role.
type InvitationReplacement struct {
	InvitationID           string
	WorkspaceID            string
	ProjectID              string
	ReissuedByUserID       string
	ReplacementID          string
	ReplacementTokenPrefix string
	ReplacementVerifier    []byte
	CreatedAt              time.Time
	ExpiresAt              time.Time
}

// ReissueInvitationMetadata contains only durable, non-secret lifecycle
// evidence for both sides of an atomic replacement.
type ReissueInvitationMetadata struct {
	ReplacedInvitation Invitation `json:"replaced_invitation"`
	Invitation         Invitation `json:"invitation"`
}

// ReissueInvitationResult returns the new raw secret exactly once.
type ReissueInvitationResult struct {
	InvitationToken string                    `json:"invitation_token"`
	Metadata        ReissueInvitationMetadata `json:"metadata"`
}

type InvitationCreator interface {
	CreateInvitation(context.Context, InvitationCredential) (Invitation, error)
}

type InvitationReissuer interface {
	ReissueInvitation(context.Context, InvitationReplacement) (ReissueInvitationMetadata, error)
}

// Function adapters let a transaction coordinator commit a prepared plan
// through CreateInvitationInTx/ReissueInvitationInTx without regenerating raw
// credential material on a serializable retry.
type InvitationCreatorFunc func(context.Context, InvitationCredential) (Invitation, error)

func (f InvitationCreatorFunc) CreateInvitation(ctx context.Context, credential InvitationCredential) (Invitation, error) {
	return f(ctx, credential)
}

type InvitationReissuerFunc func(context.Context, InvitationReplacement) (ReissueInvitationMetadata, error)

func (f InvitationReissuerFunc) ReissueInvitation(ctx context.Context, replacement InvitationReplacement) (ReissueInvitationMetadata, error) {
	return f(ctx, replacement)
}

// InvitationStore owns authority checks and the atomic invitation-to-
// membership transition. Implementations persist only InvitationCredential's
// verifier, never a raw invitation token.
type InvitationStore interface {
	InvitationCreator
	InvitationReissuer
	ListInvitations(context.Context, ListInvitationsRequest) ([]Invitation, error)
	GetInvitation(context.Context, GetInvitationRequest) (Invitation, error)
	AcceptInvitation(context.Context, InvitationAcceptance, time.Time) (Invitation, error)
	RevokeInvitation(context.Context, RevokeInvitationRequest, time.Time) (Invitation, error)
}

type InvitationServiceConfig struct {
	Store  InvitationStore
	MACKey []byte
	Clock  func() time.Time
	Random func([]byte) error
}

// InvitationService owns raw invitation-secret lifecycle. Authorization and
// concurrency invariants remain inside the store transaction/lock boundary.
type InvitationService struct {
	store  InvitationStore
	macKey []byte
	now    func() time.Time
	random func([]byte) error
}

func NewInvitationService(cfg InvitationServiceConfig) (*InvitationService, error) {
	if cfg.Store == nil {
		return nil, errors.New("identity invitation service: store is required")
	}
	if len(cfg.MACKey) < InvitationMinMACBytes {
		return nil, fmt.Errorf(
			"identity invitation service: MAC key is %d bytes, want at least %d",
			len(cfg.MACKey),
			InvitationMinMACBytes,
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
	return &InvitationService{
		store:  cfg.Store,
		macKey: append([]byte(nil), cfg.MACKey...),
		now:    cfg.Clock,
		random: cfg.Random,
	}, nil
}

func (s *InvitationService) Create(ctx context.Context, request CreateInvitationRequest) (CreateInvitationResult, error) {
	plan, err := s.PrepareCreate(request)
	if err != nil {
		return CreateInvitationResult{}, err
	}
	defer plan.Destroy()
	return plan.Commit(ctx, s.store)
}

// InvitationCreatePlan holds one generated raw secret and verifier. Preparing
// outside a retry loop and committing through an InTx adapter prevents
// credential drift between serializable attempts.
type InvitationCreatePlan struct {
	invitationToken string
	credential      InvitationCredential
}

func (s *InvitationService) PrepareCreate(request CreateInvitationRequest) (*InvitationCreatePlan, error) {
	now := s.now().UTC()
	request, err := normalizeCreateInvitationRequest(request, now)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("create identity invitation id: %w", err)
	}
	raw, err := s.newToken()
	if err != nil {
		return nil, fmt.Errorf("create identity invitation token: %w", err)
	}
	credential := InvitationCredential{
		Invitation: Invitation{
			ID:              id,
			Email:           request.Email,
			WorkspaceID:     request.WorkspaceID,
			ProjectID:       request.ProjectID,
			WorkspaceRole:   request.WorkspaceRole,
			ProjectRole:     request.ProjectRole,
			TokenPrefix:     publicInvitationTokenPrefix(raw),
			InvitedByUserID: request.InvitedByUserID,
			CreatedAt:       now,
			UpdatedAt:       now,
			ExpiresAt:       request.ExpiresAt,
			ResourceVersion: 1,
		},
		Verifier: s.tokenMAC(raw),
	}
	return &InvitationCreatePlan{invitationToken: raw, credential: credential}, nil
}

func (p *InvitationCreatePlan) Commit(ctx context.Context, creator InvitationCreator) (CreateInvitationResult, error) {
	if p == nil || creator == nil || p.invitationToken == "" ||
		validateInvitationCredential(p.credential) != nil {
		return CreateInvitationResult{}, ErrInvalidInvitation
	}
	metadata, err := creator.CreateInvitation(ctx, cloneInvitationCredential(p.credential))
	if err != nil {
		return CreateInvitationResult{}, fmt.Errorf("persist identity invitation: %w", err)
	}
	return CreateInvitationResult{InvitationToken: p.invitationToken, Invitation: metadata}, nil
}

func (p *InvitationCreatePlan) Destroy() {
	if p == nil {
		return
	}
	clear(p.credential.Verifier)
	p.credential = InvitationCredential{}
	p.invitationToken = ""
}

func (s *InvitationService) List(ctx context.Context, request ListInvitationsRequest) ([]Invitation, error) {
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.WorkspaceID == "" || request.ActorUserID == "" {
		return nil, ErrInvalidInvitation
	}
	if request.Limit == 0 {
		request.Limit = InvitationDefaultPageLimit
	}
	if request.Limit < 1 || request.Limit > InvitationMaxPageLimit+1 ||
		request.Offset < 0 {
		return nil, ErrInvalidInvitation
	}
	invitations, err := s.store.ListInvitations(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("list identity invitations: %w", err)
	}
	return invitations, nil
}

func (s *InvitationService) Get(ctx context.Context, request GetInvitationRequest) (Invitation, error) {
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ActorUserID = strings.TrimSpace(request.ActorUserID)
	if request.InvitationID == "" || request.WorkspaceID == "" || request.ActorUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	invitation, err := s.store.GetInvitation(ctx, request)
	if err != nil {
		return Invitation{}, fmt.Errorf("get identity invitation: %w", err)
	}
	return invitation, nil
}

func (s *InvitationService) Reissue(ctx context.Context, request ReissueInvitationRequest) (ReissueInvitationResult, error) {
	plan, err := s.PrepareReissue(request)
	if err != nil {
		return ReissueInvitationResult{}, err
	}
	defer plan.Destroy()
	return plan.Commit(ctx, s.store)
}

type InvitationReissuePlan struct {
	invitationToken string
	replacement     InvitationReplacement
}

func (s *InvitationService) PrepareReissue(request ReissueInvitationRequest) (*InvitationReissuePlan, error) {
	now := s.now().UTC()
	request, err := normalizeReissueInvitationRequest(request, now)
	if err != nil {
		return nil, err
	}
	id, err := s.newID()
	if err != nil {
		return nil, fmt.Errorf("reissue identity invitation id: %w", err)
	}
	raw, err := s.newToken()
	if err != nil {
		return nil, fmt.Errorf("reissue identity invitation token: %w", err)
	}
	return &InvitationReissuePlan{
		invitationToken: raw,
		replacement: InvitationReplacement{
			InvitationID:           request.InvitationID,
			WorkspaceID:            request.WorkspaceID,
			ProjectID:              request.ProjectID,
			ReissuedByUserID:       request.ReissuedByUserID,
			ReplacementID:          id,
			ReplacementTokenPrefix: publicInvitationTokenPrefix(raw),
			ReplacementVerifier:    s.tokenMAC(raw),
			CreatedAt:              now,
			ExpiresAt:              request.ExpiresAt,
		},
	}, nil
}

func (p *InvitationReissuePlan) Commit(ctx context.Context, reissuer InvitationReissuer) (ReissueInvitationResult, error) {
	if p == nil || reissuer == nil || p.invitationToken == "" ||
		validateInvitationReplacement(p.replacement) != nil {
		return ReissueInvitationResult{}, ErrInvalidInvitation
	}
	metadata, err := reissuer.ReissueInvitation(ctx, cloneInvitationReplacement(p.replacement))
	if err != nil {
		return ReissueInvitationResult{}, fmt.Errorf("reissue identity invitation: %w", err)
	}
	return ReissueInvitationResult{InvitationToken: p.invitationToken, Metadata: metadata}, nil
}

func (p *InvitationReissuePlan) Destroy() {
	if p == nil {
		return
	}
	clear(p.replacement.ReplacementVerifier)
	p.replacement = InvitationReplacement{}
	p.invitationToken = ""
}

func (s *InvitationService) Accept(ctx context.Context, request AcceptInvitationRequest) (Invitation, error) {
	acceptance, err := s.PrepareAcceptance(request)
	if err != nil {
		return Invitation{}, err
	}
	defer acceptance.Destroy()
	metadata, err := s.store.AcceptInvitation(ctx, acceptance, s.now().UTC())
	if err != nil {
		return Invitation{}, fmt.Errorf("accept identity invitation: %w", err)
	}
	return metadata, nil
}

// PrepareAcceptance validates and hashes one raw invitation token outside a
// serializable retry loop. The returned verifier can be reused with
// AcceptInvitationInTx and should be destroyed after the transaction ends.
func (s *InvitationService) PrepareAcceptance(request AcceptInvitationRequest) (InvitationAcceptance, error) {
	request.UserID = strings.TrimSpace(request.UserID)
	request.Email = normalizeInvitationEmail(request.Email)
	if request.UserID == "" || !validInvitationEmail(request.Email) ||
		!validRawInvitationToken(request.InvitationToken) {
		return InvitationAcceptance{}, ErrInvalidInvitation
	}
	return InvitationAcceptance{
		Verifier: s.tokenMAC(request.InvitationToken),
		UserID:   request.UserID,
		Email:    request.Email,
	}, nil
}

func (s *InvitationService) Revoke(ctx context.Context, request RevokeInvitationRequest) (Invitation, error) {
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.RevokedByUserID = strings.TrimSpace(request.RevokedByUserID)
	request.Reason = cleanInvitationReason(request.Reason)
	if request.InvitationID == "" || request.WorkspaceID == "" ||
		request.RevokedByUserID == "" {
		return Invitation{}, ErrInvalidInvitation
	}
	metadata, err := s.store.RevokeInvitation(ctx, request, s.now().UTC())
	if err != nil {
		return Invitation{}, fmt.Errorf("revoke identity invitation: %w", err)
	}
	return metadata, nil
}

func normalizeCreateInvitationRequest(request CreateInvitationRequest, now time.Time) (CreateInvitationRequest, error) {
	request.Email = normalizeInvitationEmail(request.Email)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.InvitedByUserID = strings.TrimSpace(request.InvitedByUserID)
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = now.Add(defaultInvitationTTL)
	} else {
		request.ExpiresAt = request.ExpiresAt.UTC()
	}
	if !validInvitationEmail(request.Email) || request.WorkspaceID == "" ||
		request.InvitedByUserID == "" {
		return CreateInvitationRequest{}, ErrInvalidInvitation
	}
	ttl := request.ExpiresAt.Sub(now)
	if ttl < minInvitationTTL || ttl > maxInvitationTTL {
		return CreateInvitationRequest{}, ErrInvalidInvitation
	}
	workspaceRole, workspaceRoleOK := ParseWorkspaceRole(string(request.WorkspaceRole))
	projectRole, projectRoleOK := ParseProjectRole(string(request.ProjectRole))
	switch {
	case request.ProjectID == "" && workspaceRoleOK && request.ProjectRole == "":
		request.WorkspaceRole = workspaceRole
	case request.ProjectID != "" && projectRoleOK && request.WorkspaceRole == "":
		request.ProjectRole = projectRole
	default:
		return CreateInvitationRequest{}, ErrInvalidInvitation
	}
	return request, nil
}

func normalizeReissueInvitationRequest(request ReissueInvitationRequest, now time.Time) (ReissueInvitationRequest, error) {
	request.InvitationID = strings.TrimSpace(request.InvitationID)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.ReissuedByUserID = strings.TrimSpace(request.ReissuedByUserID)
	if request.ExpiresAt.IsZero() {
		request.ExpiresAt = now.Add(defaultInvitationTTL)
	} else {
		request.ExpiresAt = request.ExpiresAt.UTC()
	}
	ttl := request.ExpiresAt.Sub(now)
	if request.InvitationID == "" || request.WorkspaceID == "" ||
		request.ReissuedByUserID == "" ||
		ttl < minInvitationTTL || ttl > maxInvitationTTL {
		return ReissueInvitationRequest{}, ErrInvalidInvitation
	}
	return request, nil
}

func validateInvitationReplacement(replacement InvitationReplacement) error {
	if strings.TrimSpace(replacement.InvitationID) != replacement.InvitationID ||
		replacement.InvitationID == "" ||
		strings.TrimSpace(replacement.WorkspaceID) != replacement.WorkspaceID ||
		replacement.WorkspaceID == "" ||
		strings.TrimSpace(replacement.ProjectID) != replacement.ProjectID ||
		strings.TrimSpace(replacement.ReissuedByUserID) != replacement.ReissuedByUserID ||
		replacement.ReissuedByUserID == "" ||
		strings.TrimSpace(replacement.ReplacementID) != replacement.ReplacementID ||
		!strings.HasPrefix(replacement.ReplacementID, "inv_") ||
		replacement.ReplacementID == replacement.InvitationID ||
		len(replacement.ReplacementTokenPrefix) != invitationPrefixSize ||
		!strings.HasPrefix(replacement.ReplacementTokenPrefix, InvitationTokenPrefix) ||
		len(replacement.ReplacementVerifier) != sha256.Size ||
		replacement.ExpiresAt.Sub(replacement.CreatedAt) < minInvitationTTL ||
		replacement.ExpiresAt.Sub(replacement.CreatedAt) > maxInvitationTTL {
		return ErrInvalidInvitation
	}
	return nil
}

func validateInvitationCredential(credential InvitationCredential) error {
	invitation := credential.Invitation
	if len(credential.Verifier) != sha256.Size ||
		strings.TrimSpace(invitation.ID) == "" ||
		!strings.HasPrefix(invitation.ID, "inv_") ||
		!validInvitationEmail(invitation.Email) ||
		invitation.Email != normalizeInvitationEmail(invitation.Email) ||
		strings.TrimSpace(invitation.WorkspaceID) != invitation.WorkspaceID ||
		invitation.WorkspaceID == "" ||
		strings.TrimSpace(invitation.ProjectID) != invitation.ProjectID ||
		strings.TrimSpace(invitation.InvitedByUserID) != invitation.InvitedByUserID ||
		invitation.InvitedByUserID == "" ||
		len(invitation.TokenPrefix) != invitationPrefixSize ||
		!strings.HasPrefix(invitation.TokenPrefix, InvitationTokenPrefix) ||
		invitation.ResourceVersion != 1 ||
		!invitation.ExpiresAt.After(invitation.CreatedAt) ||
		invitation.ExpiresAt.Sub(invitation.CreatedAt) < minInvitationTTL ||
		invitation.ExpiresAt.Sub(invitation.CreatedAt) > maxInvitationTTL ||
		!invitation.UpdatedAt.Equal(invitation.CreatedAt) ||
		invitation.RevokedAt != nil || invitation.AcceptedAt != nil ||
		invitation.RevokedByUserID != "" || invitation.AcceptedByUserID != "" ||
		invitation.RevokedReason != "" ||
		strings.TrimSpace(invitation.ReplacesInvitationID) != invitation.ReplacesInvitationID ||
		invitation.ReplacesInvitationID == invitation.ID {
		return ErrInvalidInvitation
	}
	_, workspaceRoleOK := ParseWorkspaceRole(string(invitation.WorkspaceRole))
	_, projectRoleOK := ParseProjectRole(string(invitation.ProjectRole))
	if invitation.ProjectID == "" {
		if !workspaceRoleOK || invitation.ProjectRole != "" {
			return ErrInvalidInvitation
		}
	} else if !projectRoleOK || invitation.WorkspaceRole != "" {
		return ErrInvalidInvitation
	}
	return nil
}

func (s *InvitationService) newToken() (string, error) {
	var secret [invitationSecretBytes]byte
	if err := s.random(secret[:]); err != nil {
		return "", err
	}
	raw := InvitationTokenPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	clear(secret[:])
	return raw, nil
}

func (s *InvitationService) newID() (string, error) {
	var value [invitationIDBytes]byte
	if err := s.random(value[:]); err != nil {
		return "", err
	}
	id := "inv_" + base64.RawURLEncoding.EncodeToString(value[:])
	clear(value[:])
	return id, nil
}

func (s *InvitationService) tokenMAC(raw string) []byte {
	mac := hmac.New(sha256.New, s.macKey)
	_, _ = mac.Write([]byte(invitationMACDomain))
	_, _ = mac.Write([]byte(raw))
	return mac.Sum(nil)
}

func validRawInvitationToken(raw string) bool {
	if !strings.HasPrefix(raw, InvitationTokenPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(raw, InvitationTokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	defer clear(decoded)
	return len(decoded) == invitationSecretBytes &&
		base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

func publicInvitationTokenPrefix(raw string) string {
	if len(raw) <= invitationPrefixSize {
		return raw
	}
	return raw[:invitationPrefixSize]
}

func normalizeInvitationEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validInvitationEmail(email string) bool {
	at := strings.LastIndexByte(email, '@')
	return at > 0 && at < len(email)-1 && len(email) <= 254 &&
		!strings.ContainsAny(email, " \t\r\n")
}

func cleanInvitationReason(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "user-requested"
	}
	if len(value) > 128 {
		return value[:128]
	}
	return value
}

func cloneInvitation(value Invitation) Invitation {
	if value.RevokedAt != nil {
		cloned := *value.RevokedAt
		value.RevokedAt = &cloned
	}
	if value.AcceptedAt != nil {
		cloned := *value.AcceptedAt
		value.AcceptedAt = &cloned
	}
	return value
}

func cloneInvitationCredential(value InvitationCredential) InvitationCredential {
	value.Invitation = cloneInvitation(value.Invitation)
	value.Verifier = append([]byte(nil), value.Verifier...)
	return value
}

func cloneInvitationReplacement(value InvitationReplacement) InvitationReplacement {
	value.ReplacementVerifier = append([]byte(nil), value.ReplacementVerifier...)
	return value
}
