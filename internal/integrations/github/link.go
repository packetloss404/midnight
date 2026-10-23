// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package github

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ServiceLink is the panel-locked shape Midnight persists under
// `service.spec.github`. Lane 4 keeps it self-contained so the API
// layer doesn't depend on a separate "github_links" table until link
// volume justifies one (see ADR 0022).
//
// Wire format mirrors the JSON written into Postgres.
type ServiceLink struct {
	// InstallationID is the GitHub App installation backing this link.
	// The build worker uses it to mint a short-lived clone token.
	InstallationID int64 `json:"installation_id"`
	// RepositoryID is GitHub's immutable repository identity. Canonical
	// tenant links require it so webhook routing survives display-name case
	// differences and cannot cross installations that expose the same name.
	RepositoryID int64 `json:"repository_id,omitempty"`
	// Repo is the canonical "owner/name" identifier from the push
	// webhook's repository.full_name field.
	Repo string `json:"repo"`
	// Branch is the GitHub ref minus refs/heads/ that this service
	// auto-deploys on (e.g. "main").
	Branch string `json:"branch"`
	// WatchPaths optionally filters the link so a push only triggers a
	// deploy when at least one commit touches a file under one of these
	// roots. Empty list = no filter.
	WatchPaths []string `json:"watch_paths,omitempty"`
	// RootDirectory is the monorepo directory used as the build context. It is
	// independent from WatchPaths: a changed file may trigger a build without
	// changing the directory the builder receives.
	RootDirectory string `json:"root_directory,omitempty"`
	// ConfigPath is the optional repository-relative Midnight/Railway config
	// file path. It is retained as source metadata and must not be conflated
	// with either RootDirectory or WatchPaths.
	ConfigPath string `json:"config_path,omitempty"`
	// CloneURL is the canonical https clone URL recorded at link time
	// (so subsequent pushes don't have to re-derive it from the body
	// when the operator changed the repo's visibility).
	CloneURL string `json:"clone_url,omitempty"`
	// WaitForCI gates the deploy on Checks API success when set.
	WaitForCI bool `json:"wait_for_ci,omitempty"`
	// RequiredChecks names checks that must succeed when WaitForCI is
	// set. An empty list means "any check_suite that runs must succeed".
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// ErrLinkInvalid is returned by Validate when ServiceLink is malformed.
var ErrLinkInvalid = errors.New("github: service link invalid")

// Validate enforces the panel-locked shape.
func (l ServiceLink) Validate() error {
	if l.InstallationID <= 0 {
		return fmt.Errorf("%w: installation_id required", ErrLinkInvalid)
	}
	repo := strings.TrimSpace(l.Repo)
	if repo == "" || !strings.Contains(repo, "/") {
		return fmt.Errorf("%w: repo must be \"owner/name\"", ErrLinkInvalid)
	}
	parts := strings.SplitN(repo, "/", 2)
	if parts[0] == "" || parts[1] == "" || strings.Contains(parts[1], "/") {
		return fmt.Errorf("%w: repo must be \"owner/name\"", ErrLinkInvalid)
	}
	branch := strings.TrimSpace(l.Branch)
	if branch == "" {
		return fmt.Errorf("%w: branch required", ErrLinkInvalid)
	}
	for _, wp := range l.WatchPaths {
		if strings.TrimSpace(wp) == "" {
			return fmt.Errorf("%w: watch_paths must not contain empty entries", ErrLinkInvalid)
		}
	}
	if !l.WaitForCI && len(l.RequiredChecks) != 0 {
		return fmt.Errorf("%w: required_checks require wait_for_ci", ErrLinkInvalid)
	}
	if err := validateRelativeSourcePath("root_directory", l.RootDirectory); err != nil {
		return err
	}
	if err := validateRelativeSourcePath("config_path", l.ConfigPath); err != nil {
		return err
	}
	if strings.TrimSpace(l.CloneURL) != "" {
		if err := validateCloneURL(l.CloneURL, repo); err != nil {
			return err
		}
	}
	return nil
}

// Normalize returns a copy of l with trimmed strings and canonical
// branch / paths.
func (l ServiceLink) Normalize() ServiceLink {
	out := l
	out.Repo = strings.TrimSpace(l.Repo)
	out.Branch = strings.TrimSpace(strings.TrimPrefix(l.Branch, "refs/heads/"))
	out.RootDirectory = normalizeRelativeSourcePath(l.RootDirectory)
	out.ConfigPath = normalizeRelativeSourcePath(l.ConfigPath)
	wp := make([]string, 0, len(l.WatchPaths))
	for _, p := range l.WatchPaths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			continue
		}
		wp = append(wp, p)
	}
	out.WatchPaths = wp
	checks := make([]string, 0, len(l.RequiredChecks))
	for _, c := range l.RequiredChecks {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		checks = append(checks, c)
	}
	out.RequiredChecks = checks
	return out
}

// Public is the redaction-safe view returned by GET endpoints. It
// intentionally omits any future-secret-bearing field.
type LinkPublic struct {
	InstallationID int64    `json:"installation_id"`
	RepositoryID   int64    `json:"repository_id,omitempty"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	WatchPaths     []string `json:"watch_paths,omitempty"`
	RootDirectory  string   `json:"root_directory,omitempty"`
	ConfigPath     string   `json:"config_path,omitempty"`
	WaitForCI      bool     `json:"wait_for_ci,omitempty"`
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// PublicView converts a ServiceLink to its redaction-safe shape.
func (l ServiceLink) PublicView() LinkPublic {
	return LinkPublic{
		InstallationID: l.InstallationID,
		RepositoryID:   l.RepositoryID,
		Repo:           l.Repo,
		Branch:         l.Branch,
		WatchPaths:     append([]string(nil), l.WatchPaths...),
		RootDirectory:  l.RootDirectory,
		ConfigPath:     l.ConfigPath,
		WaitForCI:      l.WaitForCI,
		RequiredChecks: append([]string(nil), l.RequiredChecks...),
	}
}

func validateRelativeSourcePath(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	cleaned := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(value, "/") || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: %s must be a relative path inside the repository", ErrLinkInvalid, field)
	}
	return nil
}

func normalizeRelativeSourcePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	cleaned := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func validateCloneURL(rawURL, repo string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" {
		return fmt.Errorf("%w: clone_url must be an HTTPS URL", ErrLinkInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: clone_url must not contain credentials, query, or fragment", ErrLinkInvalid)
	}
	gotPath := strings.TrimSuffix(strings.TrimRight(parsed.EscapedPath(), "/"), ".git")
	wantPath := "/" + strings.Trim(repo, "/")
	if !strings.EqualFold(gotPath, wantPath) {
		return fmt.Errorf("%w: clone_url repository path must match repo", ErrLinkInvalid)
	}
	return nil
}

// CloneURLForRepo returns the canonical https clone URL for a
// "owner/name" repo. Used when the operator-supplied link omits the
// optional clone_url override.
func CloneURLForRepo(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return ""
	}
	return "https://github.com/" + repo + ".git"
}

// AuthenticatedCloneURL returns the panel-locked
// https://x-access-token:{token}@github.com/{owner}/{repo}.git form
// the build worker passes to `git clone`. cloneURL may be empty; we
// derive the canonical github.com URL from repo in that case.
func AuthenticatedCloneURL(cloneURL, repo, token string) string {
	if strings.TrimSpace(cloneURL) == "" {
		cloneURL = CloneURLForRepo(repo)
	}
	if strings.TrimSpace(token) == "" {
		return cloneURL
	}
	const httpsPrefix = "https://"
	if !strings.HasPrefix(cloneURL, httpsPrefix) {
		return cloneURL
	}
	rest := strings.TrimPrefix(cloneURL, httpsPrefix)
	return httpsPrefix + "x-access-token:" + token + "@" + rest
}
