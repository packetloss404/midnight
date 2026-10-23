package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// PermissionLevel mirrors the GitHub App manifest permission strings.
type PermissionLevel string

const (
	PermissionRead  PermissionLevel = "read"
	PermissionWrite PermissionLevel = "write"
)

// Manifest is the JSON shape of the GitHub App manifest registration
// payload. Lane 4 ships a strict-typed shape; the installer wraps it
// with the operator-supplied callback URL and posts it to GitHub.
//
// Panel decision pinning (Sprint 10 GH-1005):
//
//	"Minimum initial permissions: repository contents read, metadata
//	 read, commit statuses or checks write, deployments write if using
//	 deployments, pull requests read for previews, and webhooks for
//	 push/pull request/installation events.
//
//	 Do not request administration, workflows, secrets, or broad
//	 organization permissions in MVP."
type Manifest struct {
	Name                  string                     `json:"name"`
	URL                   string                     `json:"url"`
	HookAttributes        ManifestHookAttributes     `json:"hook_attributes"`
	RedirectURL           string                     `json:"redirect_url"`
	SetupURL              string                     `json:"setup_url"`
	CallbackURLs          []string                   `json:"callback_urls,omitempty"`
	RequestOAuthOnInstall bool                       `json:"request_oauth_on_install"`
	Public                bool                       `json:"public"`
	DefaultEvents         []string                   `json:"default_events"`
	DefaultPerms          map[string]PermissionLevel `json:"default_permissions"`
	Description           string                     `json:"description,omitempty"`
}

// ManifestHookAttributes is the panel-locked webhook delivery
// configuration.
type ManifestHookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// BannedPermissions lists permissions the panel forbids in MVP.
var BannedPermissions = map[string]struct{}{
	"administration":                   {},
	"workflows":                        {},
	"secrets":                          {},
	"organization_secrets":             {},
	"organization_administration":      {},
	"organization_packages":            {},
	"organization_projects":            {},
	"organization_self_hosted_runners": {},
	"organization_user_blocking":       {},
}

// RequiredEvents is the panel-locked subscribed-event set
// (Sprint 10 GH-1005 acceptance criterion 3).
var RequiredEvents = []string{"push", "pull_request", "installation", "installation_repositories"}

// DefaultManifest returns a Manifest populated with the panel-locked
// minimum permission set + the panel-locked event subscription set.
// Callers MUST set Name, URL, RedirectURL, SetupURL, CallbackURLs, and
// HookAttributes.URL to deployment-specific values before posting.
func DefaultManifest() Manifest {
	return Manifest{
		HookAttributes:        ManifestHookAttributes{Active: true},
		Public:                false,
		RequestOAuthOnInstall: true,
		DefaultEvents:         append([]string(nil), RequiredEvents...),
		DefaultPerms: map[string]PermissionLevel{
			// Minimum permissions per the panel decision quoted above.
			"contents":      PermissionRead,
			"metadata":      PermissionRead,
			"statuses":      PermissionWrite,
			"checks":        PermissionWrite,
			"deployments":   PermissionWrite,
			"pull_requests": PermissionWrite,
		},
	}
}

// ErrManifestInvalid is returned when ValidateManifest rejects a
// manifest because it requests a banned permission, omits a required
// event, or otherwise violates the panel-locked policy.
var ErrManifestInvalid = errors.New("github: manifest invalid")

// ValidateManifest enforces the panel-locked policy:
//
//   - No banned permissions.
//   - All RequiredEvents subscribed.
//   - Permission level for "contents" is read; "statuses"/"checks" must
//     be write; "deployments" must be write if present.
//
// The function returns nil on success.
func ValidateManifest(m Manifest) error {
	for perm := range m.DefaultPerms {
		if _, banned := BannedPermissions[perm]; banned {
			return fmt.Errorf("%w: banned permission %q (panel: do not request administration/workflows/secrets/broad org)", ErrManifestInvalid, perm)
		}
	}
	if level := m.DefaultPerms["contents"]; level != PermissionRead {
		return fmt.Errorf("%w: contents permission must be read, got %q", ErrManifestInvalid, level)
	}
	if level := m.DefaultPerms["metadata"]; level != PermissionRead {
		return fmt.Errorf("%w: metadata permission must be read, got %q", ErrManifestInvalid, level)
	}
	if level := m.DefaultPerms["statuses"]; level != "" && level != PermissionWrite {
		return fmt.Errorf("%w: statuses permission must be write, got %q", ErrManifestInvalid, level)
	}
	if level := m.DefaultPerms["checks"]; level != "" && level != PermissionWrite {
		return fmt.Errorf("%w: checks permission must be write, got %q", ErrManifestInvalid, level)
	}
	if level := m.DefaultPerms["deployments"]; level != "" && level != PermissionWrite {
		return fmt.Errorf("%w: deployments permission must be write, got %q", ErrManifestInvalid, level)
	}
	got := map[string]bool{}
	for _, e := range m.DefaultEvents {
		got[e] = true
	}
	for _, want := range RequiredEvents {
		if !got[want] {
			return fmt.Errorf("%w: required event %q not subscribed", ErrManifestInvalid, want)
		}
	}
	if !m.RequestOAuthOnInstall {
		return fmt.Errorf("%w: request_oauth_on_install must be enabled", ErrManifestInvalid)
	}
	setupURL, err := validatedHTTPSURL(m.SetupURL)
	if err != nil || setupURL.Path != "/api/v1alpha/github/setup/callback" || setupURL.RawQuery != "" {
		return fmt.Errorf("%w: setup_url must target the HTTPS /api/v1alpha/github/setup/callback endpoint", ErrManifestInvalid)
	}
	callbackMatch := false
	for _, callbackURL := range m.CallbackURLs {
		callback, callbackErr := validatedHTTPSURL(callbackURL)
		if callbackErr != nil || callback.RawQuery != "" {
			return fmt.Errorf("%w: callback_urls must contain only absolute HTTPS URLs without query", ErrManifestInvalid)
		}
		if callback.String() == setupURL.String() {
			callbackMatch = true
		}
	}
	if !callbackMatch {
		return fmt.Errorf("%w: callback_urls must include setup_url", ErrManifestInvalid)
	}
	return nil
}

// RenderManifest returns the JSON bytes the installer posts to GitHub.
// Map keys are sorted lexicographically for deterministic output (handy
// for golden tests and ADR fixtures).
func RenderManifest(m Manifest) ([]byte, error) {
	if err := ValidateManifest(m); err != nil {
		return nil, err
	}
	// Build a deterministic ordering by re-marshalling the permission
	// map through a sorted intermediate.
	type out struct {
		Name                  string                 `json:"name"`
		URL                   string                 `json:"url"`
		HookAttributes        ManifestHookAttributes `json:"hook_attributes"`
		RedirectURL           string                 `json:"redirect_url"`
		SetupURL              string                 `json:"setup_url"`
		CallbackURLs          []string               `json:"callback_urls,omitempty"`
		RequestOAuthOnInstall bool                   `json:"request_oauth_on_install"`
		Public                bool                   `json:"public"`
		DefaultEvents         []string               `json:"default_events"`
		DefaultPerms          map[string]string      `json:"default_permissions"`
		Description           string                 `json:"description,omitempty"`
	}
	perms := make(map[string]string, len(m.DefaultPerms))
	for k, v := range m.DefaultPerms {
		perms[k] = string(v)
	}
	keys := make([]string, 0, len(perms))
	for k := range perms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	events := append([]string(nil), m.DefaultEvents...)
	sort.Strings(events)
	o := out{
		Name:                  m.Name,
		URL:                   m.URL,
		HookAttributes:        m.HookAttributes,
		RedirectURL:           m.RedirectURL,
		SetupURL:              m.SetupURL,
		CallbackURLs:          m.CallbackURLs,
		RequestOAuthOnInstall: m.RequestOAuthOnInstall,
		Public:                m.Public,
		DefaultEvents:         events,
		DefaultPerms:          perms,
		Description:           m.Description,
	}
	if strings.TrimSpace(o.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrManifestInvalid)
	}
	if strings.TrimSpace(o.URL) == "" {
		return nil, fmt.Errorf("%w: url is required", ErrManifestInvalid)
	}
	return json.MarshalIndent(o, "", "  ")
}
