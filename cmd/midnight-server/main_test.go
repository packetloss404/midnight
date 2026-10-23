package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/midnight/midnight/internal/agent/conversation"
	"github.com/midnight/midnight/internal/api"
	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/automationtoken"
	"github.com/midnight/midnight/internal/db/pg"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/internal/variableintent"
)

func TestParseServeConfigDefaultsAndFlags(t *testing.T) {
	clearServerEnv(t)

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	}
	if cfg.Listen != defaultListenAddr {
		t.Fatalf("Listen = %q, want %q", cfg.Listen, defaultListenAddr)
	}
	if cfg.Storage != defaultStorageMode {
		t.Fatalf("Storage = %q, want %q", cfg.Storage, defaultStorageMode)
	}
	if cfg.AutomationTokenKeyPath != automationtoken.DefaultMACKeyPath {
		t.Fatalf(
			"AutomationTokenKeyPath = %q, want %q",
			cfg.AutomationTokenKeyPath,
			automationtoken.DefaultMACKeyPath,
		)
	}
	if cfg.InvitationKeyPath != identity.DefaultInvitationMACKeyPath {
		t.Fatalf(
			"InvitationKeyPath = %q, want %q",
			cfg.InvitationKeyPath,
			identity.DefaultInvitationMACKeyPath,
		)
	}
	if cfg.VariableIntentKeyPath != variableintent.DefaultMACKeyPath {
		t.Fatalf("VariableIntentKeyPath = %q, want %q", cfg.VariableIntentKeyPath, variableintent.DefaultMACKeyPath)
	}

	cfg, err = parseServeConfig([]string{"--listen", "127.0.0.1:9090", "--storage", "memory"})
	if err != nil {
		t.Fatalf("parseServeConfig(flags) error = %v", err)
	}
	if cfg.Listen != "127.0.0.1:9090" || cfg.Storage != "memory" {
		t.Fatalf("config = %#v, want listen/storage overrides", cfg)
	}

	cfg, err = parseServeConfig([]string{
		"--api-bearer-token", "flag-token",
		"--automation-token-key", "/run/secrets/automation-token.key",
		"--invitation-key", "/run/secrets/invitation.key",
		"--variable-intent-key", "/run/secrets/variable-intent.key",
		"--log-dir", filepath.Join(t.TempDir(), "logs"),
		"--log-pattern", "*.jsonl",
		"--traefik-access-log", "/var/log/traefik/access.jsonl",
	})
	if err != nil {
		t.Fatalf("parseServeConfig(api token flag) error = %v", err)
	}
	if cfg.APIBearerToken != "flag-token" {
		t.Fatalf("APIBearerToken = %q, want flag-token", cfg.APIBearerToken)
	}
	if cfg.AutomationTokenKeyPath != "/run/secrets/automation-token.key" {
		t.Fatalf("AutomationTokenKeyPath = %q, want flag override", cfg.AutomationTokenKeyPath)
	}
	if cfg.InvitationKeyPath != "/run/secrets/invitation.key" {
		t.Fatalf("InvitationKeyPath = %q, want flag override", cfg.InvitationKeyPath)
	}
	if cfg.VariableIntentKeyPath != "/run/secrets/variable-intent.key" {
		t.Fatalf("VariableIntentKeyPath = %q, want flag override", cfg.VariableIntentKeyPath)
	}
	if cfg.LogDir == "" || cfg.LogPattern != "*.jsonl" {
		t.Fatalf("log config = %q/%q, want flag overrides", cfg.LogDir, cfg.LogPattern)
	}
	if cfg.TraefikAccessLog != "/var/log/traefik/access.jsonl" {
		t.Fatalf("TraefikAccessLog = %q, want flag override", cfg.TraefikAccessLog)
	}
}

func TestParseServeConfigUsesEnvironment(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("MIDNIGHT_SERVER_ADDR", "127.0.0.1:7070")
	t.Setenv("MIDNIGHT_STORAGE", "postgres")
	t.Setenv("MIDNIGHT_DB_DSN_FILE", filepath.Join(t.TempDir(), "db.dsn"))
	automationTokenKeyPath := filepath.Join(t.TempDir(), "automation-token.key")
	t.Setenv(automationtoken.EnvMACKeyPath, automationTokenKeyPath)
	invitationKeyPath := filepath.Join(t.TempDir(), "invitation.key")
	t.Setenv(identity.EnvInvitationMACKeyPath, invitationKeyPath)
	variableIntentKeyPath := filepath.Join(t.TempDir(), "variable-intent.key")
	t.Setenv(variableintent.EnvMACKeyPath, variableIntentKeyPath)
	t.Setenv("MIDNIGHT_API_BEARER_TOKEN", "env-token")
	t.Setenv("MIDNIGHT_LOG_DIR", filepath.Join(t.TempDir(), "logs"))
	t.Setenv("MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH", "/var/log/midnight/traefik/access.jsonl")

	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	}
	if cfg.Listen != "127.0.0.1:7070" {
		t.Fatalf("Listen = %q", cfg.Listen)
	}
	if cfg.Storage != "postgres" {
		t.Fatalf("Storage = %q", cfg.Storage)
	}
	if cfg.PostgresDSNFile == "" {
		t.Fatal("PostgresDSNFile is empty")
	}
	if cfg.APIBearerToken != "env-token" {
		t.Fatalf("APIBearerToken = %q, want env-token", cfg.APIBearerToken)
	}
	if cfg.AutomationTokenKeyPath != automationTokenKeyPath {
		t.Fatalf("AutomationTokenKeyPath = %q, want %q", cfg.AutomationTokenKeyPath, automationTokenKeyPath)
	}
	if cfg.InvitationKeyPath != invitationKeyPath {
		t.Fatalf("InvitationKeyPath = %q, want %q", cfg.InvitationKeyPath, invitationKeyPath)
	}
	if cfg.VariableIntentKeyPath != variableIntentKeyPath {
		t.Fatalf("VariableIntentKeyPath = %q, want %q", cfg.VariableIntentKeyPath, variableIntentKeyPath)
	}
	if cfg.LogDir == "" || cfg.LogPattern != defaultLogPattern {
		t.Fatalf("log config = %q/%q, want env log dir and default pattern", cfg.LogDir, cfg.LogPattern)
	}
	if cfg.TraefikAccessLog != "/var/log/midnight/traefik/access.jsonl" {
		t.Fatalf("TraefikAccessLog = %q, want environment value", cfg.TraefikAccessLog)
	}
}

func TestParseServeConfigRejectsUnsupportedStorage(t *testing.T) {
	clearServerEnv(t)
	if _, err := parseServeConfig([]string{"--storage", "sqlite"}); err == nil {
		t.Fatal("parseServeConfig() error = nil, want unsupported storage error")
	}
}

func TestParseServeConfigRejectsUnsafeSourceArtifactLease(t *testing.T) {
	clearServerEnv(t)
	t.Setenv(sourceartifact.EnvUploadLease, "9m59s")
	if _, err := parseServeConfig(nil); err == nil ||
		!strings.Contains(err.Error(), "upload lease") {
		t.Fatalf("parseServeConfig error=%v, want strict source upload lease failure", err)
	}
}

func TestParseServeConfigRejectsScopedMidnightRecoveryClasses(t *testing.T) {
	for _, token := range []string{
		"midn_user_recovery-secret",
		"midn_sa_recovery-secret",
		"midn_agent_recovery-secret",
		"midn_mcp_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			clearServerEnv(t)
			_, err := parseServeConfig([]string{
				"--storage", "memory",
				"--api-bearer-token", token,
			})
			if !errors.Is(err, auth.ErrOperatorRecoveryTokenClass) {
				t.Fatalf("parseServeConfig(%q) = %v", token, err)
			}
			if err != nil && strings.Contains(err.Error(), "recovery-secret") {
				t.Fatalf("configuration error leaked bearer material: %v", err)
			}
		})
	}
}

func TestParseServeConfigPreservesLegacyAndAdminRecoveryTokens(t *testing.T) {
	for _, token := range []string{
		"legacy-operator-secret",
		"midn_admin_recovery-secret",
	} {
		t.Run(token, func(t *testing.T) {
			clearServerEnv(t)
			cfg, err := parseServeConfig([]string{
				"--storage", "memory",
				"--api-bearer-token", "  " + token + "  ",
			})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.APIBearerToken != token {
				t.Fatalf("APIBearerToken = %q, want %q", cfg.APIBearerToken, token)
			}
		})
	}
}

func TestBuildServerHandlerRejectsBypassedScopedRecoveryConfiguration(t *testing.T) {
	clearServerEnv(t)
	_, cleanup, _, err := buildServerHandler(context.Background(), serveConfig{
		Storage:        "memory",
		APIBearerToken: "midn_sa_recovery-secret",
	})
	if cleanup != nil {
		cleanup()
		t.Fatal("invalid recovery configuration allocated cleanup resources")
	}
	if !errors.Is(err, auth.ErrOperatorRecoveryTokenClass) {
		t.Fatalf("buildServerHandler() error = %v", err)
	}
}

func TestProductionAgentAuthenticationFailsClosedWithoutKeyring(t *testing.T) {
	_, err := configureAgentControlCredentials(api.DataStack{}, serveConfig{}, true)
	if err == nil || !strings.Contains(err.Error(), "agent authentication requires") {
		t.Fatalf("configureAgentControlCredentials() error = %v, want missing production keyring", err)
	}
}

func TestProductionAgentAuthenticationLoadsScopedKeyring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"agents":{"agent-a":["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stack, err := configureAgentControlCredentials(api.DataStack{}, serveConfig{AgentCredentialsFile: path}, true)
	if err != nil {
		t.Fatal(err)
	}
	if stack.AgentCredentials == nil || len(stack.AgentHMACSecret) != 0 {
		t.Fatalf("agent credentials = %#v legacy secret bytes=%d", stack.AgentCredentials, len(stack.AgentHMACSecret))
	}
}

func TestListenAddressCompatibility(t *testing.T) {
	clearServerEnv(t)
	tests := []struct {
		args []string
		want string
		ok   bool
	}{
		{args: []string{"serve"}, want: defaultListenAddr, ok: true},
		{args: []string{"serve", "--listen", "127.0.0.1:9090"}, want: "127.0.0.1:9090", ok: true},
		{args: []string{"--listen", "127.0.0.1:9091"}, want: "127.0.0.1:9091", ok: true},
		{args: []string{"version"}, ok: false},
	}
	for _, tt := range tests {
		got, ok := listenAddress(tt.args)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("listenAddress(%v) = (%q,%t), want (%q,%t)", tt.args, got, ok, tt.want, tt.ok)
		}
	}
}

// storage=auto must not degrade to ephemeral in-memory storage on a host whose
// DSN is missing: that boots a server that looks healthy while losing every
// write. It refuses to start unless the operator opted in.
func TestBuildServerHandlerAutoRefusesMemoryWithoutDSN(t *testing.T) {
	clearServerEnv(t)
	cfg := serveConfig{
		Storage:         "auto",
		PostgresDSNFile: filepath.Join(t.TempDir(), "missing.dsn"),
	}
	handler, cleanup, storage, err := buildServerHandler(context.Background(), cfg)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatalf("buildServerHandler() error = nil (storage=%q, handler=%v), want refusal", storage, handler != nil)
	}
	if !errors.Is(err, pg.ErrDSNUnavailable) {
		t.Fatalf("buildServerHandler() error = %v, want it to wrap pg.ErrDSNUnavailable", err)
	}
	for _, want := range []string{"--storage=memory", envAllowMemoryFallback} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("buildServerHandler() error = %v, want it to name %q", err, want)
		}
	}
}

// The opt-in keeps local smoke runs (and the acceptance runner's explicit
// --storage=memory) working.
func TestBuildServerHandlerAutoFallsBackToMemoryWhenOptedIn(t *testing.T) {
	clearServerEnv(t)
	cfg := serveConfig{
		Storage:             "auto",
		PostgresDSNFile:     filepath.Join(t.TempDir(), "missing.dsn"),
		AllowMemoryFallback: true,
	}
	handler, cleanup, storage, err := buildServerHandler(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildServerHandler() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup is non-nil for memory fallback")
	}
	if storage != "memory" {
		t.Fatalf("storage = %q, want memory", storage)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/compat", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("compat status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestParseServeConfigMemoryFallbackOptIn(t *testing.T) {
	clearServerEnv(t)
	cfg, err := parseServeConfig(nil)
	if err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	}
	if cfg.AllowMemoryFallback {
		t.Fatal("AllowMemoryFallback defaults to true; the auto fallback must be opt-in")
	}
	if cfg, err = parseServeConfig([]string{"--allow-memory-fallback"}); err != nil {
		t.Fatalf("parseServeConfig(--allow-memory-fallback) error = %v", err)
	} else if !cfg.AllowMemoryFallback {
		t.Fatal("--allow-memory-fallback did not set AllowMemoryFallback")
	}
	t.Setenv(envAllowMemoryFallback, "1")
	if cfg, err = parseServeConfig(nil); err != nil {
		t.Fatalf("parseServeConfig() error = %v", err)
	} else if !cfg.AllowMemoryFallback {
		t.Fatalf("%s=1 did not set AllowMemoryFallback", envAllowMemoryFallback)
	}
}

func TestBuildServerHandlerMemoryHonorsAPIBearerToken(t *testing.T) {
	clearServerEnv(t)
	cfg := serveConfig{
		Storage:        "memory",
		APIBearerToken: "serve-token",
	}
	handler, cleanup, storage, err := buildServerHandler(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildServerHandler() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup is non-nil for memory storage")
	}
	if storage != "memory" {
		t.Fatalf("storage = %q, want memory", storage)
	}

	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("protected status = %d body=%s, want %d", protected.Code, protected.Body.String(), http.StatusUnauthorized)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1alpha/projects", nil)
	req.Header.Set("Authorization", "Bearer serve-token")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, req)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s, want %d", authorized.Code, authorized.Body.String(), http.StatusOK)
	}

	public := httptest.NewRecorder()
	handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/v1alpha/compat", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("public status = %d body=%s, want %d", public.Code, public.Body.String(), http.StatusOK)
	}
}

// TestWireAgentWithoutAPIKey asserts that wireAgent leaves AgentCaller
// nil and populates every other auth seam when ANTHROPIC_API_KEY is
// unset. The server must continue to boot — the warning path is the
// only thing that should fire.
func TestWireAgentWithoutAPIKey(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	stack := wireAgent(api.DataStack{}, nil)

	if stack.AgentCaller != nil {
		t.Fatalf("AgentCaller = %T, want nil when ANTHROPIC_API_KEY is unset", stack.AgentCaller)
	}
	if stack.ConversationStore == nil {
		t.Fatal("ConversationStore is nil; want in-memory store when pool is nil")
	}
	if _, ok := stack.ConversationStore.(*conversation.MemoryStore); !ok {
		t.Fatalf("ConversationStore = %T, want *conversation.MemoryStore", stack.ConversationStore)
	}
	if stack.AgentAuthorizer == nil {
		t.Fatal("AgentAuthorizer is nil")
	}
	if stack.AgentMutationGate == nil {
		t.Fatal("AgentMutationGate is nil")
	}
	if stack.AgentBudget == nil {
		t.Fatal("AgentBudget is nil")
	}
	if stack.AgentRevoker == nil {
		t.Fatal("AgentRevoker is nil")
	}
	// AuditChain is nil on the bare stack used here, so the sink
	// should be left nil — the router's nil-default code path
	// covers the live case.
	if stack.AgentAuditSink != nil {
		t.Fatalf("AgentAuditSink = %T, want nil when AuditChain is unset", stack.AgentAuditSink)
	}
	if stack.AgentPrincipalFromRequest == nil {
		t.Fatal("AgentPrincipalFromRequest is nil")
	}
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Midnight-Actor-Id", "spoofed-actor")
	req = req.WithContext(idempotency.WithActor(req.Context(), idempotency.ActorRecoveryOperator))
	if got := stack.AgentPrincipalFromRequest(req); got != string(idempotency.ActorRecoveryOperator) {
		t.Fatalf("AgentPrincipalFromRequest = %q, want authenticated context actor %q", got, idempotency.ActorRecoveryOperator)
	}
}

func TestWireAgentIgnoresSpoofedActorHeaderForConversationAndCursorScope(t *testing.T) {
	clearServerEnv(t)
	stack := wireAgent(api.DataStack{
		APIBearerToken:    "serve-token",
		ConversationStore: conversation.NewMemoryStore(nil),
		CursorCodec:       api.NewCursorCodec([]byte("conversation-cursor-test-key")),
	}, nil)
	handler := api.NewRouterWithDataStack(stack)

	request := func(method, target, body, spoofedActor string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer serve-token")
		req.Header.Set("X-Midnight-Actor-Id", spoofedActor)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	for _, spoofedActor := range []string{"victim-a", "victim-b"} {
		response := request(http.MethodPost, "/api/v1alpha/agent/conversations", `{}`, spoofedActor)
		if response.Code != http.StatusCreated {
			t.Fatalf("create with spoofed actor %q status=%d body=%s", spoofedActor, response.Code, response.Body.String())
		}
		var created conversation.Conversation
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		if created.ActorID != string(idempotency.ActorRecoveryOperator) {
			t.Fatalf("created actor_id=%q, want authenticated actor %q", created.ActorID, idempotency.ActorRecoveryOperator)
		}
	}

	firstPage := request(http.MethodGet, "/api/v1alpha/agent/conversations?limit=1", "", "victim-a")
	if firstPage.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", firstPage.Code, firstPage.Body.String())
	}
	var page struct {
		Page struct {
			NextCursor string `json:"next_cursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(firstPage.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page.NextCursor == "" {
		t.Fatal("first page did not return a cursor")
	}
	secondPage := request(
		http.MethodGet,
		"/api/v1alpha/agent/conversations?limit=1&cursor="+page.Page.NextCursor,
		"",
		"victim-b",
	)
	if secondPage.Code != http.StatusOK {
		t.Fatalf("cursor reuse under a different spoofed header status=%d body=%s", secondPage.Code, secondPage.Body.String())
	}
}

func TestWireAgentPreservesExplicitPrincipalExtractor(t *testing.T) {
	custom := func(*http.Request) string { return "embedder-principal" }
	stack := wireAgent(api.DataStack{AgentPrincipalFromRequest: custom}, nil)
	if got := stack.AgentPrincipalFromRequest(httptest.NewRequest(http.MethodGet, "/", nil)); got != "embedder-principal" {
		t.Fatalf("custom principal extractor returned %q", got)
	}
}

// TestWireAgentWithAPIKey asserts that wireAgent constructs a non-nil
// Caller when ANTHROPIC_API_KEY is set. The placeholder value avoids
// any real Anthropic API traffic — the assertion fires before a request
// is ever issued.
func TestWireAgentWithAPIKey(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-key-do-not-call")

	stack := wireAgent(api.DataStack{}, nil)

	if stack.AgentCaller == nil {
		t.Fatal("AgentCaller is nil; want non-nil Caller when ANTHROPIC_API_KEY is set")
	}
	if stack.ConversationStore == nil {
		t.Fatal("ConversationStore is nil; want in-memory store when pool is nil")
	}
}

func TestWireAgentWithOpenAIProvider(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("MIDNIGHT_AGENT_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "test-key-do-not-call")

	stack := wireAgent(api.DataStack{}, nil)

	if stack.AgentCaller == nil {
		t.Fatal("AgentCaller is nil; want non-nil Caller when OPENAI_API_KEY is set")
	}
	if stack.ConversationStore == nil {
		t.Fatal("ConversationStore is nil; want in-memory store when pool is nil")
	}
}

func TestWireAgentWithMiniMaxProvider(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("MIDNIGHT_AGENT_PROVIDER", "minimax")
	t.Setenv("MINIMAX_API_KEY", "test-key-do-not-call")

	stack := wireAgent(api.DataStack{}, nil)

	if stack.AgentCaller == nil {
		t.Fatal("AgentCaller is nil; want non-nil Caller when MINIMAX_API_KEY is set")
	}
	if stack.ConversationStore == nil {
		t.Fatal("ConversationStore is nil; want in-memory store when pool is nil")
	}
}

// TestBuildServerHandlerWithoutAPIKey boots the full handler in memory
// mode without an Anthropic key. The handler must come up non-nil and
// the chat panel endpoint must surface the expected "caller is not
// configured" sentinel rather than panicking.
func TestBuildServerHandlerWithoutAPIKey(t *testing.T) {
	clearServerEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg := serveConfig{Storage: "memory"}
	handler, cleanup, storage, err := buildServerHandler(context.Background(), cfg)
	if err != nil {
		t.Fatalf("buildServerHandler() error = %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup is non-nil for memory storage")
	}
	if storage != "memory" {
		t.Fatalf("storage = %q, want memory", storage)
	}
	if handler == nil {
		t.Fatal("handler is nil")
	}

	// Create a conversation, then POST /messages and confirm the
	// caller-not-configured guard fires (proving the chat path is
	// wired through to conversation.Service.Send and the missing
	// caller is the only thing preventing a real turn).
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, api.AgentConversationsPrefix, strings.NewReader(`{"title":"boot"}`))
	createReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST conversations status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode conversation create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("created conversation id is empty: %s", createRec.Body.String())
	}

	sendRec := httptest.NewRecorder()
	sendReq := httptest.NewRequest(http.MethodPost,
		api.AgentConversationsPrefix+"/"+created.ID+"/messages",
		strings.NewReader(`{"content":"hello"}`))
	sendReq.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(sendRec, sendReq)
	if sendRec.Code < 400 {
		t.Fatalf("POST /messages status=%d body=%s, want 4xx/5xx", sendRec.Code, sendRec.Body.String())
	}
	if !strings.Contains(sendRec.Body.String(), "caller is not configured") {
		t.Fatalf("POST /messages body does not mention caller-not-configured: %s", sendRec.Body.String())
	}
}

func clearServerEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"MIDNIGHT_SERVER_ADDR",
		"MIDNIGHT_LISTEN_ADDR",
		"MIDNIGHT_STORAGE",
		envAllowMemoryFallback,
		"MIDNIGHT_DB_DSN",
		"MIDNIGHT_DATABASE_URL",
		"MIDNIGHT_DB_DSN_FILE",
		"MIDNIGHT_CURSOR_KEY_PATH",
		"MIDNIGHT_AUTOMATION_TOKEN_KEY_PATH", // not-a-failure-code
		"MIDNIGHT_INVITATION_KEY_PATH",       // not-a-failure-code
		"MIDNIGHT_VARIABLE_INTENT_KEY_PATH",  // not-a-failure-code
		"MIDNIGHT_AGENT_HMAC_SECRET",
		"MIDNIGHT_AGENT_CREDENTIALS_FILE", // not-a-failure-code
		"MIDNIGHT_AGENT_AUTH_DEVELOPMENT", // not-a-failure-code
		"MIDNIGHT_API_BEARER_TOKEN",
		"MIDNIGHT_LOG_DIR",
		"MIDNIGHT_LOG_PATTERN",
		"MIDNIGHT_TRAEFIK_ACCESS_LOG_PATH",
		"MIDNIGHT_AGENT_PROVIDER",
		"ANTHROPIC_API_KEY",
		"MIDNIGHT_AGENT_CHAT_MODEL",
		"OPENAI_API_KEY",
		"MIDNIGHT_OPENAI_BASE_URL",
		"MIDNIGHT_OPENAI_CHAT_MODEL",
		"MINIMAX_API_KEY",
		"MIDNIGHT_MINIMAX_BASE_URL",
		"MIDNIGHT_MINIMAX_CHAT_MODEL",
		"MIDNIGHT_AGENT_ALLOW_MUTATIONS",
		"MIDNIGHT_AGENT_CONVERSATION_TOKEN_CAP",
		"MIDNIGHT_AGENT_CONVERSATION_COST_CENTS",
		"MIDNIGHT_AGENT_HOST_DAILY_COST_CENTS",
		sourceartifact.EnvWorkspaceQuotaBytes,
		sourceartifact.EnvWorkspaceInflightBytes,
		sourceartifact.EnvWorkspaceInflightCount,
		sourceartifact.EnvGlobalInflightBytes,
		sourceartifact.EnvGlobalInflightCount,
		sourceartifact.EnvUploadLease,
		sourceartifact.EnvOrphanChargeRetention,
		sourceartifact.EnvBlobRetention,
		sourceartifact.EnvQuarantineRetention,
		sourceartifact.EnvGCInterval,
		sourceartifact.EnvGCTimeout,
	} {
		t.Setenv(name, "")
	}
}

func TestWireAgentMountsMCPRegistryAndChatModel(t *testing.T) {
	t.Setenv("MIDNIGHT_AGENT_PROVIDER", "anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("MIDNIGHT_AGENT_CHAT_MODEL", "claude-sonnet-5")
	stack := wireAgent(api.DataStack{}, nil)
	if stack.MCPRegistry == nil {
		t.Fatal("wireAgent must expose the tool registry so /mcp/v1 is mounted")
	}
	if stack.AgentChatModel != "claude-sonnet-5" {
		t.Fatalf("AgentChatModel = %q, want env override", stack.AgentChatModel)
	}
}
