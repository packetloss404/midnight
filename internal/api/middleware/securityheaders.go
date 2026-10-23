package middleware

import (
	"net/http"
	"strings"
)

const (
	headerContentSecurityPolicy = "Content-Security-Policy"
	headerFrameOptions          = "X-Frame-Options"
	headerContentTypeOptions    = "X-Content-Type-Options"
	headerReferrerPolicy        = "Referrer-Policy"
	headerPermissionsPolicy     = "Permissions-Policy"
	headerStrictTransport       = "Strict-Transport-Security"
)

const (
	strictTransportPolicy = "max-age=31536000; includeSubDomains"
	frameOptions          = "DENY"
	contentTypeOptions    = "nosniff"
	referrerPolicy        = "no-referrer"
	permissionsPolicy     = "accelerometer=(), autoplay=(), camera=(), clipboard-read=(self), clipboard-write=(self), display-capture=(), encrypted-media=(), fullscreen=(self), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(self), screen-wake-lock=(), usb=()"
)

const consoleContentSecurityPolicy = "base-uri 'none'; " +
	"connect-src 'self'; " +
	"default-src 'self'; " +
	"font-src 'self' data:; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"frame-src 'none'; " +
	"img-src 'self' data: blob:; " +
	"manifest-src 'self'; " +
	"media-src 'self' blob:; " +
	"object-src 'none'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"worker-src 'self' blob:"

// GraphiQL is a disabled-by-default development surface whose existing HTML
// bootstrap imports React and GraphiQL from two pinned CDN origins and contains
// an inline initialization script. Keep that exception confined to the exact
// playground route; production console and API responses retain the strict
// same-origin policy above.
const graphiQLContentSecurityPolicy = "base-uri 'none'; " +
	"connect-src 'self'; " +
	"default-src 'none'; " +
	"font-src 'self' data:; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"img-src 'self' data: blob:; " +
	"object-src 'none'; " +
	"script-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net; " +
	"style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net"

// The legacy landing page has one inline progressive-enhancement script that
// updates its copyright year. No other console route receives this exception.
const legacyLandingContentSecurityPolicy = "base-uri 'none'; " +
	"connect-src 'self'; " +
	"default-src 'self'; " +
	"font-src 'self' data:; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'; " +
	"frame-src 'none'; " +
	"img-src 'self' data: blob:; " +
	"object-src 'none'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'"

// SecurityHeaders applies the browser-facing security policy at Midnight's
// HTTP edge. It deliberately does not wrap ResponseWriter, preserving optional
// interfaces such as http.Flusher and http.Hijacker for SSE and future
// WebSocket handlers.
//
// HSTS is emitted on every response because TLS terminates at Traefik. Browsers
// ignore HSTS received over plaintext HTTP, so direct localhost development
// remains usable while proxied HTTPS responses receive the production policy.
func SecurityHeaders(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set(headerContentSecurityPolicy, contentSecurityPolicy(r.URL.Path))
		headers.Set(headerFrameOptions, frameOptions)
		headers.Set(headerContentTypeOptions, contentTypeOptions)
		headers.Set(headerReferrerPolicy, referrerPolicy)
		headers.Set(headerPermissionsPolicy, permissionsPolicy)
		headers.Set(headerStrictTransport, strictTransportPolicy)
		next.ServeHTTP(w, r)
	})
}

func contentSecurityPolicy(requestPath string) string {
	switch {
	case requestPath == "/graphiql" || strings.HasPrefix(requestPath, "/graphiql/"):
		return graphiQLContentSecurityPolicy
	case requestPath == "/dashboard/landing.html":
		return legacyLandingContentSecurityPolicy
	default:
		return consoleContentSecurityPolicy
	}
}
