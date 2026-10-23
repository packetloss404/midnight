// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// loopbackResolver points the local-registry endpoint at an httptest server
// so the SSRF dialer's loopback allowance is exercised for real.
func newLocalResolver(t *testing.T, handler http.Handler) (*Resolver, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint := strings.TrimPrefix(server.URL, "http://")
	return NewResolver(ResolverConfig{
		LocalEndpoint:  endpoint,
		LocalInsecure:  true,
		RequestTimeout: 2 * time.Second,
		TotalTimeout:   4 * time.Second,
	}), endpoint
}

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestResolvePinsMutableTagToDigest(t *testing.T) {
	var method, path, accept string
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, accept = r.Method, r.URL.Path, r.Header.Get("Accept")
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))

	resolution, err := resolver.Resolve(context.Background(), endpoint+"/app:1.27")
	if err != nil {
		t.Fatalf("resolve tag: %v", err)
	}
	if resolution.Digest != testDigest {
		t.Fatalf("digest = %q, want %q", resolution.Digest, testDigest)
	}
	if want := endpoint + "/app:1.27@" + testDigest; resolution.Reference != want {
		t.Fatalf("reference = %q, want %q", resolution.Reference, want)
	}
	if !resolution.Pinned {
		t.Fatal("Pinned = false, want true for a tag-only reference")
	}
	if resolution.Tag != "1.27" {
		t.Fatalf("tag = %q, want 1.27", resolution.Tag)
	}
	if method != http.MethodHead {
		t.Fatalf("method = %q, want HEAD", method)
	}
	if path != "/v2/app/manifests/1.27" {
		t.Fatalf("path = %q", path)
	}
	for _, mediaType := range []string{MediaTypeOCIIndex, MediaTypeOCIManifest, MediaTypeDockerList, MediaTypeDockerManifest} {
		if !strings.Contains(accept, mediaType) {
			t.Fatalf("Accept %q is missing %q", accept, mediaType)
		}
	}
}

func TestResolveFallsBackToGETWhenHeadIsUnsupported(t *testing.T) {
	body := []byte(`{"schemaVersion":2}`)
	sum := sha256.Sum256(body)
	want := "sha256:" + hex.EncodeToString(sum[:])

	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", MediaTypeOCIManifest)
		_, _ = w.Write(body)
	}))

	resolution, err := resolver.Resolve(context.Background(), endpoint+"/app:v1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolution.Digest != want {
		t.Fatalf("digest = %q, want body sha256 %q", resolution.Digest, want)
	}
}

func TestResolveAcceptsExistingDigestWithoutRewriting(t *testing.T) {
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/manifests/"+testDigest) {
			t.Errorf("path = %q, want digest lookup", r.URL.Path)
		}
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))

	reference := endpoint + "/app:1.27@" + testDigest
	resolution, err := resolver.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatalf("resolve digest: %v", err)
	}
	if resolution.Reference != reference {
		t.Fatalf("reference = %q, want %q", resolution.Reference, reference)
	}
	if resolution.Pinned {
		t.Fatal("Pinned = true, want false for an already-immutable reference")
	}
}

func TestResolveRejectsDigestTheRegistryDoesNotHave(t *testing.T) {
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	_, err := resolver.Resolve(context.Background(), endpoint+"/app@"+testDigest)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("err = %v, want ErrManifestNotFound", err)
	}
}

func TestResolveRejectsDigestSubstitution(t *testing.T) {
	other := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", other)
		w.WriteHeader(http.StatusOK)
	}))

	_, err := resolver.Resolve(context.Background(), endpoint+"/app@"+testDigest)
	if !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("err = %v, want ErrManifestNotFound for a substituted digest", err)
	}
}

func TestResolveServerErrorIsUnavailableNotValidation(t *testing.T) {
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	_, err := resolver.Resolve(context.Background(), endpoint+"/app:1.0")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("err = %v, want ErrRegistryUnavailable", err)
	}
	if errors.Is(err, ErrManifestNotFound) {
		t.Fatal("a 502 must never be reported as a missing image")
	}
}

func TestResolvePerformsAnonymousTokenExchange(t *testing.T) {
	var authorized bool
	var scope string
	mux := http.NewServeMux()
	var endpoint string
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		scope = r.URL.Query().Get("scope")
		_, _ = w.Write([]byte(`{"token":"anon-token"}`))
	})
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer anon-token" {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="http://%s/token",service="test",scope="repository:app:pull"`, endpoint))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authorized = true
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	})

	resolver, resolvedEndpoint := newLocalResolver(t, mux)
	endpoint = resolvedEndpoint

	resolution, err := resolver.Resolve(context.Background(), endpoint+"/app:1.27")
	if err != nil {
		t.Fatalf("resolve with token exchange: %v", err)
	}
	if !authorized {
		t.Fatal("registry was never re-read with the anonymous token")
	}
	if scope != "repository:app:pull" {
		t.Fatalf("scope = %q", scope)
	}
	if resolution.Digest != testDigest {
		t.Fatalf("digest = %q", resolution.Digest)
	}
}

// stubDNS answers every lookup with one fixed address so the SSRF guard can
// be proven without touching real DNS.
type stubDNS struct{ address string }

func (s stubDNS) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP(s.address)}}, nil
}

func TestResolveRefusesRegistryOnAPrivateAddress(t *testing.T) {
	resolver := NewResolver(ResolverConfig{
		RequestTimeout: time.Second,
		TotalTimeout:   2 * time.Second,
		DNS:            stubDNS{address: "10.1.2.3"},
	})

	_, err := resolver.Resolve(context.Background(), "registry.example.com/app:1.0")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("err = %v, want ErrRegistryUnavailable", err)
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("err = %v, want the SSRF reason to survive", err)
	}
}

func TestResolveAllowsLoopbackOnlyForTheConfiguredLocalRegistry(t *testing.T) {
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))
	if _, err := resolver.Resolve(context.Background(), endpoint+"/app:1.0"); err != nil {
		t.Fatalf("configured local registry must resolve: %v", err)
	}

	// Same loopback address, but not the configured endpoint.
	other := NewResolver(ResolverConfig{RequestTimeout: time.Second, TotalTimeout: 2 * time.Second})
	_, err := other.Resolve(context.Background(), endpoint+"/app:1.0")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("err = %v, want an unconfigured loopback registry to be refused", err)
	}
}

func TestResolveRejectsUnparseableReference(t *testing.T) {
	resolver := NewResolver(ResolverConfig{})
	for _, reference := range []string{"", "nginx", "not a ref", "https://registry/app:1"} {
		if _, err := resolver.Resolve(context.Background(), reference); !errors.Is(err, ErrReferenceInvalid) {
			t.Fatalf("reference %q: err = %v, want ErrReferenceInvalid", reference, err)
		}
	}
}

func TestResolveNormalizesHostlessDockerHubRepositories(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Docker-Content-Digest", testDigest)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	endpoint := strings.TrimPrefix(server.URL, "http://")

	// Point the "public" registry at the loopback stub and mark it local so
	// the dialer is permitted to reach it.
	resolver := NewResolver(ResolverConfig{
		PublicRegistry: endpoint,
		LocalEndpoint:  endpoint,
		LocalInsecure:  true,
		RequestTimeout: time.Second,
		TotalTimeout:   2 * time.Second,
	})

	resolution, err := resolver.Resolve(context.Background(), "nginx:1.27")
	if err != nil {
		t.Fatalf("resolve hostless reference: %v", err)
	}
	if path != "/v2/library/nginx/manifests/1.27" {
		t.Fatalf("path = %q, want the library/ normalization", path)
	}
	if resolution.Reference != "nginx:1.27@"+testDigest {
		t.Fatalf("reference = %q, want the caller spelling preserved", resolution.Reference)
	}
}

func TestResolveHonoursTheRequestBudget(t *testing.T) {
	resolver, endpoint := newLocalResolver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	resolver.config.RequestTimeout = 150 * time.Millisecond
	resolver.config.TotalTimeout = 400 * time.Millisecond

	start := time.Now()
	_, err := resolver.Resolve(context.Background(), endpoint+"/app:1.0")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("err = %v, want ErrRegistryUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("resolve hung for %s; the budget must bound it", elapsed)
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, service, scope := parseBearerChallenge(`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/nginx:pull,push"`)
	if realm != "https://auth.docker.io/token" {
		t.Fatalf("realm = %q", realm)
	}
	if service != "registry.docker.io" {
		t.Fatalf("service = %q", service)
	}
	if scope != "repository:library/nginx:pull,push" {
		t.Fatalf("scope = %q", scope)
	}
	if realm, _, _ := parseBearerChallenge(`Basic realm="x"`); realm != "" {
		t.Fatalf("non-bearer challenge must not yield a realm, got %q", realm)
	}
}
