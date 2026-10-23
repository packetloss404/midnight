// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package automationtoken

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var testMACKey = []byte("0123456789abcdef0123456789abcdef")

func TestMintAuthenticateListAndRevokeWithoutPersistingRawSecret(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	scope := Scope{
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_one",
		ProjectID:        "prj_one",
	}
	store := NewMemoryStore(scope)
	manager := newTestManager(t, store, &now)

	minted, err := manager.Mint(context.Background(), MintRequest{
		ServiceAccountID: scope.ServiceAccountID,
		WorkspaceID:      scope.WorkspaceID,
		ProjectID:        scope.ProjectID,
		Name:             "production deploy",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_owner",
		ExpiresAt:        now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(minted.AccessToken, TokenPrefix) || !validRawToken(minted.AccessToken) {
		t.Fatalf("minted token has unexpected shape: %q", minted.AccessToken)
	}
	if minted.Token.TokenPrefix != minted.AccessToken[:publicPrefixSize] {
		t.Fatalf("public prefix = %q", minted.Token.TokenPrefix)
	}
	if strings.Contains(fmt.Sprintf("%#v", store.records), minted.AccessToken) {
		t.Fatal("memory store retained raw bearer material")
	}
	record := store.records[minted.Token.ID]
	if len(record.Verifier) != 32 ||
		string(record.Verifier) == minted.AccessToken ||
		strings.Contains(string(record.Verifier), minted.AccessToken) {
		t.Fatal("stored verifier is not an opaque HMAC-SHA-256 value")
	}

	principal, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != "automation" ||
		principal.TokenID != minted.Token.ID ||
		principal.ServiceAccountID != scope.ServiceAccountID ||
		principal.WorkspaceID != scope.WorkspaceID ||
		principal.ProjectID != scope.ProjectID ||
		principal.Profile != ProfileCIDeployV1 {
		t.Fatalf("principal = %#v", principal)
	}

	listed, err := manager.List(context.Background(), scope.WorkspaceID, scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("listed tokens = %#v", listed)
	}
	encoded, err := json.Marshal(listed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), minted.AccessToken) ||
		strings.Contains(string(encoded), "verifier") ||
		strings.Contains(string(encoded), "token_hash") {
		t.Fatalf("metadata response leaked credential material: %s", encoded)
	}

	now = now.Add(time.Minute)
	first, err := manager.Revoke(context.Background(), RevokeRequest{
		TokenID:         minted.Token.ID,
		WorkspaceID:     scope.WorkspaceID,
		ProjectID:       scope.ProjectID,
		RevokedByUserID: "usr_owner",
		Reason:          "rotation",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	second, err := manager.Revoke(context.Background(), RevokeRequest{
		TokenID:         minted.Token.ID,
		WorkspaceID:     scope.WorkspaceID,
		ProjectID:       scope.ProjectID,
		RevokedByUserID: "usr_other",
		Reason:          "overwrite-attempt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RevokedAt == nil || second.RevokedAt == nil ||
		!first.RevokedAt.Equal(*second.RevokedAt) ||
		second.RevokedByUserID != "usr_owner" ||
		second.RevokedReason != "rotation" ||
		second.ResourceVersion != 2 {
		t.Fatalf("idempotent revocation changed first attribution: %#v", second)
	}
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("authenticate revoked token = %v", err)
	}
}

func TestAutomationTokenAuthenticationFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	scope := Scope{
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_one",
		ProjectID:        "prj_one",
	}
	store := NewMemoryStore(scope)
	manager := newTestManager(t, store, &now)

	minted, err := manager.Mint(context.Background(), MintRequest{
		ServiceAccountID: scope.ServiceAccountID,
		WorkspaceID:      scope.WorkspaceID,
		ProjectID:        scope.ProjectID,
		Name:             "short lived",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_owner",
		ExpiresAt:        now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, authorization := range []string{
		"",
		"Bearer midn_sa_short",
		"Bearer midn_user_abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG",
		"Bearer midn_admin_abcdefghijklmnopqrstuvwxyz0123456789ABC",
		"Bearer midn_sa_abcdefghijklmnopqrstuvwxyz0123456789ABCDE!",
	} {
		if _, err := manager.Authenticate(
			context.Background(),
			authorization,
		); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("Authenticate(%q) = %v", authorization, err)
		}
	}

	now = minted.Token.ExpiresAt
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("authenticate at expiry boundary = %v", err)
	}

	now = minted.Token.CreatedAt.Add(time.Minute)
	store.SetProjectActive(scope.WorkspaceID, scope.ProjectID, false)
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("authenticate deleted-project token = %v", err)
	}
	store.SetProjectActive(scope.WorkspaceID, scope.ProjectID, true)
	store.SetServiceAccountActive(scope.WorkspaceID, scope.ServiceAccountID, false)
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("authenticate deleted-service-account token = %v", err)
	}
}

func TestAutomationTokenMetadataGetAndConditionalRevokeAreConcurrencySafe(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	scope := Scope{ServiceAccountID: "sa_ci", WorkspaceID: "wrk_one", ProjectID: "prj_one"}
	store := NewMemoryStore(scope)
	manager := newTestManager(t, store, &now)
	minted, err := manager.Mint(context.Background(), MintRequest{
		ServiceAccountID: scope.ServiceAccountID, WorkspaceID: scope.WorkspaceID,
		ProjectID: scope.ProjectID, Name: "conditional", Profile: ProfileCIDeployV1,
		CreatedByUserID: "usr_owner", ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := manager.Get(context.Background(), scope.WorkspaceID, scope.ProjectID, minted.Token.ID)
	if err != nil || metadata.ResourceVersion != 1 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	if _, err := manager.Get(context.Background(), "wrk_other", scope.ProjectID, minted.Token.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("cross-scope get error=%v", err)
	}
	expected := metadata.ResourceVersion
	results := make(chan error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, revokeErr := manager.Revoke(context.Background(), RevokeRequest{
				TokenID: minted.Token.ID, WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID,
				RevokedByUserID: "usr_owner", ExpectedVersion: &expected,
			})
			results <- revokeErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("revoke error=%v", result)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMintAndMetadataOperationsRequireExactScope(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	scope := Scope{
		ServiceAccountID: "sa_ci",
		WorkspaceID:      "wrk_one",
		ProjectID:        "prj_one",
	}
	store := NewMemoryStore(scope)
	manager := newTestManager(t, store, &now)
	request := MintRequest{
		ServiceAccountID: scope.ServiceAccountID,
		WorkspaceID:      scope.WorkspaceID,
		ProjectID:        "prj_other",
		Name:             "wrong scope",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_owner",
		ExpiresAt:        now.Add(time.Hour),
	}
	if _, err := manager.Mint(context.Background(), request); !errors.Is(err, ErrScopeNotFound) {
		t.Fatalf("cross-project mint = %v", err)
	}
	request.ProjectID = scope.ProjectID
	minted, err := manager.Mint(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.List(context.Background(), scope.WorkspaceID, "prj_other")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-project list = %#v", other)
	}
	if _, err := manager.Revoke(context.Background(), RevokeRequest{
		TokenID:         minted.Token.ID,
		WorkspaceID:     scope.WorkspaceID,
		ProjectID:       "prj_other",
		RevokedByUserID: "usr_owner",
	}); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("cross-project revoke = %v", err)
	}
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+minted.AccessToken,
	); err != nil {
		t.Fatalf("cross-project revoke changed token: %v", err)
	}
}

func TestManagerRejectsUnsafeConfigurationAndProfile(t *testing.T) {
	store := NewMemoryStore()
	if _, err := NewManager(ManagerConfig{
		Store:  store,
		MACKey: []byte("short"),
	}); err == nil {
		t.Fatal("weak MAC key accepted")
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager := newTestManager(t, store, &now)
	if _, err := manager.Mint(context.Background(), MintRequest{
		ServiceAccountID: "sa",
		WorkspaceID:      "wrk",
		ProjectID:        "prj",
		Name:             "arbitrary profile",
		Profile:          Profile("admin"),
		CreatedByUserID:  "usr",
		ExpiresAt:        now.Add(time.Hour),
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("arbitrary profile mint = %v", err)
	}
}

func TestStoreFailureDoesNotBecomeAnotherPrincipal(t *testing.T) {
	sentinel := errors.New("database unavailable")
	manager, err := NewManager(ManagerConfig{
		Store:  failingStore{err: sentinel},
		MACKey: testMACKey,
		Random: deterministicRandom(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := TokenPrefix + strings.Repeat("A", 43)
	if !validRawToken(raw) {
		t.Fatalf("test token is not canonical: %q", raw)
	}
	if _, err := manager.Authenticate(
		context.Background(),
		"Bearer "+raw,
	); !errors.Is(err, sentinel) {
		t.Fatalf("store failure = %v, want sentinel", err)
	}
}

func TestPreparedMintKeepsOneCredentialAcrossTransactionRetries(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	scope := Scope{
		ServiceAccountID: "sa_release",
		WorkspaceID:      "wrk_acme",
		ProjectID:        "prj_app",
	}
	store := NewMemoryStore(scope)
	manager := newTestManager(t, store, &now)
	plan, err := manager.PrepareMint(MintRequest{
		ServiceAccountID: scope.ServiceAccountID,
		WorkspaceID:      scope.WorkspaceID,
		ProjectID:        scope.ProjectID,
		Name:             "release",
		Profile:          ProfileCIDeployV1,
		CreatedByUserID:  "usr_owner",
		ExpiresAt:        now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	failing := creatorFunc(func(context.Context, Credential) (Token, error) {
		return Token{}, errors.New("serialization retry")
	})
	if _, err := plan.Commit(t.Context(), failing); err == nil {
		t.Fatal("first Commit error = nil")
	}
	result, err := plan.Commit(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := manager.Authenticate(t.Context(), "Bearer "+result.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if principal.TokenID != result.Token.ID {
		t.Fatalf("principal=%#v token=%#v", principal, result.Token)
	}
}

type creatorFunc func(context.Context, Credential) (Token, error)

func (f creatorFunc) Create(ctx context.Context, credential Credential) (Token, error) {
	return f(ctx, credential)
}

func newTestManager(
	t *testing.T,
	store Store,
	now *time.Time,
) *Manager {
	t.Helper()
	manager, err := NewManager(ManagerConfig{
		Store:  store,
		MACKey: testMACKey,
		Clock:  func() time.Time { return *now },
		Random: deterministicRandom(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func deterministicRandom() func([]byte) error {
	value := byte(0)
	return func(dst []byte) error {
		value++
		for i := range dst {
			dst[i] = value
		}
		return nil
	}
}

type failingStore struct {
	err error
}

func (s failingStore) Create(context.Context, Credential) (Token, error) {
	return Token{}, s.err
}

func (s failingStore) Authenticate(context.Context, []byte, time.Time) (Token, error) {
	return Token{}, s.err
}

func (s failingStore) List(context.Context, string, string) ([]Token, error) {
	return nil, s.err
}

func (s failingStore) Get(context.Context, string, string, string) (Token, error) {
	return Token{}, s.err
}

func (s failingStore) Revoke(context.Context, RevokeRequest, time.Time) (Token, error) {
	return Token{}, s.err
}
