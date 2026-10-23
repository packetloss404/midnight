package router

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MiddlewareKind enumerates the Phase 0 middleware shapes the renderer
// understands. Additional shapes can be added by extending RenderMiddleware.
type MiddlewareKind string

const (
	// MiddlewareKindCompress emits the response compression middleware.
	MiddlewareKindCompress MiddlewareKind = "compress"
	// MiddlewareKindRedirectScheme emits a permanent or temporary scheme redirect.
	MiddlewareKindRedirectScheme MiddlewareKind = "redirectScheme"
	// MiddlewareKindHeaders emits a header-rewriting middleware with HSTS, request, and response headers.
	MiddlewareKindHeaders MiddlewareKind = "headers"
)

// MiddlewareDefinition is the renderer-input shape for a single Traefik
// middleware. Concrete fields are populated based on Kind.
type MiddlewareDefinition struct {
	Name string
	Kind MiddlewareKind

	// Compress carries the compress-middleware options.
	Compress *CompressMiddleware
	// RedirectScheme carries the redirect-scheme-middleware options.
	RedirectScheme *RedirectSchemeMiddleware
	// Headers carries the headers-middleware options.
	Headers *HeadersMiddleware
}

// CompressMiddleware controls the response compressor.
type CompressMiddleware struct {
	// MinResponseBytes skips compression for responses smaller than this size.
	MinResponseBytes int
	// ExcludedContentTypes never get compressed when present.
	ExcludedContentTypes []string
}

// RedirectSchemeMiddleware emits a scheme redirect (e.g., http -> https).
type RedirectSchemeMiddleware struct {
	// Scheme is the destination scheme (e.g., "https").
	Scheme string
	// Permanent toggles a 301 vs 302 status code.
	Permanent bool
	// Port optionally targets a non-default port on the destination scheme.
	Port string
}

// HeadersMiddleware emits HSTS, request, and response header rewrites.
type HeadersMiddleware struct {
	// HSTSSeconds is the Strict-Transport-Security max-age in seconds.
	HSTSSeconds int
	// HSTSIncludeSubdomains toggles includeSubDomains in the HSTS header.
	HSTSIncludeSubdomains bool
	// HSTSPreload toggles preload in the HSTS header.
	HSTSPreload bool
	// CustomRequestHeaders is appended to inbound requests.
	CustomRequestHeaders map[string]string
	// CustomResponseHeaders is appended to outbound responses.
	CustomResponseHeaders map[string]string
}

// TLSOptions captures the TLS handshake parameters that operators can pin per
// route group.
type TLSOptions struct {
	// Name is the unique key used by routers' tls.options reference.
	Name string
	// MinVersion is the negotiated TLS minimum (e.g., "VersionTLS12").
	MinVersion string
	// CipherSuites pins an allow-list of cipher suite identifiers.
	CipherSuites []string
	// ClientAuthRequireMTLS toggles mTLS hint emission. When true the renderer
	// emits an mTLS hint on routers referencing this option. Mutually exclusive
	// with ClientAuthType when the latter is non-empty.
	ClientAuthRequireMTLS bool
	// ClientAuthType, when non-empty, pins the Traefik clientAuthType value. It
	// takes precedence over ClientAuthRequireMTLS. Common values include
	// "RequireAndVerifyClientCert" (mTLS required) and "RequestClientCert"
	// (mTLS optional / hint).
	ClientAuthType string
	// ClientCAFiles is the CA bundle path list a future live executor would consume.
	ClientCAFiles []string
}

// RouteRenderInput is the extended renderer input used by RenderTraefikDynamicV2.
// Phase 0 keeps the Phase-1 V1 renderer in place; this shape is opt-in.
type RouteRenderInput struct {
	Routes      []RouteInputV2
	Middlewares []MiddlewareDefinition
	TLSOptions  []TLSOptions
}

// RouteInputV2 extends RouteInput with TLS options and an mTLS hint reference.
type RouteInputV2 struct {
	RouteInput
	// TLSOptionName references a TLSOptions block by Name. When non-empty the
	// renderer wires `tls.options` on the router.
	TLSOptionName string
	// MTLSHint, when set, toggles a `tls.clientAuthHint` synthetic field that
	// downstream tools surface to operators.
	MTLSHint string
}

// MiddlewareRenderError is returned when a middleware definition fails validation.
type MiddlewareRenderError struct {
	Name   string
	Reason string
}

// Error implements the error interface.
func (e *MiddlewareRenderError) Error() string {
	return fmt.Sprintf("middleware %q: %s", e.Name, e.Reason)
}

// validateMiddleware enforces the per-kind required fields.
func validateMiddleware(def MiddlewareDefinition) error {
	if strings.TrimSpace(def.Name) == "" {
		return &MiddlewareRenderError{Name: def.Name, Reason: "name is required"}
	}
	switch def.Kind {
	case MiddlewareKindCompress:
		if def.Compress == nil {
			return &MiddlewareRenderError{Name: def.Name, Reason: "compress options are required"}
		}
		if def.Compress.MinResponseBytes < 0 {
			return &MiddlewareRenderError{Name: def.Name, Reason: "min response bytes must be non-negative"}
		}
	case MiddlewareKindRedirectScheme:
		if def.RedirectScheme == nil {
			return &MiddlewareRenderError{Name: def.Name, Reason: "redirect scheme options are required"}
		}
		if strings.TrimSpace(def.RedirectScheme.Scheme) == "" {
			return &MiddlewareRenderError{Name: def.Name, Reason: "scheme is required"}
		}
	case MiddlewareKindHeaders:
		if def.Headers == nil {
			return &MiddlewareRenderError{Name: def.Name, Reason: "headers options are required"}
		}
		if def.Headers.HSTSSeconds < 0 {
			return &MiddlewareRenderError{Name: def.Name, Reason: "HSTS seconds must be non-negative"}
		}
	default:
		return &MiddlewareRenderError{Name: def.Name, Reason: "unsupported middleware kind"}
	}
	return nil
}

func validateTLSOption(opt TLSOptions) error {
	if strings.TrimSpace(opt.Name) == "" {
		return errors.New("TLS option name is required")
	}
	for _, suite := range opt.CipherSuites {
		if strings.TrimSpace(suite) == "" {
			return fmt.Errorf("TLS option %q has empty cipher suite", opt.Name)
		}
	}
	switch strings.TrimSpace(opt.ClientAuthType) {
	case "",
		"NoClientCert",
		"RequestClientCert",
		"RequireAnyClientCert",
		"VerifyClientCertIfGiven",
		"RequireAndVerifyClientCert":
	default:
		return fmt.Errorf("TLS option %q has unsupported clientAuthType %q", opt.Name, opt.ClientAuthType)
	}
	return nil
}

// sortedMiddlewareNames returns middleware names in a deterministic order.
func sortedMiddlewareNames(defs []MiddlewareDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names
}

func sortedTLSOptionNames(opts []TLSOptions) []string {
	names := make([]string, 0, len(opts))
	for _, opt := range opts {
		names = append(names, opt.Name)
	}
	sort.Strings(names)
	return names
}

func sortedHeaderKeys(headers map[string]string) []string {
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
