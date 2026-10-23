// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// PushConfig describes how a local `distribution/distribution` registry is
// reached. BuildKit (invoked from internal/build) performs the actual push as
// part of its solve; this helper exists to (1) describe the wiring so the
// build worker can reason about the endpoint and (2) re-validate the digest
// against the registry's manifest API once BuildKit reports completion.
type PushConfig struct {
	// Endpoint is the host[:port] the registry listens on, e.g.
	// `registry.midnight.local:5000`. No scheme.
	Endpoint string
	// Insecure is true when the registry is served over plain HTTP. The
	// embedded Phase 1 registry ships without TLS for the localhost
	// loopback case; production endpoints must use HTTPS.
	Insecure bool
	// HTTPClient is the seam tests use to inject a fake transport. nil =
	// http.DefaultClient.
	HTTPClient *http.Client
}

// Validate rejects PushConfigs with missing required fields.
func (c PushConfig) Validate() error {
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("registry push: endpoint is required")
	}
	if strings.Contains(c.Endpoint, "://") {
		return errors.New("registry push: endpoint must be host[:port] without scheme")
	}
	return nil
}

// Scheme returns "http" or "https" based on the Insecure flag.
func (c PushConfig) Scheme() string {
	if c.Insecure {
		return "http"
	}
	return "https"
}

// BaseURL returns the URL prefix for `/v2/...` API calls.
func (c PushConfig) BaseURL() string {
	return c.Scheme() + "://" + strings.TrimSuffix(c.Endpoint, "/")
}

// QualifiedImageRef stitches the registry endpoint onto a bare
// repository[:tag] reference. Repositories that already include a host (e.g.
// supplied by an operator) are returned verbatim.
func (c PushConfig) QualifiedImageRef(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if firstSlash := strings.Index(image, "/"); firstSlash >= 0 {
		host := image[:firstSlash]
		if strings.Contains(host, ".") || strings.Contains(host, ":") || host == "localhost" {
			return image
		}
	}
	return strings.TrimSuffix(c.Endpoint, "/") + "/" + image
}

// HeadManifest issues HEAD /v2/<repo>/manifests/<reference> and returns the
// Docker-Content-Digest header. This is how callers confirm the registry
// actually stored the manifest BuildKit pushed. The header value is the
// canonical `sha256:<hex>` digest a deploy intake call expects.
func (c PushConfig) HeadManifest(ctx context.Context, repository, reference string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(repository) == "" {
		return "", errors.New("registry push: repository is required")
	}
	if strings.TrimSpace(reference) == "" {
		return "", errors.New("registry push: reference is required")
	}
	url := fmt.Sprintf("%s/v2/%s/manifests/%s", c.BaseURL(), repository, reference)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registry head manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("registry head manifest: not found")
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("registry head manifest: status %d", resp.StatusCode)
	}
	digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return "", errors.New("registry head manifest: missing Docker-Content-Digest header")
	}
	if !sha256DigestPattern.MatchString(digest) {
		return "", fmt.Errorf("registry head manifest: digest %q is not sha256", digest)
	}
	return digest, nil
}

// Catalog returns the registry's `/v2/_catalog` payload. The endpoint is
// optional in the distribution config; if it returns 404 the caller treats it
// as an empty catalog.
type Catalog struct {
	Repositories []string `json:"repositories"`
}

// Catalog fetches the registry catalog.
func (c PushConfig) Catalog(ctx context.Context) (Catalog, error) {
	if err := c.Validate(); err != nil {
		return Catalog{}, err
	}
	url := c.BaseURL() + "/v2/_catalog"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Catalog{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Catalog{}, fmt.Errorf("registry catalog: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return Catalog{}, nil
	}
	if resp.StatusCode >= 400 {
		return Catalog{}, fmt.Errorf("registry catalog: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return Catalog{}, fmt.Errorf("registry catalog: parse: %w", err)
	}
	return catalog, nil
}
