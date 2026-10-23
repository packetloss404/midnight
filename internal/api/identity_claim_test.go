package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
)

func TestIdentityClaimRequiresVerifiedTOTPAndRedactsIdempotencyFingerprint(t *testing.T) {
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
	idem := idempotency.NewMemoryStoreWithClock(func() time.Time { return now })
	chain := audit.NewMemoryChain()
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		IdentityAccess:   access,
		IdempotencyStore: idem,
		AuditChain:       chain,
		Clock:            func() time.Time { return now },
	})

	missingMFA := httptest.NewRecorder()
	missingMFARequest := httptest.NewRequest(http.MethodPost, identityClaimPath, strings.NewReader(`{
		"email":"owner@example.com",
		"display_name":"Owner",
		"password":"correct horse battery staple",
		"workspace_slug":"acme-platform",
		"workspace_display_name":"Acme Platform"
	}`))
	missingMFARequest.Header.Set("Authorization", "Bearer operator-recovery-token")
	missingMFARequest.Header.Set("Idempotency-Key", "missing-mfa")
	handler.ServeHTTP(missingMFA, missingMFARequest)
	if missingMFA.Code != http.StatusBadRequest ||
		!strings.Contains(missingMFA.Body.String(), `"code":"MFA_INVALID_CODE"`) {
		t.Fatalf("missing MFA claim status=%d body=%s", missingMFA.Code, missingMFA.Body.String())
	}

	secretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := bootstrap.DecodeTOTPSecret(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	code, err := bootstrap.GenerateTOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	document := identityClaimRequest{
		Email:                "owner@example.com",
		DisplayName:          "Owner",
		Password:             "correct horse battery staple",
		TOTPSecret:           secretBase32,
		TOTPCode:             code,
		WorkspaceSlug:        "acme-platform",
		WorkspaceDisplayName: "Acme Platform",
	}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	claim := performIdentityClaim(handler, body, "claim-default")
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim status=%d body=%s", claim.Code, claim.Body.String())
	}
	var first identity.ClaimDefaultWorkspaceResult
	if err := json.Unmarshal(claim.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if !first.User.MFAEnabled {
		t.Fatalf("claimed user = %#v, want MFA enabled", first.User)
	}

	redactedBody, _ := json.Marshal(struct {
		Email                string `json:"email"`
		DisplayName          string `json:"display_name"`
		WorkspaceSlug        string `json:"workspace_slug"`
		WorkspaceDisplayName string `json:"workspace_display_name"`
	}{
		Email:                document.Email,
		DisplayName:          document.DisplayName,
		WorkspaceSlug:        document.WorkspaceSlug,
		WorkspaceDisplayName: document.WorkspaceDisplayName,
	})
	idempotencyContext := idempotency.WithActor(t.Context(), idempotency.ActorRecoveryOperator)
	idempotencyContext = idempotency.WithTenant(idempotencyContext, idempotency.Tenant(identity.DefaultWorkspaceID))
	stored, err := idem.Load(
		idempotencyContext,
		idempotency.Key(identityClaimIdempotencyKey("claim-default")),
		idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       identityClaimPath,
			BodySHA256: idempotency.BodyHashSHA256(redactedBody),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fingerprint.BodySHA256 != idempotency.BodyHashSHA256(redactedBody) {
		t.Fatalf("stored claim fingerprint = %q, want secret-free document hash", stored.Fingerprint.BodySHA256)
	}
	if stored.Fingerprint.BodySHA256 == idempotency.BodyHashSHA256(body) {
		t.Fatal("claim idempotency persisted a verifier for the password-bearing body")
	}

	replay := performIdentityClaim(handler, body, "claim-default")
	if replay.Code != http.StatusCreated {
		t.Fatalf("claim replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayed identity.ClaimDefaultWorkspaceResult
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.User.ID != first.User.ID {
		t.Fatalf("claim replay user=%q, want %q", replayed.User.ID, first.User.ID)
	}
	replayedCode := performIdentityClaim(handler, body, "different-claim-key")
	if replayedCode.Code != http.StatusConflict {
		t.Fatalf("replayed enrollment proof status=%d body=%s", replayedCode.Code, replayedCode.Body.String())
	}
	rows, err := chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Action != "identity.default-workspace.claim" ||
		rows[0].ActorID != first.User.ID || rows[0].WorkspaceID != identity.DefaultWorkspaceID ||
		rows[1].Action != "identity.default-workspace.claim.denied" {
		t.Fatalf("claim audit rows = %#v", rows)
	}
}

func TestPostgresIdentityClaimCoordinatorRejectsSplitAuthorities(t *testing.T) {
	primaryPool := &pg.Pool{}
	otherPool := &pg.Pool{}
	coordinator := &PostgresIdentityClaimCoordinator{
		Pool:        primaryPool,
		Identity:    identity.NewPostgresStore(primaryPool),
		Idempotency: idempotency.NewPostgresStore(otherPool),
		Audit:       audit.NewPostgresChain(primaryPool),
	}
	_, err := coordinator.ClaimDefaultWorkspace(t.Context(), IdentityClaimCommitRequest{
		Claim: identity.ClaimDefaultWorkspaceRequest{UserID: "usr_claim"},
		Key:   "identity-claim-test-key",
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       identityClaimPath,
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{"email":"owner@example.com"}`)),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "do not share one PostgreSQL pool") {
		t.Fatalf("split-authority identity claim error = %v", err)
	}
}

func TestIdentityClaimRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store:       identity.NewMemoryStore(),
		KeyProvider: testVariableKeyProvider(t),
		MACKey:      []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		IdentityAccess:   identity.NewMemoryAccessStore(),
	})
	tests := map[string]struct {
		body       string
		wantStatus int
	}{
		"duplicate member":  {`{"email":"first@example.com","email":"owner@example.com"}`, http.StatusBadRequest},
		"trailing document": {`{"email":"owner@example.com"} {}`, http.StatusBadRequest},
		"oversized":         {`{"email":"` + strings.Repeat("x", int(sensitiveJSONBodyLimitBytes)) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			response := performIdentityClaim(handler, []byte(test.body), "malformed-claim")
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if got := response.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type=%q", got)
			}
			var problem ErrorResponse
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil || problem.Code != failurecodes.ResourceValidationFailed {
				t.Fatalf("problem=%#v err=%v body=%s", problem, err, response.Body.String())
			}
		})
	}
}

func performIdentityClaim(handler http.Handler, body []byte, key string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, identityClaimPath, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer operator-recovery-token")
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}
