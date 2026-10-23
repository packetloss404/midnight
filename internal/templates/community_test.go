// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type communityDoer func(*http.Request) (*http.Response, error)

func (f communityDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }

func TestGitHubCommunityDiscovererBuildsImmutableProvenance(t *testing.T) {
	const ref = "0123456789abcdef0123456789abcdef01234567"
	sbom := []byte(`{"bomFormat":"CycloneDX"}`)
	manifest := validCommunityYAML(digestHex(sbom))
	assets := map[string][]byte{
		"/acme/queue/" + ref + "/template.yaml": manifest,
		"/acme/queue/" + ref + "/README.md":     []byte("# Queue\nSafe local defaults."),
		"/acme/queue/" + ref + "/sbom.json":     sbom,
	}
	discoverer := &GitHubCommunityDiscoverer{HTTP: communityDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "raw.githubusercontent.com" || request.URL.Scheme != "https" {
			t.Fatalf("unexpected target = %s", request.URL)
		}
		body, ok := assets[request.URL.Path]
		if !ok {
			t.Fatalf("unexpected asset = %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}

	discovery, err := discoverer.Discover(context.Background(), "https://github.com/acme/queue.git/", strings.ToUpper(ref))
	if err != nil {
		t.Fatal(err)
	}
	if !discovery.Valid || discovery.TrustClass != "community" || len(discovery.Findings) != 0 {
		t.Fatalf("discovery = %+v", discovery)
	}
	if discovery.Provenance.Source != "https://github.com/acme/queue" || discovery.Provenance.Ref != ref {
		t.Fatalf("provenance = %+v", discovery.Provenance)
	}
	if discovery.Provenance.ManifestSHA256 != digestHex(manifest) || discovery.Provenance.SBOMSHA256 != digestHex(sbom) {
		t.Fatalf("provenance digests = %+v", discovery.Provenance)
	}
	if !strings.Contains(discovery.Readme, "Safe local defaults") {
		t.Fatalf("readme = %q", discovery.Readme)
	}
}

func TestGitHubCommunityDiscovererRejectsMutableOrArbitrarySources(t *testing.T) {
	discoverer := &GitHubCommunityDiscoverer{HTTP: communityDoer(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsafe input must fail before HTTP")
		return nil, nil
	})}
	for _, test := range []struct {
		source string
		ref    string
	}{
		{"http://github.com/acme/queue", strings.Repeat("a", 40)},
		{"https://example.com/acme/queue", strings.Repeat("a", 40)},
		{"https://github.com/acme/queue?token=secret", strings.Repeat("a", 40)},
		{"https://github.com/acme/queue", "main"},
		{"https://github.com/acme/queue/extra", strings.Repeat("a", 40)},
	} {
		if _, err := discoverer.Discover(context.Background(), test.source, test.ref); !errors.Is(err, ErrCommunitySourceInvalid) {
			t.Errorf("Discover(%q, %q) error = %v", test.source, test.ref, err)
		}
	}
}

func TestGitHubCommunityDiscovererReportsSBOMDigestMismatch(t *testing.T) {
	const ref = "0123456789abcdef0123456789abcdef01234567"
	manifest := validCommunityYAML(strings.Repeat("0", 64))
	discoverer := &GitHubCommunityDiscoverer{HTTP: communityDoer(func(request *http.Request) (*http.Response, error) {
		content := "documentation"
		if strings.HasSuffix(request.URL.Path, CommunityManifestPath) {
			content = string(manifest)
		} else if strings.HasSuffix(request.URL.Path, "sbom.json") {
			content = `{}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(content)), Header: make(http.Header)}, nil
	})}
	discovery, err := discoverer.Discover(context.Background(), "https://github.com/acme/queue", ref)
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Valid || len(discovery.Findings) != 1 || discovery.Findings[0].Code != "sbom_digest_mismatch" {
		t.Fatalf("discovery = %+v", discovery)
	}
}

func TestValidateCommunityManifestBlocksTrustAndSecretImpersonation(t *testing.T) {
	manifest, parseFindings := ParseCommunityManifest(validCommunityYAML(strings.Repeat("a", 64)))
	if len(parseFindings) != 0 {
		t.Fatal(parseFindings)
	}
	manifest.Template.Slug = "postgres"
	manifest.Template.Tags = append(manifest.Template.Tags, "official")
	manifest.Template.Services[0].Image = "acme/queue:latest"
	manifest.Template.Services[0].Env["TOKEN"] = TemplateEnv{Kind: EnvKindLiteral, Value: "embedded", Secret: true}
	findings := ValidateCommunityManifest(manifest)
	for _, code := range []string{"official_collision", "trust_impersonation", "image_digest", "literal_secret"} {
		if !hasFinding(findings, code) {
			t.Errorf("missing %s in %+v", code, findings)
		}
	}
}

func TestParseCommunityManifestRejectsUnknownFieldsAndExtraDocuments(t *testing.T) {
	for _, content := range [][]byte{
		[]byte("schema_version: 1\nunknown: true\n"),
		append(validCommunityYAML(strings.Repeat("a", 64)), []byte("\n---\n{}\n")...),
	} {
		_, findings := ParseCommunityManifest(content)
		if len(findings) != 1 || findings[0].Severity != "error" {
			t.Fatalf("findings = %+v", findings)
		}
	}
}

func validCommunityYAML(sbomDigest string) []byte {
	return []byte(`schema_version: 1
template:
  slug: queue-worker
  name: Queue worker
  version: 1.2.3
  summary: A pinned queue worker.
  tags: [worker]
  services:
    - slug: worker
      image: ghcr.io/acme/worker@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      env:
        LOG_LEVEL:
          kind: literal
          value: info
        TOKEN:
          kind: generated
          generated_kind: password
          length: 32
          secret: true
      ports:
        - port: 8080
          protocol: tcp
metadata:
  maintainer: Acme Platform <platform@example.com>
  license: Apache-2.0
  security_review: unreviewed
  risk_class: moderate
  update_policy: Pinned releases are reviewed monthly.
  deprecation_policy: Six months notice in the README.
  readme_path: README.md
  sbom:
    path: sbom.json
    sha256: ` + sbomDigest + "\n")
}

func hasFinding(findings []ValidationFinding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
