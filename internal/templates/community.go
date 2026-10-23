// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package templates

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
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/ssrf"
	"go.yaml.in/yaml/v3"
)

const (
	CommunityManifestPath    = "template.yaml"
	CommunitySchemaVersion   = 1
	maxCommunityManifestSize = 256 << 10
	maxCommunityReadmeSize   = 128 << 10
	maxCommunitySBOMSize     = 2 << 20
)

var (
	ErrCommunitySourceInvalid = errors.New("community template source is invalid")
	ErrCommunityFetchFailed   = errors.New("community template fetch failed")
	ErrCommunityManifest      = errors.New("community template manifest is invalid")
	ErrCommunityForkNotFound  = errors.New("community template fork not found")
	ErrCommunityForkConflict  = errors.New("community template fork version conflict")

	githubComponentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	commitPattern          = regexp.MustCompile(`^[0-9a-f]{40}$`)
	templateSlugPattern    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	envNamePattern         = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	sha256ImagePattern     = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
	spdxExpressionPattern  = regexp.MustCompile(`^[A-Za-z0-9.+() -]{1,128}$`)
	safeAssetPathPattern   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,240}$`)
)

// CommunityManifest is the versioned public template.yaml contract. The
// metadata is deliberately separate from Template: source trust is assigned
// by the server and can never be asserted by a repository payload.
type CommunityManifest struct {
	SchemaVersion int               `json:"schema_version" yaml:"schema_version"`
	Template      Template          `json:"template" yaml:"template"`
	Metadata      CommunityMetadata `json:"metadata" yaml:"metadata"`
}

type CommunityMetadata struct {
	Maintainer        string        `json:"maintainer" yaml:"maintainer"`
	License           string        `json:"license" yaml:"license"`
	SecurityReview    string        `json:"security_review" yaml:"security_review"`
	RiskClass         string        `json:"risk_class" yaml:"risk_class"`
	UpdatePolicy      string        `json:"update_policy" yaml:"update_policy"`
	DeprecationPolicy string        `json:"deprecation_policy" yaml:"deprecation_policy"`
	ReadmePath        string        `json:"readme_path" yaml:"readme_path"`
	SBOM              CommunitySBOM `json:"sbom" yaml:"sbom"`
}

type CommunitySBOM struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type ValidationFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type CommunityProvenance struct {
	Source         string `json:"source"`
	Ref            string `json:"ref"`
	ManifestPath   string `json:"manifest_path"`
	ManifestSHA256 string `json:"manifest_sha256"`
	ReadmePath     string `json:"readme_path"`
	ReadmeSHA256   string `json:"readme_sha256"`
	SBOMPath       string `json:"sbom_path"`
	SBOMSHA256     string `json:"sbom_sha256"`
}

type CommunityDiscovery struct {
	TrustClass string              `json:"trust_class"`
	Manifest   CommunityManifest   `json:"manifest"`
	Provenance CommunityProvenance `json:"provenance"`
	Readme     string              `json:"readme"`
	Findings   []ValidationFinding `json:"findings"`
	Valid      bool                `json:"valid"`
}

// CommunityFork is a project-owned editable copy. UpstreamManifest and
// Provenance are immutable evidence; Draft is the only editable document.
type CommunityFork struct {
	ID               string              `json:"id"`
	ProjectID        string              `json:"project_id"`
	TrustClass       string              `json:"trust_class"`
	UpstreamManifest CommunityManifest   `json:"upstream_manifest"`
	Draft            CommunityManifest   `json:"draft"`
	Provenance       CommunityProvenance `json:"provenance"`
	Readme           string              `json:"readme"`
	Findings         []ValidationFinding `json:"findings"`
	Valid            bool                `json:"valid"`
	ResourceVersion  int64               `json:"resource_version"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type CommunityForkStore interface {
	CreateFork(context.Context, CommunityFork) (CommunityFork, error)
	ListForks(context.Context, string) ([]CommunityFork, error)
	GetFork(context.Context, string, string) (CommunityFork, error)
	UpdateFork(context.Context, string, string, int64, CommunityManifest, []ValidationFinding) (CommunityFork, error)
}

type CommunityDiscoverer interface {
	Discover(context.Context, string, string) (CommunityDiscovery, error)
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GitHubCommunityDiscoverer reads immutable public GitHub content only. The
// request surface accepts no arbitrary fetch URL, branch, tag, or credential.
type GitHubCommunityDiscoverer struct {
	HTTP HTTPDoer
}

func NewGitHubCommunityDiscoverer() *GitHubCommunityDiscoverer {
	dialer := ssrf.Dialer{ExpectedHost: "raw.githubusercontent.com", DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.Dial
	return &GitHubCommunityDiscoverer{HTTP: &http.Client{
		Timeout:   20 * time.Second,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("community template fetch redirects are disabled")
		},
	}}
}

func (d *GitHubCommunityDiscoverer) Discover(ctx context.Context, source, ref string) (CommunityDiscovery, error) {
	canonical, owner, repository, err := canonicalGitHubSource(source)
	if err != nil {
		return CommunityDiscovery{}, err
	}
	ref = strings.ToLower(strings.TrimSpace(ref))
	if !commitPattern.MatchString(ref) {
		return CommunityDiscovery{}, fmt.Errorf("%w: ref must be an exact 40-character commit SHA", ErrCommunitySourceInvalid)
	}
	if d == nil || d.HTTP == nil {
		return CommunityDiscovery{}, fmt.Errorf("%w: HTTP client is unavailable", ErrCommunityFetchFailed)
	}

	manifestBytes, err := d.fetch(ctx, owner, repository, ref, CommunityManifestPath, maxCommunityManifestSize)
	if err != nil {
		return CommunityDiscovery{}, err
	}
	manifest, parseFindings := ParseCommunityManifest(manifestBytes)
	findings := append(parseFindings, ValidateCommunityManifest(manifest)...)
	if hasErrors(findings) {
		return CommunityDiscovery{
			TrustClass: "community", Manifest: manifest,
			Provenance: CommunityProvenance{Source: canonical, Ref: ref, ManifestPath: CommunityManifestPath, ManifestSHA256: digestHex(manifestBytes)},
			Findings:   normalizeFindings(findings), Valid: false,
		}, nil
	}

	readmeBytes, err := d.fetch(ctx, owner, repository, ref, manifest.Metadata.ReadmePath, maxCommunityReadmeSize)
	if err != nil {
		return CommunityDiscovery{}, fmt.Errorf("%w: readme: %v", ErrCommunityFetchFailed, err)
	}
	sbomBytes, err := d.fetch(ctx, owner, repository, ref, manifest.Metadata.SBOM.Path, maxCommunitySBOMSize)
	if err != nil {
		return CommunityDiscovery{}, fmt.Errorf("%w: sbom: %v", ErrCommunityFetchFailed, err)
	}
	actualSBOM := digestHex(sbomBytes)
	if actualSBOM != manifest.Metadata.SBOM.SHA256 {
		findings = append(findings, ValidationFinding{
			Severity: "error", Code: "sbom_digest_mismatch", Path: "metadata.sbom.sha256",
			Message: "SBOM content does not match the declared SHA-256 digest",
		})
	}
	findings = normalizeFindings(findings)
	return CommunityDiscovery{
		TrustClass: "community",
		Manifest:   manifest,
		Provenance: CommunityProvenance{
			Source: canonical, Ref: ref, ManifestPath: CommunityManifestPath,
			ManifestSHA256: digestHex(manifestBytes), ReadmePath: manifest.Metadata.ReadmePath,
			ReadmeSHA256: digestHex(readmeBytes), SBOMPath: manifest.Metadata.SBOM.Path, SBOMSHA256: actualSBOM,
		},
		Readme: string(readmeBytes), Findings: findings, Valid: !hasErrors(findings),
	}, nil
}

func (d *GitHubCommunityDiscoverer) fetch(ctx context.Context, owner, repository, ref, assetPath string, limit int64) ([]byte, error) {
	assetPath, err := validateAssetPath(assetPath)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(assetPath, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	rawURL := "https://raw.githubusercontent.com/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/" + ref + "/" + strings.Join(parts, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: request", ErrCommunityFetchFailed)
	}
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", ErrCommunityFetchFailed)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: upstream returned HTTP %d", ErrCommunityFetchFailed, resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read response", ErrCommunityFetchFailed)
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("%w: asset exceeds %d bytes", ErrCommunityFetchFailed, limit)
	}
	return content, nil
}

func canonicalGitHubSource(source string) (string, string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", fmt.Errorf("%w: source must be https://github.com/{owner}/{repository}", ErrCommunitySourceInvalid)
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 2 {
		return "", "", "", fmt.Errorf("%w: source must identify one GitHub repository", ErrCommunitySourceInvalid)
	}
	owner, err := url.PathUnescape(segments[0])
	if err != nil {
		return "", "", "", fmt.Errorf("%w: invalid owner", ErrCommunitySourceInvalid)
	}
	repository, err := url.PathUnescape(segments[1])
	if err != nil {
		return "", "", "", fmt.Errorf("%w: invalid repository", ErrCommunitySourceInvalid)
	}
	repository = strings.TrimSuffix(repository, ".git")
	if !githubComponentPattern.MatchString(owner) || !githubComponentPattern.MatchString(repository) || owner == "." || owner == ".." || repository == "." || repository == ".." {
		return "", "", "", fmt.Errorf("%w: invalid GitHub repository", ErrCommunitySourceInvalid)
	}
	canonical := "https://github.com/" + owner + "/" + repository
	return canonical, owner, repository, nil
}

func validateAssetPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !safeAssetPathPattern.MatchString(value) || strings.HasPrefix(value, "/") || strings.Contains(value, "//") || strings.Contains(value, "\\") || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("%w: repository asset path is unsafe", ErrCommunityManifest)
	}
	return value, nil
}

func ParseCommunityManifest(content []byte) (CommunityManifest, []ValidationFinding) {
	if len(content) == 0 || len(content) > maxCommunityManifestSize {
		return CommunityManifest{}, []ValidationFinding{{Severity: "error", Code: "manifest_size", Path: "$", Message: "template.yaml must be between 1 byte and 256 KiB"}}
	}
	var manifest CommunityManifest
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return CommunityManifest{}, []ValidationFinding{{Severity: "error", Code: "manifest_parse", Path: "$", Message: "template.yaml must be strict YAML using only documented fields"}}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return CommunityManifest{}, []ValidationFinding{{Severity: "error", Code: "manifest_documents", Path: "$", Message: "template.yaml must contain exactly one YAML document"}}
	}
	return manifest, nil
}

// DecodeCommunityDraft accepts the dashboard's JSON editor without permitting
// unknown fields or multiple values.
func DecodeCommunityDraft(content []byte) (CommunityManifest, error) {
	var manifest CommunityManifest
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return CommunityManifest{}, fmt.Errorf("%w: draft must be strict JSON: %v", ErrCommunityManifest, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return CommunityManifest{}, fmt.Errorf("%w: draft must contain exactly one JSON value", ErrCommunityManifest)
	}
	return manifest, nil
}

func ValidateCommunityManifest(manifest CommunityManifest) []ValidationFinding {
	findings := make([]ValidationFinding, 0)
	add := func(code, field, message string) {
		findings = append(findings, ValidationFinding{Severity: "error", Code: code, Path: field, Message: message})
	}
	if manifest.SchemaVersion != CommunitySchemaVersion {
		add("schema_version", "schema_version", "schema_version must be 1")
	}
	t := manifest.Template
	if !templateSlugPattern.MatchString(t.Slug) {
		add("slug", "template.slug", "slug must be a lowercase DNS-style label")
	}
	if _, err := Lookup(t.Slug); err == nil {
		add("official_collision", "template.slug", "community templates cannot reuse a built-in template slug")
	}
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 100 {
		add("name", "template.name", "name is required and must not exceed 100 characters")
	}
	if strings.TrimSpace(t.Version) == "" || len(t.Version) > 64 {
		add("version", "template.version", "version is required and must not exceed 64 characters")
	}
	if strings.TrimSpace(t.Summary) == "" || len(t.Summary) > 500 {
		add("summary", "template.summary", "summary is required and must not exceed 500 characters")
	}
	if len(t.ConsumerDocs) > 2000 {
		add("consumer_docs", "template.consumer_docs", "consumer_docs must not exceed 2,000 characters")
	}
	if len(t.Tags) > 12 {
		add("tags", "template.tags", "at most 12 tags are allowed")
	}
	seenTags := map[string]bool{}
	for i, tag := range t.Tags {
		tag = strings.TrimSpace(tag)
		if !templateSlugPattern.MatchString(tag) || seenTags[tag] {
			add("tag", fmt.Sprintf("template.tags[%d]", i), "tags must be unique lowercase labels")
		}
		if tag == "official" {
			add("trust_impersonation", fmt.Sprintf("template.tags[%d]", i), "community templates cannot assert official trust")
		}
		seenTags[tag] = true
	}
	if len(t.Services) == 0 || len(t.Services) > 12 {
		add("services", "template.services", "a template must declare between 1 and 12 services")
	}
	seenServices := map[string]bool{}
	for i, service := range t.Services {
		base := fmt.Sprintf("template.services[%d]", i)
		if !templateSlugPattern.MatchString(service.Slug) || seenServices[service.Slug] {
			add("service_slug", base+".slug", "service slugs must be unique lowercase DNS-style labels")
		}
		seenServices[service.Slug] = true
		if !sha256ImagePattern.MatchString(service.Image) {
			add("image_digest", base+".image", "images must be pinned to a lowercase sha256 digest")
		}
		if len(service.Args) > 64 {
			add("args", base+".args", "at most 64 arguments are allowed")
		}
		for j, arg := range service.Args {
			if len(arg) > 1024 || strings.ContainsRune(arg, '\x00') {
				add("arg", fmt.Sprintf("%s.args[%d]", base, j), "arguments must be NUL-free and at most 1,024 characters")
			}
		}
		if len(service.Env) > 128 {
			add("env_count", base+".env", "at most 128 environment variables are allowed")
		}
		for name, env := range service.Env {
			field := base + ".env." + name
			if !envNamePattern.MatchString(name) {
				add("env_name", field, "environment variable names must use uppercase shell identifier syntax")
			}
			switch env.Kind {
			case EnvKindLiteral:
				if env.Secret {
					add("literal_secret", field, "secret values cannot be embedded as literals; use a generated value")
				}
				if len(env.Value) > 4096 || env.GeneratedKind != "" || env.Length != 0 || env.Template != "" {
					add("literal_shape", field, "literal values must use only value and optional secret=false")
				}
			case EnvKindGenerated:
				if env.Value != "" || env.Template != "" {
					add("generated_shape", field, "generated values cannot contain value or template")
				}
				switch env.GeneratedKind {
				case GeneratedKindPassword:
					if !env.Secret || env.Length < 16 || env.Length > 128 {
						add("generated_password", field, "generated passwords must be secret and 16-128 characters")
					}
				case GeneratedKindUUID:
					if env.Length != 0 {
						add("generated_uuid", field, "generated UUIDs do not accept a length")
					}
				default:
					add("generated_kind", field, "generated_kind must be password or uuid")
				}
			case EnvKindComputedURL:
				if env.Value != "" || env.GeneratedKind != "" || env.Length != 0 || strings.TrimSpace(env.Template) == "" || len(env.Template) > 4096 {
					add("computed_shape", field, "computed_url values require only a non-empty template")
				}
			default:
				add("env_kind", field, "kind must be literal, generated, or computed_url")
			}
		}
		if err := validateServiceEnv(service); err != nil {
			add("env_reference", base+".env", "computed_url templates may reference only declared variables")
		}
		if volume := service.Volume; volume != nil {
			if !templateSlugPattern.MatchString(volume.Name) {
				add("volume_name", base+".volume.name", "volume name must be a lowercase label")
			}
			cleaned := path.Clean(volume.MountPath)
			if !strings.HasPrefix(volume.MountPath, "/") || cleaned != volume.MountPath || cleaned == "/" {
				add("volume_mount", base+".volume.mount_path", "mount_path must be a clean absolute path below root")
			}
			if volume.SizeBytes < 64<<20 || volume.SizeBytes > 10<<40 {
				add("volume_size", base+".volume.size_bytes", "volume size must be between 64 MiB and 10 TiB")
			}
		}
		if service.Healthcheck != nil && (strings.TrimSpace(service.Healthcheck.Cmd) == "" || len(service.Healthcheck.Cmd) > 2048 || strings.ContainsRune(service.Healthcheck.Cmd, '\x00')) {
			add("healthcheck", base+".healthcheck.cmd", "healthcheck command must be NUL-free and at most 2,048 characters")
		}
		if len(service.Ports) > 16 {
			add("ports", base+".ports", "at most 16 ports are allowed")
		}
		seenPorts := map[string]bool{}
		for j, portSpec := range service.Ports {
			protocol := strings.ToLower(strings.TrimSpace(portSpec.Protocol))
			if protocol == "" {
				protocol = "tcp"
			}
			key := fmt.Sprintf("%d/%s", portSpec.Port, protocol)
			if portSpec.Port < 1 || portSpec.Port > 65535 || (protocol != "tcp" && protocol != "udp") || seenPorts[key] {
				add("port", fmt.Sprintf("%s.ports[%d]", base, j), "ports must be unique valid TCP or UDP ports")
			}
			seenPorts[key] = true
		}
	}

	metadata := manifest.Metadata
	if strings.TrimSpace(metadata.Maintainer) == "" || len(metadata.Maintainer) > 200 {
		add("maintainer", "metadata.maintainer", "maintainer is required and must not exceed 200 characters")
	}
	if !spdxExpressionPattern.MatchString(strings.TrimSpace(metadata.License)) {
		add("license", "metadata.license", "license must be a non-empty SPDX expression")
	}
	if metadata.SecurityReview != "unreviewed" && metadata.SecurityReview != "reviewed" {
		add("security_review", "metadata.security_review", "security_review must be reviewed or unreviewed")
	}
	if metadata.RiskClass != "low" && metadata.RiskClass != "moderate" && metadata.RiskClass != "high" {
		add("risk_class", "metadata.risk_class", "risk_class must be low, moderate, or high")
	}
	if strings.TrimSpace(metadata.UpdatePolicy) == "" || len(metadata.UpdatePolicy) > 500 {
		add("update_policy", "metadata.update_policy", "update_policy is required and must not exceed 500 characters")
	}
	if strings.TrimSpace(metadata.DeprecationPolicy) == "" || len(metadata.DeprecationPolicy) > 500 {
		add("deprecation_policy", "metadata.deprecation_policy", "deprecation_policy is required and must not exceed 500 characters")
	}
	if _, err := validateAssetPath(metadata.ReadmePath); err != nil {
		add("readme_path", "metadata.readme_path", "readme_path must be a safe repository-relative path")
	}
	if _, err := validateAssetPath(metadata.SBOM.Path); err != nil {
		add("sbom_path", "metadata.sbom.path", "SBOM path must be a safe repository-relative path")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(metadata.SBOM.SHA256) {
		add("sbom_sha256", "metadata.sbom.sha256", "SBOM sha256 must be a lowercase 64-character digest")
	}
	return normalizeFindings(findings)
}

func normalizeFindings(findings []ValidationFinding) []ValidationFinding {
	if findings == nil {
		return []ValidationFinding{}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Path != findings[j].Path {
			return findings[i].Path < findings[j].Path
		}
		return findings[i].Code < findings[j].Code
	})
	return findings
}

func hasErrors(findings []ValidationFinding) bool {
	for _, finding := range findings {
		if finding.Severity == "error" {
			return true
		}
	}
	return false
}

func digestHex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
