package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/bootstrap"
)

func TestSessionLoginAuthenticateExpireAndRevoke(t *testing.T) {
	passwordHash, err := bootstrap.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(UserRecord{
		ID:                "usr_alice",
		Email:             "alice@example.com",
		DisplayName:       "Alice",
		InstanceAdmin:     true,
		PasswordHash:      passwordHash,
		SessionGeneration: 1,
	})
	manager, err := NewManager(ManagerConfig{
		Store:   store,
		MACKey:  []byte("0123456789abcdef0123456789abcdef"),
		Clock:   func() time.Time { return now },
		TTL:     2 * time.Hour,
		IdleTTL: 30 * time.Minute,
		Random:  deterministicRandom,
	})
	if err != nil {
		t.Fatal(err)
	}

	login, err := manager.Login(context.Background(), LoginRequest{
		Email:      " ALICE@example.com ",
		Password:   "correct horse battery staple",
		ClientKind: "cli",
		UserAgent:  "midnight/test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(login.AccessToken, "midn_user_") {
		t.Fatalf("token = %q", login.AccessToken)
	}
	if !login.User.InstanceAdmin {
		t.Fatal("login result omitted installation-administrator authority")
	}
	if strings.Contains(string(login.Session.TokenHash), login.AccessToken) {
		t.Fatal("stored token hash contains raw token")
	}

	principal, session, err := manager.Authenticate(context.Background(), "Bearer "+login.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if principal.UserID != "usr_alice" || principal.SessionID != login.Session.ID {
		t.Fatalf("principal = %#v", principal)
	}
	if principal.AuthMethod != "password" {
		t.Fatalf("auth method = %q, want password", principal.AuthMethod)
	}
	if !principal.InstanceAdmin {
		t.Fatal("authenticated principal omitted installation-administrator authority")
	}
	if !session.IdleExpiresAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("idle expiry = %v", session.IdleExpiresAt)
	}

	if err := manager.Revoke(context.Background(), principal, session.ID, "logout"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Authenticate(context.Background(), "Bearer "+login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("authenticate after revoke = %v", err)
	}
}

func TestSessionAbsoluteAndIdleExpiryFailClosed(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(UserRecord{ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash, SessionGeneration: 1})
	manager, err := NewManager(ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now }, TTL: time.Hour, IdleTTL: 10 * time.Minute,
		Random: deterministicRandom,
	})
	if err != nil {
		t.Fatal(err)
	}
	login, err := manager.Login(context.Background(), LoginRequest{Email: "a@example.com", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(11 * time.Minute)
	if _, _, err := manager.Authenticate(context.Background(), "Bearer "+login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("idle expiry error = %v", err)
	}
}

func TestCredentialStateChangeInvalidatesExistingSession(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	store := NewMemoryStore(UserRecord{
		ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash,
		SessionGeneration: 1,
	})
	manager, err := NewManager(ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Random: deterministicRandom,
	})
	if err != nil {
		t.Fatal(err)
	}
	login, err := manager.Login(context.Background(), LoginRequest{
		Email: "a@example.com", Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.RequirePasswordRotation("usr_a")
	if _, _, err := manager.Authenticate(context.Background(), "Bearer "+login.AccessToken); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("credential-state change left session active: %v", err)
	}
}

func TestRevokeAllInvalidatesEveryGeneration(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(UserRecord{ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash, SessionGeneration: 1})
	counter := byte(0)
	manager, _ := NewManager(ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now },
		Random: func(dst []byte) error {
			counter++
			for i := range dst {
				dst[i] = counter
			}
			return nil
		},
	})
	first, _ := manager.Login(context.Background(), LoginRequest{Email: "a@example.com", Password: "correct horse battery staple"})
	second, _ := manager.Login(context.Background(), LoginRequest{Email: "a@example.com", Password: "correct horse battery staple"})
	principal, _, err := manager.Authenticate(context.Background(), "Bearer "+first.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeAll(context.Background(), principal, "logout-all"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.AccessToken, second.AccessToken} {
		if _, _, err := manager.Authenticate(context.Background(), "Bearer "+token); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("token remained active: %v", err)
		}
	}
}

func TestLoginRejectsDisabledAndUnknownUsersGenerically(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	store := NewMemoryStore(UserRecord{
		ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash,
		SessionGeneration: 1, Disabled: true,
	})
	manager, _ := NewManager(ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Random: deterministicRandom,
	})
	for _, email := range []string{"a@example.com", "missing@example.com"} {
		_, err := manager.Login(context.Background(), LoginRequest{Email: email, Password: "wrong password value"})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("email %q error = %v", email, err)
		}
	}
}

func TestLoginRateLimitBoundsKnownAndUnknownAccountsAndRecovers(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	for _, email := range []string{"a@example.com", "missing@example.com"} {
		t.Run(email, func(t *testing.T) {
			now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
			store := NewMemoryStore(UserRecord{
				ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash,
				SessionGeneration: 1,
			})
			manager, _ := NewManager(ManagerConfig{
				Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
				Clock: func() time.Time { return now }, Random: deterministicRandom,
				LoginWindow: time.Minute, LoginLimit: 2,
			})
			request := LoginRequest{
				Email: email, Password: "wrong password value", RemoteAddr: "192.0.2.10:4242",
			}
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := manager.Login(context.Background(), request); !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("attempt %d error = %v", attempt+1, err)
				}
			}
			if _, err := manager.Login(context.Background(), request); !errors.Is(err, ErrLoginRateLimited) {
				t.Fatalf("rate-limited error = %v", err)
			}
			now = now.Add(time.Minute)
			if _, err := manager.Login(context.Background(), request); !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("post-window error = %v", err)
			}
		})
	}
}

func TestSuccessfulLoginCannotResetAnotherAccountsNetworkBudget(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager, err := NewManager(ManagerConfig{
		Store: NewMemoryStore(UserRecord{
			ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash,
			SessionGeneration: 1,
		}),
		MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock:  func() time.Time { return now }, Random: deterministicRandom,
		LoginWindow: time.Minute, LoginLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < loginNetworkMultiplier-1; attempt++ {
		_, err := manager.Login(context.Background(), LoginRequest{
			Email:      fmt.Sprintf("missing-%d@example.com", attempt),
			Password:   "wrong password value",
			RemoteAddr: "192.0.2.10:4242",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("unknown attempt %d error = %v", attempt+1, err)
		}
	}
	if _, err := manager.Login(context.Background(), LoginRequest{
		Email: "a@example.com", Password: "correct horse battery staple",
		RemoteAddr: "192.0.2.10:4242",
	}); err != nil {
		t.Fatalf("valid login at network budget boundary: %v", err)
	}
	if _, err := manager.Login(context.Background(), LoginRequest{
		Email: "another-missing@example.com", Password: "wrong password value",
		RemoteAddr: "192.0.2.10:4242",
	}); !errors.Is(err, ErrLoginRateLimited) {
		t.Fatalf("network budget was reset by another account's success: %v", err)
	}
}

func TestLoginBoundsActiveSessions(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(UserRecord{
		ID: "usr_a", Email: "a@example.com", PasswordHash: passwordHash,
		SessionGeneration: 1,
	})
	counter := byte(0)
	manager, _ := NewManager(ManagerConfig{
		Store: store, MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now }, MaxSessions: 2,
		Random: func(dst []byte) error {
			counter++
			for i := range dst {
				dst[i] = counter
			}
			return nil
		},
	})
	tokens := make([]string, 0, 3)
	for range 3 {
		login, err := manager.Login(context.Background(), LoginRequest{
			Email: "a@example.com", Password: "correct horse battery staple",
		})
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, login.AccessToken)
		now = now.Add(time.Second)
	}
	if _, _, err := manager.Authenticate(context.Background(), "Bearer "+tokens[0]); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("oldest session remained active: %v", err)
	}
	for _, token := range tokens[1:] {
		if _, _, err := manager.Authenticate(context.Background(), "Bearer "+token); err != nil {
			t.Fatalf("new session rejected: %v", err)
		}
	}
}

func deterministicRandom(dst []byte) error {
	for i := range dst {
		dst[i] = byte(i + 1)
	}
	return nil
}
