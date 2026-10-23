package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/auth"
	"github.com/midnight/midnight/internal/bootstrap"
	"github.com/midnight/midnight/internal/secrets"
)

const (
	SessionTokenPrefix     = "midn_user_"
	DefaultSessionTTL      = 24 * time.Hour
	DefaultSessionIdleTTL  = 8 * time.Hour
	MinSessionMACKeyBytes  = 32
	DefaultLoginWindow     = 15 * time.Minute
	DefaultLoginLimit      = 10
	loginNetworkMultiplier = 10
	DefaultMaxSessions     = 20
)

var (
	ErrIdentityNotFound   = errors.New("identity record not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrMFARequired        = errors.New("multi-factor authentication required")
	ErrMFAInvalid         = errors.New("multi-factor authentication failed")
	ErrSessionInvalid     = errors.New("session is invalid, expired, or revoked")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionForbidden   = errors.New("session does not belong to the principal")
	ErrLoginRateLimited   = errors.New("login rate limited")
)

type LoginRateLimitError struct {
	RetryAfter time.Duration
}

func (e *LoginRateLimitError) Error() string { return ErrLoginRateLimited.Error() }
func (e *LoginRateLimitError) Unwrap() error { return ErrLoginRateLimited }

// UserRecord is the private credential-bearing representation consumed by the
// session manager. Handlers must expose User, never this type.
type UserRecord struct {
	ID                 string
	Email              string
	DisplayName        string
	InstanceAdmin      bool
	PasswordHash       string
	TOTPSecretEnvelope []byte
	TOTPEnrolledAt     time.Time
	MustRotatePassword bool
	SessionGeneration  int64
	Disabled           bool
	Deleted            bool
}

// User is the non-secret representation returned by identity endpoints.
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	DisplayName   string    `json:"display_name"`
	InstanceAdmin bool      `json:"instance_admin"`
	MFAEnabled    bool      `json:"mfa_enabled"`
	LastLoginAt   time.Time `json:"last_login_at,omitempty"`
}

// Session is durable metadata. Raw bearer material is never stored here.
type Session struct {
	ID                string     `json:"id"`
	UserID            string     `json:"user_id"`
	TokenHash         []byte     `json:"-"`
	TokenPrefix       string     `json:"token_prefix"`
	ClientKind        string     `json:"client_kind"`
	SessionGeneration int64      `json:"-"`
	UserAgentHash     string     `json:"-"`
	CreatedAt         time.Time  `json:"created_at"`
	LastSeenAt        time.Time  `json:"last_seen_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	IdleExpiresAt     time.Time  `json:"idle_expires_at"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	RevokedReason     string     `json:"revoked_reason,omitempty"`
	Current           bool       `json:"current"`
}

// Principal is immutable verified identity. Actor attribution and
// authorization must derive from this value rather than request headers.
type Principal struct {
	Kind          string `json:"kind"`
	UserID        string `json:"user_id"`
	SessionID     string `json:"session_id"`
	AuthMethod    string `json:"auth_method"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	InstanceAdmin bool   `json:"instance_admin"`
}

// LoginRequest contains local-account credentials and client metadata.
type LoginRequest struct {
	Email      string
	Password   string
	TOTPCode   string
	ClientKind string
	UserAgent  string
	RemoteAddr string
}

// LoginResult returns raw bearer material exactly once.
type LoginResult struct {
	AccessToken string  `json:"access_token"`
	TokenType   string  `json:"token_type"`
	ExpiresIn   int64   `json:"expires_in"`
	User        User    `json:"user"`
	Session     Session `json:"session"`
}

// Store is the durable identity/session boundary. AuthenticateSession must
// validate and touch a session atomically so revoke/expiry races fail closed.
type Store interface {
	UserByEmail(context.Context, string) (UserRecord, error)
	InsertSession(context.Context, UserRecord, Session, time.Time, int) error
	AuthenticateSession(context.Context, []byte, time.Time, time.Time) (UserRecord, Session, error)
	ListSessions(context.Context, string) ([]Session, error)
	RevokeSession(context.Context, string, string, string, time.Time) error
	RevokeAllSessions(context.Context, string, string, time.Time) error
	ConsumeLoginAttempts(context.Context, [][]byte, time.Time, time.Duration, int) (time.Duration, error)
	ClearLoginAttempts(context.Context, [][]byte) error
}

// Manager owns credential verification and opaque session-token lifecycle.
type Manager struct {
	store       Store
	keys        secrets.KeyProvider
	macKey      []byte
	now         func() time.Time
	random      func([]byte) error
	ttl         time.Duration
	idleTTL     time.Duration
	loginWindow time.Duration
	loginLimit  int
	maxSessions int
}

type ManagerConfig struct {
	Store       Store
	KeyProvider secrets.KeyProvider
	MACKey      []byte
	Clock       func() time.Time
	Random      func([]byte) error
	TTL         time.Duration
	IdleTTL     time.Duration
	LoginWindow time.Duration
	LoginLimit  int
	MaxSessions int
}

func NewManager(cfg ManagerConfig) (*Manager, error) {
	if cfg.Store == nil {
		return nil, errors.New("identity session manager: store is required")
	}
	if len(cfg.MACKey) < MinSessionMACKeyBytes {
		return nil, fmt.Errorf("identity session manager: MAC key is %d bytes, want at least %d", len(cfg.MACKey), MinSessionMACKeyBytes)
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
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultSessionTTL
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = DefaultSessionIdleTTL
	}
	if cfg.IdleTTL > cfg.TTL {
		return nil, errors.New("identity session manager: idle TTL cannot exceed absolute TTL")
	}
	if cfg.LoginWindow <= 0 {
		cfg.LoginWindow = DefaultLoginWindow
	}
	if cfg.LoginLimit <= 0 {
		cfg.LoginLimit = DefaultLoginLimit
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	return &Manager{
		store:       cfg.Store,
		keys:        cfg.KeyProvider,
		macKey:      append([]byte(nil), cfg.MACKey...),
		now:         cfg.Clock,
		random:      cfg.Random,
		ttl:         cfg.TTL,
		idleTTL:     cfg.IdleTTL,
		loginWindow: cfg.LoginWindow,
		loginLimit:  cfg.LoginLimit,
		maxSessions: cfg.MaxSessions,
	}, nil
}

// Login verifies a local password and enrolled TOTP, then persists a hashed,
// expiring session. All invalid account/password cases deliberately collapse
// to ErrInvalidCredentials.
func (m *Manager) Login(ctx context.Context, request LoginRequest) (LoginResult, error) {
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if email == "" || request.Password == "" {
		return LoginResult{}, ErrInvalidCredentials
	}
	now := m.now().UTC()
	accountBucket := m.loginBucketMAC("account", email)
	networkBucket := m.loginBucketMAC("network", normalizeRemoteHost(request.RemoteAddr))
	retryAfter, err := m.store.ConsumeLoginAttempts(ctx, [][]byte{accountBucket}, now, m.loginWindow, m.loginLimit)
	if err != nil {
		return LoginResult{}, err
	}
	if retryAfter > 0 {
		return LoginResult{}, &LoginRateLimitError{RetryAfter: retryAfter}
	}
	maxInt := int(^uint(0) >> 1)
	networkLimit := maxInt
	if m.loginLimit <= maxInt/loginNetworkMultiplier {
		networkLimit = m.loginLimit * loginNetworkMultiplier
	}
	retryAfter, err = m.store.ConsumeLoginAttempts(ctx, [][]byte{networkBucket}, now, m.loginWindow, networkLimit)
	if err != nil {
		return LoginResult{}, err
	}
	if retryAfter > 0 {
		return LoginResult{}, &LoginRateLimitError{RetryAfter: retryAfter}
	}
	user, err := m.store.UserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			_ = bootstrap.VerifyPassword(request.Password, dummyPasswordHash)
			return LoginResult{}, ErrInvalidCredentials
		}
		return LoginResult{}, err
	}
	if user.Disabled || user.Deleted || user.PasswordHash == "" || user.MustRotatePassword {
		_ = bootstrap.VerifyPassword(request.Password, dummyPasswordHash)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := bootstrap.VerifyPassword(request.Password, user.PasswordHash); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if !user.TOTPEnrolledAt.IsZero() {
		if strings.TrimSpace(request.TOTPCode) == "" {
			return LoginResult{}, ErrMFARequired
		}
		secret, err := m.openTOTP(ctx, user)
		if err != nil {
			return LoginResult{}, fmt.Errorf("open TOTP credential: %w", err)
		}
		defer clearBytes(secret)
		if err := bootstrap.VerifyTOTP(secret, request.TOTPCode, m.now()); err != nil {
			return LoginResult{}, ErrMFAInvalid
		}
	}

	token, err := m.newToken()
	if err != nil {
		return LoginResult{}, fmt.Errorf("mint session token: %w", err)
	}
	sessionID, err := m.newID("ses", 18)
	if err != nil {
		return LoginResult{}, fmt.Errorf("mint session id: %w", err)
	}
	clientKind := strings.ToLower(strings.TrimSpace(request.ClientKind))
	switch clientKind {
	case "dashboard", "cli", "api":
	default:
		clientKind = "dashboard"
	}
	expiresAt := now.Add(m.ttl)
	idleExpiresAt := now.Add(m.idleTTL)
	session := Session{
		ID:                sessionID,
		UserID:            user.ID,
		TokenHash:         m.tokenMAC(token),
		TokenPrefix:       publicTokenPrefix(token),
		ClientKind:        clientKind,
		SessionGeneration: user.SessionGeneration,
		UserAgentHash:     hashMetadata(request.UserAgent),
		CreatedAt:         now,
		LastSeenAt:        now,
		ExpiresAt:         expiresAt,
		IdleExpiresAt:     idleExpiresAt,
		Current:           true,
	}
	if err := m.store.InsertSession(ctx, user, session, now, m.maxSessions); err != nil {
		return LoginResult{}, err
	}
	// A successful login resets only the account budget. Keeping the broader
	// network budget prevents one valid credential from resetting the
	// brute-force allowance for every other account behind the same address.
	if err := m.store.ClearLoginAttempts(ctx, [][]byte{accountBucket}); err != nil {
		_ = m.store.RevokeSession(ctx, user.ID, session.ID, "login-rate-limit-cleanup-failed", now)
		return LoginResult{}, err
	}
	return LoginResult{
		AccessToken: token,
		TokenType:   "Bearer",
		ExpiresIn:   int64(m.ttl.Seconds()),
		User:        publicUser(user, now),
		Session:     session,
	}, nil
}

// Authenticate verifies a midn_user bearer against durable state and extends
// its idle deadline atomically without extending the absolute deadline.
func (m *Manager) Authenticate(ctx context.Context, authorization string) (Principal, Session, error) {
	token, err := auth.ParseBearerToken(authorization)
	if err != nil || token.Class != auth.TokenClassUser {
		return Principal{}, Session{}, ErrSessionInvalid
	}
	now := m.now().UTC()
	user, session, err := m.store.AuthenticateSession(ctx, m.tokenMAC(token.Raw), now, now.Add(m.idleTTL))
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return Principal{}, Session{}, ErrSessionInvalid
		}
		return Principal{}, Session{}, err
	}
	session.Current = true
	authMethod := "password"
	if !user.TOTPEnrolledAt.IsZero() {
		authMethod = "password+totp"
	}
	return Principal{
		Kind:          "user",
		UserID:        user.ID,
		SessionID:     session.ID,
		AuthMethod:    authMethod,
		Email:         user.Email,
		DisplayName:   user.DisplayName,
		InstanceAdmin: user.InstanceAdmin,
	}, session, nil
}

func (m *Manager) Sessions(ctx context.Context, principal Principal) ([]Session, error) {
	sessions, err := m.store.ListSessions(ctx, principal.UserID)
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == principal.SessionID
	}
	return sessions, nil
}

func (m *Manager) Revoke(ctx context.Context, principal Principal, sessionID, reason string) error {
	if strings.TrimSpace(sessionID) == "" {
		return ErrSessionNotFound
	}
	return m.store.RevokeSession(ctx, principal.UserID, sessionID, cleanReason(reason), m.now().UTC())
}

func (m *Manager) RevokeAll(ctx context.Context, principal Principal, reason string) error {
	return m.store.RevokeAllSessions(ctx, principal.UserID, cleanReason(reason), m.now().UTC())
}

// SealTOTP binds a verified enrollment secret to a user ID and encrypts it
// with the configured identity KEK. The caller must verify the first code
// before persisting the returned envelope.
func (m *Manager) SealTOTP(ctx context.Context, userID string, secret []byte) ([]byte, error) {
	if m == nil || m.keys == nil {
		return nil, errors.New("TOTP key provider is required")
	}
	if strings.TrimSpace(userID) == "" || len(secret) == 0 {
		return nil, errors.New("TOTP user id and secret are required")
	}
	envelope, err := secrets.SealForVariable(ctx, m.keys, secrets.VariableID("admin-totp:"+userID), secret)
	if err != nil {
		return nil, err
	}
	return secrets.MarshalEnvelope(envelope)
}

func (m *Manager) openTOTP(ctx context.Context, user UserRecord) ([]byte, error) {
	if len(user.TOTPSecretEnvelope) == 0 {
		return nil, ErrMFARequired
	}
	if m.keys == nil {
		return nil, errors.New("TOTP key provider is required")
	}
	envelope, err := secrets.UnmarshalEnvelope(user.TOTPSecretEnvelope)
	if err != nil {
		return nil, err
	}
	return secrets.OpenForVariable(ctx, m.keys, secrets.VariableID("admin-totp:"+user.ID), envelope)
}

func (m *Manager) newToken() (string, error) {
	var secret [32]byte
	if err := m.random(secret[:]); err != nil {
		return "", err
	}
	return SessionTokenPrefix + base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

func (m *Manager) newID(prefix string, size int) (string, error) {
	buf := make([]byte, size)
	if err := m.random(buf); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func (m *Manager) tokenMAC(token string) []byte {
	mac := hmac.New(sha256.New, m.macKey)
	_, _ = mac.Write([]byte(token))
	return mac.Sum(nil)
}

func (m *Manager) loginBucketMAC(kind, value string) []byte {
	mac := hmac.New(sha256.New, m.macKey)
	_, _ = mac.Write([]byte("midnight/login-limit/v1\x00" + kind + "\x00" + strings.ToLower(strings.TrimSpace(value))))
	return mac.Sum(nil)
}

func normalizeRemoteHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func publicTokenPrefix(token string) string {
	const shown = 18
	if len(token) <= shown {
		return token
	}
	return token[:shown]
}

func hashMetadata(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
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

func publicUser(user UserRecord, lastLogin time.Time) User {
	return User{
		ID:            user.ID,
		Email:         user.Email,
		DisplayName:   user.DisplayName,
		InstanceAdmin: user.InstanceAdmin,
		MFAEnabled:    !user.TOTPEnrolledAt.IsZero(),
		LastLoginAt:   lastLogin,
	}
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

// Valid PHC-style hash used only to equalize the missing-account password
// verification path. It is not associated with any real account.
const dummyPasswordHash = "pbkdf2-sha256$600000$AAAAAAAAAAAAAAAAAAAAAA$O4QwQbRbprrfNOrAQy0e+kHlLE+AlkgdYvYr5VBs8B0"
