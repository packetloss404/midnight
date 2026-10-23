// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Package automationtoken owns the closed storage and verification foundation
// for project-scoped service-account credentials. It does not authorize HTTP
// routes or deployments.
package automationtoken

import (
	"context"
	"errors"
	"time"
)

const (
	TokenPrefix               = "midn_sa_"
	ProfileCIDeployV1 Profile = "ci-deploy-v1"
	MinMACKeyBytes            = 32
)

var (
	ErrInvalidToken    = errors.New("automation token is invalid, expired, or revoked")
	ErrTokenNotFound   = errors.New("automation token not found")
	ErrScopeNotFound   = errors.New("automation token scope is not active")
	ErrInvalidRequest  = errors.New("invalid automation token request")
	ErrVersionConflict = errors.New("automation token resource version conflict")
)

// Profile is an immutable server-owned automation policy version. A profile is
// metadata only until an exact route/capability admission registry consumes it.
type Profile string

// Token is non-secret durable metadata. It intentionally has no verifier field.
type Token struct {
	ID               string     `json:"id"`
	ServiceAccountID string     `json:"service_account_id"`
	WorkspaceID      string     `json:"workspace_id"`
	ProjectID        string     `json:"project_id"`
	Name             string     `json:"name"`
	TokenPrefix      string     `json:"token_prefix"`
	Profile          Profile    `json:"profile"`
	CreatedByUserID  string     `json:"created_by_user_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokedByUserID  string     `json:"revoked_by_user_id,omitempty"`
	RevokedReason    string     `json:"revoked_reason,omitempty"`
	ResourceVersion  int64      `json:"resource_version"`
}

// Principal is verified automation identity metadata. It contains neither the
// raw credential nor its verifier and conveys no route authorization by itself.
type Principal struct {
	Kind             string    `json:"kind"`
	TokenID          string    `json:"token_id"`
	ServiceAccountID string    `json:"service_account_id"`
	WorkspaceID      string    `json:"workspace_id"`
	ProjectID        string    `json:"project_id"`
	Profile          Profile   `json:"profile"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// MintRequest names an existing, active service account and project in the
// same workspace. Service-account creation is outside this package.
type MintRequest struct {
	ServiceAccountID string
	WorkspaceID      string
	ProjectID        string
	Name             string
	Profile          Profile
	CreatedByUserID  string
	ExpiresAt        time.Time
}

// MintResult returns raw bearer material exactly once alongside safe metadata.
type MintResult struct {
	AccessToken string `json:"access_token"`
	Token       Token  `json:"token"`
}

// RevokeRequest is explicitly tenant/project scoped. The first successful
// revocation attribution is immutable; repeated revocation is idempotent.
type RevokeRequest struct {
	TokenID         string
	WorkspaceID     string
	ProjectID       string
	RevokedByUserID string
	Reason          string
	ExpectedVersion *int64
}

// Credential is the private hash-bearing value passed to a Store. Verifier is
// a domain-separated HMAC-SHA-256 value, never a raw bearer.
type Credential struct {
	Token
	Verifier []byte `json:"-"`
}

// Creator is the minimum persistence seam needed to commit a prepared mint.
// Store implements it; transaction adapters may implement it so credential
// creation can join authorization, idempotency, and audit in one transaction.
type Creator interface {
	Create(context.Context, Credential) (Token, error)
}

// Revoker is the minimum persistence seam needed to commit revocation.
type Revoker interface {
	Revoke(context.Context, RevokeRequest, time.Time) (Token, error)
}

// Store persists only a verifier and non-secret metadata. Authenticate must
// atomically reject inactive scope/expiry/revocation and record last use.
type Store interface {
	Creator
	Authenticate(context.Context, []byte, time.Time) (Token, error)
	List(context.Context, string, string) ([]Token, error)
	Get(context.Context, string, string, string) (Token, error)
	Revoker
}

// Scope identifies a service account that may mint credentials for one project.
// MemoryStore uses it to model the composite foreign-key boundary.
type Scope struct {
	ServiceAccountID string
	WorkspaceID      string
	ProjectID        string
}
