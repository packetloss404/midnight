// Package middleware contains small HTTP middleware shared by v1alpha handlers.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const HeaderRequestID = "X-Request-Id"

const maxExternalRequestIDLength = 128

type requestIDKey struct{}

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(HeaderRequestID)
		if !validExternalRequestID(requestID) {
			requestID = newRequestID()
		}

		w.Header().Set(HeaderRequestID, requestID)
		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validExternalRequestID accepts a deliberately small, log-safe correlation
// alphabet. Request ids are persisted in audit and observability records, so
// arbitrary control characters or unbounded caller input must not cross the
// HTTP trust boundary.
func validExternalRequestID(value string) bool {
	if value == "" || len(value) > maxExternalRequestIDLength {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

func FromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-00000000000000000000000000000000"
	}
	return "req-" + hex.EncodeToString(b[:])
}
