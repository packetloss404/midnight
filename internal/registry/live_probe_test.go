// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

// Opt-in probes that exercise the resolver against a real registry. They are
// skipped unless their environment variable is set, so ordinary `go test`
// runs stay hermetic.
package registry

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Manual probe against the running local stack registry.
// MIDNIGHT_LIVE_REGISTRY=127.0.0.1:15000 go test ./internal/registry -run TestLiveLocalRegistryProbe -v
func TestLiveLocalRegistryProbe(t *testing.T) {
	// not-a-failure-code
	endpoint := os.Getenv("MIDNIGHT_LIVE_REGISTRY") // not-a-failure-code
	if endpoint == "" {
		t.Skip("set MIDNIGHT_LIVE_REGISTRY to run")
	}
	repo := os.Getenv("MIDNIGHT_LIVE_REPO") // not-a-failure-code
	tag := os.Getenv("MIDNIGHT_LIVE_TAG")   // not-a-failure-code
	resolver := NewResolver(ResolverConfig{LocalEndpoint: endpoint, LocalInsecure: true, RequestTimeout: 3 * time.Second, TotalTimeout: 6 * time.Second})

	pinned, err := resolver.Resolve(context.Background(), endpoint+"/"+repo+":"+tag)
	if err != nil {
		t.Fatalf("tag resolve: %v", err)
	}
	t.Logf("TAG      %s:%s -> %s", repo, tag, pinned.Reference)

	same, err := resolver.Resolve(context.Background(), pinned.Reference)
	if err != nil {
		t.Fatalf("digest resolve: %v", err)
	}
	t.Logf("DIGEST   accepted unchanged: %s pinned=%v", same.Reference, same.Pinned)

	bogus := endpoint + "/" + repo + "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := resolver.Resolve(context.Background(), bogus); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("bogus digest: err = %v, want ErrManifestNotFound", err)
	} else {
		t.Logf("BOGUS    rejected: %v", err)
	}

	down := NewResolver(ResolverConfig{LocalEndpoint: "127.0.0.1:15999", LocalInsecure: true, RequestTimeout: 2 * time.Second, TotalTimeout: 3 * time.Second})
	start := time.Now()
	if _, err := down.Resolve(context.Background(), "127.0.0.1:15999/nope:1"); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("down registry: err = %v, want ErrRegistryUnavailable", err)
	} else {
		t.Logf("DOWN     rejected in %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
}

// MIDNIGHT_LIVE_HUB=1 go test ./internal/registry -run TestLiveDockerHubProbe -v
func TestLiveDockerHubProbe(t *testing.T) {
	// not-a-failure-code
	if os.Getenv("MIDNIGHT_LIVE_HUB") == "" {
		t.Skip("set MIDNIGHT_LIVE_HUB to run")
	}
	resolver := NewResolver(ResolverConfig{RequestTimeout: 8 * time.Second, TotalTimeout: 20 * time.Second})
	pinned, err := resolver.Resolve(context.Background(), "nginx:1.27")
	if err != nil {
		t.Fatalf("nginx:1.27: %v", err)
	}
	t.Logf("TAG   nginx:1.27 -> %s", pinned.Reference)

	official := "postgres:16.14@sha256:33f923b05f64ca54ac4401c01126a6b92afe839a0aa0a52bc5aeb5cc958e5f20"
	if _, err := resolver.Resolve(context.Background(), official); err != nil {
		t.Fatalf("official template digest: %v", err)
	}
	t.Logf("SHIPPED accepted: %s", official)

	bogus := "nginx:1.27@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := resolver.Resolve(context.Background(), bogus); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("bogus: err = %v, want ErrManifestNotFound", err)
	} else {
		t.Logf("BOGUS  rejected: %v", err)
	}
}
