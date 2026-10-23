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
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

func TestIdentitySessionLifecycleDoesNotGrantLegacyGlobalAPI(t *testing.T) {
	passwordHash, err := bootstrap.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	store := identity.NewMemoryStore(identity.UserRecord{
		ID:                "usr_alice",
		Email:             "alice@example.com",
		DisplayName:       "Alice",
		PasswordHash:      passwordHash,
		SessionGeneration: 1,
	})
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store:  store,
		MACKey: []byte("0123456789abcdef0123456789abcdef"),
		Clock:  func() time.Time { return now },
		Random: func(dst []byte) error {
			for i := range dst {
				dst[i] = byte(i + 1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chain := audit.NewMemoryChain()
	handler := NewRouterWithDataStack(DataStack{
		APIBearerToken:   "operator-recovery-token",
		IdentitySessions: manager,
		AuditChain:       chain,
	})

	login := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, identitySessionsPath, strings.NewReader(`{
		"email":"alice@example.com",
		"password":"correct horse battery staple",
		"client_kind":"dashboard"
	}`))
	handler.ServeHTTP(login, request)
	if login.Code != http.StatusCreated {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var result identity.LoginResult
	if err := json.Unmarshal(login.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" || result.Session.ID == "" {
		t.Fatalf("login result = %#v", result)
	}

	me := authenticatedIdentityRequest(handler, http.MethodGet, identityMePath, result.AccessToken)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"user_id":"usr_alice"`) {
		t.Fatalf("me status=%d body=%s", me.Code, me.Body.String())
	}
	sessions := authenticatedIdentityRequest(handler, http.MethodGet, identitySessionsPath, result.AccessToken)
	if sessions.Code != http.StatusOK || !strings.Contains(sessions.Body.String(), `"current":true`) {
		t.Fatalf("sessions status=%d body=%s", sessions.Code, sessions.Body.String())
	}

	// A valid session-shaped credential is classified as human before any
	// operator-token comparison. Without one shared tenant authority, the
	// product capability fails closed without exposing the global store.
	projects := authenticatedIdentityRequest(handler, http.MethodGet, v1alphaPrefix+"/projects", result.AccessToken)
	if projects.Code != http.StatusServiceUnavailable ||
		!strings.Contains(projects.Body.String(), `"code":"INTERNAL_ERROR"`) ||
		!strings.Contains(projects.Body.String(), "tenant authorization is unavailable") {
		t.Fatalf("session reached legacy global project API: status=%d body=%s", projects.Code, projects.Body.String())
	}

	revoke := authenticatedIdentityRequest(handler, http.MethodDelete, identitySessionsPath+"/"+result.Session.ID, result.AccessToken)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	after := authenticatedIdentityRequest(handler, http.MethodGet, identityMePath, result.AccessToken)
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d body=%s", after.Code, after.Body.String())
	}
	rows, err := chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 ||
		rows[0].Action != "identity.login.succeeded" ||
		rows[0].ActorID != "usr_alice" ||
		rows[1].Action != "identity.session.revoke" ||
		rows[1].ActorID != "usr_alice" {
		t.Fatalf("identity audit rows = %#v", rows)
	}
}

func TestIdentityLoginUsesGenericCredentialFailure(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	manager, _ := identity.NewManager(identity.ManagerConfig{
		Store: identity.NewMemoryStore(identity.UserRecord{
			ID: "usr_alice", Email: "alice@example.com", PasswordHash: passwordHash,
			SessionGeneration: 1,
		}),
		MACKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	chain := audit.NewMemoryChain()
	handler := NewRouterWithDataStack(DataStack{IdentitySessions: manager, AuditChain: chain})
	for _, body := range []string{
		`{"email":"alice@example.com","password":"incorrect password"}`,
		`{"email":"missing@example.com","password":"incorrect password"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, identitySessionsPath, strings.NewReader(body)))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), `"code":"AUTH_CREDENTIALS_INVALID"`) {
			t.Fatalf("body=%s", response.Body.String())
		}
	}
	rows, err := chain.Read(t.Context(), audit.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("login-denied audit rows=%d, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Action != "identity.login.denied" || row.ActorType != "system" ||
			row.ActorID != "" || row.ReasonCode != string(failurecodes.AuthCredentialsInvalid) ||
			row.Attrs["actor_class"] != "anonymous" {
			t.Fatalf("login-denied audit row = %#v", row)
		}
	}
}

func TestIdentityLoginRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	manager, _ := identity.NewManager(identity.ManagerConfig{
		Store: identity.NewMemoryStore(identity.UserRecord{
			ID: "usr_alice", Email: "alice@example.com", PasswordHash: passwordHash,
			SessionGeneration: 1,
		}),
		MACKey: []byte("0123456789abcdef0123456789abcdef"),
	})
	handler := NewRouterWithDataStack(DataStack{IdentitySessions: manager})
	tests := map[string]struct {
		body       string
		wantStatus int
	}{
		"duplicate member":  {`{"email":"missing@example.com","email":"alice@example.com","password":"correct horse battery staple"}`, http.StatusBadRequest},
		"trailing document": {`{"email":"alice@example.com","password":"correct horse battery staple"} {}`, http.StatusBadRequest},
		"oversized":         {`{"email":"alice@example.com","password":"` + strings.Repeat("x", int(sensitiveJSONBodyLimitBytes)) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, identitySessionsPath, strings.NewReader(test.body)))
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

func TestIdentityLoginReturnsRetryAfterWhenRateLimited(t *testing.T) {
	passwordHash, _ := bootstrap.HashPassword("correct horse battery staple")
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager, err := identity.NewManager(identity.ManagerConfig{
		Store: identity.NewMemoryStore(identity.UserRecord{
			ID: "usr_alice", Email: "alice@example.com", PasswordHash: passwordHash,
			SessionGeneration: 1,
		}),
		MACKey:      []byte("0123456789abcdef0123456789abcdef"),
		Clock:       func() time.Time { return now },
		LoginLimit:  1,
		LoginWindow: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRouterWithDataStack(DataStack{IdentitySessions: manager})
	for attempt, want := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, identitySessionsPath, strings.NewReader(
			`{"email":"alice@example.com","password":"wrong password value"}`,
		))
		request.RemoteAddr = "192.0.2.10:4242"
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("attempt %d status=%d body=%s, want %d", attempt+1, response.Code, response.Body.String(), want)
		}
		if want == http.StatusTooManyRequests {
			if response.Header().Get("Retry-After") == "" {
				t.Fatal("rate-limited response omitted Retry-After")
			}
			if !strings.Contains(response.Body.String(), `"code":"AUTH_RATE_LIMITED"`) {
				t.Fatalf("rate-limited body=%s", response.Body.String())
			}
		}
	}
}

func authenticatedIdentityRequest(handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(response, request)
	return response
}
