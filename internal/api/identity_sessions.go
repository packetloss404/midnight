package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/midnight/midnight/internal/audit"
	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/identity"
)

const (
	identitySessionsPath = v1alphaPrefix + "/auth/sessions"
	identityMePath       = v1alphaPrefix + "/auth/me"
)

type identityPrincipalContextKey struct{}

type createIdentitySessionRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	TOTPCode   string `json:"totp_code,omitempty"`
	ClientKind string `json:"client_kind,omitempty"`
}

type identityMeResponse struct {
	Principal identity.Principal `json:"principal"`
	Session   identity.Session   `json:"session"`
}

func registerIdentitySessionRoutes(mux handlerRegistrar, manager *identity.Manager, emitter *auditEmitter) {
	if manager == nil {
		unavailable := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.CapabilityUnavailable, "durable human sessions are not configured", nil)
		})
		mux.Handle(identitySessionsPath, unavailable)
		mux.Handle(identitySessionsPath+"/", unavailable)
		mux.Handle(identityMePath, unavailable)
		return
	}

	authenticated := identitySessionMiddleware(manager)
	mux.Handle(identitySessionsPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createIdentitySession(w, r, manager, emitter)
		case http.MethodGet:
			authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				listIdentitySessions(w, r, manager)
			})).ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		}
	}))
	mux.Handle(identitySessionsPath+"/", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revokeIdentitySession(w, r, manager, emitter)
	})))
	mux.Handle(identityMePath, authenticated(http.HandlerFunc(identityMe)))
}

func createIdentitySession(w http.ResponseWriter, r *http.Request, manager *identity.Manager, emitter *auditEmitter) {
	var request createIdentitySessionRequest
	if _, ok := decodeStrictJSONRequestBodyWithLimit(w, r, &request, "invalid login request", sensitiveJSONBodyLimitBytes); !ok {
		return
	}
	result, err := manager.Login(r.Context(), identity.LoginRequest{
		Email:      request.Email,
		Password:   request.Password,
		TOTPCode:   request.TOTPCode,
		ClientKind: request.ClientKind,
		UserAgent:  r.UserAgent(),
		RemoteAddr: r.RemoteAddr,
	})
	if err != nil {
		emitter.emit(r.Context(), audit.Row{
			ActorType: "system", ResourceKind: "identity",
			ResourceID: "login", Action: "identity.login.denied",
			ReasonCode: identityLoginAuditReason(err), PolicyDecision: "denied",
			Attrs: map[string]any{
				"actor_class": "anonymous",
				"client_kind": strings.ToLower(strings.TrimSpace(request.ClientKind)),
			},
		})
	}
	switch {
	case errors.Is(err, identity.ErrLoginRateLimited):
		retryAfter := identity.DefaultLoginWindow
		var rateLimitErr *identity.LoginRateLimitError
		if errors.As(err, &rateLimitErr) && rateLimitErr.RetryAfter > 0 {
			retryAfter = rateLimitErr.RetryAfter
		}
		seconds := int64(retryAfter.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		writeRequestError(w, r, http.StatusTooManyRequests, failurecodes.AuthRateLimited, "login attempts are temporarily rate limited", nil)
		return
	case errors.Is(err, identity.ErrInvalidCredentials):
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthCredentialsInvalid, "invalid account credentials", nil)
		return
	case errors.Is(err, identity.ErrMFARequired):
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.MFARequired, "multi-factor authentication is required", nil)
		return
	case errors.Is(err, identity.ErrMFAInvalid):
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.MFAInvalidCode, "multi-factor authentication failed", nil)
		return
	case err != nil:
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
		return
	}
	authMethod := "password"
	if result.User.MFAEnabled {
		authMethod = "password+totp"
	}
	emitter.emit(r.Context(), audit.Row{
		ActorID: result.User.ID, ActorType: "user", AuthMethod: authMethod,
		ResourceKind: "session", ResourceID: result.Session.ID,
		Action: "identity.login.succeeded", PolicyDecision: "allowed",
		After: map[string]any{
			"client_kind": result.Session.ClientKind,
			"expires_at":  result.Session.ExpiresAt,
		},
	})
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusCreated, result)
}

func identitySessionMiddleware(manager *identity.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, session, err := manager.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				w.Header().Set("WWW-Authenticate", "Bearer")
				if errors.Is(err, identity.ErrSessionInvalid) {
					writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid, expired, or revoked", nil)
					return
				}
				writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
				return
			}
			ctx := context.WithValue(r.Context(), identityPrincipalContextKey{}, principal)
			ctx = context.WithValue(ctx, identitySessionContextKey{}, session)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type identitySessionContextKey struct{}

func identityPrincipalFromContext(ctx context.Context) (identity.Principal, bool) {
	principal, ok := ctx.Value(identityPrincipalContextKey{}).(identity.Principal)
	return principal, ok
}

func identitySessionFromContext(ctx context.Context) (identity.Session, bool) {
	session, ok := ctx.Value(identitySessionContextKey{}).(identity.Session)
	return session, ok
}

func identityMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	principal, principalOK := identityPrincipalFromContext(r.Context())
	session, sessionOK := identitySessionFromContext(r.Context())
	if !principalOK || !sessionOK {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, identityMeResponse{Principal: principal, Session: session})
}

func listIdentitySessions(w http.ResponseWriter, r *http.Request, manager *identity.Manager) {
	principal, ok := identityPrincipalFromContext(r.Context())
	if !ok {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	sessions, err := manager.Sessions(r.Context(), principal)
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, sessions)
}

func revokeIdentitySession(w http.ResponseWriter, r *http.Request, manager *identity.Manager, emitter *auditEmitter) {
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodDelete)
		writeRequestError(w, r, http.StatusMethodNotAllowed, failurecodes.MethodNotAllowed, "method not allowed", nil)
		return
	}
	sessionID := strings.TrimPrefix(r.URL.Path, identitySessionsPath+"/")
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "session not found", nil)
		return
	}
	principal, ok := identityPrincipalFromContext(r.Context())
	if !ok {
		writeRequestError(w, r, http.StatusUnauthorized, failurecodes.AuthSessionInvalid, "session is invalid", nil)
		return
	}
	err := manager.Revoke(r.Context(), principal, sessionID, "user-requested")
	if errors.Is(err, identity.ErrSessionNotFound) || errors.Is(err, identity.ErrSessionForbidden) {
		emitter.emit(r.Context(), audit.Row{
			ActorID: principal.UserID, ActorType: "user", AuthMethod: principal.AuthMethod,
			ResourceKind: "session", ResourceID: sessionID,
			Action:     "identity.session.revoke.denied",
			ReasonCode: string(failurecodes.ResourceNotFound), PolicyDecision: "denied",
		})
		writeRequestError(w, r, http.StatusNotFound, failurecodes.ResourceNotFound, "session not found", nil)
		return
	}
	if err != nil {
		writeRequestError(w, r, http.StatusServiceUnavailable, failurecodes.InternalError, "identity service is unavailable", nil)
		return
	}
	emitter.emit(r.Context(), audit.Row{
		ActorID: principal.UserID, ActorType: "user", AuthMethod: principal.AuthMethod,
		ResourceKind: "session", ResourceID: sessionID,
		Action: "identity.session.revoke", PolicyDecision: "allowed",
	})
	w.WriteHeader(http.StatusNoContent)
}

func identityLoginAuditReason(err error) string {
	switch {
	case errors.Is(err, identity.ErrLoginRateLimited):
		return string(failurecodes.AuthRateLimited)
	case errors.Is(err, identity.ErrMFARequired):
		return string(failurecodes.MFARequired)
	case errors.Is(err, identity.ErrMFAInvalid):
		return string(failurecodes.MFAInvalidCode)
	case errors.Is(err, identity.ErrInvalidCredentials):
		return string(failurecodes.AuthCredentialsInvalid)
	default:
		return string(failurecodes.InternalError)
	}
}
