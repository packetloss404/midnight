package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

func TestIdentityClaimStatusIsAnonymousAndFlipsOnClaim(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	access := identity.NewMemoryAccessStore()
	sessionStore := identity.NewMemoryStore()
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store:       sessionStore,
		KeyProvider: testVariableKeyProvider(t),
		MACKey:      []byte("0123456789abcdef0123456789abcdef"),
		Clock:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		IdentityAccess:   access,
		IdempotencyStore: idempotency.NewMemoryStoreWithClock(func() time.Time { return now }),
		AuditChain:       audit.NewMemoryChain(),
		Clock:            func() time.Time { return now },
	})

	// No Authorization header: the console needs this before any credential
	// exists, so the operator gate must not apply.
	before := httptest.NewRecorder()
	handler.ServeHTTP(before, httptest.NewRequest(http.MethodGet, identityClaimStatusPath, nil))
	if before.Code != http.StatusOK {
		t.Fatalf("unclaimed status=%d body=%s", before.Code, before.Body.String())
	}
	if got := before.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(before.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 1 {
		t.Fatalf("claim status leaked extra fields: %v", payload)
	}
	if payload["claimed"] != false {
		t.Fatalf("claimed = %v, want false", payload["claimed"])
	}

	secretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := bootstrap.DecodeTOTPSecret(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	totpCode, err := bootstrap.GenerateTOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(identityClaimRequest{
		Email:                "owner@example.com",
		DisplayName:          "Owner",
		Password:             "correct horse battery staple",
		TOTPSecret:           secretBase32,
		TOTPCode:             totpCode,
		WorkspaceSlug:        "acme-platform",
		WorkspaceDisplayName: "Acme Platform",
	})
	if err != nil {
		t.Fatal(err)
	}
	if claim := performIdentityClaim(handler, body, "claim-status-flip"); claim.Code != http.StatusCreated {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}

	after := httptest.NewRecorder()
	handler.ServeHTTP(after, httptest.NewRequest(http.MethodGet, identityClaimStatusPath, nil))
	if after.Code != http.StatusOK {
		t.Fatalf("claimed status=%d body=%s", after.Code, after.Body.String())
	}
	if err := json.Unmarshal(after.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["claimed"] != true {
		t.Fatalf("claimed = %v, want true", payload["claimed"])
	}
}

func TestIdentityClaimStatusRejectsNonGET(t *testing.T) {
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken: "operator-recovery-token",
		IdentityAccess: identity.NewMemoryAccessStore(),
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, identityClaimStatusPath, nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestIdentityClaimStatusUnavailableWithoutIdentityStorage(t *testing.T) {
	handler := NewRouterWithDataStack(DataStack{APIBearerToken: "operator-recovery-token"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, identityClaimStatusPath, nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
