package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/midnight/midnight/internal/failurecodes"
	"github.com/midnight/midnight/internal/idempotency"
)

// mutationScopeContextKey carries the forward-looking scope declared for a
// mutation route so downstream handlers and audit sinks can observe it.
type mutationScopeContextKey struct{}

// MutationScopeFromContext returns the scope declared by the mutation seam for
// the current request, if any. It is a forward-looking annotation; MAIN
// currently authorizes with a single shared bearer token, so the value is not
// yet used to gate access.
func MutationScopeFromContext(ctx context.Context) string {
	scope, _ := ctx.Value(mutationScopeContextKey{}).(string)
	return scope
}

// mutation is the uniform mutation seam, adopted from the light reference
// implementation where mutation(scope, next) == requireScope(scope,
// requireIdempotencyKey(next)).
//
// It gives every mutating route a single choke point that (a) declares a scope
// and (b) enforces Idempotency-Key presence/format BEFORE the handler runs,
// instead of relying on each handler to remember. It deliberately does NOT
// perform MAIN's richer idempotency Reserve/AttachOperation/CompleteSuccess
// bookkeeping — that logic stays in the handlers because it drives the
// 202-operation replay model. The seam only adds presence/format enforcement
// in front, matching the error shape handlers already emit for that case.
func mutation(scope string, next http.HandlerFunc) http.HandlerFunc {
	return requireScope(scope, requireIdempotencyKey(next))
}

// requireScope is a forward-looking, pass-through scope hook. MAIN currently
// authorizes mutations with a single shared bearer token applied by
// optionalBearerTokenMiddleware at route registration, so this hook does not
// itself authenticate; it records the declared scope in the request context.
// When a per-route token/scope model lands (a separate deferred project), the
// authorization check belongs here.
func requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if scope != "" {
			r = r.WithContext(context.WithValue(r.Context(), mutationScopeContextKey{}, scope))
		}
		next(w, r)
	}
}

func tenantGeneratedDomainMethodScope(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := ""
		switch r.Method {
		case http.MethodGet:
			scope = "domains:read"
		case http.MethodPost, http.MethodDelete:
			scope = "domains:write"
		}
		requireScope(scope, next)(w, r)
	}
}

// requireIdempotencyKey enforces the Idempotency-Key presence/format contract
// for mutating methods (POST/PUT/PATCH/DELETE). Non-mutating methods pass
// through untouched so the seam is safe to wrap around dispatchers that also
// serve reads. It only inspects the header and never consumes the request body,
// leaving the handler's own body reads and Reserve/Complete flow intact. The
// error envelopes mirror what the individual handlers emit today: a missing key
// yields IDEMPOTENCY_KEY_REQUIRED and a malformed key yields
// IDEMPOTENCY_KEY_INVALID.
func requireIdempotencyKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isMutatingMethod(r.Method) {
			_, failureCode := singletonIdempotencyKey(r.Header)
			if failureCode == failurecodes.IdempotencyKeyRequired {
				writeRequestError(w, r, http.StatusBadRequest, failureCode, "exactly one Idempotency-Key header is required", nil)
				return
			}
			if failureCode == failurecodes.IdempotencyKeyInvalid {
				writeRequestError(w, r, http.StatusBadRequest, failureCode, "invalid idempotency key", nil)
				return
			}
		}
		next(w, r)
	}
}

// singletonIdempotencyKey rejects both repeated field lines and the
// comma-delimited form produced when an intermediary coalesces them. Without
// this cardinality check, different hops can select different replay
// identities for the same request.
func singletonIdempotencyKey(header http.Header) (idempotency.Key, failurecodes.Code) {
	values := header.Values("Idempotency-Key")
	if len(values) != 1 {
		return "", failurecodes.IdempotencyKeyRequired
	}
	key := idempotency.Key(values[0])
	if strings.Contains(string(key), ",") || idempotency.ValidateKey(key) != nil {
		return "", failurecodes.IdempotencyKeyInvalid
	}
	return key, ""
}

// isMutatingMethod reports whether the HTTP method mutates server state and thus
// requires an Idempotency-Key under the seam.
func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
