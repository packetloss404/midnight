// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/ssrf"
)

// Manifest media types sent on every resolve. Both the OCI and the Docker
// registry-v2 spellings are offered, and both the single-manifest and the
// index/list spellings, so a multi-architecture tag resolves to its index
// digest instead of returning 404 or an architecture-specific child.
const (
	MediaTypeOCIIndex       = "application/vnd.oci.image.index.v1+json"
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeDockerList     = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
)

// manifestAcceptHeader is the single `Accept` value used for manifest reads.
var manifestAcceptHeader = strings.Join([]string{
	MediaTypeOCIIndex,
	MediaTypeOCIManifest,
	MediaTypeDockerList,
	MediaTypeDockerManifest,
}, ", ")

// DefaultPublicRegistry is where a host-less reference such as `nginx:1.27`
// or `library/redis:7` is looked up.
const DefaultPublicRegistry = "registry-1.docker.io"

// Resolve budgets. They are deliberately small: image admission sits on the
// synchronous deploy-intake request path, so a slow or black-holed registry
// must surface as a typed error long before any client or proxy timeout.
const (
	DefaultResolveRequestTimeout = 5 * time.Second
	DefaultResolveTotalTimeout   = 12 * time.Second
)

// maxManifestBytes caps the GET fallback body. Registry manifests are a few
// kilobytes; the cap keeps a hostile endpoint from streaming forever.
const maxManifestBytes = 4 << 20

// Typed resolve failures. Callers map these onto their own API contract:
// ErrManifestNotFound is a caller mistake (validation), the remaining two are
// instance-side conditions and must never be reported as a bad request.
var (
	// ErrReferenceInvalid means the string is not a parseable image reference.
	ErrReferenceInvalid = errors.New("registry resolve: image reference is invalid")
	// ErrManifestNotFound means the registry answered authoritatively that it
	// does not hold this tag or digest.
	ErrManifestNotFound = errors.New("registry resolve: manifest not found in registry")
	// ErrRegistryDenied means the registry refused the anonymous read.
	ErrRegistryDenied = errors.New("registry resolve: registry denied anonymous read access")
	// ErrRegistryUnavailable covers DNS, dial, TLS, timeout, and 5xx. It is
	// never a statement about the image itself.
	ErrRegistryUnavailable = errors.New("registry resolve: registry is unreachable")
)

// Resolution is the immutable pin produced for a caller-supplied reference.
//
// Reference is always digest-pinned and keeps the caller's original registry,
// repository, and tag spelling so the console can render `nginx:1.27` with the
// resolved digest underneath it. Pinned is true when the caller supplied a
// mutable tag that this call turned into a digest.
type Resolution struct {
	Reference  string
	Registry   string
	Repository string
	Tag        string
	Digest     string
	Pinned     bool
}

// ResolverConfig describes how a Resolver reaches registries.
type ResolverConfig struct {
	// LocalEndpoint is the operator-configured private registry, host[:port]
	// with no scheme (MIDNIGHT_REGISTRY_ENDPOINT / MIDNIGHT_REGISTRY_ADDR).
	// References to this endpoint are allowed to resolve to loopback and
	// private addresses; every other registry stays behind the public-unicast
	// SSRF guard.
	LocalEndpoint string
	// LocalInsecure serves the local endpoint over plain HTTP.
	LocalInsecure bool
	// PublicRegistry overrides DefaultPublicRegistry for host-less references.
	PublicRegistry string
	// RequestTimeout bounds one HTTP exchange; TotalTimeout bounds the whole
	// resolve including the token exchange and the GET fallback.
	RequestTimeout time.Duration
	TotalTimeout   time.Duration
	// DNS is the injectable resolver used by the SSRF dialer.
	DNS ssrf.Resolver
}

// Resolver turns image references into immutable digest pins by reading the
// registry manifest API. It performs no writes and sends no credentials: only
// public/anonymous reads and the anonymous bearer-token exchange a registry
// asks for are attempted.
type Resolver struct {
	config ResolverConfig
}

// NewResolver builds a Resolver with the config defaults applied.
func NewResolver(config ResolverConfig) *Resolver {
	config.LocalEndpoint = normalizeEndpoint(config.LocalEndpoint)
	config.PublicRegistry = normalizeEndpoint(config.PublicRegistry)
	if config.PublicRegistry == "" {
		config.PublicRegistry = DefaultPublicRegistry
	}
	if config.RequestTimeout <= 0 {
		config.RequestTimeout = DefaultResolveRequestTimeout
	}
	if config.TotalTimeout <= 0 {
		config.TotalTimeout = DefaultResolveTotalTimeout
	}
	return &Resolver{config: config}
}

// Resolve pins reference to an immutable digest.
//
// A tag is resolved against the registry and returned as
// `name:tag@sha256:<hex>`; a reference that already carries a digest is
// verified to exist and returned unchanged. Every failure is one of the typed
// errors above, wrapped with context that is safe to show an operator.
func (r *Resolver) Resolve(ctx context.Context, reference string) (Resolution, error) {
	if r == nil {
		return Resolution{}, fmt.Errorf("%w: no resolver is configured", ErrRegistryUnavailable)
	}
	parsed, err := ParseImageReference(reference)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: %s", ErrReferenceInvalid, err.Error())
	}

	host := parsed.Registry
	repository := parsed.Repository
	if host == "" {
		host = r.config.PublicRegistry
		// Docker Hub stores single-component repositories under `library/`.
		if !strings.Contains(repository, "/") {
			repository = "library/" + repository
		}
	}
	lookup := parsed.Digest
	if lookup == "" {
		lookup = parsed.Tag
	}

	ctx, cancel := context.WithTimeout(ctx, r.config.TotalTimeout)
	defer cancel()

	digest, err := r.manifestDigest(ctx, host, repository, lookup)
	if err != nil {
		return Resolution{}, err
	}
	if parsed.Digest != "" && digest != "" && digest != parsed.Digest {
		// A content-addressed read that answers with a different address is
		// not a pin we may forward, whatever the registry meant by it.
		return Resolution{}, fmt.Errorf("%w: registry answered digest %s for %s", ErrManifestNotFound, digest, parsed.Digest)
	}
	if digest == "" {
		digest = parsed.Digest
	}
	if !sha256DigestPattern.MatchString(digest) {
		return Resolution{}, fmt.Errorf("%w: registry returned a non-sha256 digest", ErrRegistryUnavailable)
	}

	name := parsed.Repository
	if parsed.Registry != "" {
		name = parsed.Registry + "/" + parsed.Repository
	}
	if parsed.Tag != "" {
		name += ":" + parsed.Tag
	}
	return Resolution{
		Reference:  name + "@" + digest,
		Registry:   parsed.Registry,
		Repository: parsed.Repository,
		Tag:        parsed.Tag,
		Digest:     digest,
		Pinned:     parsed.Digest == "",
	}, nil
}

// manifestDigest reads the canonical digest for one repository reference.
// HEAD is preferred; registries that do not implement it (405/501) or that
// omit Docker-Content-Digest fall back to GET, where the digest is the
// sha256 of the exact manifest bytes.
func (r *Resolver) manifestDigest(ctx context.Context, host, repository, reference string) (string, error) {
	client, err := r.clientFor(host)
	if err != nil {
		return "", err
	}
	endpoint := r.baseURL(host) + "/v2/" + repository + "/manifests/" + url.PathEscape(reference)

	token := ""
	// At most two passes: the anonymous read, then the same read carrying the
	// token a public registry challenged for.
	for pass := 0; pass < 2; pass++ {
		digest, response, err := r.readManifest(ctx, client, endpoint, token)
		if err != nil {
			return "", err
		}
		if response.status == http.StatusUnauthorized && response.challenge != "" && token == "" {
			token, err = r.anonymousToken(ctx, response.challenge, repository)
			if err != nil {
				return "", err
			}
			continue
		}
		return digest, statusError(response, host)
	}
	return "", fmt.Errorf("%w: registry did not accept the anonymous read", ErrRegistryDenied)
}

// readManifest issues HEAD and falls back to GET when the registry does not
// implement HEAD or answers without a Docker-Content-Digest header.
func (r *Resolver) readManifest(ctx context.Context, client *http.Client, endpoint, token string) (string, manifestResponse, error) {
	digest, response, err := r.attempt(ctx, client, http.MethodHead, endpoint, token)
	if err != nil {
		return "", response, err
	}
	if response.status == http.StatusMethodNotAllowed ||
		response.status == http.StatusNotImplemented ||
		(response.ok() && digest == "") {
		return r.attempt(ctx, client, http.MethodGet, endpoint, token)
	}
	return digest, response, nil
}

// manifestResponse is the small projection of a registry answer the resolver
// needs. The body is never retained.
type manifestResponse struct {
	status    int
	challenge string
}

func (m manifestResponse) ok() bool { return m.status >= 200 && m.status < 300 }

// statusError maps a registry status onto the typed resolve errors.
func statusError(response manifestResponse, host string) error {
	switch {
	case response.ok():
		return nil
	case response.status == http.StatusNotFound:
		return ErrManifestNotFound
	case response.status == http.StatusUnauthorized, response.status == http.StatusForbidden:
		return fmt.Errorf("%w: %s answered %d", ErrRegistryDenied, host, response.status)
	default:
		return fmt.Errorf("%w: %s answered %d", ErrRegistryUnavailable, host, response.status)
	}
}

// attempt performs one manifest request. It returns the digest when the
// registry supplied (or the body proved) one.
func (r *Resolver) attempt(ctx context.Context, client *http.Client, method, endpoint, token string) (string, manifestResponse, error) {
	requestCtx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint, nil)
	if err != nil {
		return "", manifestResponse{}, fmt.Errorf("%w: %s", ErrRegistryUnavailable, err.Error())
	}
	request.Header.Set("Accept", manifestAcceptHeader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", manifestResponse{}, unreachable(err)
	}
	defer func() { _ = response.Body.Close() }()

	projection := manifestResponse{
		status:    response.StatusCode,
		challenge: response.Header.Get("Www-Authenticate"),
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if digest != "" && !sha256DigestPattern.MatchString(digest) {
		digest = ""
	}
	if digest == "" && method == http.MethodGet && projection.ok() {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxManifestBytes+1))
		if readErr != nil {
			return "", projection, unreachable(readErr)
		}
		if len(body) > maxManifestBytes {
			return "", projection, fmt.Errorf("%w: manifest exceeds %d bytes", ErrRegistryUnavailable, maxManifestBytes)
		}
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManifestBytes))
	return digest, projection, nil
}

// anonymousToken performs the token-service exchange a registry asks for with
// a `Bearer realm=...` challenge. No credential is presented: this only buys
// the anonymous pull scope that a public registry requires even for reads.
func (r *Resolver) anonymousToken(ctx context.Context, challenge, repository string) (string, error) {
	realm, service, scope := parseBearerChallenge(challenge)
	if realm == "" {
		return "", fmt.Errorf("%w: registry sent an unusable authentication challenge", ErrRegistryDenied)
	}
	realmURL, err := url.Parse(realm)
	if err != nil || realmURL.Host == "" {
		return "", fmt.Errorf("%w: registry sent an unusable token realm", ErrRegistryDenied)
	}
	if !r.isLocal(realmURL.Host) && !strings.EqualFold(realmURL.Scheme, "https") {
		return "", fmt.Errorf("%w: token realm must use https", ErrRegistryDenied)
	}
	if scope == "" {
		scope = "repository:" + repository + ":pull"
	}
	query := realmURL.Query()
	if service != "" {
		query.Set("service", service)
	}
	query.Set("scope", scope)
	realmURL.RawQuery = query.Encode()

	client, err := r.clientFor(realmURL.Host)
	if err != nil {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, realmURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrRegistryUnavailable, err.Error())
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", unreachable(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token service answered %d", ErrRegistryDenied, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", unreachable(err)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("%w: token service response is not JSON", ErrRegistryDenied)
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("%w: token service returned no token", ErrRegistryDenied)
	}
	return token, nil
}

// parseBearerChallenge extracts realm/service/scope from a WWW-Authenticate
// header. Unknown parameters are ignored; a non-Bearer scheme yields no realm.
func parseBearerChallenge(challenge string) (realm, service, scope string) {
	challenge = strings.TrimSpace(challenge)
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return "", "", ""
	}
	for _, part := range splitChallengeParams(challenge[len("bearer "):]) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "realm":
			realm = value
		case "service":
			service = value
		case "scope":
			scope = value
		}
	}
	return realm, service, scope
}

// splitChallengeParams splits on commas that are not inside a quoted value.
func splitChallengeParams(value string) []string {
	var parts []string
	quoted := false
	current := strings.Builder{}
	for _, char := range value {
		switch {
		case char == '"':
			quoted = !quoted
			current.WriteRune(char)
		case char == ',' && !quoted:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// clientFor builds a per-host client whose dialer resolves once, validates
// the complete answer set, and then dials the approved literal. Only the
// operator-configured local registry may resolve to a non-public address.
func (r *Resolver) clientFor(host string) (*http.Client, error) {
	hostname := host
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		hostname = parsedHost
	}
	dialer := ssrf.Dialer{
		Resolver:      r.config.DNS,
		DialContext:   (&net.Dialer{Timeout: r.config.RequestTimeout}).DialContext,
		ExpectedHost:  hostname,
		AllowLoopback: r.isLocal(host),
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: default transport is unavailable", ErrRegistryUnavailable)
	}
	cloned := transport.Clone()
	// Never let an ambient proxy carry the request past the validated dial.
	cloned.Proxy = nil
	cloned.DialContext = dialer.Dial
	cloned.ResponseHeaderTimeout = r.config.RequestTimeout
	return &http.Client{
		Timeout:   r.config.TotalTimeout,
		Transport: cloned,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			// The dialer is bound to one validated host. A same-host redirect
			// stays inside that guarantee; a cross-host redirect would escape
			// it, so it is refused rather than followed unvalidated.
			if len(via) >= 3 {
				return errors.New("registry resolve: too many redirects")
			}
			if !strings.EqualFold(normalizeEndpoint(request.URL.Host), normalizeEndpoint(host)) {
				return errors.New("registry resolve: cross-host redirect is not followed")
			}
			return nil
		},
	}, nil
}

func (r *Resolver) baseURL(host string) string {
	scheme := "https"
	if r.isLocal(host) && r.config.LocalInsecure {
		scheme = "http"
	}
	return scheme + "://" + host
}

// isLocal reports whether host is the operator-configured private registry.
func (r *Resolver) isLocal(host string) bool {
	local := r.config.LocalEndpoint
	if local == "" {
		return false
	}
	return strings.EqualFold(normalizeEndpoint(host), local)
}

// unreachable converts a transport error into the typed unavailable error
// without echoing a full URL (which may name internal infrastructure).
func unreachable(err error) error {
	if errors.Is(err, ssrf.ErrUnsafeAddress) {
		return fmt.Errorf("%w: registry host resolves to a non-public address", ErrRegistryUnavailable)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: registry did not answer within the resolve budget", ErrRegistryUnavailable)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return fmt.Errorf("%w: registry did not answer within the resolve budget", ErrRegistryUnavailable)
	}
	return fmt.Errorf("%w: %s", ErrRegistryUnavailable, transportReason(err))
}

// transportReason keeps the operator-facing reason short and free of URLs.
func transportReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err.Error()
	}
	return err.Error()
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return strings.ToLower(strings.TrimRight(endpoint, "/"))
}
