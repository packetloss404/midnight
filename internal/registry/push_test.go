// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushConfigValidate(t *testing.T) {
	if err := (PushConfig{}).Validate(); err == nil {
		t.Fatal("validate should fail on empty endpoint")
	}
	if err := (PushConfig{Endpoint: "https://example.test"}).Validate(); err == nil {
		t.Fatal("validate should fail when endpoint has scheme")
	}
	if err := (PushConfig{Endpoint: "registry.midnight.local:5000"}).Validate(); err != nil {
		t.Fatalf("validate err = %v", err)
	}
}

func TestPushConfigScheme(t *testing.T) {
	if (PushConfig{Endpoint: "x"}).Scheme() != "https" {
		t.Fatal("default scheme should be https")
	}
	if (PushConfig{Endpoint: "x", Insecure: true}).Scheme() != "http" {
		t.Fatal("insecure scheme should be http")
	}
}

func TestPushConfigQualifiedImageRef(t *testing.T) {
	cfg := PushConfig{Endpoint: "registry.midnight.local:5000"}
	if got := cfg.QualifiedImageRef("midnight/web:rev_abc"); got != "registry.midnight.local:5000/midnight/web:rev_abc" {
		t.Fatalf("QualifiedImageRef = %q", got)
	}
	if got := cfg.QualifiedImageRef("other.example/midnight/web:rev_abc"); got != "other.example/midnight/web:rev_abc" {
		t.Fatalf("QualifiedImageRef should respect existing host: %q", got)
	}
	if got := cfg.QualifiedImageRef("localhost:5000/midnight/web:rev_abc"); got != "localhost:5000/midnight/web:rev_abc" {
		t.Fatalf("QualifiedImageRef should respect localhost: %q", got)
	}
}

func TestHeadManifestReturnsDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		if r.URL.Path != "/v2/midnight/web/manifests/rev_abc" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := PushConfig{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true, HTTPClient: srv.Client()}
	got, err := cfg.HeadManifest(context.Background(), "midnight/web", "rev_abc")
	if err != nil {
		t.Fatalf("HeadManifest: %v", err)
	}
	if got != digest {
		t.Fatalf("digest = %q, want %q", got, digest)
	}
}

func TestHeadManifestRejectsMissingHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := PushConfig{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true, HTTPClient: srv.Client()}
	if _, err := cfg.HeadManifest(context.Background(), "midnight/web", "rev_abc"); err == nil {
		t.Fatal("HeadManifest should fail when Docker-Content-Digest is missing")
	}
}

func TestCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Catalog{Repositories: []string{"midnight/web"}})
	}))
	defer srv.Close()
	cfg := PushConfig{Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true, HTTPClient: srv.Client()}
	cat, err := cfg.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(cat.Repositories) != 1 || cat.Repositories[0] != "midnight/web" {
		t.Fatalf("catalog = %#v", cat)
	}
}
