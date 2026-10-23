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

type identityRecoveryFixture struct {
	handler     http.Handler
	store       *identity.MemoryStore
	manager     *identity.Manager
	chain       *audit.MemoryChain
	idem        idempotency.Store
	now         time.Time
	oldPassword string
	oldTOTP     string
	newPassword string
}

// newIdentityRecoveryFixture builds a locked-out installation administrator
// whose enrollment is really sealed, so a reset can be observed against a
// session and a login that actually worked beforehand. mutate adjusts the
// account before it is stored (disabled, non-admin, no second factor).
func newIdentityRecoveryFixture(t *testing.T, mutate func(*identity.UserRecord)) identityRecoveryFixture {
	t.Helper()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	keys := testVariableKeyProvider(t)
	macKey := []byte("0123456789abcdef0123456789abcdef")

	oldTOTP, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := identity.NewManager(identity.ManagerConfig{
		Store: identity.NewMemoryStore(), KeyProvider: keys, MACKey: macKey,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealer.SealTOTP(t.Context(), "usr_admin", mustDecodeTOTP(t, oldTOTP))
	if err != nil {
		t.Fatal(err)
	}
	const oldPassword = "forgotten password value"
	hash, err := bootstrap.HashPassword(oldPassword)
	if err != nil {
		t.Fatal(err)
	}
	user := identity.UserRecord{
		ID:                 "usr_admin",
		Email:              "ops@example.com",
		DisplayName:        "Ops",
		InstanceAdmin:      true,
		PasswordHash:       hash,
		TOTPSecretEnvelope: envelope,
		TOTPEnrolledAt:     now.Add(-24 * time.Hour),
		SessionGeneration:  1,
	}
	if mutate != nil {
		mutate(&user)
	}

	store := identity.NewMemoryStore(user)
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store: store, KeyProvider: keys, MACKey: macKey,
		Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	idem := idempotency.NewMemoryStoreWithClock(func() time.Time { return now })
	chain := audit.NewMemoryChain()
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		IdentityRecovery: store,
		IdentityAccess:   identity.NewMemoryAccessStore(),
		IdempotencyStore: idem,
		AuditChain:       chain,
		Clock:            func() time.Time { return now },
	})
	return identityRecoveryFixture{
		handler: handler, store: store, manager: manager,
		chain: chain, idem: idem, now: now,
		oldPassword: oldPassword, oldTOTP: oldTOTP,
		newPassword: "correct horse battery staple",
	}
}

// signIn establishes a live session with the pre-reset credentials.
func (f identityRecoveryFixture) signIn(t *testing.T) identity.LoginResult {
	t.Helper()
	code, err := bootstrap.GenerateTOTP(mustDecodeTOTP(t, f.oldTOTP), f.now)
	if err != nil {
		t.Fatal(err)
	}
	login, err := f.manager.Login(t.Context(), identity.LoginRequest{
		Email:      "ops@example.com",
		Password:   f.oldPassword,
		TOTPCode:   code,
		ClientKind: "dashboard",
	})
	if err != nil {
		t.Fatalf("sign in before reset: %v", err)
	}
	return login
}

func withoutTOTPEnrollment(user *identity.UserRecord) {
	user.TOTPSecretEnvelope = nil
	user.TOTPEnrolledAt = time.Time{}
}

func performIdentityRecovery(handler http.Handler, body []byte, key, bearer string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, identityRecoveryPath, strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Idempotency-Key", key)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}

func recoveryEnrollment(t *testing.T, now time.Time) (string, string) {
	t.Helper()
	secretBase32, err := bootstrap.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, err := bootstrap.GenerateTOTP(mustDecodeTOTP(t, secretBase32), now)
	if err != nil {
		t.Fatal(err)
	}
	return secretBase32, code
}

func mustDecodeTOTP(t *testing.T, secretBase32 string) []byte {
	t.Helper()
	secret, err := bootstrap.DecodeTOTPSecret(secretBase32)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestIdentityRecoveryReenrollsTOTPRevokesSessionsAndAudits(t *testing.T) {
	fixture := newIdentityRecoveryFixture(t, nil)
	// A session that predates the reset must not survive it: recovery is also
	// the answer to a compromised credential, not only a forgotten one.
	login := fixture.signIn(t)

	secretBase32, code := recoveryEnrollment(t, fixture.now)
	body, err := json.Marshal(identityRecoveryRequest{
		Email:      "ops@example.com",
		Password:   fixture.newPassword,
		TOTPSecret: secretBase32,
		TOTPCode:   code,
	})
	if err != nil {
		t.Fatal(err)
	}
	reset := performIdentityRecovery(fixture.handler, body, "recover-ops", "operator-recovery-token")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	var result identity.ResetAdministratorCredentialsResult
	if err := json.Unmarshal(reset.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.User.ID != "usr_admin" || !result.TOTPReenrolled || result.SessionsRevoked != 1 {
		t.Fatalf("reset result = %#v", result)
	}
	if strings.Contains(reset.Body.String(), secretBase32) ||
		strings.Contains(reset.Body.String(), fixture.newPassword) {
		t.Fatal("reset response echoed credential material")
	}
	if _, _, err := fixture.manager.Authenticate(t.Context(), "Bearer "+login.AccessToken); err == nil {
		t.Fatal("session issued before the reset still authenticates")
	}

	replacementCode, err := bootstrap.GenerateTOTP(mustDecodeTOTP(t, secretBase32), fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Login(t.Context(), identity.LoginRequest{
		Email:      "ops@example.com",
		Password:   fixture.newPassword,
		TOTPCode:   replacementCode,
		ClientKind: "cli",
	}); err != nil {
		t.Fatalf("login with the recovered credentials: %v", err)
	}
	// The superseded authenticator is genuinely gone.
	supersededCode, err := bootstrap.GenerateTOTP(mustDecodeTOTP(t, fixture.oldTOTP), fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Login(t.Context(), identity.LoginRequest{
		Email:      "ops@example.com",
		Password:   fixture.newPassword,
		TOTPCode:   supersededCode,
		ClientKind: "cli",
	}); err == nil {
		t.Fatal("the superseded authenticator still satisfies the second factor")
	}

	redacted, _ := json.Marshal(struct {
		Email        string `json:"email"`
		UserID       string `json:"user_id"`
		PreserveTOTP bool   `json:"preserve_totp"`
	}{Email: "ops@example.com", UserID: "usr_admin"})
	idempotencyContext := idempotency.WithActor(t.Context(), idempotency.ActorRecoveryOperator)
	idempotencyContext = idempotency.WithTenant(idempotencyContext, idempotency.Tenant(identity.DefaultWorkspaceID))
	stored, err := fixture.idem.Load(
		idempotencyContext,
		idempotency.Key(identityRecoveryIdempotencyKey("recover-ops")),
		idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       identityRecoveryPath,
			BodySHA256: idempotency.BodyHashSHA256(redacted),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fingerprint.BodySHA256 == idempotency.BodyHashSHA256(body) {
		t.Fatal("recovery idempotency persisted a verifier for the credential-bearing body")
	}

	rows, err := fixture.chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 ||
		rows[0].Action != "identity.administrator.credentials.reset" ||
		rows[0].ResourceID != "usr_admin" ||
		rows[0].PolicyDecision != "allowed" {
		t.Fatalf("recovery audit rows = %#v", rows)
	}
	if revoked, ok := rows[0].After["sessions_revoked"].(int); !ok || revoked != 1 {
		t.Fatalf("recovery audit sessions_revoked = %#v", rows[0].After["sessions_revoked"])
	}
}

func TestIdentityRecoveryPreservesTheExistingSecondFactor(t *testing.T) {
	fixture := newIdentityRecoveryFixture(t, nil)
	body, _ := json.Marshal(identityRecoveryRequest{
		Email:        "ops@example.com",
		Password:     fixture.newPassword,
		PreserveTOTP: true,
	})
	reset := performIdentityRecovery(fixture.handler, body, "preserve-totp", "operator-recovery-token")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	var result identity.ResetAdministratorCredentialsResult
	if err := json.Unmarshal(reset.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TOTPReenrolled || !result.User.MFAEnabled {
		t.Fatalf("preserved reset result = %#v", result)
	}
	code, err := bootstrap.GenerateTOTP(mustDecodeTOTP(t, fixture.oldTOTP), fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.Login(t.Context(), identity.LoginRequest{
		Email:      "ops@example.com",
		Password:   fixture.newPassword,
		TOTPCode:   code,
		ClientKind: "cli",
	}); err != nil {
		t.Fatalf("login with the preserved authenticator: %v", err)
	}
	if _, err := fixture.manager.Login(t.Context(), identity.LoginRequest{
		Email:      "ops@example.com",
		Password:   fixture.oldPassword,
		TOTPCode:   code,
		ClientKind: "cli",
	}); err == nil {
		t.Fatal("the superseded password still signs in")
	}
}

func TestIdentityRecoveryRefusesHumanSessions(t *testing.T) {
	fixture := newIdentityRecoveryFixture(t, nil)
	login := fixture.signIn(t)
	secretBase32, code := recoveryEnrollment(t, fixture.now)
	body, _ := json.Marshal(identityRecoveryRequest{
		Email:      "ops@example.com",
		Password:   fixture.newPassword,
		TOTPSecret: secretBase32,
		TOTPCode:   code,
	})
	denied := performIdentityRecovery(fixture.handler, body, "human-session", login.AccessToken)
	if denied.Code != http.StatusForbidden ||
		!strings.Contains(denied.Body.String(), string(failurecodes.AuthOperatorRecoveryRequired)) {
		t.Fatalf("human-session reset status=%d body=%s", denied.Code, denied.Body.String())
	}
	// The refused reset changed nothing: the session and its credential live on.
	if _, _, err := fixture.manager.Authenticate(t.Context(), "Bearer "+login.AccessToken); err != nil {
		t.Fatalf("refused reset disturbed the caller's session: %v", err)
	}
	rows, err := fixture.chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Action, "identity.administrator.credentials.reset") {
			t.Fatalf("refused reset appended %q", row.Action)
		}
	}
}

func TestIdentityRecoveryTOTPDispositionIsExplicit(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	secretBase32, code := recoveryEnrollment(t, now)
	const password = "correct horse battery staple"
	tests := map[string]struct {
		mutate     func(*identity.UserRecord)
		request    identityRecoveryRequest
		wantStatus int
		wantCode   failurecodes.Code
	}{
		"neither disposition": {
			request:    identityRecoveryRequest{Email: "ops@example.com", Password: password},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.ResourceValidationFailed,
		},
		"both dispositions": {
			request:    identityRecoveryRequest{Email: "ops@example.com", Password: password, PreserveTOTP: true, TOTPSecret: secretBase32, TOTPCode: code},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.ResourceValidationFailed,
		},
		"preserve without enrollment": {
			mutate:     withoutTOTPEnrollment,
			request:    identityRecoveryRequest{Email: "ops@example.com", Password: password, PreserveTOTP: true},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.ResourceValidationFailed,
		},
		"unverified enrollment proof": {
			request:    identityRecoveryRequest{Email: "ops@example.com", Password: password, TOTPSecret: secretBase32, TOTPCode: "000000"},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.MFAInvalidCode,
		},
		"short password": {
			request:    identityRecoveryRequest{Email: "ops@example.com", Password: "short", PreserveTOTP: true},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.ResourceValidationFailed,
		},
		"invalid address": {
			request:    identityRecoveryRequest{Email: "not-an-address", Password: password, PreserveTOTP: true},
			wantStatus: http.StatusBadRequest,
			wantCode:   failurecodes.ResourceValidationFailed,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newIdentityRecoveryFixture(t, test.mutate)
			body, _ := json.Marshal(test.request)
			response := performIdentityRecovery(fixture.handler, body, "disposition", "operator-recovery-token")
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantCode != "" && !strings.Contains(response.Body.String(), string(test.wantCode)) {
				t.Fatalf("body=%s, want %s", response.Body.String(), test.wantCode)
			}
			// A refused reset must leave the credential exactly as it was.
			fixture.signIn(t)
		})
	}
}

func TestIdentityRecoveryTargetsOnlyEnabledAdministrators(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	secretBase32, code := recoveryEnrollment(t, now)
	tests := map[string]struct {
		mutate func(*identity.UserRecord)
		email  string
	}{
		"non-administrator": {mutate: func(user *identity.UserRecord) { user.InstanceAdmin = false }, email: "ops@example.com"},
		"disabled":          {mutate: func(user *identity.UserRecord) { user.Disabled = true }, email: "ops@example.com"},
		"soft-deleted":      {mutate: func(user *identity.UserRecord) { user.Deleted = true }, email: "ops@example.com"},
		"unknown address":   {email: "nobody@example.com"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newIdentityRecoveryFixture(t, test.mutate)
			body, _ := json.Marshal(identityRecoveryRequest{
				Email: test.email, Password: fixture.newPassword,
				TOTPSecret: secretBase32, TOTPCode: code,
			})
			response := performIdentityRecovery(fixture.handler, body, "ineligible", "operator-recovery-token")
			if response.Code != http.StatusNotFound ||
				!strings.Contains(response.Body.String(), string(failurecodes.ResourceNotFound)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestIdentityRecoveryReplaysOneStoredResponse(t *testing.T) {
	fixture := newIdentityRecoveryFixture(t, nil)
	secretBase32, code := recoveryEnrollment(t, fixture.now)
	body, _ := json.Marshal(identityRecoveryRequest{
		Email: "ops@example.com", Password: fixture.newPassword,
		TOTPSecret: secretBase32, TOTPCode: code,
	})
	first := performIdentityRecovery(fixture.handler, body, "replay-key", "operator-recovery-token")
	if first.Code != http.StatusOK {
		t.Fatalf("first reset status=%d body=%s", first.Code, first.Body.String())
	}
	replay := performIdentityRecovery(fixture.handler, body, "replay-key", "operator-recovery-token")
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay headers = %#v", replay.Header())
	}
	rows, err := fixture.chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("replayed reset appended %d audit rows, want 1", len(rows))
	}
}

func TestIdentityRecoveryClosedWithoutASharedCredentialAuthority(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store:       identity.NewMemoryStore(),
		KeyProvider: testVariableKeyProvider(t),
		MACKey:      []byte("0123456789abcdef0123456789abcdef"),
		Clock:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	// A separate store is a separate authority: a reset committed there would
	// never affect the credentials this manager verifies.
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		IdentityRecovery: identity.NewMemoryStore(),
		IdempotencyStore: idempotency.NewMemoryStoreWithClock(func() time.Time { return now }),
	})
	body, _ := json.Marshal(identityRecoveryRequest{
		Email: "ops@example.com", Password: "correct horse battery staple", PreserveTOTP: true,
	})
	response := performIdentityRecovery(handler, body, "split-authority", "operator-recovery-token")
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), string(failurecodes.CapabilityUnavailable)) {
		t.Fatalf("split-authority reset status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPostgresIdentityRecoveryCoordinatorRejectsSplitAuthorities(t *testing.T) {
	primaryPool := &pg.Pool{}
	otherPool := &pg.Pool{}
	coordinator := &PostgresIdentityRecoveryCoordinator{
		Pool:        primaryPool,
		Identity:    identity.NewPostgresStore(primaryPool),
		Idempotency: idempotency.NewPostgresStore(otherPool),
		Audit:       audit.NewPostgresChain(primaryPool),
	}
	_, err := coordinator.ResetAdministratorCredentials(t.Context(), IdentityRecoveryCommitRequest{
		Reset: identity.ResetAdministratorCredentialsRequest{UserID: "usr_admin"},
		Key:   "identity-recovery-test-key",
		Fingerprint: idempotency.RequestFingerprint{
			Method:     http.MethodPost,
			Path:       identityRecoveryPath,
			BodySHA256: idempotency.BodyHashSHA256([]byte(`{"email":"ops@example.com"}`)),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "do not share one PostgreSQL pool") {
		t.Fatalf("split-authority recovery error = %v", err)
	}
}
